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

// rootSlugMaxLen caps the scan-root slug so even a very deep root keeps the
// filename well under the 255-byte limit common to APFS/ext4/NTFS. Truncation is
// safe because uniqueness never relies on the slug: the timestamp prefix, the
// collision suffix and the lossless scan_root column all still distinguish scans.
const rootSlugMaxLen = 60

// rootSlug derives a short, filesystem-safe, lower-case label from a scan root so
// the snapshot filename indicates what was scanned, e.g. "/" → "root",
// "/Volumes/SD" → "volumes_sd", "/Users/michael" → "users_michael",
// `C:\Users\me` → "c_users_me". Every run of characters outside [a-z0-9] collapses
// to a single "_"; the result is trimmed of leading/trailing "_" and capped at
// rootSlugMaxLen. The exact root is preserved in the scan_root column, so this
// lossy label is purely cosmetic and never the source of truth.
func rootSlug(scanRoot string) string {
	var b strings.Builder
	b.Grow(len(scanRoot))
	prevUnderscore := false
	for _, r := range strings.ToLower(scanRoot) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevUnderscore = false
		} else if !prevUnderscore && b.Len() > 0 {
			// Collapse any run of separators/punctuation into one "_"; never lead
			// with one (so the leading "/" of an absolute path is dropped).
			b.WriteByte('_')
			prevUnderscore = true
		}
		if b.Len() >= rootSlugMaxLen {
			break
		}
	}
	slug := strings.Trim(b.String(), "_")
	if slug == "" {
		return "root" // "/" (and any all-separator path) maps here
	}
	return slug
}

// SnapshotFileName returns the conventional snapshot filename for now in the local
// timezone with a scan-root suffix, e.g. "scan_20260619T153000_users_michael.parquet"
// (sortable, filesystem-safe). The scan_ts column inside the file stays
// UTC/timezone-aware regardless.
func SnapshotFileName(now time.Time, scanRoot string) string {
	return "scan_" + now.Format("20060102T150405") + "_" + rootSlug(scanRoot) + ".parquet"
}

// uniqueSnapshotPath returns a non-colliding path in dir for the snapshot at now,
// appending _1, _2, … when a snapshot of the same root from the same second already
// exists (the filename only has second resolution, and we must never silently
// overwrite).
func uniqueSnapshotPath(dir string, now time.Time, scanRoot string) string {
	base := SnapshotFileName(now, scanRoot)
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

// SaveSnapshot writes tree into scansDir as scan_<local timestamp>_<root>.parquet (the
// directory is created if missing), bucketing objects below thresholdBytes into
// "<smaller objects>" rollups. When running under sudo the directory and file are
// chowned back to the invoking user. It returns the path written.
func SaveSnapshot(tree fs.Item, scansDir string, thresholdBytes int64, now time.Time) (string, error) {
	if err := os.MkdirAll(scansDir, 0o700); err != nil {
		return "", err
	}
	common.ChownToInvoker(scansDir)

	scanRoot := tree.GetPath()
	path := uniqueSnapshotPath(scansDir, now, scanRoot)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}

	id := common.CollectScanIdentity()
	meta := ScanMeta{
		ScanRoot:       scanRoot,
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
