package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
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
)

// diffRow is one line in the diff view: either a present item (item != nil)
// carrying its delta versus the baseline, or a baseline entry removed since
// (removed != nil, item nil).
type diffRow struct {
	item    fs.Item
	removed *analyze.RemovedEntry
	delta   int64
	cat     analyze.DiffCategory
}

// SetBaseline enters diff mode against b (the snapshot identified by info) and
// re-renders the current directory sorted by growth. A nil b is a no-op; use
// clearBaseline to leave diff mode. Setting a baseline fills the header's
// Baseline slot and records the snapshot's full identity so the S picker can
// mark it as active.
func (ui *UI) SetBaseline(b *analyze.Baseline, info *parquet.SnapshotInfo) {
	if b == nil {
		return
	}
	ui.baseline = b
	ui.baselineTs = info.ScanTs
	ui.baselineKey = info.Key()
	ui.diffReverse = false
	ui.updateHeader()
	if ui.currentDir != nil {
		ui.showDir()
	}
}

// clearBaseline leaves diff mode and restores the normal view.
func (ui *UI) clearBaseline() {
	ui.baseline = nil
	ui.baselineTs = time.Time{}
	ui.baselineKey = parquet.SnapshotKey{}
	ui.updateHeader()
	if ui.currentDir != nil {
		ui.showDir()
	}
}

func (ui *UI) inDiffMode() bool { return ui.baseline != nil }

// setDiffSort flips the growth-sort direction: '>' puts the biggest growth on
// top (default), '<' the biggest shrink. A no-op outside diff mode.
func (ui *UI) setDiffSort(reverse bool) {
	if !ui.inDiffMode() {
		return
	}
	ui.diffReverse = reverse
	ui.showDir()
}

// showDiffDir renders the current directory in diff mode: a muted size column, a
// signed delta column with per-category markers, removed items shown inline, and
// a footer reconciling grown/shrunk/removed/net. It mirrors showDir's single
// table-build pass but sorts by growth rather than by an fs.SortBy.
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
		" ---").SetDynamicColors(true)

	ui.table.Clear()
	rowIndex := ui.setDiffParentRow()

	unlock := ui.currentDir.RLock()
	defer unlock()

	rows := ui.buildDiffRows()

	var maxAbs, grown, shrunk, removedTotal int64
	var removedCount int
	for i := range rows {
		if a := absInt64(rows[i].delta); a > maxAbs {
			maxAbs = a
		}
	}

	for i := range rows {
		r := &rows[i]
		switch {
		case r.removed != nil:
			removedTotal += r.removed.Size
			removedCount++
		case r.delta > 0:
			grown += r.delta
		case r.delta < 0:
			shrunk += -r.delta
		}

		var cell *tview.TableCell
		if r.removed != nil {
			cell = tview.NewTableCell(ui.formatRemovedRow(r.removed, maxAbs))
		} else {
			cell = tview.NewTableCell(ui.formatDiffRow(r.item, r.delta, r.cat, maxAbs))
			cell.SetReference(r.item)
		}
		cell.SetStyle(tcell.Style{}.Foreground(tcell.ColorDefault))
		ui.table.SetCell(rowIndex, 0, cell)
		rowIndex++
	}

	ui.setDiffFooter(grown, shrunk, removedTotal, removedCount)

	ui.table.Select(0, 0)
	ui.table.ScrollToBeginning()
	if !ui.filtering && !ui.typeFiltering {
		ui.app.SetFocus(ui.table)
	}
}

// setDiffParentRow adds the "/.." navigation row when not at the top dir and
// returns the next free row index.
func (ui *UI) setDiffParentRow() int {
	if ui.currentDirPath == ui.topDirPath {
		return 0
	}
	cell := tview.NewTableCell("                         [::b]/..")
	var parent fs.Item
	if ui.collapsePath {
		parent = findCollapsedParent(ui.currentDir)
	} else {
		parent = ui.currentDir.GetParent()
	}
	cell.SetReference(parent)
	cell.SetStyle(tcell.Style{}.Foreground(tcell.ColorDefault))
	ui.table.SetCell(0, 0, cell)
	return 1
}

// buildDiffRows gathers the visible present children (with their deltas) plus the
// baseline items removed under this directory, then sorts them by growth.
func (ui *UI) buildDiffRows() []diffRow {
	var rows []diffRow
	present := make(map[string]struct{})

	for item := range ui.currentDir.GetFiles(fs.SortByNone, fs.SortAsc) {
		present[item.GetPath()] = struct{}{}
		if !ui.diffRowVisible(item.GetName(), item.IsDir()) {
			continue
		}
		delta, cat := ui.baseline.Delta(item)
		rows = append(rows, diffRow{item: item, delta: delta, cat: cat})
	}

	for _, e := range ui.baseline.RemovedUnder(ui.currentDirPath, present) {
		entry := e
		if !ui.diffRowVisible(entry.Name, entry.IsDir) {
			continue
		}
		rows = append(rows, diffRow{removed: &entry, delta: -entry.Size})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if ui.diffReverse {
			return rows[i].delta < rows[j].delta
		}
		return rows[i].delta > rows[j].delta
	})
	return rows
}

// diffRowVisible applies the active name and type filters, so filtering works in
// diff mode exactly as in normal browsing.
func (ui *UI) diffRowVisible(name string, isDir bool) bool {
	if ui.filterValue != "" &&
		!strings.Contains(strings.ToLower(name), strings.ToLower(ui.filterValue)) {
		return false
	}
	return ui.matchesTypeFilter(name, isDir)
}

// formatDiffRow renders a present item: flag, muted current size, the signed
// delta with its marker, a bar scaled to |delta|, and the (dir-colored) name.
func (ui *UI) formatDiffRow(item fs.Item, delta int64, cat analyze.DiffCategory, maxAbs int64) string {
	row := string(item.GetFlag())
	row += ui.mutedSizeCol(item.GetUsage())
	row += " " + ui.deltaField(delta, cat)
	row += ui.diffBar(absInt64(delta), maxAbs)

	if item.IsDir() {
		if ui.UseColors {
			row += fmt.Sprintf("[%s::b]/", ui.resultRow.DirectoryColor)
		} else {
			row += defaultColorBold + "/"
		}
	}
	row += tview.Escape(item.GetName())
	return row
}

// formatRemovedRow renders a baseline item that no longer exists: its size shown
// as a parenthesized "(then)" value, a violet ✗ delta, and a struck-through name.
func (ui *UI) formatRemovedRow(e *analyze.RemovedEntry, maxAbs int64) string {
	then := "(" + ui.plainSize(e.Size) + ")"
	row := " "
	if ui.UseColors {
		row += fmt.Sprintf("[%s::b]%15s", diffSizeMuteColor, then)
	} else {
		row += fmt.Sprintf("%s%15s", defaultColorBold, then)
	}

	body := padCells(fmt.Sprintf("✗ −%s", ui.plainSize(e.Size)), diffDeltaWidth)
	if ui.UseColors {
		row += " [" + diffRemovedColor + "::b]" + body
	} else {
		row += " " + defaultColorBold + body
	}

	row += ui.diffBar(e.Size, maxAbs)

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

// mutedSizeCol renders the current size in a neutral color, so the warm/cool
// delta color owns the eye in diff mode. Width matches formatFileRow's column.
func (ui *UI) mutedSizeCol(bytes int64) string {
	s := fmt.Sprintf("%15s", ui.plainSize(bytes))
	if ui.UseColors {
		return "[" + diffSizeMuteColor + "::b]" + s
	}
	return defaultColorBold + s
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

// diffBar draws the usage bar scaled to |delta| relative to the largest change
// in view, reusing the normal block-glyph graph.
func (ui *UI) diffBar(mag, maxAbs int64) string {
	part := 0
	if maxAbs > 0 {
		part = int(mag * 100 / maxAbs)
	}
	if ui.useOldSizeBar {
		return " " + getUsageGraphOld(part) + " "
	}
	return getUsageGraph(part)
}

// setDiffFooter writes the tree-wide reconciliation for the current directory:
// how much grew, shrank and was removed, and the net change.
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
	dir := "desc"
	if ui.diffReverse {
		dir = "asc"
	}

	text := txtColor + " Growth: " +
		growTag + "+" + ui.plainSize(grown) + txtColor + " grown  " +
		shrinkTag + minusSign + ui.plainSize(shrunk) + txtColor + " shrunk"
	if removedCount > 0 {
		text += "  " + removedTag + minusSign + ui.plainSize(removedTotal) +
			txtColor + fmt.Sprintf(" removed (%d)", removedCount)
	}
	text += txtColor + "  net " + numColor + netSign + ui.plainSize(absInt64(net)) +
		txtColor + "  Sort: growth " + dir
	ui.setFooter(text)
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
