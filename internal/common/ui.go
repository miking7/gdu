// Package common contains commong logic and interfaces used across Gdu
// nolint: revive //Why: this is common package
package common

import (
	"regexp"
	"strconv"
	"sync/atomic"
	"time"
)

// UI struct
type UI struct {
	Analyzer              Analyzer
	IgnoreDirPaths        map[string]struct{}
	IgnoreDirPathPatterns *regexp.Regexp
	IgnoreHidden          bool
	IgnoreTypes           []string
	IncludeTypes          []string
	UseColors             bool
	UseSIPrefix           bool
	ShowProgress          bool
	ShowApparentSize      bool
	ShowRelativeSize      bool
	FilteringFiles        bool
	ExportThreshold       int64
	SaveSnapshotEnabled   bool
	SnapshotsDir          string
	SnapshotThreshold     int64
	SnapshotSpec          string    // --snapshot: which snapshot to load from a multi-snapshot input
	SnapshotRoot          string    // --snapshot-root: disambiguating scan_root filter
	SnapshotHost          string    // with SnapshotTs: full-identity pin from archive resolution
	SnapshotTs            time.Time // zero unless SetSnapshotIdentity pinned a snapshot
	AutoCompactEnabled    bool
	autoCompactClaimed    atomic.Bool
	// nestedMountPaths holds the mount points nested under the root of the
	// current scan (see SetNestedMountPaths). It is scan state, replaced per
	// scan, and deliberately not the user's ignore configuration.
	nestedMountPaths map[string]struct{}
}

// SetAnalyzer sets analyzer instance
func (ui *UI) SetAnalyzer(a Analyzer) {
	ui.Analyzer = a
}

// SetFollowSymlinks sets whether symlinks to files should be followed
func (ui *UI) SetFollowSymlinks(v bool) {
	ui.Analyzer.SetFollowSymlinks(v)
}

// SetShowAnnexedSize sets whether to use annexed size of git-annex files
func (ui *UI) SetShowAnnexedSize(v bool) {
	ui.Analyzer.SetShowAnnexedSize(v)
}

// SetTimeFilter sets the time filter function for file inclusion
func (ui *UI) SetTimeFilter(timeFilter TimeFilter) {
	ui.Analyzer.SetTimeFilter(timeFilter)
	ui.FilteringFiles = true
}

// SetArchiveBrowsing sets whether browsing of zip/jar archives is enabled
func (ui *UI) SetArchiveBrowsing(v bool) {
	ui.Analyzer.SetArchiveBrowsing(v)
}

// SetExportThreshold sets the size threshold (in bytes) below which objects are
// bucketed into a "<smaller objects>" rollup on export. 0 disables the rollup.
func (ui *UI) SetExportThreshold(v int64) {
	ui.ExportThreshold = v
}

// SetSaveSnapshot enables auto-saving each completed scan to dir as a Parquet
// snapshot, bucketing objects below thresholdBytes.
func (ui *UI) SetSaveSnapshot(dir string, thresholdBytes int64) {
	ui.SaveSnapshotEnabled = true
	ui.SnapshotsDir = dir
	ui.SnapshotThreshold = thresholdBytes
}

// SetAutoCompact enables one opportunistic compaction of the snapshot archive
// after a snapshot is saved (on by default; --no-auto-compact opts out). It
// only ever takes effect
// together with SetSaveSnapshot — the save is the trigger.
func (ui *UI) SetAutoCompact(v bool) {
	ui.AutoCompactEnabled = v
}

// ClaimAutoCompactRun claims the process's single opportunistic compaction
// run: it returns true exactly once, and only when auto-compaction is
// enabled. UIs call it at their save-snapshot hook so at most one auto-run
// happens per gdu process no matter how the hook fires — the atomic claim
// holds even if two scans ever complete concurrently.
func (ui *UI) ClaimAutoCompactRun() bool {
	if !ui.AutoCompactEnabled {
		return false
	}
	return ui.autoCompactClaimed.CompareAndSwap(false, true)
}

// SetSnapshotSelector records which snapshot to load from a multi-snapshot Parquet
// input: spec is the --snapshot value (empty ⇒ latest) and root the --snapshot-root
// filter.
// Stored as primitives so this package needn't import pkg/parquet (which would
// form a common→parquet→analyze→common cycle); the reading UIs build the
// parquet.SnapshotSelector from these.
func (ui *UI) SetSnapshotSelector(spec, root string) {
	ui.SnapshotSpec = spec
	ui.SnapshotRoot = root
}

// SetSnapshotIdentity pins the snapshot to load to one full identity —
// (host, root, ts) — set by archive resolution, which has already chosen the
// snapshot. A textual selector would be lossy here (second precision, DST-fold
// collisions); the identity is not. Overrides any SetSnapshotSelector spec.
func (ui *UI) SetSnapshotIdentity(root, host string, ts time.Time) {
	ui.SnapshotSpec = ""
	ui.SnapshotRoot = root
	ui.SnapshotHost = host
	ui.SnapshotTs = ts
}

// binary multiplies prefixes (IEC)
const (
	_ float64 = 1 << (10 * iota)
	Ki
	Mi
	Gi
	Ti
	Pi
	Ei
)

// SI prefixes
const (
	K float64 = 1e3
	M float64 = 1e6
	G float64 = 1e9
	T float64 = 1e12
	P float64 = 1e15
	E float64 = 1e18
)

// FormatNumber returns number as a string with thousands separator
func FormatNumber(n int64) string {
	in := []byte(strconv.FormatInt(n, 10))

	var out []byte
	if i := len(in) % 3; i != 0 {
		if out, in = append(out, in[:i]...), in[i:]; len(in) > 0 {
			out = append(out, ',')
		}
	}
	for len(in) > 0 {
		if out, in = append(out, in[:3]...), in[3:]; len(in) > 0 {
			out = append(out, ',')
		}
	}
	return string(out)
}
