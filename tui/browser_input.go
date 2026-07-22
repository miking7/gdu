package tui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/dundee/gdu/v5/report"
)

// browserKey handles every key while the browser is up (the top-level keyPressed
// routes to it because the browser owns the "snapshotpicker" page). Navigation
// drives the focused cursor with constraint-aware stepping; [ ] { } move the two
// cursors directly regardless of focus; Tab flips focus; Enter applies; Esc
// discards.
func (ui *UI) browserKey(event *tcell.EventKey) *tcell.EventKey {
	st := ui.browser
	if st == nil {
		return event
	}
	if ui.browserNamedKey(st, event.Key()) || ui.browserRuneKey(st, event.Rune()) {
		return nil
	}
	return event // unhandled: let tview's Table have it
}

// browserNamedKey handles the special (non-rune) keys, returning whether it
// consumed one. Arrows drive the focused cursor; Home/End and PgUp/PgDn/Ctrl-B/
// Ctrl-F jump and page it (consumed here so tview's Table paging can't move the
// selection highlight out from under viewCur/baseCur).
//
//nolint:exhaustive // Why: only the listed keys act; the rest return false to try runes
func (ui *UI) browserNamedKey(st *browserState, key tcell.Key) bool {
	switch key {
	case tcell.KeyEsc:
		ui.browserDiscard(st)
	case tcell.KeyEnter:
		ui.applyBrowser(st)
	case tcell.KeyTab:
		ui.browserFlipFocus(st)
	case tcell.KeyUp:
		ui.browserMoveFocused(st, -1)
	case tcell.KeyDown:
		ui.browserMoveFocused(st, +1)
	case tcell.KeyHome:
		ui.browserJump(st, true)
	case tcell.KeyEnd:
		ui.browserJump(st, false)
	case tcell.KeyPgUp, tcell.KeyCtrlB:
		ui.browserPage(st, -1)
	case tcell.KeyPgDn, tcell.KeyCtrlF:
		ui.browserPage(st, +1)
	default:
		return false
	}
	return true
}

// browserRuneKey handles the rune keys (vim navigation and the two-cursor
// [ ] { } stepping), returning whether it consumed one.
func (ui *UI) browserRuneKey(st *browserState, r rune) bool {
	switch r {
	case 'q':
		ui.browserDiscard(st)
	case 'k':
		ui.browserMoveFocused(st, -1)
	case 'j':
		ui.browserMoveFocused(st, +1)
	case 'g':
		ui.browserJump(st, true)
	case 'G':
		ui.browserJump(st, false)
	case '[':
		ui.browserMoveView(st, +1) // older
	case ']':
		ui.browserMoveView(st, -1) // newer
	case '{':
		ui.browserMoveBase(st, +1) // older
	case '}':
		ui.browserMoveBase(st, -1) // newer
	default:
		return false
	}
	return true
}

// browserMoveFocused steps whichever cursor the arrows currently drive.
func (ui *UI) browserMoveFocused(st *browserState, dir int) {
	if st.focus == focusBaseline {
		ui.browserMoveBase(st, dir)
		return
	}
	ui.browserMoveView(st, dir)
}

// browserMoveView steps the ● cursor (dir +1 older, -1 newer), re-rendering the
// Δ column, which reads against ●.
func (ui *UI) browserMoveView(st *browserState, dir int) {
	next := st.nextSelectable(st.viewCur, dir, false)
	if next == st.viewCur {
		return
	}
	st.viewCur = next
	ui.refreshBrowser(st)
}

// browserMoveBase steps the ◇ cursor within the covering snapshots. From "none"
// either direction engages the default (the snapshot just older than ●); walking
// ◇ off the newest end clears it again — the in-browser "no baseline" gesture,
// mirroring the tree view's } onto ●.
func (ui *UI) browserMoveBase(st *browserState, dir int) {
	if st.cfg.baselineOnly {
		return
	}
	if st.baseCur < 0 {
		st.baseCur = st.baselineDefault()
		ui.refreshBrowser(st)
		return
	}
	next := st.nextSelectable(st.baseCur, dir, true)
	if next == st.baseCur && dir < 0 {
		st.baseCur = -1 // walked past the newest allowed snapshot → clear
	} else {
		st.baseCur = next
	}
	ui.refreshBrowser(st)
}

// browserJump moves the focused cursor to its first (toNewest) or last selectable
// row — the g/G / Home/End gesture.
func (ui *UI) browserJump(st *browserState, toNewest bool) {
	forBase := st.focus == focusBaseline
	if forBase && st.baseCur < 0 {
		st.baseCur = st.baselineDefault()
	}
	target := st.firstSelectable(forBase)
	if !toNewest {
		for i := len(st.rows) - 1; i >= 0; i-- {
			if st.rowSelectable(i, forBase) {
				target = i
				break
			}
		}
	}
	if forBase {
		if st.baseCur >= 0 {
			st.baseCur = target
		}
	} else {
		st.viewCur = target
	}
	ui.refreshBrowser(st)
}

// browserPageRows is the page size used before the table has been laid out (its
// height is not known yet, e.g. in tests).
const browserPageRows = 10

// browserPageStep is one visible page: the table's inner height less a row of
// overlap, or the fallback before the table has a size.
func browserPageStep(table *tview.Table) int {
	//nolint:dogsled // Why: GetInnerRect returns x,y,w,h and only the height is needed
	_, _, _, height := table.GetInnerRect()
	if height <= 1 {
		return browserPageRows
	}
	return height - 1
}

// browserPage moves the focused cursor by one page (PgUp/PgDn, Ctrl-B/Ctrl-F).
// tview's Table maps those keys to its own paging, which would move only the
// selection highlight and leave viewCur/baseCur — the logical cursors Enter acts
// on — behind; consuming them here keeps highlight and cursor in step.
func (ui *UI) browserPage(st *browserState, dir int) {
	if st.focus == focusBaseline {
		ui.browserPageBase(st, dir, browserPageStep(st.table))
		return
	}
	target := st.viewCur
	for n := 0; n < browserPageStep(st.table); n++ {
		next := st.searchSelectable(target, dir, false)
		if next < 0 {
			break // clamped at the end
		}
		target = next
	}
	if target != st.viewCur {
		st.viewCur = target
		ui.refreshBrowser(st)
	}
}

// browserPageBase pages the ◇ cursor within the covering snapshots. From "none"
// it first engages the default, then walks up to step ◇-eligible rows, clamped
// at both ends — paging never clears (that stays the single-step } gesture).
func (ui *UI) browserPageBase(st *browserState, dir, step int) {
	if st.cfg.baselineOnly {
		return
	}
	engaged := false
	if st.baseCur < 0 {
		st.baseCur = st.baselineDefault()
		if st.baseCur < 0 {
			return // nowhere for ◇ to rest
		}
		engaged = true
	}
	target := st.baseCur
	for n := 0; n < step; n++ {
		next := st.searchSelectable(target, dir, true)
		if next < 0 {
			break
		}
		target = next
	}
	if target != st.baseCur || engaged {
		st.baseCur = target
		ui.refreshBrowser(st)
	}
}

// browserFlipFocus is Tab: swap which cursor the arrows drive. Flipping to ◇
// engages it at the default when it was off; if there is nowhere valid for ◇,
// focus stays on ●.
func (ui *UI) browserFlipFocus(st *browserState) {
	if st.cfg.baselineOnly {
		return
	}
	if st.focus == focusViewing {
		if st.baseCur < 0 {
			st.baseCur = st.baselineDefault()
		}
		if st.baseCur < 0 {
			return // no covering snapshot for ◇ to rest on
		}
		st.focus = focusBaseline
	} else {
		st.focus = focusViewing
	}
	ui.refreshBrowser(st)
}

// refreshBrowser re-renders the rows, moves the highlight to the focused cursor,
// and updates the header and hint after any cursor or focus change.
func (ui *UI) refreshBrowser(st *browserState) {
	ui.renderBrowserBody(st)
	ui.syncBrowserSelection(st)
	ui.updateBrowserChrome(st)
}

// syncBrowserSelection moves the tview selection highlight onto the focused
// cursor's row (table row = rows index + 1), so the highlight and the focus
// caret always agree.
func (ui *UI) syncBrowserSelection(st *browserState) {
	row := st.viewCur
	if st.focus == focusBaseline && st.baseCur >= 0 {
		row = st.baseCur
	}
	st.table.Select(row+1, 0)
}

// browserDiscard is Esc/q: throw away the pending cursor changes and close. The
// -f startup chooser has nothing to return to, so there Esc/q quits the app.
func (ui *UI) browserDiscard(st *browserState) {
	if st.cfg.escQuits {
		ui.finishQuit(false)
		if ui.done != nil {
			ui.done <- struct{}{}
		}
		return
	}
	refocus := tview.Primitive(ui.table)
	if st.cfg.refocus != nil {
		refocus = st.cfg.refocus
	}
	ui.closeSnapshotPicker()
	ui.app.SetFocus(refocus)
}

// applyBrowser is Enter: commit whatever the cursors changed — the View (●), the
// Baseline (◇), or both. A changed View lands first (its tree loads off the
// event loop); the pending baseline is then applied on top via the continuation,
// so it renders against the new View.
func (ui *UI) applyBrowser(st *browserState) {
	cfg := st.cfg
	viewChanged := st.viewCur != st.initViewCur
	baseChanged := st.baseCur != st.initBaseCur

	var pendingBase *report.SnapshotListing
	if st.baseCur >= 0 && st.rows[st.baseCur].kind == browserSnapRow {
		l := st.rows[st.baseCur].listing // copy; the appliers outlive the browser
		pendingBase = &l
	}
	applyBase := func() {
		if !baseChanged || cfg.baselineOnly {
			return
		}
		if pendingBase == nil {
			if cfg.clearBaseline != nil {
				cfg.clearBaseline()
			}
			return
		}
		if cfg.applyBaseline != nil {
			cfg.applyBaseline(pendingBase)
		}
	}

	viewRow := st.rows[st.viewCur]
	refocus := tview.Primitive(ui.table)
	if cfg.refocus != nil {
		refocus = cfg.refocus
	}

	switch {
	case !viewChanged && !baseChanged:
		ui.closeSnapshotPicker()
		ui.app.SetFocus(refocus)
	case !viewChanged:
		ui.closeSnapshotPicker()
		applyBase()
	case viewRow.kind == browserLiveRow:
		ui.closeSnapshotPicker()
		if cfg.goLive != nil {
			cfg.goLive(applyBase)
		}
	default:
		l := viewRow.listing // copy; the loader outlives the browser
		ui.closeSnapshotPicker()
		if cfg.openView != nil {
			cfg.openView(&l, applyBase)
		}
	}
}
