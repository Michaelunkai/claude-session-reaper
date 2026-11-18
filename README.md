<div align="center">

# 🧹 claude-session-reaper

**Instantly delete every idle Claude Code session on Windows, and reclaim gigabytes. Running sessions are never touched.**

![Go](https://img.shields.io/badge/Go-1.21%2B-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-Windows-0078D6?logo=windows&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-green)
![Dependencies](https://img.shields.io/badge/dependencies-none-brightgreen)

</div>

---

## Why

The Claude desktop app keeps every Code session forever: a JSON record per session, plus the CLI transcript, file history and session environment. After a few weeks that is thousands of sessions and well over a gigabyte of dead weight. This tool archives idle sessions from the last 24 hours and deletes everything older, in seconds.

## Features

- ⚡ **One command, instant cleanup**, with no prompts and a summary at the end.
- 🛡️ **Fail-closed safety.** A session is kept if any of these hold:
  - a turn is in flight (the app recorded a user message newer than the last turn end)
  - it was touched in the last N minutes (default 2)
  - one of its files is held open by another process
  - its record is unreadable
  - you listed it with `--keep`
- 🧾 **Full cleanup per session:** desktop record, `projects/**/<id>.jsonl` (+ sub-dir), `file-history`, `session-env`, `todos`, `debug`, preview partition.
- 👻 **Orphan sweep:** terminal-only transcripts with no desktop record are removed too (`--no-orphans` to skip).
- 🔍 **`--dry-run`** shows exactly what would go and how much space it frees.
- 📦 **Single static `.exe`**, no runtime and no dependencies.

## Install

Download `claude-session-reaper.exe` from the [latest release](https://github.com/Michaelunkai/claude-session-reaper/releases/latest) and run it. No installer, no build step.

## Usage

```powershell
# preview, deletes nothing
claude-session-reaper.exe --dry-run -v

# delete everything that is not running
claude-session-reaper.exe

# explicitly protect sessions (desktop ids or CLI uuids)
claude-session-reaper.exe --keep local_aaaa...,local_bbbb...
```

Example output:

```
sessions scanned: 1642 | kept (running/recent/locked/unreadable): 6 | DELETED: 1636
space reclaimed: 1657.6 MB
```

| Flag | Default | Meaning |
|---|---|---|
| `--dry-run` | off | report only |
| `--min-idle-min N` | 2 | anything touched within N minutes counts as running |
| `--keep a,b` | none | never touch these ids |
| `--delete-after-hours N` | 24 | idle sessions newer than N hours are **archived** (data kept), older are **deleted** |
| `--no-orphans` | off | keep CLI transcripts without a desktop record |
| `-v` | off | list every kept session and why |
| `--appdata`, `--claudehome` | auto | override data locations (testing) |

> An idle `claude.exe` still attached to a session does **not** count as running; only an in-flight turn does.

> ⚠️ Deletion is **permanent**. Run `--dry-run` first. Pass `--keep` for any session you started that is idle but that you still need.

## Live pass: the open Claude app, no restart

After the file cleanup the exe drives the **open** Claude desktop app through Windows UI Automation (hidden PowerShell child, embedded in the exe). In Code mode it unpins and **archives every session that is not Running**. In Chat mode it **deletes every chat** (chats have no archive; this is permanent). It re-finds each row's button by name right before clicking, verifies the menu belongs to that row, refuses to confirm any "Delete session" dialog, and aborts if the app is not really in the expected mode. The Android app and claude.ai follow because the account syncs. It moves your mouse while running.

| Flag | Meaning |
|---|---|
| `--no-ui` | skip the live pass |
| `--no-chats` | live pass archives sessions but does not delete chats |
| `--keep-chats N` | keep the newest N chats |
| `--watch S` | repeat everything every S seconds |

## What it does *not* touch

Claude.ai cloud and Remote-Control bridge sessions live on Anthropic's servers and need account credentials, so they are out of scope. Cowork configuration and skills are never modified. The Claude app keeps an in-memory sidebar cache, so deleted entries disappear after you restart the app.

## Build from source

```powershell
git clone https://github.com/Michaelunkai/claude-session-reaper
cd claude-session-reaper
go test ./...
go build -ldflags "-s -w" -o claude-session-reaper.exe .
```

## Troubleshooting

- **"process scan failed, refusing to delete anything"**: PowerShell/CIM was unavailable; the tool deliberately deletes nothing.
- **A session you want gone is kept**: run with `-v` to see the reason (recent activity, live process or locked file).
- **Sidebar still shows deleted sessions**: restart the Claude app.

## License

MIT
