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
