package tui

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
)

// The snapshot browser is the one window behind O and B: a single table that
// carries two cursors — the solid ● (Viewing) and the hollow ◇ (Baseline) —
// mirroring the tree view's roles. O opens it with ● focused, B with ◇ focused;
// Tab flips focus, [ ] step ● and { } step ◇ regardless of focus, Enter applies
// whatever changed, Esc discards. The same component, seeded differently, serves
// the launcher's S (row-scoped, no live row) and the -f startup chooser
// (file-seeded, ●-only, Esc quits).

// browserFocus names which cursor the arrow keys drive.
type browserFocus int

const (
	focusViewing browserFocus = iota
	focusBaseline
)

// browserRowKind classifies a browser row.
type browserRowKind int

const (
	browserLiveRow    browserRowKind = iota // the pinned live scan (● only)
	browserSnapRow                          // a covering snapshot (both cursors)
	browserSectionRow                       // the "other roots (view only)" divider
	browserOtherRow                         // a non-covering snapshot (● only)
)

// browserRow is one line in the browser table.
type browserRow struct {
	kind    browserRowKind
	listing report.SnapshotListing // valid for snap/other rows
}

// browserLive describes the pinned live row: the in-memory live tree the ●
// cursor can select to run the go-live flow.
type browserLive struct {
	scannedAt time.Time
	size      int64 // the shown folder's live disk usage, for the This-folder cell
}

// browserSizeState tracks a covering snapshot's async-filled folder size.
type browserSizeState int

const (
	sizeFilling  browserSizeState = iota // still being read
	sizeResolved                         // read; bytes is valid
	sizeAbsent                           // the folder had no row in that snapshot
	sizeError                            // the containing file could not be read
)

type browserSize struct {
	state browserSizeState
	bytes int64
}

// browserConfig parameterizes the shared browser for its four doors.
type browserConfig struct {
	scopeLabel string // the folder/root the browser is about, for the title

	covering   []report.SnapshotListing // both-cursor rows, newest first
	otherRoots []report.SnapshotListing // ●-only rows, newest first ("" for -f / launcher)
	live       *browserLive             // pinned live row, or nil (launcher, -f)

	// fillTarget is the folder whose per-snapshot size the async fill reads;
	// "" disables the fill and the This-folder column shows each snapshot's total.
	fillTarget string

	initialFocus browserFocus
	baselineOnly bool            // -f: no ◇ cursor, no baseline apply
	escQuits     bool            // -f startup: Esc/q quit the app
	refocus      tview.Primitive // focus target on Esc when the browser had no live tree

	// current applied state, for initial cursor placement and change detection.
	curViewLive bool
	curViewKey  parquet.SnapshotKey
	hasBaseline bool
	baselineKey parquet.SnapshotKey

	// hooks
	hint          func(l *report.SnapshotListing) string // per-● teaching line (CLI equivalent)
	openView      func(l *report.SnapshotListing, then func())
	goLive        func(then func()) // live-row Enter; nil when there is no live row
	applyBaseline func(l *report.SnapshotListing)
	clearBaseline func()
}

// browserState holds the live browser screen. The pointer stored on the UI
// doubles as the async-fill generation guard, like the launcher's.
type browserState struct {
	cfg   *browserConfig
	table *tview.Table
	head  *tview.TextView
	hint  *tview.TextView // the dynamic scriptable-equivalent line

	rows    []browserRow
	viewCur int // rows index of ● (always a selectable row)
	baseCur int // rows index of ◇, or -1 for none
	focus   browserFocus

	// the applied positions at open, so Enter applies only what changed.
	initViewCur int // -1 when the browser had no current view (launcher, -f)
	initBaseCur int // -1 when no baseline was applied at open

	sizes map[parquet.SnapshotKey]browserSize

	// localHost is the machine name, resolved once at open (a syscall). The row
	// renderer and the async fill reuse it instead of resolving it per row.
	localHost string

	// column indices (host is -1 when no snapshot is foreign)
	whenCol, sizeCol, deltaCol, rootCol, hostCol int
}

// showBrowser reads the archive (or takes a seeded list) off the event loop and
// opens the two-cursor browser. Must be called on the event loop.
func (ui *UI) showBrowser(cfg *browserConfig) {
	ui.pages.RemovePage("info") // never stack over an open info overlay
	ui.snapshotPickerGen++
	st := &browserState{cfg: cfg, sizes: map[parquet.SnapshotKey]browserSize{}}
	ui.browser = st

	st.rows = buildBrowserRows(cfg)
	ui.placeBrowserCursors(st)

	table := tview.NewTable().SetSelectable(true, false)
	table.SetBackgroundColor(tcell.ColorDefault)
	table.SetBorder(true).SetTitle(fmt.Sprintf(" Snapshots — %s ", cfg.scopeLabel))
	if ui.UseColors {
		table.SetSelectedStyle(tcell.Style{}.
			Foreground(ui.selectedTextColor).
			Background(ui.selectedBackgroundColor).Bold(true))
	}
	table.SetInputCapture(ui.browserKey)
	st.table = table

	head := tview.NewTextView()
	head.SetBackgroundColor(tcell.ColorDefault)
	st.head = head

	keys := tview.NewTextView() // no dynamic colors: the hint prints literal [ ] { }
	keys.SetBackgroundColor(tcell.ColorDefault)
	keys.SetText(cfg.keyHintText())

	hint := tview.NewTextView().SetDynamicColors(true)
	hint.SetBackgroundColor(tcell.ColorDefault)
	st.hint = hint

	ui.setBrowserHeaderCells(st)
	ui.renderBrowserBody(st)
	ui.syncBrowserSelection(st)
	ui.updateBrowserChrome(st)

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(head, cfg.headLineCount(), 0, false).
		AddItem(table, 0, 1, true).
		AddItem(keys, 1, 0, false).
		AddItem(hint, 1, 0, false)

	ui.pages.AddPage("snapshotpicker", flex, true, true)
	ui.app.SetFocus(table)

	if cfg.fillTarget != "" {
		ui.startBrowserFill(st)
	}
}

// headLineCount is the browser header's height: one line for ●, plus one for ◇
// unless the config has no baseline cursor at all.
func (cfg *browserConfig) headLineCount() int {
	if cfg.baselineOnly {
		return 1
	}
	return 2
}

// keyHintText is the static key legend. It advertises only the gestures that
// exist for this configuration.
func (cfg *browserConfig) keyHintText() string {
	cancel := "Esc cancel"
	if cfg.escQuits {
		cancel = "Esc quit"
	}
	if cfg.baselineOnly {
		return fmt.Sprintf(" [ ] move · Enter open · %s", cancel)
	}
	return fmt.Sprintf(" Tab move ●/◇ · [ ] ● · { } ◇ · Enter apply · %s", cancel)
}

// buildBrowserRows lays out the flat row list: the live row (if any), the
// covering snapshots, then an "other roots" divider and the non-covering
// snapshots (if any).
func buildBrowserRows(cfg *browserConfig) []browserRow {
	var rows []browserRow
	if cfg.live != nil {
		rows = append(rows, browserRow{kind: browserLiveRow})
	}
	for i := range cfg.covering {
		rows = append(rows, browserRow{kind: browserSnapRow, listing: cfg.covering[i]})
	}
	if len(cfg.otherRoots) > 0 {
		rows = append(rows, browserRow{kind: browserSectionRow})
		for i := range cfg.otherRoots {
			rows = append(rows, browserRow{kind: browserOtherRow, listing: cfg.otherRoots[i]})
		}
	}
	return rows
}

// placeBrowserCursors sets the initial ● and ◇ positions and the focus, and
// records them so Enter can tell what changed.
func (ui *UI) placeBrowserCursors(st *browserState) {
	cfg := st.cfg
	st.focus = cfg.initialFocus

	// ◇ off before ● is placed: firstSelectable(false) excludes ◇'s row, so its
	// zero value must be the "none" sentinel, not row 0.
	st.baseCur = -1
	st.initBaseCur = -1

	// ● starts on the current View's row (the live row, or the matching
	// snapshot); with no current view it starts on the first selectable row and
	// initViewCur stays -1 so any Enter applies it.
	st.viewCur = st.firstSelectable(false)
	st.initViewCur = -1
	switch {
	case cfg.curViewLive && cfg.live != nil:
		st.viewCur = 0
		st.initViewCur = 0
	case !cfg.curViewLive:
		if r := st.rowForKey(cfg.curViewKey); r >= 0 {
			st.viewCur = r
			st.initViewCur = r
		}
	}

	// ◇ starts on the applied baseline; with none it stays off unless the
	// baseline door (◇-focused) opened the browser, which pre-arms the snapshot
	// immediately older than ● — the one-keypress "compare vs previous" default.
	switch {
	case cfg.baselineOnly:
		st.focus = focusViewing
	case cfg.hasBaseline:
		if r := st.baselineRowForKey(cfg.baselineKey); r >= 0 {
			st.baseCur = r
			st.initBaseCur = r
		}
	case cfg.initialFocus == focusBaseline:
		st.baseCur = st.baselineDefault()
	}
	// If the baseline door found nowhere valid for ◇, fall back to driving ●.
	if st.focus == focusBaseline && st.baseCur < 0 {
		st.focus = focusViewing
	}
}

// rowForKey returns the rows index of the snapshot/other row with this identity,
// or -1.
func (st *browserState) rowForKey(key parquet.SnapshotKey) int {
	for i := range st.rows {
		if st.rows[i].kind == browserSnapRow || st.rows[i].kind == browserOtherRow {
			if st.rows[i].listing.Key() == key {
				return i
			}
		}
	}
	return -1
}

// baselineRowForKey returns the rows index where ◇ may rest for this identity: a
// covering-snapshot row that ● does not already hold, or -1. rowForKey also
// matches an "other roots" row (● may view those, ◇ may not — a baseline must
// cover the shown folder) and does not exclude ●'s own row (the two cursors
// never share a row), so those cases resolve to -1 here.
func (st *browserState) baselineRowForKey(key parquet.SnapshotKey) int {
	r := st.rowForKey(key)
	if r < 0 || st.rows[r].kind != browserSnapRow || r == st.viewCur {
		return -1
	}
	return r
}

// baselineDefault is the covering snapshot ◇ engages when turned on with none
// set: the one immediately older than ● — the one-keypress "compare vs previous"
// default. When ● already sits on the oldest covering snapshot it falls back to
// the next newer one (◇ resting newer than ● is allowed; the direction reads
// from the two header timestamps). It is -1 only when no covering snapshot is
// ◇-eligible at all — ● on the sole covering snapshot, or none exist — so every
// baseCur < 0 guard stays live rather than dead.
func (st *browserState) baselineDefault() int {
	if r := st.searchSelectable(st.viewCur, +1, true); r >= 0 {
		return r // the snapshot just older than ●
	}
	return st.searchSelectable(st.viewCur, -1, true) // fall back to the next newer, else -1
}

// rowSelectable reports whether cursor forBase may rest on row i: ● on the live,
// covering and other rows; ◇ on covering rows only. Neither cursor may share a
// row with the other, so the two glyphs never collide.
func (st *browserState) rowSelectable(i int, forBase bool) bool {
	if i < 0 || i >= len(st.rows) {
		return false
	}
	if forBase {
		return st.rows[i].kind == browserSnapRow && i != st.viewCur
	}
	k := st.rows[i].kind
	return (k == browserLiveRow || k == browserSnapRow || k == browserOtherRow) && i != st.baseCur
}

// firstSelectable returns the first row the given cursor may rest on, or 0.
func (st *browserState) firstSelectable(forBase bool) int {
	for i := range st.rows {
		if st.rowSelectable(i, forBase) {
			return i
		}
	}
	return 0
}

// nextSelectable returns the next row in direction dir (from, exclusive) the
// cursor may rest on, or from itself when there is none (a clamp).
func (st *browserState) nextSelectable(from, dir int, forBase bool) int {
	if i := st.searchSelectable(from, dir, forBase); i >= 0 {
		return i
	}
	return from
}

// searchSelectable returns the next row in direction dir (from, exclusive) the
// cursor may rest on, or -1 when there is none. Unlike nextSelectable it does
// not clamp to from, so a caller producing a ◇ position can tell "nowhere valid"
// (leave the cursor off) apart from "stayed put".
func (st *browserState) searchSelectable(from, dir int, forBase bool) int {
	for i := from + dir; i >= 0 && i < len(st.rows); i += dir {
		if st.rowSelectable(i, forBase) {
			return i
		}
	}
	return -1
}
