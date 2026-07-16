package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/build"
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

// showSnapshotPicker opens the Baseline picker (key S): the snapshots in the
// archive that cover the current folder, newest first. The list appears at
// once; each row's folder size then and its change since now fill in behind it
// as the archive is read, so a large (e.g. compacted whole-disk) archive never
// keeps the picker waiting. Enter sets the highlighted snapshot as the diff
// baseline.
func (ui *UI) showSnapshotPicker() {
	if ui.currentDir == nil {
		return
	}
	ui.pages.RemovePage("info") // never stack the picker over an open info overlay
	if ui.snapshotsDir == "" {
		ui.showErr("No snapshot archive", fmt.Errorf("no snapshots directory is configured"))
		return
	}

	target := ui.currentDirPath
	nowSize := ui.currentDir.GetUsage()
	devices, getter := ui.devices, ui.getter // captured on the loop for off-loop mount resolution

	// Listing the archive reads each file's footer; brief, but done off the event
	// loop behind a loading page so the UI never stalls.
	ui.showLoadingPage("Reading snapshots...", " Snapshots ")
	ui.goPickerWork(func() {
		covering, err := ui.coveringForTarget(target, devices, getter)
		ui.app.QueueUpdateDraw(func() {
			ui.pages.RemovePage(loadingPage)
			switch {
			case err != nil:
				ui.showErr("Error reading snapshot archive", err)
			case len(covering) == 0:
				ui.showErr("No snapshots", fmt.Errorf("no archived snapshot covers %s", target))
			default:
				shortTarget := strings.TrimPrefix(target, build.RootPathPrefix)
				ui.buildPicker(&pickerConfig{
					// A long path would push "Baseline" off the centered title.
					title: fmt.Sprintf(
						" Baseline for %s (%d) — Enter to set, Esc to cancel ",
						path.ShortenPath(shortTarget, 48), len(covering)),
					listings: covering,
					hint: func(l *report.SnapshotListing) string {
						return fmt.Sprintf(" gdu --baseline %s %s",
							parquet.FormatSnapshotTime(&l.SnapshotInfo), shortTarget)
					},
					onSelect:  func(l *report.SnapshotListing) { ui.setBaselineFromListing(l) },
					fillSizes: true,
					target:    target,
					nowSize:   nowSize,
				})
			}
		})
	})
}

// showOpenPicker opens the Open/View picker (key O): every snapshot in the
// archive, all roots and dates, newest first — the long jump. Enter opens the
// highlighted snapshot as the View.
func (ui *UI) showOpenPicker() {
	ui.pages.RemovePage("info")
	if ui.snapshotsDir == "" {
		ui.showErr("No snapshot archive", fmt.Errorf("no snapshots directory is configured"))
		return
	}

	ui.showLoadingPage("Reading snapshots...", " Snapshots ")
	ui.goPickerWork(func() {
		listings, err := report.ListSnapshotsInDir(ui.snapshotsDir)
		ui.app.QueueUpdateDraw(func() {
			ui.pages.RemovePage(loadingPage)
			switch {
			case err != nil:
				ui.showErr("Error reading snapshot archive", err)
			case len(listings) == 0:
				ui.showErr("No snapshots", fmt.Errorf("the snapshot archive %s holds no snapshots", ui.snapshotsDir))
			default:
				ui.buildPicker(&pickerConfig{
					title:    fmt.Sprintf(" Open a snapshot (%d) — Enter to open, Esc to cancel ", len(listings)),
					listings: listings,
					hint: func(l *report.SnapshotListing) string {
						return fmt.Sprintf(" gdu --snapshot %s %s",
							parquet.FormatSnapshotTime(&l.SnapshotInfo), l.ScanRoot)
					},
					onSelect: func(l *report.SnapshotListing) { ui.openSnapshotFromListing(l) },
				})
			}
		})
	})
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
	ui.pages.RemovePage("snapshotpicker")
}

// coveringListings lists the archived snapshots that mount-accurately cover
// target, newest first — the S picker's and timeline's membership rule. A
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

// baselineMarker returns a picker row's leading marker: a filled dot on the
// active baseline row, else blank padding of the same width so columns line up.
// The glyph falls back to ASCII '*' under --no-unicode.
func (ui *UI) baselineMarker(active bool) string {
	if !active {
		return "  "
	}
	glyph := "●"
	if ui.useOldSizeBar { // --no-unicode
		glyph = "*"
	}
	if ui.UseColors {
		return "[" + deviceSizeColor + "::b]" + glyph + "[-:-:-] "
	}
	return glyph + " "
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

// openSnapshotFromListing loads the chosen archived snapshot as the View (the
// O picker's Enter): read off the event loop behind the loading page, then
// shown with the current folder preserved when the snapshot covers it.
func (ui *UI) openSnapshotFromListing(l *report.SnapshotListing) {
	ui.openSnapshotView(l, ui.currentDirPath, ui.selectedItemName(), false)
}

// openSnapshotView loads listing as the View, off the event loop behind the
// loading page, preserving wantPath/wantSel (nearest ancestor when the snapshot
// doesn't reach wantPath). When setReturn is true and the session has no return
// view yet, the loaded snapshot becomes it — so a launcher s/S can be the View
// the session launched into (the set-return-view-if-unset rule). It
// is shared by the O picker and the launcher; capture wantPath/wantSel at the
// call site (on the event loop) before it runs.
func (ui *UI) openSnapshotView(l *report.SnapshotListing, wantPath, wantSel string, setReturn bool) {
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
