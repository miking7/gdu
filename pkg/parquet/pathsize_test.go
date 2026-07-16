package parquet

import (
	"bytes"
	"sort"
	"testing"

	pq "github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanWithSub builds a four-row scan of root (root dir + nested sub dir + sub's
// file + a top-level file) stamped with one identity, so a single file can hold
// several scans that differ only by timestamp.
func scanWithSub(root, host string, tsMs, subUsage, bigUsage int64) []Row {
	subTotal := subUsage
	rootTotal := subUsage + bigUsage
	return []Row{
		{
			Path: root, Parent: "/", Name: root[1:], IsDir: true, Depth: 0,
			DirTotalDsize: &rootTotal, ScanRoot: root, ScanTs: tsMs, Host: host,
		},
		{
			Path: root + "/sub", Parent: root, Name: "sub", IsDir: true, Depth: 1,
			DirTotalDsize: &subTotal, ScanRoot: root, ScanTs: tsMs, Host: host,
		},
		{
			Path: root + "/sub/s", Parent: root + "/sub", Name: "s", Depth: 2,
			Asize: subUsage, Dsize: subUsage, ScanRoot: root, ScanTs: tsMs, Host: host,
		},
		{
			Path: root + "/big", Parent: root, Name: "big", Depth: 1,
			Asize: bigUsage, Dsize: bigUsage, ScanRoot: root, ScanTs: tsMs, Host: host,
		},
	}
}

func TestPathSizes(t *testing.T) {
	// One file, two scans of /root differing only by timestamp and sub size.
	s1 := scanWithSub("/root", "h1", 1000, 10, 30) // sub 10, root total 40
	s2 := scanWithSub("/root", "h1", 2000, 25, 30) // sub 25, root total 55
	buf := writeRawRows(t, append(s1, s2...))
	reader := bytes.NewReader(buf.Bytes())
	size := int64(buf.Len())

	// A directory target resolves to dir_total_dsize, keyed per scan identity —
	// the whole file is read in one projected pass, not once per scan.
	sizes, err := PathSizes(reader, size, "/root/sub")
	require.NoError(t, err)
	require.Len(t, sizes, 2)
	assert.Equal(t, int64(10), sizes[SnapshotKey{Host: "h1", Root: "/root", TsMs: 1000}])
	assert.Equal(t, int64(25), sizes[SnapshotKey{Host: "h1", Root: "/root", TsMs: 2000}])

	// A file target resolves to its own dsize, not a directory total.
	fileSizes, err := PathSizes(reader, size, "/root/big")
	require.NoError(t, err)
	assert.Equal(t, int64(30), fileSizes[SnapshotKey{Host: "h1", Root: "/root", TsMs: 1000}])

	// A path present in no scan yields an empty result (no claim, not zero).
	none, err := PathSizes(reader, size, "/root/missing")
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestPathSizesPrunesRowGroups builds a path-sorted, multi-row-group file (as
// compaction produces) and verifies row-group pruning never drops a group that
// holds the target: every directory must still resolve even though it lives in a
// middle row group whose flanking groups are skipped from statistics.
func TestPathSizesPrunesRowGroups(t *testing.T) {
	names := []string{"aaa", "bbb", "ccc", "ddd", "eee", "fff", "ggg", "hhh"}
	total := int64(len(names) * 100)
	rows := []Row{{
		Path: "/", Parent: "", Name: "/", IsDir: true, Depth: 0,
		DirTotalDsize: &total, ScanRoot: "/", ScanTs: 1000, Host: "h",
	}}
	for _, n := range names {
		u := int64(100)
		rows = append(rows, Row{
			Path: "/" + n, Parent: "/", Name: n, IsDir: true, Depth: 1,
			DirTotalDsize: &u, ScanRoot: "/", ScanTs: 1000, Host: "h",
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })

	// Two rows per row group forces many groups with tight, non-overlapping path
	// ranges — the layout pruning is meant to exploit.
	var buf bytes.Buffer
	w := pq.NewGenericWriter[Row](&buf, pq.MaxRowsPerRowGroup(2))
	_, err := w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	pf, err := pq.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Greater(t, len(pf.RowGroups()), 1, "test needs multiple row groups")

	key := SnapshotKey{Host: "h", Root: "/", TsMs: 1000}
	for _, n := range names {
		sizes, serr := PathSizes(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "/"+n)
		require.NoError(t, serr)
		assert.Equal(t, int64(100), sizes[key], "pruning must not drop the row group holding /%s", n)
	}

	// A path lexically between real entries (in no row group's data) resolves nowhere.
	none, err := PathSizes(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "/ccd")
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestRowGroupMayContainPathBounds checks the prune predicate: it excludes only
// when target sits outside a fully-present [MinValue, MaxValue], and fails safe
// (keeps the group) whenever a bound is missing — so a malformed or foreign file
// with half-present statistics is never wrongly pruned.
func TestRowGroupMayContainPathBounds(t *testing.T) {
	mk := func(minv, maxv []byte) *format.RowGroup {
		return &format.RowGroup{Columns: []format.ColumnChunk{{
			MetaData: format.ColumnMetaData{
				PathInSchema: []string{"path"},
				Statistics:   format.Statistics{MinValue: minv, MaxValue: maxv},
			},
		}}}
	}
	// In range → keep; out of range → prune.
	assert.True(t, rowGroupMayContainPath(mk([]byte("/a"), []byte("/z")), []byte("/m")))
	assert.False(t, rowGroupMayContainPath(mk([]byte("/a"), []byte("/c")), []byte("/z")))
	assert.False(t, rowGroupMayContainPath(mk([]byte("/x"), []byte("/z")), []byte("/a")))
	// Half-present or absent bounds must never prune (fail safe).
	assert.True(t, rowGroupMayContainPath(mk([]byte("/a"), nil), []byte("/z")))
	assert.True(t, rowGroupMayContainPath(mk(nil, []byte("/c")), []byte("/z")))
	assert.True(t, rowGroupMayContainPath(&format.RowGroup{}, []byte("/x")))
}
