package analyze

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const mib = 1 << 20

// buildTree returns a root dir with one large file, two small files and a small
// subdir, with stats already computed.
func buildTree() *Dir {
	root := &Dir{File: &File{Name: "root"}, ItemCount: 1}

	big := &File{Name: "big", Size: 20 * mib, Usage: 20 * mib, Parent: root}
	small1 := &File{Name: "s1", Size: 1024, Usage: 1024, Parent: root}
	small2 := &File{Name: "s2", Size: 2048, Usage: 2048, Parent: root}

	sub := &Dir{File: &File{Name: "sub", Parent: root}, ItemCount: 1}
	subFile := &File{Name: "sf", Size: 4096, Usage: 4096, Parent: sub}
	sub.AddFile(subFile)

	root.AddFile(big)
	root.AddFile(small1)
	root.AddFile(small2)
	root.AddFile(sub)

	root.UpdateStats(make(fs.HardLinkedItems))
	return root
}

func childrenByName(d *Dir) map[string]fs.Item {
	out := make(map[string]fs.Item, d.Files.Len())
	for _, f := range d.Files {
		out[f.GetName()] = f
	}
	return out
}

func TestRollupBucketsSubThresholdObjects(t *testing.T) {
	root := buildTree()
	totalUsage := root.GetUsage()
	totalSize := root.GetSize()

	out, ok := Rollup(root, 10*mib).(*Dir)
	require.True(t, ok)

	kids := childrenByName(out)
	// The large file survives; the small files and the small subdir collapse.
	assert.Contains(t, kids, "big")
	assert.Contains(t, kids, SmallObjectsName)
	assert.NotContains(t, kids, "s1")
	assert.NotContains(t, kids, "s2")
	assert.NotContains(t, kids, "sub")
	assert.Len(t, kids, 2)

	bucket := kids[SmallObjectsName]
	assert.False(t, bucket.IsDir())
	// 1024 + 2048 + 4096 (the subdir's file) collapsed together.
	assert.Equal(t, int64(1024+2048+4096), bucket.GetUsage())

	// Recursive totals on the surviving root are preserved.
	assert.Equal(t, totalUsage, out.GetUsage())
	assert.Equal(t, totalSize, out.GetSize())
}

func TestRollupDisabledReturnsSameTree(t *testing.T) {
	root := buildTree()
	assert.Same(t, fs.Item(root), Rollup(root, 0))
	assert.Same(t, fs.Item(root), Rollup(root, -1))
}

func TestRollupKeepsEverythingAboveThreshold(t *testing.T) {
	root := buildTree()
	// Threshold of 1 byte: every object has usage >= 1 except nothing collapses
	// that is genuinely below it, so no bucket is produced.
	out := Rollup(root, 1).(*Dir)
	kids := childrenByName(out)
	assert.NotContains(t, kids, SmallObjectsName)
	assert.Contains(t, kids, "big")
	assert.Contains(t, kids, "s1")
	assert.Contains(t, kids, "sub")
}

func TestRollupEncodesSmallObjectsInJSON(t *testing.T) {
	root := buildTree()
	out := Rollup(root, 10*mib)

	var buff bytes.Buffer
	require.NoError(t, out.EncodeJSON(&buff, true))

	// Decode and confirm the bucket survives as a real node with the literal
	// name. (gdu's encoder HTML-escapes '<'/'>'; any JSON parser decodes them
	// back, and the Parquet writer stores the literal string directly.)
	var data any
	require.NoError(t, json.Unmarshal(buff.Bytes(), &data))
	assert.True(t, jsonContainsName(data, SmallObjectsName))
}

// jsonContainsName reports whether any object in the decoded ncdu/gdu JSON tree
// has a "name" field equal to want.
func jsonContainsName(v any, want string) bool {
	switch node := v.(type) {
	case map[string]any:
		n, ok := node["name"].(string)
		return ok && n == want
	case []any:
		for _, e := range node {
			if jsonContainsName(e, want) {
				return true
			}
		}
	}
	return false
}

func TestRollupNonDirReturnedAsIs(t *testing.T) {
	f := &File{Name: "lonely", Size: 1, Usage: 1}
	assert.Same(t, fs.Item(f), Rollup(f, 10*mib))
}
