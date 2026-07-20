//go:build linux

package tui

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/stretchr/testify/assert"
)

func TestShowDevicesWithError(t *testing.T) {
	app, simScreen := testapp.CreateTestAppWithSimScreen(50, 50)
	defer simScreen.Fini()

	// a fresh TempDir subpath cannot exist, unlike a fixed absolute path a
	// root-run test elsewhere might have left behind
	getter := device.LinuxDevicesInfoGetter{MountsPath: filepath.Join(t.TempDir(), "nonexistent")}

	ui := CreateUI(app, simScreen, &bytes.Buffer{}, false, false, false, false)
	err := ui.ListDevices(getter)

	assert.Contains(t, err.Error(), "no such file")
}
