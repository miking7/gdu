package tui

import (
	"fmt"
	"time"

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
	historyHint = " gdu ~ [ back in time · S compare · ? help "

	// headerTimeLayout renders snapshot timestamps in header/footer copy
	// ("2026-06-19 15:30"); headerDateLayout is the compact-prefix form.
	headerTimeLayout = "2006-01-02 15:04"
	headerDateLayout = "2006-01-02"

	// headerRootMaxLen caps the snapshot root shown on the Viewing line so the
	// key hints stay on screen.
	headerRootMaxLen = 40
)

// updateHeader renders the two-slot header for the current View/Baseline state
// and grows the header row to two lines only while both slots are
// active. Safe to call before the widgets exist (options run during CreateUI).
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

	switch {
	case viewing == "" && baseline == "":
		if ui.coveringHint {
			ui.header.SetText(historyHint)
		} else {
			ui.header.SetText(upstreamHint)
		}
	case viewing == "":
		ui.header.SetText(baseline)
	case baseline == "":
		ui.header.SetText(viewing)
	default:
		ui.header.SetText(viewing + "\n" + baseline)
	}

	lines := 1
	if viewing != "" && baseline != "" {
		lines = 2
	}
	ui.setHeaderHeight(lines)
}

// viewingLine renders the Viewing slot, or "" when the View is live (the
// default slot). The Esc hint appears only when it is the innermost layer: no
// Baseline set, and a return view to go back to.
func (ui *UI) viewingLine() string {
	if ui.viewIsLive() {
		return ""
	}
	v := ui.currentView
	var what string
	if v.snapshot != nil {
		what = fmt.Sprintf("snapshot %s · %s · read-only",
			v.snapshot.ScanTs.Local().Format(headerTimeLayout),
			path.ShortenPath(v.snapshot.ScanRoot, headerRootMaxLen))
	} else {
		what = fmt.Sprintf("import %s · read-only", v.importLabel)
	}
	hints := "[ older · ] newer"
	if !ui.inDiffMode() && ui.returnView != nil && ui.currentView != ui.returnView {
		hints += " · Esc return"
	}
	return fmt.Sprintf(" Viewing  %-52s %s", what, hints)
}

// baselineLine renders the Baseline slot, or "" when no baseline is set.
func (ui *UI) baselineLine() string {
	if !ui.inDiffMode() {
		return ""
	}
	what := fmt.Sprintf("snapshot %s · Δ shown", ui.baselineTs.Local().Format(headerTimeLayout))
	return fmt.Sprintf(" Baseline %-52s %s", what, "> < sort · Esc clear")
}

// setHeaderHeight resizes the grid's header row (1↔2 lines), preserving the
// status-bar row when it is shown.
func (ui *UI) setHeaderHeight(lines int) {
	if ui.grid == nil || ui.headerHidden || lines == ui.headerLines {
		return
	}
	ui.headerLines = lines
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
// for header-hidden configs, so the mode is never invisible.
func (ui *UI) dirLabelPrefix() string {
	if !ui.headerHidden {
		return ""
	}
	prefix := ""
	if !ui.viewIsLive() {
		v := ui.currentView
		if v.snapshot != nil {
			prefix += fmt.Sprintf("[snapshot %s] ", v.snapshot.ScanTs.Local().Format(headerDateLayout))
		} else {
			prefix += fmt.Sprintf("[import %s] ", v.importLabel)
		}
	}
	if ui.inDiffMode() {
		prefix += fmt.Sprintf("[Δ vs %s] ", ui.baselineTs.Local().Format(headerDateLayout))
	}
	return prefix
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
	return fmt.Sprintf(" live · scanned %s — r to refresh", scannedAt.Local().Format("15:04"))
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
		folded := ui.liveSavedValid && !ui.liveDiverged.Load() &&
			covering[i].Key() == ui.liveSavedKey
		if !folded {
			return true
		}
	}
	return false
}
