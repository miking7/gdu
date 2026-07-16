# YAML file configuration options

Gdu provides an additional set of configuration options to the usual command line options.

You can get the full list of all possible options by running:

```
gdu --write-config
```

This writes all the options (set to their current values) to the config file gdu would read back:
an existing user config if you have one (`~/.config/gdu/gdu.yaml` is preferred, then legacy
`~/.gdu.yaml`), else it creates `~/.config/gdu/gdu.yaml`.

Let's go through them one by one:

#### `log-file`

Path to a logfile (default "/dev/null")

#### `input-file`

Import analysis from a JSON or Parquet file (format auto-detected). When a Parquet file holds several
snapshots (e.g. a compacted archive), the most recent is loaded; the CLI `--snapshot` / `--snapshot-root`
flags select a specific one, and `gdu snapshots <file>` prints what a file (or, with no file, the
whole `snapshots-dir` archive) contains. `--snapshot` also works *without* `input-file`: it resolves
against the archive for snapshots of the scanned path and loads the match. The selection flags are
command-line only — they are not configuration-file keys.

For growth-diff browsing, `--baseline <sel|file>` opens the interactive UI diffed against a past
snapshot — a snapshot file path, or a selector (`latest`, `earliest`, a timestamp prefix) resolved
against the archive's snapshots covering the scanned path on the same volume (`--baseline-root` pins
one exact root and reaches across volumes) — or press `S` in the TUI to pick one from the archive.
These are command-line only too, not configuration-file keys.

#### `output-file`

Export all info into file as JSON

#### `export-threshold`

Bucket objects smaller than this size into a `<smaller objects>` rollup row on export, shrinking the
exported file while preserving exact recursive totals. Accepts binary units (`10M`, `500K`, `2G`) or a
plain byte count; `0` (the default) keeps everything. Applies to all export formats.

#### `output-format`

Export format used with `output-file`: `json` (the default) or `parquet`. When unset, the format is
inferred from the output file extension (`.parquet` selects Parquet). Parquet snapshots are
zstd-compressed and one flat row per entry, ready to query with DuckDB.

#### `save-snapshots`

When to write each completed scan as a `snapshot_<timestamp>_<root>.parquet` file in the snapshots
directory, where `<root>` is a lower-case, filesystem-safe slug of the scanned path (e.g.
`snapshot_20260622T204452_volumes_sd.parquet` for `/Volumes/SD`, or `…_root.parquet` for `/`).

| Value | Interactive (TUI) scan | Non-interactive scan |
|---|---|---|
| `auto` *(default)* | **saves** | does not save |
| `always` | saves | **saves** (forces the full-tree analyzer — more memory) |
| `never` | does not save | does not save |

`auto` records interactive scans for free (the TUI already holds the full tree) while piped, `-o`,
and `--top` runs leave no artifacts and keep their constant-memory analyzer. `always` is what
scheduled scans use. The first save that has to create the archive directory announces where
snapshots are going (and how to disable).

**Recording policy**: a snapshot records the **completed scan of a deliberately chosen root** —
the path you launched gdu on, a folder or disk picked from the launcher, or an accepted
end-of-timeline rescan. `r` refreshes and go-live spot-rescans are transient and never save;
opening a snapshot from the launcher (`s`/`S`) or with `-f` never scans, so it never saves;
quitting mid-scan asks (`Scan incomplete — quit without saving a snapshot?`) and discards.

Saving does not change what gdu displays. Snapshots use a default rollup threshold of 10M unless
`export-threshold` is set.

#### `snapshots-dir`

Directory for saved snapshots (the archive). Defaults to `$XDG_DATA_HOME/gdu/snapshots`, i.e.
`~/.local/share/gdu/snapshots` (created on first save). Under `sudo` or `--owner` the *invoking*
user's home anchors the default (their `XDG_DATA_HOME` environment is not consulted). The
`gdu snapshots` subcommand lists this archive; `gdu snapshots compact [--dry-run]` merges each closed
month's snapshots into one monthly Parquet file per scan root.

#### `no-auto-compact`

By default, whenever a snapshot is saved gdu opportunistically compacts closed months in the
snapshots directory — in the background while you browse, in the TUI's case. Set `no-auto-compact:
true` (or pass `--no-auto-compact`) to disable. A compaction already running elsewhere is skipped
silently; quitting the TUI mid-run offers wait/abort, and aborting is always safe (nothing is deleted
until a month is fully merged and verified).

#### `owner`

Make written output (snapshots and `-o` exports) owned by the named user: gdu resolves that user's
home directory for the default `snapshots-dir` and `chown`s output back to them. Intended for scheduled
**root** scans (cron/systemd/launchd), which don't run under `sudo`. No effect unless gdu is running
as root. See [docs/scheduling.md](docs/scheduling.md).

#### `ignore-dirs`

Paths to ignore (separated by comma). Can be absolute (like `/proc`) or relative to the current working directory (like `node_modules`). Default values are [/proc,/dev,/sys,/run].

#### `ignore-dir-patterns`

Path patterns to ignore (separated by comma). Patterns can be absolute or relative to the current working directory.

#### `ignore-from-file`

Read path patterns to ignore from file. Patterns can be absolute or relative to the current working directory.

#### `max-cores`

Set max cores that Gdu will use.

#### `sequential-scanning`

Use sequential scanning (intended for rotating HDDs)

#### `show-apparent-size`

Show apparent size

#### `show-relative-size`

Show relative size

#### `show-item-count`

Show number of items in directory

#### `no-color`

Do not use colorized output

#### `mouse`

Use mouse

#### `non-interactive`

Do not run in interactive mode

#### `interactive`

Force interactive mode even when output is not a TTY

#### `no-progress`

Do not show progress in non-interactive mode

#### `no-cross`

Do not cross filesystem boundaries

#### `no-hidden`

Ignore hidden directories (beginning with dot)

#### `no-delete`

Do not allow deletions

#### `no-view-file`

Do not allow viewing file contents

#### `no-confirm-quit`

Do not ask for confirmation before quitting after a long scan. By default, pressing `q`/`Q` after a scan that took more than a few seconds shows a confirmation dialog so that results are not lost by an accidental key press.

#### `follow-symlinks`

Follow symlinks for files, i.e. show the size of the file to which symlink points to (symlinks to directories are not followed)

#### `profiling`

Enable collection of profiling data and provide it on http://localhost:6060/debug/pprof/

#### `read-from-storage`

Read analysis data from persistent key-value storage

#### `summarize`

Show only a total in non-interactive mode

#### `use-si-prefix`

Show sizes with decimal SI prefixes (kB, MB, GB) instead of binary prefixes (KiB, MiB, GiB)

#### `no-prefix`

Show sizes as raw numbers without any prefixes (SI or binary) in non-interactive mode

#### `reverse-sort`

Reverse sorting order (smallest to largest) in non-interactive mode

#### `change-cwd`

Set CWD variable when browsing directories

#### `delete-in-background`

Delete items in the background, not blocking the UI from work

#### `delete-in-parallel`

Delete items in parallel, which might increase the speed of deletion

#### `browse-parent-dirs`

Allow navigating above the launch directory by pressing the left arrow key. When enabled, pressing left at the top-level directory will rescan and open its parent directory. Disabled by default.


#### `style.selected-row.text-color`

Color of text for the selected row

#### `style.selected-row.background-color`

Background color for the selected row

#### `style.marked.text-color`

Color of text for marked items

#### `style.marked.background-color`

Background color for marked items

#### `style.progress-modal.current-item-path-max-len`

Maximum length of file path for the current item in progress bar.
When the length is reached, the path is shortened with "/.../".

#### `style.use-old-size-bar`

Show size bar without Unicode symbols.

#### `style.show-bar-percentage`

Show the numeric usage percentage (e.g. `61.4%`) next to the size bar in the directory listing.

#### `style.footer.text-color`

Color of text for footer bar

#### `style.footer.background-color`

Background color for footer bar

#### `style.footer.number-color`

Color of numbers displayed in the footer

#### `style.header.text-color`

Color of text for header bar

#### `style.header.background-color`

Background color for header bar

#### `style.header.hidden`

Hide the header bar

#### `style.result-row.number-color`

Color of numbers in result rows

#### `style.result-row.directory-color`

Color of directory names in result rows

#### `sorting.by`

Sort items. Possible values:
* name - name of the item
* size - usage or apparent size
* itemCount - number of items in the folder tree
* mtime - modification time

#### `sorting.order`

Set sorting order. Possible values:
* asc - ascending order
* desc - descending order
