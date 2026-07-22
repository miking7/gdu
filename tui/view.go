package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
)

// view is one tree the UI can show: the live disk or one snapshot. Every gdu
// screen shows a View, optionally against a Baseline; only a live View may be
// mutated.
type view struct {
	tree    fs.Item
	topPath string
	// snapshot is the identity of a snapshot View (from the archive or a
	// Parquet -f file). nil for live Views and identity-less imports.
	snapshot *parquet.SnapshotInfo
	// importLabel marks an identity-less non-live View (JSON import, stdin,
	// stored database) with the name shown in the header.
	importLabel string
	// scannedAt is when a live View's scan completed (for "scanned 14:02").
	scannedAt time.Time
}

// isLive reports whether v is the live disk (mutations allowed).
func (v *view) isLive() bool {
	return v.snapshot == nil && v.importLabel == ""
}

// viewIsLive reports whether the *current* View is the live disk. A UI that
// predates the view model (device list, tests driving currentDir directly)
// counts as live — the read-only rule guards snapshot Views, not legacy state.
func (ui *UI) viewIsLive() bool {
	return ui.currentView == nil || ui.currentView.isLive()
}

// applyView switches the UI to v, preserving wantPath (falling back to the
// nearest existing ancestor) and re-selecting wantSel by name, best-effort.
// It returns the path actually shown and whether it matched wantPath exactly.
// Marked and ignored rows are index-bound to the old tree and always reset.
func (ui *UI) applyView(v *view, wantPath, wantSel string) (shownPath string, exact bool) {
	dir, exact := descendToPath(v.tree, v.topPath, wantPath)

	ui.currentView = v
	if v.isLive() {
		ui.liveView = v
	}
	ui.topDir = v.tree
	ui.topDirPath = v.topPath
	ui.currentDir = dir
	ui.markedRows = make(map[int]struct{})
	ui.ignoredRows = make(map[int]struct{})

	ui.showDir()
	if wantSel != "" {
		ui.selectItemByName(wantSel)
	}
	ui.updateHeader()
	return dir.GetPath(), exact
}

// descendToPath walks from root (at rootPath) down to want, returning the
// deepest existing directory on that path. exact is false when want lies
// outside the tree or some component is missing — the nearest ancestor (or the
// root itself) is returned instead.
func descendToPath(root fs.Item, rootPath, want string) (dir fs.Item, exact bool) {
	if want == rootPath || want == "" {
		return root, true
	}
	if !report.PathCoveredBy(rootPath, want) {
		return root, false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(want, rootPath), string(filepath.Separator))
	cur := root
	for _, comp := range strings.Split(rel, string(filepath.Separator)) {
		next := findDirChild(cur, comp)
		if next == nil {
			return cur, false
		}
		cur = next
	}
	return cur, true
}

// viewContains reports whether v's in-memory tree actually holds folder as a
// directory node — the ground-truth "covers" test for a live tree. A
// "/"-rooted live scan that ignored nested mounts does not contain a folder on
// another volume, so we ask the tree (via the applyView descend) rather than
// path arithmetic, which would wrongly claim coverage. nil / empty-folder views
// don't contain anything specific.
func viewContains(v *view, folder string) bool {
	if v == nil || folder == "" {
		return false
	}
	_, exact := descendToPath(v.tree, v.topPath, folder)
	return exact
}

// findDirChild returns dir's immediate subdirectory named name, or nil.
func findDirChild(dir fs.Item, name string) fs.Item {
	unlock := dir.RLock()
	defer unlock()
	for item := range dir.GetFiles(fs.SortByNone, fs.SortAsc) {
		if item.IsDir() && item.GetName() == name {
			return item
		}
	}
	return nil
}

// selectItemByName moves the table cursor to the row referencing an item with
// the given name, if one is visible.
func (ui *UI) selectItemByName(name string) {
	for row := 0; row < ui.table.GetRowCount(); row++ {
		cell := ui.table.GetCell(row, 0)
		if cell == nil || cell.GetReference() == nil {
			continue
		}
		if item, ok := cell.GetReference().(fs.Item); ok && item.GetName() == name {
			ui.table.Select(row, 0)
			return
		}
	}
}

// selectItemByReference moves the table cursor to the row whose cell references
// item (by pointer identity), if one is visible. Identity is exact and
// order-independent, so it works across the plain and compare renderings even
// though they order rows differently and compare interleaves reference-less
// removed rows.
func (ui *UI) selectItemByReference(item fs.Item) {
	for row := 0; row < ui.table.GetRowCount(); row++ {
		cell := ui.table.GetCell(row, 0)
		if cell == nil {
			continue
		}
		if ref, ok := cell.GetReference().(fs.Item); ok && ref == item {
			ui.table.Select(row, 0)
			return
		}
	}
}

// selectedItemName returns the name of the item under the cursor, or "".
func (ui *UI) selectedItemName() string {
	row, column := ui.table.GetSelection()
	cell := ui.table.GetCell(row, column)
	if cell == nil || cell.GetReference() == nil {
		return ""
	}
	if item, ok := cell.GetReference().(fs.Item); ok {
		return item.GetName()
	}
	return ""
}

// handleEscape implements the layered Esc promise: modal-close and
// help/info dismissal are handled earlier in keyPressed; here Esc first clears
// a set Baseline, then returns to the session's return view. Esc never scans.
// It returns false when there was nothing left for Esc to do.
func (ui *UI) handleEscape() bool {
	if ui.inDiffMode() {
		ui.clearBaseline()
		return true
	}
	if ui.returnView != nil && ui.currentView != ui.returnView {
		ui.returnToReturnView()
		return true
	}
	return false
}

// returnToReturnView switches back to the View the session was launched into,
// instantly — the tree is already in memory. Time-stepping state is left
// behind (a later step re-derives the timeline). While a scan runs, a live
// return view is represented by the scan's progress screen — the tree
// is being built or grafted and must not be browsed mid-scan.
func (ui *UI) returnToReturnView() {
	ui.resetTimeline()
	if ui.scanning && ui.returnView.isLive() {
		ui.scanPageHidden = false
		ui.pages.ShowPage(scanProgressPage)
		return
	}
	ui.applyView(ui.returnView, ui.currentDirPath, ui.selectedItemName())
}

// Read-only Views & the go-live flow.

// Deliberate copy: the second line of the d/e signpost dialog.
const signpostDeleteLine = "gdu deletes from the live disk, and this listing may no longer match it."

// signpostRefreshLine is the second line when `r` is pressed in a snapshot
// View — refresh reads the live disk, so it signposts the same way as d/e.
const signpostRefreshLine = "Refreshing reads the live disk — go live here to see current data."

// blockMutation intercepts d/e/r on a non-live View with the go-live signpost
// dialog. It returns true when the action was blocked.
func (ui *UI) blockMutation(refresh bool) bool {
	if ui.viewIsLive() {
		return false
	}
	secondLine := signpostDeleteLine
	if refresh {
		secondLine = signpostRefreshLine
	}
	ui.showGoLiveSignpost(fmt.Sprintf("%s\n%s", ui.readOnlyStatement(), secondLine))
	return true
}

// readOnlyStatement renders the first line of the signpost dialog for the
// current (non-live) View.
func (ui *UI) readOnlyStatement() string {
	v := ui.currentView
	if v.snapshot != nil {
		return fmt.Sprintf("Viewing a snapshot from %s (%s ago) — read-only.",
			v.snapshot.ScanTs.Local().Format(headerDateLayout), humanAge(time.Since(v.snapshot.ScanTs)))
	}
	return fmt.Sprintf("Viewing an import (%s) — read-only.", v.importLabel)
}

// showGoLiveSignpost shows the read-only signpost dialog: not a confirm — its
// primary action is going live here.
func (ui *UI) showGoLiveSignpost(text string) {
	modal := tview.NewModal().
		SetText(text).
		AddButtons([]string{"Go live here", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			ui.pages.RemovePage("confirm")
			if buttonIndex == 0 {
				ui.goLiveHere()
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

// goLiveHere reaches the live disk from a snapshot View: switch to the
// in-memory live tree when it covers the current folder (instant, cursor
// kept); otherwise offer a scoped spot-rescan of just this folder.
func (ui *UI) goLiveHere() {
	ui.goLiveHereThen(nil)
}

// goLiveHereThen is goLiveHere with a continuation run on the event loop right
// after the instant switch — the browser uses it to set a pending baseline on
// the freshly live tree. The spot-rescan branch does not run it: applying a
// baseline against a tree that is still being scanned would render a diff of a
// partial tree, so a go-live that must rescan drops the pending baseline (the
// user can set one once the scan completes). While a scan runs there is no live
// tree to stand on — the in-memory one is being built or grafted — so the flow
// waits (no concurrent scans).
func (ui *UI) goLiveHereThen(then func()) {
	folder := ui.currentDirPath
	sel := ui.selectedItemName()

	if ui.scanning {
		ui.showScanRunningNotice()
		return
	}

	if viewContains(ui.liveView, folder) {
		ui.resetTimeline()
		ui.applyView(ui.liveView, folder, sel)
		ui.flashFooter(liveSwitchFooter(ui.liveView.scannedAt))
		if then != nil {
			then()
		}
		return
	}

	modal := tview.NewModal().
		SetText(fmt.Sprintf("Scan %s live now (this folder only)?", folder)).
		AddButtons([]string{"Scan", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			ui.pages.RemovePage("confirm")
			if buttonIndex == 0 {
				ui.spotRescan(folder, sel)
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

// showScanRunningNotice explains why a spot-rescan can't start while another
// scan runs (no concurrent scans).
func (ui *UI) showScanRunningNotice() {
	modal := tview.NewModal().
		SetText("A scan is already running — wait for it to finish before going live here.").
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(int, string) {
			ui.pages.RemovePage("confirm")
			ui.app.SetFocus(ui.table)
		})
	if !ui.UseColors {
		modal.SetBackgroundColor(tcell.ColorGray)
	} else {
		modal.SetBackgroundColor(tcell.ColorBlack)
	}
	modal.SetBorderColor(tcell.ColorDefault)
	ui.pages.AddPage("confirm", modal, true, true)
}

// spotRescan scans just folder into a fresh subtree-rooted live View, keeping
// the cursor on sel. Spot-rescans are transient: they never save a snapshot
// — they exist to act on reality, not to record it.
func (ui *UI) spotRescan(folder, sel string) {
	ui.resetTimeline()
	ui.Analyzer.ResetProgress()
	ui.linkedItems = make(fs.HardLinkedItems)
	err := ui.analyzePath(folder, nil, scanOpts{transient: true, keepSelection: sel})
	if err != nil {
		ui.showErr("Error scanning path", err)
	}
}

// humanAge renders a duration in the copy's units: "5 min", "2 h", "25 days".
func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	minutes := int(d / time.Minute)
	switch {
	case minutes < 1:
		return "1 min"
	case minutes < 60:
		return fmt.Sprintf("%d min", minutes)
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
	}
}
