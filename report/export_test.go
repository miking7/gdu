package report

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/testdir"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/stretchr/testify/assert"
)

func init() {
	log.SetLevel(log.WarnLevel)
}

func TestAnalyzePath(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, false, false, 0, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"nested"`)
}

func TestAnalyzePathWithTop(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, false, false, 2, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"file"`)
	assert.Contains(t, reportOutput.String(), `"name":"file2"`)
	assert.NotContains(t, reportOutput.String(), `"name":"nested"`)
	assert.NotContains(t, reportOutput.String(), `"name":"subnested"`)
}

func TestAnalyzePathWithTopAndTypeFilter(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, false, false, 10, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	ui.SetIncludeTypes([]string{"none"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.NotContains(t, reportOutput.String(), `"name":"file"`)
	assert.NotContains(t, reportOutput.String(), `"name":"file2"`)
}

func TestAnalyzePathWithDepth(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 0, 2, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"nested"`)
	assert.Contains(t, reportOutput.String(), `"name":"file2"`)
	assert.Contains(t, reportOutput.String(), `"name":"subnested"`)
}

func TestAnalyzePathWithDepthOne(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 0, 1, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"nested"`)
	assert.NotContains(t, reportOutput.String(), `"name":"file2"`)
	assert.NotContains(t, reportOutput.String(), `"name":"subnested"`)
	assert.NotContains(t, reportOutput.String(), `"name":"file"`)

	readDir, err := ReadAnalysis(bytes.NewReader(reportOutput.Bytes()))
	assert.Nil(t, err)
	assert.Equal(t, "test_dir", readDir.GetName())

	var nested fs.Item
	for f := range readDir.GetFiles(fs.SortByName, fs.SortAsc) {
		if f.GetName() == "nested" {
			nested = f
		}
	}
	assert.NotNil(t, nested)
	assert.True(t, nested.IsDir())
}

func TestAnalyzePathWithSummarize(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 0, 0, true)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"test_dir"`)
	assert.NotContains(t, reportOutput.String(), `"name":"nested"`)
	assert.NotContains(t, reportOutput.String(), `"name":"file"`)
}

func TestAnalyzePathWithTopRoundTrip(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 2, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()
	assert.Nil(t, err)

	result := reportOutput.Bytes()
	assert.Contains(t, string(result), `"name":"file"`)
	assert.Contains(t, string(result), `"name":"file2"`)

	readDir, err := ReadAnalysis(bytes.NewReader(result))
	assert.Nil(t, err)
	assert.Equal(t, "test_dir", readDir.GetName())

	var names []string
	for f := range readDir.GetFiles(fs.SortByName, fs.SortAsc) {
		names = append(names, f.GetName())
	}
	assert.Equal(t, []string{"file", "file2"}, names)
}

func TestAnalyzePathWithTopLargerThanFileCount(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 100, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"file"`)
	assert.Contains(t, reportOutput.String(), `"name":"file2"`)
}

func TestAnalyzePathWithDepthLargerThanTreeDepth(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 0, 100, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"nested"`)
	assert.Contains(t, reportOutput.String(), `"name":"subnested"`)
	assert.Contains(t, reportOutput.String(), `"name":"file"`)
}

func TestAnalyzePathWithSummarizeAndTop(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 2, 0, true)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"test_dir"`)
	assert.NotContains(t, reportOutput.String(), `"name":"file"`)
	assert.NotContains(t, reportOutput.String(), `"name":"nested"`)
}

func TestAnalyzePathWithSummarizeAndDepth(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 0, 1, true)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"test_dir"`)
	assert.NotContains(t, reportOutput.String(), `"name":"nested"`)
	assert.NotContains(t, reportOutput.String(), `"name":"file"`)
}

func TestLimitDirByDepthWithNonDir(t *testing.T) {
	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 0, 1, false)
	file := &analyze.File{Name: "file"}
	result := ui.limitDirByDepth(file, 0)

	assert.Equal(t, file, result)
}

func TestAnalyzePathWithDepthZeroIsIgnored(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, false, false, 0, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"nested"`)
	assert.Contains(t, reportOutput.String(), `"name":"subnested"`)
	assert.Contains(t, reportOutput.String(), `"name":"file"`)
}

func TestAnalyzePathWithProgress(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, true, true, true, 0, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"nested"`)
}

func TestAnalyzePathWithThreshold(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, false, false, 0, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	ui.SetExportThreshold(1 << 20) // 1 MiB collapses the tiny test tree into a rollup
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()
	assert.Nil(t, err)

	out := reportOutput.String()
	// "smaller objects" survives escaping ('<'/'>' are escaped, the words are not).
	assert.Contains(t, out, "smaller objects")
	// Everything sub-threshold collapsed, so the individual entries are gone.
	assert.NotContains(t, out, `"name":"nested"`)
	assert.NotContains(t, out, `"name":"file2"`)
}

// TestExportFiltersRunBeforeThreshold pins the order of the JSON export
// transforms: the scope filter (here --top) is applied first, then the
// threshold rollup runs over the already-filtered tree. The tree is built so
// the two orderings diverge observably: five small files together outweigh
// either big file, so a rollup-first bug would bucket them and let that bucket
// displace a real file from the top-N — dropping big2 and emitting a
// "<smaller objects>" bucket. Filters-first keeps both big files and never
// buckets (both survive --top and both clear the threshold).
func TestExportFiltersRunBeforeThreshold(t *testing.T) {
	root := &analyze.Dir{
		File:     &analyze.File{Name: "root"},
		BasePath: "/",
	}
	addFile := func(name string, sizeUsage int64) {
		root.AddFile(&analyze.File{Name: name, Size: sizeUsage, Usage: sizeUsage, Parent: root})
	}
	addFile("big1", 1000)
	addFile("big2", 1000)
	for i := 0; i < 5; i++ {
		addFile(fmt.Sprintf("small%d", i), 300) // 5 * 300 = 1500 aggregate > 1000
	}
	root.UpdateStats(make(fs.HardLinkedItems, 10))

	var output, reportOutput bytes.Buffer
	ui := CreateExportUI(&output, &reportOutput, false, false, false, 2, 0, false) // --top 2
	ui.SetExportThreshold(500)                                                     // buckets the 300-usage smalls

	var waitWritten sync.WaitGroup
	assert.Nil(t, ui.exportDir(root, &waitWritten))

	out := reportOutput.String()
	// Filters-first: both big files survive --top and clear the threshold.
	assert.Contains(t, out, `"name":"big1"`)
	assert.Contains(t, out, `"name":"big2"`)
	// The smalls were filtered out by --top before the rollup ever ran, so no
	// bucket is emitted; a rollup-first bug would surface one here and drop big2.
	assert.NotContains(t, out, "smaller objects")
}

func TestExportToParquetFile(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	f, err := os.OpenFile("output.parquet", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	assert.Nil(t, err)
	defer func() { os.Remove("output.parquet") }()

	output := bytes.NewBuffer(make([]byte, 10))
	ui := CreateExportUI(output, f, false, false, false, 0, 0, false)
	ui.SetOutputFormat("parquet")
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err = ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	assert.Nil(t, ui.StartUILoop())

	data, err := os.ReadFile("output.parquet")
	assert.Nil(t, err)
	// A valid Parquet file is framed by the "PAR1" magic at both ends.
	assert.Greater(t, len(data), 8)
	assert.Equal(t, "PAR1", string(data[:4]))
	assert.Equal(t, "PAR1", string(data[len(data)-4:]))
}

func TestShowDevices(t *testing.T) {
	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, true, false, 0, 0, false)
	err := ui.ListDevices(device.Getter)

	assert.Contains(t, err.Error(), "not supported")
}

func TestReadAnalysisWhileExporting(t *testing.T) {
	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, true, false, 0, 0, false)
	err := ui.ReadAnalysis(&output)

	assert.Contains(t, err.Error(), "not possible while exporting")
}

func TestExportToFile(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	reportOutput, err := os.OpenFile("output.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	assert.Nil(t, err)
	defer func() {
		os.Remove("output.json")
	}()

	var output bytes.Buffer

	ui := CreateExportUI(&output, reportOutput, false, true, false, 0, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err = ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()
	assert.Nil(t, err)

	reportOutput, err = os.OpenFile("output.json", os.O_RDONLY, 0o644)
	assert.Nil(t, err)
	_, err = reportOutput.Seek(0, 0)
	assert.Nil(t, err)
	buff := make([]byte, 200)
	_, err = reportOutput.Read(buff)
	assert.Nil(t, err)

	assert.Contains(t, string(buff), `"name":"nested"`)
}

func TestExportToFileWithTop(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.json")
	}()

	reportOutput, err := os.OpenFile("output.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	assert.Nil(t, err)

	var output bytes.Buffer

	ui := CreateExportUI(&output, reportOutput, false, true, false, 2, 0, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err = ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()
	assert.Nil(t, err)

	reportOutput, err = os.OpenFile("output.json", os.O_RDONLY, 0o644)
	assert.Nil(t, err)
	_, err = reportOutput.Seek(0, 0)
	assert.Nil(t, err)
	buff := make([]byte, 200)
	_, err = reportOutput.Read(buff)
	assert.Nil(t, err)

	assert.Contains(t, string(buff), `"name":"file"`)
	assert.Contains(t, string(buff), `"name":"file2"`)
	assert.NotContains(t, string(buff), `"name":"nested"`)
}

func TestExportToFileWithDepth(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.json")
	}()

	reportOutput, err := os.OpenFile("output.json", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	assert.Nil(t, err)

	var output bytes.Buffer

	ui := CreateExportUI(&output, reportOutput, false, true, false, 0, 2, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err = ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()
	assert.Nil(t, err)

	reportOutput, err = os.OpenFile("output.json", os.O_RDONLY, 0o644)
	assert.Nil(t, err)
	_, err = reportOutput.Seek(0, 0)
	assert.Nil(t, err)
	buff := make([]byte, 400)
	_, err = reportOutput.Read(buff)
	assert.Nil(t, err)

	assert.Contains(t, string(buff), `"name":"nested"`)
	assert.Contains(t, string(buff), `"name":"file2"`)
	assert.Contains(t, string(buff), `"name":"subnested"`)

	_, err = reportOutput.Seek(0, 0)
	assert.Nil(t, err)
	readDir, err := ReadAnalysis(reportOutput)
	assert.Nil(t, err)
	assert.Equal(t, "test_dir", readDir.GetName())
}

func TestFormatSize(t *testing.T) {
	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, true, false, 0, 0, false)

	assert.Contains(t, ui.formatSize(1), "B")
	assert.Contains(t, ui.formatSize(1<<10+1), "KiB")
	assert.Contains(t, ui.formatSize(1<<20+1), "MiB")
	assert.Contains(t, ui.formatSize(1<<30+1), "GiB")
	assert.Contains(t, ui.formatSize(1<<40+1), "TiB")
	assert.Contains(t, ui.formatSize(1<<50+1), "PiB")
	assert.Contains(t, ui.formatSize(1<<60+1), "EiB")
	assert.Contains(t, ui.formatSize(-1<<10-1), "KiB")
}

func TestFormatSizeDec(t *testing.T) {
	var output bytes.Buffer
	var reportOutput bytes.Buffer

	ui := CreateExportUI(&output, &reportOutput, false, true, true, 0, 0, false)

	assert.Contains(t, ui.formatSize(1), "B")
	assert.Contains(t, ui.formatSize(1<<10+1), "kB")
	assert.Contains(t, ui.formatSize(1<<20+1), "MB")
	assert.Contains(t, ui.formatSize(1<<30+1), "GB")
	assert.Contains(t, ui.formatSize(1<<40+1), "TB")
	assert.Contains(t, ui.formatSize(1<<50+1), "PB")
	assert.Contains(t, ui.formatSize(1<<60+1), "EB")
	assert.Contains(t, ui.formatSize(-1<<10-1), "kB")
}
