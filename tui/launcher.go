package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/pkg/path"
	"github.com/dundee/gdu/v5/report"
)

// The launcher is the interactive front door: the folder you're in, the disk it
// lives on, your other disks, and an Other-folder opener — each showing its
// newest covering snapshot once history exists. It renders through the upstream
// device table (shared with the classic `-d` page), pinning the folder row
// first and Other folder… last into the Mount point column. Bare `gdu`,
// `gdu <path>` and `gdu -d` all land here.

const (
	launcherPage      = "launcher"
	launcherInputPage = "launcher-input"

	launcherTitle = " gdu ~ Choose a location — scan it or open a snapshot "
	launcherHints = " Enter scan · s open latest snapshot · S pick snapshot · n sort · q quit"

	// scanAnotherFolderLabel is the Other-folder opener's text — three ASCII
	// dots, not the … glyph (which also renders under --no-unicode).
	scanAnotherFolderLabel = "Scan another folder..."
	scanAnotherFolderTitle = " Scan another folder "

	// launcherFallbackWidth is the assumed terminal width when the screen size
	// is unknown (e.g. under the simulation screen in tests).
	launcherFallbackWidth = 100
	// launcherFolderMinWidth floors the shortened folder-path / mount cells so
	// they stay readable on a narrow terminal.
	launcherFolderMinWidth = 24
)

// launcherRowKind classifies a launcher row.
type launcherRowKind int

const (
	launcherFolder launcherRowKind = iota // the default dir (cwd or CLI path)
	launcherDisk                          // a mounted device
	launcherOther                         // the "Other folder…" input opener
)

// launcherRow is one selectable line in the launcher.
type launcherRow struct {
	kind launcherRowKind
	root string         // scan root / snapshot scope ("" for launcherOther)
	dev  *device.Device // set for launcherDisk
	// land is where a scan/open of this row lands the view when it differs from
	// root — the default dir for the pinned own-disk row, so opening the
	// disk shows where you are, not the mount root. "" falls back to root.
	land string
	// pinned marks the default-dir's own disk row: kept directly below the
	// folder row across n sorting.
	pinned bool
	// mount is the folder row's most-specific mount point — the lower bound for
	// its snapshot mapping. "" for disk/other rows.
	mount string
	// note is a pinned row's dim role label, e.g. "(current folder)". "" for disks.
	note string
	// covering is the archived snapshots this row maps to, newest first —
	// filled asynchronously, then read by s/S, the snapshot column, and the tip.
	covering []report.SnapshotListing
}

// landPath is the path a scan/open of this row lands on: its land override,
// else its root.
func (r *launcherRow) landPath() string {
	if r.land != "" {
		return r.land
	}
	return r.root
}

// launcherState holds the transient launcher screen. The *launcherState pointer
// stored on the UI (ui.launcher) doubles as the async-fill generation guard: a
// background archive read applies its result only while ui.launcher still points
// at the same value, so a launcher that was closed or reopened drops stale work.
type launcherState struct {
	table      *tview.Table
	footer     *tview.TextView // upstream-style bottom bar: key hints + Sorting by
	tip        *tview.TextView // sudo tip, its own line above the footer
	rows       []*launcherRow
	hasSnapCol bool // any row maps to covering history (progressive disclosure)
	fillDone   bool // the archive read finished (s/S may act)
	sortByName bool // false = usage desc (default), true = name asc
}

// OpenLauncher shows the launcher for defaultPath (the cwd or the CLI path).
// pathFromArg is true when a path was named on the command line — the D12
// discriminator: an explicit path pre-selects the folder row, a bare launch the
// cwd's disk row. Device discovery failure is not fatal: the launcher still
// offers the folder and Other-folder rows, so plain `gdu` keeps working where
// mount enumeration is flaky.
func (ui *UI) OpenLauncher(getter device.DevicesInfoGetter, defaultPath string, pathFromArg bool) error {
	ui.getter = getter
	devices, err := getter.GetDevicesInfo()
	if err != nil {
		log.Printf("launcher: listing devices failed, showing folder rows only: %s", err)
		devices = nil
	}
	// Unfiltered by HideSystemVolumes: the rows filter for display, but
	// snapshot row-mapping needs every mount to resolve a path to its disk.
	ui.devices = devices
	ui.showLauncher(defaultPath, pathFromArg)
	return nil
}

// showLauncher builds and displays the launcher page and starts its async fill.
func (ui *UI) showLauncher(defaultPath string, pathFromArg bool) {
	rows, preselect := buildLauncherRows(defaultPath, ui.devices, pathFromArg)
	st := &launcherState{rows: rows}
	ui.launcher = st
	ui.usingLauncher = true

	table := tview.NewTable().SetSelectable(true, false)
	table.SetBackgroundColor(tcell.ColorDefault)
	if ui.UseColors {
		table.SetSelectedStyle(tcell.Style{}.
			Foreground(ui.selectedTextColor).
			Background(ui.selectedBackgroundColor).Bold(true))
	} else {
		table.SetSelectedStyle(tcell.Style{}.
			Foreground(tcell.ColorWhite).
			Background(tcell.ColorGray).Bold(true))
	}
	st.table = table
	table.SetInputCapture(ui.launcherKey)
	table.SetSelectedFunc(func(row, _ int) { ui.launcherActivate(row) })
	table.SetSelectionChangedFunc(func(int, int) { ui.updateLauncherTip() })

	tip := tview.NewTextView().SetDynamicColors(true)
	tip.SetBackgroundColor(tcell.ColorDefault)
	st.tip = tip

	footer := tview.NewTextView().SetDynamicColors(true)
	footer.SetTextColor(tcell.GetColor(ui.footerTextColor))
	footer.SetBackgroundColor(tcell.GetColor(ui.footerBackgroundColor))
	st.footer = footer

	ui.renderLauncherRows()
	ui.updateLauncherFooter()

	// A header bar styled like the classic screens' (user's header colors),
	// dropped under style.header.hidden.
	flex := tview.NewFlex().SetDirection(tview.FlexRow)
	if !ui.headerHidden {
		header := tview.NewTextView().SetDynamicColors(true)
		header.SetTextColor(tcell.GetColor(ui.headerTextColor))
		header.SetBackgroundColor(tcell.GetColor(ui.headerBackgroundColor))
		header.SetText(launcherTitle)
		flex.AddItem(header, 1, 0, false).
			AddItem(tview.NewBox().SetBackgroundColor(tcell.ColorDefault), 1, 0, false)
	}
	flex.AddItem(table, 0, 1, true).
		AddItem(tip, 1, 0, false).
		AddItem(footer, 1, 0, false)

	ui.pages.AddPage(launcherPage, flex, true, true)
	table.Select(preselect+1, 0) // +1 for the header row
	ui.updateLauncherTip()
	ui.app.SetFocus(table)

	ui.startLauncherFill()
}

// roleNote is the folder row's dim label: whether the path came from the CLI
// (the wording tracks the pre-selection discriminator).
func roleNote(pathFromArg bool) string {
	if pathFromArg {
		return "(specified folder)"
	}
	return "(current folder)"
}

// buildLauncherRows lays out the launcher rows: the default-dir folder pinned
// first (omitted when it IS a mount root), the disks (system volumes filtered
// from display), then the Other-folder opener pinned last. It returns the rows
// and the pre-selected row index (into rows): the folder row for an explicit
// path, else the cwd's disk row.
func buildLauncherRows(defaultPath string, fullDevices device.Devices, pathFromArg bool) (rows []*launcherRow, preselect int) {
	ownDev := device.ForPath(fullDevices, defaultPath)
	folderMount := defaultPath
	if ownDev != nil {
		folderMount = ownDev.MountPoint
	}
	folderIsMountRoot := ownDev != nil && ownDev.MountPoint == defaultPath

	hasFolder := !folderIsMountRoot
	if hasFolder {
		rows = append(rows, &launcherRow{
			kind:  launcherFolder,
			root:  defaultPath,
			land:  defaultPath,
			mount: folderMount,
			note:  roleNote(pathFromArg),
		})
	}

	display := device.HideSystemVolumes(fullDevices)
	ownShown := false
	for _, d := range display {
		if d == ownDev {
			ownShown = true
			break
		}
	}

	// Pin the default-dir's own disk directly below the folder row (first when
	// the folder was omitted); it lands the view at the default dir, not the
	// mount root. It is excluded from n sorting (launcherDiskSpan).
	ownDiskRow := -1
	if ownShown {
		ownDiskRow = len(rows)
		rows = append(rows, &launcherRow{
			kind: launcherDisk, root: ownDev.MountPoint, land: defaultPath, dev: ownDev, pinned: true,
		})
	}

	// The other disks (system volumes hidden), sorted usage-desc.
	var others device.Devices
	for _, d := range display {
		if d != ownDev {
			others = append(others, d)
		}
	}
	sort.Stable(sort.Reverse(device.ByUsedSize(others)))
	for _, d := range others {
		rows = append(rows, &launcherRow{kind: launcherDisk, root: d.MountPoint, dev: d})
	}

	rows = append(rows, &launcherRow{kind: launcherOther})

	// Pre-selection: explicit path → folder row; bare launch → the
	// pinned own-disk row, falling back to the folder row when that disk is
	// hidden or absent.
	switch {
	case pathFromArg && hasFolder:
		preselect = 0
	case ownDiskRow >= 0:
		preselect = ownDiskRow
	default:
		preselect = 0
	}
	return rows, preselect
}

// renderLauncherRows (re)fills the launcher table through the shared device-table
// renderer, preserving the cursor. Row 0 is the header; st.rows[i] renders at
// table row i+1. The Snapshot column (before Mount point) appears only once some
// row maps to covering history (progressive disclosure).
func (ui *UI) renderLauncherRows() {
	st := ui.launcher
	if st == nil {
		return
	}
	t := st.table
	sel, _ := t.GetSelection()
	t.Clear()

	nameColor, sizeColor := ui.deviceTableColors()
	mountCol := deviceMountCol
	if st.hasSnapCol {
		mountCol = deviceMountCol + 1
	}
	mountWidth := ui.deviceMountWidth(ui.launcherDisplayDevices(), st.hasSnapCol)

	setDeviceHeaderCells(t, mountCol)
	if st.hasSnapCol {
		t.SetCell(0, deviceMountCol, tview.NewTableCell("Snapshot").SetSelectable(false))
	}

	for i, r := range st.rows {
		row := i + 1
		if r.kind == launcherDisk {
			ui.setDeviceRowCells(t, row, mountCol, mountWidth, r.dev, nameColor, sizeColor)
		} else {
			t.SetCell(row, mountCol, tview.NewTableCell(ui.pinnedMountCell(r, mountWidth)))
		}
		if st.hasSnapCol {
			t.SetCell(row, deviceMountCol, tview.NewTableCell(launcherSnapCell(r)))
		}
	}

	if sel < 1 {
		sel = 1
	}
	if sel > len(st.rows) {
		sel = len(st.rows)
	}
	t.Select(sel, 0)
}

// launcherDisplayDevices collects the devices currently shown as disk rows —
// for the mount-column width estimate (their names size the first column).
func (ui *UI) launcherDisplayDevices() device.Devices {
	st := ui.launcher
	if st == nil {
		return nil
	}
	var devices device.Devices
	for _, r := range st.rows {
		if r.dev != nil {
			devices = append(devices, r.dev)
		}
	}
	return devices
}

// pinnedMountCell renders a pinned row's Mount point cell: the folder path in
// mount-point blue with a dim role note, or "Other folder…".
func (ui *UI) pinnedMountCell(r *launcherRow, width int) string {
	nameColor, _ := ui.deviceTableColors()
	if r.kind == launcherOther {
		return nameColor + scanAnotherFolderLabel
	}
	budget := width - len(r.note) - 1
	if budget < launcherFolderMinWidth {
		budget = launcherFolderMinWidth
	}
	p := path.ShortenPath(abbrevHome(r.root, homeDir()), budget)
	return nameColor + p + ui.dimTag() + " " + r.note
}

// launcherSnapCell renders a row's newest-mapped-snapshot age, or "".
func launcherSnapCell(r *launcherRow) string {
	if len(r.covering) == 0 {
		return ""
	}
	return "snapshot " + humanAge(time.Since(r.covering[0].ScanTs)) + " ago"
}

// startLauncherFill reads the archive off the event loop and maps each row's
// snapshots. With no archive configured there is nothing to read, so the
// fill is marked done immediately and the launcher stays history-free.
func (ui *UI) startLauncherFill() {
	st := ui.launcher
	if ui.snapshotsDir == "" {
		st.fillDone = true
		return
	}
	ui.goPickerWork(func() {
		listings, err := report.ListSnapshotsInDir(ui.snapshotsDir)
		ui.app.QueueUpdateDraw(func() {
			if ui.launcher != st {
				return // the launcher was closed or reopened while listing
			}
			st.fillDone = true
			if err != nil {
				log.Printf("launcher: reading snapshot archive failed: %s", err)
				return // leave the launcher without a snapshot column
			}
			ui.applyLauncherCovering(listings)
		})
	})
}

// applyLauncherCovering attaches each row's mapped snapshots (newest
// first), reveals the snapshot column when any exist, and refreshes the tip.
// Mapping is mount-accurate, not merely path-covering: a disk row lists
// snapshots of exactly its mount point; the folder row lists snapshots rooted
// between its most-specific mount point and the folder itself. Runs on the event
// loop.
func (ui *UI) applyLauncherCovering(listings []report.SnapshotListing) {
	st := ui.launcher
	hasAny := false
	for _, r := range st.rows {
		var covering []report.SnapshotListing
		for i := range listings {
			if launcherRowMapsSnapshot(r, listings[i].ScanRoot) {
				covering = append(covering, listings[i])
			}
		}
		sort.SliceStable(covering, func(i, j int) bool {
			return covering[i].ScanTs.After(covering[j].ScanTs)
		})
		r.covering = covering
		if len(covering) > 0 {
			hasAny = true
		}
	}
	st.hasSnapCol = hasAny
	ui.renderLauncherRows()
	ui.updateLauncherTip()
}

// launcherRowMapsSnapshot reports whether a snapshot rooted at scanRoot belongs
// to row r: a disk row matches its exact mount point; a folder row matches
// roots that cover the folder and lie at-or-below its mount point (so a "/" scan
// credits a folder on "/" but not one on the SD card). The folder case is the
// shared mount-accurate predicate the S picker and timeline also use.
func launcherRowMapsSnapshot(r *launcherRow, scanRoot string) bool {
	//nolint:exhaustive // Why: launcherOther has no root and maps nothing (default)
	switch r.kind {
	case launcherDisk:
		return scanRoot == r.root
	case launcherFolder:
		return report.RootCoversWithinMount(scanRoot, r.root, r.mount)
	default:
		return false
	}
}

// launcherKey handles the launcher's own keys; navigation (arrows, j/k, g/G)
// passes through to tview's table.
func (ui *UI) launcherKey(event *tcell.EventKey) *tcell.EventKey {
	//nolint:exhaustive // Why: only Enter/Esc are special; the rest fall through to runes/navigation
	switch event.Key() {
	case tcell.KeyEnter:
		row, _ := ui.launcher.table.GetSelection()
		ui.launcherActivate(row)
		return nil
	case tcell.KeyEsc:
		return nil // the front door has nothing to return to; Esc is a no-op
	}
	switch event.Rune() {
	case 'q':
		ui.quitApp(false)
		return nil
	case 'Q':
		ui.quitApp(true)
		return nil
	case 's':
		ui.launcherOpenLatest()
		return nil
	case 'S':
		ui.launcherPickSnapshot()
		return nil
	case 'n':
		ui.launcherToggleSort()
		return nil
	case 'R':
		// Manual restart-elevated. Gated to non-root Unix; a no-op
		// elsewhere (root has nothing to elevate, Windows has no sudo).
		if sudoTipRelevant() {
			ui.confirmRestartElevated()
		}
		return nil
	}
	return event
}

// launcherToggleSort flips the disk sort (usage-desc ↔ name-asc), leaving
// the pinned folder and Other-folder rows fixed, and re-renders.
func (ui *UI) launcherToggleSort() {
	st := ui.launcher
	if st == nil {
		return
	}
	st.sortByName = !st.sortByName
	lo, hi := launcherDiskSpan(st.rows)
	disks := st.rows[lo:hi]
	sort.SliceStable(disks, func(i, j int) bool {
		if st.sortByName {
			return disks[i].dev.Name < disks[j].dev.Name
		}
		return disks[i].dev.GetUsage() > disks[j].dev.GetUsage()
	})
	ui.renderLauncherRows()
	ui.updateLauncherFooter()
	// The cursor stays on the same table row, which now holds a different disk,
	// so refresh the tip (tview fires no selection-changed event on a re-sort).
	ui.updateLauncherTip()
}

// launcherDiskSpan returns the [lo, hi) index range of the *sortable* disk rows
// within rows — after the pinned folder row and the pinned own-disk row,
// before the pinned Other-folder row.
func launcherDiskSpan(rows []*launcherRow) (lo, hi int) {
	lo, hi = 0, len(rows)
	if len(rows) > 0 && rows[0].kind == launcherFolder {
		lo = 1
	}
	if lo < len(rows) && rows[lo].kind == launcherDisk && rows[lo].pinned {
		lo++ // the own disk stays pinned below the folder, not sorted
	}
	if hi > 0 && rows[hi-1].kind == launcherOther {
		hi--
	}
	return lo, hi
}

// launcherMouse handles mouse events while the launcher is up (called from
// onMouse). It blocks clicks under the keyboard-driven Other-folder modal,
// activates a row on double-click (tview's Table never fires "selected" on a
// double-click — only keyboard Enter does — so the classic device page's
// behavior is reproduced here), and passes single clicks / scrolls through to
// the launcher table. handled is false when the launcher isn't showing.
func (ui *UI) launcherMouse(
	event *tcell.EventMouse, action tview.MouseAction,
) (ev *tcell.EventMouse, ac tview.MouseAction, handled bool) {
	if ui.pages.HasPage(launcherInputPage) {
		return nil, action, true
	}
	if !ui.pages.HasPage(launcherPage) {
		return nil, action, false
	}
	if action == tview.MouseLeftDoubleClick && ui.launcher != nil {
		row, _ := ui.launcher.table.GetSelection()
		ui.launcherActivate(row)
		return nil, action, true
	}
	return event, action, true
}

// selectedLauncherRow returns the row under the launcher cursor, or nil (the
// header at table row 0 has no row).
func (ui *UI) selectedLauncherRow() *launcherRow {
	st := ui.launcher
	if st == nil || st.table == nil {
		return nil
	}
	row, _ := st.table.GetSelection()
	idx := row - 1 // table row 0 is the header
	if idx < 0 || idx >= len(st.rows) {
		return nil
	}
	return st.rows[idx]
}

// launcherActivate runs a row's Enter action (tableRow is 1-based over the
// header): scan a folder/disk, or open the Other-folder input.
func (ui *UI) launcherActivate(tableRow int) {
	st := ui.launcher
	if st == nil {
		return
	}
	idx := tableRow - 1
	if idx < 0 || idx >= len(st.rows) {
		return
	}
	r := st.rows[idx]
	if r.kind == launcherOther {
		ui.showOtherFolderInput()
		return
	}
	ui.launcherScan(r)
}

// launcherScan is the choke point for scanning a chosen launcher row. It gates a
// whole-root-volume (/) scan behind the forced sudo interstitial when
// running non-root on a Unix-like OS; every other scan — and the elevated re-run,
// which comes up as root — falls straight through to launcherRunScan.
func (ui *UI) launcherScan(r *launcherRow) {
	if sudoTipRelevant() && isRootVolume(r.root) {
		ui.confirmScanElevated(r)
		return
	}
	ui.launcherRunScan(r)
}

// launcherRunScan closes the launcher and scans a chosen row — a deliberate root,
// so it saves a snapshot per the tri-state. A disk row retains the
// nested-mount ignore behavior from deviceItemSelected.
func (ui *UI) launcherRunScan(r *launcherRow) {
	ui.closeLauncher()
	ui.resetSorting()

	// Clear the device size so a prior disk scan's size never leaks into a
	// later folder scan (reachable via the left-arrow-returns-to-launcher
	// flow). The scan's mount boundary is resolved per scan, in analyzePath.
	ui.currentDeviceSize = 0
	wholeDevice := r.kind == launcherDisk && r.dev != nil
	if wholeDevice {
		ui.currentDeviceSize = r.dev.Size
	}

	ui.Analyzer.ResetProgress()
	ui.linkedItems = make(fs.HardLinkedItems)
	// landPath lands the view at the default dir when this is the pinned own-disk
	// row; for every other row it equals the scanned root.
	if err := ui.analyzePath(r.root, nil, scanOpts{landPath: r.landPath(), wholeDevice: wholeDevice}); err != nil {
		ui.showErr("Error analyzing path", err)
	}
}

// launcherOpenLatest opens the selected row's newest mapped snapshot as a
// read-only View (key s) — no scan.
func (ui *UI) launcherOpenLatest() {
	r := ui.selectedLauncherRow()
	if r == nil || r.kind == launcherOther {
		return
	}
	if !ui.launcher.fillDone {
		ui.launcherNotice("still reading the snapshot archive…")
		return
	}
	if len(r.covering) == 0 {
		ui.launcherNotice("no snapshots of this yet — Enter scans it")
		return
	}
	listing := r.covering[0]
	wantPath := r.landPath() // the pinned own-disk row lands at the default dir
	ui.closeLauncher()
	ui.openSnapshotView(&listing, wantPath, "", true, nil)
}

// launcherPickSnapshot opens the unified browser over the selected row's mapped
// snapshots (key S), scoped to that row's root — the full two-cursor grammar, so
// a view and a baseline can both be chosen before the first tree is ever shown
// (landing directly in a compare view). There is no live row (nothing is scanned
// yet); Enter opens the ● snapshot as the View and applies the ◇ baseline; Esc
// returns to the launcher.
func (ui *UI) launcherPickSnapshot() {
	r := ui.selectedLauncherRow()
	if r == nil || r.kind == launcherOther {
		return
	}
	if !ui.launcher.fillDone {
		ui.launcherNotice("still reading the snapshot archive…")
		return
	}
	if len(r.covering) == 0 {
		ui.launcherNotice("no snapshots of this yet — Enter scans it")
		return
	}
	covering := r.covering
	wantPath := r.landPath() // the pinned own-disk row lands at the default dir
	st := ui.launcher
	ui.showBrowser(&browserConfig{
		scopeLabel:   path.ShortenPath(abbrevHome(wantPath, homeDir()), 48),
		covering:     covering,
		fillTarget:   wantPath,
		initialFocus: focusViewing,
		refocus:      st.table,
		hint: func(l *report.SnapshotListing) string {
			return fmt.Sprintf(" gdu --snapshot %s %s",
				parquet.FormatSnapshotTime(&l.SnapshotInfo), l.ScanRoot)
		},
		openView: func(l *report.SnapshotListing, then func()) {
			ui.closeLauncher()
			ui.openSnapshotView(l, wantPath, "", true, then)
		},
		applyBaseline: func(l *report.SnapshotListing) { ui.setBaselineFromListing(l) },
		clearBaseline: func() { ui.clearBaseline() },
	})
}

// showOtherFolderInput opens the Other-folder input: a path field with
// ~ expansion and existence/kind validation. Enter scans a valid folder; an
// invalid entry keeps the field open and reports why in its title.
func (ui *UI) showOtherFolderInput() {
	input := tview.NewInputField().SetLabel(" Folder: ").SetFieldWidth(48)
	input.SetFieldBackgroundColor(tcell.ColorDefault)
	input.SetDoneFunc(func(key tcell.Key) {
		//nolint:exhaustive // Why: only Esc (cancel) and Enter (submit) act; other keys are ignored
		switch key {
		case tcell.KeyEsc:
			ui.pages.RemovePage(launcherInputPage)
			ui.app.SetFocus(ui.launcher.table)
		case tcell.KeyEnter:
			raw := strings.TrimSpace(input.GetText())
			if raw == "" {
				return
			}
			target, err := resolveOtherFolder(raw)
			if err != nil {
				input.SetTitle(fmt.Sprintf(" Scan another folder — %s ", err))
				return
			}
			ui.pages.RemovePage(launcherInputPage)
			ui.launcherScan(&launcherRow{kind: launcherFolder, root: target})
		}
	})
	input.SetBorder(true).SetTitle(scanAnotherFolderTitle).SetBorderColor(tcell.ColorDefault)
	if !ui.UseColors {
		input.SetBackgroundColor(tcell.ColorGray)
	}
	ui.pages.AddPage(launcherInputPage, modal(input, 60, 3), true, true)
	ui.app.SetFocus(input)
}

// launcherNotice flashes text in the tip line for 2 seconds, then restores the
// real tip. Safe from the event loop (the restore hands off to a goroutine).
func (ui *UI) launcherNotice(text string) {
	st := ui.launcher
	if st == nil || st.tip == nil {
		return
	}
	st.tip.SetText(" " + text)
	go func() {
		time.Sleep(2 * time.Second)
		ui.app.QueueUpdateDraw(func() {
			if ui.launcher == st {
				ui.updateLauncherTip()
			}
		})
	}()
}

// closeLauncher dismisses the launcher; nil-ing ui.launcher also invalidates any
// in-flight async fill (the pointer generation guard).
func (ui *UI) closeLauncher() {
	ui.launcher = nil
	ui.pages.RemovePage(launcherPage)
	ui.pages.RemovePage(launcherInputPage)
}

// returnToLauncher reopens the launcher from a live tree's top (left-arrow).
// It never scans; the running-scan case is guarded by the caller. The
// re-entry is a bare launch (no CLI path), so pre-selection is the disk row.
func (ui *UI) returnToLauncher() {
	if ui.getter == nil {
		return
	}
	devices, err := ui.getter.GetDevicesInfo()
	if err != nil {
		log.Printf("launcher: listing devices failed: %s", err)
		devices = nil
	}
	ui.devices = devices
	ui.showLauncher(ui.topDirPath, false)
}

// updateLauncherFooter renders the upstream-style bottom bar: the key
// hints plus the active sort state; no Total usage.
func (ui *UI) updateLauncherFooter() {
	st := ui.launcher
	if st == nil || st.footer == nil {
		return
	}
	sortDesc := "size desc"
	if st.sortByName {
		sortDesc = "name asc"
	}
	var numColor, txtColor string
	if ui.UseColors {
		numColor = fmt.Sprintf("[%s:%s:b]", ui.footerNumberColor, ui.footerBackgroundColor)
		txtColor = fmt.Sprintf("[%s:%s:-]", ui.footerTextColor, ui.footerBackgroundColor)
	} else {
		numColor = blackOnWhiteBold
		txtColor = blackOnWhite
	}
	st.footer.SetText(txtColor + launcherHints + "    Sorting by: " + numColor + sortDesc)
}

// updateLauncherTip renders the sudo tip for the selected row: shown only
// to a non-root Unix user, passive until a mapped snapshot's recorded read-error
// count gives it evidence.
func (ui *UI) updateLauncherTip() {
	st := ui.launcher
	if st == nil || st.tip == nil {
		return
	}
	st.tip.SetText(ui.launcherTipText())
}

// launcherTipText builds the sudo tip. It is empty for root and on Windows
// (where sudo is meaningless); otherwise it delegates to sudoTipBody.
func (ui *UI) launcherTipText() string {
	if !sudoTipRelevant() {
		return ""
	}
	return ui.sudoTipBody(ui.selectedLauncherRow())
}

// sudoTipBody is the sudo tip's text (independent of the euid gate, so it is
// unit-testable): passive by default, upgrading to the evidence form when r's
// newest mapped snapshot recorded unreadable folders. The macOS FDA
// caveat is appended on darwin — even root can't read everything without it.
func (ui *UI) sudoTipBody(r *launcherRow) string {
	if r != nil && len(r.covering) > 0 && r.covering[0].ErrCount > 0 {
		n := r.covering[0].ErrCount
		noun := "folders"
		if n == 1 {
			noun = "folder"
		}
		return fmt.Sprintf(" Tip: last scan of %s couldn't read %d %s — press R to restart with sudo and include them%s.",
			path.ShortenPath(r.covering[0].ScanRoot, 32), n, noun, fdaCaveat())
	}
	return fmt.Sprintf(" Tip: press R to restart with sudo and reach folders you don't have permission to read%s.", fdaCaveat())
}

// sudoTipRelevant reports whether a sudo tip makes sense: a non-root effective
// user on a Unix-like OS. os.Geteuid returns -1 on Windows, so > 0 excludes both
// root (0) and Windows.
func sudoTipRelevant() bool {
	return os.Geteuid() > 0
}

// isRootVolume reports whether root names the whole root volume (/) — the one
// scan the launcher forces a sudo prompt for. filepath.Clean folds any trailing
// separator so "/" and "//" both match.
func isRootVolume(root string) bool {
	return filepath.Clean(root) == "/"
}

// fdaCaveat is the macOS Full Disk Access reminder appended to the sudo tip:
// even root can't read everything without it. Empty off macOS.
func fdaCaveat() string {
	if runtime.GOOS == "darwin" {
		return " (still limited by macOS Full Disk Access)"
	}
	return ""
}

// homeDir returns the user's home directory, or "" when it can't be determined.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// abbrevHome renders p with a leading ~ when it is at or under home, for display.
func abbrevHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// resolveOtherFolder validates an Other-folder entry: ~ expansion, absolute
// path, and that it names an existing directory. The returned errors are short
// enough to sit in the field's title.
func resolveOtherFolder(raw string) (string, error) {
	p := raw
	if p == "~" || strings.HasPrefix(p, "~/") {
		home := homeDir()
		if home == "" {
			return "", fmt.Errorf("no home dir")
		}
		p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no such folder")
		}
		return "", fmt.Errorf("cannot open")
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a folder")
	}
	return abs, nil
}
