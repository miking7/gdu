package tui

import (
	"path/filepath"

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

	if ui.pages.HasPage("file") || ui.pages.HasPage("export") || ui.pages.HasPage("snapshotpicker") {
		return key // send event to primitive (the picker/form handles its own keys)
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

	// The scan progress screen is the timeline's live position, and step loads
	// show their own loading page: while either is up, the timeline stays
	// walkable (scan-wait time travel) and everything else stays blocked.
	// The visibility check (front page, not HasPage) matters: a scan whose
	// progress the user has stepped away from must not block browsing the past.
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
	case 'a', 'B', 'c', 'm':
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
	case 'S':
		ui.showSnapshotPicker()
		return nil
	case 'O':
		ui.showOpenPicker()
		return nil
	case '[', ']':
		ui.handleStepKey(key.Rune())
		return nil
	case '>':
		if ui.inDiffMode() {
			ui.setDiffSort(false)
			return nil
		}
	case '<':
		if ui.inDiffMode() {
			ui.setDiffSort(true)
			return nil
		}
	}
	return key
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
	if (key.Rune() == '[' || key.Rune() == ']') && (ui.scanning || ui.timelineActive) {
		ui.handleStepKey(key.Rune())
		return nil
	}
	// On the scan's progress screen Esc backs out of a recording scan, raising
	// the same quit-without-saving confirmation as 'q'. This is the layered
	// Esc promise at the live position: the layer to peel here is the
	// in-flight scan. Guarded on scanIsRecording, not merely ui.scanning, so Esc
	// never hard-quits a transient refresh (which 'q' would) — Esc must not cause
	// an unconfirmed exit. (Stepped into the past mid-scan, the front page is a
	// snapshot View, not this one, so that Esc still returns to live progress.)
	if key.Key() == tcell.KeyEsc && ui.scanIsRecording() {
		ui.quitApp(false)
		return nil
	}
	return key
}

func (ui *UI) handleToggles(key *tcell.EventKey) {
	switch key.Rune() {
	case 'a':
		ui.ShowApparentSize = !ui.ShowApparentSize
	case 'B':
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
		if ui.devices != nil {
			ui.currentDir = nil
			if err := ui.ListDevices(ui.getter); err != nil {
				ui.showErr("Error listing devices", err)
			}
		} else if ui.browseParentDirs {
			ui.analyzeParentOfTopDir()
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
