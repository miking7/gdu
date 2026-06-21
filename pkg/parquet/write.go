package parquet

import (
	"io"
	"path/filepath"
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
	Host           string // os.Hostname()
	Username       string // effective user the scan ran as
	SudoUser       string // invoking user under sudo; "" otherwise
}

type emitFunc func(Row)

// WriteTree flattens the analyzed tree rooted at root into Parquet rows and
// writes them to w (zstd-compressed). Objects whose disk usage is below
// meta.ThresholdBytes are bucketed into "<smaller objects>" rollup rows while
// each directory keeps its exact recursive totals. Rows are streamed in tree
// (DFS) order; readers reconstruct by path, so global ordering is not required.
// (Future compaction can sort by (path, scan_ts) when it merges snapshots.)
func WriteTree(w io.Writer, root fs.Item, meta *ScanMeta) error {
	pw := parquet.NewGenericWriter[Row](w,
		parquet.Compression(&zstd.Codec{}),
		// parquet-go defaults to math.MaxInt64 rows/group, i.e. the whole dataset
		// buffered in memory as one group; bound it so pages stream to the writer.
		parquet.MaxRowsPerRowGroup(maxRowsPerRowGroup),
	)

	// Stream rows to the writer in batches instead of building one giant []Row:
	// a full-system scan has millions of rows, and holding them all (then on
	// macOS never returning the freed pages) was a large part of the save-scan RSS.
	batch := make([]Row, 0, writeBatchSize)
	var writeErr error
	flush := func() {
		if writeErr != nil || len(batch) == 0 {
			return
		}
		_, writeErr = pw.Write(batch)
		batch = batch[:0]
	}
	emit := func(r Row) {
		batch = append(batch, r)
		if len(batch) >= writeBatchSize {
			flush()
		}
	}

	rootPath := root.GetPath()
	emitDir(root, rootPath, filepath.Dir(rootPath), 0, meta, emit)
	flush()

	if writeErr != nil {
		_ = pw.Close()
		return writeErr
	}
	return pw.Close()
}

const (
	// maxRowsPerRowGroup caps the in-memory row group held by the writer.
	maxRowsPerRowGroup = 128 << 10
	// writeBatchSize is how many rows are buffered before each Write call.
	writeBatchSize = 8192
)

// emitDir emits rows for dir's significant children (plus a rollup bucket for the
// rest) and for dir itself, returning the exact recursive (files, folders) counts
// of dir's descendants (not counting dir itself).
func emitDir(
	dir fs.Item, path, parentPath string, depth int32, meta *ScanMeta, emit emitFunc,
) (descFiles, descFolders int64) {
	var bucketASize, bucketDSize, bucketFiles, bucketFolders int64

	// Iterate in stored order (SortByNone avoids a per-directory sorted copy);
	// WriteTree re-sorts all rows by path once at the end. Child paths are built
	// only for items we actually emit, not for sub-threshold ones we roll up.
	for child := range dir.GetFiles(fs.SortByNone, fs.SortAsc) {
		significant := meta.ThresholdBytes <= 0 || child.GetUsage() >= meta.ThresholdBytes

		if child.IsDir() {
			var cf, cfo int64
			if significant {
				childPath := filepath.Join(path, child.GetName())
				cf, cfo = emitDir(child, childPath, path, depth+1, meta, emit)
			} else {
				cf, cfo = countDescendants(child)
				bucketASize += child.GetSize()
				bucketDSize += child.GetUsage()
				bucketFiles += cf
				bucketFolders += cfo + 1
			}
			descFiles += cf
			descFolders += cfo + 1 // +1 for the child dir itself
		} else {
			descFiles++
			if significant {
				childPath := filepath.Join(path, child.GetName())
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
	for child := range dir.GetFiles(fs.SortByNone, fs.SortAsc) {
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

func fileRow(item fs.Item, path, parentPath string, depth int32, meta *ScanMeta) Row {
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
	item fs.Item, path, parentPath string, depth int32, descFiles, descFolders int64, meta *ScanMeta,
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
	path, parentPath string, depth int32, asize, dsize, files, folders int64, meta *ScanMeta,
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

func stampMeta(r *Row, meta *ScanMeta) {
	r.ScanRoot = meta.ScanRoot
	r.ScanTs = meta.ScanTime.UnixMilli()
	r.ThresholdBytes = meta.ThresholdBytes
	r.Host = meta.Host
	r.Username = meta.Username
	if meta.SudoUser != "" {
		su := meta.SudoUser
		r.SudoUser = &su
	}
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
