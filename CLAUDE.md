# CLAUDE.md

Guidance for working in **gdu** (`go DiskUsage()`) — a fast, parallel disk usage analyzer
(TUI + CLI) written in Go. Upstream: `github.com/dundee/gdu` (module path `github.com/dundee/gdu/v5`).
Think of it as a faster alternative to `ncdu`/`du`, optimized for SSDs via parallel scanning.

For *user-facing* docs (flags, examples, config keys) see [README.md](README.md) and
[configuration.md](configuration.md). This file is about *how the code is built and how to change it*.

## Build / run / test / lint

Toolchain: Go (go.mod requires `1.25`, [.tool-versions](.tool-versions) pins `1.26.1`, CI tests
`1.25.x` + `1.26.x`). **Everything builds with `CGO_ENABLED=0`** — pure-Go, fully static binaries.

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
reintroduce per-item progress channels).

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
  the JSON encoder; `pkg/parquet` does its own threshold-aware flatten for the flat row schema. Default
  `0` = keep everything (current behavior).
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
  live is the newest point, the just-saved snapshot *folds* into it); `O` opens any snapshot;
  `Esc` is layered (modal → clear baseline → return view) and **never scans**. The two-slot header
  lives in [tui/header.go](tui/header.go). **Recording policy**: only completed scans of a
  deliberately chosen root save (`scanOpts.transient` marks `r`-refreshes and spot-rescans, which
  never save; quit-mid-scan confirms and discards). The timeline stays walkable during scans
  (scan-wait time travel): the progress page is the live position, and completion never steals
  focus from a user browsing the past.
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
  chosen root (saves), `s` opens the row's latest snapshot, `S` a picker (both reuse the snapshot picker
  via `openSnapshotView`), `n` toggles the disk sort (usage-desc↔name-asc; the folder + pinned own
  disk + scan-another-folder stay fixed via `launcherDiskSpan`). Pre-selection: explicit path →
  folder row; bare/`-d` → the cwd's pinned disk. **Choosing the pinned own disk lands the view at the
  default dir, not the mount root** — `launcherRow.land`/`landPath()`, threaded as
  `scanOpts.landPath` (honored by `finishRootScan` when the scan root covers it) and as the `s`/`S`
  open's `wantPath`; the whole disk is still scanned and `scan_root` is the mount.
  **macOS `/System/Volumes/*` are hidden from the launcher display only
  (`device.HideSystemVolumes`); `ui.devices` stays UNFILTERED so `launcherScan`'s nested-mount ignores
  don't double-count `/System/Volumes/Data` when scanning `/`.** Snapshot↔row mapping is
  **mount-accurate (`launcherRowMapsSnapshot`)**: disk rows match `scan_root == mount` exactly;
  the folder row matches roots between its most-specific mount (longest prefix over `ui.devices`) and
  itself — never path-covering alone. `ui.launcher` (a `*launcherState`) doubles as the async-fill
  generation guard. The **read-error count** the sudo tip cites is persisted per snapshot in the
  footer manifest (`SnapshotInfo.ErrCount`, counted at write time by `countReadErrorDirs`; rounds
  through compaction for free).
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
  shared by `launcherRowMapsSnapshot`, the S picker (`coveringListings`), the `[`/`]` timeline, and
  CLI `--baseline`/`--snapshot` covering hints. The mount comes from `device.ForPath(devices, path)`
  (the launcher's `deviceForPath` folded into `pkg/device`); the TUI captures `ui.devices`/`ui.getter`
  on the event loop and resolves off it via `mountForTarget` (getter fallback when the launcher was
  skipped — never mutating `ui.devices`). Go-live tests **actual tree membership** (`viewContains` →
  `descendToPath`), not path arithmetic, so a `/`-rooted live tree doesn't claim an SD folder.
  **Host is foreign-only** (`common.HostnameBestEffort`/`HostIsForeign`, exact match) across the
  pickers, `report.PrintSnapshots`, and `parquet.FormatSnapshotList`. The Baseline picker gains a
  **Root column** and device-table styling (blue roots/amber sizes/dim ages via the shared
  `deviceNameColor`/`deviceSizeColor` tags), and marks + pre-selects the active baseline
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
  updating parent stats.
- **Keep dependencies CGO-free and pure-Go.** Don't introduce a dep that needs cgo — it breaks the
  static-build promise and the cross-compilation matrix in the `build-all` Makefile target.
- **Exported identifiers need doc comments** (revive `exported` is enabled). Match the existing
  `// Name ...` comment style.
- **Comments are self-contained — never point code at a document.** Planning docs and their
  section/decision numbering are transient; code outlives the prose written about it. A comment that
  says *see §5.4* rots the moment the doc is rewritten, and dies outright when it's deleted (the
  fork's development-era plans are gone — they survive only at tag `archive/pre-squash`). So state
  the reasoning **in place**, at whatever length the decision deserves, and don't cite
  [docs/DESIGN.md](docs/DESIGN.md) either: it is a living document, not an immutable ADR. The only
  link targets allowed in code are **durable and externally owned** — an upstream issue, an RFC, a
  CVE. Prose may cross-reference prose freely (DESIGN.md ↔ [FORK.md](FORK.md) ↔ this file); the
  dependency just never points backwards from code. Deep archaeology is what `git log` and the
  archive tags are for.

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
  [docs/run-books.md](docs/run-books.md).
- **Commit messages carry no AI attribution.** Never append `Co-Authored-By: Claude ...`, a
  "Generated with ..." line, or any similar trailer, to a commit or a PR body. Messages are
  subject, body, nothing else. This overrides any tooling default that would add one.
