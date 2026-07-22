package tui

import (
	"bytes"
	"testing"
	"time"

	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/report"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTinySnapshot(t *testing.T, dir, root string) {
	t.Helper()
	require.NoError(t, testanalyze.WriteSnapshot(dir, "snap.parquet", root, "f", time.Unix(1700000000, 0).UTC()))
}

func newPickerUI(t *testing.T, snapshotsDir string) *UI {
	t.Helper()
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.SetSnapshotsDir(snapshotsDir)

	// A live /root that grew to 150 versus the snapshot's 100.
	cur := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/"}
	cur.AddFile(&analyze.File{Name: "f", Size: 150, Usage: 150, Parent: cur})
	cur.UpdateStats(make(fs.HardLinkedItems))
	ui.currentDir = cur
	ui.topDir = cur
	ui.topDirPath = "/root"
	ui.currentDirPath = "/root"
	return ui
}

func TestSnapshotPickerNoCoveringSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeTinySnapshot(t, dir, "/other") // does not cover /root
	ui := newPickerUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	assert.Empty(t, covering, "no scan covers /root")
}

// TestLoadBaselineEntersDiffMode drives loadBaseline (the synchronous core that
// setBaselineFromListing runs off the event loop) plus SetBaseline.
func TestLoadBaselineEntersDiffMode(t *testing.T) {
	dir := t.TempDir()
	writeTinySnapshot(t, dir, "/root")
	ui := newPickerUI(t, dir)

	listings, err := report.ListSnapshotsInDir(dir)
	require.NoError(t, err)
	require.Len(t, listings, 1)

	b, err := ui.loadBaseline(&listings[0])
	require.NoError(t, err)
	ui.SetBaseline(b, &listings[0].SnapshotInfo)
	assert.True(t, ui.inDiffMode())
	assert.True(t, ui.baselineTs.Equal(listings[0].ScanTs), "baseline timestamp should be recorded")
}
