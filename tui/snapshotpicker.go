package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/pkg/path"
	"github.com/dundee/gdu/v5/report"
)

// Picker "This folder" cell states before/instead of a resolved size.
const (
	snapshotSizePlaceholder = "…" // size still being read in the background
	snapshotErrorMarker     = "?" // the containing snapshot file could not be read
	snapshotAbsentMarker    = "—" // the folder did not exist in that snapshot

	// pickerRootWidth caps the browser's Root column, home-abbreviated then
	// shortened (head + leaf) so a long scan root reads instead of hard-clipping.
	pickerRootWidth = 40
)

// showStartupSnapshotPicker presents the snapshots held in a multi-snapshot
// Parquet file opened with -f, so the user can choose which to load — the Open
// picker seeded with that file. Enter loads the highlighted snapshot as
// the View; Esc/q quits. The hint line shows the exact `--snapshot` invocation
// for the highlighted snapshot, so the interactive choice teaches the
// scriptable one.
func (ui *UI) showStartupSnapshotPicker(f *os.File, snapshots []parquet.SnapshotInfo) {
	ordered := make([]parquet.SnapshotInfo, len(snapshots))
	copy(ordered, snapshots)
	sort.SliceStable(ordered, func(i, j int) bool { // newest first
		return ordered[i].ScanTs.After(ordered[j].ScanTs)
	})
	listings := make([]report.SnapshotListing, len(ordered))
	for i := range ordered {
		listings[i] = report.SnapshotListing{SnapshotInfo: ordered[i]}
	}

	ui.showBrowser(&browserConfig{
		scopeLabel: filepath.Base(f.Name()),
		covering:   listings,
		escQuits:   true, // the startup chooser has nothing to fall back to
		hint: func(l *report.SnapshotListing) string {
			return fmt.Sprintf(" gdu -f %s --snapshot %s", f.Name(), parquet.FormatSnapshotTime(&l.SnapshotInfo))
		},
		// No applyBaseline hook: the file's snapshots are the ● view and there is
		// nothing to compare against, which is exactly what viewOnly() keys off.
		openView: func(l *report.SnapshotListing, _ func()) {
			ui.loadSnapshotFromFile(f, &l.SnapshotInfo)
		},
	})
}

// closeSnapshotPicker dismisses the browser and stops its background size fill:
// bumping the generation drops any queued cell updates, and cancelling the
// context stops the reader mid-archive.
func (ui *UI) closeSnapshotPicker() {
	ui.snapshotPickerGen++
	if ui.snapshotSizeCancel != nil {
		ui.snapshotSizeCancel()
		ui.snapshotSizeCancel = nil
	}
	ui.browser = nil
	ui.pages.RemovePage("snapshotpicker")
}

// coveringListings lists the archived snapshots that mount-accurately cover
// target, newest first — the browser's and timeline's membership rule. A
// "/" snapshot no longer pollutes the list for a folder on another volume. mount
// is target's most-specific mount point ("" degrades to plain path-covering). It
// performs file I/O and must run off the tview event loop.
func (ui *UI) coveringListings(target, mount string) ([]report.SnapshotListing, error) {
	listings, err := report.ListSnapshotsInDir(ui.snapshotsDir)
	if err != nil {
		return nil, err
	}
	var covering []report.SnapshotListing
	for i := range listings {
		if report.RootCoversWithinMount(listings[i].ScanRoot, target, mount) {
			covering = append(covering, listings[i])
		}
	}
	return covering, nil
}

// coveringForTarget lists target's mount-accurate covering snapshots,
// resolving target's mount from devices/getter first. Callers capture
// devices/getter on the event loop (ui.devices, ui.getter) and run this off it,
// so a background archive read never touches live UI state.
func (ui *UI) coveringForTarget(
	target string, devices device.Devices, getter device.DevicesInfoGetter,
) ([]report.SnapshotListing, error) {
	return ui.coveringListings(target, mountForTarget(devices, getter, target))
}

// mountForTarget resolves target's most-specific mount point for the D17
// covering clamp. It prefers the device list captured when the launcher ran
// (devices); when the launcher was skipped (-f, --read-from-storage,
// launcher:false) that list is empty, so it falls back to a fresh getter query.
// Either failure leaves mount "" and covering degrades to path-covering. It may
// do I/O (the getter query) and so must run off the event loop; the caller
// captures devices/getter on the loop and passes them in.
func mountForTarget(devices device.Devices, getter device.DevicesInfoGetter, target string) string {
	if len(devices) == 0 && getter != nil {
		if mounts, err := getter.GetMounts(); err == nil {
			devices = mounts
		} else {
			log.Printf("snapshot covering: mount query failed, using path-covering: %s", err)
		}
	}
	if d := device.ForPath(devices, target); d != nil {
		return d.MountPoint
	}
	return ""
}

// pickerSizeCell renders a size in the device-table amber (plain without colors).
func (ui *UI) pickerSizeCell(size int64) string {
	if ui.UseColors {
		return "[" + deviceSizeColor + "::b]" + ui.plainSize(size)
	}
	return ui.plainSize(size)
}

// pickerRootCell renders a snapshot's scan root home-abbreviated and
// width-shortened, in the device-table blue (plain without colors).
func (ui *UI) pickerRootCell(root string) string {
	p := path.ShortenPath(abbrevHome(root, homeDir()), pickerRootWidth)
	if ui.UseColors {
		return "[" + deviceNameColor + "::b]" + p
	}
	return p
}

// pickerHostCell renders a foreign host name bold — shown only for
// snapshots taken on another machine, so it should draw the eye.
func (ui *UI) pickerHostCell(host string) string {
	if ui.UseColors {
		return "[::b]" + host
	}
	return host
}

// dim wraps s in the picker's dim gray tag (ages, placeholders, the hint line),
// or returns it unchanged without colors.
func (ui *UI) dim(s string) string {
	return ui.dimTag() + s
}

// pickerDelta renders the current-vs-snapshot change for the browser's Δ column,
// warm for growth and cool for shrink.
func (ui *UI) pickerDelta(delta int64) string {
	if delta == 0 {
		return "0"
	}
	sign, color := "+", diffGrowColor
	if delta < 0 {
		sign, color = minusSign, diffShrinkColor
	}
	if ui.UseColors {
		return fmt.Sprintf("[%s::b]%s%s", color, sign, ui.plainSize(absInt64(delta)))
	}
	return sign + ui.plainSize(absInt64(delta))
}

// setBaselineFromListing enters diff mode against the chosen archived snapshot. The
// baseline load reads the whole scan tree, so it runs off the event loop behind a
// loading page — a compacted whole-disk snapshot never freezes the UI on select.
func (ui *UI) setBaselineFromListing(l *report.SnapshotListing) {
	listing := *l // copy; the goroutine outlives the covering slice's row
	ui.showLoadingPage("Loading baseline...", " Baseline ")
	ui.goPickerWork(func() {
		b, err := ui.loadBaseline(&listing)
		ui.app.QueueUpdateDraw(func() {
			ui.pages.RemovePage(loadingPage)
			if err != nil {
				ui.showErr("Error loading baseline", err)
			} else {
				ui.SetBaseline(b, &listing.SnapshotInfo)
			}
			ui.app.SetFocus(ui.table)
		})
	})
}

// openSnapshotFromListing loads the chosen archived snapshot as the View: read
// off the event loop behind the loading page, then shown with the current folder
// preserved when the snapshot covers it.
func (ui *UI) openSnapshotFromListing(l *report.SnapshotListing) {
	ui.openSnapshotView(l, ui.currentDirPath, ui.selectedItemName(), false, nil)
}

// openSnapshotView loads listing as the View, off the event loop behind the
// loading page, preserving wantPath/wantSel (nearest ancestor when the snapshot
// doesn't reach wantPath). When setReturn is true and the session has no return
// view yet, the loaded snapshot becomes it — so a launcher s/S can be the View
// the session launched into (the set-return-view-if-unset rule). then, when
// non-nil, runs on the event loop right after the view is applied — the browser
// uses it to set a pending baseline on top of the freshly loaded View. It is
// shared by the browser and the launcher; capture wantPath/wantSel at the call
// site (on the event loop) before it runs.
func (ui *UI) openSnapshotView(l *report.SnapshotListing, wantPath, wantSel string, setReturn bool, then func()) {
	listing := *l // copy; the goroutine outlives the listings slice
	ui.resetTimeline()
	gen := ui.stepGen

	ui.showLoadingPage("Loading snapshot...", " Snapshots ")
	ui.goPickerWork(func() {
		tree, err := ui.loadListingTree(&listing)
		ui.app.QueueUpdateDraw(func() {
			ui.pages.RemovePage(loadingPage)
			if gen != ui.stepGen {
				return // superseded while loading
			}
			if err != nil {
				ui.showErr("Error loading snapshot", err)
				ui.app.SetFocus(ui.table)
				return
			}
			info := listing.SnapshotInfo
			v := &view{tree: tree, topPath: listing.ScanRoot, snapshot: &info}
			if setReturn && ui.returnView == nil {
				ui.returnView = v
			}
			ui.applyView(v, wantPath, wantSel)
			if then != nil {
				then()
			}
		})
	})
}

// loadBaseline opens the listing's snapshot file and indexes the baseline. It is
// synchronous; setBaselineFromListing runs it off the event loop.
func (ui *UI) loadBaseline(l *report.SnapshotListing) (*analyze.Baseline, error) {
	f, err := os.Open(filepath.Join(ui.snapshotsDir, l.File)) //nolint:gosec // archive path, read-only
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return report.BuildBaselineForSnapshot(f, &l.SnapshotInfo)
}

// goPickerWork runs fn on a background goroutine tracked so a quit can wait for it
// to drain its final QueueUpdateDraw before Stop (see drainSnapshotWorkThenQuit).
// It is a no-op once a drain has begun, so no new work races that wait. Must be
// called on the event loop.
func (ui *UI) goPickerWork(fn func()) {
	if ui.snapshotShuttingDown {
		return
	}
	ui.snapshotWork.Add(1)
	ui.snapshotWorkActive.Add(1)
	go func() {
		defer ui.snapshotWork.Done()
		defer ui.snapshotWorkActive.Add(-1)
		fn()
	}()
}

// drainSnapshotWorkThenQuit stops new picker work, cancels the in-flight size
// reader, waits for the fill/load goroutines to drain their final QueueUpdateDraw
// (which runs while the event loop is still alive), then quits — so none blocks
// after Stop. Call on the event loop; the wait hands off to its own goroutine,
// never the loop the workers need to drain onto.
func (ui *UI) drainSnapshotWorkThenQuit(printPath bool) {
	ui.snapshotShuttingDown = true
	if ui.snapshotSizeCancel != nil {
		ui.snapshotSizeCancel()
	}
	go func() {
		ui.snapshotWork.Wait()
		ui.app.QueueUpdateDraw(func() { ui.finishQuit(printPath) })
	}()
}
