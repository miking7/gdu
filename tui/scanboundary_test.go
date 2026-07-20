package tui

import (
	"bytes"
	"testing"

	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// boundaryFolder is a plain folder — not a mount point — with another
	// filesystem mounted inside it.
	boundaryFolder = "/home/user"
	boundaryNested = "/home/user/backup"
)

// boundaryMountsMock offers one mount nested inside boundaryFolder, so a test
// can tell whether a scan resolved its boundary.
type boundaryMountsMock struct{}

func boundaryDevices() device.Devices {
	return device.Devices{
		{Name: "/dev/disk1", MountPoint: "/", Size: 1e12, Free: 5e11},
		{Name: "/dev/disk2", MountPoint: boundaryNested, Size: 1e11, Free: 5e10},
	}
}

func (boundaryMountsMock) GetDevicesInfo() (device.Devices, error) { return boundaryDevices(), nil }
func (boundaryMountsMock) GetMounts() (device.Devices, error)      { return boundaryDevices(), nil }

func newBoundaryUI(t *testing.T) *UI {
	t.Helper()
	app := testapp.CreateMockedApp(false)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.Analyzer = &testanalyze.MockedAnalyzer{}
	ui.done = make(chan struct{})
	ui.SetDevicesGetter(boundaryMountsMock{})
	return ui
}

// runScan drives one scan to completion the way the event loop would.
func runScan(t *testing.T, ui *UI, path string, opts scanOpts) {
	t.Helper()
	require.NoError(t, ui.analyzePath(path, nil, opts))
	<-ui.done
	drainUpdates(ui)
}

func TestFolderScanCrossesMountsByDefault(t *testing.T) {
	ui := newBoundaryUI(t)

	runScan(t, ui, boundaryFolder, scanOpts{})

	assert.False(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested),
		"without --no-cross a folder scan descends into a nested mount")
}

func TestFolderScanHonorsNoCross(t *testing.T) {
	// --no-cross used to be resolved once at startup, against the working
	// directory, so it did nothing for a root the user picked afterwards.
	ui := newBoundaryUI(t)
	ui.SetNoCross(true)

	runScan(t, ui, boundaryFolder, scanOpts{})

	assert.True(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested),
		"--no-cross bounds a scan of any root, not just the startup path")
}

func TestWholeDeviceScanAlwaysStopsAtNestedMounts(t *testing.T) {
	ui := newBoundaryUI(t)

	runScan(t, ui, "/", scanOpts{wholeDevice: true})

	assert.True(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested),
		"measuring a device excludes what is mounted inside it")
}

func TestScanBoundaryIsResolvedPerScan(t *testing.T) {
	// Each scan replaces the boundary, so a disk scan's does not linger over the
	// folder scan that follows it.
	ui := newBoundaryUI(t)

	runScan(t, ui, "/", scanOpts{wholeDevice: true})
	require.True(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested))

	runScan(t, ui, boundaryFolder, scanOpts{})
	assert.False(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested),
		"the previous scan's boundary is gone")
}

func TestTransientScanResolvesBoundaryToo(t *testing.T) {
	// Refreshes (r) and go-live spot-rescans reach analyzePath with
	// transient set; they get the same boundary as any other scan.
	ui := newBoundaryUI(t)
	ui.SetNoCross(true)

	runScan(t, ui, boundaryFolder, scanOpts{transient: true})

	assert.True(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested))
}

func TestScanWithoutDevicesGetterStillRuns(t *testing.T) {
	// A UI built without a getter (tests, embedders) must not panic; it just
	// scans without a mount boundary.
	ui := newBoundaryUI(t)
	ui.SetDevicesGetter(nil)
	ui.SetNoCross(true)

	runScan(t, ui, boundaryFolder, scanOpts{})

	assert.False(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested))
}
