//go:build linux

package stdout

import (
	"bytes"
	"path/filepath"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/stretchr/testify/assert"
)

func init() {
	log.SetLevel(log.WarnLevel)
}

func TestShowDevicesWithErr(t *testing.T) {
	output := bytes.NewBuffer(make([]byte, 10))

	// a fresh TempDir subpath cannot exist, unlike a fixed absolute path a
	// root-run test elsewhere might have left behind
	getter := device.LinuxDevicesInfoGetter{MountsPath: filepath.Join(t.TempDir(), "nonexistent")}
	ui := CreateStdoutUI(output, false, true, false, false, false, false, false, "", 0, false, 0)
	err := ui.ListDevices(getter)

	assert.Contains(t, err.Error(), "no such file")
}
