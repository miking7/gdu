package report

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
)

// writeNestedSnapshot writes a snapshot of root with a nested subdir:
//
//	<root>/big      30
//	<root>/sub/s    10   -> <root>/sub total 10, <root> total 40
func writeNestedSnapshot(t *testing.T, dir, root string, when time.Time) string {
	t.Helper()
	return writeNestedSnapshotNamed(t, dir, filepath.Base(root)+".parquet", root, when)
}

// writeNestedSnapshotNamed is writeNestedSnapshot with an explicit file name, so a
// test can place several same-root snapshots in distinct files.
func writeNestedSnapshotNamed(t *testing.T, dir, file, root string, when time.Time) string {
	t.Helper()
	tree := &analyze.Dir{File: &analyze.File{Name: filepath.Base(root)}, BasePath: filepath.Dir(root)}
	tree.AddFile(&analyze.File{Name: "big", Size: 30, Usage: 30, Parent: tree})
	sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: tree}}
	sub.AddFile(&analyze.File{Name: "s", Size: 10, Usage: 10, Parent: sub})
	tree.AddFile(sub)
	tree.UpdateStats(make(fs.HardLinkedItems))

	path := filepath.Join(dir, file)
	f, err := os.Create(path)
	require.NoError(t, err)
	meta := parquet.ScanMeta{ScanRoot: root, ScanTime: when.UTC(), Host: "h1", Username: "u1"}
	require.NoError(t, parquet.WriteTree(f, tree, &meta))
	require.NoError(t, f.Close())
	return path
}

func TestBuildBaselineFromFile(t *testing.T) {
	dir := t.TempDir()
	path := writeNestedSnapshot(t, dir, "/root", time.Unix(1700000000, 0))

	b, info, err := BuildBaselineFromFile(path, parquet.SnapshotSelector{})
	require.NoError(t, err)
	assert.Equal(t, "/root", info.ScanRoot)
	require.NotNil(t, b)

	// A current /root/big at 50 has grown by 20 versus the baseline's 30.
	root := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/"}
	big := &analyze.File{Name: "big", Size: 50, Usage: 50, Parent: root}
	delta, cat := b.Delta(big)
	assert.Equal(t, int64(20), delta)
	assert.Equal(t, analyze.DiffGrew, cat)
}

func TestBuildBaselineFromFileRejectsNonSnapshot(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "a.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`[1,2,{},[]]`), 0o600))

	_, _, err := BuildBaselineFromFile(jsonPath, parquet.SnapshotSelector{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a gdu Parquet snapshot")
}

func TestSnapshotPathSize(t *testing.T) {
	dir := t.TempDir()
	writeNestedSnapshot(t, dir, "/root", time.Unix(1700000000, 0))

	listings, err := ListSnapshotsInDir(dir)
	require.NoError(t, err)
	require.Len(t, listings, 1)
	l := listings[0]

	// The scan root itself is served from the listing total, no tree read.
	size, ok, err := SnapshotPathSize(dir, &l, "/root")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(40), size)

	// A nested directory is read and walked.
	size, ok, err = SnapshotPathSize(dir, &l, "/root/sub")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(10), size)

	// A path absent from the snapshot reports ok=false.
	_, ok, err = SnapshotPathSize(dir, &l, "/root/missing")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestFolderSizes covers the picker's batched folder-size lookup: the scan root
// is served free from the manifest total, deeper targets are read and keyed by
// scan identity, and an absent path contributes nothing.
func TestFolderSizes(t *testing.T) {
	dir := t.TempDir()
	// Two snapshots of the same root at different times, in separate files.
	writeNestedSnapshotNamed(t, dir, "d1.parquet", "/root", time.Unix(1700000000, 0))
	writeNestedSnapshotNamed(t, dir, "d2.parquet", "/root", time.Unix(1700086400, 0))

	listings, err := ListSnapshotsInDir(dir)
	require.NoError(t, err)
	require.Len(t, listings, 2)

	// A deep target: both snapshots report sub = 10, keyed by snapshot identity.
	sizes := FolderSizes(dir, listings, "/root/sub")
	require.Len(t, sizes, 2)
	for i := range listings {
		assert.Equal(t, int64(10), sizes[listings[i].Key()],
			"each scan's /root/sub size, keyed by identity")
	}

	// The scan root itself comes free from the manifest total (40) — no data read.
	rootSizes := FolderSizes(dir, listings, "/root")
	require.Len(t, rootSizes, 2)
	for i := range listings {
		assert.Equal(t, int64(40), rootSizes[listings[i].Key()])
	}

	// A path present in no scan contributes nothing.
	assert.Empty(t, FolderSizes(dir, listings, "/root/nope"))
}

// TestFolderSizesEachCancel checks that a cancelled context stops the traversal
// before any snapshot file is read — the mechanism the picker uses to abandon a
// size fill when the user closes it.
func TestFolderSizesEachCancel(t *testing.T) {
	dir := t.TempDir()
	writeNestedSnapshotNamed(t, dir, "d1.parquet", "/root", time.Unix(1700000000, 0))
	writeNestedSnapshotNamed(t, dir, "d2.parquet", "/root", time.Unix(1700086400, 0))

	listings, err := ListSnapshotsInDir(dir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	emitted := 0
	// A deep target needs a file read, which the cancelled context must skip.
	FolderSizesEach(ctx, dir, listings, "/root/sub",
		func(parquet.SnapshotKey, int64) { emitted++ },
		func(parquet.SnapshotKey) { emitted++ })
	assert.Zero(t, emitted, "cancelled context emits nothing for file-read targets")
}

// TestFolderSizesEachUnreadable checks that a snapshot whose file can't be read is
// reported via onUnreadable, distinct from a folder that simply didn't exist.
func TestFolderSizesEachUnreadable(t *testing.T) {
	dir := t.TempDir()
	// A listing pointing at a missing file: the deep-target read fails.
	l := SnapshotListing{
		SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/root", Host: "h", ScanTs: time.UnixMilli(1000).UTC()},
		File:         "gone.parquet",
	}
	var unreadable []parquet.SnapshotKey
	FolderSizesEach(context.Background(), dir, []SnapshotListing{l}, "/root/sub",
		func(parquet.SnapshotKey, int64) { t.Fatal("must not emit a size for an unreadable file") },
		func(k parquet.SnapshotKey) { unreadable = append(unreadable, k) })
	require.Len(t, unreadable, 1)
	assert.Equal(t, l.Key(), unreadable[0])
}

// TestSnapshotPathSizeRootSlash guards the volume-root prefix bug: for a whole-disk
// ("/") scan, a non-root path lookup must still resolve (prefix must not become
// "//" and exclude every child).
func TestSnapshotPathSizeRootSlash(t *testing.T) {
	dir := t.TempDir()
	tree := &analyze.Dir{File: &analyze.File{Name: "/"}}
	usr := &analyze.Dir{File: &analyze.File{Name: "usr", Parent: tree}}
	usr.AddFile(&analyze.File{Name: "bin", Size: 100, Usage: 100, Parent: usr})
	tree.AddFile(usr)
	tree.UpdateStats(make(fs.HardLinkedItems))

	f, err := os.Create(filepath.Join(dir, "root.parquet"))
	require.NoError(t, err)
	meta := parquet.ScanMeta{ScanRoot: "/", ScanTime: time.Unix(1700000000, 0).UTC(), Host: "h1", Username: "u1"}
	require.NoError(t, parquet.WriteTree(f, tree, &meta))
	require.NoError(t, f.Close())

	listings, err := ListSnapshotsInDir(dir)
	require.NoError(t, err)
	require.Len(t, listings, 1)

	size, ok, err := SnapshotPathSize(dir, &listings[0], "/usr")
	require.NoError(t, err)
	assert.True(t, ok, "a non-root path under a / scan must resolve")
	assert.Equal(t, int64(100), size)
}
