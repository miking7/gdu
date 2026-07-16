package analyze

import (
	"path/filepath"
	"strings"

	"github.com/dundee/gdu/v5/pkg/fs"
)

// DiffCategory classifies how one item changed between a baseline snapshot and
// the current tree.
type DiffCategory int

const (
	// DiffUnchanged marks an item present in both with identical disk usage.
	DiffUnchanged DiffCategory = iota
	// DiffGrew marks an item present in both that is larger now.
	DiffGrew
	// DiffShrank marks an item present in both that is smaller now.
	DiffShrank
	// DiffNew marks an item absent from a baseline that captured everything
	// (threshold 0), so it certainly did not exist then; the whole current size
	// is the delta.
	DiffNew
	// DiffApprox marks an item absent from a thresholded baseline, so it may have
	// existed below the threshold — the reported delta is an upper bound.
	DiffApprox
	// DiffUncovered marks a path outside the baseline's scan root, about which the
	// baseline makes no claim.
	DiffUncovered
)

// Baseline is a past snapshot indexed for O(1) path lookups, used to annotate
// the current tree with per-item growth or shrinkage. It is built once when the
// user sets a baseline and is read-only thereafter.
//
// Sizes are disk usage: a directory's size is its recursive usage
// (dir_total_dsize, reconstructed by UpdateStats), a file's or rollup bucket's
// its own dsize. gdu stores no recursive apparent size, so diffing is
// disk-usage-based regardless of the apparent-size view toggle.
type Baseline struct {
	sizes     map[string]int64    // absolute path -> disk usage
	dirs      map[string]struct{} // set of absolute paths that are directories
	children  map[string][]string // parent path -> child absolute paths, in stored order
	root      string              // scan_root: the baseline's coverage boundary (cleaned)
	threshold int64               // threshold_bytes: 0 means the baseline kept everything
}

// RemovedEntry is one baseline item that no longer exists under a directory in
// the current tree.
type RemovedEntry struct {
	Path  string
	Name  string
	Size  int64 // the item's disk usage in the baseline
	IsDir bool
}

// BuildBaseline indexes a loaded baseline tree. scanRoot and thresholdBytes come
// from the snapshot's SnapshotInfo and drive the new/approximate/uncovered
// distinctions. The tree must already have had UpdateStats called (so directory
// usages are recursive), as the normal load path does. The tree is walked once
// and may be discarded by the caller afterwards.
func BuildBaseline(root fs.Item, scanRoot string, thresholdBytes int64) *Baseline {
	b := &Baseline{
		sizes:     make(map[string]int64),
		dirs:      make(map[string]struct{}),
		children:  make(map[string][]string),
		root:      filepath.Clean(scanRoot),
		threshold: thresholdBytes,
	}
	b.index(root)
	return b
}

func (b *Baseline) index(item fs.Item) {
	path := item.GetPath()
	b.sizes[path] = item.GetUsage()
	if !item.IsDir() {
		return
	}
	b.dirs[path] = struct{}{}
	for child := range item.GetFiles(fs.SortByNone, fs.SortAsc) {
		b.children[path] = append(b.children[path], child.GetPath())
		b.index(child)
	}
}

// Threshold reports the baseline snapshot's rollup threshold in bytes (0 = kept
// everything).
func (b *Baseline) Threshold() int64 { return b.threshold }

// Delta returns the change in the given item's disk usage versus the baseline
// and how to classify it. A positive delta is growth, negative is shrinkage.
func (b *Baseline) Delta(item fs.Item) (delta int64, cat DiffCategory) {
	path := item.GetPath()
	cur := item.GetUsage()
	if base, ok := b.sizes[path]; ok {
		delta = cur - base
		switch {
		case delta > 0:
			return delta, DiffGrew
		case delta < 0:
			return delta, DiffShrank
		default:
			return 0, DiffUnchanged
		}
	}
	// The item is not in the baseline: either it is outside what the baseline
	// captured (uncovered), or it appeared since (new / approximate, depending on
	// whether the baseline could have held it below its threshold).
	if !b.covers(path) {
		return 0, DiffUncovered
	}
	if b.threshold > 0 {
		return cur, DiffApprox
	}
	return cur, DiffNew
}

// RemovedUnder returns the baseline items directly under parentPath that are
// absent from present (the set of current child absolute paths under the same
// directory) — i.e. what was there in the baseline and is gone now. The
// synthetic "<smaller objects>" rollup bucket is never reported as removed: it
// is an aggregate, not a real object, and a live (unthresholded) current tree
// legitimately has no such child.
func (b *Baseline) RemovedUnder(parentPath string, present map[string]struct{}) []RemovedEntry {
	var out []RemovedEntry
	for _, childPath := range b.children[parentPath] {
		if _, ok := present[childPath]; ok {
			continue
		}
		if filepath.Base(childPath) == SmallObjectsName {
			continue
		}
		_, isDir := b.dirs[childPath]
		out = append(out, RemovedEntry{
			Path:  childPath,
			Name:  filepath.Base(childPath),
			Size:  b.sizes[childPath],
			IsDir: isDir,
		})
	}
	return out
}

// covers reports whether path lies within the baseline's scan root, and so
// whether the baseline's silence about a path means "did not exist" rather than
// "never looked".
func (b *Baseline) covers(path string) bool {
	p := filepath.Clean(path)
	if p == b.root {
		return true
	}
	// Match paths under the root. Append a separator so a sibling that merely
	// shares a name prefix ("/home/mike2" under root "/home/mike") is excluded —
	// but only when the cleaned root doesn't already end in one, as volume roots
	// do ("/" on unix, "C:\" on Windows). Without this, "/"+sep would be "//" and
	// wrongly exclude every child.
	sep := string(filepath.Separator)
	prefix := b.root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(p, prefix)
}
