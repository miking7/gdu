package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/internal/testdev"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// launcherDevicesMock returns the root disk (bigger used) and an SD card, so
// path-prefix logic (deviceForPath) and the usage-desc default sort behave
// realistically.
func launcherDevicesMock() device.DevicesInfoGetter {
	mock := testdev.DevicesInfoGetterMock{}
	mock.Devices = launcherDevices()
	return mock
}

func launcherDevices() device.Devices {
	return device.Devices{
		{Name: "Macintosh HD", MountPoint: "/", Size: 1e12, Free: 5e11},       // used 5e11
		{Name: "SD Card", MountPoint: "/Volumes/SD", Size: 512e9, Free: 3e11}, // used 212e9
	}
}

// devicesWithSystemVolume adds a macOS system volume (filtered from the launcher
// display).
func devicesWithSystemVolume() device.Devices {
	return device.Devices{
		{Name: "disk1s1", MountPoint: "/", Size: 1e12, Free: 5e11},
		{Name: "disk1s2", MountPoint: "/System/Volumes/Data", Size: 1e12, Free: 5e11},
		{Name: "SD Card", MountPoint: "/Volumes/SD", Size: 512e9, Free: 3e11},
	}
}

func newLauncherUI(t *testing.T) *UI {
	t.Helper()
	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	return ui
}

func TestBuildLauncherRowsExplicitPath(t *testing.T) {
	// gdu <path>: folder pinned first, "(specified folder)", pre-select the folder.
	rows, preselect := buildLauncherRows("/Users/x/proj", launcherDevices(), true)

	require.Len(t, rows, 4) // folder, root disk, SD disk, Other folder…
	assert.Equal(t, launcherFolder, rows[0].kind)
	assert.Equal(t, "/Users/x/proj", rows[0].root)
	assert.Equal(t, "/", rows[0].mount, "the folder's most-specific mount point")
	assert.Equal(t, "(specified folder)", rows[0].note)

	assert.Equal(t, launcherDisk, rows[1].kind)
	assert.Equal(t, "/", rows[1].root, "usage-desc default puts the bigger disk first")
	assert.Equal(t, launcherDisk, rows[2].kind)
	assert.Equal(t, "/Volumes/SD", rows[2].root)
	assert.Equal(t, launcherOther, rows[3].kind)

	assert.Equal(t, 0, preselect, "an explicit path pre-selects the folder row")
}

func TestBuildLauncherRowsBareLaunch(t *testing.T) {
	// bare gdu: "(current folder)", pre-select the cwd's disk row.
	rows, preselect := buildLauncherRows("/Users/x/proj", launcherDevices(), false)
	assert.Equal(t, "(current folder)", rows[0].note)
	assert.Equal(t, 1, preselect, "a bare launch pre-selects the cwd's disk row")
	assert.Equal(t, "/", rows[preselect].root)
}

func TestBuildLauncherRowsFolderIsMountRoot(t *testing.T) {
	// When the default dir IS a mount root, its folder row is omitted (no
	// duplicate of the disk row), and pre-selection falls to that disk.
	rows, preselect := buildLauncherRows("/", launcherDevices(), false)

	require.Len(t, rows, 3) // root disk, SD disk, Other folder…
	assert.Equal(t, launcherDisk, rows[0].kind)
	assert.Equal(t, "/", rows[0].root)
	assert.Equal(t, 0, preselect)
}

func TestBuildLauncherRowsFiltersSystemVolumes(t *testing.T) {
	// /System/Volumes/* is hidden from the launcher display.
	rows, _ := buildLauncherRows("/Users/x", devicesWithSystemVolume(), false)
	for _, r := range rows {
		if r.kind == launcherDisk {
			assert.NotEqual(t, "/System/Volumes/Data", r.root, "system volumes are filtered out")
		}
	}
	// root + SD remain, plus folder + Other folder.
	assert.Len(t, rows, 4)
}

func TestLauncherRowMapsSnapshot(t *testing.T) {
	// Disk rows match their exact mount point.
	disk := &launcherRow{kind: launcherDisk, root: "/"}
	assert.True(t, launcherRowMapsSnapshot(disk, "/"))
	assert.False(t, launcherRowMapsSnapshot(disk, "/Volumes/SD"), "a / snapshot is not the SD disk's")
	assert.False(t, launcherRowMapsSnapshot(disk, "/Users"), "not a sub-root either")

	// Folder on the root filesystem: roots between its mount (/) and itself.
	rootFolder := &launcherRow{kind: launcherFolder, root: "/Users/x/proj", mount: "/"}
	assert.True(t, launcherRowMapsSnapshot(rootFolder, "/"), "a / scan covers a folder on /")
	assert.True(t, launcherRowMapsSnapshot(rootFolder, "/Users/x/proj"))
	assert.False(t, launcherRowMapsSnapshot(rootFolder, "/Volumes/SD"), "a cross-mount root does not map")

	// Folder on the SD card: only SD-rooted (or deeper) snapshots, never /'s.
	sdFolder := &launcherRow{kind: launcherFolder, root: "/Volumes/SD/photos", mount: "/Volumes/SD"}
	assert.True(t, launcherRowMapsSnapshot(sdFolder, "/Volumes/SD"))
	assert.False(t, launcherRowMapsSnapshot(sdFolder, "/"), "a / scan ignored the SD card, so it does not map")
}

func TestDeviceForPath(t *testing.T) {
	devs := launcherDevices()
	assert.Equal(t, "/", device.ForPath(devs, "/Users/x/proj").MountPoint, "root covers a home path")
	assert.Equal(t, "/Volumes/SD", device.ForPath(devs, "/Volumes/SD/photos").MountPoint,
		"the longest-prefix mount wins")
	assert.Nil(t, device.ForPath(nil, "/anything"))
}

func TestAbbrevHome(t *testing.T) {
	assert.Equal(t, "~/proj", abbrevHome("/home/u/proj", "/home/u"))
	assert.Equal(t, "~", abbrevHome("/home/u", "/home/u"))
	assert.Equal(t, "/etc", abbrevHome("/etc", "/home/u"))
	assert.Equal(t, "/home/user2", abbrevHome("/home/user2", "/home/u"), "prefix must be a path boundary")
	assert.Equal(t, "/x", abbrevHome("/x", ""), "no home dir → unchanged")
}

func TestResolveOtherFolder(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveOtherFolder(dir)
	require.NoError(t, err)
	assert.Equal(t, dir, got)

	_, err = resolveOtherFolder(filepath.Join(dir, "nope"))
	assert.ErrorContains(t, err, "no such folder")

	file := filepath.Join(dir, "afile")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	_, err = resolveOtherFolder(file)
	assert.ErrorContains(t, err, "not a folder")

	if home, herr := os.UserHomeDir(); herr == nil {
		got, err := resolveOtherFolder("~")
		require.NoError(t, err)
		assert.Equal(t, home, got, "~ expands to the home dir")
	}
}

func TestSudoTipBody(t *testing.T) {
	ui := newLauncherUI(t)

	passive := ui.sudoTipBody(&launcherRow{root: "/"})
	assert.Contains(t, passive, "restart with sudo")
	assert.NotContains(t, passive, "couldn't read")

	evidence := ui.sudoTipBody(&launcherRow{
		root: "/",
		covering: []report.SnapshotListing{
			{SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/", ErrCount: 18}},
		},
	})
	assert.Contains(t, evidence, "couldn't read 18 folders")
	assert.Contains(t, evidence, "sudo")

	clean := ui.sudoTipBody(&launcherRow{
		root:     "/",
		covering: []report.SnapshotListing{{SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/"}}},
	})
	assert.NotContains(t, clean, "couldn't read")
}

func TestOpenLauncherRendersDeviceTable(t *testing.T) {
	ui := newLauncherUI(t)
	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "/Users/x/proj", true))

	require.NotNil(t, ui.launcher)
	assert.True(t, ui.pages.HasPage(launcherPage))
	assert.True(t, ui.usingLauncher)
	assert.Len(t, ui.launcher.rows, 4)
	assert.True(t, ui.launcher.fillDone)
	assert.False(t, ui.launcher.hasSnapCol, "no snapshot column until history exists")

	tb := ui.launcher.table
	assert.Equal(t, "Device name", tb.GetCell(0, 0).Text, "header row present")
	// table row 0 = header; folder at row 1, root disk at row 2.
	assert.Contains(t, tb.GetCell(1, deviceMountCol).Text, "proj", "folder path in the mount column")
	assert.Contains(t, tb.GetCell(1, deviceMountCol).Text, "(specified folder)")
	assert.Contains(t, tb.GetCell(2, 0).Text, "Macintosh HD", "disk name in column 0")
	assert.Contains(t, tb.GetCell(2, deviceMountCol).Text, "/", "disk mount point")
}

func TestLauncherSnapshotColumnMountAccurate(t *testing.T) {
	ui := newLauncherUI(t)
	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "/Users/x/proj", true))
	require.False(t, ui.launcher.hasSnapCol)

	// A "/" snapshot maps to the folder (on /) and the root disk, but not SD.
	ui.applyLauncherCovering([]report.SnapshotListing{
		{SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/", ScanTs: time.Now().Add(-2 * time.Hour)}},
	})

	assert.True(t, ui.launcher.hasSnapCol, "covering history reveals the snapshot column")
	tb := ui.launcher.table
	// With the Snapshot column shown, it sits at deviceMountCol; Mount point shifts right.
	assert.Contains(t, tb.GetCell(1, deviceMountCol).Text, "snapshot", "folder on / gets the / snapshot")
	assert.Contains(t, tb.GetCell(2, deviceMountCol).Text, "snapshot", "root disk gets its exact snapshot")
	assert.Empty(t, tb.GetCell(3, deviceMountCol).Text, "SD disk is not credited a / snapshot")
}

func TestBuildLauncherRowsPinsOwnDisk(t *testing.T) {
	// The default-dir's own disk is pinned directly below the folder row.
	rows, preselect := buildLauncherRows("/Users/x/proj", launcherDevices(), false)
	require.Equal(t, launcherFolder, rows[0].kind)
	assert.Equal(t, launcherDisk, rows[1].kind)
	assert.True(t, rows[1].pinned, "the own disk is pinned")
	assert.Equal(t, "/", rows[1].root, "the disk the folder lives on")
	assert.Equal(t, "/Users/x/proj", rows[1].land, "the pinned disk lands at the default dir")
	assert.Equal(t, 1, preselect, "a bare launch pre-selects the pinned own disk")
	// Other disks are not pinned and land at their root.
	assert.False(t, rows[2].pinned)
	assert.Empty(t, rows[2].land)
}

func TestLauncherToggleSortKeepsOwnDiskPinned(t *testing.T) {
	// Own disk "/" is named "zdisk" so a name sort would move it LAST — but being
	// pinned it stays directly below the folder.
	getter := testdev.DevicesInfoGetterMock{Devices: device.Devices{
		{Name: "zdisk", MountPoint: "/", Size: 1e12, Free: 5e11},
		{Name: "adisk", MountPoint: "/Volumes/A", Size: 4e9, Free: 1e9},
		{Name: "mdisk", MountPoint: "/Volumes/M", Size: 6e9, Free: 1e9},
	}}
	ui := newLauncherUI(t)
	require.NoError(t, ui.OpenLauncher(getter, "/Users/x", false))
	require.Equal(t, "/", ui.launcher.rows[1].root, "own disk pinned below the folder")

	ui.launcherToggleSort() // n → name asc
	assert.True(t, ui.launcher.sortByName)
	assert.Equal(t, launcherFolder, ui.launcher.rows[0].kind, "folder stays pinned first")
	assert.Equal(t, "/", ui.launcher.rows[1].root, "own disk stays pinned, not sorted to last by name")
	assert.Equal(t, "/Volumes/A", ui.launcher.rows[2].root, "the other disks sort by name: adisk before mdisk")
	assert.Equal(t, "/Volumes/M", ui.launcher.rows[3].root)
	assert.Equal(t, launcherOther, ui.launcher.rows[4].kind, "Scan-another-folder stays pinned last")
}

func TestFinishRootScanLandsAtLandPath(t *testing.T) {
	// A whole-disk scan with a covered landPath lands the view there.
	ui := newLauncherUI(t)
	root := &analyze.Dir{File: &analyze.File{Name: "disk"}, BasePath: "/"}
	sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: root}}
	root.AddFile(sub)
	root.UpdateStats(make(fs.HardLinkedItems))
	ui.pages.AddPage(scanProgressPage, tview.NewBox(), true, false)

	ui.finishRootScan(root, "/disk", time.Now(), parquet.SnapshotKey{}, false, scanOpts{landPath: "/disk/sub"})

	assert.Equal(t, "/disk/sub", ui.currentDirPath, "the view lands at landPath, not the scan root")
	assert.Equal(t, "/disk", ui.topDirPath, "the whole disk is still the scanned tree")
}

func TestLauncherScanFolderClosesLauncherAndScans(t *testing.T) {
	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	defer sim.Fini()
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.Analyzer = &testanalyze.MockedAnalyzer{}
	ui.done = make(chan struct{})

	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "test_dir", true))
	require.Equal(t, launcherFolder, ui.launcher.rows[0].kind)

	ui.launcherActivate(1) // Enter on the folder row (table row 1, past the header)
	<-ui.done
	drainUpdates(ui)

	assert.Nil(t, ui.launcher, "the launcher is dismissed once a scan starts")
	assert.False(t, ui.pages.HasPage(launcherPage))
	assert.Equal(t, "test_dir", ui.currentDir.GetName(), "the chosen folder is scanned")
}

func TestLauncherOpenLatestSnapshot(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, testanalyze.WriteSnapshot(dir, "snap.parquet", "/root", "f", time.Unix(1700000000, 0).UTC()))

	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	defer sim.Fini()
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.SetSnapshotsDir(dir)
	ui.done = make(chan struct{})

	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "/root", true))
	// Map the real archived snapshot (full identity, incl. host) so the load can
	// find it; the /root folder maps it (mount is /, so root is between / and /root).
	listings, err := report.ListSnapshotsInDir(dir)
	require.NoError(t, err)
	ui.applyLauncherCovering(listings)
	require.NotEmpty(t, ui.launcher.rows[0].covering, "the /root folder maps the /root snapshot")
	ui.launcher.fillDone = true
	ui.launcher.table.Select(1, 0) // the /root folder row (past the header)

	ui.launcherOpenLatest() // key s
	ui.snapshotWork.Wait()
	drainUpdates(ui)

	require.NotNil(t, ui.currentView)
	assert.False(t, ui.viewIsLive(), "s opens a read-only snapshot View")
	assert.Nil(t, ui.launcher, "the launcher is dismissed")
	require.NotNil(t, ui.returnView)
	assert.Equal(t, ui.currentView, ui.returnView, "the opened snapshot is the session's return view")
}

func TestLauncherPickSnapshotStacksPickerOverLauncher(t *testing.T) {
	ui := newLauncherUI(t)
	ui.SetSnapshotsDir("/some/archive")
	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "/root", true))
	ui.applyLauncherCovering([]report.SnapshotListing{
		{SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/root", ScanTs: time.Unix(1700000000, 0).UTC()}, File: "snap.parquet"},
	})
	ui.launcher.fillDone = true
	ui.launcher.table.Select(1, 0)

	ui.launcherPickSnapshot() // key S

	assert.True(t, ui.pages.HasPage("snapshotpicker"), "S opens the snapshot picker")
	assert.True(t, ui.pages.HasPage(launcherPage), "the picker stacks over the launcher so Esc returns to it")
}

func TestLauncherOtherFolderInputOpens(t *testing.T) {
	ui := newLauncherUI(t)
	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "/root", true))

	other := len(ui.launcher.rows) - 1
	require.Equal(t, launcherOther, ui.launcher.rows[other].kind)
	ui.launcherActivate(other + 1) // Enter on "Other folder…" (table row past the header)

	assert.True(t, ui.pages.HasPage(launcherInputPage), "the Other-folder input opens")
}

func TestLauncherOpenLatestNoHistoryNotice(t *testing.T) {
	ui := newLauncherUI(t)
	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "/root", true))
	ui.launcher.fillDone = true // no archive → no covering
	ui.launcher.table.Select(1, 0)

	ui.launcherOpenLatest() // key s with no covering snapshot

	assert.Nil(t, ui.currentView, "no View is opened when there is no snapshot")
	assert.True(t, ui.pages.HasPage(launcherPage), "the launcher stays up")
}

func TestDeviceMountWidthAdaptsToWidth(t *testing.T) {
	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	defer sim.Fini()
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	devs := launcherDevices()

	sim.SetSize(200, 50)
	wide := ui.deviceMountWidth(devs, false)
	sim.SetSize(130, 50)
	mid := ui.deviceMountWidth(devs, false)
	assert.Greater(t, wide, mid, "a wider terminal gives the mount column more room")

	sim.SetSize(60, 50)
	assert.Equal(t, 20, ui.deviceMountWidth(devs, false), "a narrow terminal floors to the readable minimum")

	// The Snapshot column reserves extra width, so the mount column shrinks.
	sim.SetSize(130, 50)
	assert.Less(t, ui.deviceMountWidth(devs, true), mid, "the snapshot column reserves width")
}

func TestLauncherScanResetsIgnorePatternForFolder(t *testing.T) {
	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	defer sim.Fini()
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.Analyzer = &testanalyze.MockedAnalyzer{}
	ui.done = make(chan struct{})

	require.NoError(t, ui.SetIgnoreDirPatterns([]string{"myignore"}))
	base := ui.IgnoreDirPathPatterns
	require.NotNil(t, base)

	require.NoError(t, ui.OpenLauncher(launcherDevicesMock(), "/root", true))
	assert.Equal(t, base, ui.launcherBaseIgnore, "OpenLauncher captures the user's ignore pattern")

	// A disk scan overwrites the ignore pattern with that disk's nested mounts.
	// (launcherRunScan, not launcherScan: a / root would trip the forced sudo prompt.)
	ui.launcherRunScan(&launcherRow{kind: launcherDisk, root: "/", dev: &device.Device{MountPoint: "/", Size: 100}})
	assert.NotEqual(t, base, ui.IgnoreDirPathPatterns, "a disk scan sets its nested-mount ignore pattern")
	assert.EqualValues(t, 100, ui.currentDeviceSize)
	<-ui.done
	drainUpdates(ui)

	// A subsequent folder scan must restore the user's base pattern and clear the
	// device size — not leak the disk's.
	ui.launcherScan(&launcherRow{kind: launcherFolder, root: "test_dir"})
	assert.Equal(t, base, ui.IgnoreDirPathPatterns, "a folder scan restores the user's configured ignore pattern")
	assert.Zero(t, ui.currentDeviceSize, "a folder scan clears the device size")
	<-ui.done
	drainUpdates(ui)
}

func TestLauncherIgnoresUseUnfilteredMounts(t *testing.T) {
	// The display hides /System/Volumes/*, but nested-mount ignore
	// computation must use the FULL list, or scanning / would descend into
	// /System/Volumes/Data and double-count the disk.
	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	defer sim.Fini()
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.Analyzer = &testanalyze.MockedAnalyzer{}
	ui.done = make(chan struct{})

	mock := testdev.DevicesInfoGetterMock{}
	mock.Devices = devicesWithSystemVolume()
	require.NoError(t, ui.OpenLauncher(mock, "/Users/x", false))

	// Hidden from the rows...
	for _, r := range ui.launcher.rows {
		if r.kind == launcherDisk {
			require.NotEqual(t, "/System/Volumes/Data", r.root)
		}
	}
	// ...but ui.devices stays unfiltered, so a / scan's ignore pattern covers it.
	rootDev := ui.devices[0]
	require.Equal(t, "/", rootDev.MountPoint)
	ui.launcherRunScan(&launcherRow{kind: launcherDisk, root: "/", dev: rootDev})
	require.NotNil(t, ui.IgnoreDirPathPatterns)
	assert.True(t, ui.IgnoreDirPathPatterns.MatchString("/System/Volumes/Data"),
		"scanning / ignores the hidden system volume (no double-count)")
	<-ui.done
	drainUpdates(ui)
}

func TestLauncherLeftArrowReturnsToLauncher(t *testing.T) {
	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	defer sim.Fini()
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.getter = launcherDevicesMock()
	ui.usingLauncher = true

	ui.topDirPath = "/root"
	ui.currentDirPath = "/root"

	ui.handleLeft()

	assert.True(t, ui.pages.HasPage(launcherPage), "left-arrow at a live tree's top returns to the launcher")
	require.NotNil(t, ui.launcher)
}

// drainUpdates runs any queued QueueUpdateDraw closures (once each), the way the
// event loop would, until the queue is empty.
func drainUpdates(ui *UI) {
	mocked := ui.app.(*testapp.MockedApp)
	for {
		pending := mocked.GetUpdateDraws()
		if len(pending) == 0 {
			return
		}
		for _, f := range pending {
			f()
		}
	}
}
