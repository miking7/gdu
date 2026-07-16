package parquet

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// Footer key-value metadata keys stamped on every snapshot file gdu writes.
// Format 2 (the vocabulary rename) uses gdu.snapshots for the manifest; foreign
// files and pre-rename format-1 files lack it and are listed via the statistics
// or column-projection tiers instead (the foreign-file path).
const (
	// FormatKey is the footer key holding the manifest/schema version.
	FormatKey = "gdu.format"
	// FormatVersion is the current value written under FormatKey.
	FormatVersion = "2"
	// SnapshotsKey is the footer key holding the JSON snapshot manifest.
	SnapshotsKey = "gdu.snapshots"
)

// SnapshotInfo identifies and summarizes one snapshot inside a snapshot file. A
// snapshot's identity is the (Host, ScanRoot, ScanTs) tuple; the remaining fields
// are informational. Rows counts every row belonging to the snapshot (files, dirs
// and rollups) and TotalDsize is the scan root's recursive disk usage.
type SnapshotInfo struct {
	ScanRoot       string
	ScanTs         time.Time // UTC instant of scan completion
	Host           string
	Username       string
	SudoUser       string // invoking user under sudo; empty otherwise
	Rows           int64
	TotalDsize     int64
	ThresholdBytes int64
	// ErrCount is how many directories the scan could not read (permission
	// denied etc.). It is the evidence the launcher's sudo tip draws on. Zero
	// for legacy/foreign files whose manifest predates the field, which read
	// back as "no recorded errors".
	ErrCount int64
}

// SameSnapshot reports whether two SnapshotInfos name the same snapshot, comparing the
// (Host, ScanRoot, ScanTs) identity tuple only.
func (s *SnapshotInfo) SameSnapshot(o *SnapshotInfo) bool {
	return s.Host == o.Host && s.ScanRoot == o.ScanRoot && s.ScanTs.Equal(o.ScanTs)
}

// manifestSnapshot is the JSON wire form of SnapshotInfo inside the SnapshotsKey footer
// value. scan_ts is Unix milliseconds UTC, matching the column encoding.
type manifestSnapshot struct {
	ScanRoot       string `json:"scan_root"`
	ScanTsMs       int64  `json:"scan_ts"`
	Host           string `json:"host"`
	Username       string `json:"username"`
	SudoUser       string `json:"sudo_user,omitempty"`
	Rows           int64  `json:"rows"`
	TotalDsize     int64  `json:"total_dsize"`
	ThresholdBytes int64  `json:"threshold_bytes"`
	ErrCount       int64  `json:"err_count,omitempty"`
}

// marshalManifest encodes snapshots as the SnapshotsKey footer JSON.
func marshalManifest(scans []SnapshotInfo) (string, error) {
	wire := make([]manifestSnapshot, len(scans))
	for i, s := range scans {
		wire[i] = manifestSnapshot{
			ScanRoot:       s.ScanRoot,
			ScanTsMs:       s.ScanTs.UnixMilli(),
			Host:           s.Host,
			Username:       s.Username,
			SudoUser:       s.SudoUser,
			Rows:           s.Rows,
			TotalDsize:     s.TotalDsize,
			ThresholdBytes: s.ThresholdBytes,
			ErrCount:       s.ErrCount,
		}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// parseManifest decodes a SnapshotsKey footer value.
func parseManifest(raw string) ([]SnapshotInfo, error) {
	var wire []manifestSnapshot
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, fmt.Errorf("invalid %s manifest: %w", SnapshotsKey, err)
	}
	scans := make([]SnapshotInfo, len(wire))
	for i, w := range wire {
		scans[i] = SnapshotInfo{
			ScanRoot:       w.ScanRoot,
			ScanTs:         time.UnixMilli(w.ScanTsMs).UTC(),
			Host:           w.Host,
			Username:       w.Username,
			SudoUser:       w.SudoUser,
			Rows:           w.Rows,
			TotalDsize:     w.TotalDsize,
			ThresholdBytes: w.ThresholdBytes,
			ErrCount:       w.ErrCount,
		}
	}
	return scans, nil
}

// ListSnapshots returns the snapshots contained in a snapshot file, ordered oldest-first by
// (ScanTs, ScanRoot, Host). It reads only the footer manifest when present;
// foreign and pre-rename format-1 files (no manifest) fall back to a cheap
// column projection that never decodes path or name data. An empty file yields
// an empty slice.
func ListSnapshots(r io.ReaderAt, size int64) ([]SnapshotInfo, error) {
	pf, err := parquet.OpenFile(r, size,
		parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
	if err != nil {
		return nil, err
	}
	return listSnapshots(pf)
}

// listSnapshots is ListSnapshots over an already-open file. It tries three sources in
// increasing cost: the footer manifest (one footer read, exact), then column
// statistics (footer only, exact when every identity column is single-valued —
// the common single-snapshot file), then a full column-projection row scan
// (foreign multi-snapshot files where stats can't prove the identity set).
func listSnapshots(pf *parquet.File) ([]SnapshotInfo, error) {
	if raw, ok := pf.Lookup(SnapshotsKey); ok {
		scans, err := parseManifest(raw)
		if err != nil {
			return nil, err
		}
		sortSnapshots(scans)
		return scans, nil
	}

	if scans, ok := statsSnapshotList(pf); ok {
		sortSnapshots(scans)
		return scans, nil
	}

	scans, err := fallbackSnapshotList(pf)
	if err != nil {
		return nil, err
	}
	sortSnapshots(scans)
	return scans, nil
}

// sortSnapshots orders snapshots oldest-first, tie-breaking on root then host so the
// "latest" pick (the last element) is deterministic.
func sortSnapshots(scans []SnapshotInfo) {
	sort.Slice(scans, func(i, j int) bool {
		a, b := scans[i], scans[j]
		if !a.ScanTs.Equal(b.ScanTs) {
			return a.ScanTs.Before(b.ScanTs)
		}
		if a.ScanRoot != b.ScanRoot {
			return a.ScanRoot < b.ScanRoot
		}
		return a.Host < b.Host
	})
}

// colStatus classifies an identity column across a whole file's row groups.
type colStatus int

const (
	colAbsent colStatus = iota // column not present, or carries no usable statistics
	colSingle                  // every row group holds one identical value
	colMulti                   // the column holds more than one distinct value
)

// statsSnapshotList derives the snapshot list from footer column statistics alone — no
// data-page reads. It succeeds (ok) only when the file provably holds a single
// scan: scan_root and scan_ts each resolve to one value across every row group.
// host may vary only by being absent (a foreign file lacking gdu's host column) —
// a genuinely multi-host or multi-value file returns ok=false so the caller does
// the full row scan.
// Column min/max stats are written by default by parquet-go and pyarrow, so
// this fast path lights up for gdu snapshots and external files alike.
func statsSnapshotList(pf *parquet.File) ([]SnapshotInfo, bool) {
	md := pf.Metadata()
	if len(md.RowGroups) == 0 {
		return nil, false
	}

	rootVal, rootStatus := columnValue(md, "scan_root")
	if rootStatus != colSingle || len(rootVal) == 0 {
		return nil, false
	}
	tsVal, tsStatus := columnValue(md, "scan_ts")
	if tsStatus != colSingle || len(tsVal) < 8 {
		return nil, false
	}

	// Soft, per-scan-constant fields: a value if single, defaulted if absent, but
	// any multi-value contradicts "single snapshot", so bail to the row scan.
	hostVal, hostStatus := columnValue(md, "host")
	userVal, userStatus := columnValue(md, "username")
	thrVal, thrStatus := columnValue(md, "threshold_bytes")
	if hostStatus == colMulti || userStatus == colMulti || thrStatus == colMulti {
		return nil, false
	}

	info := SnapshotInfo{
		ScanRoot: string(rootVal),
		ScanTs:   time.UnixMilli(leInt64(tsVal)).UTC(),
		Host:     string(hostVal),
		Username: string(userVal),
		Rows:     sumRowGroupRows(md),
		// The root dir's recursive usage is the maximum dir_total_dsize in the
		// file (an ancestor's total is never smaller than a descendant's), so the
		// column max is exactly the scan total — no row read needed.
		TotalDsize: maxInt64Column(md, "dir_total_dsize"),
	}
	if thrStatus == colSingle && len(thrVal) >= 8 {
		info.ThresholdBytes = leInt64(thrVal)
	}
	return []SnapshotInfo{info}, true
}

// columnValue reports the single file-wide value of a top-level column from its
// row-group statistics, or the reason it can't (absent / no stats, or multiple
// distinct values). The returned bytes are the raw PLAIN-encoded min value.
func columnValue(md *format.FileMetaData, name string) ([]byte, colStatus) {
	var first []byte
	seen := false
	for i := range md.RowGroups {
		chunk := findColumnChunk(&md.RowGroups[i], name)
		if chunk == nil {
			return nil, colAbsent
		}
		mn, mx, ok := statMinMax(&chunk.MetaData.Statistics)
		if !ok {
			return nil, colAbsent
		}
		if !bytes.Equal(mn, mx) {
			return nil, colMulti
		}
		if !seen {
			first, seen = mn, true
		} else if !bytes.Equal(first, mn) {
			return nil, colMulti
		}
	}
	if !seen {
		return nil, colAbsent
	}
	return first, colSingle
}

// maxInt64Column returns the numeric maximum of an int64 column across all row
// groups from statistics, skipping row groups whose stat is absent (e.g. an
// all-null dir_total_dsize page). Zero if never present.
func maxInt64Column(md *format.FileMetaData, name string) int64 {
	var best int64
	have := false
	for i := range md.RowGroups {
		chunk := findColumnChunk(&md.RowGroups[i], name)
		if chunk == nil {
			return 0
		}
		_, mx, ok := statMinMax(&chunk.MetaData.Statistics)
		if !ok || len(mx) < 8 {
			continue
		}
		if v := leInt64(mx); !have || v > best {
			best, have = v, true
		}
	}
	return best
}

// sumRowGroupRows totals the row counts declared in each row group's metadata.
func sumRowGroupRows(md *format.FileMetaData) int64 {
	var n int64
	for i := range md.RowGroups {
		n += md.RowGroups[i].NumRows
	}
	return n
}

// findColumnChunk returns the top-level column chunk named name in rg, or nil.
func findColumnChunk(rg *format.RowGroup, name string) *format.ColumnChunk {
	for i := range rg.Columns {
		if p := rg.Columns[i].MetaData.PathInSchema; len(p) == 1 && p[0] == name {
			return &rg.Columns[i]
		}
	}
	return nil
}

// statMinMax returns a column chunk's min/max bounds, preferring the correctly
// typed MinValue/MaxValue and falling back to the deprecated signed Min/Max.
// ok is false when neither pair carries bytes (no statistics written).
func statMinMax(st *format.Statistics) (minv, maxv []byte, ok bool) {
	minv, maxv = st.MinValue, st.MaxValue
	if len(minv) == 0 && len(maxv) == 0 {
		minv, maxv = st.Min, st.Max
	}
	if len(minv) == 0 && len(maxv) == 0 {
		return nil, nil, false
	}
	return minv, maxv, true
}

// leInt64 decodes a little-endian PLAIN-encoded INT64 (the physical form of
// scan_ts and dir_total_dsize). Callers guard len >= 8.
func leInt64(b []byte) int64 {
	return int64(binary.LittleEndian.Uint64(b[:8]))
}

// snapshotListRow projects only the cheap, per-scan-constant columns (plus the
// root-dir totals) used to reconstruct snapshot infos from foreign, manifest-less
// files. Columns absent from such files (host, username, sudo_user) read as zero
// values.
type snapshotListRow struct {
	ScanRoot       string  `parquet:"scan_root"`
	ScanTs         int64   `parquet:"scan_ts,timestamp(millisecond)"`
	ThresholdBytes int64   `parquet:"threshold_bytes"`
	Host           string  `parquet:"host"`
	Username       string  `parquet:"username"`
	SudoUser       *string `parquet:"sudo_user,optional"`
	IsDir          bool    `parquet:"is_dir"`
	Depth          int32   `parquet:"depth"`
	DirTotalDsize  *int64  `parquet:"dir_total_dsize,optional"`
}

// SnapshotKey is the comparable form of a scan's (Host, ScanRoot, ScanTs) identity —
// a map-friendly key for per-snapshot lookups (folder sizes, dedup) that avoids
// carrying a time.Time in the map key.
type SnapshotKey struct {
	Host, Root string
	TsMs       int64 // ScanTs as Unix milliseconds
}

// Key returns the snapshot's comparable identity key.
func (s *SnapshotInfo) Key() SnapshotKey {
	return SnapshotKey{Host: s.Host, Root: s.ScanRoot, TsMs: s.ScanTs.UnixMilli()}
}

// fallbackSnapshotList reconstructs the snapshot list of a manifest-less file by
// streaming a narrow column projection and grouping rows by identity tuple.
func fallbackSnapshotList(pf *parquet.File) ([]SnapshotInfo, error) {
	reader := parquet.NewGenericReader[snapshotListRow](pf)
	defer reader.Close()

	infos := make(map[SnapshotKey]*SnapshotInfo)
	buf := make([]snapshotListRow, readBatchSize)
	for {
		n, readErr := reader.Read(buf)
		for i := 0; i < n; i++ {
			row := &buf[i]
			key := SnapshotKey{Host: row.Host, Root: row.ScanRoot, TsMs: row.ScanTs}
			info := infos[key]
			if info == nil {
				info = &SnapshotInfo{
					ScanRoot:       row.ScanRoot,
					ScanTs:         time.UnixMilli(row.ScanTs).UTC(),
					Host:           row.Host,
					Username:       row.Username,
					ThresholdBytes: row.ThresholdBytes,
				}
				if row.SudoUser != nil {
					info.SudoUser = *row.SudoUser
				}
				infos[key] = info
			}
			info.Rows++
			if row.IsDir && row.Depth == 0 && row.DirTotalDsize != nil {
				info.TotalDsize = *row.DirTotalDsize
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}

	scans := make([]SnapshotInfo, 0, len(infos))
	for _, info := range infos {
		scans = append(scans, *info)
	}
	return scans, nil
}
