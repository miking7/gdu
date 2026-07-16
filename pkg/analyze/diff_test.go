package analyze

import (
	"testing"

	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// baselineTree returns a "root" tree used as the past snapshot:
//
//	root/big        20 MiB
//	root/s1          1 KiB
//	root/s2          2 KiB
//	root/obsolete    5 MiB   (removed in the current tree)
//	root/sub/sf      4 KiB
func baselineTree() *Dir {
	root := &Dir{File: &File{Name: "root"}, ItemCount: 1}
	root.AddFile(&File{Name: "big", Size: 20 * mib, Usage: 20 * mib, Parent: root})
	root.AddFile(&File{Name: "s1", Size: 1024, Usage: 1024, Parent: root})
	root.AddFile(&File{Name: "s2", Size: 2048, Usage: 2048, Parent: root})
	root.AddFile(&File{Name: "obsolete", Size: 5 * mib, Usage: 5 * mib, Parent: root})

	sub := &Dir{File: &File{Name: "sub", Parent: root}, ItemCount: 1}
	sub.AddFile(&File{Name: "sf", Size: 4096, Usage: 4096, Parent: sub})
	root.AddFile(sub)

	root.UpdateStats(make(fs.HardLinkedItems))
	return root
}

// currentTree returns a "root" tree used as the live view:
//
//	root/big        25 MiB   (grew +5 MiB)
//	root/s1          1 KiB   (unchanged)
//	root/s2          1 KiB   (shrank -1 KiB)
//	root/brandnew   10 MiB   (new; obsolete is gone)
//	root/sub/sf      4 KiB   (unchanged)
//	root/sub/extra   8 MiB   (new -> sub grew)
func currentTree() *Dir {
	root := &Dir{File: &File{Name: "root"}, ItemCount: 1}
	root.AddFile(&File{Name: "big", Size: 25 * mib, Usage: 25 * mib, Parent: root})
	root.AddFile(&File{Name: "s1", Size: 1024, Usage: 1024, Parent: root})
	root.AddFile(&File{Name: "s2", Size: 1024, Usage: 1024, Parent: root})
	root.AddFile(&File{Name: "brandnew", Size: 10 * mib, Usage: 10 * mib, Parent: root})

	sub := &Dir{File: &File{Name: "sub", Parent: root}, ItemCount: 1}
	sub.AddFile(&File{Name: "sf", Size: 4096, Usage: 4096, Parent: sub})
	sub.AddFile(&File{Name: "extra", Size: 8 * mib, Usage: 8 * mib, Parent: sub})
	root.AddFile(sub)

	root.UpdateStats(make(fs.HardLinkedItems))
	return root
}

func childByName(d *Dir, name string) fs.Item {
	for _, f := range d.Files {
		if f.GetName() == name {
			return f
		}
	}
	return nil
}

func TestBaselineDeltaCategories(t *testing.T) {
	base := BuildBaseline(baselineTree(), "root", 0)
	cur := currentTree()

	cases := []struct {
		name      string
		wantDelta int64
		wantCat   DiffCategory
	}{
		{"big", 5 * mib, DiffGrew},
		{"s1", 0, DiffUnchanged},
		{"s2", -1024, DiffShrank},
		{"brandnew", 10 * mib, DiffNew},
		{"sub", 8 * mib, DiffGrew}, // sub grew because extra appeared inside it
	}
	for _, c := range cases {
		item := childByName(cur, c.name)
		require.NotNil(t, item, c.name)
		delta, cat := base.Delta(item)
		assert.Equal(t, c.wantDelta, delta, "delta for %s", c.name)
		assert.Equal(t, c.wantCat, cat, "category for %s", c.name)
	}
}

func TestBaselineNewVsApproximate(t *testing.T) {
	cur := currentTree()
	brandnew := childByName(cur, "brandnew")
	require.NotNil(t, brandnew)

	// A baseline that kept everything: an unknown covered path is certainly new.
	full := BuildBaseline(baselineTree(), "root", 0)
	delta, cat := full.Delta(brandnew)
	assert.Equal(t, int64(10*mib), delta)
	assert.Equal(t, DiffNew, cat)

	// A thresholded baseline: it might have held the item below the threshold,
	// so the same lookup is only approximate.
	thresholded := BuildBaseline(baselineTree(), "root", 10*mib)
	delta, cat = thresholded.Delta(brandnew)
	assert.Equal(t, int64(10*mib), delta)
	assert.Equal(t, DiffApprox, cat)
}

func TestBaselineCoversVolumeAndTrailingSlashRoots(t *testing.T) {
	// Root "/" is a volume root: filepath.Clean keeps its trailing separator, so
	// covers() must still treat every absolute path as inside it (a new item ->
	// DiffNew, not DiffUncovered). This guards the "//" prefix-doubling bug that
	// also affects Windows "C:\".
	rootSlash := &Dir{File: &File{Name: "/"}}
	rootSlash.AddFile(&File{Name: "existing", Size: mib, Usage: mib, Parent: rootSlash})
	rootSlash.UpdateStats(make(fs.HardLinkedItems))
	base := BuildBaseline(rootSlash, "/", 0)

	newItem := &File{Name: "newfile", Size: mib, Usage: mib, Parent: rootSlash} // /newfile
	_, cat := base.Delta(newItem)
	assert.Equal(t, DiffNew, cat, `root "/" should cover a new absolute path`)

	// A scan_root written with a trailing separator ("/data/") still covers its
	// children once cleaned.
	dataRoot := &Dir{File: &File{Name: "data"}, BasePath: "/"} // GetPath -> /data
	dataRoot.AddFile(&File{Name: "old", Size: mib, Usage: mib, Parent: dataRoot})
	dataRoot.UpdateStats(make(fs.HardLinkedItems))
	base2 := BuildBaseline(dataRoot, "/data/", 0)

	child := &File{Name: "fresh", Size: mib, Usage: mib, Parent: dataRoot} // /data/fresh
	_, cat2 := base2.Delta(child)
	assert.Equal(t, DiffNew, cat2, "a trailing-slash scan_root should cover its children")
}

func TestBaselineUncovered(t *testing.T) {
	base := BuildBaseline(baselineTree(), "root", 0)

	// An item whose absolute path is outside the baseline's scan root.
	elsewhere := &Dir{File: &File{Name: "elsewhere"}}
	stray := &File{Name: "x", Size: mib, Usage: mib, Parent: elsewhere}

	delta, cat := base.Delta(stray)
	assert.Equal(t, int64(0), delta)
	assert.Equal(t, DiffUncovered, cat)
}

func TestBaselineRemovedUnder(t *testing.T) {
	base := BuildBaseline(baselineTree(), "root", 0)
	cur := currentTree()

	present := make(map[string]struct{})
	for _, f := range cur.Files {
		present[f.GetPath()] = struct{}{}
	}

	removed := base.RemovedUnder("root", present)
	require.Len(t, removed, 1)
	assert.Equal(t, "obsolete", removed[0].Name)
	assert.Equal(t, "root/obsolete", removed[0].Path)
	assert.Equal(t, int64(5*mib), removed[0].Size)
	assert.False(t, removed[0].IsDir)
}

func TestBaselineRemovedUnderSkipsRollupBucket(t *testing.T) {
	// A thresholded baseline dir carries a "<smaller objects>" child; it must
	// never be reported as removed even when the current tree has no such child.
	root := &Dir{File: &File{Name: "root"}, ItemCount: 1}
	root.AddFile(&File{Name: "big", Size: 20 * mib, Usage: 20 * mib, Parent: root})
	root.AddFile(&File{Name: SmallObjectsName, Size: 3072, Usage: 3072, Parent: root})
	root.UpdateStats(make(fs.HardLinkedItems))

	base := BuildBaseline(root, "root", 10*mib)

	// Current tree still has "big" but no bucket.
	present := map[string]struct{}{"root/big": {}}
	removed := base.RemovedUnder("root", present)
	assert.Empty(t, removed, "the rollup bucket must not be reported as removed")
}

func TestBaselineRemovedDir(t *testing.T) {
	// A whole directory present in the baseline but gone now is reported once,
	// flagged as a directory, with its recursive usage.
	base := BuildBaseline(baselineTree(), "root", 0)

	// Current tree missing "sub" entirely.
	present := map[string]struct{}{
		"root/big":      {},
		"root/s1":       {},
		"root/s2":       {},
		"root/obsolete": {},
	}
	removed := base.RemovedUnder("root", present)

	var subEntry *RemovedEntry
	for i := range removed {
		if removed[i].Name == "sub" {
			subEntry = &removed[i]
		}
	}
	require.NotNil(t, subEntry)
	assert.True(t, subEntry.IsDir)
	assert.Equal(t, int64(4096), subEntry.Size)
}
