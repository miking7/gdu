package tui

import (
	"path/filepath"
	"time"

	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

var analyzeParentPath = func(ui *UI, path string, parentDir fs.Item) error {
	return ui.AnalyzePath(path, parentDir)
}

func (ui *UI) keyPressed(key *tcell.EventKey) *tcell.EventKey {
	if ui.handleCtrlZ(key) == nil {
		return nil
	}

	if ui.pages.HasPage("file") || ui.pages.HasPage("export") || ui.pages.HasPage("snapshotpicker") ||
		ui.pages.HasPage(launcherPage) {
		return key // send event to primitive (the launcher/picker/form handles its own keys)
	}
	if ui.filtering || ui.typeFiltering {
		return key
	}

	key = ui.handleClosingModals(key)
	if key == nil {
		return nil
	}
	key = ui.handleInfoPageEvents(key)
	if key == nil {
		return nil
	}
	key = ui.handleQuit(key)
	if key == nil {
		return nil
	}

	if ui.pages.HasPage("confirm") ||
		ui.pages.HasPage(scanQuitPage) ||
		ui.pages.HasPage(autoCompactQuitPage) {
		return ui.handleConfirmation(key)
	}

	// A mid-scan preview is a read-only page over the partial live tree; while
	// it is up, its own key handler owns input (navigation/sorting only).
	if ui.previewing {
		return ui.handlePreviewKeys(key)
	}

	// The scan progress screen is the timeline's live position, and step loads
	// show their own loading page: while either is up, the timeline stays
	// walkable (scan-wait time travel) and everything else stays blocked.
	// The visibility check (front page, not HasPage) matters: a scan whose
	// progress the user has stepped away from must not block browsing the past.
	// Tab-to-preview and the mid-scan Esc rule live in handleLoadingPageKeys.
	if front, _ := ui.pages.GetFrontPage(); front == scanProgressPage || front == loadingPage {
		return ui.handleLoadingPageKeys(key)
	}

	if ui.pages.HasPage("deleting") ||
		ui.pages.HasPage("emptying") {
		return key
	}

	key = ui.handleHelp(key)
	if key == nil {
		return nil
	}

	if ui.pages.HasPage("help") {
		return key
	}

	key = ui.handleShell(key)
	if key == nil {
		return nil
	}

	key = ui.handleLeftRight(key)
	if key == nil {
		return nil
	}

	key = ui.handleFiltering(key)
	if key == nil {
		return nil
	}

	return ui.handleMainActions(key)
}

func (ui *UI) handleClosingModals(key *tcell.EventKey) *tcell.EventKey {
	if key.Key() == tcell.KeyEsc || key.Rune() == 'q' {
		if ui.pages.HasPage("help") {
			ui.pages.RemovePage("help")
			ui.app.SetFocus(ui.table)
			return nil
		}
		if ui.pages.HasPage("info") {
			ui.pages.RemovePage("info")
			ui.app.SetFocus(ui.table)
			return nil
		}
	}
	return key
}

func (ui *UI) handleConfirmation(key *tcell.EventKey) *tcell.EventKey {
	if key.Rune() == 'h' {
		return tcell.NewEventKey(tcell.KeyLeft, 0, 0)
	}
	if key.Rune() == 'l' {
		return tcell.NewEventKey(tcell.KeyRight, 0, 0)
	}
	return key
}

func (ui *UI) handleInfoPageEvents(key *tcell.EventKey) *tcell.EventKey {
	if ui.pages.HasPage("info") {
		switch key.Rune() {
		case 'i':
			ui.pages.RemovePage("info")
			ui.app.SetFocus(ui.table)
			return nil
		case '?':
			return nil
		}

		if key.Key() == tcell.KeyUp ||
			key.Key() == tcell.KeyDown ||
			key.Rune() == 'j' ||
			key.Rune() == 'k' {
			row, column := ui.table.GetSelection()
			if (key.Key() == tcell.KeyUp || key.Rune() == 'k') && row > 0 {
				row--
			} else if (key.Key() == tcell.KeyDown || key.Rune() == 'j') &&
				row+1 < ui.table.GetRowCount() {
				row++
			}
			ui.table.Select(row, column)
		}
		ui.showInfo() // refresh file info after any change
	}
	return key
}

// handle ctrl+z job control
func (ui *UI) handleCtrlZ(key *tcell.EventKey) *tcell.EventKey {
	if key.Key() == tcell.KeyCtrlZ {
		ui.app.Suspend(func() {
			termApp := ui.app.(*tview.Application)
			termApp.Lock()
			defer termApp.Unlock()

			err := stopProcess()
			if err != nil {
				ui.showErr("Error sending STOP signal", err)
			}
		})
		return nil
	}

	return key
}

// confirmQuitMinScanDuration is the scan time above which quitting asks for
// confirmation, so a long scan is not lost by an accidental key press.
const confirmQuitMinScanDuration = 3 * time.Second

func (ui *UI) handleQuit(key *tcell.EventKey) *tcell.EventKey {
	clearTerminalProgress()

	switch key.Rune() {
	case 'Q':
		ui.quitApp(true)
		return nil
	case 'q':
		ui.quitApp(false)
		return nil
	}
	return key
}

// enterPreview switches from the scanning progress modal to a read-only,
// point-in-time view of the directory tree discovered so far. The view does not
// auto-refresh: pressing Tab again returns to the progress modal, and entering
// the preview once more takes a fresh snapshot.
func (ui *UI) enterPreview() {
	analyzer, ok := ui.Analyzer.(interface{ GetCurrentDir() fs.Item })
	if !ok {
		return
	}
	root := analyzer.GetCurrentDir()
	if root == nil {
		return // nothing scanned yet
	}

	// compute aggregated sizes on the partial tree using a throwaway hard-link
	// map so the running scan's accounting is left untouched
	root.UpdateStats(make(fs.HardLinkedItems))

	ui.previewing = true
	ui.previewSavedDir = ui.currentDir
	ui.currentDir = root
	ui.markedRows = make(map[int]struct{})
	ui.ignoredRows = make(map[int]struct{})
	ui.pages.RemovePage(scanProgressPage)
	ui.showDir()
	ui.table.Select(0, 0)
	ui.app.SetFocus(ui.table)
}

// exitPreview leaves the mid-scan preview and restores the scanning progress
// modal. The progress page is only re-shown while a scan is still running: a
// scan that completed during the preview already cleared previewing and
// rendered the final tree, so re-adding a stale progress page here must not
// happen.
func (ui *UI) exitPreview() {
	ui.previewing = false
	ui.currentDir = ui.previewSavedDir
	ui.previewSavedDir = nil
	if ui.scanning && ui.progressFlex != nil {
		ui.pages.AddPage(scanProgressPage, ui.progressFlex, true, true)
	}
	ui.app.SetFocus(ui.table)
}

// handlePreviewKeys handles input while a mid-scan preview is shown. Navigation
// and sorting are allowed; destructive or external actions are intentionally
// ignored because the tree is still being built.
func (ui *UI) handlePreviewKeys(key *tcell.EventKey) *tcell.EventKey {
	if ui.pages.HasPage("help") {
		return key
	}

	if key.Key() == tcell.KeyTab || key.Key() == tcell.KeyEsc {
		ui.exitPreview()
		return nil
	}
	if key.Key() == tcell.KeyLeft {
		ui.previewLeft()
		return nil
	}
	if key.Key() == tcell.KeyRight {
		ui.handleRight()
		return nil
	}

	switch key.Rune() {
	case 'q':
		// Deliberate fork divergence from upstream, which leaves q dead in the
		// preview: route through the quit chain, which already confirms an
		// in-flight recording scan before abandoning it.
		ui.quitApp(false)
		return nil
	case 'Q':
		ui.quitApp(true)
		return nil
	case '[', ']':
		// The timeline is reachable from a preview: leave the preview first, then
		// step. Stepping forward to live lands on the progress page — a preview is
		// point-in-time by design and never auto-re-previews.
		ui.exitPreview()
		ui.handleStepKey(key.Rune())
		return nil
	case '?':
		ui.showHelp()
		return nil
	case 'h':
		ui.previewLeft()
		return nil
	case 'l':
		ui.handleRight()
		return nil
	case 's', 'C', 'n', 'M':
		ui.handleSorting(key)
		return nil
	case 'a', '%', 'c', 'm':
		ui.handleToggles(key)
		return nil
	}

	// up/down/pgup/pgdn and Enter are handled by the table itself
	return key
}

// previewLeft navigates to the parent dir within the preview, or leaves the
// preview when already at its root.
func (ui *UI) previewLeft() {
	if ui.currentDir == nil || ui.currentDir.GetParent() == nil {
		ui.exitPreview()
		return
	}
	ui.fileItemSelected(0, 0)
}

func (ui *UI) handleHelp(key *tcell.EventKey) *tcell.EventKey {
	if key.Rune() == '?' {
		if ui.pages.HasPage("help") {
			ui.pages.RemovePage("help")
			ui.app.SetFocus(ui.table)
			return nil
		}
		ui.showHelp()
		return nil
	}
	return key
}

func (ui *UI) handleShell(key *tcell.EventKey) *tcell.EventKey {
	if key.Rune() == 'b' {
		if ui.isInArchive() {
			ui.showErr("Spawning shell is not supported in archives", nil)
			return nil
		}
		if ui.noSpawnShell {
			ui.headerNoticeNow("Shell spawning is disabled!")
			return nil
		}
		ui.spawnShell()
		return nil
	}
	return key
}

func (ui *UI) handleLeftRight(key *tcell.EventKey) *tcell.EventKey {
	if key.Rune() == 'h' || key.Key() == tcell.KeyLeft {
		ui.handleLeft()
		return nil
	}

	if key.Rune() == 'l' || key.Key() == tcell.KeyRight {
		ui.handleRight()
		return nil
	}
	return key
}

func (ui *UI) handleFiltering(key *tcell.EventKey) *tcell.EventKey {
	if key.Key() != tcell.KeyTab {
		return key
	}
	if ui.filteringInput != nil {
		ui.filtering = true
		ui.app.SetFocus(ui.filteringInput)
		return nil
	}
	if ui.typeFilteringInput != nil {
		ui.typeFiltering = true
		ui.app.SetFocus(ui.typeFilteringInput)
		return nil
	}
	return key
}

//nolint:funlen,gocyclo // Why: there's a lot of options to handle
func (ui *UI) handleMainActions(key *tcell.EventKey) *tcell.EventKey {
	// Esc is layered: modal/help/info dismissal is consumed earlier in
	// keyPressed; here it first clears a set Baseline, then returns to the
	// session's return view. Esc never scans.
	if key.Key() == tcell.KeyEsc {
		if ui.handleEscape() {
			return nil
		}
		return key
	}
	// Tab is the tree screen's counterpart toggle: plain ↔ Δ. The filter bar
	// owns Tab when open (E11) — handleFiltering runs earlier and consumes it —
	// so reaching here means no filter bar, and Tab flips the compare rendering.
	if key.Key() == tcell.KeyTab {
		ui.handleTabToggle()
		return nil
	}
	switch key.Rune() {
	case 'd', 'e':
		if ui.isInArchive() {
			ui.showErr("Deletion is not supported in archives", nil)
			return nil
		}
		ui.handleDelete(key.Rune() == 'e')
	case 'v':
		if ui.isInArchive() {
			ui.showErr("Viewing content is not supported in archives", nil)
			return nil
		}
		if ui.noViewFile {
			ui.headerNoticeNow("Viewing files is disabled!")
			return nil
		}
		ui.showFile()
	case 'o':
		if ui.noSpawnShell {
			ui.headerNoticeNow("Opening items is disabled!")
			return nil
		}
		ui.openItem()
	case 'i':
		ui.showInfo()
	case 'a', '%', 'c', 'm':
		ui.handleToggles(key)
	case 'r':
		if ui.currentDir != nil {
			ui.rescanDir()
		}
	case 'E':
		ui.confirmExport()
		return nil
	case 's', 'C', 'n', 'M':
		ui.handleSorting(key)
	case '/':
		ui.showFilterInput()
		return nil
	case 'T':
		ui.showTypeFilterInput()
		return nil
	case ' ':
		ui.handleMark()
	case 'p':
		ui.printMarked()
		return nil
	case 'I':
		ui.ignoreItem()
	case 'O':
		ui.showSnapshotBrowser(focusViewing)
		return nil
	case 'B':
		ui.showSnapshotBrowser(focusBaseline)
		return nil
	case '[', ']':
		ui.handleStepKey(key.Rune())
		return nil
	case 'D':
		// D joins s/n/C/M as a sort key, but only the compare view has a Δ to
		// sort by; elsewhere it teaches the way into a comparison.
		if ui.renderingDelta() {
			ui.setSorting(deltaSortKey)
		} else {
			ui.headerNoticeNow(ui.noDeltaSortNotice())
		}
		return nil
	}
	return key
}

// Teach-flash copy for the compare gestures when there is nothing to compare.
// It names the key that opens the browser on its baseline door (B); this is
// transitional copy, expected to be rewritten when the baseline-stepping keys
// are added.
const noBaselineNotice = "no baseline set — B to compare"

// handleTabToggle flips the compare view's Δ rendering on and off — the tree
// screen's counterpart pair. With no baseline set there is nothing to compare,
// so it teaches the key that sets one. The filter bar owns Tab while it is open;
// that precedence is handled upstream in handleFiltering, which runs first.
func (ui *UI) handleTabToggle() {
	if !ui.inDiffMode() {
		ui.headerNoticeNow(noBaselineNotice)
		return
	}
	// The plain and compare renderings order rows differently (their own sort,
	// and compare interleaves reference-less removed rows), so the index-keyed
	// mark/ignore maps must reset across the flip — the same invariant every
	// re-sort upholds. Otherwise a mark would silently move to another item.
	sel := ui.selectedItemName()
	ui.diffHidden = !ui.diffHidden
	ui.resetRowSelection()
	ui.updateHeader()
	if ui.currentDir != nil {
		ui.showDir()
		if sel != "" {
			ui.selectItemByName(sel)
		}
	}
}

// noDeltaSortNotice explains why D did nothing: either no baseline is set, or
// one is set but the Δ column is toggled off (so there is no visible Δ to sort).
func (ui *UI) noDeltaSortNotice() string {
	if ui.inDiffMode() {
		return "Δ hidden — Tab to compare"
	}
	return noBaselineNotice
}

// handleStepKey maps the timeline keys to steps: `[` older, `]` newer.
func (ui *UI) handleStepKey(r rune) {
	if r == '[' {
		ui.handleStep(-1)
	} else {
		ui.handleStep(1)
	}
}

// handleLoadingPageKeys keeps the timeline walkable while the scan progress
// screen or a step-load page is what the user sees; every other key
// passes through to the page.
func (ui *UI) handleLoadingPageKeys(key *tcell.EventKey) *tcell.EventKey {
	front, _ := ui.pages.GetFrontPage()
	// Tab peeks at the partial tree found so far, but only on the live scan
	// progress screen — never on a step-load's loading page, and never during a
	// -f read (scanning is false there).
	if key.Key() == tcell.KeyTab && front == scanProgressPage && ui.scanning {
		ui.enterPreview()
		return nil
	}
	if (key.Rune() == '[' || key.Rune() == ']') && (ui.scanning || ui.timelineActive) {
		ui.handleStepKey(key.Rune())
		return nil
	}
	// On the scan's progress screen Esc backs out of a recording scan, raising
	// the same quit-without-saving confirmation as 'q'. This is the layered
	// Esc promise at the live position: the layer to peel here is the
	// in-flight scan. Guarded on scanIsRecording, not merely ui.scanning, so Esc
	// never hard-quits a transient refresh (which 'q' would) — Esc must not cause
	// an unconfirmed exit. Also gated on confirmQuit: with confirmations off Esc
	// does nothing mid-scan rather than silently hard-quitting. (Stepped into the
	// past mid-scan, the front page is a snapshot View, not this one, so that Esc
	// still returns to live progress.)
	if key.Key() == tcell.KeyEsc && ui.scanIsRecording() && ui.confirmQuit {
		ui.quitApp(false)
		return nil
	}
	return key
}

func (ui *UI) handleToggles(key *tcell.EventKey) {
	switch key.Rune() {
	case 'a':
		ui.ShowApparentSize = !ui.ShowApparentSize
	case '%':
		ui.ShowRelativeSize = !ui.ShowRelativeSize
	case 'c':
		ui.showItemCount = !ui.showItemCount
	case 'm':
		ui.showMtime = !ui.showMtime
	}
	if ui.currentDir != nil {
		row, column := ui.table.GetSelection()
		ui.showDir()
		ui.table.Select(row, column)
	}
}

func (ui *UI) handleSorting(key *tcell.EventKey) {
	switch key.Rune() {
	case 's':
		ui.setSorting("size")
	case 'C':
		ui.setSorting("itemCount")
	case 'n':
		ui.setSorting("name")
	case 'M':
		ui.setSorting("mtime")
	}
}

func (ui *UI) handleLeft() {
	if ui.currentDirPath == ui.topDirPath {
		// A snapshot View is all one thing: no device list, no parent scan.
		// O jumps roots; Esc returns.
		if !ui.viewIsLive() || ui.scanning {
			return
		}
		switch {
		case ui.browseParentDirs:
			// browse-parent-dirs is an explicit opt-in to walk above the launch
			// dir, so it wins over the launcher/device-list fallback. (This is a
			// deliberate change from upstream, where the device list took the
			// top-of-tree left-arrow ahead of browse-parent-dirs; the launcher
			// era favors the user's explicit up-navigation choice.)
			ui.analyzeParentOfTopDir()
		case ui.usingLauncher:
			// The launcher is this session's home: left-arrow at the top
			// of a live tree returns there, not to the standalone device list.
			ui.returnToLauncher()
		case ui.devices != nil:
			ui.currentDir = nil
			if err := ui.ListDevices(ui.getter); err != nil {
				ui.showErr("Error listing devices", err)
			}
		}
		return
	}
	if ui.currentDir != nil {
		ui.fileItemSelected(0, 0)
	}
}

func (ui *UI) analyzeParentOfTopDir() {
	if ui.currentDir == nil || ui.isInArchive() || !ui.viewIsLive() || ui.scanning {
		return
	}

	currentPath := ui.currentDir.GetPath()
	parentPath := filepath.Dir(currentPath)
	if parentPath == currentPath {
		return
	}

	ui.Analyzer.ResetProgress()
	ui.linkedItems = make(fs.HardLinkedItems)

	if err := analyzeParentPath(ui, parentPath, nil); err != nil {
		ui.showErr("Error analyzing parent directory", err)
	}
}

func (ui *UI) handleRight() {
	row, column := ui.table.GetSelection()
	if ui.currentDirPath != ui.topDirPath && row == 0 { // do not select /..
		return
	}

	if ui.currentDir != nil {
		ui.fileItemSelected(row, column)
	} else {
		ui.deviceItemSelected(row, column)
	}
}

func (ui *UI) handleDelete(shouldEmpty bool) {
	if ui.currentDir == nil {
		return
	}
	// Mutations require a live View and no scan mutating the
	// tree underneath. Guarded here — at the mutation's entry, not only
	// in the key dispatch — so no future trigger path can bypass it.
	if ui.blockMutation(false) {
		return
	}
	if ui.scanning {
		ui.showScanRunningNotice()
		return
	}
	// do not allow deleting parent dir
	row, column := ui.table.GetSelection()
	selectedFile, ok := ui.table.GetCell(row, column).GetReference().(fs.Item)
	if !ok || selectedFile == ui.currentDir.GetParent() {
		return
	}

	if ui.askBeforeDelete {
		ui.confirmDeletion(shouldEmpty)
	} else {
		ui.delete(shouldEmpty)
	}
}

func (ui *UI) handleMark() {
	if ui.currentDir == nil {
		return
	}
	// do not allow deleting parent dir
	row, column := ui.table.GetSelection()
	selectedFile, ok := ui.table.GetCell(row, column).GetReference().(fs.Item)
	if !ok || selectedFile == ui.currentDir.GetParent() {
		return
	}

	ui.fileItemMarked(row)
}

func (ui *UI) ignoreItem() {
	if ui.currentDir == nil {
		return
	}
	// do not allow ignoring parent dir
	row, column := ui.table.GetSelection()
	selectedFile, ok := ui.table.GetCell(row, column).GetReference().(fs.Item)
	if !ok || selectedFile == ui.currentDir.GetParent() {
		return
	}

	if _, ok := ui.ignoredRows[row]; ok {
		delete(ui.ignoredRows, row)
	} else {
		ui.ignoredRows[row] = struct{}{}
	}
	ui.showDir()
	// select next row if possible
	ui.table.Select(min(row+1, ui.table.GetRowCount()-1), 0)
}
