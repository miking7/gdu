package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/maruel/natural"
	"github.com/rivo/tview"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/build"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
)

// Diff-mode marker colors. Growth is warm (the problem you hunt), shrink cool,
// removal a distinct violet; the three form a color-vision-deficiency-safe
// triad, and the glyphs below keep every distinction legible under --no-color.
const (
	diffGrowColor     = "#ff6b35"
	diffShrinkColor   = "#34c5b8"
	diffRemovedColor  = "#b48ead"
	diffApproxColor   = "#f2c94c"
	diffSizeMuteColor = "#8a92a0"

	// diffDeltaWidth is the cell width reserved for the marker + signed delta. It
	// fits the widest single-unit binary magnitude ("▲ +1023.9 GiB" = 13 cells).
	diffDeltaWidth = 14

	minusSign = "−" // U+2212, the sign shown on negative (shrink/removed) deltas

	// Column widths formatFileRow draws, mirrored here so a removed row (which
	// has no live item to format) lines its Δ column up under the present rows'.
	pctColWidth   = 7  // formatUsagePercentage: " %5.1f%%"
	newBarWidth   = 12 // getUsageGraph
	oldBarWidth   = 14 // " " + getUsageGraphOld + " "
	countColWidth = 12 // "%11s "
	mtimeColWidth = 20 // "2006-01-02 15:04:05" + " "
	markColWidth  = 2  // "✓ " / "  "
	// diffSizeColWidth is the visible width of formatFileRow's size column: its
	// %15s field always embeds a 5-cell color tag ("[-::]"), so ten cells show.
	diffSizeColWidth = 10
)

// diffRow is one line in the compare view: either a present item (item != nil)
// carrying its signed delta versus the baseline, or a baseline entry removed
// since (removed != nil, item nil, delta = −then-size).
type diffRow struct {
	item    fs.Item
	removed *analyze.RemovedEntry
	delta   int64
}

// SetBaseline enters compare mode against b (the snapshot identified by info)
// and re-renders the current directory. A nil b is a no-op; use clearBaseline
// to leave. Setting a baseline fills the header's Baseline slot, records the
// snapshot's full identity so the picker can mark it as active, and shows the Δ
// column (Tab hides it). The compare sort is deliberately left untouched — it is
// session-scoped and survives baseline set/clear cycles.
func (ui *UI) SetBaseline(b *analyze.Baseline, info *parquet.SnapshotInfo) {
	if b == nil {
		return
	}
	ui.baseline = b
	ui.baselineTs = info.ScanTs
	ui.baselineKey = info.Key()
	ui.diffHidden = false
	ui.updateHeader()
	if ui.currentDir != nil {
		ui.showDir()
	}
}

// clearBaseline leaves compare mode and restores the normal view.
func (ui *UI) clearBaseline() {
	ui.baseline = nil
	ui.baselineTs = time.Time{}
	ui.baselineKey = parquet.SnapshotKey{}
	ui.updateHeader()
	if ui.currentDir != nil {
		ui.showDir()
	}
}

// inDiffMode reports whether a baseline is set (axis A). It drives the header's
// two lines and the Esc ladder — everything that must state a comparison
// exists — regardless of whether the Δ column is currently drawn.
func (ui *UI) inDiffMode() bool { return ui.baseline != nil }

// renderingDelta reports whether the Δ column should actually be drawn: a
// baseline is set and the Tab peek toggle has not hidden it. When false the tree
// renders exactly as the plain view even though the baseline persists, so Tab
// flips between the two renderings without touching the baseline.
func (ui *UI) renderingDelta() bool { return ui.inDiffMode() && !ui.diffHidden }

// showDiffDir renders the current directory as the normal table with a Δ column
// appended: the same flag/size/percentage/bar/count/mtime/mark anatomy as the
// plain view (present rows go through formatFileRow, which appends the Δ field),
// plus removed-since-baseline rows shown inline, and a footer reconciling
// grown/shrunk/removed/net. The bar stays usage-scaled — it is the same bar as
// the plain view; the Δ ranking comes from the sort and the signed numbers.
//
//nolint:funlen // Why: one cohesive table-build pass, matching showDir
func (ui *UI) showDiffDir() {
	ui.currentDirPath = ui.currentDir.GetPath()
	if ui.changeCwdFn != nil {
		if err := ui.changeCwdFn(ui.currentDirPath); err != nil {
			log.Printf("error setting cwd: %s", err.Error())
		}
	}
	ui.currentDirLabel.SetText("[::b]" + ui.dirLabelPrefix() + " --- " +
		tview.Escape(strings.TrimPrefix(ui.currentDirPath, build.RootPathPrefix)) +
		" ---" + ui.previewLabelSuffix()).SetDynamicColors(true)

	ui.table.Clear()
	rowIndex := ui.setParentRow()

	unlock := ui.currentDir.RLock()
	defer unlock()

	rows := ui.buildDiffRows()

	// Bar scale: the same rule as the plain view, over the present rows only —
	// removed items have no live size to contribute.
	var maxUsage, maxSize int64
	idx := rowIndex
	for i := range rows {
		if rows[i].removed == nil {
			if _, ignored := ui.ignoredRows[idx]; !ignored {
				ui.accumulateBarMax(rows[i].item, &maxUsage, &maxSize)
			}
		}
		idx++
	}

	var grown, shrunk, removedTotal int64
	var removedCount int
	idx = rowIndex
	for i := range rows {
		r := &rows[i]
		_, ignored := ui.ignoredRows[idx]
		switch {
		case r.removed != nil:
			removedTotal += r.removed.Size
			removedCount++
		case ignored:
			// ignored rows are excluded from the reconciliation, as from the totals
		case r.delta > 0:
			grown += r.delta
		case r.delta < 0:
			shrunk += -r.delta
		}

		var cell *tview.TableCell
		marked := false
		if r.removed != nil {
			cell = tview.NewTableCell(ui.formatRemovedRow(r.removed))
		} else {
			_, marked = ui.markedRows[idx]
			cell = tview.NewTableCell(ui.formatFileRow(r.item, maxUsage, maxSize, marked, ignored))
			cell.SetReference(r.item)
		}
		ui.applyRowStyle(cell, marked, ignored)
		ui.table.SetCell(idx, 0, cell)
		idx++
	}

	ui.setDiffFooter(grown, shrunk, removedTotal, removedCount)

	ui.table.Select(0, 0)
	ui.table.ScrollToBeginning()
	if !ui.filtering && !ui.typeFiltering {
		ui.app.SetFocus(ui.table)
	}
}

// buildDiffRows gathers the visible present children (with their deltas) plus
// the baseline items removed under this directory, then orders them by the
// compare view's own sort.
func (ui *UI) buildDiffRows() []diffRow {
	var rows []diffRow
	present := make(map[string]struct{})

	for item := range ui.currentDir.GetFiles(fs.SortByNone, fs.SortAsc) {
		present[item.GetPath()] = struct{}{}
		if !ui.diffRowVisible(item.GetName(), item.IsDir()) {
			continue
		}
		delta, _ := ui.baseline.Delta(item)
		rows = append(rows, diffRow{item: item, delta: delta})
	}

	for _, e := range ui.baseline.RemovedUnder(ui.currentDirPath, present) {
		entry := e
		if !ui.diffRowVisible(entry.Name, entry.IsDir) {
			continue
		}
		rows = append(rows, diffRow{removed: &entry, delta: -entry.Size})
	}

	ui.sortDiffRows(rows)
	return rows
}

// sortDiffRows orders the compare rows by the compare view's (sortBy, order).
// Removed rows carry a synthetic delta of −(their then-size), so a Δ sort
// interleaves them honestly; the other keys read a removed row's baseline size
// and name, and treat its count and mtime as unknown.
func (ui *UI) sortDiffRows(rows []diffRow) {
	by := ui.diffSortBy
	desc := ui.diffSortOrder == descOrder
	sort.SliceStable(rows, func(i, j int) bool {
		if desc {
			return ui.diffRowLess(&rows[j], &rows[i], by)
		}
		return ui.diffRowLess(&rows[i], &rows[j], by)
	})
}

// diffRowLess reports whether a sorts before b in ascending order for the key.
func (ui *UI) diffRowLess(a, b *diffRow, by string) bool {
	switch by {
	case nameSortKey:
		return natural.Less(diffRowName(a), diffRowName(b))
	case sizeSortKey:
		return ui.diffRowSize(a) < ui.diffRowSize(b)
	case itemCountSortKey:
		return diffRowCount(a) < diffRowCount(b)
	case mtimeSortKey:
		return diffRowMtime(a).Before(diffRowMtime(b))
	default: // deltaSortKey
		return a.delta < b.delta
	}
}

func diffRowName(r *diffRow) string {
	if r.removed != nil {
		return r.removed.Name
	}
	return r.item.GetName()
}

func (ui *UI) diffRowSize(r *diffRow) int64 {
	if r.removed != nil {
		return r.removed.Size
	}
	if ui.ShowApparentSize {
		return r.item.GetSize()
	}
	return r.item.GetUsage()
}

func diffRowCount(r *diffRow) int64 {
	if r.removed != nil {
		return 0
	}
	return r.item.GetItemCount()
}

func diffRowMtime(r *diffRow) time.Time {
	if r.removed != nil {
		return time.Time{}
	}
	return r.item.GetMtime()
}

// diffRowVisible applies the active name and type filters, so filtering works in
// compare mode exactly as in normal browsing.
func (ui *UI) diffRowVisible(name string, isDir bool) bool {
	if ui.filterValue != "" &&
		!strings.Contains(strings.ToLower(name), strings.ToLower(ui.filterValue)) {
		return false
	}
	return ui.matchesTypeFilter(name, isDir)
}

// deltaColumn is the compare view's appended Δ field: the marker glyph and
// signed growth of item versus the baseline, in a fixed-width cell so the names
// still line up. It is empty in every non-compare rendering, so the plain table
// (and a compare view with Δ toggled off) is byte-for-byte unchanged.
func (ui *UI) deltaColumn(item fs.Item) string {
	if !ui.renderingDelta() {
		return ""
	}
	delta, cat := ui.baseline.Delta(item)
	return " " + ui.deltaField(delta, cat) + " "
}

// formatRemovedRow renders a baseline item that no longer exists: its size shown
// as a parenthesized "(then)" value, an em dash where the live bar would be, a
// violet ✗ delta, and a struck-through name. It reproduces formatFileRow's
// column widths so its Δ column lines up under the present rows'.
func (ui *UI) formatRemovedRow(e *analyze.RemovedEntry) string {
	then := "(" + ui.plainSize(e.Size) + ")"
	row := " " // flag column: a removed item carries none
	if ui.UseColors {
		row += fmt.Sprintf("[%s::b]%*s", diffSizeMuteColor, diffSizeColWidth, then)
	} else {
		row += fmt.Sprintf("%s%*s", defaultColorBold, diffSizeColWidth, then)
	}

	row += ui.diffMiddleBlank()

	body := padCells(fmt.Sprintf("✗ −%s", ui.plainSize(e.Size)), diffDeltaWidth)
	if ui.UseColors {
		row += " [" + diffRemovedColor + "::b]" + body + " "
	} else {
		row += " " + defaultColorBold + body + " "
	}

	name := e.Name
	if e.IsDir {
		name = "/" + name
	}
	if ui.UseColors {
		row += fmt.Sprintf("[%s::bs]%s[-::-] [%s::-]removed",
			diffRemovedColor, tview.Escape(name), diffSizeMuteColor)
	} else {
		row += defaultColorBold + tview.Escape(name) + " (removed)"
	}
	return row
}

// diffMiddleBlank reproduces the width of the percentage + bar (+ optional
// count/mtime/mark) columns that formatFileRow draws, filled with blanks and a
// single em dash where the bar would be — a removed item has no live size to
// scale a bar from. It inherits the muted color of the size column before it.
func (ui *UI) diffMiddleBlank() string {
	var b strings.Builder
	if ui.showBarPercentage {
		b.WriteString(strings.Repeat(" ", pctColWidth))
	}
	barWidth := newBarWidth
	if ui.useOldSizeBar {
		barWidth = oldBarWidth
	}
	b.WriteString(padCells("  —", barWidth))
	if ui.showItemCount {
		b.WriteString(strings.Repeat(" ", countColWidth))
	}
	if ui.showMtime {
		b.WriteString(strings.Repeat(" ", mtimeColWidth))
	}
	if len(ui.markedRows) > 0 {
		b.WriteString(strings.Repeat(" ", markColWidth))
	}
	return b.String()
}

// deltaField renders the marker glyph and signed magnitude for one category,
// padded to a fixed cell width so the bars line up.
func (ui *UI) deltaField(delta int64, cat analyze.DiffCategory) string {
	glyph, color := diffGlyphColor(cat)
	var body string
	switch cat {
	case analyze.DiffUnchanged, analyze.DiffUncovered:
		body = string(glyph)
	case analyze.DiffGrew, analyze.DiffShrank, analyze.DiffNew, analyze.DiffApprox:
		sign := "+"
		if delta < 0 {
			sign = minusSign
		}
		body = fmt.Sprintf("%c %s%s", glyph, sign, ui.plainSize(absInt64(delta)))
	}
	body = padCells(body, diffDeltaWidth)
	if ui.UseColors {
		return "[" + color + "::b]" + body
	}
	return defaultColorBold + body
}

// setDiffFooter writes the tree-wide reconciliation for the current directory:
// how much grew, shrank and was removed, the net change, and the active sort.
func (ui *UI) setDiffFooter(grown, shrunk, removedTotal int64, removedCount int) {
	var numColor, txtColor string
	// growTag/shrinkTag/removedTag fall back to the plain number style so the
	// footer honors --no-color (no hex hues emitted when colors are off).
	growTag, shrinkTag, removedTag := blackOnWhiteBold, blackOnWhiteBold, blackOnWhiteBold
	if ui.UseColors {
		numColor = fmt.Sprintf("[%s:%s:b]", ui.footerNumberColor, ui.footerBackgroundColor)
		txtColor = fmt.Sprintf("[%s:%s:-]", ui.footerTextColor, ui.footerBackgroundColor)
		growTag = fmt.Sprintf("[%s:%s:b]", diffGrowColor, ui.footerBackgroundColor)
		shrinkTag = fmt.Sprintf("[%s:%s:b]", diffShrinkColor, ui.footerBackgroundColor)
		removedTag = fmt.Sprintf("[%s:%s:b]", diffRemovedColor, ui.footerBackgroundColor)
	} else {
		numColor = blackOnWhiteBold
		txtColor = blackOnWhite
	}

	net := grown - shrunk - removedTotal
	netSign := "+"
	if net < 0 {
		netSign = minusSign
	}

	text := txtColor + " Growth: " +
		growTag + "+" + ui.plainSize(grown) + txtColor + " grown · " +
		shrinkTag + minusSign + ui.plainSize(shrunk) + txtColor + " shrunk"
	if removedCount > 0 {
		text += txtColor + " · " + removedTag + minusSign + ui.plainSize(removedTotal) +
			txtColor + fmt.Sprintf(" removed (%d)", removedCount)
	}
	text += txtColor + " · net " + numColor + netSign + ui.plainSize(absInt64(net)) +
		txtColor + " · Sorting by: " + ui.compareSortLabel() + " " + ui.diffSortOrder
	ui.setFooter(text)
}

// compareSortLabel names the compare view's active sort key for the footer: "Δ"
// for the growth sort, otherwise the same key the plain footer prints.
func (ui *UI) compareSortLabel() string {
	if ui.diffSortBy == deltaSortKey {
		return "Δ"
	}
	return ui.diffSortBy
}

// plainSize formats a byte count with no color tags, honoring the SI/binary
// prefix choice.
func (ui *UI) plainSize(bytes int64) string {
	if ui.UseSIPrefix {
		return formatWithDecPrefix(bytes, "")
	}
	return formatWithBinPrefix(float64(bytes), "")
}

func diffGlyphColor(cat analyze.DiffCategory) (glyph rune, color string) {
	switch cat {
	case analyze.DiffGrew:
		return '▲', diffGrowColor
	case analyze.DiffShrank:
		return '▼', diffShrinkColor
	case analyze.DiffNew:
		return '✦', diffGrowColor
	case analyze.DiffApprox:
		return '~', diffApproxColor
	case analyze.DiffUncovered:
		return '?', diffSizeMuteColor
	case analyze.DiffUnchanged:
		return '·', diffSizeMuteColor
	}
	return '·', diffSizeMuteColor
}

// padCells right-pads s with spaces to width display cells. Every glyph used
// here is a single cell, so a rune count is the cell count.
func padCells(s string, width int) string {
	if n := utf8.RuneCountInString(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
