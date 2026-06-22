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

## Parquet scan snapshots

gdu can export/import scans as Apache Parquet and auto-archive them for trend analysis. Design notes
(full plan + status in [docs/parquet-persistence-plan.md](docs/parquet-persistence-plan.md)):

- **Pure-Go only — never DuckDB/cgo.** Writing uses `github.com/parquet-go/parquet-go`. The DuckDB Go
  driver requires `CGO_ENABLED=1` and would break the static cross-platform builds, so it is
  deliberately **not** used; DuckDB stays an *external* tool users run on the `.parquet` files. Do not
  add `go-duckdb` or any cgo dependency.
- **Threshold rollup** ([pkg/analyze/rollup.go](pkg/analyze/rollup.go)): `--threshold`/`export-threshold`
  collapses files/dirs whose disk usage is below the threshold into a synthetic `<smaller objects>`
  `File`, preserving each directory's exact recursive totals. `analyze.Rollup` builds a pruned tree for
  the JSON encoder; `pkg/parquet` does its own threshold-aware flatten for the flat row schema. Default
  `0` = keep everything (current behavior).
- **Flows**: `-o x.parquet` / `--output-format parquet` export ([report/export.go](report/export.go)),
  `-f x.parquet` import dispatched by the `PAR1` magic in [report/import.go](report/import.go), and
  `--save-scan` auto-archive to `$HOME/.gdu-scans/scan_<ts>_<root>.parquet`.
- **`--save-scan` hooks at each UI's scan-completion point, not at the app level** — the TUI scans
  *asynchronously* (needs its event loop running for `QueueUpdateDraw`), so there's no app-level moment
  where the tree is ready before the UI starts. Config lives on `common.UI` (`SetSaveScan`) kept free of
  the `parquet` import to avoid a `common→parquet→analyze→common` cycle. Non-interactive `--save-scan`
  forces `analyze.CreateAnalyzer()` because the default `TopDirAnalyzer` is shallow.
- **Schema** ([pkg/parquet/schema.go](pkg/parquet/schema.go)): 22 columns, one flat row per
  file/dir/rollup; `scan_ts`/`mtime` are timezone-aware (`timestamp(millisecond)` → DuckDB
  `TIMESTAMPTZ`); directory rows carry `asize=dsize=0` with recursive totals in `dir_total_*` (gdu has
  no per-inode dir size). Scan-level identity columns `host`/`username` (effective user) /`sudo_user`
  (invoking user under sudo, nullable) are stamped on every row.
- **Parquet read/write are streaming and memory-sensitive** (a full-disk snapshot is millions of
  rows). **Crucial macOS fact:** `debug.FreeOSMemory()` does *not* lower RSS on macOS (`MADV_FREE`), so
  the rule is *don't allocate the peak*, not *free it after*. Concretely: `WriteTree` streams rows to
  the writer in batches (no whole-`[]Row`; output is DFS order, **not** globally path-sorted — readers
  rebuild by path, compaction can sort later); the flatten iterates with `fs.SortByNone` (no per-dir
  sorted copy) and builds child paths only for emitted items; `ReadTree` streams batches via
  `parquet.NewGenericReader` building the tree incrementally; `report.ReadAnalysis` streams straight
  from the `*os.File`. `--save-scan` calls `runtime.GC()` before writing so the snapshot reuses freed
  scan garbage. Don't reintroduce all-rows buffering, per-dir sorting, or a global sort here.
- **Snapshot filenames use local time** with a scan-root slug
  (`scan_<YYYYMMDDTHHMMSS>_<root>.parquet`, collision-suffixed) — `rootSlug()` in
  [pkg/parquet/snapshot.go](pkg/parquet/snapshot.go) lower-cases the absolute root and collapses any
  non-`[a-z0-9]` run to `_` (`/`→`root`, `/Volumes/SD`→`volumes_sd`), capped at 60 chars. The slug is
  cosmetic; the lossless `scan_root` *column* is the source of truth and the fixed-width timestamp
  still leads so lexical sort stays chronological. The `scan_ts` *column* stays UTC. **sudo-safe output:** [internal/common/ownership.go](internal/common/ownership.go)
  resolves the invoking user (`SUDO_USER`/`UID`/`GID`) for the default scans-dir and `chown`s every
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
