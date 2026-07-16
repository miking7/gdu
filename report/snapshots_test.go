package report

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	pq "github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
)

// writeMultiSnapshotFile writes a file holding two snapshots of /m (older + newer) with
// no footer manifest, exercising the multi-snapshot read/list paths.
func writeMultiSnapshotFile(t *testing.T, dir string) string {
	t.Helper()
	scanRow := func(name, host string, tsMs, usage int64, isDir bool, dirTotal *int64) parquet.Row {
		return parquet.Row{
			Path: "/m/" + name, Parent: "/m", Name: name, IsDir: isDir, Depth: 1,
			Asize: usage, Dsize: usage, DirTotalDsize: dirTotal,
			ScanRoot: "/m", ScanTs: tsMs, Host: host,
		}
	}
	rootRow := func(tsMs, total int64) parquet.Row {
		tt := total
		return parquet.Row{
			Path: "/m", Parent: "/", Name: "m", IsDir: true, Depth: 0, DirTotalDsize: &tt,
			ScanRoot: "/m", ScanTs: tsMs, Host: "h1",
		}
	}
	rows := []parquet.Row{
		rootRow(1000, 111), scanRow("old", "h1", 1000, 111, false, nil),
		rootRow(5000, 222), scanRow("new", "h1", 5000, 222, false, nil),
	}

	path := filepath.Join(dir, "multi.parquet")
	f, err := os.Create(path)
	require.NoError(t, err)
	w := pq.NewGenericWriter[parquet.Row](f)
	_, err = w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())
	require.NoError(t, f.Close())
	return path
}

func TestMultiSnapshotNote(t *testing.T) {
	dir := t.TempDir()

	multi := writeMultiSnapshotFile(t, dir)
	f, err := os.Open(multi)
	require.NoError(t, err)
	defer f.Close()

	// Multi-snapshot, no explicit selector → a note naming the count and latest snapshot.
	note := MultiSnapshotNote(f, parquet.SnapshotSelector{})
	assert.Contains(t, note, "2 snapshots")
	assert.Contains(t, note, "/m")
	assert.Contains(t, note, "--snapshot")

	// An explicit selector suppresses the note.
	assert.Empty(t, MultiSnapshotNote(f, parquet.SnapshotSelector{Spec: "latest"}))

	// A single-scan file warrants no note.
	single := writeSnapshot(t, dir, "/one", 4096, time.Unix(1700000000, 0))
	sf, err := os.Open(single)
	require.NoError(t, err)
	defer sf.Close()
	assert.Empty(t, MultiSnapshotNote(sf, parquet.SnapshotSelector{}))

	// Non-seekable / non-file input warrants no note.
	assert.Empty(t, MultiSnapshotNote(bytes.NewBufferString("x"), parquet.SnapshotSelector{}))
}

func TestParquetSnapshotsFromFile(t *testing.T) {
	dir := t.TempDir()

	f, err := os.Open(writeMultiSnapshotFile(t, dir))
	require.NoError(t, err)
	defer f.Close()
	scans, err := ParquetSnapshotsFromFile(f)
	require.NoError(t, err)
	assert.Len(t, scans, 2)

	// A JSON (non-Parquet) file yields no snapshots and no error.
	jsonPath := filepath.Join(dir, "a.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(`[1,2,{},[]]`), 0o600))
	jf, err := os.Open(jsonPath)
	require.NoError(t, err)
	defer jf.Close()
	scans, err = ParquetSnapshotsFromFile(jf)
	require.NoError(t, err)
	assert.Nil(t, scans)
}

func TestReadAnalysisSnapshot(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Open(writeMultiSnapshotFile(t, dir))
	require.NoError(t, err)
	defer f.Close()

	scans, err := ParquetSnapshotsFromFile(f)
	require.NoError(t, err)
	require.Len(t, scans, 2)

	// Load the older scan by identity and confirm only its file is present.
	var older parquet.SnapshotInfo
	for _, s := range scans {
		if s.ScanTs.UnixMilli() == 1000 {
			older = s
		}
	}
	tree, err := ReadAnalysisSnapshot(f, &older)
	require.NoError(t, err)
	tree.UpdateStats(make(fs.HardLinkedItems))

	names := map[string]bool{}
	for _, c := range tree.Files {
		names[c.GetName()] = true
	}
	assert.True(t, names["old"])
	assert.False(t, names["new"])
}

// writeSnapshot writes a one-scan snapshot for root with the given usage and
// scan time into dir, returning its path.
func writeSnapshot(t *testing.T, dir, root string, usage int64, when time.Time) string {
	t.Helper()
	tree := &analyze.Dir{File: &analyze.File{Name: filepath.Base(root)}, BasePath: filepath.Dir(root)}
	tree.AddFile(&analyze.File{Name: "f", Size: usage, Usage: usage, Parent: tree})
	tree.UpdateStats(make(fs.HardLinkedItems))

	path := filepath.Join(dir, filepath.Base(root)+".parquet")
	f, err := os.Create(path)
	require.NoError(t, err)
	meta := parquet.ScanMeta{ScanRoot: root, ScanTime: when.UTC(), Host: "h1", Username: "u1"}
	require.NoError(t, parquet.WriteTree(f, tree, &meta))
	require.NoError(t, f.Close())
	return path
}

func TestListSnapshotsInFile(t *testing.T) {
	dir := t.TempDir()
	path := writeSnapshot(t, dir, "/data", 4096, time.Unix(1700000000, 0))

	listings, err := ListSnapshotsInFile(path)
	require.NoError(t, err)
	require.Len(t, listings, 1)
	assert.Equal(t, "/data", listings[0].ScanRoot)
	assert.Equal(t, "data.parquet", listings[0].File)
	assert.Equal(t, int64(4096), listings[0].TotalDsize)
}

func TestListSnapshotsInDirNewestFirst(t *testing.T) {
	dir := t.TempDir()
	writeSnapshot(t, dir, "/older", 1000, time.Unix(1700000000, 0))
	writeSnapshot(t, dir, "/newer", 2000, time.Unix(1700009999, 0))

	listings, err := ListSnapshotsInDir(dir)
	require.NoError(t, err)
	require.Len(t, listings, 2)
	assert.Equal(t, "/newer", listings[0].ScanRoot) // newest first
	assert.Equal(t, "/older", listings[1].ScanRoot)
}

func TestListSnapshotsInDirSkipsForeignFiles(t *testing.T) {
	dir := t.TempDir()
	writeSnapshot(t, dir, "/ok", 1000, time.Unix(1700000000, 0))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "junk.parquet"), []byte("not parquet"), 0o600))

	listings, err := ListSnapshotsInDir(dir)
	require.NoError(t, err)
	require.Len(t, listings, 1) // the junk file is skipped, not fatal
	assert.Equal(t, "/ok", listings[0].ScanRoot)
}

func TestPrintSnapshotsColumns(t *testing.T) {
	dir := t.TempDir()
	writeSnapshot(t, dir, "/a", 5<<20, time.Unix(1700000000, 0))
	writeSnapshot(t, dir, "/b", 2<<20, time.Unix(1700009999, 0))
	listings, err := ListSnapshotsInDir(dir)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, PrintSnapshots(&out, listings))
	s := out.String()

	assert.Contains(t, s, "WHEN")
	assert.Contains(t, s, "ROOT")
	assert.Contains(t, s, "HOST") // host set → column shown
	assert.Contains(t, s, "FILE") // two files → column shown
	assert.Contains(t, s, "/a")
	assert.Contains(t, s, "/b")
	assert.Contains(t, s, "5.0 MiB")
}

func TestPrintSnapshotsSingleFileHidesFileColumn(t *testing.T) {
	dir := t.TempDir()
	path := writeSnapshot(t, dir, "/only", 1024, time.Unix(1700000000, 0))
	listings, err := ListSnapshotsInFile(path)
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, PrintSnapshots(&out, listings))
	assert.NotContains(t, out.String(), "FILE")
}

func TestPrintSnapshotsEmpty(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, PrintSnapshots(&out, nil))
	assert.Contains(t, out.String(), "No snapshots found")
}

func TestFormatBinarySize(t *testing.T) {
	assert.Equal(t, "512 B", formatBinarySize(512))
	assert.Equal(t, "1.0 KiB", formatBinarySize(1024))
	assert.Equal(t, "5.0 MiB", formatBinarySize(5<<20))
	assert.Equal(t, "2.0 GiB", formatBinarySize(2<<30))
}

func TestPathCoveredBy(t *testing.T) {
	assert.True(t, PathCoveredBy("/root", "/root"))
	assert.True(t, PathCoveredBy("/root", "/root/sub"))
	assert.True(t, PathCoveredBy("/", "/anything"))
	assert.False(t, PathCoveredBy("/home/mike", "/home/mike2"))
	assert.False(t, PathCoveredBy("/root", "/other"))
}

func TestRootCoversWithinMount(t *testing.T) {
	cases := []struct {
		name                    string
		scanRoot, target, mount string
		want                    bool
	}{
		{"root equals target", "/Volumes/SD", "/Volumes/SD", "/Volumes/SD", true},
		{"same-volume folder under a whole-disk scan", "/Volumes/SD", "/Volumes/SD/photos", "/Volumes/SD", true},
		{"cross-volume / scan excluded for an SD folder", "/", "/Volumes/SD/photos", "/Volumes/SD", false},
		{"/ scan covers a root-volume folder", "/", "/Users/me/proj", "/", true},
		{"a folder at /Volumes belongs to the root volume", "/", "/Volumes", "/", true},
		{"root deeper than the folder does not cover it", "/Volumes/SD/a", "/Volumes/SD", "/Volumes/SD", false},
		{"root between mount and target", "/Users/me", "/Users/me/proj/sub", "/Users", true},
		{"root above the mount is excluded", "/", "/Users/me/proj", "/Users", false},
		{"empty mount degrades to path-covering", "/", "/Volumes/SD/photos", "", true},
		{"empty mount still needs path coverage", "/Volumes/SD", "/other", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, RootCoversWithinMount(c.scanRoot, c.target, c.mount))
		})
	}
}

func TestResolveArchiveBaselineMountClamp(t *testing.T) {
	dir := t.TempDir()
	writeSnapshot(t, dir, "/data", 10<<20, time.Unix(1700000000, 0))    // parent-volume scan
	writeSnapshot(t, dir, "/data/vol", 5<<20, time.Unix(1700009999, 0)) // nested-volume scan
	target := "/data/vol/photos"

	// Without mount info both roots path-cover the folder; the newest wins.
	l, err := ResolveArchiveBaseline(dir, "latest", target, "", "")
	require.NoError(t, err)
	assert.Equal(t, "/data/vol", l.ScanRoot)

	// With the folder's mount, the parent-volume /data scan is clamped out, so
	// even "latest" resolves to the nested-volume scan.
	l, err = ResolveArchiveBaseline(dir, "latest", target, "/data/vol", "")
	require.NoError(t, err)
	assert.Equal(t, "/data/vol", l.ScanRoot)

	// --baseline-root selects across volumes regardless of the mount clamp.
	l, err = ResolveArchiveBaseline(dir, "latest", target, "/data/vol", "/data")
	require.NoError(t, err)
	assert.Equal(t, "/data", l.ScanRoot)
}

func TestResolveArchiveBaselineNoSameVolumeSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeSnapshot(t, dir, "/data", 10<<20, time.Unix(1700000000, 0)) // only the parent volume

	_, err := ResolveArchiveBaseline(dir, "latest", "/data/vol/photos", "/data/vol", "")
	require.Error(t, err)
	assert.ErrorContains(t, err, "volume", "the error explains the volume scoping")
	assert.ErrorContains(t, err, "--baseline-root", "and names the cross-volume escape hatch")
}

func TestPrintSnapshotsHidesLocalHost(t *testing.T) {
	local := common.HostnameBestEffort()
	listings := []SnapshotListing{
		{SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/a", Host: local, ScanTs: time.Unix(1700000000, 0)}, File: "a.parquet"},
	}
	var out bytes.Buffer
	require.NoError(t, PrintSnapshots(&out, listings))
	assert.NotContains(t, out.String(), "HOST", "a same-machine snapshot leaves the host column off")
}

func TestPrintSnapshotsShowsForeignHost(t *testing.T) {
	local := common.HostnameBestEffort()
	listings := []SnapshotListing{
		{SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/a", Host: local, ScanTs: time.Unix(1700000000, 0)}, File: "a.parquet"},
		{SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/b", Host: "elsewhere", ScanTs: time.Unix(1700009999, 0)}, File: "b.parquet"},
	}
	var out bytes.Buffer
	require.NoError(t, PrintSnapshots(&out, listings))
	s := out.String()
	assert.Contains(t, s, "HOST", "a foreign snapshot reveals the host column")
	assert.Contains(t, s, "elsewhere", "and the foreign host name is shown")
}
