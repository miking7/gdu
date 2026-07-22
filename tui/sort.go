package tui

import (
	"sort"

	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/fs"
)

const (
	nameSortKey      = "name"
	sizeSortKey      = "size"
	itemCountSortKey = "itemCount"
	mtimeSortKey     = "mtime"
	// deltaSortKey sorts the compare view by growth versus the baseline. It has
	// no fs.SortBy equivalent (the delta is computed against the baseline, not a
	// property of the item), so the compare renderer sorts its own rows by it.
	deltaSortKey = "delta"

	ascOrder  = "asc"
	descOrder = "desc"
)

// SetDefaultSorting sets the default sorting
func (ui *UI) SetDefaultSorting(by, order string) {
	if by != "" {
		ui.defaultSortBy = by
	}
	if order == ascOrder || order == descOrder {
		ui.defaultSortOrder = order
	}
}

func (ui *UI) setSorting(newOrder string) {
	// A re-sort reorders the rows; both the mark and ignore maps are keyed by row
	// index, so both must reset or a mark/ignore would silently move onto another
	// item — the same invariant every mode transition upholds.
	ui.resetRowSelection()

	// Per-mode memory: the compare view keeps its own (sortBy, order) so plain
	// and compare each stay exactly as last sorted across a Tab toggle. Re-press
	// flips direction; a new key starts ascending (the app-wide convention).
	by, order := &ui.sortBy, &ui.sortOrder
	if ui.renderingDelta() {
		by, order = &ui.diffSortBy, &ui.diffSortOrder
	}

	if newOrder == *by {
		if *order == ascOrder {
			*order = descOrder
		} else {
			*order = ascOrder
		}
	} else {
		*by = newOrder
		*order = ascOrder
	}

	if ui.currentDir != nil {
		ui.showDir()
	} else if ui.devices != nil && (newOrder == sizeSortKey || newOrder == nameSortKey) {
		ui.showDevices()
	}
}

// getSortParams returns the current sort parameters as fs.SortBy and fs.SortOrder
func (ui *UI) getSortParams() (fs.SortBy, fs.SortOrder) {
	var sortBy fs.SortBy
	switch ui.sortBy {
	case nameSortKey:
		sortBy = fs.SortByName
	case itemCountSortKey:
		sortBy = fs.SortByItemCount
	case mtimeSortKey:
		sortBy = fs.SortByMtime
	case sizeSortKey:
		if ui.ShowApparentSize {
			sortBy = fs.SortByApparentSize
		} else {
			sortBy = fs.SortBySize
		}
	default:
		sortBy = fs.SortBySize
	}

	sortOrder := fs.SortAsc
	if ui.sortOrder == descOrder {
		sortOrder = fs.SortDesc
	}

	return sortBy, sortOrder
}

func (ui *UI) sortDevices() {
	if ui.sortBy == sizeSortKey {
		if ui.sortOrder == descOrder {
			sort.Sort(sort.Reverse(device.ByUsedSize(ui.devices)))
		} else {
			sort.Sort(device.ByUsedSize(ui.devices))
		}
	}
	if ui.sortBy == nameSortKey {
		if ui.sortOrder == descOrder {
			sort.Sort(sort.Reverse(device.ByName(ui.devices)))
		} else {
			sort.Sort(device.ByName(ui.devices))
		}
	}
}
