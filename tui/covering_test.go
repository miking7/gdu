package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testdev"
	"github.com/dundee/gdu/v5/pkg/device"
)

// Mount-accurate covering for the browser and timeline, plus go-live tree
// membership.

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

// The browser's Root/Host columns, the ◇/● marker glyphs and their fallbacks,
// and the active-baseline pre-placement are covered in browser_test.go.
