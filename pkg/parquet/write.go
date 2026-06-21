package parquet

import (
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
)

// ScanMeta carries per-scan metadata stamped onto every row.
type ScanMeta struct {
	ScanRoot       string
	ScanTime       time.Time
	ThresholdBytes int64
}

type emitFunc func(Row)

// WriteTree flattens the analyzed tree rooted at root into Parquet rows and
// writes them to w (zstd-compressed). Objects whose disk usage is below
// meta.ThresholdBytes are bucketed into "<smaller objects>" rollup rows while
// each directory keeps its exact recursive totals. Rows are sorted by path for
// compact, compaction-friendly output.
func WriteTree(w io.Writer, root fs.Item, meta ScanMeta) error {
	rows := make([]Row, 0, 1024)
	emit := func(r Row) { rows = append(rows, r) }

	rootPath := root.GetPath()
	emitDir(root, rootPath, filepath.Dir(rootPath), 0, meta, emit)

	sort.Slice(rows, func(i, j int) bool { return rows[i].Path < rows[j].Path })

	pw := parquet.NewGenericWriter[Row](w, parquet.Compression(&zstd.Codec{}))
	if _, err := pw.Write(rows); err != nil {
		_ = pw.Close()
		return err
	}
	return pw.Close()
}

// emitDir emits rows for dir's significant children (plus a rollup bucket for the
// rest) and for dir itself, returning the exact recursive (files, folders) counts
// of dir's descendants (not counting dir itself).
func emitDir(
	dir fs.Item, path, parentPath string, depth int32, meta ScanMeta, emit emitFunc,
) (descFiles, descFolders int64) {
	var bucketASize, bucketDSize, bucketFiles, bucketFolders int64

	for child := range dir.GetFiles(fs.SortByName, fs.SortAsc) {
		childPath := filepath.Join(path, child.GetName())
		significant := meta.ThresholdBytes <= 0 || child.GetUsage() >= meta.ThresholdBytes

		if child.IsDir() {
			var cf, cfo int64
			if significant {
				cf, cfo = emitDir(child, childPath, path, depth+1, meta, emit)
			} else {
				cf, cfo = countDescendants(child)
			}
			descFiles += cf
			descFolders += cfo + 1 // +1 for the child dir itself
			if !significant {
				bucketASize += child.GetSize()
				bucketDSize += child.GetUsage()
				bucketFiles += cf
				bucketFolders += cfo + 1
			}
		} else {
			descFiles++
			if significant {
				emit(fileRow(child, childPath, path, depth+1, meta))
			} else {
				bucketASize += child.GetSize()
				bucketDSize += child.GetUsage()
				bucketFiles++
			}
		}
	}

	if bucketFiles > 0 || bucketFolders > 0 {
		emit(rollupRow(
			filepath.Join(path, analyze.SmallObjectsName), path, depth+1,
			bucketASize, bucketDSize, bucketFiles, bucketFolders, meta,
		))
	}
	emit(dirRow(dir, path, parentPath, depth, descFiles, descFolders, meta))

	return descFiles, descFolders
}

// countDescendants returns the exact (files, folders) within dir's subtree,
// excluding dir itself. Used for sub-threshold dirs that are collapsed without
// being emitted, so their counts still feed the rollup and ancestor totals.
func countDescendants(dir fs.Item) (files, folders int64) {
	for child := range dir.GetFiles(fs.SortByName, fs.SortAsc) {
		if child.IsDir() {
			cf, cfo := countDescendants(child)
			files += cf
			folders += cfo + 1
		} else {
			files++
		}
	}
	return files, folders
}

func fileRow(item fs.Item, path, parentPath string, depth int32, meta ScanMeta) Row {
	r := Row{
		Path:   path,
		Parent: parentPath,
		Name:   item.GetName(),
		Depth:  depth,
		Asize:  item.GetSize(),
		Dsize:  item.GetUsage(),
	}
	stampMeta(&r, meta)
	stampItemAttrs(&r, item)
	return r
}

func dirRow(
	item fs.Item, path, parentPath string, depth int32, descFiles, descFolders int64, meta ScanMeta,
) Row {
	usage := item.GetUsage()
	r := Row{
		Path:            path,
		Parent:          parentPath,
		Name:            item.GetName(),
		IsDir:           true,
		Depth:           depth,
		DirTotalDsize:   &usage,
		DirTotalFiles:   &descFiles,
		DirTotalFolders: &descFolders,
	}
	stampMeta(&r, meta)
	stampItemAttrs(&r, item)
	return r
}

func rollupRow(
	path, parentPath string, depth int32, asize, dsize, files, folders int64, meta ScanMeta,
) Row {
	r := Row{
		Path:            path,
		Parent:          parentPath,
		Name:            analyze.SmallObjectsName,
		IsRollup:        true,
		Depth:           depth,
		Asize:           asize,
		Dsize:           dsize,
		DirTotalFiles:   &files,
		DirTotalFolders: &folders,
	}
	stampMeta(&r, meta)
	return r
}

func stampMeta(r *Row, meta ScanMeta) {
	r.ScanRoot = meta.ScanRoot
	r.ScanTs = meta.ScanTime.UnixMilli()
	r.ThresholdBytes = meta.ThresholdBytes
}

func stampItemAttrs(r *Row, item fs.Item) {
	if mt := item.GetMtime(); !mt.IsZero() {
		r.Mtime = mt.UnixMilli()
	}
	switch item.GetFlag() {
	case '@':
		r.Notreg = true
	case 'H':
		r.Hlnkc = true
	case '!', '.':
		r.ReadError = true
	}
	if ino := item.GetMultiLinkedInode(); ino > 0 {
		r.Ino = &ino
	}
}
