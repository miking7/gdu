package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/pkg/parquet"
)

// compactingIndicatorText is shown at the right edge of the footer while the
// background auto-compaction runs.
const compactingIndicatorText = " compacting archive… "

// autoCompactQuitPage is the page name of the wait/abort modal.
const autoCompactQuitPage = "autocompact-quit"

// startAutoCompact launches the process's single opportunistic archive
// compaction on its own goroutine, right after the initial scan's snapshot
// was saved (unless --no-auto-compact). The UI never waits on it: a footer indicator
// appears while it runs and quitting mid-run offers wait/abort (abort is
// always safe — the tmp file is discarded and source deletion is the final,
// idempotent step).
func (ui *UI) startAutoCompact() {
	if !ui.ClaimAutoCompactRun() {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	ui.autoCompactCancel = cancel
	ui.autoCompactDone = make(chan struct{})
	// The Store publishes the two fields above to any reader that first sees
	// autoCompactRunning == true (atomics carry the happens-before edge).
	ui.autoCompactRunning.Store(true)
	ui.app.QueueUpdateDraw(ui.rebuildFooter) // show the indicator

	go func() {
		defer close(ui.autoCompactDone)
		if _, err := parquet.AutoCompact(ctx, ui.SnapshotsDir, time.Now()); err != nil {
			log.Printf("auto-compact stopped: %s", err) // includes user aborts (context.Canceled)
		}
		ui.autoCompactRunning.Store(false)
		// The event loop is still running here on every quit path (quits wait
		// for this goroutine via waitThenQuit before calling Stop), so this
		// QueueUpdateDraw is always drained and never blocks post-Stop.
		ui.app.QueueUpdateDraw(ui.rebuildFooter) // remove the indicator
	}()
}

// quitApp leaves the application. It first raises a quit confirmation when
// something would actually be lost — an in-flight scan that is recording or has
// run a noticeable while, or a completed scan whose results were never saved —
// then offers wait/abort when a background compaction is still running.
// printPath additionally prints the current dir on exit (the 'Q' binding).
func (ui *UI) quitApp(printPath bool) {
	// Never stack a second quit modal, and never let 'q' fall through an open
	// delete-confirm and quit out from under it — input capture is app-level, so
	// handleQuit runs even while that modal is focused.
	if ui.pages.HasPage("confirm") ||
		ui.pages.HasPage(scanQuitPage) ||
		ui.pages.HasPage(autoCompactQuitPage) {
		return
	}
	if ui.shouldConfirmQuit() {
		ui.showScanQuitModal(printPath)
		return
	}
	ui.quitAppRest(printPath)
}

// shouldConfirmQuit reports whether quitting now would discard work worth a
// confirmation. Mid-scan: a recording scan (any duration) or any scan that has
// run past the threshold. Post-scan: a completed scan whose results were not
// recorded as a snapshot and that took past the threshold — when a snapshot
// captured the scan, quitting loses nothing and is silent. --no-confirm-quit
// (confirmQuit == false) disables all of it, including the recording-scan case.
func (ui *UI) shouldConfirmQuit() bool {
	if !ui.confirmQuit {
		return false
	}
	if ui.scanning {
		return ui.scanIsRecording() || time.Since(ui.scanStart) >= confirmQuitMinScanDuration
	}
	return ui.currentDir != nil && ui.unsavedScanDuration >= confirmQuitMinScanDuration
}

// scanIsRecording reports whether an in-flight scan would be saved as a
// snapshot on completion — so abandoning it loses a recording and is worth
// confirming. Transient scans (r-refreshes, spot-rescans) and snapshot-disabled
// scans record nothing. Esc uses this to raise the same confirmation as 'q'
// without ever hard-quitting mid-scan (Esc never causes an unconfirmed exit).
func (ui *UI) scanIsRecording() bool {
	return ui.scanning && ui.SaveSnapshotEnabled && !ui.scanTransient
}

// quitAppRest continues the quit past the scan confirmation.
func (ui *UI) quitAppRest(printPath bool) {
	if ui.autoCompactRunning.Load() {
		ui.showAutoCompactQuitModal(printPath)
		return
	}
	if ui.snapshotWorkActive.Load() > 0 {
		ui.drainSnapshotWorkThenQuit(printPath)
		return
	}
	ui.finishQuit(printPath)
}

// scanQuitPage is the page name of the quit-mid-scan confirmation modal.
const scanQuitPage = "scan-quit"

// showScanQuitModal confirms a quit that would lose work. The wording tracks
// what is actually at risk: an in-flight recording scan (a snapshot would be
// saved on completion), an in-flight non-recording scan (transient, or saving
// disabled), or a completed scan whose results were never saved. "Stay" is the
// default; "Quit, don't ask again" also clears confirmQuit for the rest of the
// session (a session-only choice, never written to config).
func (ui *UI) showScanQuitModal(printPath bool) {
	var text string
	switch {
	case ui.scanIsRecording():
		text = "Scan incomplete — quit without saving a snapshot?\n\n" +
			"Running for " + time.Since(ui.scanStart).Round(time.Second).String() +
			". Quitting now records nothing; a completed scan is saved for you."
	case ui.scanning:
		text = "This scan has been running for " +
			time.Since(ui.scanStart).Round(time.Second).String() + ".\n\n" +
			"Quitting now abandons it."
	default:
		text = "Quit gdu?\n\n" +
			"This scan took " + ui.unsavedScanDuration.Round(time.Second).String() +
			" and its results were not saved.\n" +
			"Choose Stay to press E and export them, or enable save-snapshots to archive scans."
	}
	modal := tview.NewModal().
		SetText(text).
		AddButtons([]string{"Stay", "Quit", "Quit, don't ask again"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			ui.pages.RemovePage(scanQuitPage)
			switch buttonIndex {
			case 2: // Quit, don't ask again
				ui.confirmQuit = false
				fallthrough
			case 1: // Quit
				ui.quitAppRest(printPath)
			default: // Stay (index 0) and Esc (-1)
				ui.app.SetFocus(ui.table)
			}
		})
	if !ui.UseColors {
		modal.SetBackgroundColor(tcell.ColorGray)
	} else {
		modal.SetBackgroundColor(tcell.ColorBlack)
	}
	modal.SetBorderColor(tcell.ColorDefault)
	ui.pages.AddPage(scanQuitPage, modal, true, true)
}

// finishQuit stops the app exactly once (the quit-key, signal, wait and abort
// paths can all reach it) and emits the exit output.
func (ui *UI) finishQuit(printPath bool) {
	ui.quitOnce.Do(func() {
		ui.app.Stop()
		ui.printMarkedPaths()
		if printPath {
			fmt.Fprintf(ui.output, "%s\n", ui.currentDirPath)
		}
	})
}

// waitThenQuit waits for the background compaction goroutine to finish
// unwinding (its final footer update runs while the event loop is still
// draining, so no QueueUpdate deadlocks after Stop), then quits. It must run
// on its own goroutine, never on the event loop, or it would block the very
// loop the compaction goroutine needs to drain its update.
func (ui *UI) waitThenQuit(printPath bool) {
	<-ui.autoCompactDone
	ui.app.QueueUpdateDraw(func() { ui.finishQuit(printPath) })
}

// showAutoCompactQuitModal asks what to do about the in-flight compaction:
// wait for it to finish, abort it (safe by design), or Esc to keep browsing.
// Both wait and abort defer the actual quit to waitThenQuit so the compaction
// goroutine always tears down cleanly (lockfile released, tmp discarded)
// before the app stops.
func (ui *UI) showAutoCompactQuitModal(printPath bool) {
	modal := tview.NewModal().
		SetText("Archive compaction is running in the background.\n" +
			"Aborting is safe: nothing is deleted until a month is fully merged and verified.").
		AddButtons([]string{"Wait, then quit", "Abort and quit"}).
		SetDoneFunc(func(index int, _ string) {
			ui.pages.RemovePage(autoCompactQuitPage)
			switch index {
			case 0: // wait for completion, then quit
				go ui.waitThenQuit(printPath)
			case 1: // cancel the run, then quit once it has unwound
				ui.autoCompactCancel()
				go ui.waitThenQuit(printPath)
			default: // Esc: stay in the app; the compaction keeps running
				ui.app.SetFocus(ui.table)
			}
		})

	if !ui.UseColors {
		modal.SetBackgroundColor(tcell.ColorGray)
	} else {
		modal.SetBackgroundColor(tcell.ColorBlack)
	}
	modal.SetBorderColor(tcell.ColorDefault)

	ui.pages.AddPage(autoCompactQuitPage, modal, true, true)
}

// handleShutdownSignal quits in response to SIGINT/SIGTERM etc. It cancels an
// in-flight compaction cooperatively (so the lockfile and tmp are cleaned up)
// and routes through finishQuit's sync.Once, so a racing quit-key press never
// double-prints the marked paths.
func (ui *UI) handleShutdownSignal() {
	if ui.autoCompactRunning.Load() {
		ui.autoCompactCancel()
		go ui.waitThenQuit(false)
		return
	}
	// Hop to the event loop: draining picker work reads/writes event-loop-only
	// state (the shutdown flag and the size-reader cancel).
	ui.app.QueueUpdateDraw(func() {
		if ui.snapshotWorkActive.Load() > 0 {
			ui.drainSnapshotWorkThenQuit(false)
			return
		}
		ui.finishQuit(false)
	})
}
