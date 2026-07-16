---
date: {{date}}
section: 1
title: gdu
---

# NAME

gdu - Pretty fast disk usage analyzer written in Go

# SYNOPSIS

**gdu \[flags\] \[directory_to_scan\]**

**gdu snapshots \[list\] \[file.parquet\]**

**gdu snapshots compact \[\--dry-run\]**

# DESCRIPTION

Pretty fast disk usage analyzer written in Go.

Gdu is intended primarily for SSD disks where it can fully utilize
parallel processing. However HDDs work as well, but the performance gain
is not so huge.

This fork records history: every completed interactive scan is archived as a
Parquet snapshot (see **\--save-snapshots**). In the TUI, **S** compares the view
against a snapshot (growth diff), **\[** and **\]** step the view itself through
this folder's snapshots, and **O** opens any archived snapshot; snapshot views
are read-only, with a guided way back to the live disk.

# COMMANDS

**gdu snapshots \[list\] \[file.parquet\]**
List every snapshot in the snapshot archive (\--snapshots-dir), newest first,
or the snapshots held in one Parquet snapshot file (\"-\" reads from standard
input). Command alias: **snaps**; verb alias: **ls**.

**gdu snapshots compact \[\--dry-run\]**
Merge each closed month's snapshots in the archive into one monthly Parquet
file per scan root (lossless; sources are deleted only after the result is
verified). With **\--dry-run**, print what would be compacted without writing
or deleting anything.

# OPTIONS

**-h**, **\--help**\[=false\] help for gdu

**-i**, **\--ignore-dirs**=\[/proc,/dev,/sys,/run\]
    Paths to ignore (separated by comma).
    Supports both absolute and relative paths.

**-I**, **\--ignore-dirs-pattern**
    Path patterns to ignore (separated by comma).
    Supports both absolute and relative path patterns.

**-X**, **\--ignore-from**
    Read path patterns to ignore from file.
    Supports both absolute and relative path patterns.

**-T**, **\--type** File types to include (e.g., --type yaml,json)

**-E**, **\--exclude-type** File types to exclude (e.g., --exclude-type yaml,json)

**\--max-age** Include files with mtime no older than DURATION (e.g., 7d, 2h30m, 1y2mo)

**\--min-age** Include files with mtime at least DURATION old (e.g., 30d, 1w)

**\--since** Include files with mtime >= WHEN. WHEN accepts RFC3339 timestamp (e.g., 2025-08-11T01:00:00-07:00) or date only YYYY-MM-DD (calendar-day compare; includes the whole day)

**\--until** Include files with mtime <= WHEN. WHEN accepts RFC3339 timestamp or date only YYYY-MM-DD

**-l**, **\--log-file**=\"/dev/null\" Path to a logfile

**-m**, **\--max-cores** Set max cores that Gdu will use.

**-c**, **\--no-color**\[=false\] Do not use colorized output

**-x**, **\--no-cross**\[=false\] Do not cross filesystem boundaries

**-H**, **\--no-hidden**\[=false\] Ignore hidden directories (beginning with dot)

**-L**, **\--follow-symlinks**\[=false\] Follow symlinks for files, i.e. show the
size of the file to which symlink points to (symlinks to directories are not followed)

**-n**, **\--non-interactive**\[=false\] Do not run in interactive mode

**\--interactive**\[=false\] Force interactive mode even when output is not a TTY

**-p**, **\--no-progress**\[=false\] Do not show progress in
non-interactive mode

**-u**, **\--no-unicode**\[=false\] Do not use Unicode symbols (for size bar)

**-s**, **\--summarize**\[=false\] Show only a total in non-interactive mode

**\--export-threshold**\[="0"\] Bucket objects smaller than this size into a '<smaller objects>' rollup on export. Binary units: 10M, 500K, 2G, or plain bytes. 0 = keep everything.

**-t**, **\--top**\[=0\] Show only top X largest files in non-interactive mode

**-d**, **\--show-disks**\[=false\] Show all mounted disks

**-a**, **\--show-apparent-size**\[=false\] Show apparent size

**-C**, **\--show-item-count**\[=false\] Show number of items in directory

**-k**, **\--show-in-kib**\[=false\] Show sizes in KiB (or kB with --si) in non-interactive mode

**-M**, **\--show-mtime**\[=false\] Show latest mtime of items in directory

**\--archive-browsing**\[=false\] Enable browsing of zip/jar/tar archives (tar, tar.gz, tar.bz2, tar.xz)

**\--depth**\[=0\] Show directory structure up to specified depth in non-interactive mode (0 means the flag is ignored)

**\--collapse-path**\[=false\] Collapse single-child directory chains

**\--mouse**\[=false\] Use mouse

**\--si**\[=false\] Show sizes with decimal SI prefixes (kB, MB, GB) instead of binary prefixes (KiB, MiB, GiB)

**\--no-prefix**\[=false\] Show sizes as raw numbers without any prefixes (SI or binary) in non-interactive mode

**\--no-spawn-shell**\[=false\] Do not allow spawning shell

**\--no-confirm-quit**\[=false\] Do not ask for confirmation before quitting after a long scan

**\--no-delete**\[=false\] Do not allow deletions

**\--no-view-file**\[=false\] Do not allow viewing file contents

**-f**, **\--input-file** Import analysis from JSON or Parquet file (format auto-detected). If the file is \"-\", read from standard input.

**-o**, **\--output-file** Export all info into file as JSON. If the file is \"-\", write to standard output.

**\--output-format** Export format: json (default) or parquet. Inferred from the -o file extension when unset.

**\--save-snapshots**\[="auto"\] When to save each completed scan of a chosen root as a Parquet snapshot in the snapshots directory (auto|always|never, default auto): auto saves interactive scans only, always saves in every mode (forcing the full-tree analyzer non-interactively), never disables saving. Refreshes (r) and go-live spot-rescans never save; quitting mid-scan asks and discards. Snapshot rollup threshold defaults to 10M.

**\--snapshots-dir** Directory for saved snapshots (default $XDG_DATA_HOME/gdu/snapshots, i.e. ~/.local/share/gdu/snapshots).

**\--snapshot** Which snapshot to load: latest, earliest, or a local timestamp/prefix like 2026-06-19 or 2026-06-19T15:30:05. With -f, selects within that file; without -f, resolves against the archive for snapshots of the scanned path and loads the match.

**\--snapshot-root** Restrict --snapshot selection to this exact scan root (rarely needed; the positional path is the primary scope without -f).

**\--baseline** Interactive: open in growth-diff mode against this baseline — a Parquet snapshot file, or a selector (latest, earliest, or a timestamp prefix) resolved against the archive's snapshots covering the scanned path on the same volume. Pick another baseline in the TUI with S.

**\--baseline-root** Restrict a --baseline selector to snapshots of this exact scan root (also reaches across volumes).

**\--no-auto-compact**\[=false\] Do not compact the archive's closed months after a snapshot is saved.

**\--owner** Make written output (snapshots, -o exports) owned by this user: resolves their home for the default snapshots-dir and chowns output to them. For scheduled root scans.

**\--config-file** Read config from file (default is ~/.config/gdu/gdu.yaml, or ~/.gdu.yaml if that exists)

**\--write-config**\[=false\] Write current configuration to file (the config that would be read: an existing user config, else ~/.config/gdu/gdu.yaml, creating the directory)

**\--enable-profiling**\[=false\] Enable collection of profiling data and provide it on http://localhost:6060/debug/pprof/

**-D**, **\--db** Store analysis in database (*.sqlite for SQLite, *.badger for BadgerDB)

**-r**, **\--read-from-storage**\[=false\] Use existing database instead of re-scanning

**-v**, **\--version**\[=false\] Print version

# HISTORY KEYS

In the TUI, once the archive holds snapshots covering the current folder:

**\[** / **\]**

:   Step the view to an older / newer snapshot of the current folder (same
    folder, same cursor). At the newest, **\]** returns to the live view —
    instantly when the live tree is still in memory, otherwise via an
    explicit rescan offer.

**S**

:   Pick a baseline snapshot covering this folder on its volume (the picker lists
    each snapshot's scan root, size of this folder, and change since; a host
    column shows only for snapshots from another machine); every row then carries
    a signed growth delta (**\>** / **\<** sort by growth).

**O**

:   Open any archived snapshot — all roots and dates — as the view.

**Esc**

:   Layered and always instant: close an overlay, else clear the baseline,
    else return to where the session started. Esc never scans.

Snapshot views (including **-f** imports) are read-only: **d**, **e** and
**r** offer *go live here* — an instant switch when a live tree covering the
folder is in memory, else a confirmed scan of just that folder. Refreshes and
those spot-rescans never save a snapshot; only completed scans of a
deliberately chosen root are recorded.

# FILE FLAGS

Files and directories may be prefixed by a one-character
flag with following meaning:

**!**

:   An error occurred while reading this directory.

**.**

:   An error occurred while reading a subdirectory, size may be not correct.

**\@**

:  File is symlink or socket.

**H**

:  Same file was already counted (hard link).

**e**

:  Directory is empty.
