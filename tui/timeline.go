package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/report"
)

// Time stepping: `[` walks the timeline older, `]` newer. The timeline is the
// dated sequence of archived snapshots whose root covers the current folder,
// with the live disk as its newest point.

// Deliberate user-facing copy — reword with care.
const (
	noCoveringNotice   = "no snapshots of this folder — O to see the archive"
	oldestNotice       = "already at the oldest snapshot"
	scanCompleteNotice = "scan complete — ] to view"

	// braceDuringScanNotice is flashed when { / } are pressed at the live position
	// of a running scan (the progress screen or its preview): there is no complete
	// tree to compare against yet, so they teach that Δ becomes available on
	// completion. Arming the comparison from here is future work.
	braceDuringScanNotice = "scan running — Δ available when it completes"

	// baselineClearedFlash announces a { / } step that walked the baseline back
	// onto the viewed position, or off the newest end — the "walk the comparison
	// back to nothing" gesture that mirrors { entering it. viewingBaselineFlash
	// announces a [ / ] step that landed the view on the baseline snapshot itself,
	// where the diff is honestly all-unchanged.
	baselineClearedFlash = " baseline cleared"
	viewingBaselineFlash = " viewing the baseline snapshot"

	// scanProgressPage is the scan's progress page — the timeline's live
	// position while a scan runs. loadingPage is the separate page
	// picker and step loads put up, so they never collide with a running
	// scan's page or its ticker.
	scanProgressPage = "progress"
	loadingPage      = "loading"

	// scanningIndicatorText is shown at the right edge of the footer while a
	// scan runs and the user browses the past.
	scanningIndicatorText = " scanning… "
)

// resetTimeline forgets the pinned timeline and any in-flight step load, so
// the next step re-derives membership from the archive. Called when the View
// changes by means other than stepping (Esc return, O jump, go-live).
func (ui *UI) resetTimeline() {
	ui.stepGen++
	ui.timelineActive = false
	ui.timelineEntries = nil
	ui.timelineRoot = ""
	ui.stepLoading = false
	ui.baseStepTarget = -1
	ui.baseStepLoading = false
	ui.pages.RemovePage(loadingPage)
}

// dismissLoadingPage removes the shared loading page unless an async view or
// baseline load still needs it up: a concurrent [ ] and { } load share the one
// page, so the first to land must not pull it out from under the other.
func (ui *UI) dismissLoadingPage() {
	if ui.stepLoading || ui.baseStepLoading {
		return
	}
	ui.pages.RemovePage(loadingPage)
}

// handleStep is the `[` (dir = -1) / `]` (dir = +1) key: begin or continue
// walking the timeline. Must run on the event loop.
func (ui *UI) handleStep(dir int) {
	if ui.snapshotsDir == "" {
		return
	}
	folder := ui.stepFolder()
	if folder == "" {
		return
	}

	// (Re-)derive the timeline when stepping starts, when the user has
	// navigated outside the pinned root's coverage (a new walk contextually),
	// or when the pinned fold decision went stale — a delete or refresh
	// un-folds the just-saved snapshot and the walk must see it.
	if !ui.timelineActive || !report.PathCoveredBy(ui.timelineRoot, folder) || ui.timelineFoldStale() {
		ui.ensureTimelineThen(folder, func() { ui.stepTo(ui.timelinePos + dir) })
		return
	}
	ui.stepTo(ui.stepTarget + dir)
}

// timelineFoldStale reports whether the pinned timeline baked in a fold that
// no longer applies (the live tree diverged, or was replaced, since the pin).
func (ui *UI) timelineFoldStale() bool {
	return ui.timelineFolded &&
		(!ui.liveSavedValid || ui.liveDiverged.Load() || ui.scanning)
}

// stepFolder is the folder whose history a step walks: the current folder, or
// the scan root while the startup scan's progress screen is still up.
func (ui *UI) stepFolder() string {
	if ui.currentDir != nil {
		return ui.currentDirPath
	}
	if ui.scanning {
		return ui.scanningRoot
	}
	return ""
}

// ensureTimelineThen makes sure a timeline covering folder is pinned, then runs
// then() on the event loop. It lists the archive off the event loop and pins
// (membership: snapshots whose root covers folder; the pin goes to the current
// snapshot View's root when it is one of them, else to the deepest covering
// root). then() does not run when there is no walkable covering history — the
// no-coverage notice shows instead. Shared by the view walk ([ ]) and the
// baseline walk ({ }), so both derive the same pinned timeline.
func (ui *UI) ensureTimelineThen(folder string, then func()) {
	gen := ui.stepGen
	devices, getter := ui.devices, ui.getter // captured on the loop for off-loop mount resolution
	ui.showLoadingPage("Reading snapshots...", " Snapshots ")
	ui.goPickerWork(func() {
		covering, err := ui.coveringForTarget(folder, devices, getter)
		ui.app.QueueUpdateDraw(func() {
			ui.dismissLoadingPage()
			if gen != ui.stepGen {
				return // superseded (Esc, O, a new scan) while listing
			}
			if err != nil {
				ui.showErr("Error reading snapshot archive", err)
				return
			}
			if len(covering) == 0 {
				ui.headerNoticeNow(noCoveringNotice)
				return
			}
			ui.pinTimeline(covering)
			if len(ui.timelineEntries) == 0 {
				// Everything covering folded into the live position (a
				// first-ever scan): there is no past to step into yet.
				ui.timelineActive = false
				ui.headerNoticeNow(noCoveringNotice)
				return
			}
			then()
		})
	})
}

// pinTimeline builds the walkable timeline from the covering listings and
// positions the cursor: on the current snapshot View when it is a member, else
// at the live end. The just-saved snapshot of the current live tree folds into
// the live position unless the live tree has diverged since the save.
// The pin prefers the current snapshot View's root, else the deepest covering
// root that still has walkable entries after the fold — so a first-ever scan
// of a folder doesn't shadow an older whole-disk timeline.
func (ui *UI) pinTimeline(covering []report.SnapshotListing) {
	root := ""
	if v := ui.currentView; v != nil && v.snapshot != nil {
		key := v.snapshot.Key()
		for i := range covering {
			if covering[i].Key() == key {
				root = covering[i].ScanRoot
				break
			}
		}
	}
	var entries []report.SnapshotListing
	folded := false
	if root != "" {
		entries, folded = ui.foldedEntries(covering, root)
	} else {
		for _, candidate := range coveringRootsByDepth(covering) {
			entries, folded = ui.foldedEntries(covering, candidate)
			root = candidate
			if len(entries) > 0 {
				break
			}
		}
	}

	ui.timelineRoot = root
	ui.timelineEntries = entries
	ui.timelineFolded = folded
	ui.timelineActive = true

	ui.timelinePos = len(entries) // the live end
	if v := ui.currentView; v != nil && v.snapshot != nil {
		key := v.snapshot.Key()
		for i := range entries {
			if entries[i].Key() == key {
				ui.timelinePos = i
				break
			}
		}
	}
	ui.stepTarget = ui.timelinePos
}

// foldedEntries returns root's timeline (oldest → newest, from the
// newest-first covering list) with the fold rule applied: identical data
// is one timeline point, not two — when the newest entry is the snapshot
// saved from the still-unchanged live tree, the live position represents it.
// While a scan runs, the live position is the in-progress scan, not the old
// tree, so nothing folds. folded reports whether an entry was dropped.
func (ui *UI) foldedEntries(covering []report.SnapshotListing, root string) (entries []report.SnapshotListing, folded bool) {
	for i := len(covering) - 1; i >= 0; i-- {
		if covering[i].ScanRoot == root {
			entries = append(entries, covering[i])
		}
	}
	if n := len(entries); n > 0 && !ui.scanning && ui.liveView != nil &&
		ui.liveSavedValid && !ui.liveDiverged.Load() &&
		entries[n-1].Key() == ui.liveSavedKey {
		return entries[:n-1], true
	}
	return entries, false
}

// coveringRootsByDepth returns the distinct covering roots, deepest first.
func coveringRootsByDepth(covering []report.SnapshotListing) []string {
	seen := make(map[string]struct{}, len(covering))
	var roots []string
	for i := range covering {
		if _, dup := seen[covering[i].ScanRoot]; !dup {
			seen[covering[i].ScanRoot] = struct{}{}
			roots = append(roots, covering[i].ScanRoot)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	return roots
}

// stepTo moves the walk's target position and loads it (serialized: one load
// at a time; further presses retarget the chain).
func (ui *UI) stepTo(target int) {
	entries := ui.timelineEntries
	if target < 0 {
		ui.headerNoticeNow(oldestNotice)
		target = 0
	}
	if target > len(entries) {
		target = len(entries)
	}
	if target == len(entries) && ui.timelinePos == len(entries) && !ui.stepLoading &&
		!ui.scanning && ui.currentView != nil && ui.currentView == ui.liveView {
		// Already showing the live view; nothing newer. Any other view parked
		// at the live-end position (an off-timeline import, or a live tree
		// replaced by a completed scan) must still reach stepToLiveEnd for the
		// switch-or-offer.
		return
	}
	ui.stepTarget = target
	if ui.stepLoading {
		return // the in-flight load chains to the new target when it lands
	}
	ui.advanceStep()
}

// advanceStep loads or switches to the current step target. Runs on the event
// loop; re-invoked by the loader until the shown position matches the target.
func (ui *UI) advanceStep() {
	target := ui.stepTarget
	if target == ui.timelinePos && target < len(ui.timelineEntries) {
		return // already showing it
	}
	if target == len(ui.timelineEntries) {
		ui.stepToLiveEnd()
		return
	}
	ui.startStepLoad(target)
}

// stepToLiveEnd is `]` at the newest snapshot: while a scan is running
// the live position is its progress screen (never the stale pre-scan tree,
// which the scan goroutine may be mutating); else switch to the in-memory
// live tree when it covers the folder (instant); otherwise offer a rescan,
// explicit and expectation-set.
func (ui *UI) stepToLiveEnd() {
	folder := ui.stepFolder()

	if ui.scanning {
		ui.timelinePos = len(ui.timelineEntries)
		ui.scanPageHidden = false
		ui.pages.ShowPage(scanProgressPage)
		// Back at the live position of a running scan: a set baseline pauses again.
		ui.updateHeader()
		return
	}

	if viewContains(ui.liveView, folder) {
		prevPath, prevSize, hadPrev := ui.shownFolderSample()
		ui.timelinePos = len(ui.timelineEntries)
		shown, _ := ui.applyView(ui.liveView, folder, ui.selectedItemName())
		ui.flashFooter(ui.stepMicroDiff(liveSwitchFooter(ui.liveView.scannedAt), shown, prevPath, prevSize, hadPrev))
		return
	}

	if len(ui.timelineEntries) == 0 {
		return
	}
	newest := ui.timelineEntries[len(ui.timelineEntries)-1]
	age := humanAge(time.Since(newest.ScanTs))
	modal := tview.NewModal().
		SetText(fmt.Sprintf("Newest snapshot (%s old). Rescan live now? This scans %s and may take a while.",
			age, ui.timelineRoot)).
		AddButtons([]string{"Rescan", "Stay"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			ui.pages.RemovePage("confirm")
			if buttonIndex == 0 {
				ui.rescanTimelineRoot()
			} else {
				ui.app.SetFocus(ui.table)
			}
		})
	if !ui.UseColors {
		modal.SetBackgroundColor(tcell.ColorGray)
	} else {
		modal.SetBackgroundColor(tcell.ColorBlack)
	}
	modal.SetBorderColor(tcell.ColorDefault)
	ui.pages.AddPage("confirm", modal, true, true)
}

// rescanTimelineRoot runs the accepted end-of-timeline rescan: a deliberate
// full scan of the pinned root (it saves a snapshot per the tri-state — this
// is a chosen-root scan, not a spot-rescan).
func (ui *UI) rescanTimelineRoot() {
	root := ui.timelineRoot
	ui.Analyzer.ResetProgress()
	ui.linkedItems = make(fs.HardLinkedItems)
	if err := ui.analyzePath(root, nil, scanOpts{keepSelection: ui.selectedItemName()}); err != nil {
		ui.showErr("Error rescanning path", err)
	}
}

// startStepLoad reads the snapshot at target off the event loop, behind the
// loading page, and applies it when it lands (dropping superseded loads).
func (ui *UI) startStepLoad(target int) {
	entry := ui.timelineEntries[target] // copy; the goroutine outlives steps
	gen := ui.stepGen
	ui.stepLoading = true

	wantPath := ui.stepFolder()
	wantSel := ui.selectedItemName()
	prevPath, prevSize, hadPrev := ui.shownFolderSample()

	if front, _ := ui.pages.GetFrontPage(); front == scanProgressPage {
		ui.pages.HidePage(scanProgressPage) // step away from the scan's live position
		ui.scanPageHidden = true
		// Leaving the live position un-pauses a set baseline (the snapshot being
		// loaded is a complete tree the diff can render); refresh the ◇ tail now,
		// before the loading page, so it never lingers paused behind the load.
		ui.updateHeader()
	}
	ui.showLoadingPage("Loading snapshot...", " Snapshots ")

	ui.goPickerWork(func() {
		tree, err := ui.loadListingTree(&entry)
		ui.app.QueueUpdateDraw(func() {
			if gen != ui.stepGen {
				return // superseded; a newer walk owns the screen
			}
			ui.stepLoading = false
			ui.dismissLoadingPage()
			if err != nil {
				ui.showErr("Error loading snapshot", err)
				return
			}

			info := entry.SnapshotInfo
			v := &view{tree: tree, topPath: entry.ScanRoot, snapshot: &info}
			ui.timelinePos = target
			hadBaseline := ui.baseline != nil
			shown, exact := ui.applyView(v, wantPath, wantSel)
			switch {
			case hadBaseline && ui.baseline == nil:
				// applyView cleared the baseline (the step left its coverage) and
				// already flashed why — don't overwrite that with a micro-diff.
			case ui.baseline != nil && entry.Key() == ui.baselineKey:
				// The view landed on the baseline snapshot itself: an honest
				// all-unchanged diff (E5), not a "vs previous" micro-diff.
				ui.flashFooter(viewingBaselineFlash)
			default:
				ui.flashFooter(ui.stepMicroDiff(ui.snapshotFooter(), shown, prevPath, prevSize, hadPrev))
			}
			if !exact {
				ui.headerNoticeNow(fmt.Sprintf("%s did not exist at %s — showing %s",
					wantPath, info.ScanTs.Local().Format(headerTimeLayout), shown))
			}

			if ui.stepTarget != ui.timelinePos {
				ui.advanceStep() // the user kept stepping while this loaded
			}
		})
	})
}

// loadListingTree reads one archived snapshot's tree and prepares it for
// browsing. It performs file I/O and must run off the event loop.
func (ui *UI) loadListingTree(l *report.SnapshotListing) (*analyze.Dir, error) {
	f, err := os.Open(filepath.Join(ui.snapshotsDir, l.File)) //nolint:gosec // archive path, read-only
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tree, err := report.ReadAnalysisSnapshot(f, &l.SnapshotInfo)
	if err != nil {
		return nil, err
	}
	tree.UpdateStats(make(fs.HardLinkedItems))
	return tree, nil
}

// shownFolderSample captures the shown folder's path and size before a step,
// for the footer's transient micro-diff.
func (ui *UI) shownFolderSample() (path string, size int64, ok bool) {
	if ui.currentDir == nil {
		return "", 0, false
	}
	return ui.currentDirPath, ui.currentDir.GetUsage(), true
}

// stepMicroDiff appends the transient "· −4.1 G vs previous" micro-diff to a
// step's footer text when the same folder was shown before the step.
func (ui *UI) stepMicroDiff(base, shownPath, prevPath string, prevSize int64, hadPrev bool) string {
	if !hadPrev || shownPath != prevPath || ui.currentDir == nil {
		return base
	}
	delta := ui.currentDir.GetUsage() - prevSize
	if delta == 0 {
		return base
	}
	sign := "+"
	if delta < 0 {
		sign = minusSign
	}
	return fmt.Sprintf("%s · %s%s vs previous", base, sign, ui.plainSize(absInt64(delta)))
}

// showLoadingPage puts a centered "working" page up while a picker or step
// operation runs off the event loop. Unlike the scan's progress page it owns
// its own TextView, so a background scan's ticker never writes into it.
func (ui *UI) showLoadingPage(text, title string) {
	loading := tview.NewTextView().SetText(text)
	loading.SetBorder(true).SetBorderPadding(2, 2, 2, 2).SetTitle(title)
	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 10, 1, false).
			AddItem(loading, 8, 1, false).
			AddItem(nil, 10, 1, false), 0, 50, false).
		AddItem(nil, 0, 1, false)
	ui.pages.AddPage(loadingPage, flex, true, true)
}

// Baseline stepping: `{` walks the ◇ Baseline older, `}` newer, along the same
// pinned covering timeline the view walks. It is deliberately unlike the
// browser's `{` `}`: the browser's free two-cursor surface skips ◇ over ●, but a
// linear timeline can't skip a point, so ◇ stepping onto ●'s position — or off
// the newest end — clears the comparison, the "walk it back to nothing" gesture
// that mirrors `{` entering one. ◇ never rests on the live end and never equals ●.

// handleBraceKey maps the baseline-stepping keys: `{` older, `}` newer.
func (ui *UI) handleBraceKey(r rune) {
	if r == '{' {
		ui.handleBaselineStep(-1)
	} else {
		ui.handleBaselineStep(1)
	}
}

// handleBaselineStep begins or continues a ◇ walk: it ensures the covering
// timeline is pinned (reusing the view walk's off-loop derivation), then steps
// the baseline. Must run on the event loop.
func (ui *UI) handleBaselineStep(dir int) {
	if ui.snapshotsDir == "" {
		return
	}
	folder := ui.stepFolder()
	if folder == "" {
		return
	}
	// `}` with no baseline points the comparison backward to nothing that exists
	// yet — teach the `{` that enters one rather than silently doing nothing.
	if dir > 0 && ui.baseline == nil {
		ui.headerNoticeNow(noBaselineNotice)
		return
	}
	if !ui.timelineActive || !report.PathCoveredBy(ui.timelineRoot, folder) || ui.timelineFoldStale() {
		ui.ensureTimelineThen(folder, func() { ui.baselineStep(dir) })
		return
	}
	ui.baselineStep(dir)
}

// baselineStep moves the ◇ cursor one step (dir −1 older, +1 newer) from its
// current position and applies the clear rules. Runs on the event loop with a
// pinned timeline.
func (ui *UI) baselineStep(dir int) {
	entries := ui.timelineEntries
	viewPos := ui.effectiveViewPos()
	target := ui.baselinePos(viewPos) + dir

	switch {
	case target < 0:
		ui.headerNoticeNow(oldestNotice) // nothing older than the oldest snapshot
	case target >= len(entries) || target == viewPos:
		// ◇ never rests on the live end or on ●'s position: walking it there
		// clears the comparison (E4, and the `}`-off-the-newest-end gesture).
		ui.clearBaselineByStep()
	default:
		ui.baseStepTo(target)
	}
}

// effectiveViewPos is ●'s timeline index — where it is, or where an in-flight
// `[`/`]` step is heading — so the baseline anchors against the view's
// destination, not a position it is about to leave.
func (ui *UI) effectiveViewPos() int {
	if ui.stepLoading {
		return ui.stepTarget
	}
	return ui.timelinePos
}

// baselinePos is the ◇ cursor's current timeline index. While a `{`/`}` load is
// in flight it is that load's target; with no baseline set it is ●'s position, so
// the first `{` lands on the snapshot immediately before the view (E3, "compare
// vs previous"); otherwise it is the entry matching the baseline's identity, or —
// when the baseline lies off this pinned timeline (set over a different covering
// root) — the insertion point of its timestamp.
func (ui *UI) baselinePos(viewPos int) int {
	switch {
	case ui.baseStepLoading:
		return ui.baseStepTarget
	case ui.baseline == nil:
		return viewPos
	}
	if i := ui.baselineEntryIndex(); i >= 0 {
		return i
	}
	return ui.baselineInsertionPos()
}

// baselineEntryIndex is the pinned timeline index of the entry matching the
// active baseline's identity, or -1 when the baseline is off this timeline.
func (ui *UI) baselineEntryIndex() int {
	for i := range ui.timelineEntries {
		if ui.timelineEntries[i].Key() == ui.baselineKey {
			return i
		}
	}
	return -1
}

// baselineInsertionPos is the index of the first pinned entry strictly newer than
// the current baseline — where a baseline off this timeline sits temporally, so
// `{`/`}` still move it in time order.
func (ui *UI) baselineInsertionPos() int {
	for i := range ui.timelineEntries {
		if ui.timelineEntries[i].ScanTs.After(ui.baselineTs) {
			return i
		}
	}
	return len(ui.timelineEntries)
}

// clearBaselineByStep drops the comparison as the terminal move of a `{`/`}` walk
// and flashes why.
func (ui *UI) clearBaselineByStep() {
	ui.clearBaseline()
	ui.flashFooter(baselineClearedFlash)
}

// baseStepTo moves the ◇ walk's target and loads it, serialized like the view
// walk: one load at a time, further presses retarget the chain.
func (ui *UI) baseStepTo(target int) {
	ui.baseStepTarget = target
	if ui.baseStepLoading {
		return // the in-flight load chains to the new target when it lands
	}
	ui.advanceBaselineStep()
}

// advanceBaselineStep loads the current ◇ target unless the walk was cleared.
func (ui *UI) advanceBaselineStep() {
	if ui.baseStepTarget < 0 {
		return
	}
	ui.startBaselineLoad(ui.baseStepTarget)
}

// startBaselineLoad reads the snapshot at target as the baseline off the event
// loop, behind the loading page, and applies it when it lands (dropping
// superseded or retargeted loads). Mirrors startStepLoad for the ◇ cursor.
func (ui *UI) startBaselineLoad(target int) {
	entry := ui.timelineEntries[target] // copy; the goroutine outlives steps
	gen := ui.stepGen
	ui.baseStepLoading = true
	ui.showLoadingPage("Loading baseline...", " Baseline ")

	ui.goPickerWork(func() {
		b, err := ui.loadBaseline(&entry)
		ui.app.QueueUpdateDraw(func() {
			if gen != ui.stepGen {
				return // superseded; baseStepLoading was cleared by resetTimeline
			}
			ui.baseStepLoading = false
			ui.dismissLoadingPage()
			if err != nil {
				ui.showErr("Error loading baseline", err)
				return
			}
			if ui.baseStepTarget != target {
				ui.advanceBaselineStep() // the user kept stepping while this loaded
				return
			}
			info := entry.SnapshotInfo
			ui.SetBaseline(b, &info)
		})
	})
}
