package tui

import (
	"fmt"
	"time"

	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/pkg/path"
	"github.com/dundee/gdu/v5/report"
)

// Header copy. One line per non-default slot; zero new chrome when
// live-and-plain.
const (
	// upstreamHint is today's single header line, kept for first-run (no
	// covering history yet).
	upstreamHint = " gdu ~ Use arrow keys to navigate, press ? for help "
	// historyHint replaces it once covering history exists for the shown root.
	historyHint = " gdu ~ [ back in time · { compare · O snapshots · ? help "

	// headerTimeLayout renders snapshot timestamps in header/footer copy
	// ("2026-06-19 15:30"); headerDateLayout is the compact-prefix form and
	// headerClockLayout the wall clock of a live scan ("14:02"). headerMonthDayLayout
	// is the terse "07-14" the completion-growth flash names its since-date with.
	headerTimeLayout     = "2006-01-02 15:04"
	headerDateLayout     = "2006-01-02"
	headerClockLayout    = "15:04"
	headerMonthDayLayout = "01-02"

	// headerRootMaxLen caps the snapshot root shown on the Viewing line so the
	// key hints stay on screen.
	headerRootMaxLen = 40

	// baselinePausedTail is the ◇ line's tail while the comparison is suspended by
	// a running scan at the live position: the partial tree never diffs (a preview
	// renders plain), and the diff resumes automatically on completion. It names
	// neither Tab nor Esc because, at the live position of a scan, both mean
	// something other than the compare toggles (Tab enters/leaves the preview, Esc
	// backs out of the scan).
	baselinePausedTail = "Δ paused — resumes when the scan completes"
)

// Role glyphs. Every screen shows a Viewing tree, optionally against a
// Baseline; the two are asymmetric — Viewing is the room you stand in, Baseline
// a reference overlay — so their markers are too: solid versus hollow. The
// shapes alone carry the distinction, which is what keeps --no-color complete
// and lets the header stay a single-style band. (Coloring them would mean
// turning on tview's dynamic colors there, and the header's copy is full of
// literal bracket key names — "[ ] step" — that the tag parser would then have
// to be escaped around, for a distinction the shapes already make.)

// viewingGlyph marks the tree being browsed. Falls back to ASCII under
// --no-unicode, which the app carries on the same flag as the size bar.
func (ui *UI) viewingGlyph() string {
	if ui.useOldSizeBar {
		return "*"
	}
	return "●"
}

// baselineGlyph marks the snapshot being compared against.
func (ui *UI) baselineGlyph() string {
	if ui.useOldSizeBar {
		return "o"
	}
	return "◇"
}

// snapshotViewLabel renders the "snapshot <time> · <root> · read-only" phrase on
// the Viewing line. The tree-view header and the browser both name a snapshot the
// same way, so they share this to keep the copy from drifting.
func snapshotViewLabel(scanTs time.Time, scanRoot string) string {
	return fmt.Sprintf("snapshot %s · %s · read-only",
		scanTs.Local().Format(headerTimeLayout),
		path.ShortenPath(scanRoot, headerRootMaxLen))
}

// viewingFrame and baselineFrame wrap a body in the header's two role lines: the
// glyph, the role word, and hand-tuned padding that aligns both bodies in the
// same column ("Viewing" + 3 spaces and "Baseline" + 2 both reach column 10). The
// tree-view header and the browser share them so that alignment cannot drift.
func (ui *UI) viewingFrame(body string) string {
	return fmt.Sprintf(" %s Viewing   %s", ui.viewingGlyph(), body)
}

func (ui *UI) baselineFrame(body string) string {
	return fmt.Sprintf(" %s Baseline  %s", ui.baselineGlyph(), body)
}

// updateHeader renders the two-slot header for the current View/Baseline state
// and grows the header row to two lines whenever a Baseline is set. Safe to
// call before the widgets exist (options run during CreateUI).
func (ui *UI) updateHeader() {
	if ui.header == nil {
		return
	}
	if ui.headerHidden {
		return // the dir-label prefix carries the state instead
	}
	if ui.headerNotice != "" {
		return // a transient notice owns the header; it re-renders on expiry
	}

	viewing := ui.viewingLine()
	baseline := ui.baselineLine()

	lines := 1
	switch {
	case baseline != "":
		// A set Baseline always states both sides of the comparison, even when
		// the tree being viewed is the live disk — otherwise the most common
		// compare state never says what is being compared with what. The
		// Viewing line is non-empty by construction whenever this branch runs.
		ui.header.SetText(viewing + "\n" + baseline)
		lines = 2
	case viewing != "":
		ui.header.SetText(viewing)
	case ui.coveringHint:
		ui.header.SetText(historyHint)
	default:
		ui.header.SetText(upstreamHint)
	}
	ui.setHeaderHeight(lines)
}

// viewingLine renders the Viewing slot. It is empty only in the default state —
// a live tree with no Baseline — where the single hint line owns the header. The
// Esc hint appears only when it is the innermost layer: no Baseline set (Esc
// clears that first), and a return view to go back to.
func (ui *UI) viewingLine() string {
	if ui.viewIsLive() && !ui.inDiffMode() {
		return ""
	}
	what := ui.viewingWhat()
	if ui.viewIsLive() {
		return ui.viewingFrame(what)
	}
	hints := "[ ] step"
	if !ui.inDiffMode() && ui.returnView != nil && ui.currentView != ui.returnView {
		hints += " · Esc return"
	}
	return ui.viewingFrame(fmt.Sprintf("%s — %s", what, hints))
}

// viewingWhat names the tree being viewed: an archived snapshot, an
// identity-less import, or the live disk with the root and time of its scan. A
// UI that predates the view model (device list, tests driving currentDir
// directly) is live with whatever root it has and no scan time.
func (ui *UI) viewingWhat() string {
	v := ui.currentView
	switch {
	case v != nil && v.snapshot != nil:
		return snapshotViewLabel(v.snapshot.ScanTs, v.snapshot.ScanRoot)
	case v != nil && v.importLabel != "":
		return fmt.Sprintf("import %s · read-only", v.importLabel)
	}
	what := "live"
	root := ui.topDirPath
	if v != nil && v.topPath != "" {
		root = v.topPath
	}
	if root != "" {
		what += " " + path.ShortenPath(root, headerRootMaxLen)
	}
	if v != nil && !v.scannedAt.IsZero() {
		what += " — scanned " + v.scannedAt.Local().Format(headerClockLayout)
	}
	return what
}

// baselinePaused reports whether the ◇ comparison is suspended: the user is at
// the live position of a running scan — the progress screen or its mid-scan
// preview — where there is no complete live tree to diff against. Stepped into
// the past mid-scan the front page is a finished snapshot View, not the live
// position, so the full compare grammar applies there and the tail stays normal.
func (ui *UI) baselinePaused() bool {
	return ui.scanning && !ui.scanPageHidden
}

// baselineLine renders the Baseline slot, or "" when no baseline is set. The
// tail names the one view toggle (Δ rendering) and how to flip it, so the
// comparison is never silently alive and its state is always readable — except
// while a scan suspends it, when the tail says so and names neither toggle.
func (ui *UI) baselineLine() string {
	if !ui.inDiffMode() {
		return ""
	}
	when := fmt.Sprintf("%s (%s ago)",
		ui.baselineTs.Local().Format(headerTimeLayout),
		humanAge(time.Since(ui.baselineTs)))
	if ui.baselinePaused() {
		return ui.baselineFrame(when + " — " + baselinePausedTail)
	}
	tail := "Δ shown · Tab plain"
	if ui.diffHidden {
		tail = "Δ hidden · Tab compare"
	}
	return ui.baselineFrame(when + " — " + tail + " · Esc clear")
}

// setHeaderHeight resizes the grid's header row (1↔2 lines), preserving the
// status-bar row when it is shown. Before the grid exists it just records the
// height, which createGrid then lays the row out at.
func (ui *UI) setHeaderHeight(lines int) {
	if ui.headerHidden || lines == ui.headerLines {
		return
	}
	ui.headerLines = lines
	if ui.grid == nil {
		return
	}
	ui.statusMut.RLock()
	statusShown := ui.status != nil
	ui.statusMut.RUnlock()
	if statusShown {
		ui.grid.SetRows(lines, 1, 0, 1, 1)
	} else {
		ui.grid.SetRows(lines, 1, 0, 1)
	}
}

// dirLabelPrefix carries the View/Baseline state into the directory-label line
// for header-hidden configs, so the mode is never invisible. It follows the
// header's rule exactly: the Viewing slot appears whenever the header would
// render it, which includes a live tree once a Baseline is set — the
// compared-from side is never the missing one.
func (ui *UI) dirLabelPrefix() string {
	if !ui.headerHidden {
		return ""
	}
	prefix := ""
	if !ui.viewIsLive() || ui.inDiffMode() {
		prefix += fmt.Sprintf("[%s %s] ", ui.viewingGlyph(), ui.compactViewLabel())
	}
	if ui.inDiffMode() {
		prefix += fmt.Sprintf("[%s %s Δ] ", ui.baselineGlyph(),
			ui.baselineTs.Local().Format(headerDateLayout))
	}
	return prefix
}

// compactViewLabel names the viewed tree in the few characters the
// header-hidden prefix can spare.
func (ui *UI) compactViewLabel() string {
	v := ui.currentView
	switch {
	case v != nil && v.snapshot != nil:
		return "snapshot " + v.snapshot.ScanTs.Local().Format(headerDateLayout)
	case v != nil && v.importLabel != "":
		return "import " + v.importLabel
	}
	return "live"
}

// snapshotFooter renders the always-on footer for a snapshot View:
// this folder's size at the viewed moment.
func (ui *UI) snapshotFooter() string {
	v := ui.currentView
	size := ui.plainSize(ui.currentDir.GetUsage())
	if v.snapshot != nil {
		return fmt.Sprintf(" this folder: %s at %s",
			size, v.snapshot.ScanTs.Local().Format(headerTimeLayout))
	}
	return fmt.Sprintf(" this folder: %s · import %s", size, v.importLabel)
}

// liveSwitchFooter renders the footer flashed when stepping (or going live)
// lands back on the in-memory live tree.
func liveSwitchFooter(scannedAt time.Time) string {
	return fmt.Sprintf(" live · scanned %s — r to refresh", scannedAt.Local().Format(headerClockLayout))
}

// completionGrowthFlash renders the scan-completion footer flash: how much the
// scanned root grew since the previous same-root snapshot, the terse since-date,
// and the key that starts the comparison. delta is signed (the root's total now
// minus that snapshot's total_dsize).
func (ui *UI) completionGrowthFlash(delta int64, since time.Time) string {
	sign := "+"
	if delta < 0 {
		sign = minusSign
	}
	return fmt.Sprintf(" %s%s since %s — { to compare",
		sign, ui.plainSize(absInt64(delta)), since.Local().Format(headerMonthDayLayout))
}

// setFooter renders the footer's resting text and remembers it, so transient
// flashes restore the real state afterwards — not whatever text (possibly a
// previous flash) happened to precede them.
func (ui *UI) setFooter(text string) {
	ui.footerBase = text
	ui.footerLabel.SetText(text)
}

// flashFooter shows text in the footer for a few seconds, then restores the
// resting footer — unless something else rewrote the footer in the meantime,
// in which case the newer text wins. Call on the event loop.
func (ui *UI) flashFooter(text string) {
	ui.footerLabel.SetText(text)
	go func() {
		time.Sleep(3 * time.Second)
		ui.app.QueueUpdateDraw(func() {
			if ui.footerLabel.GetText(false) != text {
				return // the footer moved on; don't clobber it
			}
			ui.footerLabel.SetText(ui.footerBase)
		})
	}()
}

// refreshCoveringHint asynchronously checks whether the archive holds
// walkable history covering target and switches the plain live header to the
// context-aware hint when it does. Runs the listing off the event
// loop; must be called on the event loop.
func (ui *UI) refreshCoveringHint(target string) {
	if ui.snapshotsDir == "" || target == "" {
		return
	}
	devices, getter := ui.devices, ui.getter // captured on the loop for off-loop mount resolution
	ui.goPickerWork(func() {
		covering, err := ui.coveringForTarget(target, devices, getter)
		ui.app.QueueUpdateDraw(func() {
			has := err == nil && ui.hasWalkableHistory(covering)
			if ui.coveringHint != has {
				ui.coveringHint = has
				ui.updateHeader()
			}
		})
	})
}

// hasWalkableHistory reports whether any covering snapshot is actually a
// timeline point to step into. The snapshot just saved from the unchanged
// live tree folds into the live position — it is the present, not
// history — so a first-ever scan keeps the upstream hint. Must run on
// the event loop.
func (ui *UI) hasWalkableHistory(covering []report.SnapshotListing) bool {
	for i := range covering {
		if !ui.snapshotFoldsIntoLive(covering[i].Key()) {
			return true
		}
	}
	return false
}

// snapshotFoldsIntoLive reports whether the snapshot with this identity is the
// one just saved from the still-unchanged live tree — the present, not history.
// The timeline drops it (the live position represents it) and the browser folds
// it into the live row for the same reason. Must run on the event loop.
func (ui *UI) snapshotFoldsIntoLive(key parquet.SnapshotKey) bool {
	return ui.liveSavedValid && !ui.liveDiverged.Load() && key == ui.liveSavedKey
}
