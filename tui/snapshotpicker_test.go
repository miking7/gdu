package tui

import (
	"bytes"
	"testing"
	"time"

	"github.com/rivo/tview"

	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
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

func TestSnapshotPickerListsCoveringSnapshots(t *testing.T) {
	dir := t.TempDir()
	writeTinySnapshot(t, dir, "/root")
	ui := newPickerUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	require.Len(t, covering, 1)

	table, rows := ui.buildBaselinePickerForTest(covering)
	assert.True(t, ui.pages.HasPage("snapshotpicker"), "picker page should open")

	// The size column starts as a placeholder, filled asynchronously.
	assert.Equal(t, snapshotSizePlaceholder, table.GetCell(1, 1).Text)

	// Resolving the size renders the folder size and its change since now.
	key := covering[0].Key()
	require.Contains(t, rows, key)
	ui.setPickerSize(table, rows[key][0], 100, 150) // snapshot 100, now 150
	assert.Contains(t, table.GetCell(1, 1).Text, "100")
	assert.Contains(t, table.GetCell(1, 2).Text, "50") // grew 100 -> 150
}

func TestSnapshotPickerNoCoveringSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeTinySnapshot(t, dir, "/other") // does not cover /root
	ui := newPickerUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	assert.Empty(t, covering, "no scan covers /root")
}

// TestSnapshotPickerMarkersAndClose covers the absent ("—") and unreadable ("?")
// cell rendering and that closing the picker dismisses it and invalidates the
// async fill generation.
func TestSnapshotPickerMarkersAndClose(t *testing.T) {
	dir := t.TempDir()
	writeTinySnapshot(t, dir, "/root")
	ui := newPickerUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	require.Len(t, covering, 1)
	table, rows := ui.buildBaselinePickerForTest(covering)
	gen := ui.snapshotPickerGen
	row := rows[covering[0].Key()][0]

	// A folder that did not exist in a scan renders as absent; an unreadable file
	// renders distinctly, so a read error is not mistaken for absence.
	ui.setPickerCells(table, row, snapshotAbsentMarker)
	assert.Equal(t, snapshotAbsentMarker, table.GetCell(1, 1).Text)
	assert.Equal(t, snapshotAbsentMarker, table.GetCell(1, 2).Text)
	ui.setPickerCells(table, row, snapshotErrorMarker)
	assert.Equal(t, snapshotErrorMarker, table.GetCell(1, 1).Text)

	// Closing dismisses the page and invalidates the fill generation, so any
	// queued cell update is dropped.
	ui.closeSnapshotPicker()
	assert.False(t, ui.pages.HasPage("snapshotpicker"))
	assert.NotEqual(t, gen, ui.snapshotPickerGen)
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

// buildBaselinePickerForTest opens the Baseline configuration of the shared
// picker synchronously (no async fill) and returns its table and row index,
// mirroring what showSnapshotPicker builds after the archive listing lands.
func (ui *UI) buildBaselinePickerForTest(
	covering []report.SnapshotListing,
) (*tview.Table, map[parquet.SnapshotKey][]int) {
	var table *tview.Table
	var rows map[parquet.SnapshotKey][]int
	cfg := pickerConfig{
		title:     " test ",
		listings:  covering,
		hint:      func(l *report.SnapshotListing) string { return "" },
		onSelect:  func(l *report.SnapshotListing) {},
		fillSizes: true,
		target:    "/root",
	}
	ui.snapshotPickerGen++
	table = tview.NewTable().SetSelectable(true, false)
	rows = ui.fillPickerRows(table, &cfg)
	selectRow := 1
	if r := ui.activeBaselineRow(&cfg); r > 0 {
		selectRow = r // mirror buildPicker's active-baseline pre-selection
	}
	table.Select(selectRow, 0)
	ui.pages.AddPage("snapshotpicker", table, true, true)
	return table, rows
}
