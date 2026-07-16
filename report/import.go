package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/parquet"
)

// parquetMagic frames every Parquet file at both ends.
const parquetMagic = "PAR1"

// ReadAnalysis reads an analysis report (JSON or Parquet) and returns the
// directory tree, loading the most recent snapshot from a multi-snapshot Parquet
// file. See ReadAnalysisWithSnapshot to choose a specific snapshot.
func ReadAnalysis(input io.Reader) (dir *analyze.Dir, err error) {
	return ReadAnalysisWithSnapshot(input, parquet.SnapshotSelector{})
}

// ReadAnalysisWithSnapshot reads an analysis report (JSON or Parquet) and returns
// the directory tree. The format is detected from the leading file magic, so
// the same code path serves both `gdu -f file.json` and `gdu -f file.parquet`
// (including stdin). sel selects a snapshot from a multi-snapshot Parquet file; it
// is ignored for JSON input (which holds a single tree).
func ReadAnalysisWithSnapshot(input io.Reader, sel parquet.SnapshotSelector) (dir *analyze.Dir, err error) {
	// A seekable file lets us sniff the magic and stream a Parquet snapshot
	// directly, without buffering the whole (possibly multi-hundred-MB) file.
	if f, ok := input.(*os.File); ok {
		if d, handled, perr := readParquetFile(f, sel); handled {
			return d, perr
		}
	}

	var buff bytes.Buffer
	if _, err = buff.ReadFrom(input); err != nil {
		return nil, err
	}
	raw := buff.Bytes()
	if len(raw) >= len(parquetMagic) && string(raw[:len(parquetMagic)]) == parquetMagic {
		return parquet.ReadTreeSelected(bytes.NewReader(raw), int64(len(raw)), sel)
	}
	return parseJSONAnalysis(raw)
}

// readParquetFile streams a Parquet snapshot straight from a seekable file when
// the magic matches. handled is false (with no error) when f is not a readable
// Parquet file, so the caller falls back to buffering + JSON parsing.
func readParquetFile(f *os.File, sel parquet.SnapshotSelector) (dir *analyze.Dir, handled bool, err error) {
	st, statErr := f.Stat()
	if statErr != nil {
		return nil, false, nil
	}
	magic := make([]byte, len(parquetMagic))
	if _, rerr := f.ReadAt(magic, 0); rerr != nil || string(magic) != parquetMagic {
		return nil, false, nil
	}
	d, derr := parquet.ReadTreeSelected(f, st.Size(), sel)
	return d, true, derr
}

func parseJSONAnalysis(raw []byte) (*analyze.Dir, error) {
	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}

	dataArray, ok := data.([]any)
	if !ok {
		return nil, errors.New("JSON file does not contain top level array")
	}
	if len(dataArray) < 4 {
		return nil, errors.New("top level array must have at least 4 items")
	}

	items, ok := dataArray[3].([]any)
	if !ok {
		return nil, errors.New("array of maps not found in the top level array on 4th position")
	}

	return processDir(items)
}

func processDir(items []any) (dir *analyze.Dir, err error) {
	if len(items) == 0 {
		return nil, errors.New("directory array is empty")
	}

	dir = &analyze.Dir{
		File: &analyze.File{
			Flag: ' ',
		},
	}
	dirMap, ok := items[0].(map[string]any)
	if !ok {
		return nil, errors.New("directory item is not a map")
	}
	name, ok := dirMap["name"].(string)
	if !ok {
		return nil, errors.New("directory name is not a string")
	}
	if mtime, ok := dirMap["mtime"].(float64); ok {
		dir.Mtime = time.Unix(int64(mtime), 0)
	}

	slashPos := strings.LastIndex(name, "/")
	if slashPos > -1 {
		dir.Name = name[slashPos+1:]
		dir.BasePath = name[:slashPos+1]
	} else {
		dir.Name = name
	}

	for _, v := range items[1:] {
		switch item := v.(type) {
		case map[string]any:
			file := &analyze.File{}
			name, ok := item["name"].(string)
			if !ok {
				return nil, errors.New("file name is not a string")
			}
			file.Name = name

			if asize, ok := item["asize"].(float64); ok {
				file.Size = int64(asize)
			}
			if dsize, ok := item["dsize"].(float64); ok {
				file.Usage = int64(dsize)
			}
			if mtime, ok := item["mtime"].(float64); ok {
				file.Mtime = time.Unix(int64(mtime), 0)
			}
			if _, ok := item["notreg"].(bool); ok {
				file.Flag = '@'
			} else {
				file.Flag = ' '
			}
			if mli, ok := item["ino"].(float64); ok {
				file.Mli = uint64(mli)
			}
			if _, ok := item["hlnkc"].(bool); ok {
				file.Flag = 'H'
			}

			file.Parent = dir

			dir.AddFile(file)
		case []any:
			subdir, err := processDir(item)
			if err != nil {
				return nil, err
			}
			subdir.Parent = dir
			dir.AddFile(subdir)
		}
	}

	return dir, nil
}
