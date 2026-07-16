package tui

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
)

// snapshotViewAt builds a snapshot View of the live fixture tree shape, dated
// ts, without touching the archive — for state-machine tests that don't step.
func snapshotViewAt(ts time.Time) *view {
	info := &parquet.SnapshotInfo{ScanRoot: "/root", ScanTs: ts, Host: "h1"}
	return &view{tree: liveRootTree(), topPath: "/root", snapshot: info}
}

// pathAnalyzer is a mock analyzer whose result tree is rooted at the requested
// path (as real analyzers are) and contains file "f" plus subdir "sub" — so
// path preservation and cursor restoration behave as in production.
type pathAnalyzer struct{ testanalyze.MockedAnalyzer }

// AnalyzeDir returns a fixed tree rooted at path.
func (a *pathAnalyzer) AnalyzeDir(
	path string, _ common.ShouldDirBeIgnored, _ common.ShouldFileBeIgnored,
) fs.Item {
	dir := &analyze.Dir{
		File:      &analyze.File{Name: filepath.Base(path)},
		BasePath:  filepath.Dir(path),
		ItemCount: 1,
	}
	dir.AddFile(&analyze.File{Name: "f", Size: 60, Usage: 60, Parent: dir})
	sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: dir}, ItemCount: 1}
	sub.AddFile(&analyze.File{Name: "s", Size: 30, Usage: 30, Parent: sub})
	dir.AddFile(sub)
	return dir
}

// TestEscLayering: Esc clears the Baseline first, then returns to the return
// view, then does nothing — one promise at a time. Esc never scans.
func TestEscLayering(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())

	// Stand on a snapshot View with a Baseline set.
	snap := snapshotViewAt(ts2)
	ui.applyView(snap, "/root", "")
	ui.SetBaseline(analyze.BuildBaseline(liveRootTree(), "/root", 0), snapAt(ts1))
	require.True(t, ui.inDiffMode())
	require.False(t, ui.viewIsLive())

	pressEsc(ui)
	assert.False(t, ui.inDiffMode(), "the first Esc clears the baseline")
	assert.False(t, ui.viewIsLive(), "the view has not moved yet")

	pressEsc(ui)
	assert.True(t, ui.viewIsLive(), "the second Esc returns to the return view")
	assert.Equal(t, ui.returnView, ui.currentView)

	pressEsc(ui) // nothing left for Esc to do; must not scan or error
	assert.True(t, ui.viewIsLive())
	assert.False(t, ui.scanning)
}

// TestReadOnlyMatrix: d/e/r are blocked with the go-live signpost on every
// non-live View; v and i stay allowed.
func TestReadOnlyMatrix(t *testing.T) {
	views := map[string]*view{
		"snapshot": snapshotViewAt(ts2),
		"import":   {tree: liveRootTree(), topPath: "/root", importLabel: "export.json"},
	}
	for name, v := range views {
		for _, key := range []rune{'d', 'e', 'r'} {
			t.Run(name+"_"+string(key), func(t *testing.T) {
				ui := newLiveUI(t, t.TempDir())
				ui.liveView = nil // read-only holds with or without a live tree
				ui.applyView(v, "/root", "")
				ui.table.Select(0, 0)

				pressKey(ui, key)
				assert.True(t, ui.pages.HasPage("confirm"), "the signpost dialog appears")
				assert.False(t, ui.pages.HasPage("deleting"))
				assert.False(t, ui.scanning, "nothing scans without acceptance")
			})
		}
	}

	// v and i remain available in snapshot Views (non-destructive; fails soft).
	ui := newLiveUI(t, t.TempDir())
	ui.applyView(snapshotViewAt(ts2), "/root", "")
	ui.selectItemByName("f")
	pressKey(ui, 'i')
	assert.True(t, ui.pages.HasPage("info"), "i works in a snapshot View")
}

// TestLiveViewMutationsUnblocked: the guards target snapshot Views only —
// a live View still reaches the normal delete confirmation.
func TestLiveViewMutationsUnblocked(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.selectItemByName("f")

	pressKey(ui, 'd')
	assert.True(t, ui.pages.HasPage("confirm"), "the normal delete confirmation appears")
}

// TestGoLiveInstantSwitch: go-live from a snapshot View switches instantly to
// a covering in-memory live tree, cursor kept.
func TestGoLiveInstantSwitch(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	live := ui.liveView

	ui.applyView(snapshotViewAt(ts2), "/root", "")
	ui.selectItemByName("sub")
	require.Equal(t, "sub", ui.selectedItemName())

	ui.goLiveHere()
	assert.Equal(t, live, ui.currentView, "instant switch to the in-memory live tree")
	assert.Equal(t, "sub", ui.selectedItemName(), "cursor kept")
	assert.Contains(t, ui.footerLabel.GetText(false), "live · scanned")
	assert.False(t, ui.scanning, "no scan when a covering live tree exists")
}

// TestGoLiveSpotRescan: without a covering live tree, go-live offers a scoped
// scan of just this folder; the resulting subtree-rooted live View keeps the
// cursor and never saves a snapshot.
func TestGoLiveSpotRescan(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	snapshotsOut := t.TempDir()
	ui.SetSaveSnapshot(snapshotsOut, 0)
	ui.liveView = nil // e.g. the session started from a snapshot
	importView := snapshotViewAt(ts2)
	ui.returnView = importView
	ui.applyView(importView, "/root", "")

	ui.goLiveHere()
	require.True(t, ui.pages.HasPage("confirm"), "the scoped scan is offered, not started")
	ui.pages.RemovePage("confirm")

	// Accept (the dialog's Scan button runs exactly this).
	ui.Analyzer = &pathAnalyzer{}
	ui.done = make(chan struct{})
	ui.spotRescan("/root", "sub")
	<-ui.done
	settle(t, ui)

	assert.True(t, ui.viewIsLive(), "the spot-rescan yields a live View")
	assert.Equal(t, "/root", ui.currentView.topPath, "rooted at the folder, subtree only")
	assert.Equal(t, "sub", ui.selectedItemName(), "cursor kept")
	assert.Equal(t, importView, ui.returnView, "Esc still returns where the session started")

	entries, err := os.ReadDir(snapshotsOut)
	require.NoError(t, err)
	assert.Empty(t, entries, "spot-rescans never save a snapshot")
}

// TestRefreshNeverSaves: the r refresh is transient — no snapshot is recorded
// and the fold identity is invalidated (the replaced live tree no longer is
// the saved snapshot's data).
func TestRefreshNeverSaves(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	snapshotsOut := t.TempDir()
	ui.SetSaveSnapshot(snapshotsOut, 0)
	ui.Analyzer = &pathAnalyzer{}
	ui.done = make(chan struct{})
	ui.liveSavedValid = true // as if the session's scan had just saved

	pressKey(ui, 'r')
	<-ui.done
	settle(t, ui)

	entries, err := os.ReadDir(snapshotsOut)
	require.NoError(t, err)
	assert.Empty(t, entries, "r refreshes never save a snapshot")
	assert.False(t, ui.liveSavedValid, "a transient refresh invalidates the fold identity")
	assert.True(t, ui.viewIsLive())
}

// TestDeleteDivergesLiveTree: a delete mutates the live tree in place, so the
// just-saved snapshot must un-fold on the timeline.
func TestDeleteDivergesLiveTree(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.selectItemByName("f")
	require.False(t, ui.liveDiverged.Load())

	ui.remover = func(fs.Item, fs.Item) error { return nil }
	ui.deleteSelected(false)
	settle(t, ui)
	assert.True(t, ui.liveDiverged.Load(), "a delete diverges the live tree")
}

// TestChosenRootScanRecordsAndFolds: a completed scan of a chosen root saves
// (per the tri-state) and records the fold identity for the timeline.
func TestChosenRootScanRecordsAndFolds(t *testing.T) {
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	snapshotsOut := t.TempDir()
	ui.SetSnapshotsDir(snapshotsOut)
	ui.SetSaveSnapshot(snapshotsOut, 0)
	ui.Analyzer = &pathAnalyzer{}
	ui.done = make(chan struct{})

	require.NoError(t, ui.AnalyzePath("/chosen", nil))
	<-ui.done
	settle(t, ui)

	entries, err := os.ReadDir(snapshotsOut)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the chosen-root scan records a snapshot")
	assert.True(t, ui.liveSavedValid)
	assert.True(t, ui.viewIsLive())
	assert.Equal(t, ui.currentView, ui.returnView, "the first view is the session's return view")
	assert.Equal(t, ui.currentView, ui.liveView)
}

// TestQuitMidScanConfirms: quitting while a recording scan runs confirms and
// discards; a transient scan quits without asking.
func TestQuitMidScanConfirms(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.SetSaveSnapshot(t.TempDir(), 0)
	ui.scanning = true
	ui.scanTransient = false

	ui.quitApp(false)
	assert.True(t, ui.pages.HasPage(scanQuitPage), "the quit-mid-scan confirmation appears")
	ui.pages.RemovePage(scanQuitPage)

	// A transient scan was never going to save; quit proceeds directly.
	out := &bytes.Buffer{}
	ui.output = out
	ui.scanTransient = true
	ui.quitApp(true)
	assert.False(t, ui.pages.HasPage(scanQuitPage))
	assert.Contains(t, out.String(), "/root", "the quit completed (Q prints the current dir)")
}

// TestHeaderStates walks the header matrix.
func TestHeaderStates(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())

	// Live + no baseline, no covering history: the upstream hint.
	assert.Equal(t, upstreamHint, ui.header.GetText(false))

	// Once covering history exists, the hint becomes context-aware.
	ui.coveringHint = true
	ui.updateHeader()
	assert.Equal(t, historyHint, ui.header.GetText(false))
	assert.Equal(t, 1, ui.headerLines)

	// Snapshot View: the Viewing line, read-only, with the innermost Esc hint.
	ui.applyView(snapshotViewAt(ts2), "/root", "")
	header := ui.header.GetText(false)
	assert.Contains(t, header, "Viewing  snapshot "+ts2.Local().Format(headerTimeLayout))
	assert.Contains(t, header, "· read-only")
	assert.Contains(t, header, "[ older · ] newer")
	assert.Contains(t, header, "Esc return")
	assert.Equal(t, 1, ui.headerLines)

	// Snapshot View + Baseline: both slots; the Esc promise moves to the
	// Baseline line (innermost layer only).
	ui.SetBaseline(analyze.BuildBaseline(liveRootTree(), "/root", 0), snapAt(ts1))
	header = ui.header.GetText(false)
	lines := strings.Split(header, "\n")
	require.Len(t, lines, 2, "two slots → two header lines")
	assert.Contains(t, lines[0], "Viewing  snapshot")
	assert.NotContains(t, lines[0], "Esc return", "one Esc promise on screen at a time")
	assert.Contains(t, lines[1], "Baseline snapshot "+ts1.Local().Format(headerTimeLayout))
	assert.Contains(t, lines[1], "Δ shown")
	assert.Contains(t, lines[1], "> < sort · Esc clear")
	assert.Equal(t, 2, ui.headerLines)

	// Live + Baseline: the Baseline line only.
	ui.clearBaseline()
	pressEsc(ui) // return to live
	require.True(t, ui.viewIsLive())
	ui.SetBaseline(analyze.BuildBaseline(liveRootTree(), "/root", 0), snapAt(ts1))
	header = ui.header.GetText(false)
	assert.NotContains(t, header, "Viewing")
	assert.Contains(t, header, "Baseline snapshot")
	assert.Equal(t, 1, ui.headerLines)
	ui.clearBaseline()

	// An import View names its source.
	imp := &view{tree: liveRootTree(), topPath: "/root", importLabel: "export.json"}
	ui.applyView(imp, "/root", "")
	assert.Contains(t, ui.header.GetText(false), "import export.json · read-only")
}

// TestHeaderStartingSnapshotViewHasNoEscPromise: when the session was launched
// into the snapshot (Esc has nowhere to return), the Viewing line must not
// promise one.
func TestHeaderStartingSnapshotViewHasNoEscPromise(t *testing.T) {
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)

	snap := snapshotViewAt(ts2)
	ui.returnView = snap
	ui.applyView(snap, "/root", "")
	assert.NotContains(t, ui.header.GetText(false), "Esc return")
}

// TestHeaderHiddenCompactPrefix: header-hidden configs carry the mode on the
// dir-label line, so it is never invisible.
func TestHeaderHiddenCompactPrefix(t *testing.T) {
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.SetHeaderHidden()
	ui.createGrid()

	snap := snapshotViewAt(ts2)
	ui.applyView(snap, "/root", "")
	ui.SetBaseline(analyze.BuildBaseline(liveRootTree(), "/root", 0), snapAt(ts1))

	prefix := ui.dirLabelPrefix()
	assert.Contains(t, prefix, "[snapshot "+ts2.Local().Format(headerDateLayout)+"]")
	assert.Contains(t, prefix, "[Δ vs "+ts1.Local().Format(headerDateLayout)+"]")
	assert.Contains(t, ui.currentDirLabel.GetText(false), "[snapshot ")
}

// TestReadAnalysisParquetIsReadOnlySnapshotView: a -f Parquet import resolves
// its snapshot identity and opens hard read-only — the old graft-on-refresh
// behavior is retired.
func TestReadAnalysisParquetIsReadOnlySnapshotView(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "one.parquet", "/root", 20, 40, ts2)

	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.done = make(chan struct{})

	f, err := os.Open(filepath.Join(dir, "one.parquet"))
	require.NoError(t, err)
	defer f.Close()

	require.NoError(t, ui.ReadAnalysis(f))
	<-ui.done
	settle(t, ui)

	require.NotNil(t, ui.currentView)
	require.NotNil(t, ui.currentView.snapshot, "the -f Parquet view knows its snapshot identity")
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts2))
	assert.False(t, ui.viewIsLive())
	assert.Equal(t, ui.currentView, ui.returnView, "the import is the session's return view")

	// r must signpost, not graft live data into the imported tree.
	pressKey(ui, 'r')
	assert.True(t, ui.pages.HasPage("confirm"), "r on an import signposts the go-live flow")
	assert.False(t, ui.scanning)
}

// TestReadAnalysisJSONIsReadOnlyImportView: identity-less imports (JSON,
// stdin) are read-only import Views labeled by their source.
func TestReadAnalysisJSONIsReadOnlyImportView(t *testing.T) {
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.done = make(chan struct{})

	json := `[1,2,{"progname":"gdu"},[{"name":"/root"},{"name":"f","asize":10,"dsize":10}]]`
	require.NoError(t, ui.ReadAnalysis(strings.NewReader(json)))
	<-ui.done
	settle(t, ui)

	require.NotNil(t, ui.currentView)
	assert.False(t, ui.viewIsLive())
	assert.Equal(t, "(stdin)", ui.currentView.importLabel)
	assert.Contains(t, ui.header.GetText(false), "import (stdin)")
}

// TestOpenPickerOpensSnapshotView: O lists the whole archive and Enter opens
// the chosen snapshot as the View.
func TestOpenPickerOpensSnapshotView(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "other.parquet", "/other", 30, -1, ts3)
	ui := newLiveUI(t, dir)

	pressKey(ui, 'O')
	settle(t, ui)
	require.True(t, ui.pages.HasPage("snapshotpicker"), "the Open picker lists all roots and dates")

	// Choose the /other snapshot — the long jump to another root.
	listings, err := ui.coveringListings("/other", "")
	require.NoError(t, err)
	require.Len(t, listings, 1)
	ui.closeSnapshotPicker()
	ui.openSnapshotFromListing(&listings[0])
	settle(t, ui)

	require.NotNil(t, ui.currentView.snapshot)
	assert.Equal(t, "/other", ui.currentView.topPath)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts3))
	assert.False(t, ui.viewIsLive(), "an opened snapshot is read-only")
	require.NotNil(t, ui.returnView)
	assert.True(t, ui.returnView.isLive(), "Esc still returns to the live start")
}

// TestStartupPickerLoadsChosenSnapshot: the multi-snapshot -f chooser is the
// same component seeded with the file; Enter loads the selection.
func TestStartupPickerLoadsChosenSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "one.parquet", "/root", 20, 40, ts2)

	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.done = make(chan struct{})

	f, err := os.Open(filepath.Join(dir, "one.parquet"))
	require.NoError(t, err)
	defer f.Close()

	snapshots := []parquet.SnapshotInfo{
		{ScanRoot: "/root", ScanTs: ts2, Host: "h1"},
		{ScanRoot: "/root", ScanTs: ts1, Host: "h1"},
	}
	ui.showStartupSnapshotPicker(f, snapshots)
	assert.True(t, ui.pages.HasPage("snapshotpicker"))

	// Enter on the newest (the file holds it for real).
	ui.closeSnapshotPicker()
	ui.loadSnapshotFromFile(f, &snapshots[0])
	<-ui.done
	settle(t, ui)

	require.NotNil(t, ui.currentView.snapshot)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts2))
	assert.False(t, ui.viewIsLive())
}

// TestSnapshotViewLeftAtTopIsInert: a snapshot View is all one thing — left
// at its top neither lists devices nor scans the parent.
func TestSnapshotViewLeftAtTopIsInert(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.browseParentDirs = true
	ui.applyView(snapshotViewAt(ts2), "/root", "")

	ui.handleLeft()
	assert.NotNil(t, ui.currentDir, "no device list")
	assert.False(t, ui.scanning, "no parent scan")
	assert.False(t, ui.viewIsLive())
}

// TestViewSwitchClearsMarks: marked rows are index-bound to the old tree and
// must never survive a view switch.
func TestViewSwitchClearsMarks(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.markedRows[1] = struct{}{}

	ui.applyView(snapshotViewAt(ts2), "/root", "")
	assert.Empty(t, ui.markedRows, "marks do not survive a view switch")
}

// TestScanRunningBlocksConcurrentMutations: while a scan runs, d/e/r on the
// live view are blocked with an explanation.
func TestScanRunningBlocksConcurrentMutations(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.scanning = true
	ui.selectItemByName("f")

	pressKey(ui, 'd')
	assert.True(t, ui.pages.HasPage("confirm"))
	assert.False(t, ui.pages.HasPage("deleting"))
	ui.pages.RemovePage("confirm")

	pressKey(ui, 'r')
	assert.True(t, ui.pages.HasPage("confirm"), "r is blocked while a scan runs")
}

// TestFirstScanKeepsUpstreamHint: the snapshot saved from this very scan is
// the present, not history — a first-ever scan keeps the upstream hint and
// only genuinely walkable history flips it.
func TestFirstScanKeepsUpstreamHint(t *testing.T) {
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	snapshotsOut := t.TempDir()
	ui.SetSnapshotsDir(snapshotsOut)
	ui.SetSaveSnapshot(snapshotsOut, 0)
	ui.Analyzer = &pathAnalyzer{}
	ui.done = make(chan struct{})

	require.NoError(t, ui.AnalyzePath("/root", nil))
	<-ui.done
	settle(t, ui)
	assert.Equal(t, upstreamHint, ui.header.GetText(false),
		"the only snapshot folds into live — no history chrome on first run")

	// A second, older snapshot makes history walkable: the hint flips.
	writeArchiveSnapshot(t, snapshotsOut, "old.parquet", "/root", 10, 10, ts1)
	ui.refreshCoveringHint("/root")
	settle(t, ui)
	assert.Equal(t, historyHint, ui.header.GetText(false))
}
