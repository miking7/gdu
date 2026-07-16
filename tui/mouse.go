package tui

import (
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func (ui *UI) onMouse(event *tcell.EventMouse, action tview.MouseAction) (*tcell.EventMouse, tview.MouseAction) {
	if event == nil {
		return nil, action
	}

	if ev, ac, handled := ui.launcherMouse(event, action); handled {
		return ev, ac
	}
	if ui.pages.HasPage("confirm") ||
		ui.pages.HasPage(scanQuitPage) ||
		ui.pages.HasPage(autoCompactQuitPage) ||
		ui.pages.HasPage("deleting") ||
		ui.pages.HasPage("emptying") ||
		ui.pages.HasPage("help") ||
		ui.pages.HasPage("snapshotpicker") {
		return nil, action
	}
	// Block table mouse actions while a scan's progress or a snapshot load is
	// what the user sees (visibility, not existence: a scan whose progress the
	// user stepped away from must not block browsing the past).
	if front, _ := ui.pages.GetFrontPage(); front == scanProgressPage || front == loadingPage {
		return nil, action
	}

	// nolint: exhaustive // Why: we don't need to handle all mouse events
	switch action {
	case tview.MouseLeftDoubleClick:
		row, column := ui.table.GetSelection()
		if ui.currentDirPath != ui.topDirPath && row == 0 {
			ui.handleLeft()
		} else {
			// Removed-item rows in diff mode carry no reference; ignore clicks on them.
			selectedFile, ok := ui.table.GetCell(row, column).GetReference().(fs.Item)
			if !ok {
				return nil, action
			}
			if selectedFile.IsDir() {
				ui.handleRight()
			} else {
				if ui.noViewFile {
					ui.headerNoticeNow("Viewing files is disabled!")
					return nil, action
				}
				ui.showFile()
			}
		}
		return nil, action
	case tview.MouseScrollUp, tview.MouseScrollDown:
		row, column := ui.table.GetSelection()
		if action == tview.MouseScrollUp && row > 0 {
			row--
		} else if action == tview.MouseScrollDown && row+1 < ui.table.GetRowCount() {
			row++
		}
		ui.table.Select(row, column)
		return nil, action
	}

	return event, action
}
