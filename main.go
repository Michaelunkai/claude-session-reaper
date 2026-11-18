// claude-session-reaper: deletes every NON-running Claude session (desktop Code
// sessions + their CLI transcripts and side data, Cowork sessions) and reports
// reclaimed space. Fails closed: anything that looks running, is unreadable, or
// cannot be proven idle is kept.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

type session struct {
	file, id, cli string
	cliIDs        []string
	archived      bool
	title         string
	activity      time.Time
	keep          string // reason kept; empty = deletable
	bytes         int64
	paths         []string
}

var (
	appdata   = flag.String("appdata", filepath.Join(os.Getenv("APPDATA"), "Claude"), "Claude desktop data dir")
	claudeDir = flag.String("claudehome", filepath.Join(os.Getenv("USERPROFILE"), ".claude"), "~/.claude dir")
	idleMin   = flag.Int("min-idle-min", 2, "any session touched within N minutes counts as running")
	delAfterH = flag.Int("delete-after-hours", 24, "idle sessions newer than this are archived, older are deleted")
	keepIDs   =flag.String("keep", "", "comma list of session ids (local_... or cli uuid) to never touch")
	dryRun    = flag.Bool("dry-run", false, "report only, delete nothing")
	noOrphans = flag.Bool("no-orphans", false, "keep CLI transcripts that have no desktop session")
	noProcs   = flag.Bool("skip-process-scan", false, "TEST ONLY: skip live process scan")
	verbose   = flag.Bool("v", false, "list every kept session")
)

var flagIDRe = regexp.MustCompile(`(?i)--(?:resume|session-id)[ =]"?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

var uuidRe =regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

//go:embed ui.ps1
var uiScript string

var (
	noUI      = flag.Bool("no-ui", false, "skip the live Claude-app pass (archive/delete in the open app)")
	noChats   = flag.Bool("no-chats", false, "live pass: do not delete chats")
	keepChats = flag.Int("keep-chats", 0, "live pass: keep the newest N chats")
	watchSec  = flag.Int("watch", 0, "repeat everything every N seconds (0 = run once)")
)

// runUI drives the OPEN Claude desktop app (UI Automation, hidden PowerShell child): unpin+archive idle
// sessions and delete chats. Takes effect live, no restart.
func runUI() {
	f, err := os.CreateTemp("", "reaper-ui-*.ps1")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ui: temp file:", err)
		return
	}
	defer os.Remove(f.Name())
	f.WriteString(uiScript)
	f.Close()
	args := []string{"-NoProfile", "-STA", "-ExecutionPolicy", "Bypass", "-File", f.Name()}
	if *dryRun {
		args = append(args, "-DryRun")
	}
	if *noChats {
		args = append(args, "-NoChats")
	}
	if *keepChats > 0 {
		args = append(args, "-KeepChats", fmt.Sprint(*keepChats))
	}
	cmd := exec.Command("powershell", args...)
	var env []string
	for _, e := range os.Environ() { // stale LIB/INCLUDE break Add-Type in Windows PowerShell
		if u := strings.ToUpper(e); strings.HasPrefix(u, "LIB=") || strings.HasPrefix(u, "INCLUDE=") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ui pass error:", err)
	}
}

func main() {
	flag.Parse()
	for {
		runOnce()
		if *watchSec <= 0 {
			return
		}
		time.Sleep(time.Duration(*watchSec) * time.Second)
	}
}

func runOnce() {
	now := time.Now()
	cutoff := now.Add(-time.Duration(*idleMin) * time.Minute)

	live, err := liveIDs()
	if err != nil && !*noProcs {
		fatal("process scan failed, refusing to delete anything: %v", err)
	}
	keepSet := map[string]bool{}
	for _, k := range strings.Split(*keepIDs, ",") {
		if k = strings.ToLower(strings.TrimSpace(k)); k != "" {
			keepSet[k] = true
			keepSet[strings.TrimPrefix(k, "local_")] = true
			live[k] = true // also protects orphan transcripts
		}
	}
	selfIDs := map[string]bool{}
	for _, k := range []string{"CLAUDE_SESSION_ID", "CLAUDE_CODE_SESSION_ID"} {
		if v := os.Getenv(k); v != "" {
			selfIDs[strings.ToLower(v)] = true
		}
	}

	// Index transcripts: cliID -> every path that belongs to it.
	owned := map[string]bool{} // transcript cli ids owned by a desktop session
	var sessions []*session

	root := filepath.Join(*appdata, "claude-code-sessions")
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "local_") || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		s := &session{file: p, id: strings.TrimSuffix(d.Name(), ".json")}
		raw, rerr := os.ReadFile(p)
		var m map[string]any
		if rerr != nil || json.Unmarshal(raw, &m) != nil {
			s.keep = "unreadable json"
			sessions = append(sessions, s)
			return nil
		}
		s.title, _ = m["title"].(string)
		s.archived, _ = m["isArchived"].(bool)
		for _, k := range []string{"cliSessionId", "lastAssistantCliSessionId"} {
			if v, ok := m[k].(string); ok && v != "" {
				s.cliIDs = append(s.cliIDs, strings.ToLower(v))
				owned[strings.ToLower(v)] = true
			}
		}
		s.cli = strings.Join(s.cliIDs, ",")
		// Turn in flight = user frame newer than last turn edge (and not ancient). Idle process != running.
		if uf, ok := m["latestUserFrameAt"].(float64); ok {
			te, _ := m["lastTurnEdgeAt"].(float64)
			if uf > te && time.Since(time.UnixMilli(int64(uf))) < 12*time.Hour {
				s.keep = "turn in flight (user frame newer than last turn end)"
			}
		}
		var latest int64
		for _, k := range []string{"lastActivityAt", "lastTurnEdgeAt", "lastFocusedAt", "latestUserFrameAt", "createdAt"} {
			if v, ok := m[k].(float64); ok && int64(v) > latest {
				latest = int64(v)
			}
		}
		s.activity = time.UnixMilli(latest)
		if fi, e2 := os.Stat(p); e2 == nil && fi.ModTime().After(s.activity) {
			s.activity = fi.ModTime()
		}
		s.paths = append(s.paths, p)
		sessions = append(sessions, s)
		return nil
	})

	for _, s := range sessions {
		if s.keep != "" {
			continue
		}
		// Desktop sessions: a merely attached idle claude.exe does NOT make a session running; only --keep / this session does.
		for _, id := range append([]string{strings.ToLower(s.id), strings.ToLower(strings.TrimPrefix(s.id, "local_"))}, s.cliIDs...) {
			if keepSet[id] || selfIDs[id] {
				s.keep = "protected via --keep / this session"
			}
		}
		for _, id := range s.cliIDs {
			s.paths = append(s.paths, cliPaths(*claudeDir, id)...)
		}
		s.paths = append(s.paths, previewPartition(*appdata, s.id)...)
		if s.keep == "" && s.activity.After(cutoff) {
			s.keep = "active within last " + fmt.Sprint(*idleMin) + " min"
		}
		for _, p := range s.paths {
			if t := newest(p); t.After(cutoff) && s.keep == "" {
				s.keep = "files touched within last " + fmt.Sprint(*idleMin) + " min"
			}
			if s.keep == "" && locked(p) {
				s.keep = "file locked (open by a process)"
			}
		}
		if s.keep == "" {
			for _, p := range s.paths {
				s.bytes += size(p)
			}
		}
	}

	// Orphan CLI transcripts (terminal sessions with no desktop record).
	if !*noOrphans {
		ms, _ := filepath.Glob(filepath.Join(*claudeDir, "projects", "*", "*.jsonl"))
		for _, f := range ms {
			id := strings.ToLower(strings.TrimSuffix(filepath.Base(f), ".jsonl"))
			if !uuidRe.MatchString(id) || owned[id] {
				continue
			}
			s := &session{id: id, cli: id, title: "(orphan CLI transcript)", paths: cliPaths(*claudeDir, id)}
			for _, p := range s.paths {
				if t := newest(p); t.After(s.activity) {
					s.activity = t
				}
			}
			if live[id] || selfIDs[id] {
				s.keep = "referenced by live process / this session"
			}
			for _, p := range s.paths {
				if s.keep == "" && newest(p).After(cutoff) {
					s.keep = "files touched within last " + fmt.Sprint(*idleMin) + " min"
				}
				if s.keep == "" && locked(p) {
					s.keep = "file locked (open by a process)"
				}
			}
			if s.keep == "" {
				for _, p := range s.paths {
					s.bytes += size(p)
				}
			}
			sessions = append(sessions, s)
		}
	}

	var del, kept, arch int
	var total, freed int64
	for _, s := range sessions {
		if s.keep != "" {
			kept++
			if *verbose {
				fmt.Printf("KEEP   %-45s %s  [%s]\n", trunc(s.title, 45), s.id, s.keep)
			}
			continue
		}
		// Idle but newer than --delete-after-hours: ARCHIVE (keep data). Older: delete.
		if time.Since(s.activity) < time.Duration(*delAfterH)*time.Hour {
			if s.file == "" { // orphan transcript: cannot be archived, keep
				kept++
				continue
			}
			if !s.archived {
				arch++
				if !*dryRun && !archiveJSON(s.file) {
					fmt.Fprintf(os.Stderr, "archive failed: %s\n", s.file)
				}
			}
			continue
		}
		del++
		total += s.bytes
		if *dryRun {
			continue
		}
		okAll := true
		for _, p := range s.paths {
			if err := os.RemoveAll(p); err != nil {
				okAll = false
				fmt.Fprintf(os.Stderr, "fail %s: %v\n", p, err)
			}
		}
		if okAll {
			freed += s.bytes
		}
	}

	// Cowork (local-agent-mode) per-session dirs: only the ones in this tree that look like sessions and are idle.
	cdel, cfreed := cowork(*appdata, cutoff, live, *dryRun)

	mode := "DELETED"
	if *dryRun {
		mode = "WOULD DELETE (dry run)"
		freed = total
	}
	fmt.Printf("\nsessions scanned: %d | kept (running/recent/locked/unreadable): %d | %s: %d\n", len(sessions), kept, mode, del)
	fmt.Printf("archived (idle, newer than %dh, data kept): %d\n", *delAfterH, arch)
	fmt.Printf("cowork session dirs %s: %d\n", strings.ToLower(mode), cdel)
	fmt.Printf("space reclaimed: %.1f MB\n", float64(freed+cfreed)/1e6)
	if !*noUI {
		runUI()
	}
}

// archiveJSON flips "isArchived":false -> true in place (text edit, all other fields untouched).
func archiveJSON(p string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	re := regexp.MustCompile(`"isArchived"\s*:\s*false`)
	if !re.Match(b) {
		return false
	}
	nb := re.ReplaceAll(b, []byte(`"isArchived":true`))
	tmp := p + ".tmp"
	if os.WriteFile(tmp, nb, 0o644) != nil {
		return false
	}
	if os.Rename(tmp, p) != nil {
		os.Remove(tmp)
		return false
	}
	return true
}

// cliPaths: every on-disk artifact for one CLI session id.
func cliPaths(home, id string) []string {
	var out []string
	for _, g := range []string{
		filepath.Join(home, "projects", "*", id+".jsonl"),
		filepath.Join(home, "projects", "*", id),
		filepath.Join(home, "file-history", id),
		filepath.Join(home, "session-env", id),
		filepath.Join(home, "todos", id+"*.json"),
		filepath.Join(home, "debug", id+".txt"),
	} {
		m, _ := filepath.Glob(g)
		out = append(out, m...)
	}
	return out
}

func previewPartition(app, id string) []string {
	m, _ := filepath.Glob(filepath.Join(app, "Partitions", "launch-preview-session-"+id))
	return m
}

func cowork(app string, cutoff time.Time, live map[string]bool, dry bool) (int, int64) {
	var n int
	var freed int64
	base := filepath.Join(app, "local-agent-mode-sessions")
	// layout: <acct>/<org>/local_<uuid>/ (+ local_<uuid>.json). skills-plugin and scheduled-tasks.json are config: never touched.
	ms, _ := filepath.Glob(filepath.Join(base, "*", "*", "local_*"))
	for _, p := range ms {
		if strings.Contains(p, "skills-plugin") {
			continue
		}
		id := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "local_"), ".json"))
		if live[id] || live["local_"+id] || newest(p).After(cutoff) || locked(p) {
			continue
		}
		n++
		sz := size(p)
		if dry {
			freed += sz
			continue
		}
		if os.RemoveAll(p) == nil {
			freed += sz
		}
	}
	return n, freed
}

// liveIDs: every uuid / local_<uuid> found on the command line of any running process.
func liveIDs() (map[string]bool, error) {
	out := map[string]bool{}
	if *noProcs {
		return out, nil
	}
	// Only ids passed to a live claude CLI via --resume / --session-id count as live
	// (other uuids on command lines are just text in prompts, e.g. linked-session lists).
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance Win32_Process -Filter \"Name='claude.exe' OR Name='node.exe'\" | ForEach-Object { $_.CommandLine }")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if len(b) < 100 {
		return nil, fmt.Errorf("process list empty")
	}
	for _, m := range flagIDRe.FindAllStringSubmatch(string(b), -1) {
		out[strings.ToLower(m[1])] = true
	}
	return out, nil
}

func newest(p string) time.Time {
	var t time.Time
	_ = filepath.WalkDir(p, func(q string, d os.DirEntry, e error) error {
		if e != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if fi, e2 := d.Info(); e2 == nil && fi.ModTime().After(t) {
			t = fi.ModTime()
		}
		return nil
	})
	return t
}

func size(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(q string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			if fi, e2 := d.Info(); e2 == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// locked: true when another process holds the file (exclusive open fails). Only files are probed.
func locked(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	u, _ := syscall.UTF16PtrFromString(p)
	h, err := syscall.CreateFile(u, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return true
	}
	syscall.CloseHandle(h)
	return false
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(2)
}
