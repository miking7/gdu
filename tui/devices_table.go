package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rivo/tview"

	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/path"
)

// Shared device-table rendering: the classic device page (showDevices) and the
// launcher render device rows through the same helpers so the two never drift
// in columns, coloring, or width handling.

// deviceTableColumns is the classic 6-column device layout (Device name, Size,
// Used, Used part, Free, Mount point). The launcher inserts a Snapshot column
// before Mount point, shifting the mount column right by one.
const deviceMountCol = 5

// launcherFallbackWidth is the assumed terminal width when the screen size
// is unknown (e.g. under the simulation screen in tests).
const launcherFallbackWidth = 100

// Device-table palette (shared so the launcher, classic -d page, and the
// snapshot pickers never drift): blue for names/mount points, amber for sizes.
const (
	deviceNameColor = "#3498db"
	deviceSizeColor = "#edb20a"
)

// deviceTableColors returns the (name, size) color tags for the device table.
func (ui *UI) deviceTableColors() (nameColor, sizeColor string) {
	if ui.UseColors {
		return "[" + deviceNameColor + ":-:b]", "[" + deviceSizeColor + ":-:b]"
	}
	return "[white:-:b]", "[white:-:b]"
}

// setDeviceHeaderCells writes the device-column headers into row 0 of table,
// putting Mount point at mountCol (5 classic, 6 with the launcher's Snapshot
// column). All header cells are non-selectable so the cursor skips row 0.
func setDeviceHeaderCells(table *tview.Table, mountCol int) {
	for col, h := range []string{"Device name", "Size", "Used", "Used part", "Free"} {
		table.SetCell(0, col, tview.NewTableCell(h).SetSelectable(false))
	}
	table.SetCell(0, mountCol, tview.NewTableCell("Mount point").SetSelectable(false))
}

// setDeviceRowCells writes a device's Name/Size/Used/Used-part/Free cells into
// table row [0..4] and its Mount point into table[row][mountCol], the mount
// point shortened to mountWidth so a long one reads (head + leaf via
// path.ShortenPath) instead of hard-clipping mid-character at the screen edge.
// The row's selection reference is the device, set on column 0.
func (ui *UI) setDeviceRowCells(
	table *tview.Table, row, mountCol, mountWidth int, d *device.Device, nameColor, sizeColor string,
) {
	table.SetCell(row, 0, tview.NewTableCell(nameColor+d.Name).SetReference(d))
	table.SetCell(row, 1, tview.NewTableCell(ui.formatSize(d.Size, false, true)))
	table.SetCell(row, 2, tview.NewTableCell(sizeColor+ui.formatSize(d.Size-d.Free, false, true)))
	table.SetCell(row, 3, tview.NewTableCell(getDeviceUsagePart(d, ui.useOldSizeBar)))
	table.SetCell(row, 4, tview.NewTableCell(ui.formatSize(d.Free, false, true)))
	table.SetCell(row, mountCol, tview.NewTableCell(nameColor+path.ShortenPath(d.MountPoint, mountWidth)))
}

// deviceMountWidth is how many columns the Mount point column (and the
// launcher's spliced folder path) may use, scaled to the terminal so long paths
// shorten instead of hard-clipping at the right edge. It measures the actual
// device-name column from names and reserves fixed room for the numeric columns
// (and the Snapshot column when shown); floored so it stays readable and falls
// back to a default when the screen size is unknown (tests).
func (ui *UI) deviceMountWidth(devices device.Devices, hasSnapshotCol bool) int {
	width := launcherFallbackWidth
	if ui.screen != nil {
		if w, _ := ui.screen.Size(); w > 0 {
			width = w
		}
	}
	nameW := len("Device name")
	for _, d := range devices {
		if n := len(d.Name); n > nameW {
			nameW = n
		}
	}
	// size + used + bar + free, plus inter-column spacing (tview pads between
	// columns), then the Snapshot column. Estimated a touch generously so a long
	// mount shortens (head + leaf) rather than hard-clipping at the screen edge.
	reserved := nameW + 11 + 11 + 14 + 11 + 12
	if hasSnapshotCol {
		reserved += len("snapshot 999 days ago") + 2
	}
	if m := width - reserved; m > 20 {
		return m
	}
	return 20
}

// dimTag returns the color tag used for the launcher's dim role note
// ("(current folder)"), or "" without colors.
func (ui *UI) dimTag() string {
	if ui.UseColors {
		return "[gray::d]"
	}
	return ""
}

// homeDir returns the user's home directory, or "" when it can't be determined.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// abbrevHome renders p with a leading ~ when it is at or under home, for display.
func abbrevHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
