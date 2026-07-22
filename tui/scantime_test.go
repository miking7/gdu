package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/testdir"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
)

// baselineWithObsolete builds a /root baseline tree carrying an extra "obsolete"
// file that exists only in the past. In a compare view it can appear only as a
// removed row, so its presence is proof that a diff was rendered.
func baselineWithObsolete() *analyze.Dir {
	tree := liveRootTree()
	tree.AddFile(&analyze.File{Name: "obsolete", Size: 50, Usage: 50, Parent: tree})
	tree.UpdateStats(make(fs.HardLinkedItems))
	return tree
}

// TestPreviewNeverRendersDeltaAndResumes is the F6 regression: with a baseline
// set, r then Tab into the mid-scan preview must render plain rows over the
// partial tree — never a diff of the half-scanned tree, which would show every
// not-yet-scanned item as a phantom removal. The baseline is suppressed, not
// cleared, so the diff (and its header tail) resume when the scan completes.
func TestPreviewNeverRendersDeltaAndResumes(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.SetBaseline(analyze.BuildBaseline(baselineWithObsolete(), "/root", 0), snapAt(ts1))
	require.True(t, ui.renderingDelta(), "baseline set: the live view diffs")
	require.Contains(t, strings.Join(diffRowTexts(ui), "\n"), "obsolete",
		"the live compare view shows the removed baseline item")

	// A genuinely partial mid-scan tree (only "f" discovered), and a completed
	// rescan that restores the full /root tree.
	partial := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/", ItemCount: 1}
	partial.AddFile(&analyze.File{Name: "f", Size: 100, Usage: 100, Parent: partial})
	partial.UpdateStats(make(fs.HardLinkedItems))
	scan := &blockingAnalyzer{release: make(chan struct{}), tree: partial, final: liveRootTree()}
	ui.Analyzer = scan
	ui.done = make(chan struct{})

	pressKey(ui, 'r') // rescan the root (parentDir is nil at the top → finishRootScan)
	require.True(t, ui.scanning)
	ui.keyPressed(tcell.NewEventKey(tcell.KeyTab, 0, 0)) // Tab → mid-scan preview
	require.True(t, ui.previewing)

	// The core F6 assertions: the partial tree renders plain, the baseline persists.
	assert.False(t, ui.renderingDelta(), "the partial preview must not diff")
	assert.True(t, ui.inDiffMode(), "the baseline is suppressed, not cleared")
	rows := strings.Join(diffRowTexts(ui), "\n")
	assert.NotContains(t, rows, "obsolete", "no phantom removed row from the baseline")
	assert.NotContains(t, rows, "removed")
	assert.NotContains(t, rows, "✗")
	assert.NotContains(t, rows, "▲")
	assert.Contains(t, ui.header.GetText(false), baselinePausedTail)
	assert.NotContains(t, ui.header.GetText(false), "Esc clear")

	// Completion resumes Δ automatically (E9): the removed row and the shown tail return.
	close(scan.release)
	<-ui.done
	settle(t, ui)
	assert.False(t, ui.previewing)
	assert.True(t, ui.renderingDelta(), "Δ resumes when the scan completes")
	assert.Contains(t, strings.Join(diffRowTexts(ui), "\n"), "obsolete",
		"the removed baseline item is back in the resumed diff")
	assert.Contains(t, ui.header.GetText(false), "Δ shown · Tab plain")
}

// TestSubdirRefreshResumesHeaderAndDelta covers the graft completion path: r in a
// subfolder finishes via the parentDir != nil branch, which re-renders with
// showDir but must also updateHeader — else the paused ◇ tail sticks after the
// diff has visibly resumed (the seam every unit test used to miss).
func TestSubdirRefreshResumesHeaderAndDelta(t *testing.T) {
	ui := newLiveUI(t, t.TempDir())
	ui.SetBaseline(analyze.BuildBaseline(baselineWithObsolete(), "/root", 0), snapAt(ts1))
	enterSub(t, ui) // into /root/sub
	require.True(t, ui.renderingDelta())

	finalSub := &analyze.Dir{File: &analyze.File{Name: "sub"}, ItemCount: 1}
	finalSub.AddFile(&analyze.File{Name: "s", Size: 100, Usage: 100, Parent: finalSub})
	scan := &blockingAnalyzer{release: make(chan struct{}), final: finalSub}
	ui.Analyzer = scan
	ui.done = make(chan struct{})

	pressKey(ui, 'r') // sub has a parent → graft branch on completion
	require.True(t, ui.scanning)
	assert.Contains(t, ui.header.GetText(false), baselinePausedTail,
		"mid-scan at the live position the ◇ tail is paused")

	close(scan.release)
	<-ui.done
	settle(t, ui)
	assert.False(t, ui.baselinePaused())
	assert.NotContains(t, ui.header.GetText(false), baselinePausedTail,
		"the graft completion must resume the ◇ tail")
	assert.Contains(t, ui.header.GetText(false), "Δ shown · Tab plain")
	assert.True(t, ui.renderingDelta())
}

// TestBaselinePausedTailReflectsScanState checks the ◇ tail directly across the
// three states: resting (shown, Esc clear), at a running scan's live position
// (paused, no toggles named), and stepped into the past mid-scan (shown again).
func TestBaselinePausedTailReflectsScanState(t *testing.T) {
	ui := newDiffUI(t)
	ui.SetBaseline(analyze.BuildBaseline(diffBaselineTop(), "top", 0), snapAt(diffBaselineTime()))
	assert.Contains(t, ui.header.GetText(false), "Δ shown · Tab plain · Esc clear")

	ui.scanning = true
	ui.scanPageHidden = false
	ui.updateHeader()
	assert.Contains(t, ui.header.GetText(false), baselinePausedTail)
	assert.NotContains(t, ui.header.GetText(false), "Esc clear")

	ui.scanPageHidden = true // stepped into the past: a complete snapshot, full grammar
	ui.updateHeader()
	assert.Contains(t, ui.header.GetText(false), "Δ shown · Tab plain")
	assert.NotContains(t, ui.header.GetText(false), baselinePausedTail)
}

// TestBraceOnProgressScreenFlashesScanRunning: { } on the live scan's progress
// screen have nothing to walk yet, so they flash the E10 teach notice.
func TestBraceOnProgressScreenFlashesScanRunning(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	ui := analyzedUI(t, &bytes.Buffer{})
	ui.scanning = true
	ui.pages.AddPage(scanProgressPage, ui.progressFlex, true, true)

	for _, r := range []rune{'{', '}'} {
		ui.headerNotice = ""
		key := ui.keyPressed(tcell.NewEventKey(tcell.KeyRune, r, 0))
		assert.Nil(t, key, "%c is consumed on the progress screen", r)
		assert.Contains(t, ui.headerNotice, braceDuringScanNotice)
	}
}

// TestBraceInPreviewFlashesScanRunning: the preview is the same suspended live
// position, so { } teach there too rather than dying silently.
func TestBraceInPreviewFlashesScanRunning(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	ui := analyzedUI(t, &bytes.Buffer{})
	ui.scanning = true
	ui.enterPreview()
	require.True(t, ui.previewing)

	key := ui.keyPressed(tcell.NewEventKey(tcell.KeyRune, '{', 0))
	assert.Nil(t, key)
	assert.True(t, ui.previewing, "{ does not leave the preview")
	assert.Contains(t, ui.headerNotice, braceDuringScanNotice)
}

// TestBraceDuringFileReadStaysInert: the -f read page reuses the "progress" page
// name but never scans, so the ui.scanning gate keeps braces dead (passed through)
// there — no bogus "scan running" flash while reading a file.
func TestBraceDuringFileReadStaysInert(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	ui := analyzedUI(t, &bytes.Buffer{})
	ui.scanning = false // a -f read, not a scan
	ui.pages.AddPage(scanProgressPage, ui.progressFlex, true, true)

	key := ui.keyPressed(tcell.NewEventKey(tcell.KeyRune, '{', 0))
	require.NotNil(t, key, "{ passes through during a file read")
	assert.Equal(t, '{', key.Rune())
	assert.NotContains(t, ui.headerNotice, braceDuringScanNotice)
}

// TestCompletionGrowthFlashNudgesCompare: with no baseline set, a completed root
// scan flashes the footer with the root's growth since the previous same-root
// snapshot and the key that starts the comparison (E9).
func TestCompletionGrowthFlashNudgesCompare(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2) // total 60
	ui := newLiveUI(t, dir)                                          // live /root usage 200

	ui.finishRootScan(liveRootTree(), "/root", time.Now(), parquet.SnapshotKey{}, false, scanOpts{})
	settle(t, ui)

	assert.Contains(t, ui.footerLabel.GetText(false),
		"+140 B since "+ts2.Local().Format("01-02")+" — { to compare")
}

// TestCompletionGrowthFlashSkippedWhenComparing: a comparison already on screen
// makes its own report, so completion does not also flash the nudge.
func TestCompletionGrowthFlashSkippedWhenComparing(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2)
	ui := newLiveUI(t, dir)
	ui.SetBaseline(analyze.BuildBaseline(liveRootTree(), "/root", 0), snapAt(ts2))

	ui.finishRootScan(liveRootTree(), "/root", time.Now(), parquet.SnapshotKey{}, false, scanOpts{})
	settle(t, ui)

	assert.NotContains(t, ui.footerLabel.GetText(false), "{ to compare")
}

// TestCompletionGrowthFlashSkipsJustSavedSnapshot: the snapshot this scan just
// saved folds into the live position, so the flash compares against the one
// before it, not against itself (which would always read a zero delta).
func TestCompletionGrowthFlashSkipsJustSavedSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s1.parquet", "/root", 10, 10, ts1) // total 20
	writeArchiveSnapshot(t, dir, "s2.parquet", "/root", 20, 40, ts2) // total 60, "just saved"
	ui := newLiveUI(t, dir)

	ts2Key := snapshotKeyFor(t, dir, ts2)
	ui.finishRootScan(liveRootTree(), "/root", time.Now(), ts2Key, true, scanOpts{})
	settle(t, ui)

	footer := ui.footerLabel.GetText(false)
	assert.Contains(t, footer, "+180 B since "+ts1.Local().Format("01-02"))
	assert.NotContains(t, footer, ts2.Local().Format("01-02"), "the just-saved snapshot is skipped")
}

// TestCompletionGrowthFlashSilentWithoutSameRootHistory: only a different-root
// snapshot exists, whose manifest total is not comparable, so nothing flashes.
func TestCompletionGrowthFlashSilentWithoutSameRootHistory(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "o.parquet", "/other", 20, 40, ts2)
	ui := newLiveUI(t, dir)

	ui.finishRootScan(liveRootTree(), "/root", time.Now(), parquet.SnapshotKey{}, false, scanOpts{})
	settle(t, ui)

	assert.NotContains(t, ui.footerLabel.GetText(false), "{ to compare")
}

// TestCompletionGrowthFlashSilentAtZeroDelta: no growth since the previous
// snapshot means nothing to nudge about.
func TestCompletionGrowthFlashSilentAtZeroDelta(t *testing.T) {
	dir := t.TempDir()
	writeArchiveSnapshot(t, dir, "s.parquet", "/root", 100, 100, ts2) // total 200 == live
	ui := newLiveUI(t, dir)

	ui.finishRootScan(liveRootTree(), "/root", time.Now(), parquet.SnapshotKey{}, false, scanOpts{})
	settle(t, ui)

	assert.NotContains(t, ui.footerLabel.GetText(false), "{ to compare")
}

// snapshotKeyFor resolves the archived snapshot at ts to its identity key, so a
// test can name the "just saved" snapshot the fold rule must skip.
func snapshotKeyFor(t *testing.T, dir string, ts time.Time) parquet.SnapshotKey {
	t.Helper()
	listings, err := report.ListSnapshotsInDir(dir)
	require.NoError(t, err)
	for _, l := range listings {
		if l.ScanTs.Equal(ts) {
			return l.Key()
		}
	}
	t.Fatalf("no snapshot at %s", ts)
	return parquet.SnapshotKey{}
}
