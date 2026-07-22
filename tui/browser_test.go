package tui

import (
	"bytes"
	"testing"
	"time"

	"github.com/rivo/tview"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
