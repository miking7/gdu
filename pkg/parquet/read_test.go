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
	require.NoError(t, WriteTree(&buf, root, meta))

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
