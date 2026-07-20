# gdu — disk usage with history

<img src="./gdu.png" alt="Gdu " width="200" align="right">

Fast parallel disk usage analyzer (TUI + CLI) with **time travel**: every completed scan is
archived as a compact snapshot, and the TUI can diff against, step through, and reopen any of
them.

When the disk is mysteriously full *again*: scan it, press `S`, pick the snapshot from last
month, press Enter — every row now carries a signed Δ, sorted so the biggest grower is on top.
Follow the ▲ trail downward to the culprit. Two keys from scan to answer. `[`/`]` time-travel
the view itself; `Tab` previews the partial tree found so far while a scan is still running;
`O` opens any snapshot; snapshot views are read-only with a guided way back to live. Bare `gdu`
opens a **launcher** — the folder you're in, its disk, your other disks, and your snapshots of
each — instead of a blind scan (`launcher: false` restores scan-immediately). See
**[FORK.md](./FORK.md)** for the journeys and reference, and
**[docs/scheduling.md](./docs/scheduling.md)** for scheduling periodic scans so the history
builds itself.

**Built on [dundee/gdu](https://github.com/dundee/gdu)** — everything upstream gdu does works
here unchanged, at upstream speed, with upstream keys ([demo](https://asciinema.org/a/382738)),
optimized for SSDs where parallel scanning shines (HDDs work too, with a smaller gain). The fork
tracks upstream, keeps its engineering constraints (pure Go, `CGO_ENABLED=0`, the full
cross-platform build matrix), and aims to contribute back what fits;
[docs/UPSTREAM.md](./docs/UPSTREAM.md) records how it stays in sync and why. Note that distro
packages of `gdu` are upstream's — snapshots and time travel ship only through this fork's
[releases](https://github.com/miking7/gdu/releases), below.

## Installation

This fork publishes binaries for macOS and Linux (x86-64 and arm64) on its
[releases page](https://github.com/miking7/gdu/releases). These four lines fetch the right one for
your machine, so you don't have to work out which that is:

```sh
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -L "https://github.com/miking7/gdu/releases/latest/download/gdu_${OS}_${ARCH}.tar.gz" | tar xz gdu
sudo install gdu /usr/local/bin/gdu
```

Re-run the same four lines to **update** — they always resolve to the current release. To uninstall,
`sudo rm /usr/local/bin/gdu` (saved snapshots live in `~/.local/share/gdu/snapshots`; remove that
directory too if you want them gone).

To pick a build by hand instead, run `uname -sm` and match it:

| `uname -sm` | Download |
|---|---|
| `Darwin arm64` | `gdu_darwin_arm64.tar.gz` — Apple Silicon |
| `Darwin x86_64` | `gdu_darwin_amd64.tar.gz` — Intel Mac |
| `Linux x86_64` | `gdu_linux_amd64.tar.gz` |
| `Linux aarch64` | `gdu_linux_arm64.tar.gz` |

**On macOS, if you download through a browser** rather than with `curl`: the binaries are not
Apple-notarized, so Gatekeeper quarantines them and the first run is blocked. Clear it once with
`xattr -d com.apple.quarantine gdu`. The `curl` route above sets no quarantine flag and needs no such
step.

**On macOS, if you granted gdu Full Disk Access** — needed for whole-disk root scans to see
TCC-protected folders (Mail, Messages, other users' homes) — **you must re-grant it after every
update.** The grant is pinned to the binary's exact contents, so replacing the binary silently voids
it, and scans go back to under-reporting those folders without any error. Remove the old Full Disk
Access entry and add the new binary again. See
[docs/scheduling.md](./docs/scheduling.md#step-1--give-gdu-full-disk-access-do-this-first).

Each release also carries a `checksums.txt` to verify a download against:

```sh
shasum -a 256 -c checksums.txt   # macOS
sha256sum -c checksums.txt       # Linux
```

Windows isn't published as a binary; it still builds from source with `go build ./cmd/gdu`.

> **Note:** the packaged channels — `brew install gdu`, `apt install gdu`, snap, pacman, winget, and
> `go install github.com/dundee/gdu/v5/cmd/gdu@latest` — all install
> [**upstream** gdu](https://github.com/dundee/gdu), which does **not** include this fork's snapshot
> history. This fork is distributed as the binaries above only. See [FORK.md](./FORK.md) for what it
> adds.

## Usage

```
  gdu [flags] [directory_to_scan]
  gdu snapshots [list|compact] [file]   # list or compact the snapshot archive (alias: snaps)

Flags:
      --archive-browsing              Enable browsing of zip/jar/tar archives (tar, tar.gz, tar.bz2, tar.xz)
      --baseline string               Interactive: open in growth-diff mode against this baseline — a Parquet snapshot file, or a selector (latest, earliest, or a timestamp prefix) resolved against the archive's snapshots covering the scanned path on the same volume. Pick another baseline in the TUI with S.
      --baseline-root string          Restrict a --baseline selector to snapshots of this exact scan root (also reaches across volumes).
      --collapse-path                 Collapse single-child directory chains
      --config-file string            Read config from file (default is ~/.config/gdu/gdu.yaml, or ~/.gdu.yaml if that exists)
  -D, --db string                     Store analysis in database (*.sqlite for SQLite, *.badger for BadgerDB)
      --depth int                     Show directory structure up to specified depth in non-interactive mode (0 means the flag is ignored)
      --enable-profiling              Enable collection of profiling data and provide it on http://localhost:6060/debug/pprof/
  -E, --exclude-type strings          File types to exclude (e.g., --exclude-type yaml,json)
      --export-threshold string       Bucket objects smaller than this size into a '<smaller objects>' rollup on export. Binary units: 10M, 500K, 2G, or plain bytes. 0 = keep everything. (default "0")
  -L, --follow-symlinks               Follow symlinks for files, i.e. show the size of the file to which symlink points to (symlinks to directories are not followed)
  -h, --help                          help for gdu
  -i, --ignore-dirs strings           Paths to ignore (separated by comma). Can be absolute or relative to current directory (default [/proc,/dev,/sys,/run])
  -I, --ignore-dirs-pattern strings   Path patterns to ignore (separated by comma)
  -X, --ignore-from string            Read path patterns to ignore from file
  -f, --input-file string             Import analysis from JSON or Parquet file (format auto-detected)
      --interactive                   Force interactive mode even when output is not a TTY
      --launcher                      Open the interactive launcher (folder, its disk, your other disks, snapshots) instead of scanning immediately. --launcher=false (or launcher: false in config) restores upstream scan-immediately behavior. Ignored in non-interactive mode. (default true)
  -l, --log-file string               Path to a logfile (default "/dev/null")
      --max-age string                Include files with mtime no older than DURATION (e.g., 7d, 2h30m, 1y2mo)
  -m, --max-cores int                 Set max cores that Gdu will use. 8 cores available (default 8)
      --min-age string                Include files with mtime at least DURATION old (e.g., 30d, 1w)
      --mouse                         Use mouse
      --no-auto-compact               Do not compact the archive's closed months after a snapshot is saved.
  -c, --no-color                      Do not use colorized output
      --no-confirm-quit               Do not ask for confirmation before quitting after a long scan
  -x, --no-cross                      Do not cross filesystem boundaries
      --no-delete                     Do not allow deletions
  -H, --no-hidden                     Ignore hidden directories (beginning with dot)
      --no-prefix                     Show sizes as raw numbers without any prefixes (SI or binary) in non-interactive mode
  -p, --no-progress                   Do not show progress in non-interactive mode
      --no-spawn-shell                Do not allow spawning shell
  -u, --no-unicode                    Do not use Unicode symbols (for size bar)
      --no-view-file                  Do not allow viewing file contents
  -n, --non-interactive               Do not run in interactive mode
  -o, --output-file string            Export all info into file as JSON
      --output-format string          Export format: json (default) or parquet. Inferred from the -o file extension when unset.
      --owner string                  Make written output (snapshots, -o exports) owned by this user: resolves their home for the default snapshots-dir and chowns output to them. For scheduled root scans.
  -r, --read-from-storage             Use existing database instead of re-scanning
      --reverse-sort                  Reverse sorting order (smallest to largest) in non-interactive mode
      --save-snapshots string         When to save each completed scan as a Parquet snapshot in the snapshots directory (auto|always|never, default auto): auto saves interactive scans only, always saves in every mode (forcing the full-tree analyzer non-interactively), never disables saving. Snapshot rollup threshold defaults to 10M. (default "auto")
      --sequential                    Use sequential scanning (intended for rotating HDDs)
  -A, --show-annexed-size             Use apparent size of git-annex'ed files in case files are not present locally (real usage is zero)
  -a, --show-apparent-size            Show apparent size
  -d, --show-disks                    Show all mounted disks
  -k, --show-in-kib                   Show sizes in KiB (or kB with --si) in non-interactive mode
  -C, --show-item-count               Show number of items in directory
  -M, --show-mtime                    Show latest mtime of items in directory
  -B, --show-relative-size            Show relative size
      --si                            Show sizes with decimal SI prefixes (kB, MB, GB) instead of binary prefixes (KiB, MiB, GiB)
      --since string                  Include files with mtime >= WHEN. WHEN accepts RFC3339 timestamp (e.g., 2025-08-11T01:00:00-07:00) or date only YYYY-MM-DD (calendar-day compare; includes the whole day)
      --snapshot string               Which snapshot to load: latest, earliest, or a local timestamp/prefix like 2026-06-19 or 2026-06-19T15:30:05. With -f, selects within that file; without -f, resolves against the archive for snapshots of the scanned path and loads the match.
      --snapshot-root string          Restrict --snapshot selection to this exact scan root (rarely needed; the positional path is the primary scope without -f).
      --snapshots-dir string          Directory for saved snapshots (default $XDG_DATA_HOME/gdu/snapshots, i.e. ~/.local/share/gdu/snapshots).
  -s, --summarize                     Show only a total in non-interactive mode
  -t, --top int                       Show only top X largest files in non-interactive mode
  -T, --type strings                  File types to include (e.g., --type yaml,json)
      --until string                  Include files with mtime <= WHEN. WHEN accepts RFC3339 timestamp or date only YYYY-MM-DD
  -v, --version                       Print version
      --write-config                  Write current configuration to file (the config that would be read: an existing user config, else ~/.config/gdu/gdu.yaml, creating the directory)

Basic list of actions in interactive mode (show help modal for more):
  ↑ or k                              Move cursor up
  ↓ or j                              Move cursor down
  → or Enter or l                     Go to highlighted directory
  ← or h                              Go to parent directory
  d                                   Delete the selected file or directory
  e                                   Empty the selected directory
  n                                   Sort by name
  s                                   Sort by size
  c                                   Show number of items in directory
  ?                                   Show help modal
```

## Examples

    gdu                                   # analyze current dir
    gdu -a                                # show apparent size instead of disk usage
    gdu --no-delete                       # prevent write operations
    gdu --no-view-file                    # prevent viewing file contents
    gdu <some_dir_to_analyze>             # analyze given dir
    gdu -d                                # show all mounted disks
    gdu -l ./gdu.log <some_dir>           # write errors to log file
    gdu -i /sys,/proc /                   # ignore some paths
    gdu -I '.*[abc]+'                     # ignore paths by regular pattern
    gdu -X ignore_file /                  # ignore paths by regular patterns from file
    gdu -c /                              # use only white/gray/black colors

    gdu -n /                              # only print stats, do not start interactive mode
    gdu --interactive / | tee out.txt     # force interactive mode even when stdout is piped
    gdu -p /                              # do not show progress, useful when using its output in a script
    gdu -ps /some/dir                     # show only total usage for given dir
    gdu -t 10 /                           # show top 10 largest files
    gdu --reverse-sort -n /               # show files sorted from smallest to largest in non-interactive mode
    gdu / > file                          # write stats to file, do not start interactive mode

    gdu -o- / | gzip -c >report.json.gz   # write all info to JSON file for later analysis
    zcat report.json.gz | gdu -f-         # read analysis from file
    gdu -o- --export-threshold 10M /      # export, bucketing objects < 10 MiB into "<smaller objects>"
    gdu -o snapshot.parquet --export-threshold 10M / # export a compact Parquet snapshot (query later with DuckDB)
    gdu -f snapshot.parquet               # browse a previously exported Parquet snapshot

    gdu --db=tmp.badger /                 # use persistent key-value storage for saving analysis data
    gdu --db=tmp.db /                     # use persistent SQLite storage for saving analysis data
    gdu -r /                              # read saved analysis data from persistent key-value storage

## Modes

Gdu has three modes: interactive (default), non-interactive and export.

Non-interactive mode is started automatically when TTY is not detected (using [go-isatty](https://github.com/mattn/go-isatty)), for example if the output is being piped to a file, or it can be started explicitly by using a flag. Use `--interactive` to disable this automatic fallback and force interactive mode.

In non-interactive mode (and without `--top` and `--depth` flags), gdu uses a memory-efficient analyzer that only tracks top-level directory totals.
This means memory usage stays constant regardless of how large the scanned directory tree is.
When `--top` or `--depth` flags are used, the full directory tree is built in memory as in interactive mode.

Export mode (flag `-o`) outputs all usage data as JSON (or Parquet with `--output-format parquet` /
a `.parquet` output file), which can be later opened using the `-f` flag. The input format is detected
automatically.

Hard links are counted only once.

## File flags

Files and directories may be prefixed by a one-character
flag with following meaning:

* `!` An error occurred while reading this directory.

* `.` An error occurred while reading a subdirectory, size may be not correct.

* `@` File is symlink or socket.

* `H` Same file was already counted (hard link).

* `e` Directory is empty.

## Configuration file

Gdu can read (and write) YAML configuration file.

`$HOME/.config/gdu/gdu.yaml` and `$HOME/.gdu.yaml` are checked for the presence of the config file by default.

See the [full list of all configuration options](configuration.md).

### Examples

* To configure gdu to permanently run in gray-scale color mode:

```
echo "no-color: true" >> ~/.gdu.yaml
```

* To set default sorting in configuration file:

```
sorting:
    by: name // size, name, itemCount, mtime
    order: desc
```

* To configure gdu to set CWD variable when browsing directories:

```
echo "change-cwd: true" >> ~/.gdu.yaml
```

* To save the current configuration

```
gdu --write-config
```

## Styling

There are wide options for how terminals can be colored.
Some gdu primitives (like basic text) adapt to different color schemas, but the selected/highlighted row does not.

If the default look is not sufficient, it can be changed in configuration file, e.g.:

```
style:
    selected-row:
        text-color: black
        background-color: "#ff0000"
    marked:
        text-color: white
        background-color: "#6600cc"
```

## Deletion in background and in parallel (experimental)

Gdu can delete items in the background, thus not blocking the UI for additional work.
To enable:

```
echo "delete-in-background: true" >> ~/.gdu.yaml
```

Directory items can be also deleted in parallel, which might increase the speed of deletion.
To enable:

```
echo "delete-in-parallel: true" >> ~/.gdu.yaml
```

## Saving analysis data to database

Gdu can store the analysis data to a database file instead of just memory.
This allows you to save and reload analysis results later.
Both SQLite and BadgerDB are supported.

```
gdu --db analysis.sqlite /        # saves analysis data to SQLite database
gdu --db analysis.badger /        # saves analysis data to BadgerDB
gdu -r --db analysis.sqlite /     # reads saved data, does not run analysis again
```

## Saving snapshots

Gdu automatically saves every completed **interactive** scan as a compact Parquet **snapshot**, so
you can track disk usage over time and query the history with tools like
[DuckDB](https://duckdb.org/). There is nothing to configure: browse a directory, and the scan is
recorded in the archive (the first save announces where). Piped and scripted runs stay clean —
non-interactive scans save only when asked:

```
gdu /                                    # browse as usual; a snapshot is also archived
gdu -n --save-snapshots always /         # record a non-interactive scan too (e.g. from cron)
gdu --save-snapshots never /             # this run: browse only, record nothing
gdu --snapshots-dir ~/snaps /            # use a custom snapshots directory
```

`--save-snapshots` (yaml key `save-snapshots`) takes `auto` (the default: interactive scans only),
`always` (every mode; forces the full-tree analyzer non-interactively, which costs memory), or
`never`. Saving does not change what gdu shows; it writes `snapshot_<timestamp>_<root>.parquet`
(local time) into the snapshots directory (default `$XDG_DATA_HOME/gdu/snapshots`, i.e.
`~/.local/share/gdu/snapshots`) as the scan completes. The `<root>` suffix is a lower-case,
filesystem-safe slug of the scanned path so the file says what it covers at a glance — e.g.
`snapshot_20260622T204452_volumes_sd.parquet` for `/Volumes/SD`, `…_users_michael.parquet` for
`/Users/michael`, and `…_root.parquet` for `/`. Snapshots use a default rollup threshold of 10M
(objects smaller than that are grouped into a `<smaller objects>` row); override it with
`--export-threshold`.

To see what's archived, `gdu snapshots` (alias `gdu snaps`) prints a table of every snapshot in the
archive (root, time, size); with a file argument it lists just that file's snapshots:

```
gdu snapshots                           # every snapshot in the archive
gdu snapshots monthly.parquet           # the snapshots inside one file
gdu snaps ls                            # same listing, spelled out (list / ls)
```

To get a snapshot back, resolve it straight from the archive with `--snapshot` — no file path
needed. The selector is `latest`, `earliest`, or a local-time prefix (a month `2026-06`, a day
`2026-06-19`, or a full timestamp), matched against the archive's snapshots of exactly the path you
name; an ambiguous or unmatched selector lists the candidates (and, if the path has no snapshot of
its own but a parent does, names the covering roots):

```
gdu --snapshot latest /Volumes/SD        # reopen the newest snapshot of /Volumes/SD — no disk scan
gdu --snapshot 2026-06-19 ~              # browse your home dir as it was that day
gdu --snapshot latest -n --top 20 /      # top-20 report from the archive, disk untouched
```

A single Parquet file can hold more than one snapshot (`gdu snapshots compact` packs a month of
daily snapshots into one file). When it does, opening it interactively (`gdu -f <file>`) shows a
**snapshot picker** — choose a snapshot and it loads; the picker also prints the exact `--snapshot`
command for your choice. In non-interactive mode (`-n`) the most recent snapshot loads by default and
a note on stderr says which. Either way, `--snapshot` picks within the file, using a value straight
from `gdu snapshots <file>`:

```
gdu -f monthly.parquet --snapshot 2026-06-19            # the snapshot from that day
gdu -f monthly.parquet --snapshot 2026-06-19T15:30:05   # an exact timestamp
gdu -f monthly.parquet --snapshot earliest              # oldest snapshot in the file
gdu -f monthly.parquet --snapshot 2026-06-19 --snapshot-root /home   # disambiguate by root
```

### Growth-diff browsing

The main reason to keep snapshots is to see **what changed**. Set a past snapshot as a *baseline*
and gdu annotates every row with how much it grew or shrank since then, so you can sort by growth and
drill straight to what's eating the disk.

Press `S` while browsing to pick a baseline from the archive. The picker is contextual to the folder
you're in — it shows that folder's size in each snapshot, its change since, and the snapshot's scan
root — so you can spot when a directory ballooned and anchor the baseline there. Only snapshots whose
scan covers the current folder **on the same volume** are listed: a whole-disk `/` scan won't clutter
the list for a folder that lives on a separate drive (use `S` on that drive, or `--baseline-root`, to
reach across volumes). The host column appears only for snapshots taken on another machine.

Once a baseline is set, each row gains a signed **Δ column**: grown (`▲`, warm), shrank (`▼`, cool),
new (`✦`), or absent-from-a-thresholded-baseline (`~`, approximate). Items that existed then but are
**gone now** appear inline (`✗`, struck through). The view auto-sorts by growth; `>` / `<` flip
between biggest growth and biggest shrink, and `Esc` clears the baseline. The footer reconciles the
whole directory: grown, shrunk, removed, and net change.

You can also start straight in diff mode from the command line. `--baseline` takes a selector
(resolved against the archive's snapshots that **cover** the path you're browsing on the same volume —
a baseline must cover what you're looking at; `--baseline-root` reaches across volumes) or a snapshot
file path:

```
gdu --baseline latest /home              # live /home vs. its newest archived snapshot
gdu --baseline 2026-06 /home             # …vs. June's snapshot (prefix must match exactly one)
gdu --baseline 2026-06 --baseline-root / /home   # pin the selector to the whole-disk snapshots
gdu --baseline old.parquet /home         # …vs. an explicit snapshot file
gdu -f now.parquet --baseline old.parquet        # snapshot vs. snapshot
```

An ambiguous selector lists the candidates and suggests `--baseline-root`. When a `--baseline` file
holds several snapshots, the latest is used. Growth is measured by disk usage; a directory's
apparent-size growth isn't tracked because snapshots store no recursive apparent size.

### Time travel

Snapshots aren't just baselines — they can be **the view**. Press `[` while browsing and you're
looking at the same folder, same cursor, as of your previous snapshot — read-only; `[` again goes
older, `]` newer, and the footer shows the folder's size at each stop (ride it backwards to see
*when* something ballooned). `]` past the newest returns to the live view — instantly when the live
tree is in memory, otherwise gdu offers to rescan (your choice). `O` opens *any* archived snapshot,
any root, any date. `Esc` always returns to where you started, instantly.

Snapshot views — including `-f` imports — are **read-only**: `d`/`e`/`r` show a signpost whose
primary action is *go live here* (an instant switch, or a confirmed scan of just that folder,
cursor kept). Refreshes and those spot-rescans never save snapshots; only completed scans of a
deliberately chosen root are recorded. See [FORK.md](./FORK.md) for the full journeys.

### Compacting the archive

Daily snapshots add up. `gdu snapshots compact` merges every **closed** month's snapshot files (per
scan root and host) into one `monthly_<yyyy-mm>_<root>.parquet`, sorted so that the same paths from
different scans sit next to each other — which compresses far better than separate dailies and keeps
the archive to one file per month. Compaction is **lossless**: every snapshot remains individually
loadable (`--snapshot`, or the picker), and source files are deleted only after the merged file has
been written, re-read and verified against them. The current month is never touched, so it is always
safe to run — even while other scans are being saved:

```
gdu snapshots compact --dry-run   # show what would be merged, write nothing
gdu snapshots compact             # merge closed months, verify, then remove the dailies
```

Stragglers are fine: a daily that shows up after its month was already compacted (say, copied in
from a laptop) is folded into the existing monthly on the next run.

You rarely need to run it yourself: whenever a snapshot is saved and some closed month still has
loose dailies, gdu compacts them right after the save — in the background while you browse, in the
TUI's case, with a footer indicator and a wait/abort prompt if you quit mid-run (aborting is always
safe). If another gdu is already compacting, the run is skipped silently. `--no-auto-compact` (or
`no-auto-compact: true` in the config) turns the automatic run off.

When you scan with `sudo`, gdu writes snapshots into the **invoking** user's
`~/.local/share/gdu/snapshots` and hands the files back to that user (so they stay readable without
root). Each row also records `host`,
`username` (the effective user, e.g. `root`) and `sudo_user` (the invoking user), so you can tell
which scans ran elevated. For a scheduled root scan (which isn't under `sudo`), `--owner <user>`
requests the same hand-back. To run scans on a schedule (daily, 6-hourly, …) on macOS or Linux, see
[docs/scheduling.md](./docs/scheduling.md).

## Running tests

    make install-dev-dependencies
    make test

## Profiling

Gdu can collect profiling data when the `--enable-profiling` flag is set.
The data are provided via embedded http server on URL `http://localhost:6060/debug/pprof/`.

You can then use e.g. `go tool pprof -web http://localhost:6060/debug/pprof/heap`
to open the heap profile as SVG image in your web browser.

## Benchmarks

Benchmarks were performed on 90G directory (100k directories, 400k files) on 500 GB SSD using [hyperfine](https://github.com/sharkdp/hyperfine).
See `benchmark` target in [Makefile](Makefile) for more info.

### Cold cache

Filesystem cache was cleared using `sync; echo 3 | sudo tee /proc/sys/vm/drop_caches`.

| Command | Mean [s] | Min [s] | Max [s] | Relative |
|:---|---:|---:|---:|---:|
| `diskus ~` | 4.489 ± 0.020 | 4.449 | 4.516 | 1.00 |
| `gdu -npc ~` | 4.716 ± 0.342 | 4.109 | 5.337 | 1.05 ± 0.08 |
| `GOMAXPROCS=80 gdu -npc ~` | 4.901 ± 1.953 | 3.627 | 9.993 | 1.09 ± 0.44 |
| `pdu ~` | 5.969 ± 0.492 | 5.567 | 6.640 | 1.33 ± 0.11 |
| `dua ~` | 6.030 ± 0.249 | 5.878 | 6.597 | 1.34 ± 0.06 |
| `dust -d0 ~` | 6.181 ± 0.311 | 6.043 | 7.053 | 1.38 ± 0.07 |
| `gdu -npc --db=tmp.badger ~` | 27.479 ± 3.015 | 25.048 | 32.777 | 6.12 ± 0.67 |
| `du -hs ~` | 30.608 ± 0.221 | 30.136 | 30.794 | 6.82 ± 0.06 |
| `duc index ~` | 32.897 ± 3.168 | 31.524 | 41.865 | 7.33 ± 0.71 |
| `ncdu -0 -o /dev/null ~` | 33.163 ± 3.482 | 31.476 | 42.979 | 7.39 ± 0.78 |
| `gdu -npc --db=tmp.db ~` | 44.989 ± 0.270 | 44.622 | 45.414 | 10.02 ± 0.07 |

### Warm cache

| Command | Mean [ms] | Min [ms] | Max [ms] | Relative |
|:---|---:|---:|---:|---:|
| `diskus ~` | 270.8 ± 8.1 | 262.4 | 291.5 | 1.00 |
| `pdu ~` | 299.1 ± 4.1 | 292.1 | 305.0 | 1.10 ± 0.04 |
| `GOMAXPROCS=100 gdu -npc ~` | 459.1 ± 14.2 | 446.7 | 490.3 | 1.69 ± 0.07 |
| `gdu -npc ~` | 466.1 ± 27.9 | 421.4 | 495.3 | 1.72 ± 0.12 |
| `dua ~` | 590.6 ± 5.9 | 580.5 | 599.7 | 2.18 ± 0.07 |
| `dust -d0 ~` | 578.7 ± 3.7 | 572.2 | 586.3 | 2.14 ± 0.07 |
| `du -hs ~` | 1255.2 ± 7.4 | 1245.1 | 1273.4 | 4.63 ± 0.14 |
| `duc index ~` | 1450.5 ± 6.2 | 1440.6 | 1460.4 | 5.36 ± 0.16 |
| `ncdu -0 -o /dev/null ~` | 2222.4 ± 5.6 | 2215.6 | 2231.0 | 8.21 ± 0.25 |
| `gdu -npc --db=tmp.db ~` | 8246.7 ± 30.9 | 8181.9 | 8288.7 | 30.45 ± 0.92 |
| `gdu -npc --db=tmp.badger ~` | 15608.0 ± 3215.8 | 13960.3 | 22448.0 | 57.63 ± 12.00 |

## Alternatives

* [ncdu](https://dev.yorhel.nl/ncdu) - NCurses based tool written in pure `C` (LTS) or `zig` (Stable)
* [godu](https://github.com/viktomas/godu) - Analyzer with a carousel like user interface
* [dua](https://github.com/Byron/dua-cli) - Tool written in `Rust` with interface similar to gdu (and ncdu)
* [diskus](https://github.com/sharkdp/diskus) - Very simple but very fast tool written in `Rust`
* [duc](https://duc.zevv.nl/) - Collection of tools with many possibilities for inspecting and visualising disk usage
* [dust](https://github.com/bootandy/dust) - Tool written in `Rust` showing tree like structures of disk usage
* [pdu](https://github.com/KSXGitHub/parallel-disk-usage) - Tool written in `Rust` showing tree like structures of disk usage

## Notes

[HDD icon created by Nikita Golubev - Flaticon](https://www.flaticon.com/free-icons/hdd)
