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
	Host           string // os.Hostname()
	Username       string // effective user the scan ran as
	SudoUser       string // invoking user under sudo; "" otherwise
}

type emitFunc func(Row)

// WriteTree flattens the analyzed tree rooted at root into Parquet rows and
// writes them to w (zstd-compressed). Objects whose disk usage is below
// meta.ThresholdBytes are bucketed into "<smaller objects>" rollup rows while
// each directory keeps its exact recursive totals.
//
// Rows are written as a sequence of row groups, each internally sorted by
// (path, scan_ts) and declaring that order in its metadata: emitted rows are
// buffered sortChunkRows at a time, sorted as plain structs, then written and
// flushed so every sorted chunk becomes exactly one row group. That is all
// multi-snapshot compaction needs — its k-way merge consumes *row groups* — while
// never buffering a whole-file []Row. (parquet-go's SortingWriter would sort
// the whole file globally, but measured ~5x the memory and wall clock of this
// approach via its temp-buffer/merge/recompress cycle, and its default
// in-memory sorting pool corrupts large merges in v0.30.1 — readerAt.ReadAt
// passes memory.Buffer's legal 32 KiB short reads through, violating the
// io.ReaderAt contract. Global order buys nothing for a single snapshot.)
//
// The footer carries the FormatKey/SnapshotsKey manifest describing the snapshot.
func WriteTree(w io.Writer, root fs.Item, meta *ScanMeta) error {
	pw := newSnapshotWriter(w)

	// Buffer one chunk of rows at a time (never a whole-file []Row: a
	// full-system scan has millions of rows, and holding them all — then on
	// macOS never returning the freed pages — was the save-snapshots RSS blow-up).
	chunk := make([]Row, 0, sortChunkRows)
	var rowCount int64
	var writeErr error
	flushChunk := func() {
		if writeErr != nil || len(chunk) == 0 {
			return
		}
		sortChunk(chunk)
		if _, writeErr = pw.Write(chunk); writeErr != nil {
			return
		}
		// Close the row group so it holds exactly this sorted chunk.
		writeErr = pw.Flush()
		chunk = chunk[:0]
	}
	emit := func(r Row) {
		if writeErr != nil {
			return // stop accumulating once a write has failed (bounds error-path RSS)
		}
		rowCount++
		chunk = append(chunk, r)
		if len(chunk) >= sortChunkRows {
			flushChunk()
		}
	}

	rootPath := root.GetPath()
	emitDir(root, rootPath, filepath.Dir(rootPath), 0, meta, emit)
	flushChunk()

	if writeErr != nil {
		_ = pw.Close()
		return writeErr
	}

	manifest, err := marshalManifest([]SnapshotInfo{{
		ScanRoot:       meta.ScanRoot,
		ScanTs:         meta.ScanTime,
		Host:           meta.Host,
		Username:       meta.Username,
		SudoUser:       meta.SudoUser,
		Rows:           rowCount,
		TotalDsize:     root.GetUsage(),
		ThresholdBytes: meta.ThresholdBytes,
		ErrCount:       countReadErrorDirs(root),
	}})
	if err != nil {
		_ = pw.Close()
		return err
	}
	pw.SetKeyValueMetadata(FormatKey, FormatVersion)
	pw.SetKeyValueMetadata(SnapshotsKey, manifest)

	return pw.Close()
}

// newSnapshotWriter returns a GenericWriter configured the way every gdu
// snapshot is written — zstd-compressed, capped row groups, (path, scan_ts)
// ascending sort order declared in each row group's metadata. Shared by
// WriteTree, the compaction merge and the legacy sorted rewrite so all
// outputs are mutually mergeable.
func newSnapshotWriter(w io.Writer) *parquet.GenericWriter[Row] {
	return parquet.NewGenericWriter[Row](w,
		parquet.Compression(&zstd.Codec{}),
		// parquet-go defaults to math.MaxInt64 rows/group, i.e. the whole dataset
		// buffered in memory as one group; callers flush per sorted chunk, this
		// is a safety net.
		parquet.MaxRowsPerRowGroup(maxRowsPerRowGroup),
		// Declares the row groups' sort order in their metadata (GenericWriter
		// never reorders rows — callers write rows already sorted).
		parquet.SortingWriterConfig(
			parquet.SortingColumns(
				parquet.Ascending("path"),
				parquet.Ascending("scan_ts"),
			),
		),
	)
}

// sortChunk orders rows by (path, scan_ts) with plain struct comparisons —
// no parquet value boxing. Go's string comparison is unsigned byte-wise and
// int64 is signed, matching parquet's ordering for these column types, so a
// merge of the resulting row groups stays correctly sorted.
func sortChunk(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Path != rows[j].Path {
			return rows[i].Path < rows[j].Path
		}
		return rows[i].ScanTs < rows[j].ScanTs
	})
}

// sortChunkRows is how many rows are buffered, sorted and flushed per row
// group (~25 MB of Row structs + strings, reused across chunks). A var only so
// tests can shrink it to exercise multi-chunk output cheaply.
var sortChunkRows = 64 << 10

// maxRowsPerRowGroup caps rows per row group if a chunk ever exceeds it.
const maxRowsPerRowGroup = 128 << 10

// emitDir emits rows for dir's significant children (plus a rollup bucket for the
// rest) and for dir itself, returning the exact recursive (files, folders) counts
// of dir's descendants (not counting dir itself).
func emitDir(
	dir fs.Item, path, parentPath string, depth int32, meta *ScanMeta, emit emitFunc,
) (descFiles, descFolders int64) {
	var bucketASize, bucketDSize, bucketFiles, bucketFolders int64

	// Iterate in stored order (SortByNone avoids a per-directory sorted copy);
	// WriteTree sorts each buffered chunk into (path, scan_ts) order before it
	// becomes a row group. Child paths are built only for items we actually
	// emit, not for sub-threshold rollups.
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

// countReadErrorDirs returns how many directories in root's subtree gdu could
// not read (flag '!'), for the snapshot manifest's ErrCount — the evidence the
// launcher's sudo tip draws on. Only '!' (the unreadable directory itself) is
// counted, never the '.' propagated to its readable ancestors, so the number
// is "folders we couldn't open", not their ancestors too. It walks the real
// scanned tree independently of the export threshold (a read-error dir has
// zero known size and would otherwise roll up), read-only and allocation-free,
// so it never raises the write's peak RSS.
//
// GetFlag is only ever called on directory nodes here. That is within the
// contract WriteTree already relies on: dirRow → stampItemAttrs calls GetFlag on
// the root and every emitted dir, and the analyzers that produce saved trees
// (ParallelAnalyzer, the sequential/stored variants) emit uniform trees — so a
// sub-threshold dir is the same type as the root, never a GetFlag-panicking
// SimpleDir/ParentDir (those come from the top-dir analyzer and browse-parent,
// which never reach a snapshot write).
func countReadErrorDirs(root fs.Item) int64 {
	var n int64
	if root.GetFlag() == '!' {
		n++
	}
	for child := range root.GetFiles(fs.SortByNone, fs.SortAsc) {
		if child.IsDir() {
			n += countReadErrorDirs(child)
		}
	}
	return n
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
