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
	st.keys = tview.NewTextView()
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

// TestBrowserBDoorPreArmsPreviousSnapshot: B pre-arms ◇ on the snapshot just
// older than ● (the J2 compare-vs-previous default) and focuses it.
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
