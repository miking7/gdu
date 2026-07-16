---
name: verify
description: Build gdu and drive its TUI end-to-end through a pty (pyte screen grid) — the only way to catch tview QueueUpdateDraw self-deadlocks and real key/scan/snapshot flows that mocked tests miss.
---

# Verifying gdu (a tview TUI + cobra CLI)

gdu is a Go TUI. **Mocked-app unit tests cannot catch tview `QueueUpdateDraw`
self-deadlocks** (they surface only under a real event loop) — so verification
means driving the *built binary* in a real pty and reading the rendered screen.
This is how R4a and R4b were accepted.

## Build

```sh
go build -o /tmp/gdu ./cmd/gdu        # CGO_ENABLED=0 pure-Go; fast
```

## Drive it (pty + pyte)

`pip install pyte` gives a screen grid (tcell emits full-screen escapes that a
plain ANSI strip can't reconstruct). Spawn with `pty.fork()`, set the window
size with `TIOCSWINSZ` (e.g. 40×130), feed reads into a `pyte.Stream(Screen)`,
drain on a time budget between keystrokes (async archive/snapshot loads run off
the event loop — give them ~1.5–2s), then print `screen.display`.

Keys: arrows are `\x1bOB`/`\x1bOA`/`\x1bOC`/`\x1bOD` (down/up/right/left), Enter
`\r`, Esc `\x1b`. tview tables also take `j`/`k`/`g`/`G`. **Always force-kill the
child at the end** (`SIGTERM` then `SIGKILL`) — macOS has no `timeout(1)` and a
wedged TUI won't exit on `q`.

A working driver: `scratchpad/drive.py` (args: `<cwd> <snapshots-dir> <gdu> <steps>`).

## Make a scratch archive to drive against

The launcher / snapshot flows need covering history. Build it non-interactively:

```sh
mkdir -p world/sub && head -c 2M /dev/urandom > world/big.bin
gdu -np --save-snapshots always --snapshots-dir snaps world   # writes a snapshot
gdu --snapshots-dir snaps ...                                  # then drive the TUI
```

For a snapshot that records **read errors** (evidence sudo tip / `ErrCount`):
`mkdir world/denied && chmod 000 world/denied` before the scan, then `chmod 755`
back. Always pass `--snapshots-dir <scratch>` so you never touch the user's real
`~/.local/share/gdu/snapshots`.

## Flows worth driving

- **Launcher** (bare `gdu` / `gdu <path>` / `gdu -d`): rows render, snapshot
  column appears only with covering history, `Enter` scans into a live tree,
  `s` opens the latest snapshot (read-only View), `S` stacks a picker, left-arrow
  at a live tree's top returns to the launcher, `Other folder…` input validates.
- **Skip paths**: `--launcher=false` / `--snapshot` / `-f` / piped (`-np`) must
  NOT show the launcher.
- **Time travel** (`[`/`]`), read-only signpost (`d` on a snapshot), Esc layering.

## Gotchas

- A long cwd path can blow out a shared tview table column — check the other
  columns are still visible at ~130 cols.
- `go test ./cmd/gdu/app -run TestAnalyzePathWithExport` is a pre-existing
  timing flake (progress ticker vs a fast scan); ignore it under load.
