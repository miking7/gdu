package report

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
)

// SnapshotListing is one snapshot discovered in the snapshot archive, tagged with the
// file it lives in (several snapshots may share a compacted file).
type SnapshotListing struct {
	parquet.SnapshotInfo
	File string // base name of the containing snapshot file
}

// ParquetSnapshotsFromFile returns the snapshots in f when it is a Parquet snapshot, or
// nil (no error) when it is not — so a JSON `-f` input simply yields no choices.
// It reads via ReadAt and does not disturb f's read position.
func ParquetSnapshotsFromFile(f *os.File) ([]parquet.SnapshotInfo, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	magic := make([]byte, len(parquetMagic))
	if _, rerr := f.ReadAt(magic, 0); rerr != nil || string(magic) != parquetMagic {
		return nil, nil
	}
	return parquet.ListSnapshots(f, st.Size())
}

// ReadAnalysisSnapshot reads the tree for one specific snapshot (by identity) from a
// Parquet snapshot file — the load step behind the TUI snapshot picker.
func ReadAnalysisSnapshot(f *os.File, info *parquet.SnapshotInfo) (*analyze.Dir, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return parquet.ReadTreeSnapshot(f, st.Size(), info)
}

// MultiSnapshotNote returns a one-line, user-facing note when a Parquet input holds
// several snapshots and none was explicitly requested — reporting which snapshot was
// loaded (the latest) and how to pick another. It returns "" when no note is
// warranted: an explicit selector, JSON or non-seekable input, or a file with a
// single snapshot. Non-interactive callers print it to stderr.
func MultiSnapshotNote(input io.Reader, sel parquet.SnapshotSelector) string {
	if sel.Spec != "" || sel.Root != "" {
		return ""
	}
	f, ok := input.(*os.File)
	if !ok {
		return ""
	}
	snapshots, err := ParquetSnapshotsFromFile(f)
	if err != nil || len(snapshots) <= 1 {
		return ""
	}
	latest, err := parquet.SelectSnapshot(snapshots, sel)
	if err != nil {
		return ""
	}
	return fmt.Sprintf(
		"file contains %d snapshots; loaded the most recent (%s scanned %s). "+
			"Use --snapshot to choose another or `gdu snapshots <file>` to inspect.",
		len(snapshots), latest.ScanRoot, parquet.FormatSnapshotTime(&latest))
}

// PathCoveredBy reports whether target is at or below root, so root's scan
// could contain it. It tolerates roots that already end in a separator (volume
// roots).
func PathCoveredBy(root, target string) bool {
	if target == root {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(root, sep) {
		root += sep
	}
	return strings.HasPrefix(target, root)
}

// RootCoversWithinMount reports whether a snapshot rooted at scanRoot is a
// mount-accurate covering of target: its root must cover target AND lie
// at-or-below target's most-specific mount point, so a whole-disk "/" scan —
// which deliberately ignores nested mounts — is not credited to a folder that
// lives on a different volume. It is the general form of the launcher's
// folder-row mapping, shared by the S picker, the [ ] timeline, and the CLI
// --baseline resolver. An empty mount (device information unavailable)
// degrades to plain path-covering — callers are never worse off.
func RootCoversWithinMount(scanRoot, target, mount string) bool {
	if !PathCoveredBy(scanRoot, target) {
		return false
	}
	if mount == "" {
		return true
	}
	return PathCoveredBy(mount, scanRoot)
}

// ResolveArchiveSnapshot serves `--snapshot <sel>` without -f: resolve spec
// against the archive in dir, over the snapshots whose scan root is exactly
// root. No match or an ambiguous match is an error listing the candidates in
// --snapshot-ready form; when no snapshot of root exists but snapshots of
// covering roots do, those are named as hints — mount-accurately, so a
// folder on one volume is never pointed at another volume's whole-disk scan
// (mount "" degrades to plain path-covering).
func ResolveArchiveSnapshot(dir, spec, root, mount string) (SnapshotListing, error) {
	listings, err := archiveListings(dir)
	if err != nil {
		return SnapshotListing{}, err
	}

	var exact []SnapshotListing
	for i := range listings {
		if listings[i].ScanRoot == root {
			exact = append(exact, listings[i])
		}
	}
	if len(exact) == 0 {
		if covering := latestPerRoot(listings, func(r string) bool { return RootCoversWithinMount(r, root, mount) }); len(covering) > 0 {
			return SnapshotListing{}, fmt.Errorf(
				"the archive has no snapshots of %s, but snapshots of covering roots exist — rerun with one of these paths:\n%s",
				root, formatListings(covering))
		}
		return SnapshotListing{}, fmt.Errorf(
			"the archive has no snapshots of %s; it holds (newest per root):\n%s",
			root, formatListings(latestPerRoot(listings, nil)))
	}
	return selectListing(exact, spec, "--snapshot", "use a longer timestamp")
}

// ResolveArchiveBaseline serves the selector form of `--baseline`: resolve spec
// against the archive in dir, over the snapshots whose root **mount-accurately**
// covers target (root covers target and lies at-or-below target's
// most-specific mount point; mount "" degrades to plain path-covering), the S
// picker's rule and a baseline must cover what you're browsing; or — when
// rootOverride (--baseline-root) is given — over snapshots of exactly that root
// (the deliberate cross-volume override, unaffected by the mount clamp).
// Ambiguity is an error listing the candidates and hinting --baseline-root.
func ResolveArchiveBaseline(dir, spec, target, mount, rootOverride string) (SnapshotListing, error) {
	listings, err := archiveListings(dir)
	if err != nil {
		return SnapshotListing{}, err
	}

	var cands []SnapshotListing
	for i := range listings {
		switch {
		case rootOverride != "":
			if listings[i].ScanRoot == rootOverride {
				cands = append(cands, listings[i])
			}
		case RootCoversWithinMount(listings[i].ScanRoot, target, mount):
			cands = append(cands, listings[i])
		}
	}
	if len(cands) == 0 {
		held := formatListings(latestPerRoot(listings, nil))
		if rootOverride != "" {
			return SnapshotListing{}, fmt.Errorf(
				"the archive has no snapshots of --baseline-root %s; it holds (newest per root):\n%s",
				rootOverride, held)
		}
		return SnapshotListing{}, fmt.Errorf(
			"no archived snapshot on %s's volume covers it; use --baseline-root to compare against "+
				"a scan from another volume. The archive holds (newest per root):\n%s", target, held)
	}
	return selectListing(cands, spec, "--baseline", "use a longer timestamp or --baseline-root")
}

// archiveListings lists the archive for the selector resolvers, folding the
// empty archive into a single clear error. Identical snapshot identities
// appearing in two files (an interrupted compaction leaves a covered daily
// beside its monthly until the next run) are collapsed to one candidate — the
// copies are row-identical by compaction's own rule, and keeping both would
// make an exact-timestamp selector spuriously "ambiguous" between
// indistinguishable lines.
func archiveListings(dir string) ([]SnapshotListing, error) {
	listings, err := ListSnapshotsInDir(dir)
	if err != nil {
		return nil, fmt.Errorf("listing snapshot archive: %w", err)
	}
	seen := make(map[parquet.SnapshotKey]struct{}, len(listings))
	deduped := listings[:0]
	for i := range listings {
		if _, dup := seen[listings[i].Key()]; dup {
			continue
		}
		seen[listings[i].Key()] = struct{}{}
		deduped = append(deduped, listings[i])
	}
	if len(deduped) == 0 {
		return nil, fmt.Errorf("the snapshot archive %s holds no snapshots", dir)
	}
	return deduped, nil
}

// selectListing resolves spec — "latest" (or empty), "earliest", or a
// local-time prefix — over cands (newest first), keeping the containing-file
// tag the loader needs. flag and ambiguityHint parameterize the error texts so
// --snapshot and --baseline each speak their own grammar.
//
// This is the archive-side twin of parquet.SelectSnapshot (which serves -f
// file selection over []SnapshotInfo, oldest first): the two must stay in
// lockstep — a selector accepted with -f must mean the same thing against the
// archive.
func selectListing(cands []SnapshotListing, spec, flag, ambiguityHint string) (SnapshotListing, error) {
	switch spec {
	case "", "latest":
		return cands[0], nil
	case "earliest":
		return cands[len(cands)-1], nil
	}
	var matched []SnapshotListing
	for i := range cands {
		if strings.HasPrefix(parquet.FormatSnapshotTime(&cands[i].SnapshotInfo), spec) {
			matched = append(matched, cands[i])
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return SnapshotListing{}, fmt.Errorf(
			"no archived snapshot matching %s %q; candidates:\n%s", flag, spec, formatListings(cands))
	default:
		return SnapshotListing{}, fmt.Errorf(
			"%s %q is ambiguous (%d snapshots match); %s:\n%s",
			flag, spec, len(matched), ambiguityHint, formatListings(matched))
	}
}

// latestPerRoot returns the newest listing of each distinct root accepted by
// keep (nil keeps every root), newest first (listings must already be sorted
// newest first).
func latestPerRoot(listings []SnapshotListing, keep func(root string) bool) []SnapshotListing {
	seen := make(map[string]struct{})
	var out []SnapshotListing
	for i := range listings {
		root := listings[i].ScanRoot
		if _, dup := seen[root]; dup || (keep != nil && !keep(root)) {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, listings[i])
	}
	return out
}

// formatListings renders listings one per line in the paste-ready form the
// selector errors promise (timestamp + root, plus host when set).
func formatListings(listings []SnapshotListing) string {
	infos := make([]parquet.SnapshotInfo, len(listings))
	for i := range listings {
		infos[i] = listings[i].SnapshotInfo
	}
	return parquet.FormatSnapshotList(infos)
}

// BuildBaselineFromArchive loads one archived snapshot (as returned by the
// resolvers above) and indexes it as a growth-diff baseline.
func BuildBaselineFromArchive(dir string, l *SnapshotListing) (*analyze.Baseline, error) {
	f, err := os.Open(filepath.Join(dir, l.File)) //nolint:gosec // archive path, read-only
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return BuildBaselineForSnapshot(f, &l.SnapshotInfo)
}

// BuildBaselineForSnapshot loads the tree for one snapshot from an open snapshot file and
// indexes it as a growth-diff baseline. The tree's stats are updated so
// directory usages are recursive before indexing.
func BuildBaselineForSnapshot(f *os.File, info *parquet.SnapshotInfo) (*analyze.Baseline, error) {
	tree, err := ReadAnalysisSnapshot(f, info)
	if err != nil {
		return nil, err
	}
	tree.UpdateStats(make(fs.HardLinkedItems))
	return analyze.BuildBaseline(tree, info.ScanRoot, info.ThresholdBytes), nil
}

// BuildBaselineFromFile opens a snapshot file, selects one snapshot with sel (default
// latest), and builds a growth-diff baseline from it. It returns the chosen snapshot's
// identity so the caller can label the baseline.
func BuildBaselineFromFile(path string, sel parquet.SnapshotSelector) (*analyze.Baseline, parquet.SnapshotInfo, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied baseline path, read-only
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	defer f.Close()

	scans, err := ParquetSnapshotsFromFile(f)
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	if len(scans) == 0 {
		return nil, parquet.SnapshotInfo{}, fmt.Errorf("%s is not a gdu Parquet snapshot", path)
	}
	info, err := parquet.SelectSnapshot(scans, sel)
	if err != nil {
		return nil, parquet.SnapshotInfo{}, err
	}
	b, err := BuildBaselineForSnapshot(f, &info)
	return b, info, err
}

// SnapshotPathSize returns the disk usage of target within one archived snapshot, for the
// snapshot picker's contextual "this folder then" column. When target is the scan
// root the total is free from the listing; otherwise the snapshot file is read in
// a single projected pass (path + size columns only, no tree build) to find
// target's row. ok is false when the path did not exist in that snapshot.
func SnapshotPathSize(dir string, l *SnapshotListing, target string) (size int64, ok bool, err error) {
	if target == l.ScanRoot {
		return l.TotalDsize, true, nil
	}
	sizes, perr := pathSizesInFile(filepath.Join(dir, l.File), target)
	if perr != nil {
		return 0, false, perr
	}
	sz, ok := sizes[l.Key()]
	return sz, ok, nil
}

// FolderSizes returns target's disk usage in each of the given snapshots, keyed by
// snapshot identity. It is the eager form of FolderSizesEach, collecting every
// resolved size into a map.
func FolderSizes(dir string, listings []SnapshotListing, target string) map[parquet.SnapshotKey]int64 {
	sizes := make(map[parquet.SnapshotKey]int64, len(listings))
	FolderSizesEach(context.Background(), dir, listings, target,
		func(key parquet.SnapshotKey, size int64) { sizes[key] = size },
		func(parquet.SnapshotKey) {}) // eager callers don't distinguish read errors
	return sizes
}

// FolderSizesEach computes target's disk usage in each of the given snapshots and
// invokes emit(key, size) as each resolves, so a caller can populate a UI
// incrementally rather than waiting for the whole archive. Scans rooted exactly
// at target emit immediately from the (free) footer-manifest total; deeper targets
// are read from each containing snapshot file in a single projected pass — so a
// compacted file holding many snapshots is read once, not once per snapshot. onUnreadable
// is invoked for every snapshot whose containing file could not be read (corrupt,
// permission-denied, or a newer format), so a caller can render those distinctly
// from a snapshot that simply had no row for target (which invokes neither callback).
// Traversal stops early once ctx is cancelled. Both callbacks are invoked
// synchronously from the calling goroutine.
func FolderSizesEach(
	ctx context.Context, dir string, listings []SnapshotListing, target string,
	emit func(key parquet.SnapshotKey, size int64),
	onUnreadable func(key parquet.SnapshotKey),
) {
	byFile := make(map[string][]SnapshotListing)
	for i := range listings {
		if target == listings[i].ScanRoot {
			emit(listings[i].Key(), listings[i].TotalDsize)
			continue
		}
		byFile[listings[i].File] = append(byFile[listings[i].File], listings[i])
	}
	for file, group := range byFile {
		if ctx.Err() != nil {
			return
		}
		perScan, err := pathSizesInFile(filepath.Join(dir, file), target)
		if err != nil {
			// One bad file never aborts the rest; report its snapshots as unreadable.
			for i := range group {
				onUnreadable(group[i].Key())
			}
			continue
		}
		for i := range group {
			if sz, found := perScan[group[i].Key()]; found {
				emit(group[i].Key(), sz)
			}
		}
	}
}

// pathSizesInFile opens one snapshot file and returns target's disk usage per snapshot
// via a single projected column pass.
func pathSizesInFile(path, target string) (map[parquet.SnapshotKey]int64, error) {
	f, err := os.Open(path) //nolint:gosec // archive path, read-only
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return parquet.PathSizes(f, st.Size(), target)
}

// ListSnapshotsInFile returns the snapshots held in a single snapshot file.
func ListSnapshotsInFile(path string) ([]SnapshotListing, error) {
	f, err := os.Open(path) //nolint:gosec // user-supplied snapshot path, read-only
	if err != nil {
		return nil, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return listSnapshotsFrom(f, st.Size(), filepath.Base(path))
}

// ListSnapshotsInBytes returns the snapshots held in an in-memory snapshot (e.g. stdin).
func ListSnapshotsInBytes(raw []byte, name string) ([]SnapshotListing, error) {
	return listSnapshotsFrom(bytes.NewReader(raw), int64(len(raw)), name)
}

// ListSnapshotsInDir returns every snapshot across all *.parquet snapshots in dir,
// newest first. A file that can't be read (corrupt, not a snapshot) is skipped
// with the returned error slice unaffected — one bad file never hides the rest.
func ListSnapshotsInDir(dir string) ([]SnapshotListing, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.parquet"))
	if err != nil {
		return nil, err
	}
	var listings []SnapshotListing
	for _, path := range matches {
		scans, lerr := ListSnapshotsInFile(path)
		if lerr != nil {
			continue // skip unreadable/foreign files rather than abort the listing
		}
		listings = append(listings, scans...)
	}
	sortListingsNewestFirst(listings)
	return listings, nil
}

func listSnapshotsFrom(r io.ReaderAt, size int64, file string) ([]SnapshotListing, error) {
	scans, err := parquet.ListSnapshots(r, size)
	if err != nil {
		return nil, err
	}
	listings := make([]SnapshotListing, len(scans))
	for i := range scans {
		listings[i] = SnapshotListing{SnapshotInfo: scans[i], File: file}
	}
	sortListingsNewestFirst(listings)
	return listings, nil
}

func sortListingsNewestFirst(listings []SnapshotListing) {
	sort.SliceStable(listings, func(i, j int) bool {
		return listings[i].ScanTs.After(listings[j].ScanTs)
	})
}

// PrintSnapshots writes listings as an aligned table. The Host column is shown
// only when some snapshot is from another machine (local snapshots leave
// it blank), and the File column only when the listing spans more than one file
// (both redundant otherwise, e.g. `gdu snapshots one.parquet`).
func PrintSnapshots(w io.Writer, listings []SnapshotListing) error {
	if len(listings) == 0 {
		_, err := fmt.Fprintln(w, "No snapshots found.")
		return err
	}

	localHost := common.HostnameBestEffort()
	showHost := false
	files := make(map[string]struct{})
	for i := range listings {
		if common.HostIsForeign(listings[i].Host, localHost) {
			showHost = true
		}
		files[listings[i].File] = struct{}{}
	}
	showFile := len(files) > 1

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "#\tWHEN\tSIZE\tROWS\tROOT"
	if showHost {
		header += "\tHOST"
	}
	if showFile {
		header += "\tFILE"
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}

	for i := range listings {
		l := &listings[i]
		row := fmt.Sprintf("%d\t%s\t%s\t%d\t%s",
			i+1, parquet.FormatSnapshotTime(&l.SnapshotInfo), formatBinarySize(l.TotalDsize), l.Rows, l.ScanRoot)
		if showHost {
			host := ""
			if common.HostIsForeign(l.Host, localHost) {
				host = l.Host
			}
			row += "\t" + host
		}
		if showFile {
			row += "\t" + l.File
		}
		if _, err := fmt.Fprintln(tw, row); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// formatBinarySize renders a byte count with binary (KiB/MiB/…) units, matching
// gdu's default size display.
func formatBinarySize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1f EiB", value/unit)
}
