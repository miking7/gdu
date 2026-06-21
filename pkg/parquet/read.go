package parquet

import (
	"errors"
	"io"
	"time"

	"github.com/parquet-go/parquet-go"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/pkg/analyze"
)

// ReadTree reconstructs an analyze.Dir tree from a gdu Parquet snapshot read
// from r (size bytes). Directory sizes are left at zero; callers run UpdateStats
// to recompute recursive totals from the (leaf) file and rollup rows, exactly as
// the JSON importer does.
//
// If the file holds multiple scans (e.g. a compacted archive), the most recent
// one (highest scan_ts) is returned.
func ReadTree(r io.ReaderAt, size int64) (*analyze.Dir, error) {
	rows, err := parquet.Read[Row](r, size)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("parquet snapshot contains no rows")
	}

	latest, scans := latestScan(rows)
	if scans > 1 {
		log.Printf("Parquet snapshot contains %d scans; loading the most recent", scans)
	}

	dirs := make(map[string]*analyze.Dir)
	var root *analyze.Dir

	// First pass: create every directory node so parents exist before linking.
	for i := range rows {
		row := &rows[i]
		if row.ScanTs != latest || !row.IsDir {
			continue
		}
		dir := &analyze.Dir{
			File: &analyze.File{
				Name:  row.Name,
				Mtime: msToTime(row.Mtime),
			},
		}
		dirs[row.Path] = dir
		if row.Depth == 0 {
			dir.BasePath = row.Parent
			root = dir
		}
	}
	if root == nil {
		return nil, errors.New("parquet snapshot has no root directory")
	}

	// Second pass: attach files and link directories to their parents.
	for i := range rows {
		row := &rows[i]
		if row.ScanTs != latest {
			continue
		}
		if row.IsDir {
			child := dirs[row.Path]
			if child == root {
				continue
			}
			if parent := dirs[row.Parent]; parent != nil {
				child.Parent = parent
				parent.AddFile(child)
			}
			continue
		}
		parent := dirs[row.Parent]
		if parent == nil {
			continue
		}
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

	return root, nil
}

// latestScan returns the highest scan_ts and the number of distinct scans found.
func latestScan(rows []Row) (latest int64, scans int) {
	seen := make(map[int64]struct{})
	latest = rows[0].ScanTs
	for i := range rows {
		ts := rows[i].ScanTs
		seen[ts] = struct{}{}
		if ts > latest {
			latest = ts
		}
	}
	return latest, len(seen)
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
