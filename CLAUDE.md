# CLAUDE.md

Guidance for working in **gdu** (`go DiskUsage()`) — a fast, parallel disk usage analyzer
(TUI + CLI) written in Go. This repository is a fork of `github.com/dundee/gdu` (module path
stays `github.com/dundee/gdu/v5`) whose own product is disk usage **with history**: every
completed scan is archived as a Parquet snapshot, and the TUI diffs against, steps through, and
reopens them (time travel, growth monitoring, the launcher). Everything upstream gdu does still
works unchanged — think "faster `ncdu`, plus a timeline" — and the fork deliberately holds
upstream's engineering constraints so pieces can be contributed back.

For *user-facing* docs (flags, examples, config keys) see [README.md](README.md) and
[configuration.md](configuration.md); [FORK.md](FORK.md) is the product tour. This file is about
*how the code is built and how to change it*. How the fork tracks upstream — the sync cycle,
branch/tag naming, release/versioning policy, and the per-round decision log — lives in
[docs/UPSTREAM.md](docs/UPSTREAM.md).

## Build / run / test / lint

Toolchain: Go (go.mod requires `1.25`, [.tool-versions](.tool-versions) pins `1.26.1`, CI tests
`1.25.x` + `1.26.x`). **Everything builds with `CGO_ENABLED=0`** — pure-Go, fully static binaries. The constraint is
double load-bearing: it keeps upstream's cross-compile matrix intact *and* it is what makes the
fork's own binary releases portable.

```sh
make run            # go run ./cmd/gdu  — launch the TUI locally
make build          # build to dist/gdu (PIE, trimmed, PGO via default.pgo)
go build ./cmd/gdu  # quick local build

make test           # gotestsum ./...   (preferred)
go test ./...       # plain test run
make coverage       # race + atomic coverage -> coverage.txt

make lint           # golangci-lint run -c .golangci.yml  (pinned v2.11.2)
make gobench        # go test -bench=. ./pkg/analyze

make install-dev-dependencies   # gotestsum, gox, golangci-lint, gotraceui
```

Run a single test: `go test ./pkg/analyze/ -run TestName -v`.

## Architecture (data flows top-down)

```
cmd/gdu/main.go        cobra CLI: defines every flag, loads YAML config, builds app.App
  └─ cmd/gdu/app/app.go  App.Run(): orchestration. Picks the UI + Analyzer from Flags, wires everything
       ├─ tui/            interactive terminal UI  (tview/tcell)         — the default
       ├─ stdout/         non-interactive text output (-n, piped, etc.)
       └─ report/         export (-o JSON/Parquet) and import (-f, format auto-detected) UIs
internal/common/       shared interfaces + types used by every layer
pkg/fs/                the central `fs.Item` interface (file/dir abstraction) + sorting
pkg/analyze/           concrete File/Dir types (implement fs.Item) + all the Analyzers + rollup.go
pkg/parquet/           Parquet snapshot read/write (pure-Go parquet-go; NO cgo/DuckDB) + threshold rollup
pkg/{device,remove,path,annex,timefilter}   supporting domains
build/                 version vars injected at link time via -ldflags
```

### The two interfaces that hold the system together

1. **`fs.Item`** ([pkg/fs/file.go](pkg/fs/file.go)) — the core domain abstraction. *Everything* that
   appears in the tree is an `fs.Item`: regular `File`, `Dir`, plus `StoredDir`, `ParentDir`,
   `SimpleDir`, and archive dirs (tar/zip). When you add a feature that touches files/dirs, program
   against this interface, and remember a new implementation must satisfy the *whole* interface
   (`GetItemStats`, `UpdateStats`, `EncodeJSON`, locking helpers, etc.).
2. **`common.Analyzer`** ([internal/common/analyze.go](internal/common/analyze.go)) — a pluggable scanner.
   `app.go` selects the concrete analyzer at runtime based on flags. Concrete types assert conformance
   with `var _ common.Analyzer = (*XxxAnalyzer)(nil)`.

The `UI` interface in [cmd/gdu/app/app.go](cmd/gdu/app/app.go) is the third seam: TUI, stdout, and
export UIs all implement it, so `App.Run()` is UI-agnostic.

### Analyzers (all in `pkg/analyze`, all share `BaseAnalyzer`)

- `ParallelAnalyzer` ([parallel.go](pkg/analyze/parallel.go)) — **the default**. Recursively scans
  directories concurrently; a package-level semaphore `concurrencyLimit` (size `2*GOMAXPROCS`) caps
  goroutines. `parallel_stable.go` is a variant with stable ordering.
- `TopDirAnalyzer` ([parallel_top_dir.go](pkg/analyze/parallel_top_dir.go)) — memory-efficient: tracks
  only top-level dir totals. Used in non-interactive mode *unless* `--top`/`--depth` is set (those need
  the full tree). This is why constant memory is possible for `gdu -npc /`.
- `SequentialAnalyzer` ([sequential.go](pkg/analyze/sequential.go)) — `--sequential`, for spinning HDDs.
- DB-backed: `StoredAnalyzer` (BadgerDB, [storage.go](pkg/analyze/storage.go)/[stored.go](pkg/analyze/stored.go))
  and the SQLite analyzer ([sqlite.go](pkg/analyze/sqlite.go)) for `--db`. SQLite is `modernc.org/sqlite`
  (pure Go) — chosen specifically to keep `CGO_ENABLED=0`.
- Archive browsing: `tardir.go` / `zipdir.go` build a virtual `fs.Item` tree from archive contents.

`BaseAnalyzer` ([analyzer.go](pkg/analyze/analyzer.go)) provides progress reporting via **atomic
counters polled by a 50ms `time.Ticker`** — *not* a per-item channel (a deliberate perf choice; don't
reintroduce per-item progress channels). It also exposes the in-progress root via
`GetCurrentDir` (an atomic, set by `setCurrentDir` in each analyzer's `AnalyzeDir` —
`parallel.go`, `parallel_stable.go`, `sequential.go`) so the TUI's Tab preview can read the partial
tree mid-scan; the TUI reaches it through an optional interface assertion, so the `common.Analyzer`
interface stays untouched. Reading that live tree is race-free because `Dir.AddFile` now takes the
write lock and `Dir.updateStats` snapshots the file list under the read lock.

### Dataless (cloud placeholder) scanning

macOS marks files and directories whose contents a provider has evicted to the cloud with
`SF_DATALESS` in `st_flags`. **Every analyzer tests `dirIsDataless(path)` immediately before its
`os.ReadDir`** — the listing is the act that faults the whole subtree back through fileproviderd —
and substitutes `datalessDir(path)`: a childless leaf carrying `Flag: '~'` and `ItemCount: 1`
([pkg/analyze/dataless.go](pkg/analyze/dataless.go)). Files take the same flag from the
`os.FileInfo` the caller already holds, at **both** file paths — `setPlatformSpecificAttrs`
([dir_unix.go](pkg/analyze/dir_unix.go)) and `TopDirAnalyzer`'s own
([parallel_top_dir.go](pkg/analyze/parallel_top_dir.go), which doesn't call it, exactly as it
already handles `'H'`) — so `gdu -npc <clouddir>` shows the flag too. Evicted files are still
*counted*: they hold ~no blocks while their apparent size is real, so the totals were always honest
and the flag is informational there. **There is deliberately no opt-out knob** — a disk usage tool
must never measure a cloud by materializing it; users who want the trees gone entirely use `-I`.
The bit test lives in darwin-only files ([dataless_darwin.go](pkg/analyze/dataless_darwin.go) /
[dataless_other.go](pkg/analyze/dataless_other.go)) and must never move into `dir_unix.go`, which is
shared with netbsd/freebsd where `0x40000000` means something unrelated. Both checks sit behind
package-level `var` seams because the kernel owns the attribute and userspace cannot set it — no
fixture can be genuinely dataless, which is what makes the per-analyzer tests possible. `'~'`
renders everywhere for free through `GetFlag`; `v` on a placeholder reports it instead of opening
(an open would download it); Parquet carries it in an optional `dataless` column (additive — no
format bump; old files read as false) and it is the one *directory* flag restored on read, because
`UpdateStats` re-derives read errors from children and a placeholder has none. JSON export follows
ncdu's format, which has no field for it, so the flag is lost there.

## Parquet snapshots

gdu can export/import scans as Apache Parquet and auto-archive them for trend analysis. Design notes
(design rationale in [docs/DESIGN.md](docs/DESIGN.md)):

- **Pure-Go only — never DuckDB/cgo.** Writing uses `github.com/parquet-go/parquet-go`. The DuckDB Go
  driver requires `CGO_ENABLED=1` and would break the static cross-platform builds, so it is
  deliberately **not** used; DuckDB stays an *external* tool users run on the `.parquet` files. Do not
  add `go-duckdb` or any cgo dependency.
- **Threshold rollup** ([pkg/analyze/rollup.go](pkg/analyze/rollup.go)): `--export-threshold` (yaml key
  `export-threshold`) collapses files/dirs whose disk usage is below the threshold into a synthetic `<smaller objects>`
  `File`, preserving each directory's exact recursive totals. `analyze.Rollup` builds a pruned tree for
  the JSON encoder; `pkg/parquet` does its own threshold-aware flatten for the flat row schema.
  **`--export-threshold` is *unset* by default, and unset means a different thing per output**
  (`resolveThresholds` in [cmd/gdu/app/app.go](cmd/gdu/app/app.go)): `-o`/JSON exports keep everything
  (0, byte-identical to upstream), while **auto-saved snapshots substitute `defaultSnapshotThreshold`
  (10 MiB)** — deliberately, so a daily whole-disk archive is ~1.5 MB instead of ~60 MB and sub-10-MiB
  objects (inconsequential for growth monitoring) collapse while every directory's recursive totals
  stay exact. An **explicit** value — including `0` (keep everything) — applies verbatim to *both*.
  Absence is the only unset signal: the flag default is `""` and the yaml key is `omitempty`, so a
  written config omits it when unset and an explicit `0` is never coerced back to the 10 MiB default
  (the old `threshold <= 0` trap). Tests wanting deep rows from a small fixture pass a positive value;
  `--export-threshold 1` is enough. **Scope filters vs. output format**: Parquet output
  rejects `--top`/`--depth`/`--summarize` at startup (validated once in `App.createUI`) — a snapshot's
  manifest claims a complete scan, so a filtered file must never masquerade as one; `--export-threshold`
  is the only sanctioned lossy knob for snapshots (totals-preserving, recorded in the manifest). JSON
  export *does* honor those filters, and the order is **filters first, then rollup on the filtered
  tree** (`report/export.go`'s `exportDir`).
- **Flows**: `-o x.parquet` / `--output-format parquet` export ([report/export.go](report/export.go)),
  `-f x.parquet` import dispatched by the `PAR1` magic in [report/import.go](report/import.go),
  `--save-snapshots` auto-archive to `$XDG_DATA_HOME/gdu/snapshots/snapshot_<ts>_<root>.parquet`
  (→ `~/.local/share/gdu/snapshots`; under sudo/`--owner` the *invoking* user's `~/.local/share`,
  their env not consulted), `--snapshot <sel>` archive resolution
  ([cmd/gdu/app/snapshots.go](cmd/gdu/app/snapshots.go) + `report.ResolveArchiveSnapshot`), and the
  `gdu snapshots` subcommand (alias `snaps`; `list`/`ls`, `compact --dry-run`) whose shared flags
  (`--snapshots-dir`, `--owner`, `--max-cores`, `--config-file`, `--log-file`) are *persistent* root
  flags in [main.go](cmd/gdu/main.go).
- **`--save-snapshots` is a tri-state** (`save-snapshots` yaml key): `auto` (default) saves
  interactive scans only, `always` every mode, `never` none — resolved by
  `Flags.SaveSnapshotsEnabled`. It **hooks at each UI's scan-completion point, not at the app
  level** — the TUI scans *asynchronously* (needs its event loop running for `QueueUpdateDraw`), so
  there's no app-level moment where the tree is ready before the UI starts. Config lives on
  `common.UI` (`SetSaveSnapshot`) kept free of the `parquet` import to avoid a
  `common→parquet→analyze→common` cycle. Non-interactive saving (`always`) forces
  `analyze.CreateAnalyzer()` because the default `TopDirAnalyzer` is shallow. The save that has to
  *create* the archive dir announces it once (stderr line / 2-second TUI header notice + logrus;
  `parquet.SaveSnapshot` returns `createdDir`, message from `common.SnapshotDirAnnouncement`).
- **TUI View/Baseline model** (design rationale: [docs/DESIGN.md](docs/DESIGN.md)): every screen shows a **View** (live disk or one snapshot;
  `tui.view` in [tui/view.go](tui/view.go)), optionally against a **Baseline** (growth diff).
  Mutations (`d`/`e`/`r`) require a live View; snapshot Views — including `-f` imports and
  `--read-from-storage` — are hard read-only and signpost a *go live here* flow (instant switch to
  a covering in-memory live tree, else a confirmed transient spot-rescan). `[`/`]` walk the
  covering-snapshot timeline ([tui/timeline.go](tui/timeline.go); pinned to one root per walk,
  live is the newest point, the just-saved snapshot *folds* into it); `{`/`}` walk the **Baseline**
  `◇` along that same pinned timeline (`{` with none set = compare vs the previous snapshot; loads
  run off the event loop behind the loading page via the shared `ensureTimelineThen`). The tree
  walk is **linear**, deliberately unlike the browser's two-cursor `{`/`}`: ◇ never rests on the
  live end or on `●`, so `}` onto `●` — or off the newest end — *clears* the comparison
  (`baseline cleared`), and `[`/`]` landing `●` on the ◇ snapshot renders an honest all-`·` zero
  (`viewing the baseline snapshot`). ◇'s position is derived from `baselineKey` by identity each
  step (timestamp insertion when it lies off the pinned root), never stored positionally. A
  baseline is auto-cleared when navigation leaves its coverage (E7: `enforceBaselineCoverage` in
  `applyView`/`fileItemSelected`, same `RootCoversWithinMount` rule and flash as the browser apply
  seam) — but only after it was seen covering (`baselineEverCovered` latch), so a never-covering
  `--baseline-root` override survives. `O`/`B` open the **unified
  snapshot browser** ([tui/browser.go](tui/browser.go); `O` with the `●` Viewing cursor focused, `B`
  with the `◇` Baseline cursor focused — one window, two doors);
  `Esc` is layered (modal → clear baseline → return view) and **never scans**. The two-slot header
  lives in [tui/header.go](tui/header.go): the roles carry **glyphs** — `●` Viewing (solid: the tree
  you stand in), `◇` Baseline (hollow: the reference you compare against), ASCII `*`/`o` under
  `--no-unicode` (same `useOldSizeBar` flag as the size bar). One shape means one role everywhere,
  including the browser's two cursors; the header stays a single-style band with no color
  tags, because the shapes are what must survive `--no-color` and its copy is full of literal
  bracket key names the tag parser would otherwise eat. **A set Baseline always renders both
  lines**, live or not — a comparison must name both sides — and `dirLabelPrefix` carries the same
  pair (`[● live] [◇ 2026-07-14 Δ]`) when `header.hidden`. **Recording policy**: only completed scans of a
  deliberately chosen root save (`scanOpts.transient` marks `r`-refreshes and spot-rescans, which
  never save). **Quit confirmation is unified and snapshot-aware** (`shouldConfirmQuit` in the
  `quitApp` chain, gated by the `no-confirm-quit` flag): quitting confirms only when work is
  genuinely at risk — a completed scan whose results were never recorded (tracked by
  `unsavedScanDuration`, bumped only when a scan finishes without saving), or an in-flight scan that
  is recording (`scanIsRecording`) or has run past `confirmQuitMinScanDuration` (3s). A recorded
  scan quits silently. The timeline stays walkable during scans (scan-wait time travel): the
  progress page is the live position, and completion never steals focus from a user browsing the
  past. **Tab** previews the partial tree found so far — page-level state (progress ↔ partial
  tree), *not* a `tui.view`; the fork's `enterPreview`/`exitPreview` render the live position
  momentarily, and `[`/`]` leave the preview and step the timeline. (This folds upstream's #593
  quit-confirm and #594 mid-scan preview into the fork's model.)
- **Two tree renderers, deliberately kept parallel — keep them aligned.** The plain view
  (`showDir`) and the compare view (`showDiffDir`, [tui/diff.go](tui/diff.go)) are **separate
  functions on purpose**: compare's row source (`buildDiffRows`, with its own Δ sort and inline
  reference-less removed rows), footer, and no-collapse rule differ enough that a single merged
  pass would trade visible duplication for hidden mode-branching, and `showDir` stays close to
  upstream's shape so syncs keep applying. They are held in step by three seams, not by a full
  merge: **shared helpers** (`setParentRow`, `accumulateBarMax`, `applyRowStyle`, and above all
  `formatFileRow` — one row formatter, the Δ field passed in), a **shared column geometry**
  (`middleWidth`/`deltaCell` mirror `formatFileRow`'s implicit widths), and **guard tests**
  (`TestCompareViewHasNormalAnatomy`, `TestCompareRemovedRowColumnAlignment`, the up-nav tests).
  So: any change to a column, row order, or navigation must be made in **both** renderers (or in the
  shared helper) and its guard test extended — and the same when pulling an upstream change that
  touches `showDir`. **No order-derived row indexing outside a renderer** — select by item/reference
  identity (`selectItemByReference`/`selectItemByName`), never by recomputing a `GetFiles` index,
  because the two renderings order rows differently. Compare deliberately **ignores `--collapse-path`**
  (deltas attach to real paths; a collapsed chain hides the levels a removal sits on), so its
  up-navigation steps the plain parent one real level at a time.
- **Snapshot browser** (`tui/browser.go` + `browser_render.go`/`browser_input.go`/`browser_doors.go`):
  one window behind every door, carrying two always-visible cursors — `●` Viewing and `◇` Baseline.
  Tree-view `O`/`B` open it (`showSnapshotBrowser`; `●`/`◇` focused respectively), the launcher's `S`
  opens it scoped to a row (no live row), and the `-f` multi-snapshot chooser is the same component
  (view-only — `cfg.viewOnly()`, i.e. no `applyBaseline` hook — plus `escQuits`, no fill). `Tab` flips focus, `[ ]` step `●` and `{ }` step `◇`
  regardless of focus (skipping each other's row — the two never collide), `Enter` applies whatever
  changed (view then baseline, chained through `openSnapshotView`'s `then`), `Esc` discards. The live
  tree is a pinned first row wired to the go-live flow; covering snapshots take both cursors, other
  roots are `●`-view-only under a divider; the just-saved snapshot folds into the live row
  (`snapshotFoldsIntoLive`). The "Δ vs ●" column fills asynchronously (`startBrowserFill`, reusing the
  generation guard) and recomputes as `●` moves. **Cursor positions are `rows` indices, but selection
  is by identity** (`rowForKey`) — the async fill must not be keyed off a positional guess, the seam
  the stage-2 bug lived on. The old single-cursor picker is gone; `pickerSizeCell`/`pickerRootCell`/
  `pickerHostCell`/`pickerDelta`/`dim` in `snapshotpicker.go` remain as shared cell helpers.
- **Launcher**: the interactive front door
  ([tui/launcher.go](tui/launcher.go)), absorbing the standalone device page and left-arrow-at-top.
  Bare `gdu` **and** `gdu <path>` **and** interactive `gdu -d` open it; `App.launcherEnabled()`
  gates it (a `launcher` yaml flag, default true) and it is skipped by
  `-f`/`--snapshot`/`--read-from-storage`/`--db`/non-interactive. **It renders through the upstream
  device table** — shared cell/color/mount-shorten helpers in [tui/devices_table.go](tui/devices_table.go)
  used by both `showDevices` (classic `-d`) and the launcher, so they never drift. The
  default-dir folder is pinned first, **its own disk pinned directly below it**, and
  `Scan another folder...` last; folder + scan-another-folder are spliced into the Mount point
  column (folder with a dim `(current folder)`/`(specified folder)` note); a Snapshot column sits
  before Mount point, shown only with mapped history (progressive disclosure). A styled header bar
  (`style.header` colors, honors `header.hidden`) tops it. Table row 0 is the header, so
  `st.rows[i]` renders at table row `i+1` — selection/activation carry that offset. `Enter` scans a
  chosen root (saves), `s` opens the row's latest snapshot, `S` opens the unified snapshot browser
  scoped to the row (no live row; both reuse `openSnapshotView`), `n` toggles the disk sort (usage-desc↔name-asc; the folder + pinned own
  disk + scan-another-folder stay fixed via `launcherDiskSpan`). Pre-selection: explicit path →
  folder row; bare/`-d` → the cwd's pinned disk. **Choosing the pinned own disk lands the view at the
  default dir, not the mount root** — `launcherRow.land`/`landPath()`, threaded as
  `scanOpts.landPath` (honored by `finishRootScan` when the scan root covers it) and as the `s`/`S`
  open's `wantPath`; the whole disk is still scanned and `scan_root` is the mount.
  **macOS `/System/Volumes/*` are hidden from the launcher display only
  (`device.HideSystemVolumes`); `ui.devices` stays UNFILTERED because snapshot row-mapping resolves a
  path to its disk through it.** (Nested-mount ignores do *not* come from `ui.devices` — see the
  scan-boundary bullet below.) Snapshot↔row mapping is
  **mount-accurate (`launcherRowMapsSnapshot`)**: disk rows match `scan_root == mount` exactly;
  the folder row matches roots between its most-specific mount (longest prefix over `ui.devices`) and
  itself — never path-covering alone. `ui.launcher` (a `*launcherState`) doubles as the async-fill
  generation guard. The **read-error count** the sudo tip cites is persisted per snapshot in the
  footer manifest (`SnapshotInfo.ErrCount`, counted at write time by `countReadErrorDirs`; rounds
  through compaction for free).
- **Scan boundary (which nested mounts a scan skips) is resolved per scan root**, at the top of
  `analyzePath` (`applyScanBoundary`, [tui/actions.go](tui/actions.go)) — not once at startup, because
  the launcher lets the user pick the root *after* startup, which is what made `no-cross: true` a
  silent no-op for launcher scans. It applies when `scanOpts.wholeDevice` (launcher disk row,
  classic `-d` `deviceItemSelected`), when `ui.noCross` (`SetNoCross` ← `Flags.NoCross`), or when
  `device.ScanRootAliasesMounts(root)` (build-tagged: darwin `/` only — firmlinks splice the data
  volume into `/`, same st_dev *and* same inode both ways, so only a path ignore can stop the double
  count). The mounts come from the **raw** `getter.GetMounts()`, never `GetDevicesInfo()` (whose
  `/dev`-name filter drops autofs, nullfs and transient Time Machine local-snapshot mounts), and land
  in their own per-scan exact-path set via `common.UI.SetNestedMountPaths` — *never* in
  `IgnoreDirPathPatterns`, which belongs to the user's `-I`. `ui.getter` is therefore always wired
  (`SetDevicesGetter` from `getOptions`), even when neither launcher nor `-d` opens. The app layer's
  `setNoCross` keeps the same rule for stdout/export (their scan root *is* the startup path) and
  **skips the TUI** so no startup-derived residue lingers in `Flags.IgnoreDirs` for the session.
- **Restart-elevated** ([tui/launcher_sudo.go](tui/launcher_sudo.go)): the sudo tip now
  advertises **`R`** (manual restart) and a **forced-but-cancelable prompt fires only when the scan
  root is `/`** — `launcherScan` gates on `isRootVolume` before `launcherRunScan` (the renamed scan
  body), showing `confirmScanElevated` (*Scan anyway* default / *Restart with sudo*); manual `R` →
  `confirmRestartElevated` (*Restart* default / *Cancel*). Both hand off via `restartElevated` →
  `ui.app.Suspend` (the same terminal-restore mechanism as shell-spawn/Ctrl-Z) → `ui.reexec` (a
  seam defaulting to `reexecSudo`, build-tagged `reexec_other.go` `syscall.Exec` / `reexec_windows.go`
  stub), replacing the image with `sudo -- <self> <os.Args[1:]>` (`buildSudoArgv`). `<self>` is
  `os.Executable()`; the resolved `--config-file` is forwarded when a config was loaded and not
  already named (`SetConfigFilePath` from `getOptions`, stat-gated) since sudo's env reset would else
  hide it. Gated to non-root Unix (`sudoTipRelevant`, euid > 0); ownership hand-back rides the existing
  `SUDO_USER` plumbing. Testable without real sudo via the `ui.reexec` spy (`buildSudoArgv` is pure).
  The *post-scan* results-view prompt is deliberately deferred (future work; see
  [docs/DESIGN.md](docs/DESIGN.md)).
- **Mount-accurate covering & picker polish**: the launcher's folder-row rule is now
  the general one — `report.RootCoversWithinMount(scanRoot, target, mount)` (root covers target ∧
  root at-or-below target's most-specific mount; `mount==""` degrades to plain path-covering) is
  shared by `launcherRowMapsSnapshot`, the browser and timeline membership (`coveringListings`), and
  CLI `--baseline`/`--snapshot` covering hints. The mount comes from `device.ForPath(devices, path)`
  (the launcher's `deviceForPath` folded into `pkg/device`); the TUI captures `ui.devices`/`ui.getter`
  on the event loop and resolves off it via `mountForTarget` (getter fallback when the launcher was
  skipped — never mutating `ui.devices`). Go-live tests **actual tree membership** (`viewContains` →
  `descendToPath`), not path arithmetic, so a `/`-rooted live tree doesn't claim an SD folder.
  **Host is foreign-only** (`common.HostnameBestEffort`/`HostIsForeign`, exact match) across the
  browser, `report.PrintSnapshots`, and `parquet.FormatSnapshotList`. The browser carries a
  **Root column** and device-table styling (blue roots/amber sizes/dim ages via the shared
  `deviceNameColor`/`deviceSizeColor` tags), and pre-positions the `◇` cursor on the active baseline
  (`ui.baselineKey`, a `parquet.SnapshotKey` set by `SetBaseline`). `--baseline-root` stays the
  deliberate cross-volume override.
- **Schema** ([pkg/parquet/schema.go](pkg/parquet/schema.go)): 22 columns, one flat row per
  file/dir/rollup; `scan_ts`/`mtime` are timezone-aware (`timestamp(millisecond)` → DuckDB
  `TIMESTAMPTZ`); directory rows carry `asize=dsize=0` with recursive totals in `dir_total_*` (gdu has
  no per-inode dir size). Scan-level identity columns `host`/`username` (effective user) /`sudo_user`
  (invoking user under sudo, nullable) are stamped on every row.
- **Parquet read/write are streaming and memory-sensitive** (a full-disk snapshot is millions of
  rows). **Crucial macOS fact:** `debug.FreeOSMemory()` does *not* lower RSS on macOS (`MADV_FREE`), so
  the rule is *don't allocate the peak*, not *free it after*. Concretely: `WriteTree` buffers
  `sortChunkRows` (64K) rows at a time, sorts each chunk **as plain structs** by `(path, scan_ts)`,
  and writes+`Flush()`es it so every sorted chunk is exactly one row group that *declares* its sort
  order — per-row-group sorting is all compaction's `MergeRowGroups` needs; global file order is
  deliberately not produced. Do NOT "upgrade" this to parquet-go's `SortingWriter`: measured ~5×
  memory and wall clock (temp-buffer/merge/recompress + value boxing), and its default in-memory
  sorting pool corrupts large merges in v0.30.1 (`readerAt.ReadAt` passes `memory.Buffer`'s legal
  32 KiB short reads through, violating `io.ReaderAt` — use `NewFileBufferPool` if you ever must).
  The ban is on *whole-file* `[]Row` buffering and whole-file sorts. The flatten iterates with
  `fs.SortByNone` (no per-dir sorted copy) and builds child paths only for emitted items; `ReadTree`
  streams batches via `parquet.NewGenericReader` building the tree incrementally; `report.ReadAnalysis`
  streams straight from the `*os.File`. `--save-snapshots` calls `runtime.GC()` before writing so the
  snapshot reuses freed scan garbage.
- **Multi-snapshot files & the footer manifest** ([pkg/parquet/manifest.go](pkg/parquet/manifest.go)):
  a snapshot's identity is the **`(host, scan_root, scan_ts)` tuple** — never compare by `scan_ts` alone
  (compacted archives may hold many snapshots, even same-instant ones). Every write stamps footer
  key-value metadata `gdu.format` = `2` and `gdu.snapshots` (JSON: per-snapshot identity + rows +
  total_dsize + threshold); `ListSnapshots` resolves in three tiers of increasing cost: footer manifest
  (one read), then **column statistics** (footer min/max — a single-snapshot file has
  `scan_root`/`scan_ts`/`host` each single-valued, and `dir_total_dsize`'s column max *is* the scan
  total, so identity + rows + size need no data-page reads), then a full column projection only for
  legacy/foreign multi-snapshot files. The reader looks up only `gdu.snapshots`; format-1 files (old
  `gdu.scans` key) and foreign Parquet fall through to the statistics/projection tiers (the
  foreign-file path). `ReadTree` loads the latest snapshot by full identity.
- **Compaction** ([pkg/parquet/compact.go](pkg/parquet/compact.go), `gdu snapshots compact`
  \[`--dry-run`\]): merges each **closed** local-time month's snapshots — grouped by
  `(host, scan_root, month)`, whole files only (a file participates iff all its scans share one
  group) — into `monthly_<yyyy-mm>_<rootslug>[_<hostslug>].parquet` via a streaming
  `parquet.MergeRowGroups` k-way merge (explicit `Row` target schema folds legacy layouts in;
  unsorted legacy inputs get a chunk-sorted rewrite first). Safety sequence, in order and always:
  lockfile (`.gdu-compact.lock`, stale-reclaim by atomic rename) → write `.tmp` → **verify the tmp's
  rows** against the input manifests (scan set + per-scan rows + totals; the manifest is the claim,
  rows are the evidence) → atomic rename → `ChownToInvoker` → only then delete sources. Never
  weaken that order. A file is deleted *without* merging only when every scan is an **exact**
  duplicate (identity and rows/total/threshold) of a covered scan, and even then the covering
  monthly is row-verified first. Skipped files (corrupt, foreign, multi-group, newer `gdu.format`)
  are never deleted. The open month is never touched, so compaction can't race `--save-snapshots`.
  **Auto-compaction** (default **on** whenever a snapshot is saved; `--no-auto-compact` opts out)
  reuses the same engine after each snapshot write: a filename-only predicate (`NeedsCompaction`) decides cheaply, at most one
  auto-run happens per process (`common.UI.ClaimAutoCompactRun`), a held lock is skipped silently
  (`ErrCompactionLocked`), and the TUI runs it on a background goroutine
  ([tui/autocompact.go](tui/autocompact.go)) with a footer indicator and a wait/abort quit modal —
  abort cancels via context (`RunCompactionContext`) and is always safe by the verify-before-delete
  ordering. The non-interactive path ([stdout/stdout.go](stdout/stdout.go) `maybeAutoCompact`) runs
  the merge inline (the report is printed first); only on a progress terminal (`ShowProgress`, i.e.
  a TTY without `--no-progress`) does it show a transient `progressOut` (stderr) spinner and wire
  Ctrl-C to cancel the context — piped/`-p` runs stay byte-clean. Cancellation is keyed off
  `ctx.Err()`, not the returned error, because a Ctrl-C in the final/only group surfaces only in the
  result, not as a top-level error.
- **Snapshot filenames use local time** with a scan-root slug
  (`snapshot_<YYYYMMDDTHHMMSS>_<root>.parquet`, collision-suffixed) — `rootSlug()` in
  [pkg/parquet/snapshot.go](pkg/parquet/snapshot.go) lower-cases the absolute root and collapses any
  non-`[a-z0-9]` run to `_` (`/`→`root`, `/Volumes/SD`→`volumes_sd`), capped at 60 chars. The slug is
  cosmetic; the lossless `scan_root` *column* is the source of truth and the fixed-width timestamp
  still leads so lexical sort stays chronological. The `scan_ts` *column* stays UTC. **sudo-safe output:** [internal/common/ownership.go](internal/common/ownership.go)
  resolves the invoking user (`SUDO_USER`/`UID`/`GID`) for the default snapshots-dir and `chown`s every
  written file back (snapshots, `-o` exports, TUI export). `pkg/parquet`→`internal/common` is acyclic.

## Patterns & conventions to follow

- **Functional options for the TUI.** `tui.Option = func(*UI)`. `app.getOptions()` translates `Flags`
  into a slice of options passed to `tui.CreateUI`. Add UI knobs as a `SetXxx` method + an option, then
  wire it in `getOptions()`.
- **Adding a config/flag is a multi-file change.** A new option means: add a field with a `yaml:"..."`
  tag to `Flags` in [app.go](cmd/gdu/app/app.go); register the cobra flag in `init()` in
  [main.go](cmd/gdu/main.go); handle it in `App.Run()`/`getOptions()`; and document it in
  [configuration.md](configuration.md), [README.md](README.md), and [gdu.1.md](gdu.1.md). The `Flags`
  struct's yaml tags *are* the config-file schema (`gdu --write-config` marshals it). After editing
  `gdu.1.md`, regenerate the committed man page with `make gdu.1` (needs pandoc) — `gdu.1` is a
  generated, tracked artifact and goes stale otherwise.
- **OS-specific code via filename suffixes / build tags.** e.g. `dir_unix.go`, `dir_linux-openbsd.go`,
  `dir_other.go`, `exec_windows.go`, `dev_linux.go`, `dev_bsd.go`, `dev_freebsd_darwin_other.go`.
  Platform attributes (disk usage vs apparent size, inode for hard links) are set through
  `setPlatformSpecificAttrs` / `setDirPlatformSpecificAttrs`. Tests follow the same convention
  (`*_linux_test.go`). On Windows/Plan9 disk usage isn't available, so `--show-apparent-size` is forced.
- **Hard links counted once.** `File.Mli` holds the inode; `alreadyCounted` flips the flag to `H` and
  zeroes the duplicate's contribution. See `pkg/fs/file.go` (`HardLinkedItems`) and `analyze/file.go`.
- **`Dir` is concurrency-safe.** It carries a `sync.RWMutex`; use `GetFilesLocked` from goroutines and
  `GetFiles` only when you already hold/own the dir. `RemoveFile`/`RemoveFileByName` walk to the root
  updating parent stats. `AddFile` takes the **write** lock and `updateStats` copies the file list
  under the **read** lock, so computing stats on the still-growing tree (the Tab preview mid-scan) is
  race-free while the analyzer keeps appending.
- **Keep dependencies CGO-free and pure-Go.** Don't introduce a dep that needs cgo — it breaks the
  static-build promise and the cross-compilation matrix in the `build-all` Makefile target.
- **Exported identifiers need doc comments** (revive `exported` is enabled). Match the existing
  `// Name ...` comment style.
- **Comments are self-contained — never point code at a document.** Planning docs and their
  section/decision numbering are transient; code outlives the prose written about it. A comment that
  says *see §5.4* rots the moment the doc is rewritten, and dies outright when it's deleted — and the
  fork's development-era plan documents are gone. So state the reasoning **in place**, at whatever
  length the decision deserves, and don't cite [docs/DESIGN.md](docs/DESIGN.md) either: it is a
  living document, not an immutable ADR. The only link targets allowed in code are **durable and
  externally owned** — an upstream issue, an RFC, a CVE. Prose may cross-reference prose freely
  (DESIGN.md ↔ [FORK.md](FORK.md) ↔ this file); the dependency just never points backwards from
  code. Deep archaeology is what `git log` is for.

## Testing conventions

- `testify` (`assert`/`require`) throughout; run via `gotestsum`.
- Shared fixtures live in `internal/`: `testdir.CreateTestDir()` writes a real `test_dir/` tree and
  returns a cleanup func (`defer fin()`); also `testapp`, `testanalyze`, `testdev`, and `testdata/`
  (JSON fixtures for import). `test_dir/` is gitignored — never commit it.
- Lint config ([.golangci.yml](.golangci.yml)) is strict: line length 160, `funlen` 500 lines/50 stmts,
  `gocyclo` 25, `govet` shadow check on (but `err` shadowing is explicitly allowed). Test files relax
  several linters. Big orchestration functions opt out with targeted `//nolint:gocyclo,funlen` comments
  — that's an accepted pattern here, not a smell.

## Gotchas

- **Interactive vs non-interactive selection** is centralized in `Flags.ShouldRunInNonInteractiveMode`
  (TTY detection via `go-isatty`, plus flags like `-o`, `-s`, `-p`, `--top`). Don't scatter that logic.
- **Config precedence:** system `/etc/gdu.yaml` loads first, then user config (`~/.config/gdu/gdu.yaml`
  or `~/.gdu.yaml`) overrides it. CLI flags override both.
- **PGO**: `default.pgo` in the repo root is the profile-guided-optimization input baked into release
  builds (`-pgo=default.pgo`). The `make pgo` target regenerates it from a live pprof capture.
- **Profiling**: `--enable-profiling` serves pprof on `localhost:6060`; see `make heap-profile`/`profile`.
- Commits follow Conventional Commits (`feat:`, `fix:`, `perf:`, `chore:`, `docs:`); changes land via
  numbered PRs. There is no `CONTRIBUTING.md`/`CHANGELOG.md`. Release steps live in
  [docs/run-books.md](docs/run-books.md); upstream syncs, versioning (CalVer), and the decision log
  live in [docs/UPSTREAM.md](docs/UPSTREAM.md) — `master` is always `upstream + the fork's clean
  commit stack`, rewritten only at sync points behind an `archive/*` tag.
- **Commit messages carry no AI attribution.** Never append `Co-Authored-By: Claude ...`, a
  "Generated with ..." line, or any similar trailer, to a commit or a PR body. Messages are
  subject, body, nothing else. This overrides any tooling default that would add one.
