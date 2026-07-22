package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/pkg/path"
	"github.com/dundee/gdu/v5/report"
)

// rich reports whether the browser shows the folder-scoped columns (This
// folder / Δ vs ●), which need an async size fill, versus the simple
// When/Size/Root layout the -f chooser uses.
func (cfg *browserConfig) rich() bool { return cfg.fillTarget != "" }

// setBrowserHeaderCells writes the column headers into table row 0 and records
// the column indices. Row 0 is non-selectable so the cursor skips it.
func (ui *UI) setBrowserHeaderCells(st *browserState) {
	cfg := st.cfg
	var headers []string
	if cfg.rich() {
		// The Δ column reads against ●, so its header carries the same glyph the
		// rows do — including the --no-unicode fallback.
		headers = []string{"When", "This folder", "Δ vs " + ui.viewingGlyph(), "Root"}
		st.whenCol, st.sizeCol, st.deltaCol, st.rootCol = 0, 1, 2, 3
	} else {
		headers = []string{"When", "Size", "Root"}
		st.whenCol, st.sizeCol, st.deltaCol, st.rootCol = 0, 1, -1, 2
	}

	st.hostCol = -1
	if ui.browserHasForeignHost(st) {
		st.hostCol = len(headers)
		headers = append(headers, "Host")
	}
	for col, h := range headers {
		st.table.SetCell(0, col, tview.NewTableCell(h).SetSelectable(false).SetAttributes(tcell.AttrBold))
	}
}

// browserHasForeignHost reports whether any listed snapshot was taken on another
// machine, so the Host column earns its place.
func (ui *UI) browserHasForeignHost(st *browserState) bool {
	localHost := common.HostnameBestEffort()
	for i := range st.rows {
		if st.rows[i].kind == browserSnapRow || st.rows[i].kind == browserOtherRow {
			if common.HostIsForeign(st.rows[i].listing.Host, localHost) {
				return true
			}
		}
	}
	return false
}

// renderBrowserBody re-fills every data row (table rows 1..N) from the current
// cursor positions and resolved sizes. It overwrites cells in place rather than
// clearing the table, so the tview selection highlight is preserved. Cheap for
// the small lists a browser shows.
func (ui *UI) renderBrowserBody(st *browserState) {
	localHost := common.HostnameBestEffort()
	for i := range st.rows {
		row := i + 1
		r := &st.rows[i]
		if r.kind == browserSectionRow {
			ui.renderBrowserSectionRow(st, row)
			continue
		}
		st.table.SetCell(row, st.whenCol, tview.NewTableCell(ui.browserWhenCell(st, i)))
		st.table.SetCell(row, st.sizeCol, tview.NewTableCell(ui.browserSizeCell(st, i)))
		if st.deltaCol >= 0 {
			st.table.SetCell(row, st.deltaCol, tview.NewTableCell(ui.browserDeltaCell(st, i)))
		}
		st.table.SetCell(row, st.rootCol, tview.NewTableCell(ui.browserRootCell(st, i)))
		if st.hostCol >= 0 {
			host := ""
			if r.kind != browserLiveRow && common.HostIsForeign(r.listing.Host, localHost) {
				host = ui.pickerHostCell(r.listing.Host)
			}
			st.table.SetCell(row, st.hostCol, tview.NewTableCell(host))
		}
	}
}

// renderBrowserSectionRow writes the dim "other roots (view only)" divider,
// non-selectable so the cursor skips it, and clears the remaining columns.
func (ui *UI) renderBrowserSectionRow(st *browserState, row int) {
	st.table.SetCell(row, 0, tview.NewTableCell(ui.dim(browserOtherSection)).SetSelectable(false))
	for col := 1; col <= maxInt(st.rootCol, st.hostCol); col++ {
		st.table.SetCell(row, col, tview.NewTableCell("").SetSelectable(false))
	}
}

// browserOtherSection is the divider before the non-covering snapshots.
const browserOtherSection = "other roots (view only)"

// browserWhenCell renders a row's When cell: the two-cursor marker, then the
// timestamp (or "live") with a dim relative age.
func (ui *UI) browserWhenCell(st *browserState, i int) string {
	marker := ui.browserMarker(st, i)
	r := &st.rows[i]
	if r.kind == browserLiveRow {
		return marker + "live  " + ui.dim("scanned "+st.cfg.live.scannedAt.Local().Format(headerClockLayout))
	}
	when := parquet.FormatSnapshotTime(&r.listing.SnapshotInfo)
	age := ui.dim("(" + humanAge(time.Since(r.listing.ScanTs)) + " ago)")
	return marker + when + "  " + age
}

// browserMarker is a row's leading two-glyph cursor marker: the solid ●
// (Viewing) or hollow ◇ (Baseline) when a cursor rests here, plus a focus caret
// on the cursor the arrows currently drive; blank padding otherwise so the
// timestamps stay column-aligned. The glyphs match the header and tree view, so
// one shape means one role everywhere.
func (ui *UI) browserMarker(st *browserState, i int) string {
	switch i {
	case st.viewCur:
		return ui.viewingGlyph() + ui.browserCaret(st.focus == focusViewing) + " "
	case st.baseCur:
		return ui.baselineGlyph() + ui.browserCaret(st.focus == focusBaseline) + " "
	default:
		return "   "
	}
}

// browserCaret is the focus indicator on the cursor the arrows drive — a right
// caret, or a space. It survives --no-color (where the selection highlight is
// the only other focus signal) and --no-unicode.
func (ui *UI) browserCaret(focused bool) string {
	if !focused {
		return " "
	}
	if ui.useOldSizeBar {
		return ">"
	}
	return "▸"
}

// browserSizeCell renders a row's This-folder / Size cell from the fill state:
// the live row's own usage, a resolved snapshot size, or a placeholder / absent
// / unreadable marker while or after the fill runs.
func (ui *UI) browserSizeCell(st *browserState, i int) string {
	r := &st.rows[i]
	if r.kind == browserLiveRow {
		return ui.pickerSizeCell(st.cfg.live.size)
	}
	if !st.cfg.rich() {
		return ui.pickerSizeCell(r.listing.TotalDsize) // -f: the snapshot's total
	}
	if r.kind == browserOtherRow {
		return ui.dim(snapshotAbsentMarker) // a different root does not cover this folder
	}
	sz := st.sizes[r.listing.Key()]
	//nolint:exhaustive // Why: default renders the sizeFilling placeholder
	switch sz.state {
	case sizeResolved:
		return ui.pickerSizeCell(sz.bytes)
	case sizeAbsent:
		return ui.dim(snapshotAbsentMarker)
	case sizeError:
		return ui.browserErrorMarker()
	default:
		return ui.dim(snapshotSizePlaceholder)
	}
}

// browserDeltaCell renders a row's Δ-versus-● cell: how much bigger the ● view
// is than this row's folder. The cursor's own row and the live/other rows carry
// no Δ; a Δ waits on both ●'s size and the row's size resolving.
func (ui *UI) browserDeltaCell(st *browserState, i int) string {
	r := &st.rows[i]
	if i == st.viewCur || r.kind == browserLiveRow {
		return ui.dim(snapshotAbsentMarker)
	}
	if r.kind == browserOtherRow {
		return ui.dim(snapshotAbsentMarker)
	}
	sz := st.sizes[r.listing.Key()]
	//nolint:exhaustive // Why: default renders the sizeFilling placeholder
	switch sz.state {
	case sizeAbsent:
		return ui.dim(snapshotAbsentMarker)
	case sizeError:
		return ui.browserErrorMarker()
	case sizeResolved:
		viewSize, ok := ui.browserViewSize(st)
		if !ok {
			return ui.dim(snapshotSizePlaceholder) // ●'s own size is still resolving
		}
		return ui.pickerDelta(viewSize - sz.bytes)
	default:
		return ui.dim(snapshotSizePlaceholder)
	}
}

// browserViewSize returns the folder size at wherever ● currently sits, and
// whether it is known yet. The live row's size is always known; a snapshot's is
// known once the fill resolves it; an other-root row has no folder size.
func (ui *UI) browserViewSize(st *browserState) (int64, bool) {
	r := &st.rows[st.viewCur]
	//nolint:exhaustive // Why: default covers the section/other rows (no folder size)
	switch r.kind {
	case browserLiveRow:
		return st.cfg.live.size, true
	case browserSnapRow:
		if sz := st.sizes[r.listing.Key()]; sz.state == sizeResolved {
			return sz.bytes, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// browserRootCell renders a row's Root cell: the live row's dim "(this scan)",
// or the snapshot's scan root home-abbreviated in the device-table blue.
func (ui *UI) browserRootCell(st *browserState, i int) string {
	if st.rows[i].kind == browserLiveRow {
		return ui.dim("(this scan)")
	}
	return ui.pickerRootCell(st.rows[i].listing.ScanRoot)
}

// browserErrorMarker renders the unreadable-file "?" in red (plain otherwise).
func (ui *UI) browserErrorMarker() string {
	if ui.UseColors {
		return "[red::b]" + snapshotErrorMarker
	}
	return snapshotErrorMarker
}

// updateBrowserChrome refreshes the header lines and the dynamic scriptable
// hint from the current cursor positions.
func (ui *UI) updateBrowserChrome(st *browserState) {
	st.head.SetText(ui.browserHeaderText(st))
	st.hint.SetText(ui.browserHintText(st))
}

// browserHeaderText renders the ● Viewing / ◇ Baseline lines that mirror the
// tree view, reflecting the pending cursor positions so the state is always
// readable before Enter commits it.
func (ui *UI) browserHeaderText(st *browserState) string {
	line := fmt.Sprintf(" %s Viewing   %s", ui.viewingGlyph(), ui.browserViewingWhat(st))
	if st.cfg.baselineOnly {
		return line
	}
	return line + "\n" + fmt.Sprintf(" %s Baseline  %s", ui.baselineGlyph(), ui.browserBaselineDesc(st))
}

// browserViewingWhat names the tree ● points at: the live disk or a snapshot.
func (ui *UI) browserViewingWhat(st *browserState) string {
	r := &st.rows[st.viewCur]
	if r.kind == browserLiveRow {
		return "live — scanned " + st.cfg.live.scannedAt.Local().Format(headerClockLayout)
	}
	return fmt.Sprintf("snapshot %s · %s · read-only",
		r.listing.ScanTs.Local().Format(headerTimeLayout),
		path.ShortenPath(r.listing.ScanRoot, headerRootMaxLen))
}

// browserBaselineDesc renders the ◇ line's body: the applied baseline unchanged,
// or a pending "old → new" transition (with "none" on either side).
func (ui *UI) browserBaselineDesc(st *browserState) string {
	pendingTs := st.baselineTsAt(st.baseCur)
	if st.baseCur == st.initBaseCur {
		if st.baseCur < 0 {
			return "none"
		}
		return fmt.Sprintf("%s (%s ago)", pendingTs, ui.browserAgeAt(st, st.baseCur))
	}
	return fmt.Sprintf("%s → %s (pending)", st.baselineTsAt(st.initBaseCur), pendingTs)
}

// baselineTsAt formats the snapshot timestamp at a ◇-eligible row, or "none".
func (st *browserState) baselineTsAt(i int) string {
	if i < 0 || i >= len(st.rows) || st.rows[i].kind != browserSnapRow {
		return "none"
	}
	return parquet.FormatSnapshotTime(&st.rows[i].listing.SnapshotInfo)
}

// browserAgeAt renders the relative age of the snapshot at row i.
func (ui *UI) browserAgeAt(st *browserState, i int) string {
	if i < 0 || i >= len(st.rows) || st.rows[i].kind != browserSnapRow {
		return ""
	}
	return humanAge(time.Since(st.rows[i].listing.ScanTs))
}

// browserHintText renders the dynamic teaching line: the CLI equivalent of
// selecting the ● cursor's snapshot, or "" for the live row.
func (ui *UI) browserHintText(st *browserState) string {
	r := &st.rows[st.viewCur]
	if r.kind == browserLiveRow || st.cfg.hint == nil {
		return ""
	}
	return ui.dim(st.cfg.hint(&r.listing))
}

// startBrowserFill reads each covering snapshot's folder size in the background
// and re-renders as sizes resolve, dropping stale updates by the generation
// guard. It mirrors the picker's fill but feeds the two-cursor Δ column, which
// reads against wherever ● sits, so a resolved size can change many rows' Δ.
func (ui *UI) startBrowserFill(st *browserState) {
	ctx, cancel := context.WithCancel(context.Background())
	ui.snapshotSizeCancel = cancel
	gen := ui.snapshotPickerGen
	resolved := make(map[parquet.SnapshotKey]bool, len(st.cfg.covering))

	apply := func(update func()) {
		ui.app.QueueUpdateDraw(func() {
			if ui.snapshotPickerGen == gen && ui.browser == st {
				update()
				ui.renderBrowserBody(st)
			}
		})
	}

	ui.goPickerWork(func() {
		report.FolderSizesEach(ctx, ui.snapshotsDir, st.cfg.covering, st.cfg.fillTarget,
			func(key parquet.SnapshotKey, size int64) {
				apply(func() {
					resolved[key] = true
					st.sizes[key] = browserSize{state: sizeResolved, bytes: size}
				})
			},
			func(key parquet.SnapshotKey) {
				apply(func() {
					resolved[key] = true
					st.sizes[key] = browserSize{state: sizeError}
				})
			})
		apply(func() {
			for i := range st.cfg.covering {
				key := st.cfg.covering[i].Key()
				if !resolved[key] {
					st.sizes[key] = browserSize{state: sizeAbsent}
				}
			}
		})
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
