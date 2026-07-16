package parquet

import (
	"bytes"
	"testing"
	"time"

	pq "github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRawRows writes rows with a plain GenericWriter — no footer manifest, no
// sorting — mimicking snapshots written before the multi-snapshot groundwork.
func writeRawRows(t *testing.T, rows []Row) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	w := pq.NewGenericWriter[Row](&buf)
	_, err := w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return &buf
}

// rawScanRows builds a minimal two-row scan (root dir + one file) stamped with
// the given identity.
func rawScanRows(rootPath, fileName, host string, tsMs, usage int64) []Row {
	total := usage
	return []Row{
		{
			Path: rootPath, Parent: "/", Name: rootPath[1:], IsDir: true, Depth: 0,
			DirTotalDsize: &total,
			ScanRoot:      rootPath, ScanTs: tsMs, Host: host,
		},
		{
			Path: rootPath + "/" + fileName, Parent: rootPath, Name: fileName, Depth: 1,
			Asize: usage, Dsize: usage,
			ScanRoot: rootPath, ScanTs: tsMs, Host: host,
		},
	}
}

func TestWriteTreeManifestRoundTrip(t *testing.T) {
	root := sampleTree()
	meta := ScanMeta{
		ScanRoot:       root.GetPath(),
		ScanTime:       time.Unix(1700000000, 0).UTC(),
		ThresholdBytes: 4096,
		Host:           "h1",
		Username:       "u1",
		SudoUser:       "alice",
	}
	var buf bytes.Buffer
	require.NoError(t, WriteTree(&buf, root, &meta))

	scans, err := ListSnapshots(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, scans, 1)

	s := scans[0]
	assert.Equal(t, root.GetPath(), s.ScanRoot)
	assert.True(t, s.ScanTs.Equal(meta.ScanTime))
	assert.Equal(t, "h1", s.Host)
	assert.Equal(t, "u1", s.Username)
	assert.Equal(t, "alice", s.SudoUser)
	assert.Equal(t, int64(4096), s.ThresholdBytes)
	assert.Equal(t, root.GetUsage(), s.TotalDsize)

	rows, err := pq.Read[Row](bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	assert.Equal(t, int64(len(rows)), s.Rows)

	pf, err := pq.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	version, ok := pf.Lookup(FormatKey)
	assert.True(t, ok)
	assert.Equal(t, FormatVersion, version)
}

func TestListSnapshotsFallbackWithoutManifest(t *testing.T) {
	older := rawScanRows("/a", "afile", "h1", 1000, 111)
	newer := rawScanRows("/b", "bfile", "h2", 2000, 222)
	// Interleave the write order to prove grouping doesn't rely on it.
	buf := writeRawRows(t, append(newer, older...))

	scans, err := ListSnapshots(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, scans, 2)

	// Oldest first.
	assert.Equal(t, "/a", scans[0].ScanRoot)
	assert.Equal(t, time.UnixMilli(1000).UTC(), scans[0].ScanTs)
	assert.Equal(t, int64(2), scans[0].Rows)
	assert.Equal(t, int64(111), scans[0].TotalDsize)

	assert.Equal(t, "/b", scans[1].ScanRoot)
	assert.Equal(t, int64(2), scans[1].Rows)
	assert.Equal(t, int64(222), scans[1].TotalDsize)
}

// openFile opens buf with the same options ListSnapshots uses, so tests exercise
// the stats fast path exactly as production does.
func openFile(t *testing.T, buf *bytes.Buffer) *pq.File {
	t.Helper()
	pf, err := pq.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()),
		pq.SkipPageIndex(true), pq.SkipBloomFilters(true))
	require.NoError(t, err)
	return pf
}

func TestStatsSnapshotListSingleSnapshot(t *testing.T) {
	// Manifest-less single-scan file: the fast path must resolve it from footer
	// statistics alone, including TotalDsize from the dir_total_dsize column max.
	buf := writeRawRows(t, rawScanRows("/data", "f", "host9", 1700, 4242))

	scans, ok := statsSnapshotList(openFile(t, buf))
	require.True(t, ok, "single-scan file should resolve from stats")
	require.Len(t, scans, 1)
	assert.Equal(t, "/data", scans[0].ScanRoot)
	assert.Equal(t, "host9", scans[0].Host)
	assert.Equal(t, time.UnixMilli(1700).UTC(), scans[0].ScanTs)
	assert.Equal(t, int64(2), scans[0].Rows)
	assert.Equal(t, int64(4242), scans[0].TotalDsize)
}

func TestListSnapshotsFormat1FileUsesForeignTiers(t *testing.T) {
	// A pre-rename format-1 file carries the retired gdu.scans manifest key, which
	// the format-2 reader must NOT read (it looks up gdu.snapshots). Stamp a
	// deliberately wrong gdu.scans manifest and confirm the reader ignores it,
	// deriving the snapshot from the statistics tier (the foreign-file path).
	rows := rawScanRows("/data", "f", "h1", 1700, 4242)
	sortChunk(rows)
	var buf bytes.Buffer
	pw := newSnapshotWriter(&buf)
	_, err := pw.Write(rows)
	require.NoError(t, err)
	// A lie in the old key: wrong root and 999 rows — must never surface.
	badManifest, err := marshalManifest([]SnapshotInfo{{
		ScanRoot: "/wrong", ScanTs: time.UnixMilli(1700).UTC(), Host: "h1", Rows: 999, TotalDsize: 1,
	}})
	require.NoError(t, err)
	pw.SetKeyValueMetadata(FormatKey, "1")
	pw.SetKeyValueMetadata("gdu.scans", badManifest) // the retired format-1 key
	require.NoError(t, pw.Close())

	// ListSnapshots looks up gdu.snapshots (absent here) and falls to the foreign
	// tiers rather than trusting the old gdu.scans key.
	snaps, err := ListSnapshots(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, "/data", snaps[0].ScanRoot, "real root, not the retired-key lie")
	assert.Equal(t, int64(2), snaps[0].Rows, "real row count, not the retired-key lie")
	assert.Equal(t, int64(4242), snaps[0].TotalDsize)

	// It also still loads as a tree.
	tree, err := ReadTree(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	assert.Equal(t, "data", tree.GetName())
}

func TestStatsSnapshotListFallsBackOnMultipleRoots(t *testing.T) {
	buf := writeRawRows(t, append(
		rawScanRows("/a", "af", "h1", 1000, 111),
		rawScanRows("/b", "bf", "h1", 2000, 222)...,
	))
	_, ok := statsSnapshotList(openFile(t, buf))
	assert.False(t, ok, "multi-root file must defer to the row scan")
}

func TestStatsSnapshotListFallsBackOnMultipleHosts(t *testing.T) {
	// Same root and timestamp, two hosts — a synced/shared archive. host is
	// multi-valued, so stats can't collapse it to one scan.
	buf := writeRawRows(t, append(
		rawScanRows("/r", "af", "hostA", 1000, 111),
		rawScanRows("/r", "bf", "hostB", 1000, 222)...,
	))
	_, ok := statsSnapshotList(openFile(t, buf))
	assert.False(t, ok, "multi-host file must defer to the row scan")
}

func TestStatsSnapshotListLegacyNoHostColumn(t *testing.T) {
	// Pre-identity schema (no host column): the fast path still applies, with an
	// empty host, matching how the row scan and ReadTree treat these files.
	total := int64(999)
	var buf bytes.Buffer
	w := pq.NewGenericWriter[legacyRow](&buf)
	_, err := w.Write([]legacyRow{
		{Path: "/old", Parent: "/", Name: "old", IsDir: true, Depth: 0, DirTotalDsize: &total, ScanRoot: "/old", ScanTs: 5000},
		{Path: "/old/f", Parent: "/old", Name: "f", Depth: 1, Asize: 999, Dsize: 999, ScanRoot: "/old", ScanTs: 5000},
	})
	require.NoError(t, err)
	require.NoError(t, w.Close())

	scans, ok := statsSnapshotList(openFile(t, &buf))
	require.True(t, ok)
	require.Len(t, scans, 1)
	assert.Equal(t, "/old", scans[0].ScanRoot)
	assert.Equal(t, "", scans[0].Host)
	assert.Equal(t, int64(2), scans[0].Rows)
	assert.Equal(t, int64(999), scans[0].TotalDsize)
}

// legacyRow mirrors the pre-identity snapshot schema (no host / username /
// sudo_user columns), as written by the first Parquet export releases.
type legacyRow struct {
	Path            string  `parquet:"path"`
	Parent          string  `parquet:"parent"`
	Name            string  `parquet:"name"`
	IsDir           bool    `parquet:"is_dir"`
	IsRollup        bool    `parquet:"is_rollup"`
	Depth           int32   `parquet:"depth"`
	Asize           int64   `parquet:"asize"`
	Dsize           int64   `parquet:"dsize"`
	DirTotalDsize   *int64  `parquet:"dir_total_dsize,optional"`
	DirTotalFiles   *int64  `parquet:"dir_total_files,optional"`
	DirTotalFolders *int64  `parquet:"dir_total_folders,optional"`
	ScanRoot        string  `parquet:"scan_root"`
	ScanTs          int64   `parquet:"scan_ts,timestamp(millisecond)"`
	ThresholdBytes  int64   `parquet:"threshold_bytes"`
	Mtime           int64   `parquet:"mtime,timestamp(millisecond)"`
	Ino             *uint64 `parquet:"ino,optional"`
	Notreg          bool    `parquet:"notreg"`
	Hlnkc           bool    `parquet:"hlnkc"`
	ReadError       bool    `parquet:"read_error"`
}

func TestListSnapshotsAndReadTreeLegacySchema(t *testing.T) {
	total := int64(333)
	rows := []legacyRow{
		{
			Path: "/old", Parent: "/", Name: "old", IsDir: true, Depth: 0,
			DirTotalDsize: &total, ScanRoot: "/old", ScanTs: 5000,
		},
		{
			Path: "/old/f", Parent: "/old", Name: "f", Depth: 1,
			Asize: 333, Dsize: 333, ScanRoot: "/old", ScanTs: 5000,
		},
	}
	var buf bytes.Buffer
	w := pq.NewGenericWriter[legacyRow](&buf)
	_, err := w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	scans, err := ListSnapshots(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, scans, 1)
	assert.Equal(t, "/old", scans[0].ScanRoot)
	assert.Equal(t, "", scans[0].Host) // column absent => zero identity
	assert.Equal(t, int64(2), scans[0].Rows)
	assert.Equal(t, total, scans[0].TotalDsize)

	got, err := ReadTree(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	assert.Equal(t, "old", got.GetName())
	kids := childItems(got)
	require.Contains(t, kids, "f")
	assert.Equal(t, int64(333), kids["f"].GetUsage())
}

func TestParseManifestInvalid(t *testing.T) {
	_, err := parseManifest("{not json]")
	assert.Error(t, err)
}

func TestSameSnapshot(t *testing.T) {
	ts := time.UnixMilli(1000).UTC()
	a := SnapshotInfo{Host: "h", ScanRoot: "/r", ScanTs: ts}
	assert.True(t, a.SameSnapshot(&SnapshotInfo{Host: "h", ScanRoot: "/r", ScanTs: ts}))
	assert.False(t, a.SameSnapshot(&SnapshotInfo{Host: "h2", ScanRoot: "/r", ScanTs: ts}))
	assert.False(t, a.SameSnapshot(&SnapshotInfo{Host: "h", ScanRoot: "/r2", ScanTs: ts}))
	assert.False(t, a.SameSnapshot(&SnapshotInfo{Host: "h", ScanRoot: "/r", ScanTs: ts.Add(time.Millisecond)}))
}
