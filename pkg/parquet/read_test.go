package parquet

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
)

func writeAndRead(t *testing.T, root *analyze.Dir, threshold int64) *analyze.Dir {
	t.Helper()
	meta := ScanMeta{
		ScanRoot:       root.GetPath(),
		ScanTime:       time.Unix(1700000000, 0).UTC(),
		ThresholdBytes: threshold,
	}
	var buf bytes.Buffer
	require.NoError(t, WriteTree(&buf, root, &meta))

	got, err := ReadTree(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	got.UpdateStats(make(fs.HardLinkedItems))
	return got
}

func childItems(d *analyze.Dir) map[string]fs.Item {
	out := make(map[string]fs.Item, d.Files.Len())
	for _, f := range d.Files {
		out[f.GetName()] = f
	}
	return out
}

func TestReadTreeRoundTrip(t *testing.T) {
	root := sampleTree()
	got := writeAndRead(t, root, 0)

	assert.Equal(t, "root", got.GetName())
	assert.Equal(t, root.GetPath(), got.GetPath())
	assert.True(t, got.IsDir())
	assert.Equal(t, root.GetUsage(), got.GetUsage())

	kids := childItems(got)
	assert.Contains(t, kids, "big")
	assert.Contains(t, kids, "s1")
	assert.Contains(t, kids, "sub")

	sub := kids["sub"]
	assert.True(t, sub.IsDir())
	assert.Equal(t, got.GetPath()+"/sub", sub.GetPath())
}

func TestReadTreeWithRollup(t *testing.T) {
	root := sampleTree()
	totalUsage := root.GetUsage()
	got := writeAndRead(t, root, 10*mib)

	// Totals are preserved even though small objects collapsed.
	assert.Equal(t, totalUsage, got.GetUsage())

	kids := childItems(got)
	assert.Contains(t, kids, "big")
	assert.Contains(t, kids, analyze.SmallObjectsName)
	assert.NotContains(t, kids, "sub")

	bucket := kids[analyze.SmallObjectsName]
	assert.False(t, bucket.IsDir())
	assert.Equal(t, int64(1024+4096), bucket.GetUsage())
}

func TestReadTreeErrors(t *testing.T) {
	_, err := ReadTree(bytes.NewReader([]byte("not parquet")), 11)
	assert.Error(t, err)
}

// Regression: two scans sharing the same scan_ts (different roots) must never
// bleed into one tree — selection compares the full (host, scan_root, scan_ts)
// identity, not the timestamp alone.
func TestReadTreeDistinguishesSnapshotsWithSameTimestamp(t *testing.T) {
	const ts = int64(1700000000000)
	rows := append(
		rawScanRows("/a", "afile", "h1", ts, 111),
		rawScanRows("/b", "bfile", "h1", ts, 222)...,
	)
	buf := writeRawRows(t, rows)

	got, err := ReadTree(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	got.UpdateStats(make(fs.HardLinkedItems))

	// Equal timestamps tie-break on scan_root, so "/b" is selected — and only
	// its rows may appear in the tree.
	assert.Equal(t, "b", got.GetName())
	kids := childItems(got)
	assert.Contains(t, kids, "bfile")
	assert.NotContains(t, kids, "afile")
	assert.Len(t, kids, 1)
	assert.Equal(t, int64(222), got.GetUsage())
}

// Two snapshots of the same root in one file (the compacted-archive shape): the
// newer one wins.
func TestReadTreeMultiSnapshotPicksLatest(t *testing.T) {
	rows := append(
		rawScanRows("/r", "oldfile", "h1", 1000, 111),
		rawScanRows("/r", "newfile", "h1", 2000, 222)...,
	)
	buf := writeRawRows(t, rows)

	got, err := ReadTree(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	got.UpdateStats(make(fs.HardLinkedItems))

	kids := childItems(got)
	assert.Contains(t, kids, "newfile")
	assert.NotContains(t, kids, "oldfile")
	assert.Equal(t, int64(222), got.GetUsage())
}

// Every imported item must carry a printable flag. The TUI prints the flag as each
// row's first column, so a rune(0) left on a dir renders zero-width and shifts the
// whole directory row one column left of the file rows beside it. Read-error dirs
// import as ' ' too: read_error conflates '!' with '.', so it is not revived.
func TestReadTreeGivesEveryItemAPrintableFlag(t *testing.T) {
	for name, tree := range map[string]*analyze.Dir{
		"sampleTree":    sampleTree(),
		"readErrorTree": readErrorTree(),
	} {
		t.Run(name, func(t *testing.T) {
			got := writeAndRead(t, tree, 0)

			assert.Equal(t, ' ', got.GetFlag(), "root")
			var walk func(d *analyze.Dir)
			walk = func(d *analyze.Dir) {
				for _, item := range d.Files {
					assert.Equalf(t, ' ', item.GetFlag(), "%s (isDir=%v)", item.GetPath(), item.IsDir())
					if sub, ok := item.(*analyze.Dir); ok {
						walk(sub)
					}
				}
			}
			walk(got)
		})
	}
}
