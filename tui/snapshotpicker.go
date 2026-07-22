package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/common"
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

	// pickerRootWidth caps the picker's Root column, home-abbreviated then
	// shortened (head + leaf) so a long scan root reads instead of hard-clipping.
	pickerRootWidth = 40
)

// One picker component, three configurations: Baseline (S: covering roots,
// "this folder then / Δ", Enter sets the Baseline), Open (O: all roots and
// dates, Enter sets the View — the long jump), and Startup file (a
// multi-snapshot -f: Open seeded with that file).
// pickerConfig parameterizes the shared table + async fill + hint line.
type pickerConfig struct {
	title    string
	listings []report.SnapshotListing
	// hint renders the teaching line for the highlighted snapshot — the CLI
	// equivalent of the interactive choice.
	hint func(l *report.SnapshotListing) string
	// onSelect fires on Enter with the highlighted snapshot.
	onSelect func(l *report.SnapshotListing)
	// escQuits makes Esc/q quit the app (the startup chooser has nothing to
	// fall back to); otherwise they close the picker.
	escQuits bool
	// fillSizes starts the Baseline picker's asynchronous "this folder then /
	// Δ vs now" column fill for target (sized against nowSize).
	fillSizes bool
	target    string
	nowSize   int64
	// refocus is the primitive to focus when the picker closes without a
	// selection (Esc/q); nil focuses the main table. The launcher's S picker
	// sets it to the launcher table so Esc returns to the launcher, not the
	// empty grid behind it.
	refocus tview.Primitive
}

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

	ui.buildPicker(&pickerConfig{
		title:    fmt.Sprintf(" Select a snapshot (%d) — Enter to load, Esc to quit ", len(listings)),
		listings: listings,
		hint: func(l *report.SnapshotListing) string {
			return fmt.Sprintf(" gdu -f %s --snapshot %s", f.Name(), parquet.FormatSnapshotTime(&l.SnapshotInfo))
		},
		onSelect: func(l *report.SnapshotListing) {
			ui.closeSnapshotPicker()
			ui.loadSnapshotFromFile(f, &l.SnapshotInfo)
		},
		escQuits: true,
	})
}

// buildPicker builds and shows the shared picker: the snapshot table, the
// teaching hint line, and — for the Baseline configuration — the asynchronous
// folder-size fill.
func (ui *UI) buildPicker(cfg *pickerConfig) {
	ui.snapshotPickerGen++ // a fresh picker; invalidate any prior fill's updates

	table := tview.NewTable().SetSelectable(true, false)
	table.SetBackgroundColor(tcell.ColorDefault)
	table.SetBorder(true).SetTitle(cfg.title)
	if ui.UseColors {
		table.SetSelectedStyle(tcell.Style{}.
			Foreground(ui.selectedTextColor).
			Background(ui.selectedBackgroundColor).Bold(true))
	}

	rows := ui.fillPickerRows(table, cfg)

	hint := tview.NewTextView().SetDynamicColors(true)
	hint.SetBackgroundColor(tcell.ColorDefault)
	updateHint := func(row int) {
		idx := row - 1 // row 0 is the header
		if idx < 0 || idx >= len(cfg.listings) {
			return
		}
		hint.SetText(ui.dim(cfg.hint(&cfg.listings[idx])))
	}
	table.SetSelectionChangedFunc(func(row, _ int) { updateHint(row) })

	table.SetSelectedFunc(func(row, _ int) {
		idx := row - 1
		if idx < 0 || idx >= len(cfg.listings) {
			return
		}
		listing := cfg.listings[idx] // copy; onSelect may outlive the picker
		if !cfg.escQuits {
			ui.closeSnapshotPicker()
		}
		cfg.onSelect(&listing)
	})
	table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEsc || event.Rune() == 'q' {
			if cfg.escQuits {
				ui.finishQuit(false)
				if ui.done != nil {
					ui.done <- struct{}{}
				}
				return nil
			}
			ui.closeSnapshotPicker()
			focus := tview.Primitive(ui.table)
			if cfg.refocus != nil {
				focus = cfg.refocus
			}
			ui.app.SetFocus(focus)
			return nil
		}
		return event
	})

	flex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(table, 0, 1, true).
		AddItem(hint, 1, 0, false)

	ui.pages.AddPage("snapshotpicker", flex, true, true)
	selectRow := 1
	if r := ui.activeBaselineRow(cfg); r > 0 {
		selectRow = r // reopen on the active baseline
	}
	table.Select(selectRow, 0)
	updateHint(selectRow)
	ui.app.SetFocus(table)

	if cfg.fillSizes {
		ui.startSnapshotSizeFill(table, cfg.listings, cfg.target, cfg.nowSize, rows)
	}
}

// closeSnapshotPicker dismisses the picker and stops its background size fill:
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

// fillPickerRows lays out the header and one row per snapshot, styled to match
// the device table: a dim age on When, device-amber sizes, a mount-blue
// Root column, and a bold Host column shown only when some snapshot is
// foreign. The Baseline configuration gets placeholder folder-size cells (filled
// asynchronously) and returns the identity→rows index the fill updates; the Open
// configurations show each snapshot's total size directly. The active baseline's
// row is marked.
func (ui *UI) fillPickerRows(table *tview.Table, cfg *pickerConfig) map[parquet.SnapshotKey][]int {
	localHost := common.HostnameBestEffort()
	showHost := false
	for i := range cfg.listings {
		if common.HostIsForeign(cfg.listings[i].Host, localHost) {
			showHost = true
		}
	}

	var headers []string
	if cfg.fillSizes {
		headers = []string{"When", "This folder", "Δ vs now", "Root"}
	} else {
		headers = []string{"When", "Size", "Root"}
	}
	rootCol := len(headers) - 1
	hostCol := -1
	if showHost {
		hostCol = len(headers)
		headers = append(headers, "Host")
	}
	for col, h := range headers {
		table.SetCell(0, col, tview.NewTableCell(h).SetSelectable(false).SetAttributes(tcell.AttrBold))
	}

	activeRow := ui.activeBaselineRow(cfg)
	rows := make(map[parquet.SnapshotKey][]int, len(cfg.listings))
	for i := range cfg.listings {
		l := &cfg.listings[i]
		row := i + 1
		table.SetCell(row, 0, tview.NewTableCell(ui.pickerWhenCell(l, row == activeRow)))
		if cfg.fillSizes {
			table.SetCell(row, 1, tview.NewTableCell(ui.dim(snapshotSizePlaceholder)))
			table.SetCell(row, 2, tview.NewTableCell(""))
			rows[l.Key()] = append(rows[l.Key()], row)
		} else {
			table.SetCell(row, 1, tview.NewTableCell(ui.pickerSizeCell(l.TotalDsize)))
		}
		table.SetCell(row, rootCol, tview.NewTableCell(ui.pickerRootCell(l.ScanRoot)))
		if hostCol >= 0 {
			host := ""
			if common.HostIsForeign(l.Host, localHost) {
				host = ui.pickerHostCell(l.Host)
			}
			table.SetCell(row, hostCol, tview.NewTableCell(host))
		}
	}
	return rows
}

// activeBaselineRow returns the 1-based table row of the picker's active
// baseline — the snapshot currently set as Baseline — or 0 when none of
// the listings is it. Only the Baseline (S) picker tracks an active row.
func (ui *UI) activeBaselineRow(cfg *pickerConfig) int {
	if !cfg.fillSizes || !ui.inDiffMode() {
		return 0
	}
	for i := range cfg.listings {
		if cfg.listings[i].Key() == ui.baselineKey {
			return i + 1
		}
	}
	return 0
}

// pickerWhenCell renders a snapshot's timestamp with a dim relative-age suffix,
// prefixed by the active-baseline marker (blank padding otherwise, so the
// timestamps stay column-aligned).
func (ui *UI) pickerWhenCell(l *report.SnapshotListing, active bool) string {
	when := parquet.FormatSnapshotTime(&l.SnapshotInfo)
	age := ui.dim("(" + humanAge(time.Since(l.ScanTs)) + " ago)")
	return ui.baselineMarker(active) + when + "  " + age
}

// baselineMarker returns a picker row's leading marker: the hollow Baseline
// glyph on the row currently set as Baseline, else blank padding of the same
// width so columns line up. The glyph is the same one the header's Baseline
// line carries, so one shape means one role everywhere.
func (ui *UI) baselineMarker(active bool) string {
	if !active {
		return "  "
	}
	if ui.UseColors {
		return "[" + deviceNameColor + "::b]" + ui.baselineGlyph() + "[-:-:-] "
	}
	return ui.baselineGlyph() + " "
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

// startSnapshotSizeFill reads each covering snapshot's folder size in the background
// and fills the matching rows as sizes resolve; snapshots whose file can't be read are
// marked unreadable, and any scan still unresolved at the end had no row for the
// folder (absent). Stale updates (the picker was closed or replaced) are dropped
// by the generation guard.
func (ui *UI) startSnapshotSizeFill(
	table *tview.Table, covering []report.SnapshotListing, target string, nowSize int64,
	rows map[parquet.SnapshotKey][]int,
) {
	ctx, cancel := context.WithCancel(context.Background())
	ui.snapshotSizeCancel = cancel
	gen := ui.snapshotPickerGen
	resolved := make(map[parquet.SnapshotKey]bool, len(rows))

	// apply runs update on the event loop only while this picker is still current.
	apply := func(update func()) {
		ui.app.QueueUpdateDraw(func() {
			if ui.snapshotPickerGen == gen {
				update()
			}
		})
	}

	ui.goPickerWork(func() {
		report.FolderSizesEach(ctx, ui.snapshotsDir, covering, target,
			func(key parquet.SnapshotKey, size int64) {
				apply(func() {
					resolved[key] = true
					for _, row := range rows[key] {
						ui.setPickerSize(table, row, size, nowSize)
					}
				})
			},
			func(key parquet.SnapshotKey) {
				apply(func() {
					resolved[key] = true
					for _, row := range rows[key] {
						ui.setPickerCells(table, row, snapshotErrorMarker)
					}
				})
			})
		// Any snapshot still unresolved has no row for target — show it as absent.
		apply(func() {
			for key, rowList := range rows {
				if resolved[key] {
					continue
				}
				for _, row := range rowList {
					ui.setPickerCells(table, row, snapshotAbsentMarker)
				}
			}
		})
	})
}

// setPickerSize writes a resolved folder size (device-amber) and its change
// versus now into a picker row.
func (ui *UI) setPickerSize(table *tview.Table, row int, size, nowSize int64) {
	table.SetCell(row, 1, tview.NewTableCell(ui.pickerSizeCell(size)))
	table.SetCell(row, 2, tview.NewTableCell(ui.pickerDelta(nowSize-size)))
}

// setPickerCells fills a picker row's size and Δ columns with the same marker,
// used for the absent ("—", dim) and unreadable ("?", red) states.
func (ui *UI) setPickerCells(table *tview.Table, row int, marker string) {
	cell := ui.dim(marker)
	if marker == snapshotErrorMarker && ui.UseColors {
		cell = "[red::b]" + marker
	}
	table.SetCell(row, 1, tview.NewTableCell(cell))
	table.SetCell(row, 2, tview.NewTableCell(cell))
}

// pickerDelta renders the current-vs-snapshot change for the picker's Δ column,
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
