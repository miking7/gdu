# About this fork

This is a fork of [**dundee/gdu**](https://github.com/dundee/gdu), the fast parallel disk-usage
analyzer. It is deliberately kept **upstreamable** — it holds gdu's constraints (`CGO_ENABLED=0`,
pure-Go dependencies, every cross-compile target builds, full tests + docs) so the additions could be
contributed back.

Everything in upstream gdu works exactly as before. What the fork adds is a Parquet **scan-snapshot**
subsystem — export a scan to a compact columnar file, auto-archive every scan, and reload any
snapshot — for tracking disk usage **over time**, plus the ergonomics needed to actually run that
(threshold rollups, sudo-safe output, a scheduling guide).

## What the fork adds

### Parquet scan snapshots
Export, auto-archive, and re-import scans as [Apache Parquet](https://parquet.apache.org/), alongside
gdu's existing ncdu-style JSON:

- `gdu -o scan.parquet /` (or `gdu --output-format parquet -o- /`) — export one snapshot.
- `gdu --save-scan /` — auto-save every completed scan to `~/.gdu-scans/scan_<timestamp>.parquet`.
- `gdu -f scan.parquet` — browse a snapshot in the TUI/CLI; JSON vs Parquet is auto-detected.

Snapshots are written and read with the pure-Go [`parquet-go`](https://github.com/parquet-go/parquet-go)
library — **DuckDB is *not* a dependency**; it stays an *external* tool you can point at the files.
Reads and writes are **streaming**, so a full-disk snapshot of millions of files stays within bounded
memory. Snapshot **filenames use local time**; the `scan_ts` **column** inside is stored in UTC
(timezone-aware) so snapshots from different machines/zones compare correctly.

### Threshold rollup
`--threshold 10M` buckets every file or directory whose disk usage is below the size into a synthetic
`<smaller objects>` row, shrinking snapshots dramatically while preserving each directory's **exact**
recursive totals. It applies to **both** JSON and Parquet export. `--threshold 0` (the default for
`-o`) keeps gdu's output byte-for-byte; `--save-scan` defaults to `10M`.

### sudo-friendly output
Whole-disk scans usually need `sudo`. When run elevated, gdu resolves the **invoking** user (via
`SUDO_USER`/`SUDO_UID`/`SUDO_GID`), writes `--save-scan` snapshots into *their* `~/.gdu-scans` (not
`/root`'s), and `chown`s **every** file it writes — auto-saved snapshots, `-o` exports, and
interactive TUI exports — back to that user, so the output stays readable without root.

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

**Size conventions.** gdu has no per-inode size for directories, so directory rows carry
`asize = dsize = 0` and put their recursive figures in `dir_total_*`. Summing `dsize` over all
**non-directory** rows reproduces the scan total. A `<smaller objects>` row carries the aggregated
`asize`/`dsize` and the represented file/folder counts of everything it replaced — so a directory's
surviving children still sum back to its recursive total.

`host`, `username`, and `sudo_user` identify *where* and *as whom* each scan ran — useful when several
machines' snapshots are pooled, and to tell elevated (`root`) scans from regular ones.

### Querying with DuckDB

```sql
-- Biggest directories in the most recent snapshot
SELECT path, dir_total_dsize
FROM read_parquet('~/.gdu-scans/*.parquet')
WHERE is_dir AND scan_ts = (SELECT max(scan_ts) FROM read_parquet('~/.gdu-scans/*.parquet'))
ORDER BY dir_total_dsize DESC LIMIT 20;

-- How one folder has grown across every snapshot (the point of taking snapshots)
SELECT scan_ts, dir_total_dsize
FROM read_parquet('~/.gdu-scans/*.parquet')
WHERE is_dir AND path = '/Users/me/Downloads'
ORDER BY scan_ts;
```

## New flags & config keys

| Flag | Config key (yaml) | Default | Meaning |
|---|---|---|---|
| `--output-format <json\|parquet>` | `output-format` | inferred from the `-o` extension | Export format. `.parquet` ⇒ Parquet. |
| `--threshold <size>` | `export-threshold` | `0` (keep everything) | Bucket sub-threshold objects into `<smaller objects>`. Binary units (`10M`, `500K`). |
| `--save-scan` | `save-scan` | `false` | Auto-save each completed scan as a Parquet snapshot. |
| `--scans-dir <path>` | `scans-dir` | `~/.gdu-scans` | Directory for `--save-scan` snapshots. |
| `--owner <user>` | `owner` | (none) | Make written output owned by `<user>` (resolves their home + `chown`s output to them). For scheduled root scans. |

The existing `-f` / `--input-file` flag is **extended** to load `.parquet` snapshots as well as JSON
(format auto-detected). All keys can also live in gdu's config file — e.g. to make every scan
self-archive, put in `~/.gdu.yaml`:

```yaml
save-scan: true
export-threshold: 10M
```

See [configuration.md](configuration.md) for the full list of config keys.

## Further reading
- [docs/parquet-persistence-plan.md](docs/parquet-persistence-plan.md) — design + delivery status of the Parquet subsystem.
- [docs/post-mvp-fixes-plan.md](docs/post-mvp-fixes-plan.md) — post-MVP hardening (memory, sudo ownership, identity columns, docs).
- [docs/scheduling.md](docs/scheduling.md) — scheduling periodic scans on macOS and Linux.
- [CLAUDE.md](CLAUDE.md) — how the codebase is built and changed.
