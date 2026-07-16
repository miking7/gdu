package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-isatty"
	"github.com/rivo/tview"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dundee/gdu/v5/cmd/gdu/app"
	"github.com/dundee/gdu/v5/pkg/device"
)

const (
	osWindows = "windows"
	osPlan9   = "plan9"
)

var (
	af        *app.Flags
	configErr error
)

var rootCmd = &cobra.Command{
	Use:   "gdu [directory_to_scan]",
	Short: "Pretty fast disk usage analyzer written in Go",
	Long: `Pretty fast disk usage analyzer written in Go.

Gdu is intended primarily for SSD disks where it can fully utilize parallel processing.
However HDDs work as well, but the performance gain is not so huge.

This fork records history: every completed interactive scan is archived as a Parquet
snapshot (see --save-snapshots). In the TUI, S compares the view against a snapshot
(growth diff), [ and ] step the view itself through this folder's snapshots, and O opens
any archived snapshot; snapshot views are read-only, with a guided way back to the live disk.
`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runE,
}

var snapshotsDryRun bool

var snapshotsCmd = &cobra.Command{
	Use:     "snapshots [file.parquet]",
	Aliases: []string{"snaps"},
	Short:   "List or compact the snapshot archive",
	Long: `List every snapshot in the snapshot archive (--snapshots-dir), newest first,
or the snapshots held in one Parquet snapshot file ("-" reads from stdin).

Alias: "gdu snaps". Subcommands: list (the default action, alias "ls") and
compact (merge each closed month's snapshots into one monthly file).
`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runSnapshotsListE,
}

var snapshotsListCmd = &cobra.Command{
	Use:          "list [file.parquet]",
	Aliases:      []string{"ls"},
	Short:        "List snapshots in the archive or a snapshot file",
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runSnapshotsListE,
}

var snapshotsCompactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Merge each closed month's snapshots into one monthly file",
	Long: `Merge each closed month's snapshots in the archive (--snapshots-dir) into one
monthly Parquet file per scan root (lossless; sources are deleted only after
the result is verified).
`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSnapshotsCompactE,
}

// nolint:funlen // a lot of flags to initialize
func init() {
	af = &app.Flags{Style: app.Style{ProgressModal: app.ProgressModalOpts{ShowDiskProgressBar: true}}}

	// Flags shared with the snapshots subcommand are registered persistently so
	// `gdu snapshots …` inherits them.
	pflags := rootCmd.PersistentFlags()
	pflags.StringVar(&af.CfgFile, "config-file", "",
		"Read config from file (default is ~/.config/gdu/gdu.yaml, or ~/.gdu.yaml if that exists)")
	pflags.StringVarP(&af.LogFile, "log-file", "l", "/dev/null", "Path to a logfile")
	pflags.StringVar(&af.SnapshotsDir, "snapshots-dir", "",
		"Directory for saved snapshots (default $XDG_DATA_HOME/gdu/snapshots, i.e. ~/.local/share/gdu/snapshots).")
	pflags.StringVar(&af.Owner, "owner", "",
		"Make written output (snapshots, -o exports) owned by this user: resolves their "+
			"home for the default snapshots-dir and chowns output to them. For scheduled root scans.")
	pflags.IntVarP(&af.MaxCores, "max-cores", "m", runtime.NumCPU(), fmt.Sprintf("Set max cores that Gdu will use. %d cores available", runtime.NumCPU()))

	rootCmd.AddCommand(snapshotsCmd)
	snapshotsCmd.AddCommand(snapshotsListCmd, snapshotsCompactCmd)
	snapshotsCompactCmd.Flags().BoolVar(&snapshotsDryRun, "dry-run", false,
		"Print what would be compacted without writing or deleting anything.")

	flags := rootCmd.Flags()
	flags.StringVarP(&af.OutputFile, "output-file", "o", "", "Export all info into file as JSON")
	flags.StringVarP(&af.InputFile, "input-file", "f", "", "Import analysis from JSON or Parquet file (format auto-detected)")
	flags.StringVar(&af.ExportThreshold, "export-threshold", "0",
		"Bucket objects smaller than this size into a '<smaller objects>' rollup on export. "+
			"Binary units: 10M, 500K, 2G, or plain bytes. 0 = keep everything.")
	flags.StringVar(&af.OutputFormat, "output-format", "",
		"Export format: json (default) or parquet. Inferred from the -o file extension when unset.")
	flags.StringVar(&af.SaveSnapshots, "save-snapshots", "auto",
		"When to save each completed scan of a chosen root as a Parquet snapshot in the snapshots "+
			"directory (auto|always|never, default auto): auto saves interactive scans only, always "+
			"saves in every mode (forcing the full-tree analyzer non-interactively), never disables "+
			"saving. Refreshes and go-live spot-rescans never save. Snapshot rollup threshold "+
			"defaults to 10M.")
	flags.StringVar(&af.Snapshot, "snapshot", "",
		"Which snapshot to load: latest, earliest, or a local timestamp/prefix like "+
			"2026-06-19 or 2026-06-19T15:30:05. With -f, selects within that file; without -f, "+
			"resolves against the archive for snapshots of the scanned path and loads the match.")
	flags.StringVar(&af.SnapshotRoot, "snapshot-root", "",
		"Restrict --snapshot selection to this exact scan root (rarely needed; the "+
			"positional path is the primary scope without -f).")
	flags.StringVar(&af.Baseline, "baseline", "",
		"Interactive: open in growth-diff mode against this baseline — a Parquet snapshot "+
			"file, or a selector (latest, earliest, or a timestamp prefix) resolved against the "+
			"archive's snapshots covering the scanned path on the same volume. Pick another baseline in the TUI with S.")
	flags.StringVar(&af.BaselineRoot, "baseline-root", "",
		"Restrict a --baseline selector to snapshots of this exact scan root (also reaches across volumes).")
	flags.BoolVar(&af.NoAutoCompact, "no-auto-compact", false,
		"Do not compact the archive's closed months after a snapshot is saved.")
	flags.BoolVar(&af.SequentialScanning, "sequential", false, "Use sequential scanning (intended for rotating HDDs)")
	flags.BoolVarP(&af.ShowVersion, "version", "v", false, "Print version")

	flags.StringSliceVarP(&af.TypeFilter, "type", "T", []string{}, "File types to include (e.g., --type yaml,json)")
	flags.StringSliceVarP(&af.ExcludeTypeFilter, "exclude-type", "E", []string{}, "File types to exclude (e.g., --exclude-type yaml,json)")
	flags.StringSliceVarP(&af.IgnoreDirs, "ignore-dirs", "i", []string{"/proc", "/dev", "/sys", "/run"},
		"Paths to ignore (separated by comma). Can be absolute or relative to current directory")
	flags.StringSliceVarP(&af.IgnoreDirPatterns, "ignore-dirs-pattern", "I", []string{},
		"Path patterns to ignore (separated by comma)")
	flags.StringVarP(&af.IgnoreFromFile, "ignore-from", "X", "",
		"Read path patterns to ignore from file")
	flags.BoolVarP(&af.NoHidden, "no-hidden", "H", false, "Ignore hidden directories (beginning with dot)")
	flags.BoolVarP(
		&af.FollowSymlinks, "follow-symlinks", "L", false,
		"Follow symlinks for files, i.e. show the size of the file to which symlink points to (symlinks to directories are not followed)",
	)
	flags.BoolVarP(
		&af.ShowAnnexedSize, "show-annexed-size", "A", false,
		"Use apparent size of git-annex'ed files in case files are not present locally (real usage is zero)",
	)
	flags.BoolVarP(&af.NoCross, "no-cross", "x", false, "Do not cross filesystem boundaries")
	flags.BoolVar(&af.Profiling, "enable-profiling", false, "Enable collection of profiling data and provide it on http://localhost:6060/debug/pprof/")

	flags.StringVarP(&af.DbPath, "db", "D", "", "Store analysis in database (*.sqlite for SQLite, *.badger for BadgerDB)")
	flags.BoolVarP(&af.ReadFromStorage, "read-from-storage", "r", false, "Use existing database instead of re-scanning")
	flags.BoolVar(&af.ArchiveBrowsing, "archive-browsing", false, "Enable browsing of zip/jar/tar archives (tar, tar.gz, tar.bz2, tar.xz)")
	flags.BoolVar(&af.CollapsePath, "collapse-path", false, "Collapse single-child directory chains")

	flags.BoolVarP(&af.ShowDisks, "show-disks", "d", false, "Show all mounted disks")
	flags.BoolVarP(&af.ShowApparentSize, "show-apparent-size", "a", false, "Show apparent size")
	flags.BoolVarP(&af.ShowRelativeSize, "show-relative-size", "B", false, "Show relative size")
	flags.BoolVarP(&af.NoColor, "no-color", "c", false, "Do not use colorized output")
	flags.BoolVarP(&af.ShowItemCount, "show-item-count", "C", false, "Show number of items in directory")
	flags.BoolVarP(&af.ShowMTime, "show-mtime", "M", false, "Show latest mtime of items in directory")
	flags.BoolVarP(&af.NonInteractive, "non-interactive", "n", false, "Do not run in interactive mode")
	flags.BoolVar(&af.Interactive, "interactive", false, "Force interactive mode even when output is not a TTY")
	flags.BoolVarP(&af.NoProgress, "no-progress", "p", false, "Do not show progress in non-interactive mode")
	flags.BoolVarP(&af.NoUnicode, "no-unicode", "u", false, "Do not use Unicode symbols (for size bar)")
	flags.BoolVarP(&af.Summarize, "summarize", "s", false, "Show only a total in non-interactive mode")
	flags.IntVarP(&af.Top, "top", "t", 0, "Show only top X largest files in non-interactive mode")
	flags.IntVar(&af.Depth, "depth", 0, "Show directory structure up to specified depth in non-interactive mode (0 means the flag is ignored)")
	flags.BoolVar(&af.UseSIPrefix, "si", false, "Show sizes with decimal SI prefixes (kB, MB, GB) instead of binary prefixes (KiB, MiB, GiB)")
	flags.BoolVar(&af.NoPrefix, "no-prefix", false, "Show sizes as raw numbers without any prefixes (SI or binary) in non-interactive mode")
	flags.BoolVarP(&af.ShowInKiB, "show-in-kib", "k", false, "Show sizes in KiB (or kB with --si) in non-interactive mode")
	flags.BoolVar(&af.ReverseSort, "reverse-sort", false, "Reverse sorting order (smallest to largest) in non-interactive mode")
	flags.BoolVar(&af.Mouse, "mouse", false, "Use mouse")
	flags.BoolVar(&af.NoDelete, "no-delete", false, "Do not allow deletions")
	flags.BoolVar(&af.NoViewFile, "no-view-file", false, "Do not allow viewing file contents")
	flags.BoolVar(&af.NoSpawnShell, "no-spawn-shell", false, "Do not allow spawning shell")
	flags.BoolVar(&af.WriteConfig, "write-config", false,
		"Write current configuration to file (the config that would be read: an existing "+
			"user config, else ~/.config/gdu/gdu.yaml, creating the directory)")
	flags.StringVar(
		&af.Since, "since", "",
		"Include files with mtime >= WHEN. WHEN accepts RFC3339 timestamp (e.g., 2025-08-11T01:00:00-07:00) "+
			"or date only YYYY-MM-DD (calendar-day compare; includes the whole day)",
	)
	flags.StringVar(&af.Until, "until", "", "Include files with mtime <= WHEN. WHEN accepts RFC3339 timestamp or date only YYYY-MM-DD")
	flags.StringVar(&af.MaxAge, "max-age", "", "Include files with mtime no older than DURATION (e.g., 7d, 2h30m, 1y2mo)")
	flags.StringVar(&af.MinAge, "min-age", "", "Include files with mtime at least DURATION old (e.g., 30d, 1w)")

	initConfig()
	setDefaults()
}

var systemConfigPath = "/etc/gdu.yaml"

func loadConfig(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, &af)
}

func initConfig() {
	// Load system-wide config first (ignored on Windows/Plan9 and when absent).
	if runtime.GOOS != osWindows && runtime.GOOS != osPlan9 {
		if err := loadConfig(systemConfigPath); err != nil && !os.IsNotExist(err) {
			configErr = err
			return
		}
	}

	// Load user config; its values overwrite whatever the system config set.
	setConfigFilePath()
	if err := loadConfig(af.CfgFile); err != nil {
		if !os.IsNotExist(err) {
			configErr = err
		}
		return
	}
}

func setDefaults() {
	if af.Style.Footer.BackgroundColor == "" {
		af.Style.Footer.BackgroundColor = "#2479D0"
	}
	if af.Style.Footer.TextColor == "" {
		af.Style.Footer.TextColor = "#000000"
	}
	if af.Style.Footer.NumberColor == "" {
		af.Style.Footer.NumberColor = "#FFFFFF"
	}
	if af.Style.Header.BackgroundColor == "" {
		af.Style.Header.BackgroundColor = "#2479D0"
	}
	if af.Style.Header.TextColor == "" {
		af.Style.Header.TextColor = "#000000"
	}
	if af.Style.ResultRow.NumberColor == "" {
		af.Style.ResultRow.NumberColor = "#e67100"
	}
	if af.Style.ResultRow.DirectoryColor == "" {
		af.Style.ResultRow.DirectoryColor = "#3498db"
	}
}

func setConfigFilePath() {
	command := strings.Join(os.Args, " ")
	if strings.Contains(command, "--config-file") {
		re := regexp.MustCompile("--config-file[= ]([^ ]+)")
		parts := re.FindStringSubmatch(command)

		if len(parts) > 1 {
			af.CfgFile = parts[1]
			return
		}
	}
	setDefaultConfigFilePath()
}

func setDefaultConfigFilePath() {
	home, err := os.UserHomeDir()
	if err != nil {
		configErr = err
		return
	}

	xdgPath, legacyPath := userConfigPaths(home)
	if _, err := os.Stat(xdgPath); err == nil {
		af.CfgFile = xdgPath
		return
	}

	af.CfgFile = legacyPath
}

// userConfigPaths returns the two user-config candidates, preferred first:
// the XDG path and the legacy home dotfile. It is the single source of these
// locations for both the read path (setDefaultConfigFilePath) and the write
// path (writeConfigTarget) — the two must never disagree, or --write-config
// would write a file gdu never reads back.
func userConfigPaths(home string) (xdgPath, legacyPath string) {
	return filepath.Join(home, ".config", "gdu", "gdu.yaml"), filepath.Join(home, ".gdu.yaml")
}

// writeConfigTarget resolves where --write-config writes when --config-file is
// not given: the user config that would be read — ~/.config/gdu/gdu.yaml when it
// exists, then legacy ~/.gdu.yaml — else the XDG path, creating its directory.
func writeConfigTarget(home string) (string, error) {
	xdgPath, legacyPath := userConfigPaths(home)
	if _, err := os.Stat(xdgPath); err == nil {
		return xdgPath, nil
	}
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(xdgPath), 0o700); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}
	return xdgPath, nil
}

// writeConfig writes the current configuration to the file it would be read
// back from: an explicit --config-file verbatim, else the writeConfigTarget
// resolution.
func writeConfig(command *cobra.Command) error {
	data, err := yaml.Marshal(af)
	if err != nil {
		return fmt.Errorf("error marshaling config file: %w", err)
	}
	path := af.CfgFile
	if !command.Flags().Changed("config-file") {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("determining home dir for config: %w", herr)
		}
		if path, err = writeConfigTarget(home); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("error writing config file %s: %w", path, err)
	}
	return nil
}

// setupLogging routes logrus to --log-file (stdout for "-") and reports any
// config-load error there. The returned cleanup closes the log file.
func setupLogging() (cleanup func(), err error) {
	if runtime.GOOS == osWindows && af.LogFile == "/dev/null" {
		af.LogFile = "nul"
	}

	var f *os.File
	if af.LogFile == "-" {
		f = os.Stdout
		cleanup = func() {}
	} else {
		f, err = os.OpenFile(af.LogFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, fmt.Errorf("opening log file: %w", err)
		}
		cleanup = func() {
			if cerr := f.Close(); cerr != nil {
				panic(cerr)
			}
		}
	}
	log.SetOutput(f)

	if configErr != nil {
		log.Printf("Error reading config file: %s", configErr.Error())
	}
	return cleanup, nil
}

// runSnapshotsAction wraps a snapshots-subcommand action with the shared
// setup: logging to --log-file and an App writing to the command's output.
func runSnapshotsAction(command *cobra.Command, action func(a *app.App) error) error {
	cleanup, err := setupLogging()
	if err != nil {
		return err
	}
	defer cleanup()

	return action(&app.App{Flags: af, Writer: command.OutOrStdout()})
}

// runSnapshotsListE backs `gdu snapshots [list] [file]`.
func runSnapshotsListE(command *cobra.Command, args []string) error {
	file := ""
	if len(args) == 1 {
		file = args[0]
	}
	return runSnapshotsAction(command, func(a *app.App) error { return a.ListSnapshots(file) })
}

// runSnapshotsCompactE backs `gdu snapshots compact [--dry-run]`.
func runSnapshotsCompactE(command *cobra.Command, _ []string) error {
	return runSnapshotsAction(command, func(a *app.App) error { return a.CompactSnapshots(snapshotsDryRun) })
}

func runE(command *cobra.Command, args []string) error {
	var (
		termApp *tview.Application
		screen  tcell.Screen
		err     error
	)

	if af.WriteConfig {
		if err := writeConfig(command); err != nil {
			return err
		}
	}

	cleanup, err := setupLogging()
	if err != nil {
		return err
	}
	defer cleanup()

	istty := isatty.IsTerminal(os.Stdout.Fd())

	// we are not able to analyze disk usage on Windows and Plan9
	if runtime.GOOS == osWindows || runtime.GOOS == osPlan9 {
		af.ShowApparentSize = true
	}

	if !af.ShouldRunInNonInteractiveMode(istty) {
		screen, err = tcell.NewScreen()
		if err != nil {
			return fmt.Errorf("error creating screen: %w", err)
		}
		defer screen.Clear()
		defer screen.Fini()

		termApp = tview.NewApplication()
		termApp.SetScreen(screen)

		if af.Mouse {
			termApp.EnableMouse(true)
		}
	}

	a := app.App{
		Flags:       af,
		Args:        args,
		Istty:       istty,
		Writer:      os.Stdout,
		TermApp:     termApp,
		Screen:      screen,
		Getter:      device.Getter,
		PathChecker: os.Stat,
	}
	return a.Run()
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
