package parquet

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/fs"
)

// SnapshotFileName returns the conventional snapshot filename for now in the
// local timezone, e.g. "scan_20260619T153000.parquet" (sortable, filesystem-safe).
// The scan_ts column inside the file stays UTC/timezone-aware regardless.
func SnapshotFileName(now time.Time) string {
	return "scan_" + now.Format("20060102T150405") + ".parquet"
}

// uniqueSnapshotPath returns a non-colliding path in dir for the snapshot at now,
// appending _1, _2, … when a snapshot from the same second already exists (the
// filename only has second resolution, and we must never silently overwrite).
func uniqueSnapshotPath(dir string, now time.Time) string {
	base := SnapshotFileName(now)
	if path := filepath.Join(dir, base); !fileExists(path) {
		return path
	}
	stem := strings.TrimSuffix(base, ".parquet")
	for i := 1; ; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%s_%d.parquet", stem, i))
		if !fileExists(path) {
			return path
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// SaveSnapshot writes tree into scansDir as scan_<local timestamp>.parquet (the
// directory is created if missing), bucketing objects below thresholdBytes into
// "<smaller objects>" rollups. When running under sudo the directory and file are
// chowned back to the invoking user. It returns the path written.
func SaveSnapshot(tree fs.Item, scansDir string, thresholdBytes int64, now time.Time) (string, error) {
	if err := os.MkdirAll(scansDir, 0o700); err != nil {
		return "", err
	}
	common.ChownToInvoker(scansDir)

	path := uniqueSnapshotPath(scansDir, now)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}

	id := common.CollectScanIdentity()
	meta := ScanMeta{
		ScanRoot:       tree.GetPath(),
		ScanTime:       now.UTC(),
		ThresholdBytes: thresholdBytes,
		Host:           id.Host,
		Username:       id.Username,
		SudoUser:       id.SudoUser,
	}
	if err := WriteTree(f, tree, &meta); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	common.ChownToInvoker(path)
	return path, nil
}
