# About this fork

This is a fork of [**dundee/gdu**](https://github.com/dundee/gdu), the fast parallel disk-usage
analyzer. It is deliberately kept **upstreamable** — it holds gdu's constraints (`CGO_ENABLED=0`,
pure-Go dependencies, every cross-compile target builds, full tests + docs) so the additions could be
contributed back. How the fork tracks upstream — the sync cycle, versioning, and the per-round
decision log — is recorded in [docs/UPSTREAM.md](docs/UPSTREAM.md).

Everything in upstream gdu works exactly as before. What the fork adds is **history**: upstream gdu
answers "what's eating my disk right now?"; the fork answers that **plus** "what grew?", "what did it
look like before?", and "when did this happen?". Every completed scan is archived as a compact
Parquet **snapshot**; the TUI can compare against any of them, step through them in place, and open
them read-only. No mainstream disk-usage TUI has this — ncdu's imports are read-only with no diff,
and `duc` caches an index but has no timeline, diff, or time travel.

## Quickstart

Install with the four lines in [README § Installation](./README.md#installation) — macOS and Linux
binaries are published on the [releases page](https://github.com/miking7/gdu/releases).

```sh
gdu              # the launcher: the folder you're in, its disk, your other disks, your snapshots
                 #   Enter scans the selected row (the completed scan is archived automatically)
                 #   s  open its latest snapshot without scanning · S  pick a specific one
gdu ~            # same launcher, with ~ as the first row   (launcher: false scans immediately, like upstream)
# inside a scan or a snapshot:
#   S            #   pick an old snapshot as a baseline → every row shows a signed Δ
#   [  and  ]    #   step the view itself back and forth through your snapshots
#   O            #   open any archived snapshot (any root, any date) as the view
#   Esc          #   clear the comparison / return where you started
gdu snapshots    # list the archive from the shell
gdu --snapshot 2026-06-19 ~   # reopen a point in time without scanning
```

## Vocabulary

The fork uses these words precisely (see [docs/DESIGN.md](docs/DESIGN.md)):

| Term | Meaning |
|---|---|
| **snapshot** | One dated record of one scanned root — what gets saved, listed, selected, loaded, and diffed. Its identity is the `(host, scan_root, scan_ts)` tuple. |
| **scan** | The *act* of walking the filesystem — the event that produces a snapshot. The schema columns `scan_root`/`scan_ts` describe that event. |
| **snapshot file** | A `.parquet` file holding one or more snapshots (dailies hold one; compacted monthlies hold many). |
| **archive** | The snapshots directory (default `$XDG_DATA_HOME/gdu/snapshots`, i.e. `~/.local/share/gdu/snapshots`) holding all snapshot files. |
| **baseline** | A snapshot chosen for growth-diff comparison against the current view. |
| **View / Baseline** | Every gdu screen shows a **View** (what you're looking at: the live disk, or a snapshot), optionally against a **Baseline** (what you're comparing to). You can only *change* the disk when the View is live. |
| **timeline** | The dated sequence of snapshots covering the current folder, oldest→newest, with the live disk as its newest point. `[` and `]` walk it. |

"snapshot" is never used for the act of scanning; "scan" is never used for the stored record.

## The journeys

### "Launch" — the front door
Run `gdu`. Instead of a blind scan, you get the launcher: the folder you're standing in, the disk it
lives on, your other disks — each with its size, and, when history exists, *how recent your newest
snapshot of it is*. Enter scans the selected row. `s` opens its latest snapshot **without scanning
anything**; `S` lets you pick a specific snapshot from a list. If a previous scan couldn't read
everything, gdu tells you here and points you at `sudo` to include those folders (on macOS, Full Disk
Access too). Typed `gdu some/path`? Same screen, with that path as the first row. Prefer the old
behavior? `launcher: false` in your config scans immediately, exactly like upstream.

### "What's eating my disk?" — unchanged
Scan, browse, sort, delete — everything upstream gdu does, at upstream speed, with upstream keys.
History costs you nothing: the only difference you'll ever notice is one quiet line the first time
a completed scan is archived, telling you where the snapshots live and how to turn them off.

### "What grew?" — the headline
Weeks later, the disk is mysteriously full again. Scan it, press `S`, pick the snapshot from last
month (the picker already shows each snapshot's size *of this folder* and the Δ against now), press
Enter. Every row now carries a signed Δ, sorted so the biggest grower is on top. Follow the ▲ trail
downward — `/` → `~/Library` → `Caches` → the culprit. Two keys from scan to answer. Esc clears.

### "What did it look like before?" — time travel
Browsing anything, press `[`. Same folder, same cursor — one month ago, read-only. `[` again: older.
`]`: newer. The header's first line always tells you *when* you're looking at; the footer shows this
folder's size at that moment. Press `]` past the newest snapshot and you're back on the live view
(if there isn't one yet, gdu offers to scan — your choice, with fair warning that it takes time).
`Esc` returns you to where you started, instantly, from anywhere. If the folder didn't exist back
then, gdu shows its nearest ancestor and says so.

### "*When* did this happen?"
Something planted 40 GB in `~/Library` in June. Stand in the folder and ride `[` backwards,
watching the footer: 62 G… 62 G… 61 G… **22 G** — stop. The jump is bracketed by two timestamps,
usually a single day. Now you know *when*, which usually tells you *what*.

### "Free the space" — acting on what you found
You found the culprit in a three-week-old snapshot and press `d`. gdu declines, helpfully: *this is
a snapshot from 25 days ago; gdu deletes from the live disk, which may no longer match.* Its offer:
**go live here** — instantly if a live view of this folder is already in memory, otherwise a quick
scan of just this folder (you confirm; it's the subtree only). Cursor kept. Now `d` works — against
reality, with fresh sizes and the normal confirmation. The safe path is the shortest path.

## "Set and forget" — the operator

One line in a scheduled unit — `gdu --save-snapshots always --owner you /` — gives you nightly
whole-disk history, owned by you, self-compacting monthly, browsable without sudo. Every journey
above then works against scans root took at 3 AM that you could never run interactively.
[docs/scheduling.md](docs/scheduling.md) has ready-made cron/systemd/launchd units.

## "Take it further"

`gdu snapshots` lists the archive. `gdu --snapshot 2026-06-19 /` opens a point in time from the
shell. And the archive is just Parquet: point DuckDB at
`~/.local/share/gdu/snapshots/*.parquet` and chart your disk's history with SQL. Open data, not a
silo.

## Reference

### The launcher
Bare `gdu`, `gdu <path>`, and `gdu -d` open the launcher — the interactive front door (it replaces
upstream's standalone device list and the immediate scan). It **is** the familiar device table
(same columns, coloring, sorting), under a header bar, with the folder you're in pinned first, **its
disk pinned directly below it**, and `Scan another folder...` pinned last; the folder and
Scan-another-folder rows sit in the Mount point column — the folder with a dim `(current folder)` /
`(specified folder)` note. Disk rows list your mounts as `-d` does; on macOS the synthetic
`/System/Volumes/*` volumes are hidden from this list (they still appear in the classic `gdu -d`
table and piped `-d`). A **Snapshot column** appears before Mount point only once the archive holds
history for a row — first run shows no history chrome at all.

Snapshots are matched to rows **by mount**, not merely by path: a disk row lists snapshots of exactly
that mount, and the folder row lists snapshots rooted between its own disk and the folder — so a
whole-disk `/` snapshot (which ignored nested mounts) is never mis-credited to another disk.

| Key | Meaning |
|---|---|
| `Enter` | Scan the selected row (a deliberately chosen root, so the completed scan is archived). |
| `s` | Open that row's **latest** snapshot as a read-only View — no scan. |
| `S` | Pick a specific snapshot of that row from a list, then open it. |
| `n` | Sort the disks (usage-desc ↔ name-asc); the folder, its pinned disk, and `Scan another folder...` stay put. |
| `R` | Restart gdu with `sudo` (non-root Unix only). You're prompted for your password; the elevated gdu reopens the launcher. |
| `↑`/`↓`, `j`/`k`, `g`/`G` | Move. |
| `q` | Quit. |

Pre-selection: `gdu <path>` starts on the folder row; bare `gdu` / `gdu -d` start on your folder's
(pinned) disk. Choosing that pinned disk — scan or snapshot — opens at the folder you're in rather
than the disk root (the whole disk is still scanned and recorded); other disks open at their root.
When run without root (uid ≠ 0), a tip line invites you to **press `R` to restart with `sudo`**; once
a previous scan of the row recorded read errors, it says how many folders it couldn't read. On macOS
it adds that even `sudo` is limited without Full Disk Access. Choosing to scan the whole **root
volume (`/`)** always confirms first — *Scan anyway* or *Restart with sudo* — since a non-root `/`
scan almost always misses folders. A restart re-runs gdu verbatim under `sudo` (forwarding your
config file); snapshots still end up owned by you. The launcher is skipped by `--snapshot`, `-f`,
`--read-from-storage`, `--db`, non-interactive mode, and `launcher: false`. Left-arrow at the top of a
live tree returns here.

### History in the TUI

| Key | Meaning |
|---|---|
| `[` / `]` | Step the View to an older / newer snapshot of the current folder. At the newest, `]` returns to the live view — instantly when the live tree is still in memory, otherwise via an explicit rescan offer. |
| `S` | Compare: pick a **baseline** snapshot covering this folder (each row then carries a signed Δ; `>`/`<` sort by growth). |
| `O` | Open: pick **any** archived snapshot — all roots and dates — as the View (the long jump). |
| `Esc` | Layered, always instant: close an overlay → clear the baseline → return to where the session started. Esc never scans. |

Rules the TUI holds itself to:

- **Snapshot Views are hard read-only** — `d`/`e` (and `r`) show a signpost dialog whose primary
  action is *go live here*: an instant switch when a live tree covering the folder is in memory,
  else a confirmed scan of just that folder. `v`/`o`/`i` stay available (live paths may have
  changed; they fail soft). This applies equally to `-f` imports — a deliberate divergence from
  upstream (ncdu precedent: imports disable delete/refresh/shell); refreshing an import used to
  graft live data into it, and that behavior is retired.
- **A snapshot records the completed scan of a deliberately chosen root.** `r` refreshes and
  go-live spot-rescans never save; quitting mid-scan asks and discards.
- **The timeline stays walkable while a scan runs**: the progress screen is the timeline's live
  position (with a hint the moment covering history exists), a `scanning…` footer indicator shows
  wherever you are in the past, and completion never steals focus — the footer flashes
  `scan complete — ] to view` and the just-saved snapshot folds into the live position.
- **Upstream's own scan-time additions still work, folded into this model.** `Tab` previews the
  partial tree found so far (upstream #594) — the complement to time travel: it browses the
  *present* being built, while `[`/`]` browse the *past*. And the quit confirmation for long scans
  (upstream #593) is now snapshot-aware: quitting is silent when the scan was archived, and only
  asks when results are genuinely unsaved; `--no-confirm-quit` turns it off.

### Parquet snapshots
Save, archive, and re-import scans as [Apache Parquet](https://parquet.apache.org/), alongside gdu's
existing ncdu-style JSON:

- **Zero-config recording**: every completed interactive (TUI) scan of a chosen root saves a
  snapshot to the archive
  (`~/.local/share/gdu/snapshots/snapshot_<timestamp>_<root>.parquet`; the `<root>` suffix is a
  lower-case slug of the scanned path, e.g. `…_volumes_sd.parquet`). The first save that creates the
  archive directory announces where it went. `--save-snapshots never` (flag or yaml) opts out;
  `--save-snapshots always` extends saving to non-interactive scans (what scheduled scans use).
- `gdu -o snapshot.parquet /` (or `gdu --output-format parquet -o- /`) — export one snapshot to an
  explicit file instead.
- `gdu -f snapshot.parquet` — browse a snapshot file in the TUI/CLI; JSON vs Parquet is
  auto-detected. Interactive imports open as read-only snapshot Views.
- `gdu --snapshot latest /path` — reopen the archive's snapshot of exactly `/path` without touching
  the disk (works in every mode: TUI browsing, `-n --top 20`, …).

Snapshots are written and read with the pure-Go [`parquet-go`](https://github.com/parquet-go/parquet-go)
library — **DuckDB is *not* a dependency**; it stays an *external* tool you can point at the files.
Reads and writes are **streaming**, so a full-disk snapshot of millions of files stays within bounded
memory. Snapshot **filenames use local time**; the `scan_ts` **column** inside is stored in UTC
(timezone-aware) so snapshots from different machines/zones compare correctly.

### Snapshot selection (one grammar everywhere)
The selector grammar is `latest` (the default), `earliest`, or a local-time prefix like
`--snapshot 2026-06-19`; an ambiguous or unmatched selector errors with the candidates listed in
paste-ready form.

- **Against the archive** (no `-f`): `gdu --snapshot <sel> /path` resolves among the archive's
  snapshots whose root is exactly `/path` (`--snapshot-root` overrides the root scope). If no
  snapshot of `/path` exists but one of a covering root does (say `/`), the error names it as a
  hint.
- **Within a file**: `gdu -f monthly.parquet --snapshot <sel>` picks one snapshot from a
  multi-snapshot file; `--snapshot-root` disambiguates when several roots share a timestamp.
- Interactively (`gdu -f monthly.parquet` without `--snapshot`), the snapshot picker lists the
  file's snapshots and Enter loads one; the hint line shows the exact `--snapshot` invocation, so
  the interactive choice teaches the scriptable one. The same picker component serves `S`
  (baseline) and `O` (open) in the TUI.

### The `gdu snapshots` subcommand
- `gdu snapshots` (alias `gdu snaps`) prints every snapshot in the archive (newest first) as an
  aligned table (`#  WHEN  SIZE  ROWS  ROOT [HOST] [FILE]`); `gdu snapshots <file.parquet>` lists one
  file's snapshots. It is the discovery tool for `--snapshot` values. The explicit verb form is
  `gdu snapshots list` (alias `ls`).
- `gdu snapshots compact` merges each **closed** month's dailies — grouped by
  `(host, scan root, month)` — into one `monthly_<yyyy-mm>_<root>.parquet` per group, losslessly
  (every snapshot is present row-for-row, verified before any source is deleted).
  `gdu snapshots compact --dry-run` prints the plan without writing.

### Growth-diff (interactive)
- `gdu --baseline <sel|file> /path` opens in **growth-diff** mode: each folder is annotated with how
  it grew or shrank since the baseline snapshot. The value is either a snapshot file path (a
  multi-snapshot file loads its latest) or a selector resolved against the archive, scoped to
  snapshots whose root **covers** `/path`; `--baseline-root` pins the selector to one exact root.
- Press **S** in the TUI to pick a different baseline from the archive (the picker shows each
  snapshot's size for the current folder and its change since). The baseline stays pinned while
  `[`/`]` move the View — that *is* snapshot-vs-snapshot scrubbing; the Δ column recomputes per
  step.

### Auto-compaction
Whenever a snapshot is saved, gdu opportunistically compacts closed months (in the background in the
TUI; inline after the report in non-interactive mode). It is safe to interrupt — sources are only
deleted after the merged output is row-verified — and skips silently if another gdu already holds the
compaction lock. `--no-auto-compact` (yaml `no-auto-compact`) opts out.

### Threshold rollup
`--export-threshold 10M` buckets every file or directory whose disk usage is below the size into a
synthetic `<smaller objects>` row, shrinking snapshots dramatically while preserving each directory's
**exact** recursive totals. It applies to **both** JSON and Parquet export. `--export-threshold 0`
(the default for `-o`) keeps gdu's output byte-for-byte; saved snapshots default to `10M`.

### sudo-friendly output
Whole-disk scans usually need `sudo`. When run elevated, gdu resolves the **invoking** user (via
`SUDO_USER`/`SUDO_UID`/`SUDO_GID`), writes saved snapshots into *their*
`~/.local/share/gdu/snapshots` (not `/root`'s — that user's own `XDG_DATA_HOME` environment is
unknowable from an elevated process and is not consulted), and `chown`s **every** file it writes —
saved snapshots, `-o` exports, and interactive TUI exports — back to that user, so the output stays
readable without root.

For scheduled root scans (cron/systemd/launchd, which aren't under `sudo`), `--owner <user>` requests
the same hand-back explicitly: gdu resolves that user's home + IDs and chowns output to them. See
[docs/scheduling.md](docs/scheduling.md).

### Scheduling
gdu stays a scanner — there is no built-in scheduler. [docs/scheduling.md](docs/scheduling.md) shows
how to run periodic scans with cron, systemd timers, or launchd on macOS and Linux, including the
macOS **Full-Disk-Access** caveat and how to handle root-vs-user scans and snapshot ownership.

## Snapshot schema

Each snapshot is a flat table: **one row per file, directory, or `<smaller objects>` rollup bucket**
(22 columns). Types below are how [DuckDB](https://duckdb.org/) reads them.

| Column | Type | Description |
|---|---|---|
| `path` | VARCHAR | Full path of the entry. |
| `parent` | VARCHAR | Path of the containing directory. |
| `name` | VARCHAR | Base name (`<smaller objects>` for rollup rows). |
| `is_dir` | BOOLEAN | True for directory rows. |
| `is_rollup` | BOOLEAN | True for a `<smaller objects>` rollup bucket. |
| `depth` | INTEGER | Depth below the scan root (root = 0). |
| `asize` | BIGINT | Apparent size (files/rollups; `0` for directories). |
| `dsize` | BIGINT | Disk usage (files/rollups; `0` for directories). |
| `dir_total_dsize` | BIGINT | Recursive disk usage — directories only (else null). |
| `dir_total_files` | BIGINT | Recursive file count — directories and rollups. |
| `dir_total_folders` | BIGINT | Recursive folder count — directories and rollups. |
| `scan_root` | VARCHAR | The path that was scanned. |
| `scan_ts` | TIMESTAMPTZ | When the scan ran (UTC instant). |
| `threshold_bytes` | BIGINT | The rollup threshold used for this snapshot. |
| `host` | VARCHAR | Machine hostname (`os.Hostname()`). |
| `username` | VARCHAR | Effective user the scan ran as (e.g. `root`). |
| `sudo_user` | VARCHAR | Invoking user under sudo; null otherwise. |
| `mtime` | TIMESTAMPTZ | File modification time. |
| `ino` | UBIGINT | Inode — populated only for hard-linked files (else null). |
| `notreg` | BOOLEAN | Non-regular file (symlink, socket, …). |
| `hlnkc` | BOOLEAN | Hard link (its size is counted once, like in gdu). |
| `read_error` | BOOLEAN | Entry could not be fully read (e.g. permission denied). |

**Why `scan_*`, not `snapshot_*`.** The columns are named for the *scan event* they describe — the
root that was walked (`scan_root`) and the instant it completed (`scan_ts`). That is glossary-correct:
a snapshot's *identity* is `(host, scan_root, scan_ts)`, so these columns name facts about the scan,
not a second word for the record. It is a deliberate decision, not a leftover.

**Size conventions.** gdu has no per-inode size for directories, so directory rows carry
`asize = dsize = 0` and put their recursive figures in `dir_total_*`. Summing `dsize` over all
**non-directory** rows reproduces the scan total. A `<smaller objects>` row carries the aggregated
`asize`/`dsize` and the represented file/folder counts of everything it replaced — so a directory's
surviving children still sum back to its recursive total.

`host`, `username`, and `sudo_user` identify *where* and *as whom* each scan ran — useful when several
machines' snapshots are pooled, and to tell elevated (`root`) scans from regular ones.

**On-disk format.** Snapshot files are **format 2**: every write stamps footer key-value metadata
`gdu.format` = `2` and a `gdu.snapshots` JSON manifest (per-snapshot identity + rows + total +
threshold). The column layout is unchanged from format 1 — format 2 is a metadata-vocabulary bump.
Files without a `gdu.snapshots` manifest (foreign Parquet, or pre-rename format 1) are still listed via
footer column statistics / a projection pass, so DuckDB-written files interoperate.

### Querying with DuckDB

```sql
-- Biggest directories in the most recent snapshot
SELECT path, dir_total_dsize
FROM read_parquet('~/.local/share/gdu/snapshots/*.parquet')
WHERE is_dir AND scan_ts = (SELECT max(scan_ts) FROM read_parquet('~/.local/share/gdu/snapshots/*.parquet'))
ORDER BY dir_total_dsize DESC LIMIT 20;

-- How one folder has grown across every snapshot (the point of taking snapshots)
SELECT scan_ts, dir_total_dsize
FROM read_parquet('~/.local/share/gdu/snapshots/*.parquet')
WHERE is_dir AND path = '/Users/me/Downloads'
ORDER BY scan_ts;
```

## New commands, flags & config keys

| Command | Meaning |
|---|---|
| `gdu snapshots [list] [file]` | List the archive's (or one file's) snapshots and exit. Aliases: `snaps`, `ls`. |
| `gdu snapshots compact [--dry-run]` | Merge closed months into monthly files (verified, then prune). `--dry-run` plans only. |

| Flag | Config key (yaml) | Default | Meaning |
|---|---|---|---|
| `--output-format <json\|parquet>` | `output-format` | inferred from the `-o` extension | Export format. `.parquet` ⇒ Parquet. |
| `--export-threshold <size>` | `export-threshold` | `0` (keep everything) | Bucket sub-threshold objects into `<smaller objects>`. Binary units (`10M`, `500K`). |
| `--save-snapshots <auto\|always\|never>` | `save-snapshots` | `auto` | When to save each completed scan of a chosen root. `auto` saves interactive (TUI) scans only; `always` saves in every mode (forces the full-tree analyzer non-interactively); `never` disables saving. `r` refreshes and go-live spot-rescans never save. |
| `--snapshots-dir <path>` | `snapshots-dir` | `$XDG_DATA_HOME/gdu/snapshots` → `~/.local/share/gdu/snapshots` | Directory for saved snapshots (the archive). |
| `--snapshot <sel>` | — (CLI only) | `latest` | Which snapshot to load: with `-f`, from that file; without `-f`, from the archive for the scanned path. `latest`, `earliest`, or a local-time prefix. |
| `--snapshot-root <path>` | — | (none) | Restrict `--snapshot` selection to this exact scan root. |
| `--baseline <sel\|file>` | — | (none) | Interactive growth-diff baseline: a snapshot file, or a selector against the archive's roots covering the scanned path. |
| `--baseline-root <path>` | — | (none) | Restrict a `--baseline` selector to this exact scan root. |
| `--no-auto-compact` | `no-auto-compact` | `false` (compaction on) | Don't compact closed months after a snapshot is saved. |
| `--owner <user>` | `owner` | (none) | Make written output owned by `<user>` (resolves their home + `chown`s output). For scheduled root scans. |
| `--launcher` / `--launcher=false` | `launcher` | `true` | Open the interactive launcher (folder, its disk, other disks, snapshots) instead of scanning immediately. `false` restores upstream scan-immediately. Skipped by `-f`, `--snapshot`, `--read-from-storage`, `--db`, and non-interactive mode. |

The existing `-f` / `--input-file` flag is **extended** to load `.parquet` snapshots as well as JSON
(format auto-detected); interactive imports open as read-only Views (as does `--read-from-storage`).
Config keys can also live in gdu's config file — e.g. to make every scheduled scan self-archive, put
in `~/.config/gdu/gdu.yaml`:

```yaml
save-snapshots: always
export-threshold: 10M
```

See [configuration.md](configuration.md) for the full list of config keys.

## Further reading
- [docs/DESIGN.md](docs/DESIGN.md) — design rationale: constraints, rejected alternatives, invariants, upstream divergences, future work.
- [docs/scheduling.md](docs/scheduling.md) — scheduling periodic scans on macOS and Linux.
- [CLAUDE.md](CLAUDE.md) — how the codebase is built and changed.
