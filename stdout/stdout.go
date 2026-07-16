package stdout

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/report"
	"github.com/fatih/color"
	log "github.com/sirupsen/logrus"
)

// UI struct
type UI struct {
	output io.Writer
	// progressOut receives the transient auto-compaction activity indicator
	// (os.Stderr in normal use; a buffer in tests). It is kept separate from
	// output so the indicator never lands in piped stdout data.
	progressOut io.Writer
	*common.UI
	red         *color.Color
	orange      *color.Color
	blue        *color.Color
	showItemCnt bool
	top         int
	depth       int
	summarize   bool
	noPrefix    bool
	fixedBase   float64
	fixedSuffix string
	reverseSort bool
}

var (
	progressRunes      = []rune(`⠇⠏⠋⠙⠹⠸⠼⠴⠦⠧`)
	progressRunesOld   = []rune(`-\\|/`)
	progressRunesCount = len(progressRunes)
)

// CreateStdoutUI creates UI for stdout
func CreateStdoutUI(
	output io.Writer,
	useColors bool,
	showProgress bool,
	showApparentSize bool,
	showRelativeSize bool,
	summarize bool,
	useSIPrefix bool,
	noPrefix bool,
	fixedUnit string,
	top int,
	reverseSort bool,
	depth int,
) *UI {
	ui := &UI{
		UI: &common.UI{
			UseColors:        useColors,
			ShowProgress:     showProgress,
			ShowApparentSize: showApparentSize,
			ShowRelativeSize: showRelativeSize,
			Analyzer:         analyze.CreateTopDirAnalyzer(),
			UseSIPrefix:      useSIPrefix,
		},
		output:      output,
		progressOut: os.Stderr,
		summarize:   summarize,
		noPrefix:    noPrefix,
		top:         top,
		reverseSort: reverseSort,
		depth:       depth,
	}
	if fixedUnit != "" {
		ui.SetFixedUnit(fixedUnit)
	}
	ui.red = color.New(color.FgRed).Add(color.Bold)
	ui.orange = color.New(color.FgYellow).Add(color.Bold)
	ui.blue = color.New(color.FgBlue).Add(color.Bold)

	if ui.top > 0 || ui.depth > 0 {
		ui.Analyzer = analyze.CreateAnalyzer()
	}

	if !useColors {
		color.NoColor = true
	}

	return ui
}
func (ui *UI) SetFixedUnit(unitChar string) {
	k, m, g := common.Ki, common.Mi, common.Gi
	suffixMap := map[string]string{"k": " KiB", "m": " MiB", "g": " GiB"}

	if ui.UseSIPrefix {
		k, m, g = common.K, common.M, common.G
		suffixMap = map[string]string{"k": " kB", "m": " MB", "g": " GB"}
	}

	switch unitChar {
	case "k":
		ui.fixedBase = k
		ui.fixedSuffix = suffixMap["k"]
	case "m":
		ui.fixedBase = m
		ui.fixedSuffix = suffixMap["m"]
	case "g":
		ui.fixedBase = g
		ui.fixedSuffix = suffixMap["g"]
	}
}

func (ui *UI) SetShowItemCount() {
	ui.showItemCnt = true
}

func (ui *UI) UseOldProgressRunes() {
	progressRunes = progressRunesOld
	progressRunesCount = len(progressRunes)
}

// StartUILoop stub
func (ui *UI) StartUILoop() error {
	return nil
}

// SetCollapsePath sets the flag to collapse paths
func (ui *UI) SetCollapsePath(value bool) {
}

// ListDevices lists mounted devices and shows their disk usage
func (ui *UI) ListDevices(getter device.DevicesInfoGetter) error {
	devices, err := getter.GetDevicesInfo()
	if err != nil {
		return err
	}

	maxDeviceNameLength := maxInt(maxLength(
		devices,
		func(device *device.Device) string { return device.Name },
	), len("Devices"))

	var sizeLength, percentLength int
	if ui.UseColors {
		sizeLength = 20
		percentLength = 16
	} else {
		sizeLength = 9
		percentLength = 5
	}

	lineFormat := fmt.Sprintf(
		"%%%ds %%%ds %%%ds %%%ds %%%ds %%s\n",
		maxDeviceNameLength,
		sizeLength,
		sizeLength,
		sizeLength,
		percentLength,
	)

	fmt.Fprintf(
		ui.output,
		fmt.Sprintf("%%%ds %%9s %%9s %%9s %%5s %%s\n", maxDeviceNameLength),
		"Device",
		"Size",
		"Used",
		"Free",
		"Used%",
		"Mount point",
	)

	for _, device := range devices {
		usedPercent := math.Round(float64(device.Size-device.Free) / float64(device.Size) * 100)

		fmt.Fprintf(
			ui.output,
			lineFormat,
			device.Name,
			ui.formatSize(device.Size),
			ui.formatSize(device.Size-device.Free),
			ui.formatSize(device.Free),
			ui.red.Sprintf("%.f%%", usedPercent),
			device.MountPoint)
	}

	return nil
}

// AnalyzePath analyzes recursively disk usage in given path
func (ui *UI) AnalyzePath(path string, _ fs.Item) error {
	// When path is a regular file, create a File item directly so that
	// apparent-size (GetSize) and disk-usage (GetUsage) are both correct.
	// Running a file through AnalyzeDir + UpdateStats would report the
	// default 4096-byte directory overhead instead of the real values.
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		file := analyze.CreateFileItem(filepath.Base(path), info)
		ui.printTotalItem(file)
		return nil
	}

	var (
		dir             fs.Item
		wait            sync.WaitGroup
		updateStatsDone chan struct{}
	)
	updateStatsDone = make(chan struct{}, 1)

	if ui.ShowProgress {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ui.updateProgress(updateStatsDone)
		}()
	}

	wait.Add(1)
	go func() {
		defer wait.Done()
		dir = ui.Analyzer.AnalyzeDir(path, ui.CreateIgnoreFunc(), ui.CreateFileTypeFilter())
		if ui.IsFilteringFiles() {
			dir.UpdateStatsWithFileFiltering(make(fs.HardLinkedItems, 10))
		} else {
			dir.UpdateStats(make(fs.HardLinkedItems, 10))
		}
		updateStatsDone <- struct{}{}
	}()

	wait.Wait()

	// Persist a snapshot of the completed scan as a side effect; output is
	// unchanged whether or not --save-snapshots is set.
	if ui.SaveSnapshotEnabled {
		runtime.GC() // reclaim scan garbage so the write doesn't raise peak RSS
		ui.saveSnapshot(dir)
	}

	ui.printResults(dir)

	// Compact the archive only after the scan result is printed, so a large
	// merge never stalls piped output (non-interactive has no event loop to
	// background onto, so this runs inline — the report is out first).
	if ui.SaveSnapshotEnabled {
		ui.maybeAutoCompact()
	}

	return nil
}

// printResults renders a completed tree in the mode the flags asked for.
func (ui *UI) printResults(dir fs.Item) {
	switch {
	case ui.top > 0:
		ui.printTopFiles(dir)
	case ui.depth > 0:
		ui.printDirWithDepth(dir, 0)
	case ui.summarize:
		ui.printTotalItem(dir)
	default:
		ui.showDir(dir)
	}
}

// SetSaveSnapshot enables snapshot saving and, because the default top-dir
// analyzer keeps only top-level totals, swaps in the full-tree analyzer the
// snapshot needs — enabling the save *is* forcing the analyzer, so the two can
// never diverge. Output is unchanged (same top-level figures), at the cost of
// more memory.
func (ui *UI) SetSaveSnapshot(dir string, thresholdBytes int64) {
	ui.UI.SetSaveSnapshot(dir, thresholdBytes)
	if _, shallow := ui.Analyzer.(*analyze.TopDirAnalyzer); shallow {
		ui.Analyzer = analyze.CreateAnalyzer()
	}
}

func (ui *UI) saveSnapshot(tree fs.Item) {
	path, _, createdDir, err := parquet.SaveSnapshot(tree, ui.SnapshotsDir, ui.SnapshotThreshold, time.Now())
	if createdDir {
		// This save created the archive: say where recording is landing, on
		// stderr so piped stdout data stays byte-clean. Announced even if the
		// write itself then failed — the directory now exists, so no later save
		// would ever announce, and recording must not start silently.
		announcement := common.SnapshotDirAnnouncement(ui.SnapshotsDir)
		log.Print(announcement)
		fmt.Fprintln(ui.progressOut, "gdu: "+announcement)
	}
	if err != nil {
		log.Printf("save-snapshots failed: %s", err)
		return
	}
	log.Printf("Saved snapshot to %s", path)
}

// maybeAutoCompact runs the process's one opportunistic archive compaction
// after a snapshot save (unless --no-auto-compact). Detailed outcomes go to the log,
// which is silent by default (--log-file is /dev/null). On an interactive
// terminal with work to do it additionally shows a transient stderr indicator
// so the user knows why the prompt hasn't returned, and Ctrl-C cancels the
// merge safely. Piped and --no-progress runs stay completely silent, so stdout
// data is never touched.
func (ui *UI) maybeAutoCompact() {
	if !ui.ClaimAutoCompactRun() {
		return
	}
	now := time.Now()
	// The cheap filename-only pre-check lets a run with nothing to merge skip the
	// indicator and signal plumbing entirely; AutoCompact re-checks for real.
	if !ui.ShowProgress || !parquet.NeedsCompaction(ui.SnapshotsDir, now) {
		if _, err := parquet.AutoCompact(context.Background(), ui.SnapshotsDir, now); err != nil {
			log.Printf("auto-compact failed: %s", err)
		}
		return
	}
	ui.autoCompactWithIndicator(now)
}

// autoCompactWithIndicator runs the compaction behind a live stderr spinner,
// with Ctrl-C wired to cancel it cooperatively (the tmp is discarded and the
// sources are left intact — see parquet.RunCompactionContext). It is only used
// on an interactive terminal when there is a closed month to merge.
func (ui *UI) autoCompactWithIndicator(now time.Time) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	restore := trapInterrupt(cancel)
	defer restore()

	// One synchronous frame first so the notice always appears — even for a
	// merge that finishes before the animation's first tick. The goroutine then
	// only animates; the caller owns the final clear (all writes to progressOut
	// are ordered: this frame → goroutine ticks → clear).
	ui.writeCompactingFrame(0)
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ui.animateCompacting(stop)
	}()

	_, err := parquet.AutoCompact(ctx, ui.SnapshotsDir, now)
	close(stop)
	<-stopped
	ui.clearProgressLine()

	// A Ctrl-C cancellation may surface as the top-level error or, when it hits
	// the final/only group mid-merge, only inside the result — so key off the
	// context, the single source of truth for "did the user abort". The
	// in-flight merge is rolled back (tmp discarded, sources intact) and any
	// month already finished is kept.
	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(ui.progressOut, "gdu: compaction interrupted.")
	case err != nil:
		log.Printf("auto-compact failed: %s", err)
	}
}

// animateCompacting advances the spinner until stop is closed. It does not
// clear the line; the caller does, once, after this returns.
func (ui *UI) animateCompacting(stop <-chan struct{}) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			i = (i + 1) % progressRunesCount
			ui.writeCompactingFrame(i)
		}
	}
}

// writeCompactingFrame redraws the single-line auto-compaction notice in place.
func (ui *UI) writeCompactingFrame(i int) {
	fmt.Fprintf(ui.progressOut, "\r %s compacting snapshot archive… (Ctrl-C to skip)",
		string(progressRunes[i]))
}

// clearProgressLine wipes the in-place notice so it leaves no residue behind.
func (ui *UI) clearProgressLine() {
	fmt.Fprint(ui.progressOut, "\r")
	fmt.Fprint(ui.progressOut, strings.Repeat(" ", 60))
	fmt.Fprint(ui.progressOut, "\r")
}

// trapInterrupt makes the next SIGINT cancel the in-flight compaction
// cooperatively — the tmp file is discarded and the sources are left intact —
// instead of hard-killing gdu mid-write. The returned func restores default
// signal handling and must be called when the merge is done.
func trapInterrupt(cancel context.CancelFunc) func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}

// ReadFromStorage reads analysis data from persistent key-value storage
func (ui *UI) ReadFromStorage(storagePath, path string) error {
	storage := analyze.NewStorage(storagePath, path)
	closeFn := storage.Open()
	defer closeFn()

	dir, err := storage.GetDirForPath(path)
	if err != nil {
		return err
	}

	ui.printResults(dir)
	return nil
}

func (ui *UI) showDir(dir fs.Item) {
	sortOrder := fs.SortDesc
	if ui.reverseSort {
		sortOrder = fs.SortAsc
	}

	sort := fs.SortBySize
	if ui.ShowApparentSize {
		sort = fs.SortByApparentSize
	}

	for file := range dir.GetFiles(sort, sortOrder) {
		ui.printItem(file)
	}
}

func (ui *UI) printTopFiles(file fs.Item) {
	collected := analyze.CollectTopFiles(file, ui.top)
	for _, file := range collected {
		ui.printItemPath(file)
	}
}

func (ui *UI) printTotalItem(file fs.Item) {
	var lineFormat string
	if ui.UseColors {
		lineFormat = "%20s %s\n"
	} else {
		lineFormat = "%9s %s\n"
	}

	var size int64
	if ui.ShowApparentSize {
		size = file.GetSize()
	} else {
		size = file.GetUsage()
	}

	fmt.Fprintf(
		ui.output,
		lineFormat,
		ui.formatSize(size),
		file.GetName(),
	)
}

func (ui *UI) printItem(file fs.Item) {
	var lineFormat string
	if ui.showItemCnt {
		if ui.UseColors {
			lineFormat = "%s %23s %25s %s\n"
		} else {
			lineFormat = "%s %9s %11s %s\n"
		}
	} else {
		if ui.UseColors {
			lineFormat = "%s %23s %s\n"
		} else {
			lineFormat = "%s %9s %s\n"
		}
	}

	var size int64
	if ui.ShowApparentSize {
		size = file.GetSize()
	} else {
		size = file.GetUsage()
	}

	name := file.GetName()
	if file.IsDir() {
		name = ui.blue.Sprint("/" + file.GetName())
	}

	if ui.showItemCnt {
		fmt.Fprintf(
			ui.output,
			lineFormat,
			string(file.GetFlag()),
			ui.formatSize(size),
			ui.formatCount(file.GetItemCount()),
			name,
		)
		return
	}

	fmt.Fprintf(
		ui.output,
		lineFormat,
		string(file.GetFlag()),
		ui.formatSize(size),
		name,
	)
}

func (ui *UI) printItemPath(file fs.Item) {
	var lineFormat string
	if ui.UseColors {
		lineFormat = "%20s %s\n"
	} else {
		lineFormat = "%9s %s\n"
	}

	var size int64
	if ui.ShowApparentSize {
		size = file.GetSize()
	} else {
		size = file.GetUsage()
	}

	if file.IsDir() {
		fmt.Fprintf(ui.output,
			lineFormat,
			ui.formatSize(size),
			ui.blue.Sprint(file.GetPath()))
	} else {
		fmt.Fprintf(ui.output,
			lineFormat,
			ui.formatSize(size),
			file.GetPath())
	}
}

func (ui *UI) printDirWithDepth(dir fs.Item, currentDepth int) {
	// Print current directory
	ui.printItemPath(dir)

	// If we haven't reached the max depth, print contents
	if currentDepth < ui.depth && dir.IsDir() {
		sortOrder := fs.SortDesc
		if ui.reverseSort {
			sortOrder = fs.SortAsc
		}

		files := dir.GetFiles(fs.SortBySize, sortOrder)

		// Print all files at this depth level
		for file := range files {
			if file.IsDir() {
				// Recurse into subdirectories
				ui.printDirWithDepth(file, currentDepth+1)
			} else {
				// Print regular files
				ui.printItemPath(file)
			}
		}
	}
}

// ReadAnalysis reads analysis report from JSON file
func (ui *UI) ReadAnalysis(input io.Reader) error {
	var (
		dir      fs.Item
		wait     sync.WaitGroup
		err      error
		doneChan chan struct{}
	)

	sel := parquet.SnapshotSelector{
		Spec: ui.SnapshotSpec, Root: ui.SnapshotRoot,
		ExactTs: ui.SnapshotTs, ExactHost: ui.SnapshotHost,
	}
	// A multi-snapshot file loaded without --snapshot defaults to the latest; tell the
	// user (on stderr, so piped stdout data stays clean) which snapshot and how to
	// pick another.
	if note := report.MultiSnapshotNote(input, sel); note != "" {
		fmt.Fprintln(os.Stderr, "gdu: "+note)
	}

	if ui.ShowProgress {
		wait.Add(1)
		doneChan = make(chan struct{})
		go func() {
			defer wait.Done()
			ui.showReadingProgress(doneChan)
		}()
	}

	wait.Add(1)
	go func() {
		defer wait.Done()
		dir, err = report.ReadAnalysisWithSnapshot(input, sel)
		if err != nil {
			if ui.ShowProgress {
				doneChan <- struct{}{}
			}
			return
		}
		runtime.GC()

		if ui.IsFilteringFiles() {
			dir.UpdateStatsWithFileFiltering(make(fs.HardLinkedItems, 10))
		} else {
			dir.UpdateStats(make(fs.HardLinkedItems, 10))
		}

		if ui.ShowProgress {
			doneChan <- struct{}{}
		}
	}()

	wait.Wait()

	if err != nil {
		return err
	}

	ui.printResults(dir)

	return nil
}

func (ui *UI) showReadingProgress(doneChan chan struct{}) {
	emptyRow := "\r"
	for j := 0; j < 40; j++ {
		emptyRow += " "
	}

	i := 0
	for {
		fmt.Fprint(ui.output, emptyRow)

		select {
		case <-doneChan:
			fmt.Fprint(ui.output, "\r")
			return
		default:
		}

		fmt.Fprintf(ui.output, "\r %s ", string(progressRunes[i]))
		fmt.Fprint(ui.output, "Reading analysis from file...")

		time.Sleep(100 * time.Millisecond)
		i++
		i %= progressRunesCount
	}
}

func (ui *UI) updateProgress(updateStatsDone <-chan struct{}) {
	emptyRow := "\r"
	for j := 0; j < 100; j++ {
		emptyRow += " "
	}

	analysisDoneChan := ui.Analyzer.GetDone()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	i := 0
	for {
		select {
		case <-ticker.C:
			progress := ui.Analyzer.GetProgress()
			fmt.Fprint(ui.output, emptyRow)
			fmt.Fprintf(ui.output, "\r %s ", string(progressRunes[i]))
			fmt.Fprint(ui.output, "Scanning... Total items: "+
				ui.red.Sprint(common.FormatNumber(int64(progress.ItemCount)))+
				" size: "+
				ui.formatSize(progress.TotalUsage))
			i++
			i %= progressRunesCount
		case <-analysisDoneChan:
			ticker.Stop()
			fmt.Fprint(ui.output, emptyRow)
			for {
				fmt.Fprint(ui.output, emptyRow)
				fmt.Fprintf(ui.output, "\r %s ", string(progressRunes[i]))
				fmt.Fprint(ui.output, "Calculating disk usage...")
				time.Sleep(100 * time.Millisecond)
				i++
				i %= progressRunesCount

				select {
				case <-updateStatsDone:
					fmt.Fprint(ui.output, emptyRow)
					fmt.Fprint(ui.output, "\r")
					return
				default:
				}
			}
		}
	}
}

func (ui *UI) formatCount(count int64) string {
	count64 := float64(count)

	switch {
	case count64 >= common.G:
		return ui.red.Sprintf("%.1f", float64(count)/float64(common.G)) + "G"
	case count64 >= common.M:
		return ui.red.Sprintf("%.1f", float64(count)/float64(common.M)) + "M"
	case count64 >= common.K:
		return ui.red.Sprintf("%.1f", float64(count)/float64(common.K)) + "k"
	default:
		return ui.red.Sprintf("%d", count)
	}
}

func (ui *UI) formatSize(size int64) string {
	if ui.noPrefix {
		return ui.orange.Sprintf("%d", size)
	}
	if ui.fixedBase > 0 {
		val := float64(size) / ui.fixedBase
		return ui.orange.Sprintf("%.1f", val) + ui.fixedSuffix
	}
	if ui.UseSIPrefix {
		return ui.formatWithDecPrefix(size)
	}
	return ui.formatWithBinPrefix(size)
}

func (ui *UI) formatWithBinPrefix(size int64) string {
	fsize := float64(size)
	asize := math.Abs(fsize)

	switch {
	case asize >= common.Ei:
		return ui.orange.Sprintf("%.1f", fsize/common.Ei) + " EiB"
	case asize >= common.Pi:
		return ui.orange.Sprintf("%.1f", fsize/common.Pi) + " PiB"
	case asize >= common.Ti:
		return ui.orange.Sprintf("%.1f", fsize/common.Ti) + " TiB"
	case asize >= common.Gi:
		return ui.orange.Sprintf("%.1f", fsize/common.Gi) + " GiB"
	case asize >= common.Mi:
		return ui.orange.Sprintf("%.1f", fsize/common.Mi) + " MiB"
	case asize >= common.Ki:
		return ui.orange.Sprintf("%.1f", fsize/common.Ki) + " KiB"
	default:
		return ui.orange.Sprintf("%d", size) + " B"
	}
}

func (ui *UI) formatWithDecPrefix(size int64) string {
	fsize := float64(size)
	asize := math.Abs(fsize)

	switch {
	case asize >= common.E:
		return ui.orange.Sprintf("%.1f", fsize/common.E) + " EB"
	case asize >= common.P:
		return ui.orange.Sprintf("%.1f", fsize/common.P) + " PB"
	case asize >= common.T:
		return ui.orange.Sprintf("%.1f", fsize/common.T) + " TB"
	case asize >= common.G:
		return ui.orange.Sprintf("%.1f", fsize/common.G) + " GB"
	case asize >= common.M:
		return ui.orange.Sprintf("%.1f", fsize/common.M) + " MB"
	case asize >= common.K:
		return ui.orange.Sprintf("%.1f", fsize/common.K) + " kB"
	default:
		return ui.orange.Sprintf("%d", size) + " B"
	}
}

func maxLength(list []*device.Device, keyGetter func(*device.Device) string) int {
	maxLen := 0
	var s string
	for _, item := range list {
		s = keyGetter(item)
		if len(s) > maxLen {
			maxLen = len(s)
		}
	}
	return maxLen
}

func maxInt(x, y int) int {
	if x > y {
		return x
	}
	return y
}
