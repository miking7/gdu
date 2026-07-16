package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
)

// applyOwner applies --owner (make written output belong to the named user)
// when set. It must run before anything resolves the snapshots-dir or writes a
// file.
func (a *App) applyOwner() error {
	if a.Flags.Owner == "" {
		return nil
	}
	if err := common.ApplyOwnerOverride(a.Flags.Owner); err != nil {
		return fmt.Errorf("invalid --owner: %w", err)
	}
	return nil
}

// ListSnapshots implements `gdu snapshots [list] [file]`: print the snapshots in
// one file ("-" reads stdin), or — with no file — the whole snapshot archive
// (--snapshots-dir). It is the CLI twin of the TUI snapshot browser and the
// discovery tool for --snapshot values.
func (a *App) ListSnapshots(file string) error {
	if err := a.applyOwner(); err != nil {
		return err
	}
	var (
		listings []report.SnapshotListing
		err      error
	)
	switch {
	case file == "-":
		raw, rerr := io.ReadAll(os.Stdin)
		if rerr != nil {
			return fmt.Errorf("reading stdin: %w", rerr)
		}
		listings, err = report.ListSnapshotsInBytes(raw, "-")
	case file != "":
		listings, err = report.ListSnapshotsInFile(file)
	default:
		dir, derr := a.resolveSnapshotsDir()
		if derr != nil {
			return derr
		}
		listings, err = report.ListSnapshotsInDir(dir)
	}
	if err != nil {
		return fmt.Errorf("listing snapshots: %w", err)
	}
	return report.PrintSnapshots(a.Writer, listings)
}

// CompactSnapshots implements `gdu snapshots compact [--dry-run]`: merge every
// closed month's snapshots into one monthly file per (host, scan root, month),
// verify, then prune the sources — or, with dryRun, print the plan and write
// nothing.
func (a *App) CompactSnapshots(dryRun bool) error {
	if err := a.applyOwner(); err != nil {
		return err
	}
	a.setMaxProcs() // honor -m for the merge/compression work

	dir, err := a.resolveSnapshotsDir()
	if err != nil {
		return err
	}

	if dryRun {
		plan, perr := parquet.PlanCompaction(dir, time.Now())
		if perr != nil {
			return fmt.Errorf("planning compaction: %w", perr)
		}
		return report.PrintCompactionPlan(a.Writer, plan)
	}

	progress := func(format string, args ...interface{}) {
		fmt.Fprintf(a.Writer, format+"\n", args...)
	}
	result, err := parquet.RunCompaction(dir, time.Now(), progress)
	if err != nil {
		return fmt.Errorf("compacting snapshots: %w", err)
	}
	return report.PrintCompactionResult(a.Writer, result)
}

// resolveSnapshotsDir returns the directory for saved snapshots: the
// --snapshots-dir flag if set, otherwise the XDG data dir
// ($XDG_DATA_HOME/gdu/snapshots, falling back to ~/.local/share/gdu/snapshots).
func (a *App) resolveSnapshotsDir() (string, error) {
	if a.Flags.SnapshotsDir != "" {
		return a.Flags.SnapshotsDir, nil
	}
	// Under sudo (or --owner), anchor to the invoking user's home, not $HOME
	// (which may be /root). Their XDG_DATA_HOME environment is unknowable from
	// here and deliberately not consulted.
	if _, _, realHome, ok := common.RealUser(); ok && realHome != "" {
		return filepath.Join(realHome, ".local", "share", "gdu", "snapshots"), nil
	}
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "gdu", "snapshots"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home dir for snapshots: %w", err)
	}
	return filepath.Join(home, ".local", "share", "gdu", "snapshots"), nil
}

// loadArchiveSnapshot serves `--snapshot <sel>` without -f: resolve sel against
// the archive for snapshots whose root is exactly path (or --snapshot-root when
// given) and feed the containing file to the UI, pinned to the resolved
// snapshot's identity.
func (a *App) loadArchiveSnapshot(ui UI, path string) error {
	dir, err := a.resolveSnapshotsDir()
	if err != nil {
		return err
	}
	root := path
	if a.Flags.SnapshotRoot != "" {
		root = a.Flags.SnapshotRoot
	}
	listing, err := report.ResolveArchiveSnapshot(dir, a.Flags.Snapshot, root, a.mountForPath(root))
	if err != nil {
		return err
	}

	f, err := os.Open(filepath.Join(dir, listing.File)) //nolint:gosec // archive path, read-only
	if err != nil {
		return fmt.Errorf("opening snapshot file: %w", err)
	}
	// Pin the load to the resolved snapshot's full identity so a multi-snapshot
	// (compacted) file loads exactly the chosen one — a textual selector would
	// be lossy here (second precision, DST-fold collisions) — and never falls
	// into the TUI's file picker.
	ui.SetSnapshotIdentity(listing.ScanRoot, listing.Host, listing.ScanTs)
	if err := ui.ReadAnalysis(f); err != nil {
		return fmt.Errorf("reading snapshot: %w", err)
	}
	return nil
}

// resolveBaseline turns the --baseline value into a loaded growth-diff
// baseline. A value naming an existing file loads that file (multi-snapshot
// file → latest, with a stderr note); anything else is a selector resolved
// against the archive, scoped to snapshots whose root covers path (a baseline
// must cover what you're browsing), or to --baseline-root exactly when given.
func (a *App) resolveBaseline(path string) (*analyze.Baseline, parquet.SnapshotInfo, error) {
	if st, err := os.Stat(a.Flags.Baseline); err == nil && !st.IsDir() {
		return a.resolveBaselineFile()
	}

	dir, err := a.resolveSnapshotsDir()
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	listing, err := report.ResolveArchiveBaseline(dir, a.Flags.Baseline, path, a.mountForPath(path), a.Flags.BaselineRoot)
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	b, err := report.BuildBaselineFromArchive(dir, &listing)
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	return b, listing.SnapshotInfo, nil
}

// mountForPath resolves p's most-specific mount point best-effort, for the D17
// mount-accurate scoping of --baseline / --snapshot covering. Enumeration
// failure (or no getter) yields "", and the resolvers then degrade to plain
// path-covering — so these flags keep working where mount discovery is flaky.
func (a *App) mountForPath(p string) string {
	if a.Getter == nil {
		return ""
	}
	mounts, err := a.Getter.GetMounts()
	if err != nil {
		log.Printf("resolving mount for %s failed, using path-covering: %s", p, err)
		return ""
	}
	if d := device.ForPath(mounts, p); d != nil {
		return d.MountPoint
	}
	return ""
}

// resolveBaselineFile loads the --baseline file form: always the file's latest
// snapshot (picking within a file is intentionally not supported — the archive
// selector covers that), noting on stderr when the file held several. The file
// is listed once and the chosen snapshot loaded by identity, so the (possibly
// expensive, for foreign files) footer listing is never repeated.
func (a *App) resolveBaselineFile() (*analyze.Baseline, parquet.SnapshotInfo, error) {
	if a.Flags.BaselineRoot != "" {
		return nil, parquet.SnapshotInfo{}, fmt.Errorf(
			"--baseline-root only scopes a selector; %q is a file (which always loads its latest snapshot)",
			a.Flags.Baseline)
	}
	listings, err := report.ListSnapshotsInFile(a.Flags.Baseline)
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	if len(listings) == 0 {
		return nil, parquet.SnapshotInfo{}, fmt.Errorf("%s is not a gdu Parquet snapshot", a.Flags.Baseline)
	}
	latest := listings[0] // ListSnapshotsInFile sorts newest first
	if len(listings) > 1 {
		fmt.Fprintf(os.Stderr, "gdu: baseline file contains %d snapshots; using the most recent (%s scanned %s)\n",
			len(listings), latest.ScanRoot, parquet.FormatSnapshotTime(&latest.SnapshotInfo))
	}
	b, err := report.BuildBaselineFromArchive(filepath.Dir(a.Flags.Baseline), &latest)
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	return b, latest.SnapshotInfo, nil
}
