package parquet

import (
	"bytes"
	"errors"
	"io"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
)

// pathSizeRow projects just the columns needed to read one path's disk usage per
// snapshot: the (host, scan_root, scan_ts) identity plus is_dir/dsize/dir_total_dsize.
// Reading this narrow struct makes parquet-go touch only these columns, not the
// full 22-column Row — the baseline picker wants one number per snapshot, not the tree.
type pathSizeRow struct {
	Path          string `parquet:"path"`
	IsDir         bool   `parquet:"is_dir"`
	Dsize         int64  `parquet:"dsize"`
	DirTotalDsize *int64 `parquet:"dir_total_dsize,optional"`
	ScanRoot      string `parquet:"scan_root"`
	ScanTs        int64  `parquet:"scan_ts,timestamp(millisecond)"`
	Host          string `parquet:"host"`
}

// usage mirrors fs.Item.GetUsage: a directory's recursive disk usage lives in
// dir_total_dsize, a file's or rollup's in its own dsize.
func (row *pathSizeRow) usage() int64 {
	if row.IsDir {
		if row.DirTotalDsize != nil {
			return *row.DirTotalDsize
		}
		return 0
	}
	return row.Dsize
}

// PathSizes streams a snapshot file once, reading only the projected size/identity
// columns, and returns target's disk usage in every scan that has a row for it,
// keyed by snapshot identity. It never builds an fs.Item tree — matching a path is an
// exact string compare, so the volume-root prefix pitfalls of a tree walk don't
// arise. A snapshot with no row for target (it did not exist then, or was rolled up
// below the threshold) is simply absent from the result.
//
// Row groups whose path column min/max provably excludes target are skipped from
// their footer statistics alone (no data-page read). Since snapshots are sorted
// by path, a compacted whole-disk monthly holds each path's rows in a short run
// of row groups, so a deep target reads a fraction of the file. The skip is
// always safe: a row group is skipped only when target sits outside its min/max,
// and parquet's conservative stat truncation only ever widens that range.
func PathSizes(r io.ReaderAt, size int64, target string) (map[SnapshotKey]int64, error) {
	pf, err := parquet.OpenFile(r, size,
		parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
	if err != nil {
		return nil, err
	}

	md := pf.Metadata()
	rowGroups := pf.RowGroups()
	targetBytes := []byte(target)

	out := make(map[SnapshotKey]int64)
	buf := make([]pathSizeRow, readBatchSize)
	for i := range rowGroups {
		if i < len(md.RowGroups) && !rowGroupMayContainPath(&md.RowGroups[i], targetBytes) {
			continue
		}
		if serr := scanRowGroupForPath(rowGroups[i], target, buf, out); serr != nil {
			return nil, serr
		}
	}
	return out, nil
}

// rowGroupMayContainPath reports whether target could appear in rg, judged from
// the path column's min/max statistics. It errs toward true so a group holding
// target is never skipped: only the type-correct MinValue/MaxValue pair is used
// (parquet orders a UTF-8 string column's MinValue/MaxValue by unsigned bytes,
// matching bytes.Compare), and both bounds must be present. The deprecated signed
// Min/Max are deliberately not consulted — their ordering can disagree with
// bytes.Compare for non-ASCII paths — and a missing bound can't rule the path out.
func rowGroupMayContainPath(rg *format.RowGroup, target []byte) bool {
	chunk := findColumnChunk(rg, "path")
	if chunk == nil {
		return true
	}
	minv := chunk.MetaData.Statistics.MinValue
	maxv := chunk.MetaData.Statistics.MaxValue
	if len(minv) == 0 || len(maxv) == 0 {
		return true // no usable unsigned bounds — don't prune
	}
	return bytes.Compare(target, minv) >= 0 && bytes.Compare(target, maxv) <= 0
}

// scanRowGroupForPath reads one row group's projected rows and records target's
// size for each snapshot that has a row for it.
func scanRowGroupForPath(rg parquet.RowGroup, target string, buf []pathSizeRow, out map[SnapshotKey]int64) error {
	reader := parquet.NewGenericRowGroupReader[pathSizeRow](rg)
	defer reader.Close()
	for {
		n, readErr := reader.Read(buf)
		for i := 0; i < n; i++ {
			row := &buf[i]
			if row.Path != target {
				continue
			}
			out[SnapshotKey{Host: row.Host, Root: row.ScanRoot, TsMs: row.ScanTs}] = row.usage()
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}
