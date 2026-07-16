package tui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/build"
	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
)

const (
	defaultLinesCount = 500
	linesThreshold    = 20

	actionEmpty  = "empty"
	actionDelete = "delete"

	actingEmpty  = "emptying"
	actingDelete = "deleting"
)

// ListDevices lists mounted devices and shows their disk usage. This is the
// standalone device table (non-interactive `-d`, or `-d` with launcher:false);
// the launcher is the interactive front door otherwise. Marking usingLauncher
// false keeps left-arrow-at-top returning here, not to the launcher.
func (ui *UI) ListDevices(getter device.DevicesInfoGetter) error {
	var err error
	ui.getter = getter
	ui.usingLauncher = false
	ui.devices, err = getter.GetDevicesInfo()
	if err != nil {
		return err
	}

	ui.showDevices()

	return nil
}

// scanOpts modifies how a scan integrates with the view and recording model.
type scanOpts struct {
	// transient scans — `r` refreshes and go-live spot-rescans — never save a
	// snapshot: a snapshot records the completed scan of a deliberately
	// chosen root.
	transient bool
	// keepSelection re-selects this item by name once the scanned tree is
	// shown (the go-live flow keeps the cursor).
	keepSelection string
	// landPath, when the completed root scan covers it, is the folder the view
	// lands on instead of the scan root — the launcher's pinned own-disk row
	// scans the whole disk but shows the default dir.
	landPath string
}

// AnalyzePath analyzes recursively disk usage for given path
func (ui *UI) AnalyzePath(path string, parentDir fs.Item) error {
	return ui.analyzePath(path, parentDir, scanOpts{})
}

// analyzePath runs a scan asynchronously behind the progress page. While it
// runs the timeline stays walkable (scan-wait time travel): the progress
// page is the timeline's live position, and completion never steals focus from
// a user who has stepped into the past.
//
//nolint:funlen // Why: one cohesive scan setup + completion sequence
func (ui *UI) analyzePath(path string, parentDir fs.Item, opts scanOpts) error {
	ui.progress = tview.NewTextView().SetText("Scanning...")
	ui.progress.SetBorder(true).SetBorderPadding(2, 2, 2, 2)
	ui.progress.SetTitle(" Scanning... ")
	ui.progress.SetDynamicColors(true)

	innerFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(ui.progress, 10, 1, false)

	if ui.currentDeviceSize > 0 && ui.showDiskProgressBar {
		ui.progressBar = NewProgressBar()
		ui.progressBar.SetBorder(true)
		ui.progressBar.SetTitle(" Progress ")
		ui.progressBar.SetUseColor(ui.UseColors)
		innerFlex.AddItem(ui.progressBar, 3, 1, false)
	}

	innerFlex.AddItem(nil, 0, 1, false)

	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(innerFlex, 0, 50, false).
		AddItem(nil, 0, 1, false)

	ui.pages.AddPage("progress", flex, true, true)
	ui.progressFlex = flex

	ui.scanning = true
	ui.scanStart = time.Now()
	ui.scanningRoot = path
	ui.scanTransient = opts.transient
	ui.scanPageHidden = false
	ui.scanNudge = ""
	ui.rebuildFooter() // show the right-edge "scanning…" indicator
	if parentDir == nil {
		ui.refreshScanNudge(path)
	}

	// The stats pass must not read ui.topDir from the scan goroutine — the
	// user may step through snapshots (rewriting it) while the scan runs.
	statsTree := ui.topDir

	analyzer := ui.Analyzer
	doneChan := analyzer.GetDone()
	go ui.updateProgress(analyzer, doneChan)

	go func() {
		defer debug.FreeOSMemory()
		currentDir := ui.Analyzer.AnalyzeDir(path, ui.CreateIgnoreFunc(), ui.CreateFileTypeFilter())

		if parentDir != nil {
			currentDir.SetParent(parentDir)
			// Remove old entry with the same name and add new one
			parentDir.RemoveFileByName(currentDir.GetName())
			parentDir.AddFile(currentDir)
		} else {
			statsTree = currentDir
		}

		if ui.IsFilteringFiles() {
			statsTree.UpdateStatsWithFileFiltering(ui.linkedItems)
		} else {
			statsTree.UpdateStats(ui.linkedItems)
		}

		// Persist a snapshot of the just-completed scan of a chosen root.
		// This is a background side effect and never alters the UI. Transient
		// scans (refreshes, spot-rescans) never save.
		var savedKey parquet.SnapshotKey
		savedOK := false
		if parentDir == nil && ui.SaveSnapshotEnabled && !opts.transient {
			// Collect the scan's transient garbage first so the snapshot write
			// reuses freed heap instead of raising peak RSS (which macOS keeps
			// resident); otherwise --save-snapshots inflates memory while browsing.
			runtime.GC()
			savedKey, savedOK = ui.saveSnapshot(currentDir)
			// Unless --no-auto-compact, tidy the archive in the background while the
			// user browses (runs on its own goroutine; never blocks this one).
			ui.startAutoCompact()
		}

		scannedAt := time.Now()
		ui.app.QueueUpdateDraw(func() {
			ui.scanning = false
			ui.scanNudge = ""
			// The finished scan replaces any mid-scan preview: a previewing user
			// is at the live position, so completion renders the final tree.
			ui.previewing = false
			ui.previewSavedDir = nil
			// Track the longest completed scan whose results were NOT recorded as
			// a snapshot (saving disabled, transient, subdir graft, or a failed
			// write — savedOK is only set for a saved root scan). A post-scan quit
			// then warns that unsaved results are on screen; a recorded scan
			// leaves this untouched and quits silently.
			if !savedOK {
				if d := time.Since(ui.scanStart); d > ui.unsavedScanDuration {
					ui.unsavedScanDuration = d
				}
			}
			ui.rebuildFooter() // drop the "scanning…" indicator

			if parentDir != nil {
				// A subdir refresh grafted into the live tree: re-render in
				// place — unless the user stepped into the past meanwhile, in
				// which case their snapshot View must not be overwritten (the
				// graft already updated the live tree they will return to).
				if !ui.scanPageHidden {
					ui.currentDir = currentDir
					ui.showDir()
				}
				ui.pages.RemovePage("progress")
				return
			}

			ui.finishRootScan(currentDir, path, scannedAt, savedKey, savedOK, opts)
		})

		if ui.done != nil {
			ui.done <- struct{}{}
		}
	}()

	return nil
}

// finishRootScan integrates a completed root scan into the view model: the
// fresh tree becomes the live view (and the fold identity for the timeline).
// When the user is watching the progress screen the view switches to the
// result; when they have stepped into the past, completion never steals focus
// — the footer flashes instead and `]` leads to the new tree.
func (ui *UI) finishRootScan(
	tree fs.Item, path string, scannedAt time.Time,
	savedKey parquet.SnapshotKey, savedOK bool, opts scanOpts,
) {
	newView := &view{tree: tree, topPath: path, scannedAt: scannedAt}
	ui.liveSavedKey = savedKey
	ui.liveSavedValid = savedOK
	ui.liveDiverged.Store(false)

	switch {
	case ui.returnView == nil:
		// The session's first view: where Esc always returns.
		ui.returnView = newView
	case ui.returnView.isLive() && (!opts.transient || path == ui.returnView.topPath):
		// A live-started session's return target follows deliberate re-roots
		// (device/parent/end-of-timeline scans) and same-root refreshes; a
		// spot-rescan of an uncovered folder leaves it alone.
		ui.returnView = newView
	}

	// The user is "watching" unless they stepped away from the progress page
	// into the past. The explicit flag — not a front-page check — so a
	// modal stacked over the progress page (the quit confirmation) doesn't
	// masquerade as stepping away and swallow the finished tree.
	watching := !ui.scanPageHidden
	ui.pages.RemovePage("progress")
	ui.liveView = newView
	// Any pinned timeline predates this scan: its fold decision and positions
	// are stale now that the live position is a fresh tree. The next step
	// re-derives (and re-finds the shown snapshot by identity).
	ui.resetTimeline()

	if !watching {
		ui.flashFooter(scanCompleteNotice)
		return
	}

	targetPath := path
	switch {
	case opts.landPath != "" && report.PathCoveredBy(path, opts.landPath):
		// The launcher's pinned own-disk row scans the whole disk but lands on
		// the default dir — takes precedence over the current-folder rule.
		targetPath = opts.landPath
	case ui.currentDir != nil && report.PathCoveredBy(path, ui.currentDirPath):
		targetPath = ui.currentDirPath
	}
	ui.applyView(newView, targetPath, opts.keepSelection)
	ui.refreshCoveringHint(path)
}

// refreshScanNudge asynchronously adds the scan-wait time-travel hint to the
// progress screen when covering history exists.
func (ui *UI) refreshScanNudge(path string) {
	if ui.snapshotsDir == "" {
		return
	}
	devices, getter := ui.devices, ui.getter // captured on the loop for off-loop mount resolution
	ui.goPickerWork(func() {
		covering, err := ui.coveringForTarget(path, devices, getter)
		if err != nil || len(covering) == 0 {
			return
		}
		age := humanAge(time.Since(covering[0].ScanTs)) // newest first
		ui.app.QueueUpdateDraw(func() {
			if !ui.scanning {
				return // the scan finished before the archive listing did
			}
			ui.scanNudge = fmt.Sprintf(
				"\n\nWaiting? [ steps into your last snapshot of this folder (%s ago) — ] returns here.", age)
		})
	})
}

// saveSnapshot writes tree to the archive and returns the saved snapshot's
// identity for the timeline fold rule.
func (ui *UI) saveSnapshot(tree fs.Item) (key parquet.SnapshotKey, ok bool) {
	path, key, createdDir, err := parquet.SaveSnapshot(tree, ui.SnapshotsDir, ui.SnapshotThreshold, time.Now())
	if createdDir {
		// This save created the archive: tell the user where recording is
		// landing. Announced even if the write itself then failed — the
		// directory now exists, so no later save would ever announce, and
		// recording must not start silently.
		announcement := common.SnapshotDirAnnouncement(ui.SnapshotsDir)
		log.Print(announcement)
		ui.showHeaderNotice(announcement)
	}
	if err != nil {
		log.Printf("save-snapshots failed: %s", err)
		return key, false
	}
	log.Printf("Saved snapshot to %s", path)
	return key, true
}

// showHeaderNotice flashes text in the header for 2 seconds, then re-renders
// the state-driven header. Safe to call from any goroutine EXCEPT the event
// loop — tview's QueueUpdateDraw deadlocks when called from the loop it
// queues onto; event-loop callers use headerNoticeNow.
func (ui *UI) showHeaderNotice(text string) {
	ui.app.QueueUpdateDraw(func() {
		ui.headerNoticeNow(text)
	})
}

// headerNoticeNow flashes text in the header for 2 seconds, then re-renders
// the state-driven header. While a notice shows, updateHeader holds off so a
// view change underneath doesn't clobber it; a newer notice wins. Must run on
// the event loop.
func (ui *UI) headerNoticeNow(text string) {
	noticeText := " " + text
	ui.headerNotice = noticeText
	ui.header.SetText(noticeText)

	go func() {
		time.Sleep(2 * time.Second)
		ui.app.QueueUpdateDraw(func() {
			if ui.headerNotice != noticeText {
				return // a newer notice owns the header
			}
			ui.headerNotice = ""
			ui.updateHeader()
		})
	}()
}

// ReadAnalysis reads an analysis report (JSON or Parquet) into a read-only
// import View (hard read-only applies to `-f` startup Views too).
func (ui *UI) ReadAnalysis(input io.Reader) error {
	sel := parquet.SnapshotSelector{
		Spec: ui.SnapshotSpec, Root: ui.SnapshotRoot,
		ExactTs: ui.SnapshotTs, ExactHost: ui.SnapshotHost,
	}

	// A seekable Parquet file carries snapshot identities: resolve which
	// snapshot the View will show up front, so the header can say when it was
	// taken. A multi-snapshot file opened without an explicit --snapshot opens
	// the picker instead. stdin and JSON fall through to an identity-less load.
	if f, ok := input.(*os.File); ok {
		if snapshots, serr := report.ParquetSnapshotsFromFile(f); serr == nil && len(snapshots) > 0 {
			if len(snapshots) > 1 && sel.Spec == "" && sel.Root == "" && sel.ExactTs.IsZero() {
				ui.showStartupSnapshotPicker(f, snapshots)
				return nil
			}
			if info, ierr := parquet.SelectSnapshot(snapshots, sel); ierr == nil {
				ui.loadSnapshotFromFile(f, &info)
				return nil
			}
			// An unresolvable selector falls through; the reader below fails
			// with the same descriptive error, shown in the error modal.
		}
	}

	ui.loadAnalysis(func() (*analyze.Dir, error) {
		return report.ReadAnalysisWithSnapshot(input, sel)
	}, nil, importName(input))
	return nil
}

// loadSnapshotFromFile loads one identified snapshot from an open Parquet file
// into a snapshot View.
func (ui *UI) loadSnapshotFromFile(f *os.File, info *parquet.SnapshotInfo) {
	snapshot := *info // own copy; the async load outlives the caller's value
	ui.loadAnalysis(func() (*analyze.Dir, error) {
		return report.ReadAnalysisSnapshot(f, &snapshot)
	}, &snapshot, "")
}

// importName names an identity-less import for the header's Viewing line.
func importName(input io.Reader) string {
	if f, ok := input.(*os.File); ok && f.Name() != "" && f.Name() != os.Stdin.Name() {
		return filepath.Base(f.Name())
	}
	return "(stdin)"
}

// loadAnalysis shows the reading-progress page and, on a background goroutine,
// runs load to build the tree, then renders it as a read-only View (or shows
// an error). snapshot identifies the View when known; importLabel names an
// identity-less one. Shared by the `-f` paths and the startup picker.
func (ui *UI) loadAnalysis(load func() (*analyze.Dir, error), snapshot *parquet.SnapshotInfo, importLabel string) {
	ui.progress = tview.NewTextView().SetText("Reading analysis from file...")
	ui.progress.SetBorder(true).SetBorderPadding(2, 2, 2, 2)
	ui.progress.SetTitle(" Reading... ")
	ui.progress.SetDynamicColors(true)

	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 10, 1, false).
			AddItem(ui.progress, 8, 1, false).
			AddItem(nil, 10, 1, false), 0, 50, false).
		AddItem(nil, 0, 1, false)

	ui.pages.AddPage("progress", flex, true, true)

	go func() {
		dir, err := load()
		if err != nil {
			ui.app.QueueUpdateDraw(func() {
				ui.pages.RemovePage("progress")
				ui.showErr("Error reading file", err)
			})
			if ui.done != nil {
				ui.done <- struct{}{}
			}
			return
		}
		// The Parquet/JSON reader churns a lot of short-lived memory; return it to
		// the OS before the long-lived browsing session (see RAM notes in the plan).
		debug.FreeOSMemory()

		links := make(fs.HardLinkedItems, 10)
		dir.UpdateStats(links)

		ui.app.QueueUpdateDraw(func() {
			v := &view{tree: dir, topPath: dir.GetPath(), snapshot: snapshot, importLabel: importLabel}
			if ui.returnView == nil {
				ui.returnView = v // the View this session was launched into
			}
			ui.applyView(v, v.topPath, "")
			ui.pages.RemovePage("progress")
		})

		if ui.done != nil {
			ui.done <- struct{}{}
		}
	}()
}

// ReadFromStorage reads analysis data from persistent key-value storage. The
// result is a stored past scan, so it is a read-only import View like -f.
func (ui *UI) ReadFromStorage(storagePath, path string) error {
	storage := analyze.NewStorage(storagePath, path)
	closeFn := storage.Open()
	defer closeFn()

	dir, err := storage.GetDirForPath(path)
	if err != nil {
		return err
	}

	v := &view{tree: dir, topPath: dir.GetPath(), importLabel: filepath.Base(storagePath)}
	if ui.returnView == nil {
		ui.returnView = v
	}
	ui.applyView(v, v.topPath, "")
	return nil
}

func (ui *UI) delete(shouldEmpty bool) {
	if len(ui.markedRows) > 0 {
		ui.deleteMarked(shouldEmpty)
	} else {
		ui.deleteSelected(shouldEmpty)
	}
}

// markLiveDiverged records that the live tree no longer matches its saved
// snapshot (deletes, refreshes), un-folding that snapshot on the timeline.
// Safe from any goroutine.
func (ui *UI) markLiveDiverged() {
	ui.liveDiverged.Store(true)
}

func (ui *UI) deleteSelected(shouldEmpty bool) {
	row, column := ui.table.GetSelection()
	selectedItem := ui.table.GetCell(row, column).GetReference().(fs.Item)
	ui.markLiveDiverged()

	if ui.deleteInBackground {
		ui.queueForDeletion([]fs.Item{selectedItem}, shouldEmpty)
		return
	}

	var action, acting string
	if shouldEmpty {
		action = actionEmpty
		acting = actingEmpty
	} else {
		action = actionDelete
		acting = actingDelete
	}
	modal := tview.NewModal().SetText(
		cases.Title(language.English).String(acting) +
			" " +
			tview.Escape(selectedItem.GetName()) +
			"...",
	)
	ui.pages.AddPage(acting, modal, true, true)

	var currentDir fs.Item
	var deleteItems []fs.Item
	if shouldEmpty && selectedItem.IsDir() {
		currentDir = selectedItem
		for file := range currentDir.GetFiles(fs.SortBySize, fs.SortDesc) {
			deleteItems = append(deleteItems, file)
		}
	} else {
		currentDir = ui.currentDir
		deleteItems = append(deleteItems, selectedItem)
	}

	var deleteFun func(fs.Item, fs.Item) error
	if shouldEmpty && !selectedItem.IsDir() {
		deleteFun = ui.emptier
	} else {
		deleteFun = ui.remover
	}
	go func() {
		for _, item := range deleteItems {
			if err := deleteFun(currentDir, item); err != nil {
				msg := "Can't " + action + " " + tview.Escape(selectedItem.GetName())
				ui.app.QueueUpdateDraw(func() {
					ui.pages.RemovePage(acting)
					ui.showErr(msg, err)
				})
				if ui.done != nil {
					ui.done <- struct{}{}
				}
				return
			}
		}

		ui.app.QueueUpdateDraw(func() {
			ui.pages.RemovePage(acting)
			x, y := ui.table.GetOffset()
			ui.showDir()
			ui.table.Select(min(row, ui.table.GetRowCount()-1), 0)
			ui.table.SetOffset(min(x, ui.table.GetRowCount()-1), y)
		})

		if ui.done != nil {
			ui.done <- struct{}{}
		}
	}()
}

func (ui *UI) showInfo() {
	if ui.currentDir == nil {
		return
	}

	var content, numberColor string
	row, column := ui.table.GetSelection()
	// Removed-item rows in diff mode carry no reference; there is no info to show.
	selectedFile, ok := ui.table.GetCell(row, column).GetReference().(fs.Item)
	if !ok {
		return
	}

	if ui.UseColors {
		numberColor = fmt.Sprintf(
			"[%s::b]",
			ui.resultRow.NumberColor,
		)
	} else {
		numberColor = defaultColorBold
	}

	linesCount := 12

	text := tview.NewTextView().SetDynamicColors(true)
	text.SetBorder(true).SetBorderPadding(2, 2, 2, 2)
	text.SetBorderColor(tcell.ColorDefault)
	text.SetTitle(" Item info ")

	content += "[::b]Name:[::-] "
	content += tview.Escape(selectedFile.GetName()) + "\n"
	content += "[::b]Path:[::-] "
	content += tview.Escape(
		strings.TrimPrefix(selectedFile.GetPath(), build.RootPathPrefix),
	) + "\n"
	content += "[::b]Type:[::-] " + selectedFile.GetType() + "\n\n"

	content += "   [::b]Disk usage:[::-] "
	content += numberColor + ui.formatSize(selectedFile.GetUsage(), false, true)
	content += fmt.Sprintf(" (%s%d[-::] B)", numberColor, selectedFile.GetUsage()) + "\n"
	content += "[::b]Apparent size:[::-] "
	content += numberColor + ui.formatSize(selectedFile.GetSize(), false, true)
	content += fmt.Sprintf(" (%s%d[-::] B)", numberColor, selectedFile.GetSize()) + "\n"

	if selectedFile.GetMultiLinkedInode() > 0 {
		linkedItems := ui.linkedItems[selectedFile.GetMultiLinkedInode()]
		linesCount += 2 + len(linkedItems)
		content += "\nHard-linked files:\n"
		for _, linkedItem := range linkedItems {
			content += "\t" + linkedItem.GetPath() + "\n"
		}
	}

	text.SetText(content)

	flex := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(text, linesCount, 1, false).
			AddItem(nil, 0, 1, false), 80, 1, false).
		AddItem(nil, 0, 1, false)

	ui.pages.AddPage("info", flex, true, true)
}

func (ui *UI) openItem() {
	row, column := ui.table.GetSelection()
	selectedFile, ok := ui.table.GetCell(row, column).GetReference().(fs.Item)
	if !ok || selectedFile == ui.currentDir.GetParent() {
		return
	}

	openBinary := "xdg-open"

	switch runtime.GOOS {
	case "darwin":
		openBinary = "open"
	case "windows":
		openBinary = "explorer"
	}

	cmd := exec.Command(openBinary, selectedFile.GetPath())
	err := cmd.Start()
	if err != nil {
		ui.showErr("Error opening", err)
	}
}

func (ui *UI) confirmExport() *tview.Form {
	form := tview.NewForm().
		AddInputField("File name", "export.json", 30, nil, func(v string) {
			ui.exportName = v
		}).
		AddButton("Export", ui.exportAnalysis).
		SetButtonsAlign(tview.AlignCenter)
	form.SetBorder(true).
		SetTitle(" Export data to JSON ").
		SetInputCapture(func(key *tcell.EventKey) *tcell.EventKey {
			if key.Key() == tcell.KeyEsc {
				ui.pages.RemovePage("export")
				ui.app.SetFocus(ui.table)
				return nil
			}
			return key
		})
	flex := modal(form, 50, 7)
	ui.pages.AddPage("export", flex, true, true)
	ui.app.SetFocus(form)
	return form
}

func (ui *UI) exportAnalysis() {
	ui.pages.RemovePage("export")

	text := tview.NewTextView().SetText("Export in progress...").SetTextAlign(tview.AlignCenter)
	text.SetBorder(true).SetTitle(" Export data to JSON ")
	flex := modal(text, 50, 3)
	ui.pages.AddPage("exporting", flex, true, true)

	go func() {
		var err error
		defer ui.app.QueueUpdateDraw(func() {
			ui.pages.RemovePage("exporting")
			if err == nil {
				ui.app.SetFocus(ui.table)
			}
		})
		if ui.done != nil {
			defer func() {
				ui.done <- struct{}{}
			}()
		}

		var buff bytes.Buffer

		buff.Write([]byte(`[1,2,{"progname":"gdu","progver":"`))
		buff.Write([]byte(build.Version))
		buff.Write([]byte(`","timestamp":`))
		buff.Write([]byte(strconv.FormatInt(time.Now().Unix(), 10)))
		buff.Write([]byte("},\n"))

		file, err := os.Create(ui.exportName)
		if err != nil {
			ui.showErrFromGo("Error creating file", err)
			return
		}

		if err = ui.topDir.EncodeJSON(&buff, true); err != nil {
			ui.showErrFromGo("Error encoding JSON", err)
			return
		}

		if _, err = buff.Write([]byte("]\n")); err != nil {
			ui.showErrFromGo("Error writing to buffer", err)
			return
		}
		if _, err = buff.WriteTo(file); err != nil {
			ui.showErrFromGo("Error writing to file", err)
			return
		}
		if err = file.Close(); err != nil {
			ui.showErrFromGo("Error closing file", err)
			return
		}
		// Hand the export back to the invoking user when running under sudo.
		common.ChownToInvoker(ui.exportName)
	}()
}

func (ui *UI) isInArchive() bool {
	if ui.currentDir == nil {
		return false
	}
	if _, ok := ui.currentDir.(*analyze.ZipDir); ok {
		return true
	}
	_, ok := ui.currentDir.(*analyze.TarDir)
	return ok
}
