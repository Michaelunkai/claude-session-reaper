package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFlagIDOnlyMatchesResumeAndSessionID(t *testing.T) {
	cmd := `claude.exe --resume=4fbfcf16-d6a1-4d94-b365-0d6d8fc79440 --append-system-prompt "linked 884f3bb2-8c2f-4dad-9994-e7bb08525b2e"`
	m := flagIDRe.FindAllStringSubmatch(cmd, -1)
	if len(m) != 1 || m[0][1] != "4fbfcf16-d6a1-4d94-b365-0d6d8fc79440" {
		t.Fatalf("unexpected matches: %v", m)
	}
}

func TestCliPathsCollectsArtifacts(t *testing.T) {
	home := t.TempDir()
	id := "11111111-1111-1111-1111-111111111111"
	_ = os.MkdirAll(filepath.Join(home, "projects", "p"), 0o755)
	_ = os.WriteFile(filepath.Join(home, "projects", "p", id+".jsonl"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(home, "file-history", id), 0o755)
	if got := cliPaths(home, id); len(got) != 2 {
		t.Fatalf("want 2 paths, got %v", got)
	}
}

func TestNewestIgnoresDirsAndLockedDetects(t *testing.T) {
	d := t.TempDir()
	f := filepath.Join(d, "a")
	_ = os.WriteFile(f, []byte("x"), 0o644)
	old := time.Now().Add(-48 * time.Hour)
	_ = os.Chtimes(f, old, old)
	if newest(d).After(time.Now().Add(-time.Hour)) {
		t.Fatal("dir mtime must not count as activity")
	}
	if locked(f) {
		t.Fatal("unopened file reported locked")
	}
}
