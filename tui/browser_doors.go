package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/dundee/gdu/v5/build"
	"github.com/dundee/gdu/v5/pkg/parquet"
	"github.com/dundee/gdu/v5/pkg/path"
	"github.com/dundee/gdu/v5/report"
)

// showSnapshotBrowser opens the unified browser over the current folder — the
// tree view's O (● focused) and B (◇ focused) doors. It lists the archive off
// the event loop behind a loading page, splits it into the snapshots that cover
// this folder (both cursors) and the rest (● view-only), and pins the live tree
// as the first row. Must be called on the event loop.
func (ui *UI) showSnapshotBrowser(focus browserFocus) {
	if ui.currentDir == nil {
		return
	}
	ui.pages.RemovePage("info")
	if ui.snapshotsDir == "" {
		ui.showErr("No snapshot archive", fmt.Errorf("no snapshots directory is configured"))
		return
	}

	target := ui.currentDirPath
	wantSel := ui.selectedItemName()
	nowSize := ui.currentDir.GetUsage()
	live := ui.browserLiveRow(target, nowSize)
	devices, getter := ui.devices, ui.getter // captured on the loop for off-loop mount resolution

	ui.showLoadingPage("Reading snapshots...", " Snapshots ")
	ui.goPickerWork(func() {
		listings, err := report.ListSnapshotsInDir(ui.snapshotsDir)
		mount := mountForTarget(devices, getter, target)
		ui.app.QueueUpdateDraw(func() {
			ui.pages.RemovePage(loadingPage)
			switch {
			case err != nil:
				ui.showErr("Error reading snapshot archive", err)
			case len(listings) == 0:
				ui.showErr("No snapshots", fmt.Errorf("the snapshot archive %s holds no snapshots", ui.snapshotsDir))
			default:
				ui.openTreeBrowser(focus, target, wantSel, live, listings, mount)
			}
		})
	})
}

// openTreeBrowser builds and shows the tree-view browser from a listed archive.
func (ui *UI) openTreeBrowser(
	focus browserFocus, target, wantSel string,
	live *browserLive, listings []report.SnapshotListing, mount string,
) {
	var covering, others []report.SnapshotListing
	for i := range listings {
		switch {
		case !report.RootCoversWithinMount(listings[i].ScanRoot, target, mount):
			others = append(others, listings[i])
		case live != nil && ui.snapshotFoldsIntoLive(listings[i].Key()):
			// The snapshot just saved from the still-unchanged live tree is the
			// present, not history: the live row represents it, so it is not
			// listed a second time (matching the timeline's fold).
			continue
		default:
			covering = append(covering, listings[i])
		}
	}

	shortTarget := strings.TrimPrefix(target, build.RootPathPrefix)
	cfg := &browserConfig{
		scopeLabel:   path.ShortenPath(abbrevHome(shortTarget, homeDir()), 48),
		covering:     covering,
		otherRoots:   others,
		live:         live,
		fillTarget:   target,
		initialFocus: focus,
		curViewLive:  ui.viewIsLive(),
		hasBaseline:  ui.inDiffMode(),
		baselineKey:  ui.baselineKey,
		hint: func(l *report.SnapshotListing) string {
			return fmt.Sprintf(" gdu --snapshot %s %s",
				parquet.FormatSnapshotTime(&l.SnapshotInfo), l.ScanRoot)
		},
		openView: func(l *report.SnapshotListing, then func()) {
			ui.openSnapshotView(l, target, wantSel, false, then)
		},
		goLive:        func(then func()) { ui.goLiveHereThen(then) },
		applyBaseline: func(l *report.SnapshotListing) { ui.setBaselineFromListing(l) },
		clearBaseline: func() { ui.clearBaseline() },
	}
	if v := ui.currentView; v != nil && v.snapshot != nil {
		cfg.curViewKey = v.snapshot.Key()
	}
	ui.showBrowser(cfg)
}

// browserLiveRow describes the live row pinned first in the tree-view browser:
// the shown folder's live disk usage and when the live tree was scanned. It is
// nil only when there is no live tree to go to. When a snapshot View is shown,
// the size comes from the live tree descended to the folder (the folder's live
// size, not the snapshot's), or 0 when the live tree does not cover it.
func (ui *UI) browserLiveRow(folder string, nowSize int64) *browserLive {
	if lv := ui.liveView; lv != nil {
		size := nowSize
		if !ui.viewIsLive() {
			if dir, exact := descendToPath(lv.tree, lv.topPath, folder); exact {
				size = dir.GetUsage()
			} else {
				size = 0
			}
		}
		return &browserLive{scannedAt: lv.scannedAt, size: size}
	}
	if ui.viewIsLive() {
		return &browserLive{scannedAt: ui.currentViewScannedAt(), size: nowSize}
	}
	return nil
}

// currentViewScannedAt is the scan time of the current live View, or the zero
// time for a legacy UI that predates the view model.
func (ui *UI) currentViewScannedAt() time.Time {
	if v := ui.currentView; v != nil {
		return v.scannedAt
	}
	return time.Time{}
}
