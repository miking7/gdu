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
	assert.Contains(t, footer, "Sorting by: Δ desc")
	assert.Contains(t, footer, "removed (1)")
}

func TestDiffSortFlip(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))

	// Default is Δ desc (biggest growth first); D flips to Δ asc, putting the
	// biggest shrink/removal on top. Drive it through the real key entry point.
	ui.keyPressed(tcell.NewEventKey(tcell.KeyRune, 'D', tcell.ModNone))
	texts := diffRowTexts(ui)
	assert.Contains(t, texts[0], "obsolete")
	assert.Contains(t, ui.footerLabel.GetText(true), "Sorting by: Δ asc")
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

// diffRowContaining returns the text of the first table row containing needle.
func diffRowContaining(ui *UI, needle string) string {
	for r := 0; r < ui.table.GetRowCount(); r++ {
		if c := ui.table.GetCell(r, 0); c != nil && strings.Contains(c.Text, needle) {
			return c.Text
		}
	}
	return ""
}

func pressRune(ui *UI, r rune) {
	ui.keyPressed(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
}

// TestCompareViewHasNormalAnatomy confirms a present row keeps the plain table's
// size column and usage bar and gains the appended Δ column (fixes F3, which
// dropped columns and rescaled the bar to |Δ|).
func TestCompareViewHasNormalAnatomy(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))

	newfile := diffRowContaining(ui, "newfile")
	require.NotEmpty(t, newfile)
	assert.Contains(t, newfile, "▏", "the usage bar still renders (normal anatomy)")
	assert.Contains(t, newfile, "✦ +100 B", "the appended Δ column renders the growth")

	// Present rows carry their item so d/e/space act on them; removed rows don't.
	bigRef := cellRefContaining(ui, "big")
	_, ok := bigRef.(fs.Item)
	assert.True(t, ok, "present rows carry the item for mutation")
	assert.Nil(t, cellRefContaining(ui, "obsolete"), "removed rows are inert")
}

func cellRefContaining(ui *UI, needle string) interface{} {
	for r := 0; r < ui.table.GetRowCount(); r++ {
		if c := ui.table.GetCell(r, 0); c != nil && strings.Contains(c.Text, needle) {
			return c.GetReference()
		}
	}
	return nil
}

// TestTabTogglesCompareRendering drives the real Tab key: it flips Δ rendering
// on and off without clearing the baseline, and the header tail tracks it.
func TestTabTogglesCompareRendering(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	require.True(t, ui.renderingDelta())
	assert.Contains(t, ui.header.GetText(false), "Δ shown · Tab plain")
	assert.Contains(t, strings.Join(diffRowTexts(ui), "\n"), "obsolete")

	// Tab → plain rows; the baseline persists, the header says so.
	ui.keyPressed(tcell.NewEventKey(tcell.KeyTab, 0, 0))
	assert.True(t, ui.inDiffMode(), "Tab keeps the baseline")
	assert.False(t, ui.renderingDelta(), "Tab hid the Δ column")
	joined := strings.Join(diffRowTexts(ui), "\n")
	assert.NotContains(t, joined, "▲", "plain rows carry no delta markers")
	assert.NotContains(t, joined, "obsolete", "removed rows are gone in the plain rendering")
	assert.Contains(t, joined, "big")
	assert.Contains(t, ui.header.GetText(false), "Δ hidden · Tab compare")

	// Tab again → back to compare.
	ui.keyPressed(tcell.NewEventKey(tcell.KeyTab, 0, 0))
	assert.True(t, ui.renderingDelta())
	assert.Contains(t, strings.Join(diffRowTexts(ui), "\n"), "obsolete")
}

// TestTabAndDTeachFlashWithoutCompare covers the transitional teach-flashes:
// Tab and D with nothing to compare name today's key for setting a baseline.
func TestTabAndDTeachFlashWithoutCompare(t *testing.T) {
	ui := newDiffUI(t)
	require.False(t, ui.inDiffMode())

	ui.keyPressed(tcell.NewEventKey(tcell.KeyTab, 0, 0))
	assert.Contains(t, ui.header.GetText(false), "no baseline set — S to compare")

	pressRune(ui, 'D')
	assert.Contains(t, ui.header.GetText(false), "no baseline set — S to compare")

	// With a baseline set but Δ toggled off, D points at Tab instead.
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	ui.keyPressed(tcell.NewEventKey(tcell.KeyTab, 0, 0)) // hide Δ
	pressRune(ui, 'D')
	assert.Contains(t, ui.header.GetText(false), "Δ hidden — Tab to compare")
}

// TestPerModeSortMemory confirms the plain and compare views keep independent
// (sortBy, order): sorting in one never disturbs the other, session-scoped.
func TestPerModeSortMemory(t *testing.T) {
	ui := newDiffUI(t)

	// Plain view starts at size/desc; 'n' switches it to name/asc.
	pressRune(ui, 'n')
	assert.Equal(t, "name", ui.sortBy)
	assert.Equal(t, "asc", ui.sortOrder)
	// Compare view keeps its own default (Δ desc), untouched.
	assert.Equal(t, deltaSortKey, ui.diffSortBy)
	assert.Equal(t, descOrder, ui.diffSortOrder)

	// Enter compare; 's' now sorts the compare view only.
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	pressRune(ui, 's')
	assert.Equal(t, "size", ui.diffSortBy)
	assert.Equal(t, "asc", ui.diffSortOrder)
	assert.Equal(t, "name", ui.sortBy, "the plain sort is remembered across the compare sort")
	assert.Equal(t, "asc", ui.sortOrder)

	// Tab back to plain; 's' sorts the plain view, leaving the compare sort.
	ui.keyPressed(tcell.NewEventKey(tcell.KeyTab, 0, 0))
	pressRune(ui, 's')
	assert.Equal(t, "size", ui.sortBy)
	assert.Equal(t, "size", ui.diffSortBy, "the compare sort survives a plain re-sort")
	assert.Equal(t, "asc", ui.diffSortOrder)
}

// TestMarksRenderInCompareView confirms marking works and is visible in the
// compare view — the natural "sort by growth → mark → delete" workflow.
func TestMarksRenderInCompareView(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))

	bigRow := -1
	for r := 0; r < ui.table.GetRowCount(); r++ {
		if strings.Contains(ui.table.GetCell(r, 0).Text, "big") {
			bigRow = r
		}
	}
	require.GreaterOrEqual(t, bigRow, 0)
	ui.table.Select(bigRow, 0)
	ui.handleMark()

	assert.Len(t, ui.markedRows, 1, "the present row was marked")
	marked := diffRowContaining(ui, "big")
	assert.Contains(t, marked, "✓", "the mark glyph renders in the compare view")
	assert.Contains(t, marked, "▲", "and the row keeps its Δ marker")
}

// TestDeleteReRendersCompareView confirms a deletion (here simulated by removing
// the item from the live tree) re-renders the compare view with updated deltas:
// the removed item flips to an inline removed row.
func TestDeleteReRendersCompareView(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	assert.Contains(t, diffRowContaining(ui, "big"), "▲", "big starts as a grown row")

	ui.currentDir.RemoveFileByName("big")
	ui.showDir()

	row := diffRowContaining(ui, "big")
	assert.Contains(t, row, "✗", "the deleted item now shows as removed")
	assert.Contains(t, row, "removed")
}

// markFirstPresentRow selects and marks the first row carrying a real item.
func markFirstPresentRow(ui *UI) {
	for r := 0; r < ui.table.GetRowCount(); r++ {
		c := ui.table.GetCell(r, 0)
		if c != nil && c.GetReference() != nil {
			ui.table.Select(r, 0)
			ui.handleMark()
			return
		}
	}
}

// TestCompareTransitionsResetSelection guards the index-keyed mark/ignore maps:
// the plain and compare renderings order rows differently (own sort, plus
// compare's inline removed rows), so a mark left over from one rendering would
// silently attach to a different item — and deleteMarked would delete the wrong
// one, or panic on a reference-less removed row. Every mode transition must
// reset the maps, exactly as a normal re-sort does.
func TestCompareTransitionsResetSelection(t *testing.T) {
	setBaseline := func(ui *UI) {
		ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	}

	// Tab (compare → plain) resets marks and ignores.
	ui := newDiffUI(t)
	setBaseline(ui)
	markFirstPresentRow(ui)
	ui.ignoredRows[0] = struct{}{}
	require.NotEmpty(t, ui.markedRows)
	ui.keyPressed(tcell.NewEventKey(tcell.KeyTab, 0, 0))
	assert.Empty(t, ui.markedRows, "Tab must reset the index-keyed marks")
	assert.Empty(t, ui.ignoredRows, "Tab must reset the index-keyed ignores")

	// clearBaseline (compare → plain) resets marks.
	ui = newDiffUI(t)
	setBaseline(ui)
	markFirstPresentRow(ui)
	require.NotEmpty(t, ui.markedRows)
	ui.clearBaseline()
	assert.Empty(t, ui.markedRows, "clearBaseline must reset the marks")

	// SetBaseline (plain → compare) resets a mark made in the plain view.
	ui = newDiffUI(t)
	ui.showDir() // render the plain table so there is a row to mark
	markFirstPresentRow(ui)
	require.NotEmpty(t, ui.markedRows)
	setBaseline(ui)
	assert.Empty(t, ui.markedRows, "SetBaseline must reset the marks")
}

// alignBig/alignBaseline build a tree whose sizes are large enough to stress the
// removed-row column mirroring: a present grown file, and a removed file whose
// parenthesized "(then)" size is wider than a bare present size.
func alignCurrent() *analyze.Dir {
	const mi = 1 << 20
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	// Flag ' ' matches what a real scan sets (getFlag); a normal file is never
	// the zero rune, which would render zero-width and skew the alignment check.
	top.AddFile(&analyze.File{Name: "big", Size: 300 * mi, Usage: 300 * mi, Flag: ' ', Parent: top})
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

func alignBaseline() *analyze.Dir {
	const mi = 1 << 20
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	top.AddFile(&analyze.File{Name: "big", Size: 200 * mi, Usage: 200 * mi, Flag: ' ', Parent: top})
	top.AddFile(&analyze.File{Name: "hugegone", Size: 250 * mi, Usage: 250 * mi, Flag: ' ', Parent: top})
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

// markerVisibleOffset returns the on-screen column where marker first appears in
// the row containing name, tag-stripped (tview.TaggedStringWidth of the prefix).
func markerVisibleOffset(t *testing.T, ui *UI, name, marker string) int {
	t.Helper()
	for r := 0; r < ui.table.GetRowCount(); r++ {
		c := ui.table.GetCell(r, 0)
		if c == nil || !strings.Contains(c.Text, name) {
			continue
		}
		i := strings.Index(c.Text, marker)
		require.GreaterOrEqual(t, i, 0, "marker %q not found in row %q", marker, c.Text)
		return tview.TaggedStringWidth(c.Text[:i])
	}
	t.Fatalf("row %q not found", name)
	return -1
}

// TestCompareRemovedRowColumnAlignment asserts a removed row's Δ column lines up
// under the present rows' across every combination of the optional columns —
// the invariant formatRemovedRow's width mirroring exists to hold. It stresses
// the count column (whose width hides an embedded color tag) and a removed size
// wide enough that its parentheses overflow a bare present size.
func TestCompareRemovedRowColumnAlignment(t *testing.T) {
	for _, tc := range []struct {
		name              string
		count, mtime, pct bool
	}{
		{"bars-only", false, false, false},
		{"with-count", true, false, false},
		{"with-mtime", false, true, false},
		{"with-percentage", false, false, true},
		{"all-columns", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ui := newDiffUI(t)
			ui.currentDir = alignCurrent()
			ui.topDir = ui.currentDir
			ui.topDirPath = "top"
			ui.showItemCount = tc.count
			ui.showMtime = tc.mtime
			ui.showBarPercentage = tc.pct
			ui.SetBaseline(analyze.BuildBaseline(alignBaseline(), "top", 0), snapAt(diffBaselineTime()))

			grew := markerVisibleOffset(t, ui, "big", "▲")
			removed := markerVisibleOffset(t, ui, "hugegone", "✗")
			assert.Equal(t, grew, removed,
				"removed row's Δ marker must align with the present rows' (columns: %+v)", tc)
		})
	}
}
