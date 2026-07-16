package parquet

import (
	"bytes"
	"testing"
	"time"

	pq "github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
)

const mib = 1 << 20

// sampleTree returns root(/tmp/root){ big(20M), s1(1K), sub{ sf(4K) } } with
// stats computed.
func sampleTree() *analyze.Dir {
	root := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/tmp", ItemCount: 1}
	big := &analyze.File{Name: "big", Size: 20 * mib, Usage: 20 * mib, Parent: root}
	small := &analyze.File{Name: "s1", Size: 1024, Usage: 1024, Parent: root}

	sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: root}, ItemCount: 1}
	sf := &analyze.File{Name: "sf", Size: 4096, Usage: 4096, Parent: sub}
	sub.AddFile(sf)

	root.AddFile(big)
	root.AddFile(small)
	root.AddFile(sub)
	root.UpdateStats(make(fs.HardLinkedItems))
	return root
}

// readErrorTree returns root(/tmp/root){ big(20M), good{ denied1(!) }, denied2(!) }
// with stats computed: two directories flagged unreadable ('!'), one nested
// under a readable dir that should pick up the propagated '.'. ErrCount is 2.
func readErrorTree() *analyze.Dir {
	root := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/tmp", ItemCount: 1}
	big := &analyze.File{Name: "big", Size: 20 * mib, Usage: 20 * mib, Parent: root}
	good := &analyze.Dir{File: &analyze.File{Name: "good", Parent: root}, ItemCount: 1}
	denied1 := &analyze.Dir{File: &analyze.File{Name: "denied1", Flag: '!', Parent: good}, ItemCount: 1}
	good.AddFile(denied1)
	denied2 := &analyze.Dir{File: &analyze.File{Name: "denied2", Flag: '!', Parent: root}, ItemCount: 1}
	root.AddFile(big)
	root.AddFile(good)
	root.AddFile(denied2)
	root.UpdateStats(make(fs.HardLinkedItems))
	return root
}

// TestWriteTreeManifestReadErrorCount checks the manifest's ErrCount: it counts
// exactly the directories flagged unreadable ('!'), not the '.' propagated to
// their readable ancestors, and it survives even when a rollup threshold would
// collapse the zero-size denied dirs out of the row data (the count comes from
// the real tree, not the emitted rows).
func TestWriteTreeManifestReadErrorCount(t *testing.T) {
	root := readErrorTree()

	// A 10M threshold rolls the zero-size denied dirs out of the rows.
	meta := ScanMeta{ScanRoot: root.GetPath(), ScanTime: time.Unix(1700000000, 0).UTC(), ThresholdBytes: 10 * mib}
	var buf bytes.Buffer
	require.NoError(t, WriteTree(&buf, root, &meta))

	scans, err := ListSnapshots(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, scans, 1)
	assert.Equal(t, int64(2), scans[0].ErrCount, "only the two '!' dirs are counted")

	// Propagation sanity: the readable ancestor carries '.', the denied dirs '!'.
	good := findChildDir(root, "good")
	require.NotNil(t, good)
	assert.Equal(t, '.', good.GetFlag())
	assert.Equal(t, '!', findChildDir(root, "denied2").GetFlag())
}

// findChildDir returns dir's immediate subdirectory named name, or nil.
func findChildDir(dir fs.Item, name string) fs.Item {
	for child := range dir.GetFiles(fs.SortByNone, fs.SortAsc) {
		if child.IsDir() && child.GetName() == name {
			return child
		}
	}
	return nil
}

func readRows(t *testing.T, root *analyze.Dir, meta ScanMeta) map[string]Row {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, WriteTree(&buf, root, &meta))

	rows, err := pq.Read[Row](bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	byName := make(map[string]Row, len(rows))
	for _, r := range rows {
		byName[r.Name] = r
	}
	return byName
}

func TestWriteTreeThresholdRollup(t *testing.T) {
	root := sampleTree()
	meta := ScanMeta{
		ScanRoot:       root.GetPath(),
		ScanTime:       time.Unix(1700000000, 0).UTC(),
		ThresholdBytes: 10 * mib,
	}
	byName := readRows(t, root, meta)

	// big survives; s1 and sub collapse into the rollup.
	assert.Contains(t, byName, "big")
	assert.Contains(t, byName, analyze.SmallObjectsName)
	assert.NotContains(t, byName, "s1")
	assert.NotContains(t, byName, "sub")

	big := byName["big"]
	assert.False(t, big.IsDir)
	assert.Equal(t, int32(1), big.Depth)
	assert.Equal(t, int64(20*mib), big.Dsize)

	rollup := byName[analyze.SmallObjectsName]
	assert.True(t, rollup.IsRollup)
	assert.Equal(t, int64(1024+4096), rollup.Dsize)
	require.NotNil(t, rollup.DirTotalFiles)
	assert.Equal(t, int64(2), *rollup.DirTotalFiles) // s1 + sub/sf
	require.NotNil(t, rollup.DirTotalFolders)
	assert.Equal(t, int64(1), *rollup.DirTotalFolders) // sub
}

func TestWriteTreeDirTotalsAndScanTotal(t *testing.T) {
	root := sampleTree()
	meta := ScanMeta{ScanRoot: root.GetPath(), ScanTime: time.Unix(1700000000, 0).UTC()}
	byName := readRows(t, root, meta)

	// No threshold => everything kept, no rollup.
	assert.NotContains(t, byName, analyze.SmallObjectsName)

	rootRow := byName["root"]
	assert.True(t, rootRow.IsDir)
	assert.Equal(t, int32(0), rootRow.Depth)
	assert.Equal(t, int64(0), rootRow.Dsize) // directories carry 0; totals live in dir_total_*
	require.NotNil(t, rootRow.DirTotalDsize)
	assert.Equal(t, root.GetUsage(), *rootRow.DirTotalDsize)
	require.NotNil(t, rootRow.DirTotalFiles)
	assert.Equal(t, int64(3), *rootRow.DirTotalFiles) // big + s1 + sub/sf
	require.NotNil(t, rootRow.DirTotalFolders)
	assert.Equal(t, int64(1), *rootRow.DirTotalFolders) // sub

	// Summing dsize over non-directory rows reproduces the scan total.
	var total int64
	for _, r := range byName {
		if !r.IsDir {
			total += r.Dsize
		}
	}
	assert.Equal(t, root.GetUsage(), total)
}

func TestWriteTreeStampsMetadata(t *testing.T) {
	root := sampleTree()
	meta := ScanMeta{
		ScanRoot:       root.GetPath(),
		ScanTime:       time.Unix(1700000000, 0).UTC(),
		ThresholdBytes: 4096,
	}
	byName := readRows(t, root, meta)

	big := byName["big"]
	assert.Equal(t, root.GetPath(), big.ScanRoot)
	assert.Equal(t, meta.ScanTime.UnixMilli(), big.ScanTs)
	assert.Equal(t, int64(4096), big.ThresholdBytes)
}

func TestWriteTreeStampsIdentity(t *testing.T) {
	root := sampleTree()
	meta := ScanMeta{
		ScanRoot: root.GetPath(),
		ScanTime: time.Unix(1700000000, 0).UTC(),
		Host:     "host1",
		Username: "root",
		SudoUser: "alice",
	}
	byName := readRows(t, root, meta)

	big := byName["big"]
	assert.Equal(t, "host1", big.Host)
	assert.Equal(t, "root", big.Username)
	require.NotNil(t, big.SudoUser)
	assert.Equal(t, "alice", *big.SudoUser)
}

func TestWriteTreeNoSudoUserIsNull(t *testing.T) {
	root := sampleTree()
	meta := ScanMeta{
		ScanRoot: root.GetPath(), ScanTime: time.Unix(1700000000, 0).UTC(),
		Host: "h", Username: "u", // SudoUser empty => null column
	}
	byName := readRows(t, root, meta)
	assert.Nil(t, byName["big"].SudoUser)
}

// assertRowGroupsSorted checks the contract compaction relies on: every row
// group is internally ordered by (path, scan_ts) and declares that order in
// its metadata. (Global cross-group order is deliberately NOT required.)
func assertRowGroupsSorted(t *testing.T, data []byte) {
	t.Helper()
	pf, err := pq.OpenFile(bytes.NewReader(data), int64(len(data)))
	require.NoError(t, err)
	require.NotEmpty(t, pf.RowGroups())

	for gi, rg := range pf.RowGroups() {
		cols := rg.SortingColumns()
		require.Len(t, cols, 2, "row group %d must declare 2 sorting columns", gi)
		assert.Equal(t, []string{"path"}, cols[0].Path())
		assert.False(t, cols[0].Descending())
		assert.Equal(t, []string{"scan_ts"}, cols[1].Path())
		assert.False(t, cols[1].Descending())

		reader := pq.NewGenericRowGroupReader[Row](rg)
		rows := make([]Row, rg.NumRows())
		n, readErr := reader.Read(rows)
		require.Equal(t, int(rg.NumRows()), n)
		_ = readErr // io.EOF expected once all rows are consumed
		for i := 1; i < n; i++ {
			prev, cur := rows[i-1], rows[i]
			ordered := prev.Path < cur.Path || (prev.Path == cur.Path && prev.ScanTs <= cur.ScanTs)
			assert.True(t, ordered, "group %d: row %d (%q) sorts after row %d (%q)",
				gi, i-1, prev.Path, i, cur.Path)
		}
		reader.Close()
	}
}

func TestWriteTreeOutputSorted(t *testing.T) {
	root := sampleTree()
	meta := ScanMeta{ScanRoot: root.GetPath(), ScanTime: time.Unix(1700000000, 0).UTC()}
	var buf bytes.Buffer
	require.NoError(t, WriteTree(&buf, root, &meta))
	assertRowGroupsSorted(t, buf.Bytes())
}

// Shrinks sortChunkRows so a small tree spans several chunks, proving the
// chunk/row-group alignment, per-group sorting, and that multi-group files
// still round-trip (rows land in overlapping path ranges across groups).
func TestWriteTreeMultiChunkRowGroups(t *testing.T) {
	orig := sortChunkRows
	sortChunkRows = 4
	t.Cleanup(func() { sortChunkRows = orig })

	root := sampleTree() // 6 rows: root, big, s1, sub, sub/sf — plus dir rows
	meta := ScanMeta{ScanRoot: root.GetPath(), ScanTime: time.Unix(1700000000, 0).UTC()}
	var buf bytes.Buffer
	require.NoError(t, WriteTree(&buf, root, &meta))

	pf, err := pq.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	assert.Greater(t, len(pf.RowGroups()), 1, "expected multiple row groups")
	for _, rg := range pf.RowGroups() {
		assert.LessOrEqual(t, rg.NumRows(), int64(4))
	}
	assertRowGroupsSorted(t, buf.Bytes())

	// The multi-group file must still list and load identically.
	scans, err := ListSnapshots(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, scans, 1)
	assert.Equal(t, int64(5), scans[0].Rows)

	got, err := ReadTree(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	got.UpdateStats(make(fs.HardLinkedItems))
	assert.Equal(t, root.GetUsage(), got.GetUsage())
	kids := childItems(got)
	assert.Contains(t, kids, "big")
	assert.Contains(t, kids, "sub")
}

func TestSchemaIsTimezoneAware(t *testing.T) {
	s := pq.SchemaOf(Row{}).String()
	assert.Contains(t, s, "scan_ts")
	assert.Contains(t, s, "dir_total_dsize")
	// The user requires a timezone-aware timestamp (DuckDB TIMESTAMPTZ).
	assert.Contains(t, s, "isAdjustedToUTC=true")
	// Identity columns added post-MVP.
	assert.Contains(t, s, "host")
	assert.Contains(t, s, "username")
	assert.Contains(t, s, "sudo_user")
}
