package report

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
	log "github.com/sirupsen/logrus"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	log.SetLevel(log.WarnLevel)
}

func TestReadAnalysis(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`
		[1,2,{"progname":"gdu","progver":"development","timestamp":1626806293},
		[{"name":"/home/xxx","mtime":1629333600},
		{"name":"gdu.json","asize":33805233,"dsize":33808384},
		{"name":"sock","notreg":true},
		[{"name":"app"},
		{"name":"app.go","asize":4638,"dsize":8192},
		{"name":"app_linux_test.go","asize":1410,"dsize":4096},
		{"name":"app_linux_test2.go","ino":1234,"hlnkc":true,"asize":1410,"dsize":4096},
		{"name":"app_test.go","asize":4974,"dsize":8192}],
		{"name":"main.go","asize":3205,"dsize":4096,"mtime":1629333600}]]
	`))

	dir, err := ReadAnalysis(buff)

	assert.Nil(t, err)
	assert.Equal(t, "xxx", dir.GetName())
	assert.Equal(t, "/home/xxx", dir.GetPath())
	assert.Equal(t, 2021, dir.GetMtime().Year())
	assert.Equal(t, 2021, dir.Files[3].GetMtime().Year())
	alt2 := dir.Files[2].(*analyze.Dir).Files[2].(*analyze.File)
	assert.Equal(t, "app_linux_test2.go", alt2.Name)
	assert.Equal(t, uint64(1234), alt2.Mli)
	assert.Equal(t, 'H', alt2.Flag)
}

func TestReadAnalysisParquet(t *testing.T) {
	root := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/tmp", ItemCount: 1}
	root.AddFile(&analyze.File{Name: "f.bin", Size: 2048, Usage: 4096, Parent: root})
	root.UpdateStats(make(fs.HardLinkedItems))

	var buf bytes.Buffer
	meta := parquet.ScanMeta{ScanRoot: root.GetPath(), ScanTime: time.Unix(1700000000, 0).UTC()}
	require.NoError(t, parquet.WriteTree(&buf, root, meta))

	// ReadAnalysis must detect the PAR1 magic and route to the Parquet reader.
	dir, err := ReadAnalysis(&buf)
	require.NoError(t, err)
	assert.Equal(t, "root", dir.GetName())
	assert.Equal(t, "/tmp/root", dir.GetPath())

	idx, ok := dir.Files.FindByName("f.bin")
	require.True(t, ok)
	assert.Equal(t, int64(4096), dir.Files[idx].GetUsage())
}

func TestReadAnalysisWithEmptyInput(t *testing.T) {
	buff := bytes.NewBuffer([]byte(``))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "unexpected end of JSON input", err.Error())
}

func TestReadAnalysisWithEmptyDict(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`{}`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "JSON file does not contain top level array", err.Error())
}

func TestReadFromBrokenInput(t *testing.T) {
	_, err := ReadAnalysis(&BrokenInput{})

	assert.Equal(t, "IO error", err.Error())
}

func TestReadAnalysisWithEmptyArray(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`[]`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "top level array must have at least 4 items", err.Error())
}

func TestReadAnalysisWithWrongContent(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`[1,2,3,4]`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "array of maps not found in the top level array on 4th position", err.Error())
}

func TestReadAnalysisWithEmptyContent(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`[1,2,3,[]]`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "directory array is empty", err.Error())
}

func TestReadAnalysisWithEmptyDirContent(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`[1,2,3,[{}]]`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "directory name is not a string", err.Error())
}

func TestReadAnalysisWithWrongDirItem(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`[1,2,3,[1, 2, 3]]`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "directory item is not a map", err.Error())
}

func TestReadAnalysisWithWrongName(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`[1,2,3,[{"name":"/"},{"name":42}]]`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "file name is not a string", err.Error())
}

func TestReadAnalysisWithWrongSubdirItem(t *testing.T) {
	buff := bytes.NewBuffer([]byte(`[1,2,3,[{"name":"xxx"}, [1,2,3]]]`))

	_, err := ReadAnalysis(buff)

	assert.Equal(t, "directory item is not a map", err.Error())
}

type BrokenInput struct{}

func (i *BrokenInput) Read(p []byte) (n int, err error) {
	return 0, errors.New("IO error")
}
