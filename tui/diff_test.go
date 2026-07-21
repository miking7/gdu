package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// diffCurrentTop is the live view:
//
//	top/big      30   (baseline 20 -> +10, grew)
//	top/newfile 100   (absent from baseline -> new)
//	top/sub/s     5   (unchanged)
func diffCurrentTop() *analyze.Dir {
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	top.AddFile(&analyze.File{Name: "big", Size: 30, Usage: 30, Parent: top})
	top.AddFile(&analyze.File{Name: "newfile", Size: 100, Usage: 100, Parent: top})
	sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: top}, ItemCount: 1}
	sub.AddFile(&analyze.File{Name: "s", Size: 5, Usage: 5, Parent: sub})
	top.AddFile(sub)
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

// diffBaselineTop is the past snapshot:
//
//	top/big       20
//	top/obsolete  50   (gone in the live view -> removed)
//	top/sub/s      5
func diffBaselineTop() *analyze.Dir {
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	top.AddFile(&analyze.File{Name: "big", Size: 20, Usage: 20, Parent: top})
	top.AddFile(&analyze.File{Name: "obsolete", Size: 50, Usage: 50, Parent: top})
	sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: top}, ItemCount: 1}
	sub.AddFile(&analyze.File{Name: "s", Size: 5, Usage: 5, Parent: sub})
	top.AddFile(sub)
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

// diffBaselineTime is the timestamp tests attach to a synthetic baseline.
func diffBaselineTime() time.Time {
	return time.Date(2026, 5, 31, 23, 59, 0, 0, time.Local)
}

func diffRowTexts(ui *UI) []string {
	var out []string
	for r := 0; r < ui.table.GetRowCount(); r++ {
		if c := ui.table.GetCell(r, 0); c != nil {
			out = append(out, c.Text)
		}
	}
	return out
}

func newDiffUI(t *testing.T) *UI {
	t.Helper()
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.currentDir = diffCurrentTop()
	ui.topDir = ui.currentDir
	ui.topDirPath = "top"
	return ui
}

// TestBaselineAsCreateUIOptionDoesNotPanic reproduces the --baseline CLI path,
// where the baseline is applied as a CreateUI option that runs before the header
// widget exists. updateDiffHeader must tolerate the not-yet-built header, and the
// banner must reflect diff mode once the header is created.
func TestBaselineAsCreateUIOptionDoesNotPanic(t *testing.T) {
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })

	b := analyze.BuildBaseline(diffBaselineTop(), "top", 0)

	var ui *UI
	assert.NotPanics(t, func() {
		ui = CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false,
			func(u *UI) { u.SetBaseline(b, snapAt(time.Date(2026, 5, 31, 23, 59, 0, 0, time.Local))) })
	})
	assert.True(t, ui.inDiffMode())
	assert.Contains(t, ui.header.GetText(true), "◇ Baseline  2026-05-31 23:59",
		"header should show the Baseline slot once the widget is built")
	assert.Contains(t, ui.header.GetText(true), "● Viewing   live",
		"and the Viewing slot naming what it is compared with")
	assert.Equal(t, 2, ui.headerLines,
		"the option-applied baseline records a two-line header even though it predates the widget")
	// The grid actually laying that header row out at two lines is covered by
	// TestBaselineHeaderRendersTwoGridRows (a field check can't see the clip).
	assert.Contains(t, ui.header.GetText(true), "Esc clear")
}

func TestDiffModeRendersDeltasMarkersAndRemoved(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))

	assert.True(t, ui.inDiffMode())

	texts := diffRowTexts(ui)
	joined := strings.Join(texts, "\n")
	assert.Contains(t, joined, "✦", "newfile should be marked new")
	assert.Contains(t, joined, "▲", "big should be marked grown")
	assert.Contains(t, joined, "✗", "obsolete should be marked removed")
	assert.Contains(t, joined, "obsolete")
	assert.Contains(t, joined, "removed")

	// Growth sort (default desc): the biggest grower is on top.
	assert.Contains(t, texts[0], "newfile")

	// Footer reconciles the change.
	footer := ui.footerLabel.GetText(true)
	assert.Contains(t, footer, "Sort: growth desc")
	assert.Contains(t, footer, "removed (1)")
}

func TestDiffSortFlip(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))

	// '<' puts the biggest shrink/removal on top.
	ui.setDiffSort(true)
	texts := diffRowTexts(ui)
	assert.Contains(t, texts[0], "obsolete")
	assert.Contains(t, ui.footerLabel.GetText(true), "Sort: growth asc")
}

func TestDiffThresholdedBaselineMarksApproximate(t *testing.T) {
	ui := newDiffUI(t)
	// A thresholded baseline can't be sure an absent item is truly new.
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 10), snapAt(diffBaselineTime()))

	joined := strings.Join(diffRowTexts(ui), "\n")
	assert.Contains(t, joined, "~", "newfile should be approximate under a thresholded baseline")
	assert.NotContains(t, joined, "✦")
}

func TestDiffModeRemovedRowIsInert(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))

	removedRow := -1
	for r := 0; r < ui.table.GetRowCount(); r++ {
		if c := ui.table.GetCell(r, 0); c != nil && strings.Contains(c.Text, "obsolete") {
			removedRow = r
		}
	}
	require.GreaterOrEqual(t, removedRow, 0, "removed row should be present")
	ui.table.Select(removedRow, 0)

	// A removed row carries no reference; info and double-click must be inert, not panic.
	assert.NotPanics(t, func() { ui.showInfo() })
	assert.False(t, ui.pages.HasPage("info"))
	assert.NotPanics(t, func() {
		ui.onMouse(tcell.NewEventMouse(0, 0, tcell.ButtonPrimary, 0), tview.MouseLeftDoubleClick)
	})
}

func TestClearBaselineRestoresNormalView(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	assert.True(t, ui.inDiffMode())

	ui.clearBaseline()
	assert.False(t, ui.inDiffMode())

	joined := strings.Join(diffRowTexts(ui), "\n")
	assert.NotContains(t, joined, "▲", "delta markers gone after clearing")
	assert.NotContains(t, joined, "obsolete", "removed rows gone after clearing")
	assert.Contains(t, joined, "big")
}

// TestEscClearsBaselineInDiffMode drives the real key entry point: pressing Esc
// in the plain diff browse view leaves diff mode and restores the normal rows.
func TestEscClearsBaselineInDiffMode(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	require.True(t, ui.inDiffMode())

	ui.keyPressed(tcell.NewEventKey(tcell.KeyEsc, 0, 0))
	assert.False(t, ui.inDiffMode(), "Esc should leave diff mode")

	joined := strings.Join(diffRowTexts(ui), "\n")
	assert.NotContains(t, joined, "▲", "delta markers gone after Esc")
	assert.NotContains(t, joined, "obsolete", "removed rows gone after Esc")
	assert.Contains(t, joined, "big")
}

// TestCInDiffModeDoesNotClearBaseline confirms 'c' keeps its normal meaning
// (toggle the item-count column) even in diff mode: the baseline stays set.
func TestCInDiffModeDoesNotClearBaseline(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	require.True(t, ui.inDiffMode())

	ui.keyPressed(tcell.NewEventKey(tcell.KeyRune, 'c', tcell.ModNone))
	assert.True(t, ui.inDiffMode(), "'c' must not clear the baseline")
}
