//go:build darwin

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetNoCrossBoundsRootScanOnDarwin(t *testing.T) {
	// Firmlinks splice the data volume into /, so a / scan double-counts it
	// unless the nested mounts are skipped — whether or not --no-cross was given.
	a := App{Flags: &Flags{}, Getter: noCrossDevices()}

	require.NoError(t, a.setNoCross(&uiTimeFilterMock{}, "/"))

	assert.Contains(t, a.Flags.IgnoreDirs, "/mnt/data")
}

func TestSetNoCrossLeavesOtherRootsAloneOnDarwin(t *testing.T) {
	a := App{Flags: &Flags{}, Getter: noCrossDevices()}

	require.NoError(t, a.setNoCross(&uiTimeFilterMock{}, "/home"))

	assert.Empty(t, a.Flags.IgnoreDirs, "crossing a mount below any other root stays the user's call")
}
