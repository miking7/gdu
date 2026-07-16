package parquet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"github.com/parquet-go/parquet-go"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/common"
)

// Compaction merges each closed local-time month's snapshot files — grouped by
// (host, scan_root, month) — into one monthly Parquet file sorted globally by
// (path, scan_ts), then deletes the sources. It is lossless repacking: every
// scan in the inputs is present, row for row, in the output (verified before
// anything is deleted). The open month is never touched, so compaction can
// never race a --save-snapshots write.

const (
	// compactLockName is the archive-wide lockfile ensuring one compactor at a
	// time. --save-snapshots never takes it (it only ever writes unique-named files
	// into the open month, which compaction never reads).
	compactLockName = ".gdu-compact.lock"
	// compactLockTTL is how old a lockfile may grow before it is presumed
	// abandoned (crashed run on another host, where liveness can't be probed).
	compactLockTTL = 24 * time.Hour
	// monthLayout formats the local-time month groups are keyed by. Lexical
	// order equals chronological order, so closed months compare with <.
	monthLayout = "2006-01"
)

// CompactionGroup is one unit of compaction work: every archived snapshot of one
// scan root on one host within one closed local-time month, plus the files
// currently holding those snapshots.
type CompactionGroup struct {
	Host     string
	ScanRoot string
	Month    string // local-time month, e.g. "2026-05"

	// Inputs are the files whose row groups are merged into the output. Each
	// participating file's snapshots all belong to this group (single-group rule),
	// so whole files feed the merge — never per-row filtering.
	Inputs []string
	// Redundant are files whose every snapshot is already covered by Inputs (e.g. a
	// daily left behind by a crash after a previous run's rename). They are not
	// merged (that would double their rows) and are deleted once the group
	// succeeds.
	Redundant []string
	// Snapshots is the union of the group's snapshot manifests, oldest first. It is the
	// expected content of the output and what verification checks against.
	Snapshots []SnapshotInfo
	// InputBytes is the total on-disk size of Inputs plus Redundant.
	InputBytes int64
	// OutputPath is the monthly file this group produces (it may equal one of
	// Inputs when re-merging stragglers into an existing monthly).
	OutputPath string
	// MergeNeeded is false when Inputs is exactly the up-to-date monthly and
	// only Redundant cleanup remains.
	MergeNeeded bool
}

// SkippedFile records a file the planner refused to touch and why. Skipped
// files are never deleted.
type SkippedFile struct {
	Path   string
	Reason string
}

// CompactionPlan lists the work `gdu snapshots compact` would perform on an archive.
type CompactionPlan struct {
	Groups  []CompactionGroup
	Skipped []SkippedFile
}

// GroupResult reports the outcome of compacting one group.
type GroupResult struct {
	Group       CompactionGroup
	OutputBytes int64
	Deleted     []string // sources removed after the output was verified
	DeleteErrs  []string // sources that could not be removed (retried next run)
	Err         error    // non-nil when the group was aborted with sources intact
}

// CompactionResult is what RunCompaction did: the plan it executed and the
// per-group outcomes. InputBytes/OutputBytes sum over the groups that
// succeeded, giving the before/after archive footprint of this run.
type CompactionResult struct {
	Plan        *CompactionPlan
	Groups      []GroupResult
	InputBytes  int64
	OutputBytes int64
}

// groupKey is the comparable (host, scan_root, local month) grouping tuple.
type groupKey struct {
	host, root, month string
}

// planFile is one archive file as seen by the planner.
type planFile struct {
	path      string
	size      int64
	snapshots []SnapshotInfo
	key       groupKey
	sorted    bool   // every row group declares the (path, scan_ts) order
	manifest  bool   // footer manifest present
	format    string // FormatKey footer value; "" when absent (legacy)
}

// PlanCompaction inspects snapshotsDir and returns the compaction work due at now.
// Only closed months — strictly before now's month in now's location — are
// considered, and a file participates only when all its snapshots fall into one
// (host, scan_root, month) group; anything else is skipped with a reason and
// left untouched. now is a parameter so the local-month arithmetic is testable.
func PlanCompaction(snapshotsDir string, now time.Time) (*CompactionPlan, error) {
	matches, err := filepath.Glob(filepath.Join(snapshotsDir, "*.parquet"))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)

	plan := &CompactionPlan{}
	currentMonth := now.Format(monthLayout)
	loc := now.Location()

	// hosts per (root, month) across every readable file — including files that
	// end up skipped or already compacted — decides whether monthly filenames
	// need a host slug to stay collision-free.
	hosts := make(map[[2]string]map[string]struct{})
	files := collectPlanFiles(matches, currentMonth, loc, hosts, plan)

	byKey := make(map[groupKey][]*planFile)
	for _, f := range files {
		byKey[f.key] = append(byKey[f.key], f)
	}
	keys := make([]groupKey, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.month != b.month {
			return a.month < b.month
		}
		if a.root != b.root {
			return a.root < b.root
		}
		return a.host < b.host
	})

	taken := make(map[string]struct{}) // outputs already claimed by earlier groups this run
	for _, key := range keys {
		multiHost := len(hosts[[2]string{key.root, key.month}]) > 1
		if group := buildGroup(snapshotsDir, key, byKey[key], multiHost, taken, plan); group != nil {
			plan.Groups = append(plan.Groups, *group)
		}
	}
	return plan, nil
}

// collectPlanFiles inspects every archive file and returns the ones eligible
// for compaction — readable, current-format, identified, single-group, closed
// month — recording each rejection in plan.Skipped (those files are never
// touched) and every snapshot's host into hosts for the filename slug decision.
func collectPlanFiles(
	matches []string, currentMonth string, loc *time.Location,
	hosts map[[2]string]map[string]struct{}, plan *CompactionPlan,
) []*planFile {
	var files []*planFile
	for _, path := range matches {
		pf, ierr := inspectSnapshot(path)
		if ierr != nil {
			plan.Skipped = append(plan.Skipped, SkippedFile{path, "unreadable: " + ierr.Error()})
			continue
		}
		if len(pf.snapshots) == 0 {
			plan.Skipped = append(plan.Skipped, SkippedFile{path, "contains no snapshots"})
			continue
		}
		if pf.format != "" && pf.format != FormatVersion {
			// A newer gdu wrote this file; merging it through this binary's schema
			// would silently drop columns this version doesn't know about.
			plan.Skipped = append(plan.Skipped, SkippedFile{
				path, fmt.Sprintf("written with snapshot format %s (this gdu understands %s); upgrade gdu to compact it",
					pf.format, FormatVersion),
			})
			continue
		}
		if !snapshotsIdentified(pf.snapshots) {
			plan.Skipped = append(plan.Skipped, SkippedFile{path, "not a gdu snapshot (rows carry no scan identity)"})
			continue
		}
		pf.key = snapshotGroupKey(&pf.snapshots[0], loc)
		spansGroups := false
		for i := range pf.snapshots {
			s := &pf.snapshots[i]
			key := snapshotGroupKey(s, loc)
			if key != pf.key {
				spansGroups = true
			}
			rm := [2]string{s.ScanRoot, key.month}
			if hosts[rm] == nil {
				hosts[rm] = make(map[string]struct{})
			}
			hosts[rm][s.Host] = struct{}{}
		}
		if spansGroups {
			plan.Skipped = append(plan.Skipped, SkippedFile{path, "spans several (host, scan root, month) groups"})
			continue
		}
		if pf.key.month >= currentMonth {
			continue // open (or future-clock) month: never touched
		}
		files = append(files, pf)
	}
	return files
}

// snapshotGroupKey buckets a snapshot into its (host, root, local month) group.
func snapshotGroupKey(s *SnapshotInfo, loc *time.Location) groupKey {
	return groupKey{host: s.Host, root: s.ScanRoot, month: s.ScanTs.In(loc).Format(monthLayout)}
}

// snapshotsIdentified reports whether every snapshot carries a usable identity — a
// non-empty root and a real timestamp. Foreign Parquet files (no gdu columns)
// decode to zero identities and must never be swallowed into a monthly.
func snapshotsIdentified(scans []SnapshotInfo) bool {
	for i := range scans {
		if scans[i].ScanRoot == "" || scans[i].ScanTs.UnixMilli() == 0 {
			return false
		}
	}
	return true
}

// buildGroup turns one (host, root, month) bucket of files into a work item,
// or nil when the bucket is already fully compacted. Files whose snapshots are all
// duplicates of already-covered snapshots become Redundant (delete-only); a file
// overlapping only partially can't be consumed whole and skips the group.
func buildGroup(
	snapshotsDir string, key groupKey, fis []*planFile, multiHost bool,
	taken map[string]struct{}, plan *CompactionPlan,
) *CompactionGroup {
	// Deterministic greedy cover: the file with the most snapshots first (an
	// existing monthly), then lexical path order.
	sort.SliceStable(fis, func(i, j int) bool {
		if len(fis[i].snapshots) != len(fis[j].snapshots) {
			return len(fis[i].snapshots) > len(fis[j].snapshots)
		}
		return fis[i].path < fis[j].path
	})

	var (
		inputs    []*planFile
		redundant []string
		union     []SnapshotInfo
		bytes     int64
	)
	for _, fi := range fis {
		fresh, dups := 0, 0
		for i := range fi.snapshots {
			switch classifySnapshot(union, &fi.snapshots[i]) {
			case snapshotFresh:
				fresh++
			case snapshotDuplicate:
				dups++
			}
		}
		switch {
		case fresh == len(fi.snapshots):
			inputs = append(inputs, fi)
			union = append(union, fi.snapshots...)
		case dups == len(fi.snapshots):
			// Every snapshot is an exact duplicate (identity AND rows/totals) of one
			// already covered — e.g. a daily left behind by a crash between a
			// previous run's rename and its source deletion. Safe to prune once
			// the group succeeds; merging it would double its rows.
			redundant = append(redundant, fi.path)
		default:
			// Partial overlap, or a same-identity snapshot with different content
			// (say, the same snapshot exported at two thresholds): consuming a file
			// whole would double rows, deleting it could destroy the only copy of
			// its variant. Leave the whole group alone for the user to resolve.
			for _, f := range fis {
				plan.Skipped = append(plan.Skipped, SkippedFile{
					f.path, fmt.Sprintf("group %s %s: snapshot sets overlap across files without being identical",
						key.month, key.root),
				})
			}
			return nil
		}
		bytes += fi.size
	}
	sortSnapshots(union)

	inputPaths := make(map[string]bool, len(inputs))
	for _, in := range inputs {
		inputPaths[in.path] = true
	}
	output := monthlyTargetPath(snapshotsDir, key, multiHost, inputPaths, taken)
	mergeNeeded := len(inputs) != 1 || inputs[0].path != output ||
		!inputs[0].sorted || !inputs[0].manifest
	if !mergeNeeded && len(redundant) == 0 {
		return nil // already compacted, nothing to do
	}
	taken[output] = struct{}{}

	group := &CompactionGroup{
		Host:        key.host,
		ScanRoot:    key.root,
		Month:       key.month,
		Redundant:   redundant,
		Snapshots:   union,
		InputBytes:  bytes,
		OutputPath:  output,
		MergeNeeded: mergeNeeded,
	}
	for _, in := range inputs {
		group.Inputs = append(group.Inputs, in.path)
	}
	return group
}

// Classification of one snapshot against the union already covered by a group's
// chosen inputs.
const (
	snapshotFresh     = iota // identity not covered yet
	snapshotDuplicate        // covered by a snapshot with identical rows/totals — a true copy
	snapshotConflict         // same identity but different content — never assume a copy
)

// classifySnapshot compares snapshot against the covered union. Identity alone is
// not enough to call a snapshot a duplicate: only a matching row count, total and
// threshold make it safe to treat the file holding it as prunable.
func classifySnapshot(union []SnapshotInfo, snapshot *SnapshotInfo) int {
	for i := range union {
		u := &union[i]
		if !u.SameSnapshot(snapshot) {
			continue
		}
		if u.Rows == snapshot.Rows && u.TotalDsize == snapshot.TotalDsize && u.ThresholdBytes == snapshot.ThresholdBytes {
			return snapshotDuplicate
		}
		return snapshotConflict
	}
	return snapshotFresh
}

// monthlyTargetPath names a group's output: monthly_<yyyy-mm>_<rootslug>
// [_<hostslug>].parquet. The host slug appears only when several hosts share
// the (root, month) in the archive. An existing file that is not one of the
// group's own inputs never gets overwritten — a numeric suffix is appended,
// like snapshot filenames.
func monthlyTargetPath(
	dir string, key groupKey, withHost bool, inputPaths map[string]bool, taken map[string]struct{},
) string {
	stem := "monthly_" + key.month + "_" + rootSlug(key.root)
	if withHost {
		stem += "_" + rootSlug(key.host)
	}
	candidate := filepath.Join(dir, stem+".parquet")
	for i := 1; ; i++ {
		if _, planned := taken[candidate]; !planned && (inputPaths[candidate] || !fileExists(candidate)) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s_%d.parquet", stem, i))
	}
}

// inspectSnapshot reads one archive file's snapshot list and merge-readiness from
// its footer (plus, for legacy manifest-less multi-snapshot files, a cheap column
// projection).
func inspectSnapshot(path string) (*planFile, error) {
	f, err := os.Open(path) //nolint:gosec // archive-dir path from our own glob
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	pf, err := parquet.OpenFile(f, st.Size(),
		parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
	if err != nil {
		return nil, err
	}
	scans, err := listSnapshots(pf)
	if err != nil {
		return nil, err
	}
	_, hasManifest := pf.Lookup(SnapshotsKey)
	format, _ := pf.Lookup(FormatKey)
	return &planFile{
		path:      path,
		size:      st.Size(),
		snapshots: scans,
		sorted:    allRowGroupsSorted(pf),
		manifest:  hasManifest,
		format:    format,
	}, nil
}

// allRowGroupsSorted reports whether every row group declares the
// (path, scan_ts) ascending order the compaction merge relies on. Files
// written by gdu since the multi-snapshot groundwork do; legacy or external files
// typically don't and get a streaming sorted rewrite first.
func allRowGroupsSorted(pf *parquet.File) bool {
	for _, rg := range pf.RowGroups() {
		sc := rg.SortingColumns()
		if len(sc) < 2 || !ascendingOn(sc[0], "path") || !ascendingOn(sc[1], "scan_ts") {
			return false
		}
	}
	return true
}

func ascendingOn(c parquet.SortingColumn, column string) bool {
	path := c.Path()
	return len(path) == 1 && path[0] == column && !c.Descending()
}

// RunCompaction executes the compaction due at now on snapshotsDir: it takes the
// archive lock, clears stale temp files, re-plans, then per group merges,
// verifies the output against the input manifests, atomically renames it into
// place and only then deletes the sources. A group that fails is reported in
// the result with its sources intact; other groups still run. progress (may be
// nil) receives one printf-style line per step for CLI display.
func RunCompaction(
	snapshotsDir string, now time.Time, progress func(format string, args ...interface{}),
) (*CompactionResult, error) {
	return RunCompactionContext(context.Background(), snapshotsDir, now, progress)
}

// RunCompactionContext is RunCompaction with cooperative cancellation, for
// background (auto-compact) runs a user may abort. Cancelling is always safe:
// an in-flight group discards its tmp file and keeps its sources; groups that
// already completed stay completed (their deletions are the final, idempotent
// step — anything interrupted re-merges as a no-op next run). The partial
// result and ctx's error are returned.
func RunCompactionContext(
	ctx context.Context, snapshotsDir string, now time.Time, progress func(format string, args ...interface{}),
) (*CompactionResult, error) {
	if progress == nil {
		progress = func(string, ...interface{}) {}
	}

	// A missing archive dir simply means there is nothing to compact — the same
	// answer --dry-run gives — not an error creating the lockfile inside it.
	if _, err := os.Stat(snapshotsDir); os.IsNotExist(err) {
		return &CompactionResult{Plan: &CompactionPlan{}}, nil
	}

	release, err := acquireCompactionLock(snapshotsDir, now)
	if err != nil {
		return nil, err
	}
	defer release()

	removeStaleTemps(snapshotsDir)

	plan, err := PlanCompaction(snapshotsDir, now)
	if err != nil {
		return nil, err
	}

	res := &CompactionResult{Plan: plan}
	for i := range plan.Groups {
		if err := ctx.Err(); err != nil {
			return res, err // aborted: remaining groups untouched, sources intact
		}
		g := &plan.Groups[i]
		host := ""
		if g.Host != "" {
			host = " on " + g.Host
		}
		progress("compacting %s %s%s: %d files, %d snapshots", g.Month, g.ScanRoot, host,
			len(g.Inputs)+len(g.Redundant), len(g.Snapshots))
		gr := compactGroup(ctx, snapshotsDir, g)
		if gr.Err == nil {
			res.InputBytes += g.InputBytes
			res.OutputBytes += gr.OutputBytes
			progress("  -> %s", filepath.Base(g.OutputPath))
		}
		// Failures are not narrated here — the result (and its printer) owns
		// outcome reporting, so each failure is shown exactly once.
		res.Groups = append(res.Groups, gr)
	}
	return res, nil
}

// compactGroup merges (when needed), verifies, renames, then prunes one
// group's sources. Deletion failures are collected, not fatal: a survivor
// re-merges as a no-op next run.
func compactGroup(ctx context.Context, snapshotsDir string, g *CompactionGroup) GroupResult {
	gr := GroupResult{Group: *g}

	if g.MergeNeeded {
		outBytes, err := mergeGroup(ctx, snapshotsDir, g)
		if err != nil {
			gr.Err = err
			return gr
		}
		gr.OutputBytes = outBytes
	} else {
		// Delete-only group: the monthly claims (via its manifest) to hold every
		// scan the redundant files carry. Prove it from the rows before pruning —
		// the manifest is the claim, the rows are the evidence.
		if err := verifyCompacted(g.OutputPath, g.Snapshots); err != nil {
			gr.Err = fmt.Errorf("existing monthly failed verification: %w", err)
			return gr
		}
		if st, err := os.Stat(g.OutputPath); err == nil {
			gr.OutputBytes = st.Size()
		}
	}

	for _, path := range append(append([]string{}, g.Inputs...), g.Redundant...) {
		if path == g.OutputPath {
			continue // the straggler re-merge case: the old monthly was renamed over
		}
		if err := os.Remove(path); err != nil {
			// E.g. Windows can't unlink an open file. Safe to leave: its snapshots are
			// duplicates of the monthly now, so the next run treats it as Redundant.
			log.Printf("compaction: could not remove source %s: %s (will retry next run)", path, err)
			gr.DeleteErrs = append(gr.DeleteErrs, fmt.Sprintf("%s: %s", path, err))
		} else {
			gr.Deleted = append(gr.Deleted, path)
		}
	}
	return gr
}

// openInput is one open merge input; temp inputs are sorted rewrites of legacy
// files and are removed on close.
type openInput struct {
	f    *os.File
	pf   *parquet.File
	path string
	temp bool
}

// mergeGroup streams the group's row groups through a sorted merge into
// OutputPath+".tmp", stamps the union manifest, fsyncs, verifies the result
// row-for-row against the input manifests, and atomically renames it into
// place. Sources are not touched; the caller deletes them afterwards.
//
//nolint:gocyclo,funlen // linear safety sequence; splitting would obscure the ordering
func mergeGroup(ctx context.Context, snapshotsDir string, g *CompactionGroup) (outBytes int64, err error) {
	var inputs []*openInput
	closeInputs := func() {
		for _, in := range inputs {
			in.f.Close()
			if in.temp {
				os.Remove(in.path)
			}
		}
		inputs = nil
	}
	defer closeInputs()

	var rowGroups []parquet.RowGroup
	for _, path := range g.Inputs {
		if cerr := ctx.Err(); cerr != nil {
			return 0, cerr
		}
		in, oerr := openMergeInput(ctx, path, snapshotsDir)
		if oerr != nil {
			return 0, fmt.Errorf("opening %s: %w", filepath.Base(path), oerr)
		}
		inputs = append(inputs, in)
		rowGroups = append(rowGroups, in.pf.RowGroups()...)
	}

	// The explicit target schema pins column order and folds legacy layouts in
	// (missing columns decode as zero values); the sorting config makes this a
	// streaming k-way merge whose output arrives globally (path, scan_ts)
	// ordered. Peak memory is the open inputs' page buffers, independent of
	// total row count — but it scales with the merge fan-in, which is the
	// number of row groups (one per ~64K rows per input, since daily chunk
	// ranges overlap). Fine for threshold>0 archives (the designed use); a
	// threshold-0 archive of multi-million-row dailies would want a bounded
	// fan-in multi-pass merge here — noted in the plan doc as follow-up work.
	merged, err := parquet.MergeRowGroups(rowGroups,
		parquet.SchemaOf(Row{}),
		parquet.SortingRowGroupConfig(parquet.SortingColumns(
			parquet.Ascending("path"),
			parquet.Ascending("scan_ts"),
		)),
	)
	if err != nil {
		return 0, fmt.Errorf("merging row groups: %w", err)
	}

	tmpPath := g.OutputPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // path derived from archive dir
	if err != nil {
		return 0, err
	}
	discardTmp := func(ferr error) (int64, error) {
		out.Close()
		os.Remove(tmpPath)
		return 0, ferr
	}

	pw := newSnapshotWriter(out)
	rows := merged.Rows()
	// The copy is the long streaming phase; the ctx wrapper makes an abort take
	// effect within one read batch instead of only between groups.
	copied, err := parquet.CopyRows(pw, &ctxRowReader{ctx: ctx, rows: rows})
	rows.Close()
	if err != nil {
		return discardTmp(fmt.Errorf("writing merged rows: %w", err))
	}
	var want int64
	for i := range g.Snapshots {
		want += g.Snapshots[i].Rows
	}
	if copied != want {
		return discardTmp(fmt.Errorf("merged %d rows, inputs declare %d", copied, want))
	}

	manifest, err := marshalManifest(g.Snapshots)
	if err != nil {
		return discardTmp(err)
	}
	pw.SetKeyValueMetadata(FormatKey, FormatVersion)
	pw.SetKeyValueMetadata(SnapshotsKey, manifest)
	if err := pw.Close(); err != nil {
		return discardTmp(err)
	}
	if err := out.Sync(); err != nil {
		return discardTmp(err)
	}
	if err := out.Close(); err != nil {
		return discardTmp(err) // double Close inside is a harmless ErrClosed
	}

	// Release the sources before verify/rename: Windows can't rename over or
	// delete files that are still open.
	closeInputs()

	if err := verifyCompacted(tmpPath, g.Snapshots); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("verification failed: %w", err)
	}
	if err := os.Rename(tmpPath, g.OutputPath); err != nil {
		os.Remove(tmpPath)
		return 0, err
	}
	syncDir(snapshotsDir)
	common.ChownToInvoker(g.OutputPath)

	st, err := os.Stat(g.OutputPath)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// ctxRowReader forwards ReadRows until its context is cancelled, so a long
// streaming copy aborts within one batch.
type ctxRowReader struct {
	ctx  context.Context
	rows parquet.Rows
}

// ReadRows implements parquet.RowReader with a cancellation check per batch.
func (r *ctxRowReader) ReadRows(buf []parquet.Row) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.rows.ReadRows(buf)
}

// openMergeInput opens one source for merging, first rewriting it to a sorted
// temp file when its row groups don't declare the required order.
func openMergeInput(ctx context.Context, path, snapshotsDir string) (*openInput, error) {
	in, err := openSnapshotFile(path, false)
	if err != nil {
		return nil, err
	}
	if allRowGroupsSorted(in.pf) {
		return in, nil
	}

	log.Printf("compaction: %s has unsorted row groups; rewriting sorted first", filepath.Base(path))
	tempPath, err := rewriteSorted(ctx, in.pf, snapshotsDir)
	in.f.Close()
	if err != nil {
		return nil, fmt.Errorf("sorted rewrite: %w", err)
	}
	rewritten, err := openSnapshotFile(tempPath, true)
	if err != nil {
		os.Remove(tempPath)
		return nil, err
	}
	return rewritten, nil
}

// openSnapshotFile opens path as a Parquet file (page indexes kept — the merge
// uses them to detect non-overlapping row groups).
func openSnapshotFile(path string, temp bool) (*openInput, error) {
	f, err := os.Open(path) //nolint:gosec // archive-dir path
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	pf, err := parquet.OpenFile(f, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	return &openInput{f: f, pf: pf, path: path, temp: temp}, nil
}

// rewriteSorted streams pf's rows into a fresh temp file in dir written as
// chunk-sorted row groups declaring (path, scan_ts) — the shape the merge
// requires — normalising legacy schemas to the current Row layout on the way.
// Memory stays bounded at one chunk; this path only runs for pre-7a or foreign
// files and ages out of the archive as they compact.
func rewriteSorted(ctx context.Context, pf *parquet.File, dir string) (string, error) {
	tmp, err := os.CreateTemp(dir, ".gdu-rewrite-*.tmp")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	fail := func(ferr error) (string, error) {
		tmp.Close()
		os.Remove(path)
		return "", ferr
	}

	reader := parquet.NewGenericReader[Row](pf)
	defer reader.Close()

	pw := newSnapshotWriter(tmp)
	chunk := make([]Row, 0, sortChunkRows)
	buf := make([]Row, readBatchSize)
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		sortChunk(chunk)
		if _, werr := pw.Write(chunk); werr != nil {
			return werr
		}
		chunk = chunk[:0]
		return pw.Flush()
	}
	for {
		if cerr := ctx.Err(); cerr != nil {
			return fail(cerr)
		}
		n, readErr := reader.Read(buf)
		for i := 0; i < n; i++ {
			chunk = append(chunk, buf[i])
			if len(chunk) >= sortChunkRows {
				if ferr := flush(); ferr != nil {
					return fail(ferr)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return fail(readErr)
		}
	}
	if err := flush(); err != nil {
		return fail(err)
	}
	if err := pw.Close(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// verifyCompacted proves the freshly written monthly is complete before any
// source is deleted. It re-opens the file and recomputes the scan list from
// the rows themselves — deliberately ignoring the manifest just stamped, since
// the manifest is the claim and the rows are the evidence — then requires the
// exact snapshot set of the inputs with identical per-snapshot row counts and totals.
func verifyCompacted(path string, expected []SnapshotInfo) error {
	f, err := os.Open(path) //nolint:gosec // our own tmp file
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	pf, err := parquet.OpenFile(f, st.Size(),
		parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
	if err != nil {
		return err
	}
	actual, err := fallbackSnapshotList(pf)
	if err != nil {
		return err
	}
	sortSnapshots(actual)

	if len(actual) != len(expected) {
		return fmt.Errorf("output holds %d snapshots, inputs declare %d", len(actual), len(expected))
	}
	for i := range expected {
		e, a := &expected[i], &actual[i]
		if !e.SameSnapshot(a) {
			return fmt.Errorf("snapshot set mismatch: output has %s %s (%s), want %s %s (%s)",
				FormatSnapshotTime(a), a.ScanRoot, a.Host, FormatSnapshotTime(e), e.ScanRoot, e.Host)
		}
		if a.Rows != e.Rows {
			return fmt.Errorf("snapshot %s %s: output has %d rows, input declared %d",
				FormatSnapshotTime(e), e.ScanRoot, a.Rows, e.Rows)
		}
		if a.TotalDsize != e.TotalDsize {
			return fmt.Errorf("snapshot %s %s: output total %d bytes, input declared %d",
				FormatSnapshotTime(e), e.ScanRoot, a.TotalDsize, e.TotalDsize)
		}
	}
	return nil
}

// removeStaleTemps clears *.tmp leftovers from crashed runs. Only called under
// the archive lock, so it can't race a live compactor's tmp files.
func removeStaleTemps(snapshotsDir string) {
	matches, err := filepath.Glob(filepath.Join(snapshotsDir, "*.tmp"))
	if err != nil {
		return
	}
	for _, path := range matches {
		if err := os.Remove(path); err == nil {
			log.Printf("compaction: removed stale temp file %s", path)
		}
	}
}

// syncDir fsyncs a directory so a just-completed rename survives a crash.
// Best-effort: directories can't be fsynced on Windows.
func syncDir(dir string) {
	d, err := os.Open(dir) //nolint:gosec // archive dir
	if err != nil {
		return
	}
	if err := d.Sync(); err != nil {
		log.Printf("compaction: could not fsync %s: %s", dir, err)
	}
	if err := d.Close(); err != nil {
		log.Printf("compaction: could not close %s: %s", dir, err)
	}
}

// ErrCompactionLocked is returned when another compactor holds the archive
// lock. Auto-compaction treats it as "skip silently"; the explicit
// `gdu snapshots compact` surfaces it to the user.
var ErrCompactionLocked = errors.New("another compaction appears to be running")

// lockInfo is the JSON body of the compaction lockfile.
type lockInfo struct {
	PID  int    `json:"pid"`
	Host string `json:"host"`
	Time string `json:"time"` // RFC3339, informational; staleness uses file mtime
}

// acquireCompactionLock takes the archive-wide compaction lock, reclaiming a
// stale one (dead pid on this host, or older than compactLockTTL) once. The
// returned release func removes the lockfile.
func acquireCompactionLock(snapshotsDir string, now time.Time) (release func(), err error) {
	path := filepath.Join(snapshotsDir, compactLockName)
	for attempt := 0; ; attempt++ {
		f, oerr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // lockfile in archive dir
		if oerr == nil {
			data, merr := json.Marshal(lockInfo{
				PID: os.Getpid(), Host: common.HostnameBestEffort(), Time: now.Format(time.RFC3339),
			})
			werr := merr
			if werr == nil {
				_, werr = f.Write(data)
			}
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				os.Remove(path)
				return nil, werr
			}
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(oerr) {
			return nil, oerr
		}
		if attempt > 0 || !compactionLockStale(path, now) {
			return nil, fmt.Errorf(
				"%w (lockfile %s); remove it if no gdu is active", ErrCompactionLocked, path)
		}
		// Reclaim by rename, not remove: rename is atomic, so when two compactors
		// both judge the same lock stale only one rename succeeds — the loser
		// backs off instead of deleting the winner's freshly created lock.
		stale := fmt.Sprintf("%s.stale.%d", path, os.Getpid())
		if rerr := os.Rename(path, stale); rerr != nil {
			return nil, fmt.Errorf(
				"%w (lockfile %s); remove it if no gdu is active", ErrCompactionLocked, path)
		}
		os.Remove(stale)
		log.Printf("compaction: reclaimed stale lockfile %s", path)
	}
}

// compactionLockStale reports whether an existing lockfile can be reclaimed:
// its recorded pid provably no longer runs on this host, or the file has
// outlived the TTL (covering crashes on other hosts and unreadable locks).
func compactionLockStale(path string, now time.Time) bool {
	st, err := os.Stat(path)
	if err != nil {
		return true // vanished meanwhile; O_EXCL still arbitrates the retry
	}
	if now.Sub(st.ModTime()) > compactLockTTL {
		return true
	}
	raw, err := os.ReadFile(path) //nolint:gosec // lockfile in archive dir
	if err != nil {
		return false
	}
	var l lockInfo
	if json.Unmarshal(raw, &l) != nil {
		return false
	}
	return lockOwnerDead(&l)
}

// lockOwnerDead reports whether the lock's recorded pid provably no longer
// runs. Only ever true on the same host, and never on Windows, where signal
// probing is unsupported (the TTL handles staleness there).
func lockOwnerDead(l *lockInfo) bool {
	if runtime.GOOS == "windows" || l.PID <= 0 {
		return false
	}
	if l.Host == "" || l.Host != common.HostnameBestEffort() {
		return false
	}
	proc, err := os.FindProcess(l.PID)
	if err != nil {
		return true
	}
	err = proc.Signal(syscall.Signal(0))
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}
