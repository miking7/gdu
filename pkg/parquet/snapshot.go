package parquet

import (
	"os"
	"path/filepath"
	"time"

	"github.com/dundee/gdu/v5/pkg/fs"
)

// SnapshotFileName returns the conventional snapshot filename for now, e.g.
// "scan_20260619T053000Z.parquet" (UTC, sortable, filesystem-safe).
func SnapshotFileName(now time.Time) string {
	return "scan_" + now.UTC().Format("20060102T150405Z") + ".parquet"
}

// SaveSnapshot writes tree into scansDir as scan_<UTC timestamp>.parquet (the
// directory is created if missing), bucketing objects below thresholdBytes into
// "<smaller objects>" rollups. It returns the path written.
func SaveSnapshot(tree fs.Item, scansDir string, thresholdBytes int64, now time.Time) (string, error) {
	if err := os.MkdirAll(scansDir, 0o700); err != nil {
		return "", err
	}

	path := filepath.Join(scansDir, SnapshotFileName(now))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}

	meta := ScanMeta{
		ScanRoot:       tree.GetPath(),
		ScanTime:       now.UTC(),
		ThresholdBytes: thresholdBytes,
	}
	if err := WriteTree(f, tree, meta); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}
