//go:build darwin

package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRootScanStopsAtNestedMountsOnDarwin(t *testing.T) {
	// Firmlinks splice the data volume into /, so a / scan reaches those files
	// twice unless the nested mounts are skipped — with or without --no-cross.
	ui := newBoundaryUI(t)

	runScan(t, ui, "/", scanOpts{})

	assert.True(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested),
		"a / scan is bounded on macOS even without --no-cross")
}

func TestRefreshOfRootStopsAtNestedMountsOnDarwin(t *testing.T) {
	ui := newBoundaryUI(t)

	runScan(t, ui, "/", scanOpts{transient: true})

	assert.True(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested),
		"refreshing / keeps the boundary")
}

func TestNonRootScanUnboundedOnDarwin(t *testing.T) {
	ui := newBoundaryUI(t)

	runScan(t, ui, boundaryFolder, scanOpts{})

	assert.False(t, ui.ShouldDirBeIgnoredAsNestedMount("backup", boundaryNested),
		"the rule is about / only — any other root still crosses by default")
}
