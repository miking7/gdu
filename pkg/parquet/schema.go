// Package parquet writes and reads gdu disk-usage snapshots as Apache Parquet
// files using the pure-Go parquet-go library (no cgo, preserving gdu's static
// cross-platform builds). The column layout mirrors ncdu_to_parquet.py for the
// fields gdu can source.
package parquet

// Row is one flattened entry in a gdu Parquet snapshot: a surviving file, a
// directory, or a "<smaller objects>" rollup bucket.
//
// Size conventions (gdu tracks no per-inode size for directories):
//   - file and rollup rows carry their own apparent size (asize) and disk usage
//     (dsize); summing dsize over all non-directory rows yields the scan total.
//   - directory rows carry asize = dsize = 0; their recursive disk usage and
//     recursive file/folder counts live in dir_total_dsize / dir_total_files /
//     dir_total_folders.
//
// scan_ts and mtime use a timezone-aware Parquet TIMESTAMP (isAdjustedToUTC),
// so DuckDB reads them as TIMESTAMPTZ.
type Row struct {
	// identity / structure
	Path     string `parquet:"path"`
	Parent   string `parquet:"parent"`
	Name     string `parquet:"name"`
	IsDir    bool   `parquet:"is_dir"`
	IsRollup bool   `parquet:"is_rollup"`
	Depth    int32  `parquet:"depth"`

	// sizes & counts
	Asize           int64  `parquet:"asize"`
	Dsize           int64  `parquet:"dsize"`
	DirTotalDsize   *int64 `parquet:"dir_total_dsize,optional"`
	DirTotalFiles   *int64 `parquet:"dir_total_files,optional"`
	DirTotalFolders *int64 `parquet:"dir_total_folders,optional"`

	// per-scan metadata
	ScanRoot       string `parquet:"scan_root"`
	ScanTs         int64  `parquet:"scan_ts,timestamp(millisecond)"`
	ThresholdBytes int64  `parquet:"threshold_bytes"`

	// gdu-native passthrough
	Mtime int64   `parquet:"mtime,timestamp(millisecond)"`
	Ino   *uint64 `parquet:"ino,optional"`

	// flags decoded from gdu's rune flag
	Notreg    bool `parquet:"notreg"`     // '@' symlink/socket
	Hlnkc     bool `parquet:"hlnkc"`      // 'H' hard link
	ReadError bool `parquet:"read_error"` // '!' or '.'
}
