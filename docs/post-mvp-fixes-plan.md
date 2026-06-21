# Post-MVP Fixes & Hardening — Implementation Plan

Status: **delivered on `feat/parquet-post-mvp`, verified** — tests + `golangci-lint`
(0 issues) + 4-target `CGO_ENABLED=0` cross-compile all green; DuckDB confirms the 22-column schema;
end-to-end export→`-f`→`--save-scan` round-trip checked. A batch of tweaks/fixes landed *before* the
larger post-MVP features (df free-space, growth-diff browsing, compaction). Companion to
[parquet-persistence-plan.md](parquet-persistence-plan.md) (which covers those bigger features).

Delivered notes (RAM — measured against a real 6.17 M-row, 150 MB snapshot):
- **The key fact is macOS-specific:** `debug.FreeOSMemory()` does **not** lower RSS on macOS
  (`MADV_FREE` keeps freed pages resident until pressure). So the fix had to be *not allocating the
  transient peak*, not freeing it afterwards.
- **`-f` import (was the worst: 12.5 GB → 1.8 GB):** `parquet.Read[Row]` materialised all 6.17 M rows
  at once. Replaced with a **streaming `ReadTree`** — `parquet.NewGenericReader[Row]` read in 8 K-row
  batches, tree built incrementally via a path→node map (get-or-create handles child-before-parent),
  latest scan found via a one-column projection. Plus `report.ReadAnalysis` now streams straight from
  the `*os.File` (sniff `PAR1` via `ReadAt`) instead of buffering the whole file.
- **`--save-scan` (2.5 GB → ~upstream):** (a) flatten garbage cut ~72% (`fs.SortByNone` unsorted
  iteration + lazy path building); (b) **streaming write** — rows batched to the writer, no giant
  `[]Row` (global path-sort dropped; readers don't need it, compaction can sort later); (c)
  `runtime.GC()` *before* the snapshot write so it reuses freed scan garbage instead of raising the
  (macOS-sticky) high-water. Measured: the save adds ~0 to peak RSS on a 6 M-node tree.
- `MaxRowsPerRowGroup` bounded to 128 K (writer no longer buffers the whole dataset as one group).

## Decisions locked (from planning Q&A, 2026-06-21)

| Topic | Decision |
|---|---|
| README positioning | **Keep `README.md` upstreamable** (reads like upstream gdu, minimal edits) + a concise **`FORK.md`** describing the fork's additions, linked from a small "fork note" banner. |
| Scheduling | **Document manual scheduling** (cron / systemd timers / launchd). No scheduler in the binary. |
| sudo output ownership | **Resolve the real invoking user and `chown` outputs back** — applied to **every** write path: `--save-scan`, explicit `-o *.parquet`, explicit `-o *.json`, and TUI export (`e`). |
| Identity columns | Capture **both** the **effective** user (`username`, e.g. `root`) **and** the **invoking** user (`sudo_user`, e.g. `michael`; null when not via sudo). Plus `host`. |

---

## Issue 1 — RAM regression (the priority)

### Diagnosis (evidence-backed)
A probe (500k-file synthetic tree, threshold 0) isolated it. Reading a **0.6 MiB** snapshot peaked at
**712 MiB heap / 747 MiB Sys**; a forced scavenge dropped it to 68 MiB, **releasing ~645 MiB** — which
matches the reported 2.6 GB vs 1.95 GB (+650 MiB) almost exactly.

| Step | HeapAlloc | Sys | Note |
|---|---|---|---|
| tree built (500k files) | 64 MiB | 75 MiB | the scan |
| after `WriteTree` (thr=0) | 243 MiB | 405 MiB | ~330 MiB transient for a 0.6 MiB file |
| after `FreeOSMemory` | 90 MiB | (302 MiB released) | reclaimable |
| after `ReadTree` (0.6 MiB file) | 712 MiB | 747 MiB | `parquet.Read[Row]` materializes all rows + decode garbage |
| after `FreeOSMemory` | 68 MiB | (**645 MiB released**) | ≈ the regression |

**Root cause:** the Parquet read/write code is very allocation-heavy and transient, and **the import
(`-f`) and Parquet write paths never call `debug.FreeOSMemory()`** — unlike the live-scan goroutine
([tui/actions.go:88](../tui/actions.go)). The garbage is collected but **not returned to the OS**, so
RSS stays high for the whole browsing session. Not a leak — un-scavenged transient.

### Fix (in leverage order)
1. **Scavenge after read + write.** Add `runtime.GC()` + `debug.FreeOSMemory()` at:
   - the import boundary in [report/import.go](../report/import.go) `ReadAnalysis` (every importer —
     TUI, stdout, export — funnels through it), and
   - after the write in `SaveSnapshot` ([pkg/parquet/snapshot.go](../pkg/parquet/snapshot.go)) and the
     `-o` export funcs ([report/export.go](../report/export.go)).
   This is the fix for the browsing regression and mirrors gdu's existing pattern.
2. **Bound write row groups.** Pass `parquet.MaxRowsPerRowGroup(n)` (default is `MaxInt64` → one giant
   in-memory row group) in `WriteTree` so pages stream out. Cuts the *write* peak. Const ~`256<<10`.
3. **(Conditional) Streaming read.** Only if, after #1, the read *peak* still hurts on real snapshots:
   replace `parquet.Read[Row]` with a batched `parquet.NewGenericReader[Row]` loop (reuse a row buffer,
   build the tree incrementally) and, for file input, pass the `*os.File` (`io.ReaderAt`) through
   instead of buffering the whole file into `raw` ([report/import.go:30](../report/import.go)). Stdin
   still needs buffering. Defer until measured.

**Files:** `report/import.go`, `pkg/parquet/snapshot.go`, `pkg/parquet/write.go`, `report/export.go`.
**Tests:** a `runtime.MemStats`-based test asserting post-read `HeapInuse` returns near baseline after
the scavenge; assert multiple row groups are produced for a large write. Verify `-f` round-trip still
identical. **Validate against the real workflow once confirmed** (likely `-f` loading a snapshot).
**Risk:** very low. `FreeOSMemory` costs one blocking GC per one-shot op; row groups are standard.

---

## Issue 2 — Snapshot filename → local timezone

`SnapshotFileName(now)` ([pkg/parquet/snapshot.go:13](../pkg/parquet/snapshot.go)) currently formats
`now.UTC().Format("20060102T150405Z")`. Change to **local time**: `now.Format("20060102T150405")`
(drop the `Z`, which means UTC/Zulu and would mislead). `time.Now()` is already local.

- **The `scan_ts` *column* stays UTC / tz-aware** (`SaveSnapshot` keeps `ScanTime: now.UTC()`), so
  DuckDB `TIMESTAMPTZ` analysis is unaffected — only the human-facing filename changes.
- **Collision guard:** second-resolution names collide if two scans land in the same second (and
  `O_TRUNC` overwrites). Add a `_1`, `_2`… suffix when the target already exists.

**Files:** `pkg/parquet/snapshot.go`. **Tests:** filename format (local), collision-suffix behavior.

---

## Issue 3 — sudo-safe output ownership (shared across all write paths)

### New shared helper: `internal/common/ownership.go` (pure-Go, CGO-free)
```go
// RealUser returns the user who invoked sudo (uid, gid, home), or ok=false when
// not running under sudo (su -, direct root login, cron/systemd/launchd as root).
func RealUser() (uid, gid int, home string, ok bool)

// ChownToInvoker hands a path back to the real user. No-op when not root or not
// under sudo. Best-effort (logs at debug on failure).
func ChownToInvoker(path string) error
```
- Read `SUDO_USER` / `SUDO_UID` / `SUDO_GID`. Prefer **numeric** `SUDO_UID/GID` for `os.Chown`
  (no NSS lookup — important under `CGO_ENABLED=0` for LDAP/AD users); use `user.Lookup(SUDO_USER)`
  only to get `HomeDir`. Gate chown on `os.Geteuid() == 0`. Treat `SUDO_USER=root` as "no real user".

### Wiring (all four write paths)
| Path | Where | Chown |
|---|---|---|
| `--save-scan` | `SaveSnapshot` ([pkg/parquet/snapshot.go](../pkg/parquet/snapshot.go)) | the created `scans-dir` **and** the file |
| `-o *.parquet` / `-o *.json` | after `runAction` in [app.go](../cmd/gdu/app/app.go) (knows `OutputFile`) | the output file |
| TUI export (`e`) | `exportAnalysis` ([tui/actions.go:424](../tui/actions.go)) | `ui.exportName` |

### Real-home resolution for `--scans-dir` default
`resolveScansDir` ([app.go:368](../cmd/gdu/app/app.go)) uses `os.UserHomeDir()`, which under
`sudo -H` / `always_set_home` is `/root`. Change it to prefer `RealUser()`'s home when under sudo, so
`sudo gdu --save-scan` lands in the **invoking** user's `~/.gdu-scans`, not root's.

**Note (links to Issue 6):** scheduled root jobs (cron/systemd/launchd as root) have **no `SUDO_USER`**,
so chown-back won't fire — the scheduling doc must cover ownership for those (shared `--scans-dir`, or a
post-run chown). Document explicitly.

**Tests:** `RealUser` parsing of the env matrix (sudo / not / `SUDO_USER=root`); `ChownToInvoker`
no-ops when non-root. (Actual chown needs root; assert the no-op + env-parsing paths in CI.)
**Risk:** low; all changes are best-effort and gated on euid 0.

---

## Issue 4 — New Parquet columns: `host`, `username`, `sudo_user`

Scan-level fields, stamped on every row (dictionary-compress to ~nothing; consistent with the existing
flat schema that already repeats `scan_root`/`scan_ts`).

- **Schema** ([pkg/parquet/schema.go](../pkg/parquet/schema.go)) — add to `Row`:
  ```go
  Host     string  `parquet:"host,zstd"`       // os.Hostname()
  Username string  `parquet:"username,zstd"`   // effective user (e.g. "root")
  SudoUser *string `parquet:"sudo_user,optional,zstd"` // invoking user; null if not via sudo
  ```
- **`ScanMeta`** ([pkg/parquet/write.go](../pkg/parquet/write.go)) — add `Host`, `Username`,
  `SudoUser` fields; set them in `stampMeta`.
- **Identity collection** — `internal/common`: `os.Hostname()`; effective via `user.Current()` (fall
  back to `$USER`/uid string if the pure-Go lookup fails); invoking via `SUDO_USER`. Filled where
  `ScanMeta` is built (`report/export.go` exportParquet, `pkg/parquet/snapshot.go` SaveSnapshot).
- **Reader** — `ReadTree` ignores the three (not needed to rebuild the tree). **Back/forward compat:**
  parquet-go matches columns by name, so new code reads old snapshots (missing cols → zero/null) and
  vice-versa. Add a test.

**Schema is now 22 columns.** Update the schema table in [CLAUDE.md](../CLAUDE.md) and verify in DuckDB.
**Risk:** low. Schema additions are compatible.

---

## Issue 5 — README as a fork (upstreamable)

- **`README.md`**: stays essentially upstream gdu's, with a single small **fork-note admonition** near
  the top linking to `FORK.md` (one block, trivial to drop when upstreaming).
- **`FORK.md`** (new): "This is a fork of [dundee/gdu] kept upstreamable." Summarizes the additions —
  Parquet export/import (`-o *.parquet`, `-f`), `--threshold` rollup, `--save-scan`/`--scans-dir`,
  sudo-safe ownership, scheduling guide — links to `configuration.md` and `docs/`. Notes the CGO-free /
  pure-Go constraint and that DuckDB is an *external* query tool.

**Why this shape:** smallest upstream-PR surface (README barely changes), fork differences in one
focused doc. **Files:** `README.md` (small), `FORK.md` (new). **Risk:** docs only.

---

## Issue 6 — Scheduling guide (`docs/scheduling.md`)

Document manual scheduling for macOS + Linux; **no binary changes**. Link from `FORK.md` / `README.md`.

### Contents
- **Linux — cron:** user `crontab -e`; root via `/etc/cron.d/gdu` (explicit user field, package-
  friendly). Daily + every-6-hours examples. Always use the **absolute path** to `gdu` and `-p`
  (non-interactive). Pros/cons (ubiquitous, but no catch-up, no built-in logging).
- **Linux — systemd timers (recommended):** paired `.service` (`Type=oneshot`) + `.timer`
  (`OnCalendar=`, `Persistent=true` for catch-up, `RandomizedDelaySec=`). System timers (root) vs user
  timers (`systemctl --user` + `loginctl enable-linger`). `Nice=`/`IOSchedulingClass=idle`. Logging via
  `journalctl`; `systemctl list-timers`.
- **macOS — launchd:** LaunchAgent (`~/Library/LaunchAgents`, per-user) vs LaunchDaemon
  (`/Library/LaunchDaemons`, root). `.plist` with `StartCalendarInterval` (daily) /`StartInterval`
  (6-hourly); prefer `StartCalendarInterval` on laptops (intervals are missed during sleep). Modern
  `launchctl bootstrap`/`bootout`/`kickstart`.
- **macOS Full Disk Access (prominent caveat):** a `/`-wide scan needs FDA, or gdu silently hits
  "operation not permitted" on TCC-protected paths (Mail, Messages, other users' homes) and
  under-reports. The "drag bash into FDA" trick is **dead**. **As delivered** (see
  [scheduling.md](scheduling.md)): copy gdu to a stable path (`/usr/local/sbin/gdu`) and grant FDA to
  **that binary** in System Settings → Privacy & Security → Full Disk Access. A **root LaunchDaemon
  honours the binary's own FDA grant — no MDM/PPPC required** for a single binary you add yourself.
  (This supersedes the planning assumption that an unmanaged-Mac root daemon couldn't get FDA without
  MDM.) The grant is pinned to the binary's bytes, so re-copy + re-add it after every gdu upgrade.
  Fallback for the newest macOS, where the picker may refuse a bare CLI binary: wrap gdu in a
  one-action Automator `.app` and grant FDA to that. macOS 15 also gates cron/legacy jobs behind
  "Login Items → Legacy Background Tasks".
- **Root vs user scans + ownership:** root scans see all files but their **scheduled** form has no
  `SUDO_USER`, so the Issue-3 chown-back won't fire. Document: set `--scans-dir` to a shared/readable
  location, set `HOME=` in the unit, or add a post-run `chown` in the job. Cross-reference Issue 3.

**Files:** `docs/scheduling.md` (new). **Risk:** docs only.

---

## Sequencing (independently shippable, conventional-commit PRs)

1. **`perf:` RAM** — scavenge + row-group bound (Issue 1). Highest user pain, isolated, cleanly
   upstreamable. *(Streaming read deferred behind a measurement.)*
2. **`feat:` elevated/sudo story** — ownership helper + chown-back across all write paths + real-home
   resolution + `host`/`username`/`sudo_user` columns + local-time filename (Issues 2, 3, 4). One
   coherent "running elevated" change.
3. **`docs:` fork + scheduling** — `FORK.md` + README note + `docs/scheduling.md` (Issues 5, 6).

## Cross-cutting (must hold for every change)
- **CGO-free / pure-Go**; spot-check `make build-all` cross-compile after the schema + ownership changes.
- New exported symbols get doc comments (revive `exported`); keep `funlen`/`gocyclo` within limits.
- Update `configuration.md` / `README.md` / `gdu.1.md` for any user-facing change; regenerate `gdu.1`
  (`make gdu.1`, needs pandoc) since it's a tracked artifact.
- Tests via `gotestsum`; lint with the pinned golangci-lint; `--threshold 0` keeps JSON byte-identical.
