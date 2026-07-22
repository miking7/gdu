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
	assert.Contains(t, newfile, "✦", "the appended Δ column marks the new item")
	assert.Contains(t, newfile, "+100 B", "and renders the signed growth magnitude")

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

// upNavCurrent / upNavBaseline build a tree where the plain (size) order and the
// compare (Δ) order deliberately disagree: "aaa" is small but grew a lot, "bbb"
// is large but unchanged. So plain size-desc is [bbb, aaa] while Δ-desc is
// [aaa, bbb] — an index computed in one order and applied to the other selects
// the wrong row.
func upNavCurrent() *analyze.Dir {
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	aaa := &analyze.Dir{File: &analyze.File{Name: "aaa", Parent: top}, ItemCount: 1}
	aaa.AddFile(&analyze.File{Name: "af", Size: 100, Usage: 100, Flag: ' ', Parent: aaa})
	bbb := &analyze.Dir{File: &analyze.File{Name: "bbb", Parent: top}, ItemCount: 1}
	bbb.AddFile(&analyze.File{Name: "bf", Size: 900, Usage: 900, Flag: ' ', Parent: bbb})
	top.AddFile(aaa)
	top.AddFile(bbb)
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

func upNavBaseline() *analyze.Dir {
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	aaa := &analyze.Dir{File: &analyze.File{Name: "aaa", Parent: top}, ItemCount: 1}
	aaa.AddFile(&analyze.File{Name: "af", Size: 10, Usage: 10, Flag: ' ', Parent: aaa}) // aaa grew 10->100
	bbb := &analyze.Dir{File: &analyze.File{Name: "bbb", Parent: top}, ItemCount: 1}
	bbb.AddFile(&analyze.File{Name: "bf", Size: 900, Usage: 900, Flag: ' ', Parent: bbb}) // bbb unchanged
	top.AddFile(aaa)
	top.AddFile(bbb)
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

// descendInto navigates into the directory row named name (its own row, not a
// substring match), as a real Enter/right-arrow would.
func descendInto(t *testing.T, ui *UI, name string) {
	t.Helper()
	for r := 0; r < ui.table.GetRowCount(); r++ {
		c := ui.table.GetCell(r, 0)
		if c == nil {
			continue
		}
		if ref, ok := c.GetReference().(fs.Item); ok && ref.GetName() == name {
			ui.fileItemSelected(r, 0)
			return
		}
	}
	t.Fatalf("no navigable row named %q", name)
}

// selectedRef returns the fs.Item under the cursor.
func selectedRef(t *testing.T, ui *UI) fs.Item {
	t.Helper()
	row, col := ui.table.GetSelection()
	ref, ok := ui.table.GetCell(row, col).GetReference().(fs.Item)
	require.True(t, ok, "the selected row must carry an item")
	return ref
}

// TestCompareUpNavSelectsCameFromDir guards the wrong-highlight bug: after
// stepping up out of a child in the compare view, the cursor must land back on
// that child. The old code recomputed the row from the plain sort order and so
// selected a different row once the compare (Δ) order diverged from it.
func TestCompareUpNavSelectsCameFromDir(t *testing.T) {
	ui := newDiffUI(t)
	ui.currentDir = upNavCurrent()
	ui.topDir = ui.currentDir
	ui.topDirPath = "top"
	ui.SetBaseline(analyze.BuildBaseline(upNavBaseline(), "top", 0), snapAt(diffBaselineTime()))

	// aaa is the big grower, so Δ-desc sorts it to the top — a different row than
	// its position in the plain size order.
	descendInto(t, ui, "aaa")
	require.Equal(t, "aaa", ui.currentDir.GetName(), "descended into aaa")

	ui.handleLeft() // step back up to top

	require.Equal(t, "top", ui.currentDir.GetName(), "returned to top")
	assert.Equal(t, "aaa", selectedRef(t, ui).GetName(),
		"the cursor must land on the directory we came from, not a plain-sort index guess")
}

// TestPlainUpNavSelectsCameFromDir locks in that the by-reference reselect keeps
// the plain view's long-standing up-navigation behavior intact.
func TestPlainUpNavSelectsCameFromDir(t *testing.T) {
	ui := newDiffUI(t)
	ui.currentDir = upNavCurrent()
	ui.topDir = ui.currentDir
	ui.topDirPath = "top"
	ui.showDir()

	descendInto(t, ui, "aaa")
	require.Equal(t, "aaa", ui.currentDir.GetName())

	ui.handleLeft()

	require.Equal(t, "top", ui.currentDir.GetName())
	assert.Equal(t, "aaa", selectedRef(t, ui).GetName(),
		"plain up-navigation still lands on the directory we came from")
}

// teleportTree builds top → xxx → yyy (single-child chain) with a sibling under
// top, so with --collapse-path the plain view collapses "xxx/yyy" into one row
// and up-navigation from yyy jumps straight to top. The compare view renders the
// chain uncollapsed, so it must step yyy → xxx one real level.
func teleportTree() *analyze.Dir {
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	xxx := &analyze.Dir{File: &analyze.File{Name: "xxx", Parent: top}, ItemCount: 1}
	yyy := &analyze.Dir{File: &analyze.File{Name: "yyy", Parent: xxx}, ItemCount: 1}
	yyy.AddFile(&analyze.File{Name: "yf", Size: 100, Usage: 100, Flag: ' ', Parent: yyy})
	xxx.AddFile(yyy)
	top.AddFile(xxx)
	top.AddFile(&analyze.File{Name: "sibling", Size: 50, Usage: 50, Flag: ' ', Parent: top})
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

// TestCompareUpNavNoCollapseTeleport confirms the compare view renders the true
// tree and steps up one real level even with --collapse-path on, rather than
// teleporting over a chain the plain view would have collapsed.
func TestCompareUpNavNoCollapseTeleport(t *testing.T) {
	ui := newDiffUI(t)
	ui.collapsePath = true
	ui.currentDir = teleportTree()
	ui.topDir = ui.currentDir
	ui.topDirPath = "top"
	ui.SetBaseline(analyze.BuildBaseline(teleportTree(), "top", 0), snapAt(diffBaselineTime()))

	// The true tree: xxx renders as its own row, not collapsed into "xxx/yyy".
	joined := strings.Join(diffRowTexts(ui), "\n")
	assert.Contains(t, joined, "xxx")
	assert.NotContains(t, joined, "xxx/yyy", "compare must render the tree uncollapsed")

	descendInto(t, ui, "xxx")
	require.Equal(t, "xxx", ui.currentDir.GetName())
	descendInto(t, ui, "yyy")
	require.Equal(t, "yyy", ui.currentDir.GetName())

	ui.handleLeft() // up from yyy
	assert.Equal(t, "xxx", ui.currentDir.GetName(),
		"compare + collapse-path: up-nav is the plain parent (no teleport to top)")
}

// magnitudeRightCurrent / magnitudeRightBaseline grow two present files by
// clearly different magnitude widths ("+900 B" vs "+1.5 GiB") so a right-aligned
// Δ column lines their units up while a left-aligned one would not.
func magnitudeRightCurrent() *analyze.Dir {
	const gi = 1 << 30
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	top.AddFile(&analyze.File{Name: "small", Size: 1000, Usage: 1000, Flag: ' ', Parent: top})
	top.AddFile(&analyze.File{Name: "huge", Size: 3 * gi, Usage: 3 * gi, Flag: ' ', Parent: top})
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

func magnitudeRightBaseline() *analyze.Dir {
	const gi = 1 << 30
	top := &analyze.Dir{File: &analyze.File{Name: "top"}, ItemCount: 1}
	top.AddFile(&analyze.File{Name: "small", Size: 100, Usage: 100, Flag: ' ', Parent: top})              // +900 B
	top.AddFile(&analyze.File{Name: "huge", Size: 3 * gi / 2, Usage: 3 * gi / 2, Flag: ' ', Parent: top}) // +1.5 GiB
	top.UpdateStats(make(fs.HardLinkedItems))
	return top
}

// visibleEndOffset returns the on-screen column just past the last cell of needle
// within the row containing rowKey (tag-stripped).
func visibleEndOffset(t *testing.T, ui *UI, rowKey, needle string) int {
	t.Helper()
	for r := 0; r < ui.table.GetRowCount(); r++ {
		c := ui.table.GetCell(r, 0)
		if c == nil || !strings.Contains(c.Text, rowKey) {
			continue
		}
		i := strings.Index(c.Text, needle)
		require.GreaterOrEqual(t, i, 0, "%q not in row %q", needle, c.Text)
		return tview.TaggedStringWidth(c.Text[:i+len(needle)])
	}
	t.Fatalf("row %q not found", rowKey)
	return -1
}

// TestCompareDeltaMagnitudeRightAligned asserts the signed Δ magnitudes are
// right-aligned so their size units line up down the column, matching the size
// column and the §4.2 mock — the drift this fixes rendered them left-aligned.
func TestCompareDeltaMagnitudeRightAligned(t *testing.T) {
	ui := newDiffUI(t)
	ui.currentDir = magnitudeRightCurrent()
	ui.topDir = ui.currentDir
	ui.topDirPath = "top"
	ui.SetBaseline(analyze.BuildBaseline(magnitudeRightBaseline(), "top", 0), snapAt(diffBaselineTime()))

	// Two present rows whose magnitudes differ in width end at the same column iff
	// the magnitude is right-aligned.
	smallEnd := visibleEndOffset(t, ui, "small", "+900 B")
	hugeEnd := visibleEndOffset(t, ui, "huge", "+1.5 GiB")
	assert.Equal(t, hugeEnd, smallEnd,
		"Δ magnitudes must be right-aligned so their units line up (small ends at %d, huge at %d)",
		smallEnd, hugeEnd)
}

// TestReSortResetsIgnores confirms a re-sort clears the index-keyed ignore map
// as well as the mark map: both drift when the rows reorder, so both must reset
// (the ignore reset was missing before — only marks were cleared).
func TestReSortResetsIgnores(t *testing.T) {
	ui := newDiffUI(t)
	ui.showDir()
	ui.ignoredRows[1] = struct{}{}
	ui.markedRows[1] = struct{}{}
	require.NotEmpty(t, ui.ignoredRows)

	pressRune(ui, 'n') // sort by name

	assert.Empty(t, ui.markedRows, "a re-sort resets marks")
	assert.Empty(t, ui.ignoredRows, "a re-sort resets ignores too — they are index-keyed")
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
