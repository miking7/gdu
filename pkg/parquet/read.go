package parquet

import (
	"errors"
	"io"
	"time"

	"github.com/parquet-go/parquet-go"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/pkg/analyze"
)

// readBatchSize is how many rows are decoded per Read call when streaming a
// snapshot. Bounding it keeps a multi-million-row file from materialising every
// row at once — that peak, retained by macOS's MADV_FREE, was the import RAM
// blow-up (a 6M-row file otherwise needed ~12 GB).
const readBatchSize = 8192

// scanTsRow is a one-column projection used to find the latest scan cheaply
// without decoding every column.
type scanTsRow struct {
	ScanTs int64 `parquet:"scan_ts,timestamp(millisecond)"`
}

// ReadTree reconstructs an analyze.Dir tree from a gdu Parquet snapshot. Rows are
// streamed in batches and the tree is built incrementally via a path→node map, so
// peak memory is bounded by the resulting tree rather than the row count.
// Directory sizes are left at zero; callers run UpdateStats to recompute recursive
// totals from the (leaf) file and rollup rows, exactly as the JSON importer does.
//
// If the file holds multiple scans (e.g. a compacted archive), the most recent
// one (highest scan_ts) is loaded.
func ReadTree(r io.ReaderAt, size int64) (*analyze.Dir, error) {
	pf, err := parquet.OpenFile(r, size)
	if err != nil {
		return nil, err
	}

	latest, scans, err := latestScanTs(pf)
	if err != nil {
		return nil, err
	}
	if scans == 0 {
		return nil, errors.New("parquet snapshot contains no rows")
	}
	if scans > 1 {
		log.Printf("Parquet snapshot contains %d scans; loading the most recent", scans)
	}

	reader := parquet.NewGenericReader[Row](pf)
	defer reader.Close()

	dirs := make(map[string]*analyze.Dir)
	var root *analyze.Dir
	buf := make([]Row, readBatchSize)

	for {
		n, readErr := reader.Read(buf)
		for i := 0; i < n; i++ {
			row := &buf[i]
			if row.ScanTs != latest {
				continue
			}
			if root == nil && row.IsDir && row.Depth == 0 {
				root = getOrCreateDir(dirs, row.Path)
				root.Name = row.Name
				root.Mtime = msToTime(row.Mtime)
				root.BasePath = row.Parent
				continue
			}
			addRow(dirs, row)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}

	if root == nil {
		return nil, errors.New("parquet snapshot has no root directory")
	}
	return root, nil
}

// latestScanTs streams only the scan_ts column and returns the highest value and
// the number of distinct scans present.
func latestScanTs(pf *parquet.File) (latest int64, scans int, err error) {
	reader := parquet.NewGenericReader[scanTsRow](pf)
	defer reader.Close()

	seen := make(map[int64]struct{})
	buf := make([]scanTsRow, readBatchSize)
	for {
		n, readErr := reader.Read(buf)
		for i := 0; i < n; i++ {
			ts := buf[i].ScanTs
			if len(seen) == 0 || ts > latest {
				latest = ts
			}
			seen[ts] = struct{}{}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return 0, 0, readErr
		}
	}
	return latest, len(seen), nil
}

// getOrCreateDir returns the dir node for path, creating an empty placeholder if a
// child row referenced it before its own row was seen.
func getOrCreateDir(dirs map[string]*analyze.Dir, path string) *analyze.Dir {
	d := dirs[path]
	if d == nil {
		d = &analyze.Dir{File: &analyze.File{}}
		dirs[path] = d
	}
	return d
}

// addRow attaches one non-root row (directory, file or rollup) to the tree.
func addRow(dirs map[string]*analyze.Dir, row *Row) {
	if row.IsDir {
		d := getOrCreateDir(dirs, row.Path)
		d.Name = row.Name
		d.Mtime = msToTime(row.Mtime)
		parent := getOrCreateDir(dirs, row.Parent)
		d.Parent = parent
		parent.AddFile(d)
		return
	}

	parent := getOrCreateDir(dirs, row.Parent)
	file := &analyze.File{
		Name:   row.Name,
		Size:   row.Asize,
		Usage:  row.Dsize,
		Mtime:  msToTime(row.Mtime),
		Flag:   flagFromRow(row),
		Parent: parent,
	}
	if row.Ino != nil {
		file.Mli = *row.Ino
	}
	parent.AddFile(file)
}

func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func flagFromRow(row *Row) rune {
	switch {
	case row.Hlnkc:
		return 'H'
	case row.Notreg:
		return '@'
	case row.ReadError:
		return '!'
	default:
		return ' '
	}
}
