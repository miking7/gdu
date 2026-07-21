package parquet

import (
	"errors"
	"io"
	"time"

	"github.com/parquet-go/parquet-go"

	"github.com/dundee/gdu/v5/pkg/analyze"
)

// readBatchSize is how many rows are decoded per Read call when streaming a
// snapshot. Bounding it keeps a multi-million-row file from materialising every
// row at once — that peak, retained by macOS's MADV_FREE, was the import RAM
// blow-up (a 6M-row file otherwise needed ~12 GB).
const readBatchSize = 8192

// ReadTree reconstructs an analyze.Dir tree from a gdu Parquet snapshot. Rows are
// streamed in batches and the tree is built incrementally via a path→node map, so
// peak memory is bounded by the resulting tree rather than the row count.
// Directory sizes are left at zero; callers run UpdateStats to recompute recursive
// totals from the (leaf) file and rollup rows, exactly as the JSON importer does.
//
// If the file holds multiple snapshots (e.g. a compacted archive), the most recent
// one is loaded. Snapshots are told apart by their full (host, scan_root, scan_ts)
// identity, so two snapshots sharing a timestamp never bleed into one tree. Use
// ReadTreeSelected to load a specific snapshot.
func ReadTree(r io.ReaderAt, size int64) (*analyze.Dir, error) {
	return ReadTreeSelected(r, size, SnapshotSelector{})
}

// ReadTreeSnapshot reconstructs the tree for the exact snapshot identified by info
// (its (host, scan_root, scan_ts) tuple), for callers that already know which
// snapshot they want — e.g. the TUI picker after the user selects one from
// ListSnapshots. See ReadTree for the streaming/memory characteristics.
func ReadTreeSnapshot(r io.ReaderAt, size int64, info *SnapshotInfo) (*analyze.Dir, error) {
	pf, err := parquet.OpenFile(r, size)
	if err != nil {
		return nil, err
	}
	return readTreeSnapshot(pf, info)
}

// readTreeSnapshot streams the file's rows and builds the tree for exactly the snapshot
// identified by selected, skipping rows of any other snapshot sharing the file.
func readTreeSnapshot(pf *parquet.File, selected *SnapshotInfo) (*analyze.Dir, error) {
	selTs := selected.ScanTs.UnixMilli()

	reader := parquet.NewGenericReader[Row](pf)
	defer reader.Close()

	dirs := make(map[string]*analyze.Dir)
	var root *analyze.Dir
	buf := make([]Row, readBatchSize)

	for {
		n, readErr := reader.Read(buf)
		for i := 0; i < n; i++ {
			row := &buf[i]
			if row.ScanTs != selTs || row.ScanRoot != selected.ScanRoot || row.Host != selected.Host {
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

// getOrCreateDir returns the dir node for path, creating an empty placeholder if a
// child row referenced it before its own row was seen.
//
// Dirs get the no-op ' ' flag rather than the zero value, matching what the JSON
// importer does: the TUI prints the flag as each row's first column, and a rune(0)
// renders zero-width, shifting every directory row one column left of the file rows
// beside it. Read-error markers are deliberately not restored — read_error conflates
// '!' (this dir) with '.' (a descendant), so reviving it would relabel readable dirs
// as denied. ErrCount is counted at write time from the live tree, not from here.
func getOrCreateDir(dirs map[string]*analyze.Dir, path string) *analyze.Dir {
	d := dirs[path]
	if d == nil {
		d = &analyze.Dir{File: &analyze.File{Flag: ' '}}
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
		// A directory's read-error flag is left to UpdateStats, which re-derives
		// it from the children it can see. "Cloud placeholder" has no such
		// evidence to rebuild from — the placeholder has no children by
		// definition — so it is the one directory flag restored from the row.
		if row.Dataless {
			d.Flag = '~'
		}
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
	case row.Dataless:
		return '~'
	default:
		return ' '
	}
}
