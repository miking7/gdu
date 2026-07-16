package tui

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/device"
)

func TestBuildSudoArgv(t *testing.T) {
	t.Run("verbatim args, no config", func(t *testing.T) {
		got := buildSudoArgv("/usr/local/bin/gdu", []string{"-d", "/data"}, "")
		assert.Equal(t, []string{"sudo", "--", "/usr/local/bin/gdu", "-d", "/data"}, got)
	})
	t.Run("forwards config when loaded and not already named", func(t *testing.T) {
		got := buildSudoArgv("/gdu", nil, "/home/u/.config/gdu/gdu.yaml")
		assert.Equal(t, []string{"sudo", "--", "/gdu", "--config-file", "/home/u/.config/gdu/gdu.yaml"}, got)
	})
	t.Run("no duplicate when --config-file already given", func(t *testing.T) {
		got := buildSudoArgv("/gdu", []string{"--config-file", "/x.yaml"}, "/y.yaml")
		assert.Equal(t, []string{"sudo", "--", "/gdu", "--config-file", "/x.yaml"}, got)
	})
	t.Run("no duplicate for the --config-file= form", func(t *testing.T) {
		got := buildSudoArgv("/gdu", []string{"--config-file=/x.yaml"}, "/y.yaml")
		assert.Equal(t, []string{"sudo", "--", "/gdu", "--config-file=/x.yaml"}, got)
	})
}

func TestIsRootVolume(t *testing.T) {
	assert.True(t, isRootVolume("/"))
	assert.True(t, isRootVolume("//"))
	assert.False(t, isRootVolume("/Users/x"))
	assert.False(t, isRootVolume("test_dir"))
	assert.False(t, isRootVolume(""))
}

// TestRestartElevatedInvokesReexec drives restartElevated through the mocked
// Suspend (which runs its func inline) and asserts the assembled sudo argv,
// without ever exec'ing — sudo restarts couldn't be validated cleanly before
// this reexec seam existed.
func TestRestartElevatedInvokesReexec(t *testing.T) {
	ui := newLauncherUI(t)
	ui.configFilePath = "/tmp/gdu-test.yaml"
	var gotArgv []string
	called := false
	ui.reexec = func(argv, _ []string) error {
		called = true
		gotArgv = argv
		return nil
	}

	ui.restartElevated()

	require.True(t, called, "restartElevated hands the terminal to reexec")
	self, err := os.Executable()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(gotArgv), 5)
	assert.Equal(t, "sudo", gotArgv[0])
	assert.Equal(t, "--", gotArgv[1])
	assert.Equal(t, self, gotArgv[2])
	// The configured config file is forwarded (the test binary's args name none).
	assert.Equal(t, "--config-file", gotArgv[len(gotArgv)-2])
	assert.Equal(t, "/tmp/gdu-test.yaml", gotArgv[len(gotArgv)-1])
}

func TestConfirmScanElevatedShowsModal(t *testing.T) {
	ui := newLauncherUI(t)
	ui.confirmScanElevated(&launcherRow{kind: launcherDisk, root: "/", dev: &device.Device{MountPoint: "/"}})
	assert.True(t, ui.pages.HasPage(sudoModalPage), "the / interstitial is shown")
}

func TestConfirmRestartElevatedShowsModal(t *testing.T) {
	ui := newLauncherUI(t)
	ui.confirmRestartElevated()
	assert.True(t, ui.pages.HasPage(sudoModalPage), "the manual restart modal is shown")
}

// TestLauncherScanPromptsForRootVolume checks the gate: a / scan diverts to the
// interstitial instead of scanning. euid>0 only — as root the offer is moot.
func TestLauncherScanPromptsForRootVolume(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("restart-elevated gate is euid>0 only; running as root")
	}
	ui := newLauncherUI(t)
	ui.launcherScan(&launcherRow{kind: launcherDisk, root: "/", dev: &device.Device{MountPoint: "/"}})
	assert.True(t, ui.pages.HasPage(sudoModalPage),
		"scanning / prompts for sudo instead of scanning immediately")
}
