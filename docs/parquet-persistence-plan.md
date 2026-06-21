# Parquet Scan Persistence — Implementation Plan

Status: **proposal / not yet implemented**. This is the working plan for adding Parquet-based
scan persistence to gdu, inspired by [`ncdu_to_parquet.py`](../ncdu_to_parquet.py).

## 1. Goals

1. Export a scan to **Parquet** (zstd), as an alternative to the existing JSON export.
2. A **threshold / rollup** transform that buckets sub-threshold objects into a
   `<smaller objects>` row, shrinking snapshots while preserving exact totals — applied to
   **all** export formats (JSON and Parquet), not just Parquet.
3. **Auto-persist** each scan to a snapshot archive (default `~/.gdu-scans/scan_<ts>.parquet`),
   toggled by a CLI flag / config key.
4. Extend `-f` / input-file reading to load **Parquet** snapshots, not only JSON.
5. (Post-MVP) Snapshot **`df`-style free-space** stats alongside each scan.
6. (Post-MVP) **TUI snapshot browser** to list and load archived scans.
7. (Post-MVP) **Compaction** of daily scans into `monthly_<yyyy-mm>.parquet`, pruning originals.

## 2. Locked decisions (from planning Q&A)

| Decision | Choice | Rationale |
|---|---|---|
| Parquet engine | **Pure-Go `parquet-go/parquet-go`** | gdu is `CGO_ENABLED=0`, statically cross-compiled to many OS/arch targets. `marcboeker/go-duckdb` requires `CGO_ENABLED=1`, ships prebuilt libs for only Linux-amd64 + macOS, dropped FreeBSD in v2, and adds tens of MB. DuckDB stays the **external** query tool you run on the files. |
| Target | **Personal fork, kept upstreamable** | Hold strict gdu constraints: no CGO, minimal deps, all platforms build, full tests + docs. Keeps a future PR to `dundee/gdu` open. |
| Schema | **Match the Python column names**, drop columns gdu can't source | Existing DuckDB queries/compaction keep working for the columns that survive. |
| Threshold scope | **All formats, one decision core** | A single rollup traversal feeds both the JSON (nested) and Parquet (flat) encoders. |

## 3. Key findings that shape the design

- **gdu's JSON export is already ncdu-format** (`[1,2,{meta},<nested tree>]`; see
  [`report/export.go`](../report/export.go) and [`pkg/analyze/encode.go`](../pkg/analyze/encode.go)).
  Your Python script already works on `gdu -o` output. We are re-implementing its *rollup* +
  *Parquet write* natively in Go.
- **gdu holds the full tree in memory at export time** (the `fs.Item` Dir/File tree built by
  `ParallelAnalyzer`). So, unlike the Python streaming-stack approach, the rollup is a simple
  **recursive DFS** over the in-memory tree. Much simpler and safer.
- **Rollup preserves directory totals for free.** gdu's `Dir.updateStats` sums children
  ([`pkg/analyze/file.go`](../pkg/analyze/file.go)). Replacing N sub-threshold children with one
  equal-sized `<smaller objects>` node leaves every ancestor's recursive size/usage unchanged.
- **Parquet export needs the full-tree analyzer.** Non-interactive mode normally uses the
  memory-efficient `TopDirAnalyzer` (no full tree). Like the JSON `-o` path already does, enabling
  Parquet export/auto-save must force `CreateAnalyzer()` (full tree). Document the memory cost.

### gdu's per-item metadata (the "native columns")

Confirmed from `setPlatformSpecificAttrs` in `pkg/analyze/dir_{unix,linux-openbsd,other}.go`:

| Concept | gdu source | Notes |
|---|---|---|
| `asize` apparent | `Item.GetSize()` (`info.Size()`) | always |
| `dsize` disk usage | `Item.GetUsage()` (`stat.Blocks*512`; `Size()` on Windows) | always |
| `name` / `path` | `GetName()` / `GetPath()` | always |
| `mtime` | `File.Mtime` | **always captured** (`--show-mtime` only controls *display*) |
| `ino` | `File.Mli` | **only set when `Nlink > 1`** (hard links); else 0/null |
| flags | rune `Flag` | `@`=notreg(symlink/socket), `H`=hardlink, `!`/`.`=read error, `e`=empty dir |
| recursive item count | `Dir.ItemCount` | single number — **no native files-vs-folders split** |

**Dropped vs the Python schema** (gdu does not collect these): `dev`, `nlink`, `uid`, `gid`,
`mode`, `excluded`, `ext`. `dir_total_files` / `dir_total_folders` are **computed during the
rollup walk** (gdu only has a combined count).

## 4. Target Parquet schema

One flat row per surviving entry (significant file, kept directory, or rollup bucket).

```go
// pkg/parquet/schema.go
type Row struct {
    // identity / structure
    Path     string `parquet:"path,zstd"`
    Parent   string `parquet:"parent,zstd"`
    Name     string `parquet:"name,zstd"`
    IsDir    bool   `parquet:"is_dir"`
    IsRollup bool   `parquet:"is_rollup"`
    Depth    int32  `parquet:"depth"`
    // sizes & counts
    Asize           int64  `parquet:"asize"`
    Dsize           int64  `parquet:"dsize"`
    DirTotalDsize   *int64 `parquet:"dir_total_dsize,optional"`   // recursive usage; dirs only
    DirTotalFiles   *int64 `parquet:"dir_total_files,optional"`   // computed; dirs + rollups
    DirTotalFolders *int64 `parquet:"dir_total_folders,optional"` // computed; dirs + rollups
    // per-scan metadata
    ScanRoot       string `parquet:"scan_root,zstd"`
    ScanTs         int64  `parquet:"scan_ts,timestamp(millisecond)"` // UTC instant; see note
    ThresholdBytes int64  `parquet:"threshold_bytes"`
    // gdu-native passthrough
    Mtime *int64  `parquet:"mtime,optional"`  // unix seconds; null if zero
    Ino   *uint64 `parquet:"ino,optional"`    // only for hard links
    // flags (from gdu rune flag)
    Notreg    bool `parquet:"notreg"`     // '@'
    Hlnkc     bool `parquet:"hlnkc"`      // 'H'
    ReadError bool `parquet:"read_error"` // '!' or '.'
}
```

`scan_ts` is stored as a **timezone-aware** Parquet timestamp (UTC instant, `isAdjustedToUTC=true`),
so DuckDB reads it as `TIMESTAMPTZ` — matching the Python script's `to_timestamp(...)`. We capture the
scan's wall-clock instant in UTC at completion.

Sorted output uses `parquet.SortingColumns(parquet.Ascending("path"), parquet.Ascending("scan_ts"))`
— this ordering is what makes compaction (§ Phase 7) compress so well.

## 5. Architecture / new packages

```
pkg/parquet/        NEW — Row schema, Parquet writer + reader, tree<->rows reconstruction.
                          Imports parquet-go, pkg/analyze, pkg/fs. No import cycle.
pkg/analyze/rollup.go  NEW — threshold rollup traversal (the shared "decision core").
internal/common/size.go NEW — parse "10M"/"500K"/"2G"/"0" (binary units) like the Python.
report/parquet.go   NEW — wires the export UI to the Parquet writer (+ format dispatch).
report/import.go    EDIT — dispatch JSON vs Parquet by magic bytes / extension.
cmd/gdu/app/app.go  EDIT — new Flags fields; choose format; force full analyzer; auto-save wiring.
cmd/gdu/main.go     EDIT — register new cobra flags.
tui/                EDIT — auto-save hook after top-level scan; (later) snapshot browser.
configuration.md / README.md / gdu.1.md  EDIT — document new flags + config keys.
```

### The shared rollup core (one traversal, two adapters)

`pkg/analyze/rollup.go` exposes a single recursive traversal implementing the significance rules
(identical semantics to the Python `close_dirs`):

- A **file** is significant iff `GetUsage() >= threshold`.
- A **directory** is significant iff its **recursive** `GetUsage() >= threshold`. A sub-threshold
  directory collapses entirely (subtree included) into its parent's bucket — safe because a child's
  recursive usage can't exceed its parent's.
- `threshold == 0` ⇒ keep everything (today's exact behavior).

Two thin adapters consume the same traversal so the threshold applies everywhere:

- **JSON adapter** → builds a pruned `*analyze.Dir` tree where each bucket is a plain
  `*analyze.File{Name: "<smaller objects>"}` carrying the aggregated asize/dsize. The **existing**
  `EncodeJSON` then serializes it unchanged. (Counts aren't needed in JSON.)
- **Parquet adapter** → emits `Row`s directly during the walk, including `dir_total_files/folders`
  and per-rollup represented counts (which the flat schema needs and the pruned tree would lose).

> Design note: leading with a visitor/callback traversal avoids inventing a new `fs.Item` type.
> Alternative considered: build one pruned tree and flatten it for Parquet — rejected because a
> single `<smaller objects>` `File` node can't carry the represented file/folder counts the Parquet
> rollup row needs without a bespoke item type.

## 6. Flags & config keys (added to `app.Flags`, wired in `main.go`, documented)

| Flag | Config key (yaml) | Default | Meaning |
|---|---|---|---|
| `--output-format <json\|parquet>` | `output-format` | inferred from `-o` extension, else `json` | Export format. `.parquet` ⇒ parquet. |
| `--threshold <size>` | `export-threshold` | `0` (keep all) | Bucket objects smaller than this into `<smaller objects>`. Binary units (`10M`, `500K`). Applies to JSON **and** Parquet. |
| `--save-scan` | `save-scan` | `false` | Auto-persist each completed scan to the snapshot archive. |
| `--scans-dir <path>` | `scans-dir` | `~/.gdu-scans` | Snapshot archive location. |

Notes:
- No short flags (gdu's `-t` is already `--top`).
- Keeping `--threshold` default `0` preserves byte-for-byte current JSON `-o` output unless opted in.
  Recommend `10M` for snapshots (document it).
- Snapshot filename: `scan_<YYYYMMDDTHHMMSSZ>.parquet` (UTC, sortable, filesystem-safe).

---

## Phased delivery

Each phase is independently shippable and testable. MVP = Phases 1–4.

### Phase 0 — Foundations
**Goal:** add the dependency and skeleton without behavior change.
- `go get github.com/parquet-go/parquet-go`; run `make build` with `CGO_ENABLED=0` and a spot-check of
  `make build-all` (or a couple of `GOOS/GOARCH` cross-builds) to confirm the static/cross-compile
  promise holds. parquet-go's zstd comes from `klauspost/compress`, already in gdu's module graph
  (indirect, via Badger), so the added dependency surface is modest and pure-Go.
- Create empty `pkg/parquet` package + `internal/common/size.go` (binary-size parser) with tests.
- **Acceptance:** builds on `CGO_ENABLED=0`; cross-compile spot-check passes; `make lint` clean.

### Phase 1 — Threshold rollup transform (shared, JSON-visible) — ✅ DONE
**Delivered** (commit `d91df5d`): `--threshold` / `export-threshold` collapses sub-threshold objects
into `<smaller objects>` on the JSON export path; `0` (default) keeps output byte-for-byte.
`internal/common.ParseSizeThreshold`, `analyze.Rollup`, wired via the shared `common.UI`. Verified:
unit + integration + e2e + `-f` round-trip, `go vet`, golangci-lint (pinned v2.11.2) clean, and
`CGO_ENABLED=0` cross-compiles. Note: gdu's JSON encoder HTML-escapes `<`/`>` (cosmetic; any JSON
parser and the Phase 2 Parquet writer use the literal name).

**Goal:** `--threshold` collapses sub-threshold objects in the existing JSON export.
- Implement `pkg/analyze/rollup.go` traversal + the **JSON adapter** (pruned tree builder).
- Wire `--threshold` into `app.Flags`, parse via `internal/common/size.go`, apply before
  `EncodeJSON` in the export path.
- **Tests:** golden-tree fixtures (extend `internal/testdir`/`testanalyze`); assert (a) every kept
  directory's recursive size/usage equals the pre-rollup totals, (b) `<smaller objects>` aggregates
  match the sum of collapsed items, (c) `threshold 0` ⇒ identical to today's output.
- **Acceptance:** `gdu -o out.json --threshold 10M /dir` produces a smaller, totals-preserving JSON
  that round-trips through `gdu -f out.json`.

### Phase 2 — Parquet export format — ✅ DONE
**Delivered** (commit `6477be7`): `gdu -o scan.parquet` / `--output-format parquet` writes a
zstd Parquet snapshot via `pkg/parquet` (parquet-go, pure Go). Threshold-aware flattener emits one
row per file/dir/rollup; `scan_ts`/`mtime` are tz-aware (verified `TIMESTAMP WITH TIME ZONE` in
DuckDB), `ino` is `UBIGINT`, dir totals in `dir_total_*`. All 9 `CGO_ENABLED=0` cross-compile targets
still build; lint/tests green; DuckDB `DESCRIBE` confirms the 19-column schema. Library notes:
parquet-go pulls a few pure-Go transitive deps (brotli, lz4, go-geom, bitpack, jsonlite); binary ~38M.

**Goal:** `gdu -o scan.parquet` (or `--output-format parquet`) writes a Parquet snapshot.
- `pkg/parquet`: `Row` schema + writer (`parquet.NewGenericWriter[Row]`, zstd, sorting columns).
- **Parquet adapter** over the rollup traversal → `Row` stream (computes `dir_total_files/folders`,
  rollup counts, depth, parent).
- `report/parquet.go` + `createUI`/format dispatch in `app.go`; force `analyze.CreateAnalyzer()`
  (full tree) when format is parquet.
- **Tests:** write a fixture tree to Parquet, read it back (parquet-go reader), assert row counts,
  totals, flag mapping, and that `SELECT`-style column names match the schema table above. Verify
  zstd compression is applied.
- **Acceptance:** `gdu -o scan.parquet --threshold 10M /dir` yields a file your existing DuckDB
  queries open with matching column names.

### Phase 3 — Parquet import (`-f`) — ✅ DONE
**Delivered** (commit `1ccde8f`): `report.ReadAnalysis` sniffs the `PAR1` magic and routes to
`parquet.ReadTree`, which rebuilds the `analyze.Dir` tree via a path→node map; callers' existing
`UpdateStats` recomputes recursive totals from the leaf/rollup rows. Works for `-f file.parquet` and
stdin `-f-` (input is buffered, then read via `bytes.Reader`); JSON path unchanged. Multi-scan files
load the most recent `scan_ts`. Verified e2e (export→`-f`→listing shows the `<smaller objects>`
rollup), tests/lint/cross-compile green. `*.parquet` added to `.gitignore`.

**Goal:** `gdu -f scan.parquet` loads a snapshot into the TUI/stdout.
- Format dispatch in `report.ReadAnalysis` / `app.runAction`: detect `PAR1` magic (first 4 bytes) or
  `.parquet` extension ⇒ Parquet reader; else JSON. For `-f-` (stdin), buffer to memory/temp since
  Parquet needs a seekable reader.
- `pkg/parquet`: rows → `*analyze.Dir` tree via a `path → node` map (sort by path, create nodes, link
  parents; `<smaller objects>` rows become `File` nodes). 
- **Multi-scan files** (post-compaction) contain several `scan_ts`/`scan_root`: MVP picks the latest
  scan; surface a clear message. (Selection UI lands in Phase 6.)
- **Tests:** round-trip — export tree → Parquet → import → compare tree shape, sizes, flags. Test
  stdin buffering and the JSON-vs-Parquet dispatch.
- **Acceptance:** export then `-f` reproduces the browsable tree.

### Phase 4 — Auto-persistence to the archive — ✅ DONE (MVP complete)
**Delivered** (commit `cf1855a`): `--save-scan` writes `$HOME/.gdu-scans/scan_<ts>.parquet`
(`--scans-dir` to override) as each scan completes, default threshold 10M (overridable via
`--threshold`). Behavior-neutral — output unchanged. Implemented as a hook at the scan-completion
point in each UI (the app-level hook doesn't work because the TUI scans **asynchronously** and needs
its event loop running). Non-interactive path forces `analyze.CreateAnalyzer()` since the default
`TopDirAnalyzer` is shallow. Config lives on `common.UI` (`SetSaveScan`) to keep `common` free of the
`parquet` import (avoids `common→parquet→analyze→common` cycle). Tests cover tui + stdout + the
snapshot helper; verified e2e (`threshold_bytes=10485760`, output unchanged, reloads via `-f`).

**Goal:** `--save-scan` writes `~/.gdu-scans/scan_<ts>.parquet` once, at scan completion.
- **Behavior-neutral by design (per decision):** `--save-scan` changes **nothing** in the TUI or CLI
  output. The snapshot is emitted **as soon as the initial scan completes, before the TUI renders**
  (and for non-interactive, as a side effect at analysis completion that doesn't alter stdout).
- **Hook point:** in `app.Run`, after `runAction(ui, path)` builds the tree and **before**
  `ui.StartUILoop()`. This requires a small accessor on the `UI` interface to reach the analyzed root
  (e.g. `GetAnalyzedTree() fs.Item`, returning `topDir` for the TUI / the exported dir otherwise).
  Apply the Phase 1 rollup + Phase 2 writer to that tree.
- **Default threshold = `10M`** for auto-saved snapshots (snapshots are for trend-tracking; per
  decision), independent of `--threshold` unless the user overrides. Resolve precedence: explicit
  `--threshold` wins; otherwise auto-save uses `10M`.
- Create `scans-dir` (default `~/.gdu-scans`, `0700`) if missing. Filename
  `scan_<YYYYMMDDTHHMMSSZ>.parquet` (UTC).
- Requires the full-tree analyzer (force `CreateAnalyzer()` when `--save-scan` is set in
  non-interactive mode; document the memory implication). The TUI already has the full tree.
- Re-scans inside the TUI (`r`) do **not** auto-save (only the initial top-level scan does), keeping
  the "emit once at completion" contract. (Revisit if you later want per-rescan snapshots.)
- **Tests:** flag/config plumbing; a scan with `--save-scan` produces exactly one timestamped file in
  a temp `scans-dir`; the snapshot imports back to the same tree; output is byte-identical with vs
  without `--save-scan`.
- **Acceptance:** scanning with `--save-scan` leaves a valid, importable snapshot and changes no
  visible output.

### Phase 5 — `df` free-space snapshot (post-MVP)
**Goal:** record available disk space at scan time, for "how full was the disk then?" trend analysis.
- Source: `device.Getter.GetDevicesInfo()` → `Device{MountPoint, Fstype, Size, Free}`. Capture the
  filesystem hosting `scan_root` (longest mount-point prefix) and optionally all mounts.
- **Storage options (pick during implementation):**
  - **(a) Sidecar `scan_<ts>.df.parquet`** — clean schema separation; recommended. Columns:
    `scan_ts, scan_root, mount_point, fstype, total_bytes, free_bytes, used_bytes`.
  - **(b) Same file, `row_type` discriminator** — single file, but mostly-null columns / wider schema.
  - **(c) Parquet footer key-value metadata** — compact for a summary, but awkward for multiple mounts.
- **Tests:** captured totals match a mocked `DevicesInfoGetter`; sidecar is written next to the scan.
- **Acceptance:** each saved scan has associated free-space figures recoverable by DuckDB.

### Phase 6 — TUI snapshot browser (post-MVP)
**Goal:** list snapshots in `scans-dir` and load one without leaving gdu.
- New TUI view (key e.g. `S`): list `scan_*.parquet` (and `monthly_*.parquet`) with timestamp, root,
  size; Enter loads via the Phase 3 importer. For multi-scan/monthly files, list each `scan_ts`.
- Follows gdu's `tui.Option` + `SetXxx` convention; lazy-read Parquet footers for the listing.
- **Tests:** listing parses filenames/footers; selecting loads the right scan.
- **Acceptance:** browse history and open any past scan interactively.

### Phase 7 — Compaction (post-MVP)
**Goal:** merge daily scans into `monthly_<yyyy-mm>.parquet`, then prune originals.
- Pure-Go: read the month's `scan_*.parquet`, **sort by `(path, scan_ts)`**, write one zstd file
  (dictionary/RLE + similarity across scans ⇒ the large compression win you measured). For months too
  big for memory, do an external merge or stream row groups.
- **Safety:** write the monthly file fully, verify total row count vs sources, atomic-rename into
  place, **then** delete originals. Never prune before verification.
- Trigger: explicit `gdu --compact-scans` first; consider auto-compaction-on-startup later.
- Optional power-user path: shell out to a `duckdb` CLI if present
  (`COPY (SELECT * FROM read_parquet('scan_*.parquet') ORDER BY path, scan_ts) TO 'monthly.parquet'`),
  gated behind an explicit flag — never a build dependency.
- **Tests:** compacted file has the union of rows; per-scan slices (`WHERE scan_ts = …`) reproduce the
  originals; originals removed only after verification.
- **Acceptance:** a month of scans compacts to one much-smaller file with no data loss.

---

## 7. Cross-cutting concerns

- **Build/CI:** keep `CGO_ENABLED=0`; verify `make build-all` cross-compile matrix after Phase 0.
  Add tests to the existing `go test ./...` matrix (Go 1.25.x/1.26.x, ubuntu + macos).
- **Lint:** new exported symbols need doc comments (revive `exported`); keep functions under
  `funlen` 500 / `gocyclo` 25, or add targeted `//nolint` like the existing big orchestration funcs.
- **Docs:** every new flag/key must land in `configuration.md`, `README.md`, and `gdu.1.md`
  (the man page is generated from `gdu.1.md` via `make man`).
- **Conventional commits / PRs:** `feat:`/`fix:` etc., one phase per PR for upstreamability.
- **Backward compatibility:** `--threshold 0` default keeps current JSON `-o` byte-identical.

## 8. Decisions resolved / still open

**Resolved:**
- Flag names: `--threshold`, `--save-scan`, `--scans-dir` (committed).
- Auto-save default threshold = **`10M`** (explicit `--threshold` overrides).
- `scan_ts` = **timezone-aware Parquet timestamp** (UTC instant → DuckDB `TIMESTAMPTZ`).
- `--save-scan` is **behavior-neutral**: emitted once at scan completion, before the TUI renders;
  does not alter TUI/CLI output.

**Still open (deferred):**
- df snapshot storage: sidecar vs discriminator vs footer metadata — decide at Phase 5.
```
