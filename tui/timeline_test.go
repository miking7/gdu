package tui

import (
	"bytes"
	"os"
	"path/filepath"
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
	"github.com/gdamore/tcell/v2"
)

// Timeline fixture timestamps, oldest → newest.
var (
	ts1 = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	ts2 = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	ts3 = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
)

// writeArchiveSnapshot writes one snapshot of root into dir: root holds file
// "f" (size fSize) and, when subSize >= 0, a subdir "sub" holding file "s" of
// subSize.
func writeArchiveSnapshot(t *testing.T, dir, name, root string, fSize, subSize int64, ts time.Time) {
	t.Helper()
	tree := &analyze.Dir{
		File:      &analyze.File{Name: filepath.Base(root)},
		BasePath:  filepath.Dir(root),
		ItemCount: 1,
	}
	tree.AddFile(&analyze.File{Name: "f", Size: fSize, Usage: fSize, Parent: tree})
	if subSize >= 0 {
		sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: tree}, ItemCount: 1}
		sub.AddFile(&analyze.File{Name: "s", Size: subSize, Usage: subSize, Parent: sub})
		tree.AddFile(sub)
	}
	tree.UpdateStats(make(fs.HardLinkedItems))

	f, err := os.Create(filepath.Join(dir, name))
	require.NoError(t, err)
	meta := parquet.ScanMeta{ScanRoot: root, ScanTime: ts.UTC(), Host: "h1", Username: "u"}
	require.NoError(t, parquet.WriteTree(f, tree, &meta))
	require.NoError(t, f.Close())
}

// liveRootTree builds the in-memory live tree of /root: f=100 plus sub/s=100.
func liveRootTree() *analyze.Dir {
	tree := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/", ItemCount: 1}
	tree.AddFile(&analyze.File{Name: "f", Size: 100, Usage: 100, Parent: tree})
	sub := &analyze.Dir{File: &analyze.File{Name: "sub", Parent: tree}, ItemCount: 1}
	sub.AddFile(&analyze.File{Name: "s", Size: 100, Usage: 100, Parent: sub})
	tree.AddFile(sub)
	tree.UpdateStats(make(fs.HardLinkedItems))
	return tree
}

// newLiveUI builds a UI showing the live /root tree, wired to snapshotsDir,
// as a completed chosen-root scan would leave it.
func newLiveUI(t *testing.T, snapshotsDir string) *UI {
	t.Helper()
	app := testapp.CreateMockedApp(true)
	sim := testapp.CreateSimScreen()
	t.Cleanup(func() { sim.Fini() })
	ui := CreateUI(app, sim, &bytes.Buffer{}, false, false, false, false)
	ui.SetSnapshotsDir(snapshotsDir)

	live := &view{tree: liveRootTree(), topPath: "/root", scannedAt: time.Now()}
	ui.liveView = live
	ui.returnView = live
	ui.applyView(live, "/root", "")
	return ui
}

// enterSub navigates the shown view into its "sub" folder.
func enterSub(t *testing.T, ui *UI) {
	t.Helper()
	sub := findDirChild(ui.currentDir, "sub")
	require.NotNil(t, sub, "folder \"sub\" must exist in the current dir")
	ui.currentDir = sub
	ui.showDir()
}

// settle drains queued draws until the UI goes quiet: no queued draws, no
// in-flight picker/step work. Async flows hop between worker goroutines and
// the queued-draw event loop, so a single drain is not enough.
func settle(t *testing.T, ui *UI) {
	t.Helper()
	app := ui.app.(*testapp.MockedApp)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if app.PendingDrawCount() > 0 {
			for _, f := range app.GetUpdateDraws() {
				f()
			}
			continue
		}
		if !ui.stepLoading && !ui.baseStepLoading && ui.snapshotWorkActive.Load() == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("UI did not settle")
}

func pressKey(ui *UI, r rune) {
	ui.keyPressed(tcell.NewEventKey(tcell.KeyRune, r, 0))
}

func pressEsc(ui *UI) {
	ui.keyPressed(tcell.NewEventKey(tcell.KeyEsc, 0, 0))
}

// TestStepIntoPastAndBack: [ steps older with the folder and
// footer preserved, ] walks back, and ] at the newest switches to the
// in-memory live tree instantly.
func TestStepIntoPastAndBack(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	// [ lands on the newest snapshot, same folder.
	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts2), "first step lands on the newest snapshot")
	assert.Equal(t, "/root/sub", ui.currentDirPath, "the current folder path is preserved")
	assert.Equal(t, "/root", ui.timelineRoot)
	footer := ui.footerLabel.GetText(false)
	assert.Contains(t, footer, "this folder:")
	assert.Contains(t, footer, ts2.Local().Format(headerTimeLayout))
	assert.Contains(t, footer, "vs previous", "stepping shows the transient micro-diff")

	// [ again: older still.
	pressKey(ui, '[')
	settle(t, ui)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts1))

	// [ at the oldest: soft notice, no move.
	pressKey(ui, '[')
	settle(t, ui)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts1), "no snapshot older than the oldest")
	assert.Contains(t, ui.headerNotice, oldestNotice)

	// ] back to the newest snapshot.
	pressKey(ui, ']')
	settle(t, ui)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts2))

	// ] at the newest: the in-memory live tree covers /root/sub → instant switch.
	pressKey(ui, ']')
	settle(t, ui)
	assert.True(t, ui.viewIsLive(), "] past the newest switches to the live tree")
	assert.Equal(t, "/root/sub", ui.currentDirPath)
	assert.Contains(t, ui.footerLabel.GetText(false), "live · scanned",
		"the live switch footer names the scan time")
	assert.False(t, ui.pages.HasPage("confirm"), "no rescan offer when a covering live tree exists")
}

// TestStepPinsDeepestCoveringRoot: several roots cover the folder → the
// deepest covering root wins the pin.
func TestStepPinsDeepestCoveringRoot(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "root.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "sub.parquet", "/root/sub", 30, -1, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '[')
	settle(t, ui)
	assert.Equal(t, "/root/sub", ui.timelineRoot, "the deepest covering root wins")
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts2))
}

// TestStepPinsCurrentSnapshotRoot: stepping from a snapshot View already on
// the timeline pins to that snapshot's root — no cross-root surprises — even
// when a deeper covering root exists.
func TestStepPinsCurrentSnapshotRoot(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "r1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "r2.parquet", "/root", 20, 20, ts2)
	writeArchiveSnapshot(t, dir, "sub.parquet", "/root/sub", 30, -1, ts3)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	// Stand on the /root@ts2 snapshot (as the O picker would leave it).
	covering, err := ui.coveringListings("/root/sub", "")
	require.NoError(t, err)
	var atTs2 *view
	for i := range covering {
		if covering[i].ScanTs.Equal(ts2) {
			tree, terr := ui.loadListingTree(&covering[i])
			require.NoError(t, terr)
			info := covering[i].SnapshotInfo
			atTs2 = &view{tree: tree, topPath: covering[i].ScanRoot, snapshot: &info}
		}
	}
	require.NotNil(t, atTs2)
	ui.applyView(atTs2, "/root/sub", "")

	pressKey(ui, '[')
	settle(t, ui)
	assert.Equal(t, "/root", ui.timelineRoot, "the pin follows the current snapshot's root")
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts1), "[ from ts2 lands on ts1")
}

// TestStepAncestorFallback: the folder is absent in an older snapshot → its
// nearest existing ancestor is shown, with a one-line notice.
func TestStepAncestorFallback(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, -1, ts1) // no sub yet
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '[')
	settle(t, ui)
	assert.Equal(t, "/root/sub", ui.currentDirPath)

	pressKey(ui, '[')
	settle(t, ui)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts1))
	assert.Equal(t, "/root", ui.currentDirPath, "absent folder falls back to the nearest ancestor")
	assert.Contains(t, ui.headerNotice, "did not exist")
}

// TestStepNoCoveringSnapshots: [ with no covering history is a soft notice,
// never an error page.
func TestStepNoCoveringSnapshots(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "other.parquet", "/other", 10, -1, ts1)
	ui := newLiveUI(t, dir)

	pressKey(ui, '[')
	settle(t, ui)
	assert.True(t, ui.viewIsLive(), "the view does not change")
	assert.Contains(t, ui.headerNotice, noCoveringNotice)
	assert.False(t, ui.pages.HasPage("error"))
}

// TestTimelineFoldRule: the snapshot just saved from the still-unchanged live
// tree folds into the live position (one timeline point, not two) and
// un-folds once the live tree diverges.
func TestTimelineFoldRule(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s3.parquet", "/root", 30, 30, ts3) // the just-saved one
	ui := newLiveUI(t, dir)

	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	require.Len(t, covering, 2)

	// Mark the newest as the snapshot saved from the current live tree.
	for i := range covering {
		if covering[i].ScanTs.Equal(ts3) {
			ui.liveSavedKey = covering[i].Key()
			ui.liveSavedValid = true
		}
	}

	ui.pinTimeline(covering)
	require.Len(t, ui.timelineEntries, 1, "the just-saved snapshot folds into the live position")
	assert.True(t, ui.timelineEntries[0].ScanTs.Equal(ts1))
	assert.Equal(t, len(ui.timelineEntries), ui.timelinePos, "the walk starts at the live end")

	// A mutation (delete, refresh) diverges the live tree: the snapshot
	// reappears as its own point.
	ui.liveDiverged.Store(true)
	ui.pinTimeline(covering)
	assert.Len(t, ui.timelineEntries, 2, "a diverged live tree un-folds the saved snapshot")
}

// TestEndOfTimelineRescanOffer: ] at the newest with no covering live tree
// offers an explicit rescan — never scans as a side effect.
func TestEndOfTimelineRescanOffer(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)

	// Drop the live tree (as a session opened from a snapshot would be).
	ui.liveView = nil

	pressKey(ui, ']')
	settle(t, ui)
	assert.True(t, ui.pages.HasPage("confirm"), "the rescan offer is an explicit dialog")
	assert.NotNil(t, ui.currentView.snapshot, "the view has not moved")
	assert.False(t, ui.scanning, "no scan started without acceptance")
}

// TestEndOfTimelineNonCoveringLive: an in-memory live tree that does not
// cover the folder cannot be switched to — the rescan offer appears instead.
func TestEndOfTimelineNonCoveringLive(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)

	// The live slot holds an unrelated tree now.
	other := &analyze.Dir{File: &analyze.File{Name: "elsewhere"}, BasePath: "/"}
	other.UpdateStats(make(fs.HardLinkedItems))
	ui.liveView = &view{tree: other, topPath: "/elsewhere", scannedAt: time.Now()}

	pressKey(ui, ']')
	settle(t, ui)
	assert.True(t, ui.pages.HasPage("confirm"), "a non-covering live tree cannot be switched to")
	assert.NotNil(t, ui.currentView.snapshot)
}

// TestEndOfTimelineRescanRecords: the accepted end-of-timeline rescan is a
// deliberate chosen-root scan — it records a snapshot, unlike transient
// refreshes.
func TestEndOfTimelineRescanRecords(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	snapshotsOut := t.TempDir()
	ui.SetSaveSnapshot(snapshotsOut, 0)
	ui.Analyzer = &pathAnalyzer{}
	ui.done = make(chan struct{})

	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)
	ui.liveView = nil

	// Accept the offer (the dialog's Rescan button runs exactly this).
	ui.rescanTimelineRoot()
	<-ui.done
	settle(t, ui)

	assert.True(t, ui.viewIsLive(), "the finished rescan is watched and becomes the view")
	entries, err := os.ReadDir(snapshotsOut)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the chosen-root rescan records a snapshot")
	assert.True(t, ui.liveSavedValid, "the fold identity is recorded for the timeline")
}

// TestStepDuringScanKeepsProgressHidden drives scan-wait time travel:
// stepping works while a scan runs, the live position is the progress screen,
// and completion never steals focus from the past.
func TestStepDuringScanKeepsProgressHidden(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)

	// A scan is running: its progress page is up, the timeline stays walkable.
	blockedAnalyzer := &blockingAnalyzer{release: make(chan struct{})}
	ui.Analyzer = blockedAnalyzer
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("/root", nil))
	front, _ := ui.pages.GetFrontPage()
	require.Equal(t, "progress", front)
	require.True(t, ui.scanning)

	// [ from the progress screen: step into the past; the progress page hides.
	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)
	front, _ = ui.pages.GetFrontPage()
	assert.NotEqual(t, "progress", front, "the progress page hides while browsing the past")
	assert.True(t, ui.scanning, "the scan keeps running")

	// The right-edge indicator shows wherever you are in the past.
	assert.True(t, ui.scanning)

	// Completion while in the past: no focus steal, footer flash instead.
	close(blockedAnalyzer.release)
	<-ui.done
	settle(t, ui)
	assert.NotNil(t, ui.currentView.snapshot, "completion never steals focus from the past")
	assert.False(t, ui.scanning)
	assert.NotNil(t, ui.liveView, "the finished tree becomes the live position")
	assert.Contains(t, ui.footerLabel.GetText(false), scanCompleteNotice)

	// ] at the newest now switches to the fresh live tree.
	pressKey(ui, ']')
	settle(t, ui)
	assert.True(t, ui.viewIsLive(), "] leads to the finished scan")
}

// blockingAnalyzer is a MockedAnalyzer whose AnalyzeDir blocks until released,
// so tests can hold a scan "running". tree, when set, is returned by
// GetCurrentDir so a mid-scan preview has a partial tree to show; final, when
// set, is the completed-scan tree AnalyzeDir returns on release (nil falls back
// to the mock tree), so a test can control both the partial and the finished tree.
type blockingAnalyzer struct {
	testanalyze.MockedAnalyzer
	release chan struct{}
	tree    fs.Item
	final   fs.Item
}

func (a *blockingAnalyzer) AnalyzeDir(
	path string, ignore common.ShouldDirBeIgnored, filter common.ShouldFileBeIgnored,
) fs.Item {
	<-a.release
	if a.final != nil {
		return a.final
	}
	return a.MockedAnalyzer.AnalyzeDir(path, ignore, filter)
}

// GetCurrentDir exposes the partial tree so enterPreview works during a scan.
func (a *blockingAnalyzer) GetCurrentDir() fs.Item {
	return a.tree
}

// TestStepFromPreview: the timeline is reachable from a mid-scan
// preview — [ leaves the preview and steps into the covering snapshot, and ]
// returns to the live position, which while scanning is the progress screen.
func TestStepFromPreview(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)

	blocked := &blockingAnalyzer{release: make(chan struct{}), tree: liveRootTree()}
	ui.Analyzer = blocked
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("/root", nil))
	require.True(t, ui.scanning)

	ui.enterPreview()
	require.True(t, ui.previewing)

	// [ leaves the preview and steps into the past.
	pressKey(ui, '[')
	settle(t, ui)
	assert.False(t, ui.previewing, "[ exits the preview")
	require.NotNil(t, ui.currentView.snapshot, "[ steps into the covering snapshot")

	// ] returns to the live position — the running scan's progress screen.
	pressKey(ui, ']')
	settle(t, ui)
	front, _ := ui.pages.GetFrontPage()
	assert.Equal(t, scanProgressPage, front, "] returns to the live progress screen")

	close(blocked.release)
	<-ui.done
	settle(t, ui)
}

// TestStepAfterFirstScanShowsNoCoveringNotice: right after a first-ever scan
// the only covering snapshot is folded into the live position — [ has no past
// to reach and says so (not "already at the oldest").
func TestStepAfterFirstScanShowsNoCoveringNotice(t *testing.T) {
	dir := t.TempDir()
	ui := newLiveUI(t, dir)
	ui.SetSaveSnapshot(dir, 0)
	ui.Analyzer = &pathAnalyzer{}
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("/root", nil))
	<-ui.done
	settle(t, ui)
	require.True(t, ui.liveSavedValid)

	pressKey(ui, '[')
	settle(t, ui)
	assert.True(t, ui.viewIsLive(), "no step happened")
	assert.Contains(t, ui.headerNotice, noCoveringNotice)
	assert.False(t, ui.timelineActive, "the next [ re-derives the timeline")
}

// TestStepToLiveEndDuringScanShowsProgress: while a scan runs — even a
// same-root refresh with a covering live tree in memory — the live position is
// the progress screen, never the stale pre-scan tree the scan goroutine may be
// mutating.
func TestStepToLiveEndDuringScanShowsProgress(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)

	blocked := &blockingAnalyzer{release: make(chan struct{})}
	ui.Analyzer = blocked
	ui.done = make(chan struct{})
	require.NoError(t, ui.analyzePath("/root", nil, scanOpts{transient: true}))
	require.True(t, ui.scanning)

	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot, "stepping works during the scan")

	pressKey(ui, ']')
	settle(t, ui)
	front, _ := ui.pages.GetFrontPage()
	assert.Equal(t, scanProgressPage, front,
		"the live position while scanning is the progress screen, not the stale tree")

	// Completion while at the (progress) live position applies the new tree.
	close(blocked.release)
	<-ui.done
	settle(t, ui)
	assert.Equal(t, ui.liveView, ui.currentView, "the finished tree is shown")
	assert.True(t, ui.viewIsLive())
}

// TestStepNewerFromOffTimelineSnapshot: ] from a snapshot View that is not a
// timeline member (a -f import, or a foreign identity) must reach the
// switch-to-covering-timeline-or-offer-rescan flow, not silently no-op.
func TestStepNewerFromOffTimelineSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	ui.liveView = nil // the session started from the import
	off := &view{tree: liveRootTree(), topPath: "/root",
		snapshot: &parquet.SnapshotInfo{ScanRoot: "/root", ScanTs: ts3, Host: "other"}}
	ui.returnView = off
	ui.applyView(off, "/root", "")

	pressKey(ui, ']')
	settle(t, ui)
	assert.True(t, ui.pages.HasPage("confirm"),
		"] at the live end with no live tree offers the rescan")
}

// TestDivergenceRederivesActivePin: a delete while the timeline is pinned
// un-folds the just-saved snapshot for the *next* step too — the stale pin
// must not keep skipping it.
func TestDivergenceRederivesActivePin(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s3.parquet", "/root", 30, 30, ts3) // just saved from live
	ui := newLiveUI(t, dir)
	covering, err := ui.coveringListings("/root", "")
	require.NoError(t, err)
	for i := range covering {
		if covering[i].ScanTs.Equal(ts3) {
			ui.liveSavedKey = covering[i].Key()
			ui.liveSavedValid = true
		}
	}

	pressKey(ui, '[') // pins; ts3 folds into live; lands on ts1
	settle(t, ui)
	require.True(t, ui.currentView.snapshot.ScanTs.Equal(ts1))
	pressKey(ui, ']') // back to live
	settle(t, ui)
	require.True(t, ui.viewIsLive())

	ui.liveDiverged.Store(true) // a delete diverged the live tree; ts3 un-folds

	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts3),
		"[ after divergence lands on the just-un-folded snapshot, not past it")
}

// TestScanCompletionWithModalOverProgressStillApplies: a quit confirmation
// stacked over the progress page is not "stepping away" — the finished tree
// must still be applied (review finding).
func TestScanCompletionWithModalOverProgressStillApplies(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.SetSaveSnapshot(t.TempDir(), 0)
	blocked := &blockingAnalyzer{release: make(chan struct{})}
	ui.Analyzer = blocked
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("/root", nil))

	pressKey(ui, 'q') // quit-mid-scan confirmation over the progress page
	require.True(t, ui.pages.HasPage(scanQuitPage))

	close(blocked.release)
	<-ui.done
	settle(t, ui)
	assert.Equal(t, ui.liveView, ui.currentView,
		"completion under the quit modal still applies the finished tree")
	require.NotNil(t, ui.currentDir)
}

// TestEscOnProgressScreenRaisesQuitConfirmation: Esc on the scan's progress
// screen backs out of the scan the same way 'q' does — the same
// quit-without-saving confirmation, aligning Esc with its back-out role.
func TestEscOnProgressScreenRaisesQuitConfirmation(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.SetSaveSnapshot(t.TempDir(), 0)
	blocked := &blockingAnalyzer{release: make(chan struct{})}
	ui.Analyzer = blocked
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("/root", nil))

	pressEsc(ui) // Esc over the progress page raises the quit-mid-scan confirmation
	require.True(t, ui.pages.HasPage(scanQuitPage))

	close(blocked.release)
	<-ui.done
	settle(t, ui)
}

// TestEscDuringTransientScanDoesNotQuit: Esc must not hard-quit a transient
// refresh (snapshots enabled here, so it is the transient flag alone that keeps
// the confirmation away). Esc never causes an unconfirmed exit; only 'q' would.
func TestEscDuringTransientScanDoesNotQuit(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.SetSaveSnapshot(t.TempDir(), 0) // recording enabled...
	blocked := &blockingAnalyzer{release: make(chan struct{})}
	ui.Analyzer = blocked
	ui.done = make(chan struct{})
	// ...but this is a transient (spot-rescan-style) scan, so it records nothing.
	require.NoError(t, ui.analyzePath("/root", nil, scanOpts{transient: true}))
	require.True(t, ui.scanning)

	pressEsc(ui)
	assert.False(t, ui.pages.HasPage(scanQuitPage), "Esc raises no quit modal for a transient scan")
	assert.True(t, ui.scanning, "Esc did not quit or disturb the transient scan")
	assert.True(t, ui.pages.HasPage(scanProgressPage), "the progress page is untouched")

	close(blocked.release)
	<-ui.done
	settle(t, ui)
}

// TestFirstScanFallsBackToShallowerTimeline: when the deepest covering root's
// only snapshot folds into live (first-ever scan of this folder), the pin
// falls back to a shallower covering root with walkable history, matching the
// header hint's promise (review finding).
func TestFirstScanFallsBackToShallowerTimeline(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "disk.parquet", "/", 10, -1, ts1) // old whole-disk snapshot
	ui := newLiveUI(t, dir)
	ui.SetSaveSnapshot(dir, 0)
	ui.Analyzer = &pathAnalyzer{}
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("/root", nil)) // saves + folds /root's only snapshot
	<-ui.done
	settle(t, ui)
	require.True(t, ui.liveSavedValid)

	pressKey(ui, '[')
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot, "the walk falls back to the whole-disk timeline")
	assert.Equal(t, "/", ui.timelineRoot)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts1))
}

// --- Baseline stepping ({ }) ------------------------------------------------

// TestBaselineStepEntersCompareVsPrevious: { with no baseline compares the view
// against the snapshot immediately before it (E3); further { walk older; { at
// the oldest is a soft notice with the baseline unchanged.
func TestBaselineStepEntersCompareVsPrevious(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '{')
	settle(t, ui)
	require.True(t, ui.inDiffMode(), "{ enters a comparison")
	assert.True(t, ui.baselineTs.Equal(ts2), "compare vs the previous (newest) snapshot")
	assert.True(t, ui.renderingDelta(), "the Δ column is shown")
	assert.True(t, ui.viewIsLive(), "the view is unchanged — only the baseline moved")

	pressKey(ui, '{')
	settle(t, ui)
	assert.True(t, ui.baselineTs.Equal(ts1), "{ walks the baseline older")

	pressKey(ui, '{')
	settle(t, ui)
	assert.True(t, ui.baselineTs.Equal(ts1), "no snapshot older than the oldest")
	assert.Contains(t, ui.headerNotice, oldestNotice)
	assert.True(t, ui.inDiffMode(), "the baseline is kept at the oldest")
}

// TestBaselineStepOntoViewClears: } walking the baseline onto the view's
// position clears it (E4) — the linear-timeline mirror of { entering it.
func TestBaselineStepOntoViewClears(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '{') // baseline = ts2, just below the live view
	settle(t, ui)
	require.True(t, ui.inDiffMode())

	pressKey(ui, '}') // steps ◇ up onto the live position → clears
	settle(t, ui)
	assert.False(t, ui.inDiffMode(), "} onto the view clears the baseline")
	assert.Contains(t, ui.footerLabel.GetText(false), "baseline cleared")
}

// TestBaselineBraceNewerWithNoBaselineTeaches: } with nothing set points at {,
// rather than doing nothing.
func TestBaselineBraceNewerWithNoBaselineTeaches(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '}')
	settle(t, ui)
	assert.False(t, ui.inDiffMode())
	assert.Contains(t, ui.headerNotice, "no baseline — { compare previous")
}

// TestBaselineStepRetargetsWhileLoading: once the timeline is pinned, pressing {
// again before the load lands chains to the older target through the loading
// page (mirroring the view walk's retarget), rather than stalling or landing on
// a stale intermediate.
func TestBaselineStepRetargetsWhileLoading(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	writeArchiveSnapshot(t, dir, "s3.parquet", "/root", 30, 60, ts3)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '{') // establish baseline = ts3 and pin the timeline
	settle(t, ui)
	require.True(t, ui.baselineTs.Equal(ts3))

	pressKey(ui, '{') // heads to ts2 (loading page up, timeline active)
	pressKey(ui, '{') // retargets to ts1 before ts2 lands
	settle(t, ui)
	assert.True(t, ui.baselineTs.Equal(ts1), "the walk chained through to the older target")
}

// TestBaselineViewCrossesBaseline: stepping the view onto the baseline snapshot
// flashes the honest-zero notice and keeps the comparison (E5); stepping the
// view past it leaves ◇ newer than ● (E6); { then walks ◇ back onto ● and clears.
func TestBaselineViewCrossesBaseline(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	writeArchiveSnapshot(t, dir, "s3.parquet", "/root", 30, 60, ts3)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '{') // baseline = ts3 (the previous to live)
	settle(t, ui)
	require.True(t, ui.baselineTs.Equal(ts3))

	pressKey(ui, '[') // step the view onto ts3 — the baseline itself
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts3), "view lands on the baseline snapshot")
	assert.Contains(t, ui.footerLabel.GetText(false), "viewing the baseline snapshot", "E5 flash")
	assert.True(t, ui.inDiffMode(), "the baseline is kept")

	pressKey(ui, '[') // step the view older than the baseline → ◇ now newer than ● (E6)
	settle(t, ui)
	require.NotNil(t, ui.currentView.snapshot)
	assert.True(t, ui.currentView.snapshot.ScanTs.Equal(ts2))
	assert.True(t, ui.baselineTs.After(ui.currentView.snapshot.ScanTs), "◇ is newer than ● (E6)")

	pressKey(ui, '{') // walk ◇ older, onto ●'s position → clears (E4)
	settle(t, ui)
	assert.False(t, ui.inDiffMode(), "{ onto the view clears")
	assert.Contains(t, ui.footerLabel.GetText(false), "baseline cleared")
}

// TestBaselineStepOffNewestEndClears: with the view stepped back, } walks the
// baseline off the newest snapshot and clears — ◇ never rests on the live end.
func TestBaselineStepOffNewestEndClears(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1)
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	writeArchiveSnapshot(t, dir, "s3.parquet", "/root", 30, 60, ts3)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '{') // baseline = ts3 (newest)
	settle(t, ui)
	require.True(t, ui.baselineTs.Equal(ts3))

	pressKey(ui, '[') // view onto ts3
	settle(t, ui)
	pressKey(ui, '[') // view onto ts2 → ◇=ts3 is the newest entry and newer than ●
	settle(t, ui)
	require.True(t, ui.currentView.snapshot.ScanTs.Equal(ts2))

	pressKey(ui, '}') // ◇ steps off the newest end → clears
	settle(t, ui)
	assert.False(t, ui.inDiffMode(), "} off the newest snapshot clears")
	assert.Contains(t, ui.footerLabel.GetText(false), "baseline cleared")
}

// TestBaselineStepClearsOnCoverageLoss: navigating up out of a deeper baseline's
// coverage clears it with the E7 flash (the browser-seam rule, at nav time).
func TestBaselineStepClearsOnCoverageLoss(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "sub.parquet", "/root/sub", 30, -1, ts2)
	ui := newLiveUI(t, dir)
	enterSub(t, ui)

	pressKey(ui, '{') // baseline = the /root/sub snapshot
	settle(t, ui)
	require.True(t, ui.inDiffMode())
	require.True(t, ui.baselineEverCovered)

	ui.handleLeft() // navigate up to /root — outside the baseline's coverage
	assert.False(t, ui.inDiffMode(), "leaving the baseline's coverage clears it")
	assert.Contains(t, ui.footerLabel.GetText(false), "baseline no longer covers this folder — cleared")
	assert.NotContains(t, ui.header.GetText(false), "Baseline", "the header collapses off the ◇ line")
}

// TestBaselineNeverCoveringSurvivesNavigation: a baseline that never covers the
// shown folder — the --baseline-root cross-volume override — is not auto-cleared
// by navigation, because the coverage latch never arms.
func TestBaselineNeverCoveringSurvivesNavigation(t *testing.T) {
	dir := t.TempDir()
	ui := newLiveUI(t, dir) // viewing live /root

	other := &parquet.SnapshotInfo{ScanRoot: "/other", ScanTs: ts1}
	ui.SetBaseline(analyze.BuildBaseline(liveRootTree(), "/other", 0), other)
	require.True(t, ui.inDiffMode())
	assert.False(t, ui.baselineEverCovered, "a non-covering baseline never arms the latch")

	cleared := ui.enforceBaselineCoverage("/root/sub") // simulate navigation
	assert.False(t, cleared)
	assert.True(t, ui.inDiffMode(), "the cross-volume override survives navigation")
}
