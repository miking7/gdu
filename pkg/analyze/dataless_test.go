package analyze

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/fs"
)

// fakeDataless makes the analyzers treat exactly one path as a cloud
// placeholder. The kernel owns the real attribute and userspace cannot set it,
// so swapping the seam is the only way to exercise the skip in a fixture.
func fakeDataless(t *testing.T, path string) {
	t.Helper()

	prev := dirIsDataless
	dirIsDataless = func(p string) bool { return p == path }
	t.Cleanup(func() { dirIsDataless = prev })
}

// fakeDatalessFile makes the analyzers treat files with the given base name as
// evicted. Matching by name is enough for a fixture and keeps the seam's
// os.FileInfo signature, which is what both file paths already hold.
func fakeDatalessFile(t *testing.T, name string) {
	t.Helper()

	prev := fileIsDataless
	fileIsDataless = func(info os.FileInfo) bool { return info.Name() == name }
	t.Cleanup(func() { fileIsDataless = prev })
}

// datalessFixture builds root/{plain/keep, cloud/{inner,evicted}} and marks
// cloud as a placeholder. Nothing under cloud may ever reach a scan result:
// its presence would prove the analyzer listed the directory, which is the
// very act that downloads a real cloud subtree.
func datalessFixture(t *testing.T) (root, cloud string) {
	t.Helper()

	root = t.TempDir()

	cloud = filepath.Join(root, "cloud")
	require.NoError(t, os.MkdirAll(filepath.Join(cloud, "inner"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cloud, "evicted"), []byte("would be downloaded"), 0o600))

	plain := filepath.Join(root, "plain")
	require.NoError(t, os.MkdirAll(plain, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(plain, "keep"), []byte("local"), 0o600))

	fakeDataless(t, cloud)
	return root, cloud
}

// assertDatalessLeaf checks the placeholder is reported as a flagged, childless
// directory while its sibling was scanned normally.
func assertDatalessLeaf(t *testing.T, root fs.Item) {
	t.Helper()

	byName := map[string]fs.Item{}
	for item := range root.GetFiles(fs.SortByName, fs.SortAsc) {
		byName[item.GetName()] = item
	}

	cloud, ok := byName["cloud"]
	require.True(t, ok, "placeholder directory missing from the result")
	assert.Equal(t, '~', cloud.GetFlag())
	assert.True(t, cloud.IsDir())
	assert.Equal(t, int64(1), cloud.GetItemCount())
	assert.Empty(t, slices.Collect(cloud.GetFiles(fs.SortByName, fs.SortAsc)),
		"placeholder was enumerated — a real cloud subtree would have been downloaded")

	plain, ok := byName["plain"]
	require.True(t, ok, "sibling directory missing from the result")
	assert.Equal(t, int64(2), plain.GetItemCount(), "sibling should still be scanned normally")
}

func noIgnore(_, _ string) bool { return false }

func keepAllFiles(_ string) bool { return false }

func TestParallelAnalyzerSkipsDatalessDir(t *testing.T) {
	root, _ := datalessFixture(t)

	a := CreateAnalyzer()
	dir := a.AnalyzeDir(root, noIgnore, keepAllFiles).(*Dir)
	a.GetDone().Wait()
	a.ResetProgress()
	dir.UpdateStats(make(fs.HardLinkedItems))

	assertDatalessLeaf(t, dir)

	// A placeholder holds no data blocks, so it costs nothing towards the total
	// — exactly like an empty directory in this analyzer's accounting.
	for item := range dir.GetFiles(fs.SortByName, fs.SortAsc) {
		if item.GetName() == "cloud" {
			assert.Zero(t, item.GetUsage())
		}
	}
}

func TestStableOrderAnalyzerSkipsDatalessDir(t *testing.T) {
	root, _ := datalessFixture(t)

	a := CreateStableOrderAnalyzer()
	dir := a.AnalyzeDir(root, noIgnore, keepAllFiles).(*Dir)
	a.GetDone().Wait()
	a.ResetProgress()
	dir.UpdateStats(make(fs.HardLinkedItems))

	assertDatalessLeaf(t, dir)
}

func TestSequentialAnalyzerSkipsDatalessDir(t *testing.T) {
	root, _ := datalessFixture(t)

	a := CreateSeqAnalyzer()
	dir := a.AnalyzeDir(root, noIgnore, keepAllFiles).(*Dir)
	a.GetDone().Wait()
	a.ResetProgress()
	dir.UpdateStats(make(fs.HardLinkedItems))

	assertDatalessLeaf(t, dir)
}

func TestStoredAnalyzerSkipsDatalessDir(t *testing.T) {
	root, _ := datalessFixture(t)

	a := CreateStoredAnalyzer(filepath.Join(t.TempDir(), "badger"))
	dir := a.AnalyzeDir(root, noIgnore, keepAllFiles).(*StoredDir)
	a.GetDone().Wait()
	dir.UpdateStats(make(fs.HardLinkedItems))

	assertDatalessLeaf(t, dir)
}

// TestTopDirAnalyzerSkipsDatalessDir covers the memory-efficient analyzer, which
// reports only top-level totals: the placeholder must weigh what an empty
// directory does and still carry the flag.
func TestTopDirAnalyzerSkipsDatalessDir(t *testing.T) {
	root, _ := datalessFixture(t)

	a := CreateTopDirAnalyzer()
	dir := a.AnalyzeDir(root, noIgnore, keepAllFiles).(*SimpleDir)
	a.GetDone().Wait()

	byName := map[string]SimpleFile{}
	for _, f := range dir.Files {
		byName[f.Name] = f
	}

	cloud, ok := byName["cloud"]
	require.True(t, ok, "placeholder directory missing from the result")
	assert.Equal(t, '~', cloud.Flag)
	assert.True(t, cloud.IsDir)
	assert.Equal(t, int64(emptyDirSize), cloud.Size)
	assert.Zero(t, cloud.Usage)

	plain, ok := byName["plain"]
	require.True(t, ok, "sibling directory missing from the result")
	assert.Equal(t, ' ', plain.Flag)
	assert.Positive(t, plain.Usage, "sibling should still be scanned normally")
}

// TestTopDirAnalyzerSkipsDatalessRoot covers scanning a placeholder directly:
// gdu refuses to materialise a cloud even when asked to measure one.
func TestTopDirAnalyzerSkipsDatalessRoot(t *testing.T) {
	_, cloud := datalessFixture(t)

	a := CreateTopDirAnalyzer()
	dir := a.AnalyzeDir(cloud, noIgnore, keepAllFiles).(*SimpleDir)
	a.GetDone().Wait()

	assert.Equal(t, '~', dir.Flag)
	assert.Empty(t, dir.Files, "placeholder root was enumerated")
}

// TestParallelAnalyzerSkipsDatalessRoot is the same refusal for the default
// analyzer, whose per-directory check sits on the recursion's entry point and so
// covers the scan root too.
func TestParallelAnalyzerSkipsDatalessRoot(t *testing.T) {
	_, cloud := datalessFixture(t)

	a := CreateAnalyzer()
	dir := a.AnalyzeDir(cloud, noIgnore, keepAllFiles).(*Dir)
	a.GetDone().Wait()
	a.ResetProgress()
	dir.UpdateStats(make(fs.HardLinkedItems))

	assert.Equal(t, '~', dir.GetFlag())
	assert.Equal(t, int64(1), dir.GetItemCount())
	assert.Empty(t, slices.Collect(dir.GetFiles(fs.SortByName, fs.SortAsc)))
}

// datalessFileFixture builds root/{evicted, local} and marks evicted as a cloud
// placeholder file. Unlike a placeholder directory, an evicted file is still
// counted — it just holds no blocks — so only the flag should change.
func datalessFileFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "evicted"), []byte("stub"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "local"), []byte("here"), 0o600))
	fakeDatalessFile(t, "evicted")
	return root
}

func TestParallelAnalyzerFlagsDatalessFile(t *testing.T) {
	root := datalessFileFixture(t)

	a := CreateAnalyzer()
	dir := a.AnalyzeDir(root, noIgnore, keepAllFiles).(*Dir)
	a.GetDone().Wait()
	a.ResetProgress()
	dir.UpdateStats(make(fs.HardLinkedItems))

	byName := map[string]fs.Item{}
	for item := range dir.GetFiles(fs.SortByName, fs.SortAsc) {
		byName[item.GetName()] = item
	}

	require.Contains(t, byName, "evicted")
	assert.Equal(t, '~', byName["evicted"].GetFlag())
	assert.Equal(t, int64(4), byName["evicted"].GetSize(), "an evicted file keeps its apparent size")

	require.Contains(t, byName, "local")
	assert.Equal(t, ' ', byName["local"].GetFlag())
}

// TestTopDirAnalyzerFlagsDatalessFile covers the memory-efficient analyzer's
// separate file path, which builds a SimpleFile rather than going through the
// shared platform attributes.
func TestTopDirAnalyzerFlagsDatalessFile(t *testing.T) {
	root := datalessFileFixture(t)

	a := CreateTopDirAnalyzer()
	dir := a.AnalyzeDir(root, noIgnore, keepAllFiles).(*SimpleDir)
	a.GetDone().Wait()

	byName := map[string]SimpleFile{}
	for _, f := range dir.Files {
		byName[f.Name] = f
	}

	require.Contains(t, byName, "evicted")
	assert.Equal(t, '~', byName["evicted"].Flag)
	require.Contains(t, byName, "local")
	assert.Equal(t, ' ', byName["local"].Flag)
}

// TestDirIsDatalessOnOrdinaryDir exercises the real (unswapped) implementation.
// A fixture can never be genuinely dataless — the kernel sets the attribute — so
// what is verifiable is that an ordinary directory is not mistaken for one and
// that a missing path is handled without error.
func TestDirIsDatalessOnOrdinaryDir(t *testing.T) {
	dir := t.TempDir()

	assert.False(t, dirIsDatalessPath(dir))
	assert.False(t, dirIsDatalessPath(filepath.Join(dir, "does-not-exist")))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.False(t, fileInfoIsDataless(info))
}
