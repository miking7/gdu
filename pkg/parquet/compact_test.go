package parquet

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	pq "github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/fs"
)

// compactNow is "today" for all compaction tests: July 2026 in UTC, so May and
// June 2026 are closed months and July is the open one.
var compactNow = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

// writeDaily writes sampleTree as one snapshot file in dir, stamped with the
// given identity, and returns its path.
func writeDaily(t *testing.T, dir, name string, ts time.Time, host string) string {
	t.Helper()
	root := sampleTree()
	meta := ScanMeta{
		ScanRoot: root.GetPath(),
		ScanTime: ts.UTC(),
		Host:     host,
		Username: "u",
	}
	return writeDailyTree(t, dir, name, root, meta)
}

func writeDailyTree(t *testing.T, dir, name string, root fs.Item, meta ScanMeta) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, WriteTree(f, root, &meta))
	require.NoError(t, f.Close())
	return path
}

func listFileSnapshots(t *testing.T, path string) []SnapshotInfo {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	st, err := f.Stat()
	require.NoError(t, err)
	scans, err := ListSnapshots(f, st.Size())
	require.NoError(t, err)
	return scans
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPlanCompactionGroupsClosedMonthsOnly(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_a.parquet", time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")
	writeDaily(t, dir, "snapshot_b.parquet", time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC), "h1")
	writeDaily(t, dir, "snapshot_c.parquet", time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC), "h1")
	open := writeDaily(t, dir, "snapshot_open.parquet", time.Date(2026, 7, 3, 8, 0, 0, 0, time.UTC), "h1")

	plan, err := PlanCompaction(dir, compactNow)
	require.NoError(t, err)
	require.Len(t, plan.Groups, 1)
	assert.Empty(t, plan.Skipped)

	g := plan.Groups[0]
	assert.Equal(t, "2026-05", g.Month)
	assert.Equal(t, "/tmp/root", g.ScanRoot)
	assert.Equal(t, "h1", g.Host)
	assert.Len(t, g.Inputs, 3)
	assert.Empty(t, g.Redundant)
	assert.Len(t, g.Snapshots, 3)
	assert.True(t, g.MergeNeeded)
	assert.Equal(t, filepath.Join(dir, "monthly_2026-05_tmp_root.parquet"), g.OutputPath)
	assert.NotContains(t, g.Inputs, open, "the open month must never participate")

	// Planning writes nothing.
	assert.Len(t, dirEntries(t, dir), 4)
}

func TestRunCompactionGoldenMerge(t *testing.T) {
	dir := t.TempDir()
	timestamps := []time.Time{
		time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 12, 9, 30, 0, 0, time.UTC),
		time.Date(2026, 5, 20, 23, 45, 0, 0, time.UTC),
	}
	for i, ts := range timestamps {
		writeDaily(t, dir, fmt.Sprintf("snapshot_%d.parquet", i), ts, "h1")
	}

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	require.NoError(t, res.Groups[0].Err)
	assert.Len(t, res.Groups[0].Deleted, 3)
	assert.Positive(t, res.OutputBytes)

	monthly := filepath.Join(dir, "monthly_2026-05_tmp_root.parquet")
	assert.Equal(t, []string{"monthly_2026-05_tmp_root.parquet"}, dirEntries(t, dir),
		"sources removed, only the monthly remains")

	// The monthly carries a manifest with all three snapshots, oldest first.
	scans := listFileSnapshots(t, monthly)
	require.Len(t, scans, 3)
	for i, ts := range timestamps {
		assert.True(t, scans[i].ScanTs.Equal(ts), "scan %d timestamp", i)
		assert.Equal(t, int64(5), scans[i].Rows)
	}

	// Every scan still loads tree-identically via its identity.
	want := sampleTree()
	f, err := os.Open(monthly)
	require.NoError(t, err)
	defer f.Close()
	st, err := f.Stat()
	require.NoError(t, err)
	for i := range scans {
		got, rerr := ReadTreeSnapshot(f, st.Size(), &scans[i])
		require.NoError(t, rerr)
		got.UpdateStats(make(fs.HardLinkedItems))
		assert.Equal(t, want.GetUsage(), got.GetUsage(), "scan %d usage", i)
		kids := childItems(got)
		assert.Contains(t, kids, "big")
		assert.Contains(t, kids, "sub")
	}

	// Output is globally sorted by (path, scan_ts) and declares it per group.
	raw, err := os.ReadFile(monthly)
	require.NoError(t, err)
	assertRowGroupsSorted(t, raw)
	assertGloballySorted(t, raw)
}

// TestRunCompactionPreservesErrCount checks that the manifest's per-snapshot
// read-error count survives the monthly merge (it rounds through the union of
// input manifests — no recount).
func TestRunCompactionPreservesErrCount(t *testing.T) {
	dir := t.TempDir()
	timestamps := []time.Time{
		time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 12, 9, 30, 0, 0, time.UTC),
	}
	for i, ts := range timestamps {
		meta := ScanMeta{ScanRoot: "/tmp/root", ScanTime: ts.UTC(), Host: "h1", Username: "u"}
		writeDailyTree(t, dir, fmt.Sprintf("snapshot_%d.parquet", i), readErrorTree(), meta)
	}

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	require.NoError(t, res.Groups[0].Err)

	scans := listFileSnapshots(t, filepath.Join(dir, "monthly_2026-05_tmp_root.parquet"))
	require.Len(t, scans, 2)
	for i := range scans {
		assert.Equal(t, int64(2), scans[i].ErrCount, "scan %d err count preserved", i)
	}
}

// assertGloballySorted checks the whole-file (path, scan_ts) order that the
// compaction merge (unlike dailies) guarantees.
func assertGloballySorted(t *testing.T, data []byte) {
	t.Helper()
	rows, err := pq.Read[Row](bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		ordered := prev.Path < cur.Path || (prev.Path == cur.Path && prev.ScanTs <= cur.ScanTs)
		assert.True(t, ordered, "row %d (%q, %d) sorts after row %d (%q, %d)",
			i-1, prev.Path, prev.ScanTs, i, cur.Path, cur.ScanTs)
	}
}

func TestRunCompactionSingleDailyWraps(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_only.parquet", time.Date(2026, 6, 1, 0, 30, 0, 0, time.UTC), "h1")

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	require.NoError(t, res.Groups[0].Err)
	assert.Equal(t, []string{"monthly_2026-06_tmp_root.parquet"}, dirEntries(t, dir))
}

func TestRunCompactionIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_a.parquet", time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")
	writeDaily(t, dir, "snapshot_b.parquet", time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC), "h1")

	_, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	monthly := filepath.Join(dir, "monthly_2026-05_tmp_root.parquet")
	before, err := os.ReadFile(monthly)
	require.NoError(t, err)

	// Second run: nothing to do, monthly untouched byte for byte.
	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Groups)
	after, err := os.ReadFile(monthly)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(before, after))
}

func TestRunCompactionStragglerRemerge(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_a.parquet", time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")
	writeDaily(t, dir, "snapshot_b.parquet", time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC), "h1")
	_, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)

	// A straggler for the already-compacted month appears (e.g. copied in from
	// a laptop that was offline). The monthly is just another sorted input.
	writeDaily(t, dir, "snapshot_late.parquet", time.Date(2026, 5, 30, 8, 0, 0, 0, time.UTC), "h1")

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	require.NoError(t, res.Groups[0].Err)

	monthly := filepath.Join(dir, "monthly_2026-05_tmp_root.parquet")
	assert.Equal(t, []string{"monthly_2026-05_tmp_root.parquet"}, dirEntries(t, dir))
	scans := listFileSnapshots(t, monthly)
	assert.Len(t, scans, 3)
}

func TestRunCompactionLegacyUnsortedInput(t *testing.T) {
	dir := t.TempDir()

	// A pre-7a-style file: no manifest, no sorting declaration, rows unsorted.
	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC).UnixMilli()
	rows := rawScanRows("/data", "zfile", "h1", ts, 4096)
	rows[0], rows[1] = rows[1], rows[0] // deliberately out of path order
	buf := writeRawRows(t, rows)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "snapshot_legacy.parquet"), buf.Bytes(), 0o600))

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	require.NoError(t, res.Groups[0].Err)

	assert.Equal(t, []string{"monthly_2026-05_data.parquet"}, dirEntries(t, dir),
		"legacy source removed, rewrite temp cleaned up")

	monthly := filepath.Join(dir, "monthly_2026-05_data.parquet")
	scans := listFileSnapshots(t, monthly)
	require.Len(t, scans, 1)
	assert.Equal(t, "/data", scans[0].ScanRoot)
	assert.Equal(t, int64(2), scans[0].Rows)
	raw, err := os.ReadFile(monthly)
	require.NoError(t, err)
	assertRowGroupsSorted(t, raw)
}

func TestRunCompactionRedundantDailyDeletedNotMerged(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC)
	writeDaily(t, dir, "snapshot_a.parquet", ts, "h1")
	writeDaily(t, dir, "snapshot_b.parquet", time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC), "h1")
	_, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)

	// Simulate a crash between rename and source deletion: a daily whose scan
	// is already inside the monthly reappears. It must be deleted, not merged
	// (merging would double its rows).
	writeDaily(t, dir, "snapshot_a.parquet", ts, "h1")

	monthly := filepath.Join(dir, "monthly_2026-05_tmp_root.parquet")
	before, err := os.ReadFile(monthly)
	require.NoError(t, err)

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	g := res.Groups[0]
	require.NoError(t, g.Err)
	assert.False(t, g.Group.MergeNeeded)
	assert.Equal(t, []string{filepath.Join(dir, "snapshot_a.parquet")}, g.Group.Redundant)

	assert.Equal(t, []string{"monthly_2026-05_tmp_root.parquet"}, dirEntries(t, dir))
	after, err := os.ReadFile(monthly)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(before, after), "monthly must be untouched")
	scans := listFileSnapshots(t, monthly)
	assert.Len(t, scans, 2, "no doubled scan")
}

func TestRunCompactionMultiHostGetsHostSlugs(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_h1.parquet", time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "mbp.local")
	writeDaily(t, dir, "snapshot_h2.parquet", time.Date(2026, 5, 11, 8, 0, 0, 0, time.UTC), "nas")

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 2)
	for _, g := range res.Groups {
		require.NoError(t, g.Err)
	}
	assert.ElementsMatch(t, []string{
		"monthly_2026-05_tmp_root_mbp_local.parquet",
		"monthly_2026-05_tmp_root_nas.parquet",
	}, dirEntries(t, dir))
}

func TestPlanCompactionSkipsMultiGroupFile(t *testing.T) {
	dir := t.TempDir()

	// One file holding snapshots from two different months (only an external tool
	// could produce this): it must never be partially consumed.
	may := rawScanRows("/data", "f", "h1", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), 100)
	june := rawScanRows("/data", "f", "h1", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), 200)
	buf := writeRawRows(t, append(may, june...))
	path := filepath.Join(dir, "snapshot_span.parquet")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	plan, err := PlanCompaction(dir, compactNow)
	require.NoError(t, err)
	assert.Empty(t, plan.Groups)
	require.Len(t, plan.Skipped, 1)
	assert.Equal(t, path, plan.Skipped[0].Path)
	assert.Contains(t, plan.Skipped[0].Reason, "spans several")

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Groups)
	assert.FileExists(t, path, "skipped files must never be deleted")
}

func TestRunCompactionCorruptSourceSkippedOthersProceed(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "snapshot_corrupt.parquet")
	require.NoError(t, os.WriteFile(corrupt, []byte("PAR1 this is not parquet"), 0o600))
	writeDaily(t, dir, "snapshot_ok.parquet", time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	require.NoError(t, res.Groups[0].Err)
	require.Len(t, res.Plan.Skipped, 1)
	assert.Contains(t, res.Plan.Skipped[0].Reason, "unreadable")

	assert.FileExists(t, corrupt, "corrupt files must never be deleted")
	assert.FileExists(t, filepath.Join(dir, "monthly_2026-05_tmp_root.parquet"))
}

func TestPlanCompactionSkipsForeignParquet(t *testing.T) {
	dir := t.TempDir()
	// Valid Parquet, but no scan identity columns worth the name: rows decode
	// with scan_root "" and scan_ts 0. Must not be swallowed into 1970-01.
	buf := writeRawRows(t, []Row{{Path: "/x", Name: "x", Asize: 1, Dsize: 1}})
	path := filepath.Join(dir, "foreign.parquet")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	plan, err := PlanCompaction(dir, compactNow)
	require.NoError(t, err)
	assert.Empty(t, plan.Groups)
	require.Len(t, plan.Skipped, 1)
	assert.Contains(t, plan.Skipped[0].Reason, "no scan identity")
}

func TestRunCompactionAbortsWhenInputManifestLies(t *testing.T) {
	dir := t.TempDir()

	// A snapshot whose manifest overstates its row count: the merge total won't
	// match, the group must abort and the source must survive.
	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	rows := rawScanRows("/data", "f", "h1", ts.UnixMilli(), 4096)
	sortChunk(rows)
	var buf bytes.Buffer
	pw := newSnapshotWriter(&buf)
	_, err := pw.Write(rows)
	require.NoError(t, err)
	manifest, err := marshalManifest([]SnapshotInfo{{
		ScanRoot: "/data", ScanTs: ts, Host: "h1", Rows: 999, TotalDsize: 4096,
	}})
	require.NoError(t, err)
	pw.SetKeyValueMetadata(FormatKey, FormatVersion)
	pw.SetKeyValueMetadata(SnapshotsKey, manifest)
	require.NoError(t, pw.Close())
	path := filepath.Join(dir, "snapshot_liar.parquet")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	require.Error(t, res.Groups[0].Err)
	assert.Empty(t, res.Groups[0].Deleted)

	assert.Equal(t, []string{"snapshot_liar.parquet"}, dirEntries(t, dir),
		"source intact, no tmp or monthly left behind")
}

func TestVerifyCompactedDetectsMismatch(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	path := writeDaily(t, dir, "snapshot_v.parquet", ts, "h1")
	good := listFileSnapshots(t, path)
	require.Len(t, good, 1)

	require.NoError(t, verifyCompacted(path, good))

	wrongRows := good[0]
	wrongRows.Rows++
	assert.ErrorContains(t, verifyCompacted(path, []SnapshotInfo{wrongRows}), "rows")

	wrongSize := good[0]
	wrongSize.TotalDsize++
	assert.ErrorContains(t, verifyCompacted(path, []SnapshotInfo{wrongSize}), "total")

	wrongScan := good[0]
	wrongScan.Host = "other"
	assert.ErrorContains(t, verifyCompacted(path, []SnapshotInfo{wrongScan}), "snapshot set mismatch")

	assert.ErrorContains(t, verifyCompacted(path, nil), "snapshots")
}

func TestRunCompactionLockContention(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_a.parquet", time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")

	// A live lock (our own pid) blocks the run.
	release, err := acquireCompactionLock(dir, compactNow)
	require.NoError(t, err)
	_, err = RunCompaction(dir, compactNow, nil)
	assert.ErrorContains(t, err, "another compaction")
	release()

	// Releasing lets it proceed.
	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.NoError(t, res.Groups[0].Err)
}

func TestCompactionLockStaleReclaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, compactLockName)

	// Dead pid on this host: reclaimable immediately.
	host, _ := os.Hostname()
	data := marshalLock(lockInfo{PID: 1 << 30, Host: host, Time: compactNow.Format(time.RFC3339)})
	require.NoError(t, os.WriteFile(path, data, 0o600))
	release, err := acquireCompactionLock(dir, compactNow)
	require.NoError(t, err, "dead-pid lock must be reclaimed")
	release()

	// Fresh lock from another host with an unknown pid: not reclaimable.
	data = marshalLock(lockInfo{PID: 1234, Host: "elsewhere", Time: compactNow.Format(time.RFC3339)})
	require.NoError(t, os.WriteFile(path, data, 0o600))
	_, err = acquireCompactionLock(dir, compactNow)
	assert.ErrorContains(t, err, "another compaction")

	// ...until it outlives the TTL (staleness is judged by file mtime).
	old := compactNow.Add(-2 * compactLockTTL)
	require.NoError(t, os.Chtimes(path, old, old))
	release, err = acquireCompactionLock(dir, compactNow)
	require.NoError(t, err, "TTL-expired lock must be reclaimed")
	release()
}

func marshalLock(l lockInfo) []byte {
	return []byte(fmt.Sprintf(`{"pid":%d,"host":%q,"time":%q}`, l.PID, l.Host, l.Time))
}

func TestRunCompactionRemovesStaleTemps(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "monthly_2026-04_x.parquet.tmp")
	require.NoError(t, os.WriteFile(stale, []byte("partial"), 0o600))

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Groups)
	assert.NoFileExists(t, stale)
}

func TestRunCompactionMultipleRootsSameMonth(t *testing.T) {
	dir := t.TempDir()
	ts1 := time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC).UnixMilli()
	ts2 := time.Date(2026, 5, 11, 8, 0, 0, 0, time.UTC).UnixMilli()
	for name, rows := range map[string][]Row{
		"snapshot_data.parquet": rawScanRows("/data", "f1", "h1", ts1, 100),
		"snapshot_home.parquet": rawScanRows("/home", "f2", "h1", ts2, 200),
	} {
		buf := writeRawRows(t, rows)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600))
	}

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 2)
	for _, g := range res.Groups {
		require.NoError(t, g.Err)
	}
	assert.ElementsMatch(t, []string{
		"monthly_2026-05_data.parquet",
		"monthly_2026-05_home.parquet",
	}, dirEntries(t, dir))
}

func TestRunCompactionMissingDirIsNothingToDo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err, "a missing archive dir must behave like an empty one")
	assert.Empty(t, res.Groups)
	assert.NoDirExists(t, dir, "compaction must not create the archive dir")
}

func TestRunCompactionMismatchedDuplicateSkipsGroup(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC)
	writeDaily(t, dir, "snapshot_full.parquet", ts, "h1")

	// Same scan identity, different content: the tree exported with a rollup
	// threshold (fewer rows, different threshold_bytes). Neither file may be
	// merged (rows would double) nor deleted (each is the only copy of its
	// variant), so the whole group must be left alone.
	root := sampleTree()
	meta := ScanMeta{ScanRoot: root.GetPath(), ScanTime: ts.UTC(), Host: "h1",
		Username: "u", ThresholdBytes: 10 * mib}
	writeDailyTree(t, dir, "snapshot_thresh.parquet", root, meta)

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	assert.Empty(t, res.Groups)
	require.Len(t, res.Plan.Skipped, 2)
	for _, s := range res.Plan.Skipped {
		assert.Contains(t, s.Reason, "without being identical")
	}
	assert.ElementsMatch(t, []string{"snapshot_full.parquet", "snapshot_thresh.parquet"}, dirEntries(t, dir))
}

func TestPlanCompactionSkipsNewerFormat(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	rows := rawScanRows("/data", "f", "h1", ts.UnixMilli(), 4096)
	sortChunk(rows)
	var buf bytes.Buffer
	pw := newSnapshotWriter(&buf)
	_, err := pw.Write(rows)
	require.NoError(t, err)
	manifest, err := marshalManifest([]SnapshotInfo{{
		ScanRoot: "/data", ScanTs: ts, Host: "h1", Rows: 2, TotalDsize: 4096,
	}})
	require.NoError(t, err)
	pw.SetKeyValueMetadata(FormatKey, "3") // written by a hypothetical newer gdu
	pw.SetKeyValueMetadata(SnapshotsKey, manifest)
	require.NoError(t, pw.Close())
	path := filepath.Join(dir, "snapshot_future.parquet")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	plan, err := PlanCompaction(dir, compactNow)
	require.NoError(t, err)
	assert.Empty(t, plan.Groups)
	require.Len(t, plan.Skipped, 1)
	assert.Contains(t, plan.Skipped[0].Reason, "format 3")
	assert.FileExists(t, path)
}

func TestRunCompactionDeleteOnlyVerifiesMonthlyRows(t *testing.T) {
	dir := t.TempDir()

	// A monthly whose manifest lies about its row count, plus a byte-identical
	// leftover daily. The daily classifies as redundant, but the delete-only
	// path must still prove the monthly's rows before pruning — and abort here.
	ts := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	rows := rawScanRows("/data", "f", "h1", ts.UnixMilli(), 4096)
	sortChunk(rows)
	var buf bytes.Buffer
	pw := newSnapshotWriter(&buf)
	_, err := pw.Write(rows)
	require.NoError(t, err)
	manifest, err := marshalManifest([]SnapshotInfo{{
		ScanRoot: "/data", ScanTs: ts, Host: "h1", Rows: 999, TotalDsize: 4096,
	}})
	require.NoError(t, err)
	pw.SetKeyValueMetadata(FormatKey, FormatVersion)
	pw.SetKeyValueMetadata(SnapshotsKey, manifest)
	require.NoError(t, pw.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "monthly_2026-05_data.parquet"), buf.Bytes(), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "snapshot_dup.parquet"), buf.Bytes(), 0o600))

	res, err := RunCompaction(dir, compactNow, nil)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	g := res.Groups[0]
	assert.False(t, g.Group.MergeNeeded)
	require.Error(t, g.Err)
	assert.Contains(t, g.Err.Error(), "verification")
	assert.Empty(t, g.Deleted)
	assert.ElementsMatch(t, []string{"monthly_2026-05_data.parquet", "snapshot_dup.parquet"}, dirEntries(t, dir))
}

func TestPlanCompactionEmptyAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	plan, err := PlanCompaction(dir, compactNow)
	require.NoError(t, err)
	assert.Empty(t, plan.Groups)
	assert.Empty(t, plan.Skipped)

	// A missing archive dir is an empty plan, not an error (glob matches nothing).
	plan, err = PlanCompaction(filepath.Join(dir, "nope"), compactNow)
	require.NoError(t, err)
	assert.Empty(t, plan.Groups)
}

func TestMonthlyTargetPathCollisionSuffix(t *testing.T) {
	dir := t.TempDir()
	key := groupKey{host: "h1", root: "/data", month: "2026-05"}

	// Unrelated file already owns the natural name: suffix, never overwrite.
	existing := filepath.Join(dir, "monthly_2026-05_data.parquet")
	require.NoError(t, os.WriteFile(existing, []byte("x"), 0o600))
	got := monthlyTargetPath(dir, key, false, map[string]bool{}, map[string]struct{}{})
	assert.Equal(t, filepath.Join(dir, "monthly_2026-05_data_1.parquet"), got)

	// But when that file is one of the group's own inputs (straggler re-merge),
	// the name is reused.
	got = monthlyTargetPath(dir, key, false, map[string]bool{existing: true}, map[string]struct{}{})
	assert.Equal(t, existing, got)

	// A name claimed by an earlier group this run is also avoided.
	taken := map[string]struct{}{existing: {}}
	got = monthlyTargetPath(dir, key, false, map[string]bool{existing: true}, taken)
	assert.Equal(t, filepath.Join(dir, "monthly_2026-05_data_1.parquet"), got)
}
