# Design rationale — the snapshot-history fork

This fork adds **snapshot history** to gdu: every completed scan of a deliberately chosen root is
archived as a compact Parquet snapshot, and the TUI can diff against, step through, and open that
history. Upstream gdu answers *"what's eating my disk right now?"*; the fork adds *"what grew?"*,
*"what did it look like before?"*, and *"when did this happen?"*.

This document is the **decision record**: why the fork is built the way it is — the constraints,
the rejected alternatives, the invariants that must hold, and the deliberate divergences from
upstream. It is neither a feature reference nor a build guide:

- [FORK.md](../FORK.md) — user-facing overview: journeys, vocabulary, flag tables, the snapshot schema.
- [CLAUDE.md](../CLAUDE.md) — how the code is built and changed: architecture, conventions, gotchas.
- [scheduling.md](scheduling.md) — operating scheduled scans (cron / systemd / launchd, macOS Full Disk Access).
- **This file** — the *why*, plus the vetted future-work list (§11).

It condenses the development-era plan documents; the full phase-by-phase history survives in the
pre-squash git history (tag `archive/pre-squash`).

## 1. The founding constraint: pure Go, no cgo

gdu ships `CGO_ENABLED=0`, fully static binaries across a wide GOOS/GOARCH matrix. Every fork
decision holds that line.

- Parquet I/O uses the pure-Go **`github.com/parquet-go/parquet-go`**. The DuckDB Go driver
  (`marcboeker/go-duckdb`) was evaluated and **rejected**: it requires `CGO_ENABLED=1`, ships
  prebuilt libraries for only a few platforms, dropped FreeBSD support in v2, and adds tens of MB —
  it would break the static-build promise and the cross-compile matrix.
- **DuckDB stays an external tool** the user points at the `.parquet` files. Corollary: the archive
  is open data and **SQL is the scripting interface** — which is why there is no `gdu snapshots
  diff` or `--json` listing; a DuckDB one-liner over the archive does both better than a bespoke
  re-implementation could.
- Accepted cost: parquet-go's pure-Go transitive dependencies (brotli, lz4, go-geom, bitpack,
  jsonlite) grow the binary noticeably (~38 MB at adoption). Worth it for the constraint.

## 2. Memory model: never allocate the peak

The governing fact is macOS-specific: **`debug.FreeOSMemory()` does not lower RSS on macOS**
(`MADV_FREE` leaves freed pages resident until memory pressure). A transient allocation spike is
therefore *permanent* RSS for the session — the only real fix is **not allocating the peak**, not
freeing it afterwards. (An early "scavenge after read/write" plan died on this fact.)

Evidence, measured against a real 6.17 M-row / 150 MB snapshot: whole-file `parquet.Read[Row]` on
import peaked at **12.5 GB** (streaming replacement: 1.8 GB); in a controlled probe, reading a
0.6 MiB snapshot peaked at 712 MiB heap. Hence, everywhere:

- **Writes stream.** `WriteTree` buffers 64 K rows at a time, sorts each chunk **as plain structs**
  by `(path, scan_ts)`, and flushes it as one row group (row groups capped at 128 K rows) that
  *declares* its sort order in metadata. A globally-sorted file is deliberately **not** produced:
  readers rebuild the tree by path in any order, and compaction's merge consumes *row groups* —
  per-row-group order is all it needs. Measured cost of the chunk sort: 133 MB / 1.6 s per 1 M rows
  vs 113 MB / 1.2 s unsorted — near zero.
- **The `SortingWriter` landmine.** parquet-go's `NewSortingWriter` (globally sorted output) was
  measured and rejected: **604 MB / 6.0 s per 1 M rows — ~5× both axes** (temp-buffer write, reopen,
  k-way merge, recompress, per-value boxing). Worse, in v0.30.1 its default in-memory sorting pool
  **corrupts large merges**: `memory.Buffer.Read` legally short-reads at its 32 KiB chunk boundary,
  `readerAt.ReadAt` forwards the short read — violating the `io.ReaderAt` contract — and truncates
  the temp buffer's own metadata (reproduced at ≥ ~500 K rows as random-looking thrift decode
  errors). If it is ever genuinely needed, pass `SortingBuffers(NewFileBufferPool(...))`. The
  standing rule: the ban is on *whole-file* `[]Row` buffering and *whole-file* sorts;
  chunk-sort-per-row-group is the sanctioned mechanism.
- **Reads stream.** `ReadTree` iterates `parquet.NewGenericReader` in 8 K-row batches and builds the
  tree incrementally through a path→node get-or-create map (which also handles child-before-parent
  arrival); `report.ReadAnalysis` streams straight from the `*os.File` (format sniffed via `ReadAt`
  on the `PAR1` magic) instead of buffering the file.
- **The flatten allocates lazily.** `fs.SortByNone` iterates directories without the defensive
  sorted copy, and child paths are built only for emitted rows — together ~72 % less flatten
  garbage. `runtime.GC()` runs *before* an auto-save so the write reuses freed scan garbage instead
  of raising the (macOS-sticky) high-water mark. Net effect: the save adds ~0 to peak RSS on a
  6 M-node tree.

## 3. Snapshot identity & the on-disk format

- **A snapshot's identity is the `(host, scan_root, scan_ts)` tuple** — never `scan_ts` alone. A
  compacted file holds many snapshots, and two roots can complete a scan in the same millisecond;
  selection, timeline folding, and baseline marking all key on the full tuple.
- **Why the columns say `scan_*`, not `snapshot_*`**: they name facts about the *scan event* that
  produced the snapshot (the root that was walked, the instant it completed), which is
  glossary-correct — a deliberate decision, not drift (`snapshot_ts` would be less precise, bare
  `ts` less self-describing).
- **Identity columns** `host` / `username` / `sudo_user`: a sudo scan has *two* identities worth
  keeping — the effective user (`root`) and the invoking human — and `host` matters once several
  machines' snapshots pool in one archive. They are stamped per-row because dictionary encoding
  makes that ~free, and the flat schema already repeats `scan_root`/`scan_ts` on every row.
- **Directory rows** carry `asize = dsize = 0` with recursive totals in `dir_total_*` — gdu has no
  per-inode directory size. Summing `dsize` over non-directory rows reproduces the scan total
  (which the statistics tier below exploits).
- **Footer manifest.** Every write stamps footer key-value metadata `gdu.format = 2` and
  `gdu.snapshots` — a JSON array of per-snapshot identity + row count + total + threshold +
  read-error count. `err_count` (directories that could not be read, counted at write time) powers
  the launcher's evidence-based sudo tip and survives compaction because manifests merge. Format 1→2
  was a metadata-vocabulary bump only (`gdu.scans` → `gdu.snapshots`); the column layout never
  changed. A file stamped with a *newer* `gdu.format` is never merged or deleted by an older binary.
- **Three-tier listing**, in increasing cost:
  1. the **manifest** — one footer read, no row decode;
  2. **column statistics** — also footer-only: a single-snapshot file has `scan_root`/`scan_ts`/
     `host` each single-valued in the per-row-group min/max stats, and `max(dir_total_dsize)` *is*
     the scan total (an ancestor's total is never smaller than a descendant's), so identity, row
     count, and size need **zero data-page reads**;
  3. a **projection** of the cheap columns, grouped by identity — only for manifest-less
     multi-snapshot files.
  Tiers 2–3 are the **foreign-file path** — DuckDB-rewritten or externally-produced Parquet stays
  listable — deliberately *not* framed as legacy compatibility.
- **Time.** The `scan_ts` column is UTC and timezone-aware (`timestamp(ms)`, → DuckDB
  `TIMESTAMPTZ`); it records scan *completion*. Everything human-facing — filenames, month
  bucketing, selectors, display — uses **local time**. Filenames
  (`snapshot_<YYYYMMDDTHHMMSS>_<rootslug>.parquet`) lead with a fixed-width local timestamp so
  lexical order stays chronological; the root slug is cosmetic (lower-cased, non-alphanumeric runs
  collapsed to `_`, capped at 60 chars; collisions suffixed `_1`, `_2`, …) — the `scan_root`
  *column* is the source of truth.

## 4. Threshold rollup

`--export-threshold` buckets each directory's sub-threshold children into one synthetic
`<smaller objects>` row. **One decision core feeds every export format** (JSON and Parquet apply
identical significance rules):

- A file is significant iff its disk usage ≥ threshold; a directory iff its *recursive* usage ≥
  threshold. A sub-threshold directory collapses its entire subtree into the parent's bucket —
  safe, because a child's recursive usage can never exceed its parent's. Threshold `0` keeps
  everything.
- **Directory totals are preserved for free**: replacing N children with one node carrying their
  summed sizes leaves every ancestor's recursive totals untouched. A rollup row also carries the
  represented file/folder counts, so a directory's surviving children still sum to its recursive
  totals.
- Implementation is a **recursive DFS over the in-memory `fs.Item` tree** — gdu already holds the
  whole tree at export time, so the streaming-stack approach of the original external script is
  unnecessary. **One traversal, two adapters**: the JSON adapter builds a pruned tree for the
  existing encoder; the Parquet adapter emits flat rows directly during the walk — because a single
  `<smaller objects>` `File` node cannot carry represented-counts without inventing a bespoke
  `fs.Item` type.
- **Defaults**: the flag defaults to `0` so explicit `-o` output stays byte-identical to upstream;
  **auto-saved snapshots substitute 10 MiB** — the archive exists for trend analysis, and the
  threshold is what keeps a whole-disk snapshot at tens of thousands of rows instead of millions.
- Export/auto-save force the full-tree analyzer in non-interactive mode (the default
  `TopDirAnalyzer` is shallow); the memory cost is documented rather than hidden.

## 5. Archive lifecycle: recording, compaction, auto-compaction

### Recording policy

**A snapshot records the completed scan of a deliberately chosen root** (a launcher selection or a
CLI path). `r`-refreshes and go-live spot-rescans are *transient* and never save; quitting mid-scan
confirms and discards. Rationale: the archive should hold comparable, intentional observations —
not every incidental rescan.

- `--save-snapshots` is a tri-state: **`auto`** (default) saves interactive scans only — the TUI
  already holds the full tree, so saving is free, while piped runs shouldn't leave artifacts or
  give up `TopDirAnalyzer`'s constant memory; **`always`** extends to non-interactive scans (what
  scheduled units use); **`never`** disables. The save that *creates* the archive directory
  announces itself once (stderr line / transient TUI notice) — zero-config recording must never be
  a silent surprise.
- The save hook lives at **each UI's scan-completion point, not the app level**: the TUI scans
  asynchronously and needs its event loop running, so there is no app-level moment where the tree
  is ready before the UI starts. The configuration rides on `common.UI`, kept free of the
  `parquet` import (avoiding a `common→parquet→analyze→common` cycle).

### Compaction

Daily snapshots accrete files; `gdu snapshots compact` merges each **closed** local-time month —
grouped by `(host, scan_root, month)` — into one
`monthly_<yyyy-mm>_<rootslug>[_<hostslug>].parquet`. Compaction is **lossless repacking**: every
snapshot survives row-for-row (lossy retention is future work, §11.3, and stays a separate verb).

- **Sort order `(path, scan_ts)` — locked.** Each path's rows across the month sit adjacent, so
  dictionary/RLE on `path` plus near-identical sizes across scans compress superbly — the measured
  win compaction exists for — and per-row-group `path` min/max statistics let DuckDB prune subtree
  queries. Rejected: `(scan_ts, path)` — per-snapshot extraction would be faster, but the
  cross-scan similarity scatters into separate row groups where Parquet can't exploit it, and
  archival compactness is the point. Accepted cost: extracting one snapshot decodes the whole file
  (streamed; roughly a second per few million rows).
- **Engine**: a streaming `parquet.MergeRowGroups` k-way merge with the explicit `Row` target
  schema (folds narrower legacy layouts in; pins column order). Inputs must declare `(path,
  scan_ts)` sorting; unsorted/foreign inputs first get a chunk-sorted rewrite (the same 64 K
  mechanism as §2 — never `SortingWriter`). Peak memory ≈ the open inputs' page buffers,
  independent of total row count.
- **Whole-file participation**: a file joins a run iff *all* its snapshots are in closed months
  *and* share one `(host, root, month)` group — a file is never partially consumed.
  Delete-without-merge happens only for files whose snapshots are **exact** duplicates (identity
  *and* rows/total/threshold) of covered ones, and only after row-verifying the covering monthly.
  Corrupt, foreign, multi-group, or newer-format files are skipped with a warning, never deleted.
- **The safety sequence — never weaken, always in order**:
  1. lockfile (`.gdu-compact.lock`, pid+host+timestamp; stale locks reclaimed by atomic rename —
     no remove/create TOCTOU),
  2. write `monthly_….parquet.tmp` in the archive dir (same filesystem ⇒ atomic rename),
  3. **row-verify the tmp against the input manifests** — scan set, per-scan row counts, per-scan
     totals; the manifest is the claim, the rows are the evidence (verification recomputes from the
     output's *rows*, bypassing its just-written manifest),
  4. atomic rename into place, then chown to the invoking user (§6),
  5. **only then** delete sources.
  Any interruption anywhere loses nothing: a leftover daily re-merges as a no-op (idempotent),
  stale `.tmp` files are cleaned at the next run under the lock, and a source that can't be deleted
  (e.g. Windows open-file locks) is logged and retried next run.
- **Closed months only** (strictly before the current local month) makes auto-compaction race-free
  against saves *by construction*: `scan_ts` is completion time, so a scan straddling month-end
  lands in the new, open month — and saves never take the lock.
- Accepted limitations, revisit only if they bite: merge fan-in equals the month's total row-group
  count (fine for thresholded archives; a threshold-0 archive of multi-million-row dailies would
  want a bounded multi-pass merge); a machine timezone change can leave an existing monthly
  spanning two local months (skipped with a warning thereafter); inputs *declaring* sorted order
  are trusted (a foreign file lying about its order yields a mis-sorted but still loadable, still
  row-verified monthly).

### Auto-compaction

**Default on** whenever a snapshot was just saved; `--no-auto-compact` opts out (upstream's
`--no-X` idiom). Defaults-on is justified by the safety ordering: interrupting is always safe, a
held lock is a silent skip (one compactor per archive), and at most one auto-run happens per
process. `NeedsCompaction` is a filename-only predicate (one `ReadDir`) — a cheap *hint*; the real
run re-plans from footers.

- **TUI**: a background goroutine with a footer indicator and a quit-time modal (wait / abort).
  Every quit path — `q`/`Q` *and* SIGINT/SIGTERM — funnels through one `sync.Once` that cancels and
  *waits* for teardown before `app.Stop()`: a goroutine's final `QueueUpdateDraw` after the event
  loop stops would block forever (it waits on a done channel a stopped loop never drains).
- **Non-interactive**: runs inline *after* the report is printed (a large merge must not stall
  piped output); a transient stderr spinner and Ctrl-C cancellation appear only on a progress TTY —
  piped runs stay byte-clean. Cancellation is keyed off `ctx.Err()`, not the returned error,
  because a Ctrl-C during the final group surfaces only in the result.
- Context cancellation threads through the engine (between groups, per input open, per rewrite
  batch, per copied row batch); aborting mid-copy discards the tmp and keeps every source.

## 6. Running elevated: ownership hand-back & restart-elevated

Whole-disk scans usually need root, but the artifacts must belong to the human.

- **Under sudo**, gdu resolves the invoking user from `SUDO_USER`/`SUDO_UID`/`SUDO_GID`, defaults
  the archive into *their* `~/.local/share/gdu/snapshots` (an elevated process cannot know their
  `XDG_DATA_HOME` environment — deliberately not consulted), and **chowns every written file
  back**: saved snapshots (directory and file, including created XDG parents), `-o` exports, TUI
  exports, and compacted monthlies.
- The **numeric** `SUDO_UID`/`SUDO_GID` are preferred for `os.Chown` — no NSS lookup, which matters
  under `CGO_ENABLED=0` for LDAP/AD-backed users; `user.Lookup` supplies only the home directory.
  Everything is gated on `euid == 0`, treats `SUDO_USER=root` as "no real user", and is
  best-effort by design (failures log; they never abort a scan).
- **Scheduled root jobs** (cron/systemd/launchd) have no `SUDO_USER`, so the hand-back cannot fire
  on its own. `--owner <user>` requests it explicitly — implemented as `ApplyOwnerOverride` setting
  the `SUDO_*` environment, so the *entire* existing chown path is reused with no second mechanism.
  Operational details, including the macOS Full-Disk-Access caveats, live in
  [scheduling.md](scheduling.md).
- **Restart-elevated** (`R` at the launcher) replaces the process image: `app.Suspend` (the same
  terminal-restore path shell-spawn and Ctrl-Z already use) and then `syscall.Exec` of
  `sudo -- <self> <original args>`. Exec-over-fork because the pre-sudo instance has nothing left
  to do — "un-resumable across exec" is moot when you never resume. **No shell is involved**, so
  arguments need no quoting and there is no injection surface; `<self>` is `os.Executable()`
  (absolute — immune to sudo's `secure_path`). The resolved `--config-file` is appended when a
  config was loaded and not already named on the command line: sudo's environment reset would
  otherwise make the root instance resolve *root's* config. Exec failure (no `sudo`, not a sudoer)
  resumes the TUI with an error; Windows has a stub that the non-root-euid gate makes unreachable.
- **Prompt policy: passive by default, forced only for `/`.** A passive tip (citing recorded
  read-error counts when the archive has them) advertises `R` on non-root Unix. One
  forced-but-cancelable interstitial fires only when the chosen scan root is `/` — the whole-root
  scan where permission gaps are near-certain. Its default button is **Scan anyway**: with image
  replacement, a stray Enter on *Restart* followed by Ctrl-C at the password prompt makes gdu
  vanish — encouragement comes from the wording, never from a dangerous default. (The manual `R`
  modal defaults to *Restart*; the user asked for it.) On macOS, bare `gdu` pre-selects the folder
  row — user data lives on the hidden `/System/Volumes/Data` — so the interstitial fires only on a
  deliberate `/` scan.
- **Deliberate boundary**: elevation is offered at the launcher only. Re-offering from the
  post-scan results view ("couldn't read N folders — restart elevated?") and carrying the selected
  root through the restart were designed and consciously deferred (§11.2).

## 7. CLI surface & vocabulary

- The **glossary is normative** ([FORK.md](../FORK.md) carries it): *snapshot* = the dated record;
  *scan* = the act that produces one; *snapshot file*; *archive*; *baseline*; *View*/*timeline*.
  Two hard rules: "snapshot" is never a verb, "scan" is never the stored record. The whole surface
  was renamed onto this glossary in one **clean break** — no deprecated aliases, no legacy fallback
  reads. The fork had one user and one archive (migrated once, by a since-deleted tool that doubled
  as an end-to-end test of the format code); upstream, if it ever takes this work, should receive
  the final names, not a compatibility shim.
- **`gdu snapshots` subcommand** (alias `snaps`; `list`/`ls`; `compact --dry-run`): standalone
  verbs accumulate as subcommands rather than boolean flags (upstream precedent: `gdu completion`);
  selection, recording, and diffing stay *flags* because they modify a scan/browse run. The alias
  `snap` was rejected — snapd owns the name, `~/snap`, and `/snap`.
- **`--save-snapshots auto|always|never`**: one tri-state instead of a bool plus a would-be
  `--no-save-snapshot` double negative.
- **`--export-threshold`** (renamed from `--threshold`): now matches its yaml key and escapes the
  GNU `du --threshold` collision (which means something else there).
- **`--snapshot <sel>`**: with `-f`, selects within that file; **without `-f`, resolves against the
  archive and loads** — the previously-silent no-op given the obvious meaning, in every UI mode
  (`gdu --snapshot latest -n --top 20 /` reports from the archive without touching the disk). One
  selector grammar everywhere: `latest` | `earliest` | local-time prefix; ambiguity errors list the
  candidates in paste-ready form. Archive resolution is exact-root with covering-root *hints*;
  `--snapshot-root` / `--baseline-root` pin roots explicitly (a view selection and a baseline scope
  can legitimately differ). **`--baseline <sel|file>`** is one flag taking either a snapshot file
  or an archive selector, scoped to roots *covering* the browsed path (volume-scoped, §8).
- The selector grammar deliberately lives in **two lockstep engines** — file-scoped
  (`parquet.SelectSnapshot`) and archive-scoped (the listing selector in `report`). Unify only if
  the grammar ever grows.
- The archive lives at **`$XDG_DATA_HOME/gdu/snapshots`** on macOS and Linux alike (upstream
  already uses XDG-style config paths on both), not a bespoke dot-directory.
- **`--write-config` writes to the config path that was read** (the existing user config, else it
  creates `~/.config/gdu/gdu.yaml`) — fixing the upstream chicken-and-egg where the XDG path was
  preferred only *if it already existed*. A standalone upstream-PR candidate.
- Ordinals in listings are display-only, never selectors — compaction renumbers them.

## 8. The interactive model

One mental model: **every screen shows a View** (the live disk, or one snapshot), **optionally
against a Baseline** (a snapshot, for growth diff). Four invariants:

1. **Mutations require a live View.** No unlock toggle exists.
2. **Esc is instant and layered** — close modal → clear baseline → return to the session's starting
   view — and **Esc never scans** (it also backs out of an in-progress recording scan, like `q`).
3. Entering live via a scan is always an **explicit, accepted dialog**, never a side effect.
4. A snapshot records only the **completed scan of a deliberately chosen root** (§5).

- **Hard read-only for every non-live View — including `-f` imports and `--read-from-storage`.** A
  deliberate divergence from upstream (ncdu precedent: imports disable delete/refresh/shell);
  upstream's refresh-on-import used to *graft live data into the imported tree*, and that behavior
  is retired — a View is all one thing. Blocked mutations signpost **go live here**: an instant
  switch when an in-memory live tree *contains* the folder — actual tree membership, not path
  arithmetic, because a `/`-rooted live tree that ignored nested mounts must not claim an SD-card
  folder — else a confirmed **spot-rescan** of just that subtree (cursor kept; transient, never
  saved). `v`/`o`/`i` stay available in snapshot Views and fail soft (live paths may have changed).
- **Timeline**: the covering snapshots of the current folder ordered by `scan_ts`, with the live
  disk as the newest point. Once stepping starts, the walk **pins to the first-landed root**
  (deepest covering root wins) — no cross-root surprises mid-walk; `O` is the deliberate long jump.
  Folder path and cursor are preserved across steps; a folder absent in an older snapshot lands on
  its nearest existing ancestor with a notice. `]` past the newest point switches instantly to a
  covering in-memory live tree, else offers a rescan (explicit, sized expectation). The just-saved
  snapshot **folds into the live position** — identical data is one timeline point, not two. (The
  baseline picker still lists it: a Δ=0 baseline is a legitimate pick; the fold is a timeline rule,
  not a picker rule.)
- **Scan-wait time travel**: while a chosen scan runs, the progress screen *is* the timeline's live
  position and the past stays fully walkable (a footer `scanning…` indicator rides along);
  completion never steals focus — a footer flash announces it. While scanning: no `r`, no
  mutations, no concurrent spot-rescan; quitting confirms and discards.
- **Growth diff**: the baseline is an **overlay, not a delta tree** — the current tree stays a
  normal gdu tree; each row is annotated through a path→size lookup built from the baseline. The
  join key is the **absolute path** (the writer builds absolute child paths; live `GetPath()` is
  absolute). The diff is **disk-usage-based** — no `dir_total_asize` exists, so directory growth
  cannot track apparent size. Markers encode *certainty*: "new" is claimed only when the baseline
  had threshold 0 (it provably didn't exist), "approximate" when the baseline was thresholded,
  "uncovered" outside the baseline's root; removed entries render as synthetic struck-through rows
  carrying their "then" size, and the footer reconciles (`+grown · −shrunk · −removed · net`) so
  the numbers add up even when removed rows scroll off. Growth sort lives in the TUI sort path, not
  `fs.SortBy` — `fs` stays baseline-unaware. The three hues form a CVD-safe triad and every state
  also has a glyph, so `--no-color` loses nothing.
- **Covering is volume-scoped.** A snapshot counts for a folder iff its root covers the folder
  *and* lies at-or-below the folder's most-specific mount point (longest path-prefix over the
  *unfiltered* mount list — deliberately not `statfs`, whose macOS firmlink answer
  `/System/Volumes/Data` is path-incompatible with snapshot roots). **One shared predicate** serves
  the launcher's row mapping, the `S` picker, timeline membership, and CLI `--baseline` scoping;
  with no mount information it degrades to plain path-covering. Strict rather than evidence-based:
  a plain `/` scan may genuinely contain another volume's folder, but the archive cannot
  distinguish a nested-mount-ignoring scan from a crossing one — **under-claim rather than
  mislead**. (Recording ignore-provenance in the manifest is the future fix if this conservatism
  ever bites.) Field trigger: on an SD-card folder, two-thirds of baseline-picker rows were `/`
  snapshots, each resolving to `—` after a wasted read. Escape hatches: the `O` picker (all roots)
  and `--baseline-root`.
- **Launcher**: the interactive front door (bare `gdu`, `gdu <path>`, interactive `-d`) — a root is
  *chosen deliberately*, which is exactly what makes zero-config recording meaningful (§5). It
  renders **through the upstream device table** — a shared row renderer with the classic `-d` page
  so the two can never drift (a first custom-list version regressed upstream's headers, coloring,
  and column adaptation, and was replaced by convergence rather than another custom layout). Folder
  row pinned first; its own disk pinned directly beneath (choosing it scans the whole disk but
  lands the view at the folder); `Scan another folder...` pinned last. The snapshot column appears
  only when covering history exists (progressive disclosure — a first run shows no history chrome
  at all); `s` opens a row's latest snapshot *without scanning*, `S` picks from a list. On macOS,
  `/System/Volumes/*` are hidden **from the launcher display only** — nested-mount ignore lists are
  always computed from the *unfiltered* mounts, because a filtered list would double-count
  `/System/Volumes/Data` when scanning `/`. Disk rows map snapshots by exact `scan_root == mount`;
  the folder row by the volume-scoped covering rule. `launcher: false` restores scan-immediately.
- **Host is shown only when foreign** (exact match via one shared hostname helper used by the write
  side, compaction's lock-owner check, and every listing surface): a hostname on every row is noise
  when ~all history is local. Accepted: macOS `.local` DHCP drift makes a renamed machine's older
  history read as foreign — informative, not wrong.
- **Pickers are one component in three configurations** (Baseline / Open / startup-file), styled
  with the device table's palette rather than a second color system. The hint line always shows the
  equivalent CLI invocation — the interactive flow *teaches* the scriptable one. Baseline-picker
  reads are engineered: per-folder "then" sizes come from a **projected column read** (path + sizes
  only, no tree build), **row groups pruned** by `path` min/max statistics (path-sorted files put a
  target in a short contiguous run of groups), filled **lazily off the event loop** — a deep folder
  under `/` went ~92 s → ~3.6 s → ~1.3 s across those three layers.
- **Testing note**: the event-loop deadlock class — tview's `QueueUpdateDraw` self-deadlocks when
  called *from* the event loop, and a goroutine's final `QueueUpdateDraw` after `app.Stop()` blocks
  forever — is invisible to the mocked test app. Acceptance for interactive work is therefore
  **pty-driven walks against the built binary** ([CLAUDE.md](../CLAUDE.md) has the how).

## 9. Deliberate divergences from upstream

| Divergence | Upstream behavior | Why |
|---|---|---|
| `-f` imports and `--read-from-storage` open **read-only** Views with a go-live flow | Imports are mutable; `r` grafts live data into the imported tree | ncdu precedent; a View is one thing; the guided go-live is the safe path back (§8) |
| **Launcher** front door for bare `gdu` / `gdu <path>` / interactive `-d`; left-arrow at a live top returns to it | Bare `gdu` scans cwd immediately; `-d` is a separate device page | Deliberate root choice makes recording meaningful; one screen absorbs the device page; `launcher: false` restores upstream behavior |
| `s`/`S` on the launcher are snapshot keys; `n` toggles that screen's sort | `s` sorts by size | History affordances win on the one screen that lists locations; classic pages keep upstream keys |
| Layered Esc that also backs out of scans | Esc mostly inert | One consistent "step back" that never scans |
| Completed interactive scans **record by default** (`auto`), announced once | No persistence | The fork's point; `never` opts out; non-interactive stays opt-in so pipelines never grow artifacts |
| Auto-compaction on by default after saves | n/a | Safe by construction (verify-before-delete, lock-skip, closed months only) |
| macOS `/System/Volumes/*` hidden in the launcher display | `-d` lists them | Noise for humans; classic/piped `-d` untouched; ignore logic always uses unfiltered mounts |
| `--write-config` writes to the read path | Always writes `$HOME/.gdu.yaml` | Fixes the XDG chicken-and-egg; upstream-PR candidate |
| Release automation: goreleaser draft releases (darwin/linux, unversioned archive names); upstream's docker/winget workflows removed | gox builds; docker/winget publish to upstream's namespaces | The fork cannot publish to `ghcr.io/dundee` or winget `dundee.gdu`; stable `/releases/latest/download/` URLs are the distribution channel (module path is unchanged, so `go install` of the fork is not supported) |

**Upstreamable slices.** The clean history is ordered by upstreamability: the `--write-config` fix
(cherry-pickable as-is) → the Parquet snapshot format (structurally parallel to the existing
ncdu-JSON export/import — the plausible feature PR) → the archive lifecycle (changes gdu's
character: on-disk state, a subcommand, policy defaults) → the TUI history model (the fork's
identity). The latter two are expected to stay fork-only.

## 10. Future directions

Everything below is **designed but not built**. All other ideas that came up during development —
free-space/mount-capacity tracking, a scriptable `snapshots diff` / `--json` listing (SQL over the
open archive covers both, §1), growth sparklines and history charts, ordinal selectors,
covering-root auto-descend, assorted internal-polish advisories — were **dropped, not deferred**;
consult the pre-squash history tag if archaeology is ever needed.

### 10.1 Two-stage disk scan — *planned next; very likely*

**Problem.** Choosing a disk in the launcher blocks on the whole-disk scan before anything is
browsable. **Idea:** run two ordinary, complete scans — **stage 1** scans just the *landing folder*
(fast, blocking, `transient` → never recorded) and hands it over immediately; **stage 2** rescans
the **whole disk** in the background and, on completion, **wholesale-swaps** the full tree in
(unlocking navigation upward) and records the disk snapshot. The landing folder is deliberately
scanned twice — the waste that buys out all graft/stitch complexity. Assessed **moderate, not a
rewrite** (~days): the atomic swap with cursor preservation (`applyView`), the land-at-subpath
positioning, transient save-gating, and the background-goroutine/footer/quit-modal pattern (from
auto-compaction) all exist.

**Locked decisions:**

- **Trigger is a general predicate**, not a launcher special case: fire iff the scan *would record*
  (deliberate root, not transient) ∧ the root is a device **mount point** ∧ the view lands
  **strictly deeper** than the root ∧ interactive and enabled. Today only the launcher's pinned
  own-disk row qualifies; any future mount-with-deeper-landing flow inherits it for free.
- **Wasteful wholesale swap**: stage 2 re-scans everything and swaps via `applyView`; grafting
  stage 1 into the disk tree (a hard-link/stats seam) is rejected.
- **Read-only while the background scan runs**: browse freely, but `d`/`e`/`r` and go-live show a
  brief "full disk still loading…" notice; no second user-startable scan.
- **On by default**, `two-stage-scan` yaml key + `--no-two-stage-scan`; skipped non-interactively
  and for `-f` / `--snapshot` / `--read-from-storage` / `--db`.

**Invariants & constraints from the planning round** (the re-audit list for whoever builds it):

- Stage 2 must run on its **own** `analyze.CreateAnalyzer()` instance — never the shared
  `ui.Analyzer`, whose `ResetProgress`/`Init` swaps channels under running goroutines. Refined
  rule: *only `ui.Analyzer` is single-threaded and user-startable; stage 2 is isolated and the UI
  is read-only while it runs.*
- This deliberately **relaxes the established invariant** "while a scan runs there is no live tree
  to stand on" — stage 1's tree *is* live — so every mutation / go-live / timeline / quit guard
  built on that assumption must be re-validated (the planned shape: one `bgScanBlocking()` guard).
- **No analyzer cancellation exists** (`common.Analyzer` has no context/Stop): "abort" is
  detach-and-discard (a flag checked before the save and before the swap); real cancellation is an
  interface-wide change, explicitly out of scope.
- **Only stage 2 records**, and nothing may fold into the timeline before its snapshot exists.
- Quit during the background scan confirms (wait-and-save vs quit-and-discard) and must route
  through the post-`Stop` teardown discipline (§8's deadlock class).
- Memory ≈ the disk tree plus the small stage-1 subtree (2× only in the degenerate landing≈disk
  case — accepted); the two scans are sequential, so the analyzer's concurrency semaphore never
  contends. Mocked tests cannot cover the async swap — a pty walk is required.

**Open question:** should a *folder-root* scan (`gdu <folder>`) also background-scan its disk to
unlock going up? Leaning **no** — it would record a disk snapshot the user didn't ask for; if ever
yes, that background scan should itself be transient.

**Follow-on:** shipping this lets the launcher pre-select the **disk row** on bare launches — a UX
goal deferred precisely on the first-paint cost.

### 10.2 Progressive scan engine — *optional; the maximal version*

A single priority-scheduled scan replacing the two-stage pair: focus-first traversal re-prioritized
as the user navigates, live table fill, per-directory completion markers, live totals, and mutation
guards until each subtree completes. Spike first — live re-sort jitter and feel are the open
questions. Not a prerequisite for anything: two-stage (§10.1) captures most of the value, and if
this lands it can replace two-stage's stage 1 while keeping its swap/recording shape. Also parked
here: the **post-scan restart-elevated prompt** (live read-error evidence in the results view,
completing §6's boundary).

### 10.3 Retention / pruning — *optional; needs its own design round*

Compaction is lossless repacking; retention is **lossy deletion** — kept separate operations (the
borg/restic `compact`-vs-`prune` precedent). Default forever: **keep everything** (the rollup
threshold is what keeps monthlies small). Sketch held from the compaction design so the engine
stays future-proof: the merge takes a **snapshot-set filter** (which `(host, scan_root, scan_ts)`
tuples survive — compaction passes "all", pruning passes the survivors; the seam is the group's
snapshot list plus a filtering row reader, deliberately not yet threaded through). Vocabulary:
`gdu snapshots prune` with borg-style `--keep-daily/--keep-weekly/--keep-monthly/--keep-yearly N`,
applied **per `(host, root)` series** (pruning `/` must never eat the only `/home` snapshot),
newest-in-bucket wins, `--dry-run` as the culturally expected first step. Recorded alongside, not
designed: **progressive re-thresholding** (coarsen old snapshots, e.g. 10 M → 100 M, instead of
deleting — a row transform in the same engine) and **yearly compaction** (the same engine with a
period parameter).
