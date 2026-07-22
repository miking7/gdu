package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/pkg/remove"
	"github.com/dundee/gdu/v5/pkg/timefilter"
	"github.com/dundee/gdu/v5/report"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// UI struct
type UI struct {
	app        common.TermApplication
	screen     tcell.Screen
	output     io.Writer
	currentDir fs.Item
	topDir     fs.Item
	getter     device.DevicesInfoGetter
	*common.UI
	grid                    *tview.Grid
	header                  *tview.TextView
	footer                  *tview.Flex
	footerLabel             *tview.TextView
	currentDirLabel         *tview.TextView
	pages                   *tview.Pages
	progress                *tview.TextView
	progressBar             *ProgressBar
	status                  *tview.TextView
	help                    *tview.Flex
	table                   *tview.Table
	filteringInput          *tview.InputField
	typeFilteringInput      *tview.InputField
	done                    chan struct{}
	remover                 func(fs.Item, fs.Item) error
	emptier                 func(fs.Item, fs.Item) error
	exec                    func(argv0 string, argv []string, envv []string) error
	reexec                  func(argv []string, envv []string) error
	changeCwdFn             func(string) error
	linkedItems             fs.HardLinkedItems
	ignoredRows             map[int]struct{}
	markedRows              map[int]struct{}
	markedPaths             []string
	deleteQueue             chan deleteQueueItem
	resultRow               ResultRow
	topDirPath              string
	currentDirPath          string
	filterValue             string
	typeFilterValue         string
	sortBy                  string
	sortOrder               string
	footerTextColor         string
	footerBackgroundColor   string
	footerNumberColor       string
	headerTextColor         string
	headerBackgroundColor   string
	defaultSortBy           string
	defaultSortOrder        string
	exportName              string
	configFilePath          string
	devices                 []*device.Device
	selectedTextColor       tcell.Color
	selectedBackgroundColor tcell.Color
	markedTextColor         tcell.Color
	markedBackgroundColor   tcell.Color
	currentItemNameMaxLen   int
	activeWorkers           int
	deleteWorkersCount      int
	statusMut               sync.RWMutex
	workersMut              sync.Mutex
	askBeforeDelete         bool
	showItemCount           bool
	showMtime               bool
	filtering               bool
	typeFiltering           bool
	headerHidden            bool
	useOldSizeBar           bool
	showBarPercentage       bool
	noDelete                bool
	noViewFile              bool
	noSpawnShell            bool
	deleteInBackground      bool
	timeFilter              *timefilter.TimeFilter
	timeFilterLoc           *time.Location
	noDeleteWithFilter      bool
	collapsePath            bool
	browseParentDirs        bool
	showDiskProgressBar     bool
	currentDeviceSize       int64
	// noCross is --no-cross: keep every scan inside one filesystem. It is held
	// as the flag, not resolved into ignore paths at startup, because the root
	// to resolve it against is only known once a scan starts.
	noCross bool
	// growth-diff browsing: a session-only baseline snapshot the
	// current tree is diffed against. nil means normal browsing.
	baseline   *analyze.Baseline
	baselineTs time.Time
	// baselineKey is the active baseline snapshot's full identity, so the
	// S picker can mark and pre-select it on reopen — a timestamp alone can't
	// tell same-instant snapshots of different roots apart. Zero when no
	// snapshot-derived baseline is set.
	baselineKey parquet.SnapshotKey
	// diffHidden is the Tab peek toggle (axis B): with a baseline set the tree
	// still renders plain rows when true. inDiffMode() (a baseline exists) drives
	// the header's two lines and the Esc ladder; renderingDelta() (a baseline
	// exists AND not hidden) drives whether the Δ column is actually drawn.
	diffHidden bool
	// Compare view keeps its own (sortBy, order) so plain and compare each
	// remember how they were last sorted — Tab flips between two renderings that
	// are each left exactly as you sorted them. Session-scoped: it survives
	// baseline clear/set cycles and is never persisted. Starts at Δ descending
	// (biggest growth first).
	diffSortBy    string
	diffSortOrder string
	snapshotsDir  string // archive dir the S/O pickers list snapshots from
	// View/Baseline state: every screen shows a View, optionally against a
	// Baseline. currentView is what's shown, liveView the in-memory live tree
	// (kept while snapshot Views are shown), returnView where Esc lands — the
	// View the session was launched into.
	currentView *view
	liveView    *view
	returnView  *view
	// liveSavedKey identifies the snapshot saved from liveView's scan, for the
	// timeline fold rule; liveDiverged flips once the live tree mutates
	// (deletes, refreshes), un-folding the saved snapshot. liveDiverged is
	// atomic because delete workers set it off the event loop.
	liveSavedKey   parquet.SnapshotKey
	liveSavedValid bool
	liveDiverged   atomic.Bool
	// time stepping: the pinned timeline of covering snapshots (oldest →
	// newest; position len == live), the walk's target position, and a
	// generation counter invalidating superseded async loads.
	timelineEntries []report.SnapshotListing
	timelineRoot    string
	timelinePos     int
	timelineActive  bool
	timelineFolded  bool // the pin dropped the just-saved snapshot
	stepTarget      int
	stepLoading     bool
	stepGen         uint64
	// scan-wait state: scanning is true while a chosen scan runs (event
	// loop only), scanningRoot its root, scanNudge the progress screen's
	// time-travel hint line, scanningLabel the right-edge footer indicator.
	scanning       bool
	scanningRoot   string
	scanTransient  bool // the running scan is transient (would not save a snapshot)
	scanPageHidden bool // the user stepped away from the scan's progress page
	scanNudge      string
	scanningLabel  *tview.TextView
	// quit-confirmation state (--no-confirm-quit clears confirmQuit): scanStart
	// times the running scan; unsavedScanDuration is the longest completed scan
	// whose results were not recorded as a snapshot, so a post-scan quit warns
	// only when something is genuinely at risk — a recorded scan never bumps it.
	confirmQuit         bool
	scanStart           time.Time
	unsavedScanDuration time.Duration
	// mid-scan preview (Tab) is page-level state, not a View: previewing shows
	// the partial live tree read-only, previewSavedDir restores the pre-preview
	// currentDir, and progressFlex re-shows the scan's progress page on exit.
	previewing      bool
	previewSavedDir fs.Item
	progressFlex    *tview.Flex
	coveringHint    bool   // archive holds covering history → context-aware header hint
	headerLines     int    // current header grid-row height (1 or 2)
	headerNotice    string // transient header notice; suppresses updateHeader while set
	footerBase      string // the footer's resting text; flashes restore it
	// snapshot picker: sizes fill in asynchronously behind the shown list, and the
	// selected baseline loads on its own goroutine. snapshotPickerGen invalidates
	// stale async cell updates (touched only on the event loop); snapshotSizeCancel
	// stops the background size-reader when the picker closes; snapshotWork tracks
	// in-flight fill/load goroutines so a quit can drain them before Stop; and
	// snapshotShuttingDown (event-loop only) stops new ones racing that drain.
	snapshotPickerGen    uint64
	snapshotSizeCancel   context.CancelFunc
	snapshotWork         sync.WaitGroup
	snapshotWorkActive   atomic.Int32 // >0 while a fill/load goroutine runs (cheap quit check)
	snapshotShuttingDown bool
	// background auto-compaction run state (see tui/autocompact.go)
	autoCompactCancel  context.CancelFunc
	autoCompactDone    chan struct{}
	autoCompactRunning atomic.Bool
	compactingLabel    *tview.TextView
	quitOnce           sync.Once
	// launcher (tui/launcher.go) is the interactive front door. launcher
	// holds the current launcher screen (nil when it isn't shown; the pointer
	// also guards its async fill). usingLauncher stays true once the launcher is
	// the navigation home, so left-arrow at a live tree's top returns there
	// rather than to the standalone device list.
	launcher      *launcherState
	usingLauncher bool
}

type deleteQueueItem struct {
	item        fs.Item
	shouldEmpty bool
}

// ResultRow is a struct for a row in the result table
type ResultRow struct {
	NumberColor    string
	DirectoryColor string
}

// Option is optional function customizing the behaviour of UI
type Option func(ui *UI)

// CreateUI creates the whole UI app
func CreateUI(
	app common.TermApplication,
	screen tcell.Screen,
	output io.Writer,
	useColors bool,
	showApparentSize bool,
	showRelativeSize bool,
	useSIPrefix bool,
	opts ...Option,
) *UI {
	ui := &UI{
		UI: &common.UI{
			UseColors:        useColors,
			ShowApparentSize: showApparentSize,
			ShowRelativeSize: showRelativeSize,
			Analyzer:         analyze.CreateAnalyzer(),
			UseSIPrefix:      useSIPrefix,
		},
		app:                     app,
		screen:                  screen,
		output:                  output,
		askBeforeDelete:         true,
		confirmQuit:             true,
		showItemCount:           false,
		remover:                 remove.ItemFromDir,
		emptier:                 remove.EmptyFileFromDir,
		exec:                    Execute,
		reexec:                  reexecSudo,
		linkedItems:             make(fs.HardLinkedItems, 10),
		selectedTextColor:       tview.Styles.TitleColor,
		selectedBackgroundColor: tview.Styles.MoreContrastBackgroundColor,
		markedTextColor:         tview.Styles.PrimaryTextColor,
		markedBackgroundColor:   tview.Styles.ContrastBackgroundColor,
		currentItemNameMaxLen:   70,
		defaultSortBy:           "size",
		defaultSortOrder:        "desc",
		diffSortBy:              deltaSortKey,
		diffSortOrder:           descOrder,
		ignoredRows:             make(map[int]struct{}),
		markedRows:              make(map[int]struct{}),
		exportName:              "export.json",
		noDelete:                false,
		noViewFile:              false,
		noSpawnShell:            false,
		deleteQueue:             make(chan deleteQueueItem, 1000),
		deleteWorkersCount:      3 * runtime.GOMAXPROCS(0),
	}
	for _, o := range opts {
		o(ui)
	}

	ui.resetSorting()

	app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		screen.Clear()
		return false
	})

	ui.app.SetInputCapture(ui.keyPressed)
	ui.app.SetMouseCapture(ui.onMouse)

	ui.header = tview.NewTextView()
	ui.header.SetTextColor(tcell.GetColor(ui.headerTextColor))
	ui.header.SetBackgroundColor(tcell.GetColor(ui.headerBackgroundColor))
	// Set the initial header text. This shows the normal navigation hint, or the
	// baseline banner when a --baseline option (applied above, before this widget
	// existed) has already put the UI in diff mode.
	ui.headerLines = 1
	ui.updateHeader()

	ui.currentDirLabel = tview.NewTextView()
	ui.currentDirLabel.SetTextColor(tcell.ColorDefault)
	ui.currentDirLabel.SetBackgroundColor(tcell.ColorDefault)

	ui.table = tview.NewTable().SetSelectable(true, false)
	ui.table.SetBackgroundColor(tcell.ColorDefault)
	ui.table.SetSelectedFunc(ui.fileItemSelected)

	if ui.UseColors {
		ui.table.SetSelectedStyle(tcell.Style{}.
			Foreground(ui.selectedTextColor).
			Background(ui.selectedBackgroundColor).Bold(true))
	} else {
		ui.table.SetSelectedStyle(tcell.Style{}.
			Foreground(tcell.ColorWhite).
			Background(tcell.ColorGray).Bold(true))
	}

	ui.footerLabel = tview.NewTextView().SetDynamicColors(true)
	ui.footerLabel.SetTextColor(tcell.GetColor(ui.footerTextColor))
	ui.footerLabel.SetBackgroundColor(tcell.GetColor(ui.footerBackgroundColor))
	ui.footerLabel.SetText(" No items to display. ")

	ui.footer = tview.NewFlex()
	ui.footer.AddItem(ui.footerLabel, 0, 1, false)

	// Right-edge indicator added/removed while auto-compaction works in the
	// background; created up front so goroutines never race its construction.
	ui.compactingLabel = tview.NewTextView().SetText(compactingIndicatorText)
	ui.compactingLabel.SetTextColor(tcell.GetColor(ui.footerTextColor))
	ui.compactingLabel.SetBackgroundColor(tcell.GetColor(ui.footerBackgroundColor))

	// Right-edge indicator shown while a scan runs and the user is elsewhere on
	// the timeline (scan-wait time travel).
	ui.scanningLabel = tview.NewTextView().SetText(scanningIndicatorText)
	ui.scanningLabel.SetTextColor(tcell.GetColor(ui.footerTextColor))
	ui.scanningLabel.SetBackgroundColor(tcell.GetColor(ui.footerBackgroundColor))

	ui.createGrid()

	ui.pages = tview.NewPages().
		AddPage("background", ui.grid, true, true)
	ui.pages.SetBackgroundColor(tcell.ColorDefault)

	ui.app.SetRoot(ui.pages, true)

	return ui
}

// createGrid creates the main grid layout
func (ui *UI) createGrid() {
	if ui.headerHidden {
		ui.grid = tview.NewGrid().SetRows(1, 0, 1).SetColumns(0)
		ui.grid.AddItem(ui.currentDirLabel, 0, 0, 1, 1, 0, 0, false).
			AddItem(ui.table, 1, 0, 1, 1, 0, 0, true).
			AddItem(ui.footer, 2, 0, 1, 1, 0, 0, false)
	} else {
		// headerLines, not a literal 1: a --baseline applied during CreateUI
		// already asked for a two-line header before this grid existed.
		ui.grid = tview.NewGrid().SetRows(ui.headerLines, 1, 0, 1).SetColumns(0)
		ui.grid.AddItem(ui.header, 0, 0, 1, 1, 0, 0, false).
			AddItem(ui.currentDirLabel, 1, 0, 1, 1, 0, 0, false).
			AddItem(ui.table, 2, 0, 1, 1, 0, 0, true).
			AddItem(ui.footer, 3, 0, 1, 1, 0, 0, false)
	}
}

// SetSelectedTextColor sets the color for the highlighted selected text
func (ui *UI) SetSelectedTextColor(color tcell.Color) {
	ui.selectedTextColor = color
}

// SetSelectedBackgroundColor sets the color for the highlighted selected text
func (ui *UI) SetSelectedBackgroundColor(color tcell.Color) {
	ui.selectedBackgroundColor = color
}

// SetMarkedTextColor sets the text color for marked items
func (ui *UI) SetMarkedTextColor(color tcell.Color) {
	ui.markedTextColor = color
}

// SetMarkedBackgroundColor sets the background color for marked items
func (ui *UI) SetMarkedBackgroundColor(color tcell.Color) {
	ui.markedBackgroundColor = color
}

// SetFooterTextColor sets the color for the footer text
func (ui *UI) SetFooterTextColor(color string) {
	ui.footerTextColor = color
}

// SetFooterBackgroundColor sets the color for the footer background
func (ui *UI) SetFooterBackgroundColor(color string) {
	ui.footerBackgroundColor = color
}

// SetFooterNumberColor sets the color for the footer number
func (ui *UI) SetFooterNumberColor(color string) {
	ui.footerNumberColor = color
}

// SetHeaderTextColor sets the color for the header text
func (ui *UI) SetHeaderTextColor(color string) {
	ui.headerTextColor = color
}

// SetHeaderBackgroundColor sets the color for the header background
func (ui *UI) SetHeaderBackgroundColor(color string) {
	ui.headerBackgroundColor = color
}

// SetHeaderHidden sets the flag to hide the header
func (ui *UI) SetHeaderHidden() {
	ui.headerHidden = true
}

// SetResultRowDirectoryColor sets the color for the result row directory
func (ui *UI) SetResultRowDirectoryColor(color string) {
	ui.resultRow.DirectoryColor = color
}

// SetResultRowNumberColor sets the color for the result row number
func (ui *UI) SetResultRowNumberColor(color string) {
	ui.resultRow.NumberColor = color
}

// SetCurrentItemNameMaxLen sets the maximum length of the path of the currently processed item
// to be shown in the progress modal
func (ui *UI) SetCurrentItemNameMaxLen(maxLen int) {
	ui.currentItemNameMaxLen = maxLen
}

// UseOldSizeBar uses the old size bar (# chars) instead of the new one (unicode block elements)
func (ui *UI) UseOldSizeBar() {
	ui.useOldSizeBar = true
}

// SetShowBarPercentage shows the numeric usage percentage next to the size bar
func (ui *UI) SetShowBarPercentage() {
	ui.showBarPercentage = true
}

// SetChangeCwdFn sets function that can be used to change current working dir
// during dir browsing
func (ui *UI) SetChangeCwdFn(fn func(string) error) {
	ui.changeCwdFn = fn
}

// SetSnapshotsDir sets the snapshot archive directory the S picker lists baselines
// from (growth-diff browsing).
func (ui *UI) SetSnapshotsDir(dir string) {
	ui.snapshotsDir = dir
}

// SetDeleteInParallel sets the flag to delete files in parallel
func (ui *UI) SetDeleteInParallel() {
	ui.remover = remove.ItemFromDirParallel
}

// StartUILoop starts tview application
func (ui *UI) StartUILoop() error {
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(
			c,
			syscall.SIGHUP,
			syscall.SIGINT,
			syscall.SIGQUIT,
			syscall.SIGILL,
			syscall.SIGTRAP,
			syscall.SIGABRT,
			syscall.SIGPIPE,
			syscall.SIGTERM,
		)
		s := <-c
		log.Printf("Got signal: %s", s)
		ui.handleShutdownSignal()
	}()

	return ui.app.Run()
}

// SetConfirmQuit sets whether gdu asks for confirmation before quitting
// after a scan that took a noticeable amount of time
func (ui *UI) SetConfirmQuit(value bool) {
	ui.confirmQuit = value
}

// SetShowItemCount sets the flag to show number of items in directory
func (ui *UI) SetShowItemCount() {
	ui.showItemCount = true
}

// SetShowMTime sets the flag to show last modification time of items in directory
func (ui *UI) SetShowMTime() {
	ui.showMtime = true
}

// SetNoDelete disables all write operations
func (ui *UI) SetNoDelete() {
	ui.noDelete = true
}

// SetNoSpawnShell disables shell spawning
func (ui *UI) SetNoSpawnShell() {
	ui.noSpawnShell = true
}

func (ui *UI) SetNoViewFile() {
	ui.noViewFile = true
}

// SetNoDelete disables delete when time filters are active
func (ui *UI) SetNoDeleteWithFilter() {
	ui.noDeleteWithFilter = true
}

// SetBrowseParentDirs enables navigating above the launch directory
func (ui *UI) SetBrowseParentDirs() {
	ui.browseParentDirs = true
}

// SetNoCross keeps every scan within one filesystem, skipping the mount points
// nested under whatever root each scan is started from.
func (ui *UI) SetNoCross(value bool) {
	ui.noCross = value
}

// SetDevicesGetter sets the source of the mount table. The launcher and the
// device screen also set it when they list devices, but a scan needs it too —
// to resolve its mount boundary — so it is wired up even for a run that opens
// neither.
func (ui *UI) SetDevicesGetter(getter device.DevicesInfoGetter) {
	ui.getter = getter
}

// SetCollapsePath sets the flag to collapse paths
func (ui *UI) SetCollapsePath(value bool) {
	ui.collapsePath = value
}

// SetShowDiskProgressBar sets whether to show a progress bar when scanning a whole disk
func (ui *UI) SetShowDiskProgressBar(value bool) {
	ui.showDiskProgressBar = value
}

// SetDeleteInBackground sets the flag to delete files in background
func (ui *UI) SetDeleteInBackground() {
	ui.deleteInBackground = true

	for i := 0; i < ui.deleteWorkersCount; i++ {
		go ui.deleteWorker()
	}
	go ui.updateStatusWorker()
}

func (ui *UI) resetSorting() {
	ui.sortBy = ui.defaultSortBy
	ui.sortOrder = ui.defaultSortOrder
}

// rescanDir refreshes the current directory from the live disk. Refreshes are
// transient: they never save a snapshot and mark the live tree as
// diverged from its saved snapshot (un-folding it on the timeline).
// Refreshing a snapshot View would graft live data into it — a View is all
// one thing — so the go-live signpost intercepts instead; guarded here
// at the mutation's entry so no trigger path can bypass it.
func (ui *UI) rescanDir() {
	if ui.blockMutation(true) {
		return
	}
	if ui.scanning {
		ui.showScanRunningNotice()
		return
	}
	ui.Analyzer.ResetProgress()
	ui.linkedItems = make(fs.HardLinkedItems)
	ui.liveDiverged.Store(true)
	err := ui.analyzePath(ui.currentDirPath, ui.currentDir.GetParent(), scanOpts{transient: true})
	if err != nil {
		ui.showErr("Error rescanning path", err)
	}
}

func (ui *UI) fileItemSelected(row, column int) {
	if ui.currentDir == nil {
		return // Add this check to handle nil case
	}

	selectedDirCell := ui.table.GetCell(row, column)

	// Check if the selectedDirCell is nil before using it
	if selectedDirCell == nil || selectedDirCell.GetReference() == nil {
		return
	}

	selectedDir := selectedDirCell.GetReference().(fs.Item)
	if selectedDir == nil || !selectedDir.IsDir() {
		return
	}

	origDir := ui.currentDir
	ui.currentDir = selectedDir
	ui.hideFilterInput()
	ui.hideTypeFilterInput()
	ui.markedRows = make(map[int]struct{})
	ui.ignoredRows = make(map[int]struct{})
	ui.showDir()

	// while previewing a mid-scan snapshot there is no stable top dir to anchor
	// the "select last visited" logic to, so just render the navigated dir
	if ui.previewing {
		return
	}

	if row != 0 || origDir.GetPath() == ui.topDir.GetPath() {
		return
	}

	// we are going up in the directory tree, select the last visited directory
	if origDir.GetParent() != nil {
		nestedDir := origDir
		for nestedDir.GetParent() != nil {
			if selectedDir.GetName() == nestedDir.GetParent().GetName() {
				sortBy, sortOrder := ui.getSortParams()
				index := -1
				i := 0
				for item := range ui.currentDir.GetFiles(sortBy, sortOrder) {
					if item.GetName() == nestedDir.GetName() {
						index = i
						break
					}
					i++
				}
				if index >= 0 {
					if ui.currentDir.GetPath() != ui.topDir.GetPath() {
						index++
					}
					ui.table.Select(index, 0)
				}
				break
			}
			nestedDir = nestedDir.GetParent()
		}
	}
}

func (ui *UI) deviceItemSelected(row, column int) {
	selectedDevice, ok := ui.table.GetCell(row, column).GetReference().(*device.Device)
	if !ok {
		return
	}

	ui.resetSorting()

	ui.currentDeviceSize = selectedDevice.Size
	ui.Analyzer.ResetProgress()
	ui.linkedItems = make(fs.HardLinkedItems)
	// Measuring a device: the scan stops at the mounts nested inside it.
	if err := ui.analyzePath(selectedDevice.MountPoint, nil, scanOpts{wholeDevice: true}); err != nil {
		ui.showErr("Error analyzing device", err)
	}
}

func (ui *UI) confirmDeletion(shouldEmpty bool) {
	if ui.noDelete {
		ui.headerNoticeNow("Deletion is disabled!")
		return
	}

	// Check if deletion is allowed with active time filters
	if ui.noDeleteWithFilter {
		modal := tview.NewModal().
			SetText("Deletion is disabled when a time filter is active.\n\n" +
				"To override, set GDU_ALLOW_DELETE_WITH_FILTER=1").
			AddButtons([]string{"OK"}).
			SetDoneFunc(func(buttonIndex int, buttonLabel string) {
				ui.pages.RemovePage("confirm")
			})
		if !ui.UseColors {
			modal.SetBackgroundColor(tcell.ColorGray)
		}
		ui.pages.AddPage("confirm", modal, true, true)
		return
	}

	if len(ui.markedRows) > 0 {
		ui.confirmDeletionMarked(shouldEmpty)
	} else {
		ui.confirmDeletionSelected(shouldEmpty)
	}
}

func (ui *UI) confirmDeletionSelected(shouldEmpty bool) {
	row, column := ui.table.GetSelection()
	selectedFile := ui.table.GetCell(row, column).GetReference().(fs.Item)
	var action string
	if shouldEmpty {
		action = "empty"
	} else {
		action = "delete"
	}
	modal := tview.NewModal().
		SetText(
			"Are you sure you want to " +
				action +
				" \"" +
				tview.Escape(selectedFile.GetName()) +
				"\"?",
		).
		AddButtons([]string{"no", "yes", "don't ask me again"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			switch buttonIndex {
			case 2:
				ui.askBeforeDelete = false
				fallthrough
			case 1:
				ui.deleteSelected(shouldEmpty)
			}
			ui.pages.RemovePage("confirm")
		})

	if !ui.UseColors {
		modal.SetBackgroundColor(tcell.ColorGray)
	} else {
		modal.SetBackgroundColor(tcell.ColorBlack)
	}
	modal.SetBorderColor(tcell.ColorDefault)

	ui.pages.AddPage("confirm", modal, true, true)
}

// SetTimeFilterWithInfo sets both the time filter function and stores the filter info for display
func (ui *UI) SetTimeFilterWithInfo(tf *timefilter.TimeFilter, loc *time.Location) {
	ui.timeFilter = tf
	ui.timeFilterLoc = loc

	if tf != nil && !tf.IsEmpty() {
		timeFilterFunc := func(mtime time.Time) bool {
			return tf.IncludeByTimeFilter(mtime, loc)
		}
		ui.SetTimeFilter(timeFilterFunc)
		if !ui.isDeleteAllowedWithFilter() {
			ui.SetNoDeleteWithFilter()
		}
	}
}

// hasActiveTimeFilter returns true if any time filter is active
func (ui *UI) hasActiveTimeFilter() bool {
	return ui.timeFilter != nil && !ui.timeFilter.IsEmpty()
}

// formatTimeFilterInfo formats the time filter information for display
func (ui *UI) formatTimeFilterInfo() string {
	if !ui.hasActiveTimeFilter() {
		return ""
	}

	return ui.timeFilter.FormatForDisplay(ui.timeFilterLoc)
}

// isDeleteAllowedWithFilter checks if deletion is allowed when filters are active
func (ui *UI) isDeleteAllowedWithFilter() bool {
	if !ui.hasActiveTimeFilter() {
		return true
	}

	// Check environment variable override
	if os.Getenv("GDU_ALLOW_DELETE_WITH_FILTER") == "1" {
		return true
	}

	return false
}

// printMarkedPaths prints the paths of the marked items to the output
func (ui *UI) printMarkedPaths() {
	for _, path := range ui.markedPaths {
		fmt.Fprintf(ui.output, "%s\n", path)
	}
}
