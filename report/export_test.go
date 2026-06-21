package report

import (
	"bytes"
	"os"
	"testing"

	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/testdir"
	"github.com/dundee/gdu/v5/pkg/device"
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

	ui := CreateExportUI(output, reportOutput, false, false, false)
	ui.SetIgnoreDirPaths([]string{"/xxx"})
	err := ui.AnalyzePath("test_dir", nil)
	assert.Nil(t, err)
	err = ui.StartUILoop()

	assert.Nil(t, err)
	assert.Contains(t, reportOutput.String(), `"name":"nested"`)
}

func TestAnalyzePathWithProgress(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, true, true, true)
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

	ui := CreateExportUI(output, reportOutput, false, false, false)
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

func TestExportToParquetFile(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	f, err := os.OpenFile("output.parquet", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	assert.Nil(t, err)
	defer func() { os.Remove("output.parquet") }()

	output := bytes.NewBuffer(make([]byte, 10))
	ui := CreateExportUI(output, f, false, false, false)
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
	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, true, false)
	err := ui.ListDevices(device.Getter)

	assert.Contains(t, err.Error(), "not supported")
}

func TestReadAnalysisWhileExporting(t *testing.T) {
	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, true, false)
	err := ui.ReadAnalysis(output)

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

	output := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, true, false)
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

func TestFormatSize(t *testing.T) {
	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, true, false)

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
	output := bytes.NewBuffer(make([]byte, 10))
	reportOutput := bytes.NewBuffer(make([]byte, 10))

	ui := CreateExportUI(output, reportOutput, false, true, true)

	assert.Contains(t, ui.formatSize(1), "B")
	assert.Contains(t, ui.formatSize(1<<10+1), "kB")
	assert.Contains(t, ui.formatSize(1<<20+1), "MB")
	assert.Contains(t, ui.formatSize(1<<30+1), "GB")
	assert.Contains(t, ui.formatSize(1<<40+1), "TB")
	assert.Contains(t, ui.formatSize(1<<50+1), "PB")
	assert.Contains(t, ui.formatSize(1<<60+1), "EB")
	assert.Contains(t, ui.formatSize(-1<<10-1), "kB")
}
