package tui

import (
	"bytes"
	"testing"
	"time"

	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
)

// browserTestUI builds a bare UI for driving the browser's cursor logic directly
// (no event loop, no async fill).
func browserTestUI(t *testing.T) *UI {
	t.Helper()
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	return CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
}

// coveringListingsForTest builds n covering snapshots of /root, newest first,
// each a distinct identity (distinct timestamps).
func coveringListingsForTest(n int) []report.SnapshotListing {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	out := make([]report.SnapshotListing, n)
	for i := 0; i < n; i++ {
		// newest first: index 0 is the most recent
		ts := base.Add(time.Duration(n-i) * time.Hour)
		out[i] = report.SnapshotListing{
			SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/root", ScanTs: ts, Host: "h", TotalDsize: int64(100 * (n - i))},
		}
	}
	return out
}

// newBrowserStateForTest mirrors showBrowser's setup minus the page and async
// fill, so the cursor-movement handlers (which re-render) can run.
func (ui *UI) newBrowserStateForTest(cfg *browserConfig) *browserState {
	st := &browserState{cfg: cfg, sizes: map[parquet.SnapshotKey]browserSize{}}
	ui.browser = st
	st.rows = buildBrowserRows(cfg)
	ui.placeBrowserCursors(st)
	st.table = tview.NewTable().SetSelectable(true, false)
	st.head = tview.NewTextView()
	st.hint = tview.NewTextView()
	ui.setBrowserHeaderCells(st)
	ui.renderBrowserBody(st)
	return st
}

// treeBrowserCfg is a tree-view-shaped config: a live row + covering snapshots,
// with the fill disabled so tests need no event loop.
func treeBrowserCfg(focus browserFocus, covering []report.SnapshotListing) *browserConfig {
	return &browserConfig{
		scopeLabel:   "/root",
		covering:     covering,
		live:         &browserLive{scannedAt: time.Now(), size: 500},
		initialFocus: focus,
		curViewLive:  true,
	}
}

// TestBrowserODoorStartsOnLiveNoBaseline: O opens ● on the live row and ◇ off.
func TestBrowserODoorStartsOnLiveNoBaseline(t *testing.T) {
	ui := browserTestUI(t)
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusViewing, coveringListingsForTest(3)))

	assert.Equal(t, 0, st.viewCur, "● starts on the pinned live row")
	assert.Equal(t, browserLiveRow, st.rows[st.viewCur].kind)
	assert.Equal(t, -1, st.baseCur, "the view door does not pre-arm a baseline")
	assert.Equal(t, focusViewing, st.focus)
}

// TestBrowserNoLiveRowStartsOnNewestSnapshot: the launcher / -f configs have no
// live row, so ● starts on the newest snapshot (row 0), not skipping it.
func TestBrowserNoLiveRowStartsOnNewestSnapshot(t *testing.T) {
	ui := browserTestUI(t)
	cfg := &browserConfig{
		scopeLabel:   "/root",
		covering:     coveringListingsForTest(3),
		initialFocus: focusViewing,
		// no live row, curViewLive false (nothing is being viewed yet)
	}
	st := ui.newBrowserStateForTest(cfg)

	assert.Equal(t, 0, st.viewCur, "● starts on the newest snapshot, not the second row")
	assert.Equal(t, browserSnapRow, st.rows[0].kind)
	assert.Equal(t, -1, st.initViewCur, "with no current view, any Enter applies the ● choice")
}

// TestBrowserBDoorZeroCoveringLeavesBaselineOff: the B door over a folder whose
// archive holds only non-covering snapshots finds nowhere for ◇, so it leaves
// the baseline off and drives ● instead. Enter then changes nothing rather than
// firing the clear path — B does not silently "do something".
func TestBrowserBDoorZeroCoveringLeavesBaselineOff(t *testing.T) {
	ui := browserTestUI(t)
	cfg := treeBrowserCfg(focusBaseline, nil) // no covering snapshots
	cfg.otherRoots = []report.SnapshotListing{{
		SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/other", ScanTs: time.Now(), Host: "h"},
	}}
	st := ui.newBrowserStateForTest(cfg)

	assert.Equal(t, -1, st.baseCur, "◇ finds no covering snapshot to rest on")
	assert.Equal(t, focusViewing, st.focus, "focus falls back to ● when ◇ has nowhere to go")
	assert.Equal(t, st.initViewCur, st.viewCur, "● is unmoved, so Enter is a no-op")
	assert.Equal(t, st.initBaseCur, st.baseCur, "◇ is unmoved (both -1), so Enter is a no-op")
}

// TestBrowserViewingOnlySnapshotTabKeepsBaselineOff: with ● opened on the sole
// covering snapshot, Tab finds no other covering snapshot for ◇ and refuses to
// engage — no snapshot-vs-itself self-baseline, no committed all-zero diff.
func TestBrowserViewingOnlySnapshotTabKeepsBaselineOff(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(1)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.curViewLive = false
	cfg.curViewKey = cov[0].Key() // ● opens on the snapshot, not the live row
	st := ui.newBrowserStateForTest(cfg)
	require.Equal(t, cov[0].Key(), st.rows[st.viewCur].listing.Key(), "● opens on the only snapshot")

	ui.browserFlipFocus(st)
	assert.Equal(t, -1, st.baseCur, "Tab does not pre-arm ◇ on ●'s own row")
	assert.Equal(t, focusViewing, st.focus, "focus stays on ● when ◇ has nowhere to go")
}

// TestBrowserBraceOnOnlySnapshotKeepsBaselineOff: pressing { with ● on the sole
// covering snapshot has no other snapshot to compare against, so ◇ stays off
// rather than pinning a self-baseline.
func TestBrowserBraceOnOnlySnapshotKeepsBaselineOff(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(1)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.curViewLive = false
	cfg.curViewKey = cov[0].Key()
	st := ui.newBrowserStateForTest(cfg)

	ui.browserMoveBase(st, +1) // {
	assert.Equal(t, -1, st.baseCur, "{ finds no other covering snapshot; ◇ stays off")
}

// TestBrowserBaselineDefaultFallsBackToNewer: with ● on the oldest covering
// snapshot, engaging ◇ falls back to the next *newer* snapshot (◇ resting newer
// than ● is allowed) rather than giving up and stranding the cursor.
func TestBrowserBaselineDefaultFallsBackToNewer(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(3) // rows: live(0), newest(1), mid(2), oldest(3)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.curViewLive = false
	cfg.curViewKey = cov[2].Key() // the oldest covering snapshot
	st := ui.newBrowserStateForTest(cfg)
	require.Equal(t, 3, st.viewCur, "● opens on the oldest snapshot (row 3)")

	ui.browserFlipFocus(st) // Tab engages ◇
	require.GreaterOrEqual(t, st.baseCur, 0, "◇ engages despite ● being oldest")
	assert.Equal(t, 2, st.baseCur, "◇ falls back to the next newer snapshot")
	assert.Equal(t, focusBaseline, st.focus)
}

// TestBrowserAppliedBaselineIgnoresOtherRoot: an applied baseline whose snapshot
// is now an "other roots" row (a root that no longer covers the shown folder) is
// not seated on ◇ — ◇ may only rest on a covering snapshot.
func TestBrowserAppliedBaselineIgnoresOtherRoot(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(1)
	cfg := treeBrowserCfg(focusViewing, cov)
	other := report.SnapshotListing{
		SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/other", ScanTs: time.Now(), Host: "h"},
	}
	cfg.otherRoots = []report.SnapshotListing{other}
	cfg.hasBaseline = true
	cfg.baselineKey = other.Key() // the applied baseline is a non-covering root
	st := ui.newBrowserStateForTest(cfg)

	assert.Equal(t, -1, st.baseCur, "◇ is not seated on a view-only other-roots row")
	assert.Equal(t, -1, st.initBaseCur)
}

// TestBrowserAppliedBaselineEqualsViewLeavesBaselineOff: opening a door while
// viewing the very snapshot that is the applied baseline (● and ◇ would collide)
// seats ● on the row and leaves ◇ off, rather than stacking both on one row.
func TestBrowserAppliedBaselineEqualsViewLeavesBaselineOff(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(1)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.curViewLive = false
	cfg.curViewKey = cov[0].Key()
	cfg.hasBaseline = true
	cfg.baselineKey = cov[0].Key() // viewing the baseline snapshot itself
	st := ui.newBrowserStateForTest(cfg)

	require.Equal(t, cov[0].Key(), st.rows[st.viewCur].listing.Key(), "● holds the row")
	assert.Equal(t, -1, st.baseCur, "◇ does not share ●'s row")
}

// TestBrowserBDoorPreArmsPreviousSnapshot: B pre-arms ◇ on the snapshot just
// older than ● (the one-keypress compare-vs-previous default) and focuses it.
func TestBrowserBDoorPreArmsPreviousSnapshot(t *testing.T) {
	ui := browserTestUI(t)
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusBaseline, coveringListingsForTest(3)))

	assert.Equal(t, 0, st.viewCur, "● still starts on the live row")
	assert.Equal(t, 1, st.baseCur, "◇ pre-arms on the newest covering snapshot (just older than live)")
	assert.Equal(t, browserSnapRow, st.rows[st.baseCur].kind)
	assert.Equal(t, focusBaseline, st.focus)
	assert.Equal(t, -1, st.initBaseCur, "the pre-armed baseline is pending, not the applied one")
}

// TestBrowserCursorsAreIndependent: Tab flips focus and { / [ move each cursor
// without disturbing the other; the two never share a row.
func TestBrowserCursorsAreIndependent(t *testing.T) {
	ui := browserTestUI(t)
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusViewing, coveringListingsForTest(3)))

	// [ steps ● older, onto the newest snapshot; ◇ is still off.
	ui.browserMoveView(st, +1)
	assert.Equal(t, 1, st.viewCur)
	assert.Equal(t, -1, st.baseCur)

	// { engages ◇; it may not land on ●'s row, so it takes the next older snapshot.
	ui.browserMoveBase(st, +1)
	assert.Equal(t, 2, st.baseCur, "◇ engages just older than ●, skipping ●'s row")
	assert.Equal(t, 1, st.viewCur, "moving ◇ leaves ● put")

	// Tab flips focus to the cursor the arrows drive.
	ui.browserFlipFocus(st)
	assert.Equal(t, focusBaseline, st.focus)
	ui.browserFlipFocus(st)
	assert.Equal(t, focusViewing, st.focus)
}

// TestBrowserBaselineNeverRestsOnLiveOrOther: ◇ is confined to covering
// snapshots — never the live row nor the other-roots section.
func TestBrowserBaselineNeverRestsOnLiveOrOther(t *testing.T) {
	ui := browserTestUI(t)
	cfg := treeBrowserCfg(focusBaseline, coveringListingsForTest(2))
	cfg.otherRoots = []report.SnapshotListing{{
		SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/other", ScanTs: time.Now(), Host: "h"},
	}}
	st := ui.newBrowserStateForTest(cfg)

	// Walk ◇ toward the newest end: it clears rather than landing on the live row.
	for i := 0; i < 5; i++ {
		ui.browserMoveBase(st, -1)
	}
	assert.Equal(t, -1, st.baseCur, "◇ walked off the newest end and cleared, never onto live")

	// Re-engage and walk toward the oldest end: it stops at the last covering
	// snapshot, never entering the other-roots section.
	ui.browserMoveBase(st, +1)
	for i := 0; i < 5; i++ {
		ui.browserMoveBase(st, +1)
	}
	require.GreaterOrEqual(t, st.baseCur, 0)
	assert.Equal(t, browserSnapRow, st.rows[st.baseCur].kind, "◇ never leaves the covering snapshots")
}

// TestBrowserViewMayVisitOtherRoots: ● may rest on an other-roots row (view
// only), unlike ◇.
func TestBrowserViewMayVisitOtherRoots(t *testing.T) {
	ui := browserTestUI(t)
	cfg := treeBrowserCfg(focusViewing, coveringListingsForTest(1))
	cfg.otherRoots = []report.SnapshotListing{{
		SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/other", ScanTs: time.Now(), Host: "h"},
	}}
	st := ui.newBrowserStateForTest(cfg)

	// live(0) → snap(1) → section(2, skipped) → other(3)
	ui.browserMoveView(st, +1)
	ui.browserMoveView(st, +1)
	assert.Equal(t, browserOtherRow, st.rows[st.viewCur].kind, "● reaches the other-roots row, skipping the divider")
}

// TestBrowserPageStepsViewCursorInSync: PgUp/PgDn page the ● cursor and keep the
// tview selection highlight on it — the desync that let Enter act on a row other
// than the highlighted one cannot happen.
func TestBrowserPageStepsViewCursorInSync(t *testing.T) {
	ui := browserTestUI(t)
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusViewing, coveringListingsForTest(20)))
	require.Equal(t, 0, st.viewCur)

	ui.browserPage(st, +1) // PgDn
	assert.Greater(t, st.viewCur, 0, "PgDn advances ● by a page")
	selRow, _ := st.table.GetSelection()
	assert.Equal(t, st.viewCur+1, selRow, "the highlight stays on ●'s row (no desync)")

	top := st.viewCur
	ui.browserPage(st, -1) // PgUp
	assert.Less(t, st.viewCur, top, "PgUp moves ● back up")
	selRow, _ = st.table.GetSelection()
	assert.Equal(t, st.viewCur+1, selRow, "the highlight follows ●")
}

// TestBrowserPageStepsBaselineCursor: with ◇ focused, PgDn pages the ◇ cursor
// within the covering snapshots (never onto the live row or into other-roots),
// and the highlight follows it.
func TestBrowserPageStepsBaselineCursor(t *testing.T) {
	ui := browserTestUI(t)
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusBaseline, coveringListingsForTest(20)))
	require.GreaterOrEqual(t, st.baseCur, 0, "the B door engaged ◇")
	start := st.baseCur

	ui.browserPage(st, +1) // PgDn pages ◇ older
	assert.Greater(t, st.baseCur, start, "PgDn pages ◇ toward older")
	assert.Equal(t, browserSnapRow, st.rows[st.baseCur].kind, "◇ stays on covering snapshots")
	selRow, _ := st.table.GetSelection()
	assert.Equal(t, st.baseCur+1, selRow, "the highlight follows ◇")
}

// TestHeaderRoleFramesAlign pins the exact role-line frames the tree-view header
// and the browser share: the glyph, the role word, and the hand-tuned padding
// that lands both bodies in the same column. If either frame's spacing drifts,
// the two screens' headers stop lining up — this catches it.
func TestHeaderRoleFramesAlign(t *testing.T) {
	ui := browserTestUI(t)
	assert.Equal(t, " ● Viewing   BODY", ui.viewingFrame("BODY"))
	assert.Equal(t, " ◇ Baseline  BODY", ui.baselineFrame("BODY"))
}

// browserHeaders reads the browser table's column-header row.
func browserHeaders(table *tview.Table) []string {
	var hs []string
	for c := 0; c < table.GetColumnCount(); c++ {
		if cell := table.GetCell(0, c); cell != nil {
			hs = append(hs, cell.Text)
		}
	}
	return hs
}

// TestBrowserRootAndHostColumns: a foreign-host snapshot reveals the Host column
// and the Root column shows the snapshot's scan root.
func TestBrowserRootAndHostColumns(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(1) // host "h", foreign to any real machine
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusViewing, cov))

	headers := browserHeaders(st.table)
	assert.Contains(t, headers, "Root", "the browser has a Root column")
	assert.Contains(t, headers, "Host", "a foreign snapshot reveals the Host column")
	// The snapshot is table row 2 (row 1 is the live row).
	assert.Contains(t, st.table.GetCell(2, st.rootCol).Text, "/root")
}

// TestBrowserHidesLocalHost: a same-machine snapshot leaves the Host column off.
func TestBrowserHidesLocalHost(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(1)
	cov[0].Host = common.HostnameBestEffort()
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusViewing, cov))

	assert.NotContains(t, browserHeaders(st.table), "Host")
}

// TestBrowserMarkerGlyphs pins the two-cursor markers: ● Viewing and ◇ Baseline,
// the focus caret on the driven cursor, and the ASCII fallbacks under
// --no-unicode. ● must never render on the baseline's row.
func TestBrowserMarkerGlyphs(t *testing.T) {
	ui := browserTestUI(t)
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusBaseline, coveringListingsForTest(2)))

	assert.Contains(t, ui.browserMarker(st, st.viewCur), "●", "● marks the Viewing cursor")
	assert.Contains(t, ui.browserMarker(st, st.baseCur), "◇", "◇ marks the Baseline cursor")
	assert.NotContains(t, ui.browserMarker(st, st.baseCur), "●", "● never appears on the baseline row")
	assert.Contains(t, ui.browserMarker(st, st.baseCur), "▸", "the focused cursor carries the caret")
	assert.NotContains(t, ui.browserMarker(st, st.viewCur), "▸", "the unfocused cursor has no caret")

	ui.UseOldSizeBar() // --no-unicode
	assert.Contains(t, ui.browserMarker(st, st.viewCur), "*", "● falls back to *")
	assert.Contains(t, ui.browserMarker(st, st.baseCur), "o", "◇ falls back to o")
}

// TestBrowserPreSelectsActiveBaseline: opening with a baseline already set lands
// ◇ on it, as the applied (not pending) position.
func TestBrowserPreSelectsActiveBaseline(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(3)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.hasBaseline = true
	cfg.baselineKey = cov[2].Key() // the oldest covering snapshot
	st := ui.newBrowserStateForTest(cfg)

	require.GreaterOrEqual(t, st.baseCur, 0)
	assert.Equal(t, cov[2].Key(), st.rows[st.baseCur].listing.Key(), "◇ lands on the active baseline")
	assert.Equal(t, st.baseCur, st.initBaseCur, "the applied baseline is not a pending change")
}

// browserTestUIColors builds a bare colors-on UI so the red unreadable marker is
// exercised (browserTestUI runs with colors off).
func browserTestUIColors(t *testing.T) *UI {
	t.Helper()
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	return CreateUI(app, sim, &bytes.Buffer{}, true, false, false, false)
}

// TestBrowserAbsentAndErrorCells: a covering snapshot missing the folder renders
// the absent "—" in both the size and Δ cells; one whose file is unreadable
// renders the red "?" in both — a read error is never mistaken for absence.
func TestBrowserAbsentAndErrorCells(t *testing.T) {
	ui := browserTestUIColors(t)
	cov := coveringListingsForTest(2)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.fillTarget = "/root" // the rich When/Size/Δ/Root layout (the tree browser always fills)
	st := ui.newBrowserStateForTest(cfg)
	require.Equal(t, browserLiveRow, st.rows[st.viewCur].kind, "● is on the live row, so the snapshots carry a Δ")

	st.sizes[cov[0].Key()] = browserSize{state: sizeAbsent} // folder absent in this snapshot
	st.sizes[cov[1].Key()] = browserSize{state: sizeError}  // snapshot file unreadable
	ui.renderBrowserBody(st)

	// cov[0] is table row 2 (rows index 1); cov[1] is table row 3.
	assert.Contains(t, st.table.GetCell(2, st.sizeCol).Text, snapshotAbsentMarker, "absent size renders —")
	assert.Contains(t, st.table.GetCell(2, st.deltaCol).Text, snapshotAbsentMarker, "absent Δ renders —")
	errCell := st.table.GetCell(3, st.sizeCol).Text
	assert.Contains(t, errCell, snapshotErrorMarker, "unreadable size renders ?")
	assert.Contains(t, errCell, "red", "the unreadable marker is red")
	assert.Contains(t, st.table.GetCell(3, st.deltaCol).Text, snapshotErrorMarker, "unreadable Δ renders ?")
}

// TestBrowserDeltaUndefinedWhenViewHasNoSize: when ● sits where it has no folder
// size — an other-roots row, or a snapshot whose folder is absent/unreadable —
// the other rows' Δ renders the undefined "—", never a "…" implying a fill that
// will never complete.
func TestBrowserDeltaUndefinedWhenViewHasNoSize(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(2)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.fillTarget = "/root" // the rich layout with a Δ column
	cfg.otherRoots = []report.SnapshotListing{{
		SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/other", ScanTs: time.Now(), Host: "h"},
	}}
	st := ui.newBrowserStateForTest(cfg)
	st.sizes[cov[0].Key()] = browserSize{state: sizeResolved, bytes: 100}
	st.sizes[cov[1].Key()] = browserSize{state: sizeResolved, bytes: 90}

	// Park ● on the other-roots row (rows: live0, snap1, snap2, section3, other4).
	st.viewCur = 4
	require.Equal(t, browserOtherRow, st.rows[st.viewCur].kind)
	ui.renderBrowserBody(st)
	deltaCell := st.table.GetCell(2, st.deltaCol).Text // cov[0]
	assert.Contains(t, deltaCell, snapshotAbsentMarker, "Δ is undefined when ● has no folder size")
	assert.NotContains(t, deltaCell, snapshotSizePlaceholder, "and never the loading placeholder")

	// Park ● on a covering snapshot whose own size is unreadable.
	st.viewCur = 1
	st.sizes[cov[0].Key()] = browserSize{state: sizeError}
	ui.renderBrowserBody(st)
	deltaCell = st.table.GetCell(3, st.deltaCol).Text // cov[1], vs ● (cov[0], unreadable)
	assert.Contains(t, deltaCell, snapshotAbsentMarker, "Δ is undefined when ●'s own size is unreadable")
	assert.NotContains(t, deltaCell, snapshotSizePlaceholder, "and never the loading placeholder")
}

// TestBrowserCloseBumpsFillGeneration: closing the browser bumps the fill
// generation (so any queued async size update is dropped) and clears the browser
// pointer.
func TestBrowserCloseBumpsFillGeneration(t *testing.T) {
	ui := browserTestUI(t)
	st := ui.newBrowserStateForTest(treeBrowserCfg(focusViewing, coveringListingsForTest(1)))
	require.Same(t, st, ui.browser)

	gen := ui.snapshotPickerGen
	ui.closeSnapshotPicker()
	assert.NotEqual(t, gen, ui.snapshotPickerGen, "closing invalidates the fill generation")
	assert.Nil(t, ui.browser, "closing clears the browser pointer")
}

// TestBrowserFilledRowRepaintsOnlyThatRow: resolving a non-● snapshot repaints
// just its own cells; a second snapshot whose size also changed but was not the
// subject of the call keeps its prior cell — the fill is O(N), not a full-table
// rebuild per emit.
func TestBrowserFilledRowRepaintsOnlyThatRow(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(2)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.fillTarget = "/root"
	st := ui.newBrowserStateForTest(cfg) // ● on live; both snapshots start as "…"
	require.Equal(t, browserLiveRow, st.rows[st.viewCur].kind)
	require.Contains(t, st.table.GetCell(2, st.sizeCol).Text, snapshotSizePlaceholder)
	require.Contains(t, st.table.GetCell(3, st.sizeCol).Text, snapshotSizePlaceholder)

	// Resolve both sizes in state, but only tell the renderer about cov[0].
	st.sizes[cov[0].Key()] = browserSize{state: sizeResolved, bytes: 100}
	st.sizes[cov[1].Key()] = browserSize{state: sizeResolved, bytes: 90}
	ui.renderBrowserFilledRow(st, cov[0].Key())

	assert.Contains(t, st.table.GetCell(2, st.sizeCol).Text, "100", "cov[0]'s row was repainted")
	assert.Contains(t, st.table.GetCell(3, st.sizeCol).Text, snapshotSizePlaceholder,
		"cov[1]'s row was left as-is (not a full-table rebuild)")
}

// TestBrowserFilledRowRepaintsAllWhenViewRowResolves: when ● sits on a snapshot
// and that snapshot's size resolves, every row's Δ reads against it, so the whole
// body repaints and the other rows' Δ cells update too.
func TestBrowserFilledRowRepaintsAllWhenViewRowResolves(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(2)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.fillTarget = "/root"
	cfg.curViewLive = false
	cfg.curViewKey = cov[0].Key() // ● on the newest snapshot
	st := ui.newBrowserStateForTest(cfg)
	require.Equal(t, cov[0].Key(), st.rows[st.viewCur].listing.Key())

	// The other snapshot's size is known; ●'s is not yet, so its Δ waits.
	st.sizes[cov[1].Key()] = browserSize{state: sizeResolved, bytes: 90}
	ui.renderBrowserFilledRow(st, cov[1].Key()) // non-● resolve: single row
	assert.Contains(t, st.table.GetCell(3, st.deltaCol).Text, snapshotSizePlaceholder,
		"cov[1]'s Δ waits on ●'s own size")

	// Now ●'s own size resolves: the whole body repaints and cov[1]'s Δ appears.
	st.sizes[cov[0].Key()] = browserSize{state: sizeResolved, bytes: 100}
	ui.renderBrowserFilledRow(st, cov[0].Key())
	assert.Contains(t, st.table.GetCell(3, st.deltaCol).Text, "10", "Δ = ●(100) − cov[1](90) now renders")
}

// TestBrowserApplyOtherRootDropsBaseline: applying a view on an other-roots row
// while a baseline is set clears the baseline (a baseline must cover the viewed
// folder) and never applies it — no inert all-uncovered diff.
func TestBrowserApplyOtherRootDropsBaseline(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(1)
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.fillTarget = "/root"
	other := report.SnapshotListing{
		SnapshotInfo: parquet.SnapshotInfo{ScanRoot: "/other", ScanTs: time.Now(), Host: "h"},
	}
	cfg.otherRoots = []report.SnapshotListing{other}
	cfg.hasBaseline = true
	cfg.baselineKey = cov[0].Key()
	var applied, cleared int
	var openedRoot string
	cfg.applyBaseline = func(_ *report.SnapshotListing) { applied++ }
	cfg.clearBaseline = func() { cleared++ }
	cfg.openView = func(l *report.SnapshotListing, then func()) {
		openedRoot = l.ScanRoot
		if then != nil {
			then() // the browser runs this after the view loads; drive it synchronously
		}
	}
	st := ui.newBrowserStateForTest(cfg)
	require.GreaterOrEqual(t, st.baseCur, 0, "the applied baseline seated on ◇")

	// Move ● onto the other-roots row (rows: live0, snap1, section2, other3).
	st.viewCur = 3
	require.Equal(t, browserOtherRow, st.rows[st.viewCur].kind)

	ui.applyBrowser(st)
	assert.Equal(t, "/other", openedRoot, "the other-root snapshot opens as the view")
	assert.Equal(t, 0, applied, "the baseline is never applied against a non-covering view")
	assert.Equal(t, 1, cleared, "the incompatible baseline is cleared")
}

// TestBrowserGoLivePassesContinuationOnlyWhenBaseChanged: Enter on the live row
// hands go-live the baseline continuation only when a ◇ change is pending, so the
// spot-rescan path can warn before dropping one.
func TestBrowserGoLivePassesContinuationOnlyWhenBaseChanged(t *testing.T) {
	ui := browserTestUI(t)
	cov := coveringListingsForTest(2)

	// Case 1: ● moved to live, no ◇ change — go-live gets no continuation.
	cfg := treeBrowserCfg(focusViewing, cov)
	cfg.curViewLive = false
	cfg.curViewKey = cov[0].Key() // ● opens on a snapshot
	var gotThen1, called1 bool
	cfg.goLive = func(then func()) { gotThen1 = then != nil; called1 = true }
	st := ui.newBrowserStateForTest(cfg)
	ui.browserMoveView(st, -1) // ● → the live row (row 0)
	require.Equal(t, browserLiveRow, st.rows[st.viewCur].kind)
	ui.applyBrowser(st)
	require.True(t, called1, "Enter on the live row runs go-live")
	assert.False(t, gotThen1, "no ◇ change → go-live gets no continuation")

	// Case 2: a ◇ change is pending — go-live gets the continuation.
	cfg2 := treeBrowserCfg(focusViewing, cov)
	cfg2.curViewLive = false
	cfg2.curViewKey = cov[0].Key()
	var gotThen2 bool
	cfg2.goLive = func(then func()) { gotThen2 = then != nil }
	cfg2.applyBaseline = func(_ *report.SnapshotListing) {}
	st2 := ui.newBrowserStateForTest(cfg2)
	ui.browserMoveBase(st2, +1) // engage ◇ (pending change)
	require.GreaterOrEqual(t, st2.baseCur, 0)
	ui.browserMoveView(st2, -1) // ● → the live row
	require.Equal(t, browserLiveRow, st2.rows[st2.viewCur].kind)
	ui.applyBrowser(st2)
	assert.True(t, gotThen2, "a pending ◇ change → go-live gets the continuation")
}

// TestBrowserFillResolvesFolderSizes drives the O door end-to-end through the
// event loop: the covering snapshot's folder size fills in and its Δ vs the
// live ● is computed.
func TestBrowserFillResolvesFolderSizes(t *testing.T) {
	dir := t.TempDir()
	writeTinySnapshot(t, dir, "/root") // /root holds f=100
	ui := newPickerUI(t, dir)          // live /root is 150

	pressKey(ui, 'O')
	settle(t, ui)
	require.NotNil(t, ui.browser, "O opens the browser")
	st := ui.browser

	// Row 1 is the live row; row 2 is the covering snapshot.
	assert.Contains(t, st.table.GetCell(2, st.sizeCol).Text, "100", "the snapshot folder size resolves")
	assert.Contains(t, st.table.GetCell(2, st.deltaCol).Text, "50", "Δ vs live (150−100) renders")
}

// TestBrowserApplyBaselineEntersDiffMode drives the O door end-to-end: engage ◇
// on a covering snapshot, Enter, and the tree enters compare mode against it
// while the live View is unchanged.
func TestBrowserApplyBaselineEntersDiffMode(t *testing.T) {
	dir := t.TempDir()
	writeTinySnapshot(t, dir, "/root")
	ui := newPickerUI(t, dir)

	pressKey(ui, 'O')
	settle(t, ui)
	require.NotNil(t, ui.browser)

	// Engage ◇ on the covering snapshot, then apply.
	ui.browserMoveBase(ui.browser, +1)
	require.GreaterOrEqual(t, ui.browser.baseCur, 0, "◇ engaged on a covering snapshot")
	ui.applyBrowser(ui.browser)
	settle(t, ui)

	assert.False(t, ui.pages.HasPage("snapshotpicker"), "Enter closes the browser")
	assert.True(t, ui.inDiffMode(), "applying the ◇ cursor sets the baseline")
	assert.True(t, ui.viewIsLive(), "the live View is unchanged (only the baseline moved)")
}
