package tui

import (
	"testing"
	"time"

	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testdev"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
)

// Mount-accurate covering for the S picker and timeline, go-live tree
// membership, the Root/Host columns, and the active-baseline mark.

func TestMountForTarget(t *testing.T) {
	devs := device.Devices{{MountPoint: "/"}, {MountPoint: "/Volumes/SD"}}

	assert.Equal(t, "/Volumes/SD", mountForTarget(devs, nil, "/Volumes/SD/x"))
	assert.Equal(t, "/", mountForTarget(devs, nil, "/Users/me"))
	assert.Equal(t, "", mountForTarget(nil, nil, "/anything"), "no devices and no getter → empty (degrades)")

	// A launcher-skipped session has no captured devices; it resolves via the getter.
	getter := testdev.DevicesInfoGetterMock{Devices: devs}
	assert.Equal(t, "/Volumes/SD", mountForTarget(nil, getter, "/Volumes/SD/x"),
		"empty devices fall back to a fresh getter query")
}

func TestCoveringListingsMountAccurate(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, testanalyze.WriteSnapshot(dir, "root.parquet", "/", "f", time.Unix(1700000000, 0).UTC()))
	require.NoError(t, testanalyze.WriteSnapshot(dir, "sd.parquet", "/Volumes/SD", "f", time.Unix(1700009999, 0).UTC()))
	ui := newPickerUI(t, dir)

	// An SD-card folder with its mount: the "/" scan is clamped out.
	covering, err := ui.coveringListings("/Volumes/SD/photos", "/Volumes/SD")
	require.NoError(t, err)
	require.Len(t, covering, 1)
	assert.Equal(t, "/Volumes/SD", covering[0].ScanRoot)

	// No mount info: both roots path-cover the folder (graceful degradation).
	covering, err = ui.coveringListings("/Volumes/SD/photos", "")
	require.NoError(t, err)
	assert.Len(t, covering, 2)
}

func TestViewContains(t *testing.T) {
	live := &view{tree: liveRootTree(), topPath: "/root"}
	assert.True(t, viewContains(live, "/root"), "the root itself")
	assert.True(t, viewContains(live, "/root/sub"), "a directory node present in the tree")
	assert.False(t, viewContains(live, "/root/nope"), "a path the tree does not hold")
	assert.False(t, viewContains(live, "/other"), "outside the tree entirely — the cross-volume case")
	assert.False(t, viewContains(nil, "/root"), "no live view")
	assert.False(t, viewContains(live, ""), "no folder")
}

func headerTexts(table *tview.Table) []string {
	var hs []string
	for c := 0; c < table.GetColumnCount(); c++ {
		if cell := table.GetCell(0, c); cell != nil {
			hs = append(hs, cell.Text)
		}
	}
	return hs
}

func TestBaselinePickerRootColumnAndForeignHost(t *testing.T) {
	dir := t.TempDir()
	// The picker abbreviates $HOME to ~ in the Root cell; when the suite runs
	// as root (HOME=/root) that would swallow the literal "/root" this test
	// asserts on, so pin HOME somewhere the fixture path can never match.
	t.Setenv("HOME", t.TempDir())
	// WriteSnapshot stamps host "h1", foreign to any real test machine.
	require.NoError(t, testanalyze.WriteSnapshot(dir, "snap.parquet", "/root", "f", time.Unix(1700000000, 0).UTC()))
	ui := newPickerUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	table, _ := ui.buildBaselinePickerForTest(covering)

	headers := headerTexts(table)
	assert.Contains(t, headers, "Root", "the Baseline picker gains a Root column")
	assert.Contains(t, headers, "Host", "a foreign snapshot reveals the Host column")

	// The Root cell shows the snapshot's scan root.
	assert.Contains(t, table.GetCell(1, 3).Text, "/root")
}

func TestBaselinePickerHidesLocalHost(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, testanalyze.WriteSnapshotAs(dir, "snap.parquet", "/root", "f",
		common.HostnameBestEffort(), time.Unix(1700000000, 0).UTC()))
	ui := newPickerUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	table, _ := ui.buildBaselinePickerForTest(covering)

	assert.NotContains(t, headerTexts(table), "Host",
		"a same-machine snapshot leaves the Host column off")
}

func TestBaselinePickerMarksActiveBaseline(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, testanalyze.WriteSnapshot(dir, "a.parquet", "/root", "f", time.Unix(1700000000, 0).UTC()))
	require.NoError(t, testanalyze.WriteSnapshot(dir, "b.parquet", "/root", "f", time.Unix(1700009999, 0).UTC()))
	ui := newPickerUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	require.Len(t, covering, 2)

	// Make the older snapshot the active baseline (covering is newest-first, so
	// it is index 1 → table row 2).
	older := covering[1]
	ui.baseline = analyze.BuildBaseline(liveRootTree(), older.ScanRoot, 0)
	ui.baselineKey = older.Key()

	table, _ := ui.buildBaselinePickerForTest(covering)
	selRow, _ := table.GetSelection()
	assert.Equal(t, 2, selRow, "reopening S pre-selects the active baseline's row")
	assert.Contains(t, table.GetCell(2, 0).Text, "◇", "the active baseline row carries the Baseline glyph")
	assert.NotContains(t, table.GetCell(1, 0).Text, "◇", "other rows are not marked")
	assert.NotContains(t, table.GetCell(2, 0).Text, "●",
		"● now means the tree being viewed — never the baseline")
}
