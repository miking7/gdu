package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/internal/testdev"
	"github.com/dundee/gdu/v5/internal/testdir"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/device"
	gfs "github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/stdout"
	"github.com/dundee/gdu/v5/tui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	log.SetLevel(log.WarnLevel)
}

func TestVersion(t *testing.T) {
	out, err := runApp(t,
		&Flags{ShowVersion: true},
		[]string{},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "Version:\t development")
	assert.Nil(t, err)
}

func TestShouldRunInNonInteractiveModeInteractiveOverridesNoTTY(t *testing.T) {
	flags := &Flags{Interactive: true}

	assert.False(t, flags.ShouldRunInNonInteractiveMode(false))
}

func TestShouldRunInNonInteractiveMode(t *testing.T) {
	flags := &Flags{NonInteractive: true}

	assert.True(t, flags.ShouldRunInNonInteractiveMode(false))
}

func TestShouldRunInNonInteractiveModeInteractiveKeepsNonInteractiveOnlyFlags(t *testing.T) {
	flags := &Flags{Interactive: true, Summarize: true}

	assert.True(t, flags.ShouldRunInNonInteractiveMode(false))
}

// TestSaveSnapshotsEnabledTruthTable pins the tri-state: auto (and the
// empty value from an untouched yaml key) saves interactive scans only, always
// saves everywhere, never saves nowhere.
func TestSaveSnapshotsEnabledTruthTable(t *testing.T) {
	interactive := func(mode string) *Flags { return &Flags{SaveSnapshots: mode} }
	nonInteractive := func(mode string) *Flags { return &Flags{SaveSnapshots: mode, NonInteractive: true} }

	for _, mode := range []string{"auto", ""} {
		assert.True(t, interactive(mode).SaveSnapshotsEnabled(true), "%q must save interactively", mode)
		assert.False(t, nonInteractive(mode).SaveSnapshotsEnabled(true), "%q must not save non-interactively", mode)
		assert.False(t, interactive(mode).SaveSnapshotsEnabled(false), "%q must not save piped", mode)
	}
	assert.True(t, (&Flags{SaveSnapshots: "always"}).SaveSnapshotsEnabled(true))
	assert.True(t, (&Flags{SaveSnapshots: "always", NonInteractive: true}).SaveSnapshotsEnabled(true))
	assert.False(t, (&Flags{SaveSnapshots: "never"}).SaveSnapshotsEnabled(true))
	assert.False(t, (&Flags{SaveSnapshots: "never", NonInteractive: true}).SaveSnapshotsEnabled(true))
	// -o exports and --top runs count as non-interactive, so auto skips them.
	assert.False(t, (&Flags{SaveSnapshots: "auto", OutputFile: "x.json"}).SaveSnapshotsEnabled(true))
	assert.False(t, (&Flags{SaveSnapshots: "auto", Top: 5}).SaveSnapshotsEnabled(true))
}

func TestInteractiveAndNonInteractiveConflict(t *testing.T) {
	out, err := runApp(t,
		&Flags{Interactive: true, NonInteractive: true},
		[]string{"."},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.ErrorContains(t, err, "--interactive and --non-interactive cannot be used at once")
}

func TestAnalyzePath(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null"},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

func TestAnalyzePathWithShowItemCountNonInteractive(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", ShowItemCount: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Nil(t, err)
	assert.Regexp(t, regexp.MustCompile(`(?m)\s+\d+\s+/nested$`), out)
}

func TestSequentialScanning(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", SequentialScanning: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

func TestFollowSymlinks(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", FollowSymlinks: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

func TestShowAnnexedSize(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", ShowAnnexedSize: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

func TestAnalyzePathProfiling(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Profiling: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

func TestAnalyzePathWithIgnoring(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{
			LogFile:           "/dev/null",
			IgnoreDirPatterns: []string{"/(abc)+"},
			NoHidden:          true,
		},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

func TestAnalyzePathWithIgnoringPatternError(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{
			LogFile:           "/dev/null",
			IgnoreDirPatterns: []string{"[[["},
			NoHidden:          true,
		},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Equal(t, out, "")
	assert.NotNil(t, err)
}

func TestAnalyzePathWithIgnoringFromNotExistingFile(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{
			LogFile:        "/dev/null",
			IgnoreFromFile: "file",
			NoHidden:       true,
		},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Equal(t, out, "")
	assert.NotNil(t, err)
}

func TestAnalyzePathWithGui(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null"},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestAnalyzePathWithGuiNoColor(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", NoColor: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestGuiShowMTimeAndItemCount(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", ShowItemCount: true, ShowMTime: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestGuiNoDelete(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", NoDelete: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestGuiNoViewFile(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", NoViewFile: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestGuiNoSpawnShell(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", NoSpawnShell: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestGuiDeleteInParallel(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", DeleteInParallel: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestAnalyzePathWithGuiBackgroundDeletion(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", DeleteInBackground: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestAnalyzePathWithDefaultSorting(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{
			LogFile: "/dev/null",
			Sorting: Sorting{
				By:    "name",
				Order: "asc",
			},
		},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestAnalyzePathWithStyle(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{
			LogFile: "/dev/null",
			Style: Style{
				SelectedRow: ColorStyle{
					TextColor:       "black",
					BackgroundColor: "red",
				},
				Marked: ColorStyle{
					TextColor:       "white",
					BackgroundColor: "blue",
				},
				ProgressModal: ProgressModalOpts{
					CurrentItemNameMaxLen: 10,
				},
				Footer: FooterColorStyle{
					TextColor:       "black",
					BackgroundColor: "red",
					NumberColor:     "white",
				},
				Header: HeaderColorStyle{
					TextColor:       "black",
					BackgroundColor: "red",
					Hidden:          true,
				},
				ResultRow: ResultRowColorStyle{
					NumberColor:    "orange",
					DirectoryColor: "blue",
				},
				UseOldSizeBar:     true,
				ShowBarPercentage: true,
			},
		},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestAnalyzePathNoUnicode(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{
			LogFile:   "/dev/null",
			NoUnicode: true,
		},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

func TestAnalyzePathWithExport(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.json")
	}()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", OutputFile: "output.json"},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.NotEmpty(t, out)
	assert.Nil(t, err)
}

func TestAnalyzePathWithExportAndTop(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.json")
	}()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", OutputFile: "output.json", Top: 2},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)

	content, err := os.ReadFile("output.json")
	assert.Nil(t, err)
	assert.Contains(t, string(content), `"name":"file"`)
	assert.Contains(t, string(content), `"name":"file2"`)
	assert.NotContains(t, string(content), `"name":"nested"`)
}

func TestAnalyzePathWithExportAndDepth(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.json")
	}()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", OutputFile: "output.json", Depth: 1},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)

	content, err := os.ReadFile("output.json")
	assert.Nil(t, err)
	assert.Contains(t, string(content), `"name":"nested"`)
	assert.NotContains(t, string(content), `"name":"subnested"`)
}

func TestAnalyzePathWithExportAndSummarize(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.json")
	}()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", OutputFile: "output.json", Summarize: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)

	content, err := os.ReadFile("output.json")
	assert.Nil(t, err)
	assert.Contains(t, string(content), "test_dir")
	assert.NotContains(t, string(content), `"name":"nested"`)
}

// TestExportToParquetRejectsScopeFilters asserts a Parquet export combined with
// --top/--depth/--summarize fails at startup: a snapshot's manifest claims a
// complete scan, so a scope-filtered file must not be written under that
// identity. The rejection fires before the output file is opened or anything is
// scanned.
func TestExportToParquetRejectsScopeFilters(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.parquet")
		os.Remove("out.bin")
	}()

	cases := []struct {
		name  string
		flags *Flags
	}{
		{"top", &Flags{LogFile: "/dev/null", OutputFile: "output.parquet", Top: 5}},
		{"depth", &Flags{LogFile: "/dev/null", OutputFile: "output.parquet", Depth: 2}},
		{"summarize", &Flags{LogFile: "/dev/null", OutputFile: "output.parquet", Summarize: true}},
		{"output-format flag", &Flags{LogFile: "/dev/null", OutputFile: "out.bin", OutputFormat: "parquet", Top: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runApp(t, tc.flags, []string{"test_dir"}, false, testdev.DevicesInfoGetterMock{})
			assert.Empty(t, out)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "cannot be combined with Parquet export")
			// The rejection is before the file open, so no stray output is left.
			_, statErr := os.Stat(tc.flags.OutputFile)
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

func TestAnalyzePathWithChdir(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{
			LogFile:   "/dev/null",
			ChangeCwd: true,
		},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestReadAnalysisFromFile(t *testing.T) {
	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", InputFile: "../../../internal/testdata/test.json"},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.NotEmpty(t, out)
	assert.Contains(t, out, "main.go")
	assert.Nil(t, err)
}

func TestReadWrongAnalysisFromFile(t *testing.T) {
	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", InputFile: "../../../internal/testdata/wrong.json"},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Contains(t, err.Error(), "array of maps not found")
}

func TestWrongCombinationOfPrefixes(t *testing.T) {
	out, err := runApp(t,
		&Flags{NoPrefix: true, UseSIPrefix: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Contains(t, err.Error(), "cannot be used at once")
}

func TestReadWrongAnalysisFromNotExistingFile(t *testing.T) {
	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", InputFile: "xxx.json"},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Empty(t, out)
	assert.Contains(t, err.Error(), "no such file or directory")
}

func TestAnalyzePathWithErr(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	buff := bytes.NewBufferString("")

	app := App{
		Flags:       &Flags{LogFile: "/dev/null"},
		Args:        []string{"xxx"},
		Istty:       false,
		Writer:      buff,
		TermApp:     testapp.CreateMockedApp(false),
		Getter:      testdev.DevicesInfoGetterMock{},
		PathChecker: os.Stat,
	}
	err := app.Run()

	assert.Equal(t, "", strings.TrimSpace(buff.String()))
	assert.Contains(t, err.Error(), "no such file or directory")
}

func TestNoCross(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", NoCross: true},
		[]string{"test_dir"},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "nested")
	assert.Nil(t, err)
}

// noCrossDevices is a mount table with one filesystem mounted inside another.
func noCrossDevices() device.DevicesInfoGetter {
	return testdev.DevicesInfoGetterMock{Devices: device.Devices{
		{Name: "/dev/disk1", MountPoint: "/"},
		{Name: "/dev/disk2", MountPoint: "/mnt/data"},
	}}
}

func TestSetNoCrossAppendsMountsForNonInteractiveRun(t *testing.T) {
	a := App{Flags: &Flags{NoCross: true}, Getter: noCrossDevices()}

	require.NoError(t, a.setNoCross(&uiTimeFilterMock{}, "/"))

	assert.Contains(t, a.Flags.IgnoreDirs, "/mnt/data",
		"a non-interactive run scans the startup path, so its mounts resolve once, here")
}

func TestSetNoCrossSkipsTheTUI(t *testing.T) {
	sim := testapp.CreateSimScreen()
	defer sim.Fini()
	tuiUI := tui.CreateUI(testapp.CreateMockedApp(false), sim, &bytes.Buffer{}, false, false, false, false)
	a := App{Flags: &Flags{NoCross: true}, Getter: noCrossDevices()}

	require.NoError(t, a.setNoCross(tuiUI, "/"))

	assert.Empty(t, a.Flags.IgnoreDirs,
		"the TUI resolves the boundary per scan root; a startup-derived one would linger all session")
}

func TestListDevices(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", ShowDisks: true},
		[]string{},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Contains(t, out, "Device")
	assert.Nil(t, err)
}

func TestListDevicesToFile(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	defer func() {
		os.Remove("output.json")
	}()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", ShowDisks: true, OutputFile: "output.json"},
		[]string{},
		false,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Equal(t, "", out)
	assert.Contains(t, err.Error(), "not supported")
}

func TestListDevicesWithGui(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", ShowDisks: true},
		[]string{},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Nil(t, err)
	assert.Empty(t, out)
}

func TestMaxCores(t *testing.T) {
	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", MaxCores: 1},
		[]string{},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.Equal(t, 1, runtime.GOMAXPROCS(0))
	assert.Nil(t, err)
}

func TestMaxCoresHighEdge(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("Skipping on a single core CPU")
	}
	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", MaxCores: runtime.NumCPU() + 1},
		[]string{},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.NotEqual(t, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	assert.Empty(t, out)
	assert.Nil(t, err)
}

func TestMaxCoresLowEdge(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("Skipping on a single core CPU")
	}
	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", MaxCores: -100},
		[]string{},
		true,
		testdev.DevicesInfoGetterMock{},
	)

	assert.NotEqual(t, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	assert.Empty(t, out)
	assert.Nil(t, err)
}

type uiTimeFilterMock struct {
	timeFilter common.TimeFilter
}

func (m *uiTimeFilterMock) ListDevices(getter device.DevicesInfoGetter) error { return nil }
func (m *uiTimeFilterMock) AnalyzePath(path string, parentDir gfs.Item) error { return nil }
func (m *uiTimeFilterMock) ReadAnalysis(input io.Reader) error                { return nil }
func (m *uiTimeFilterMock) ReadFromStorage(storagePath, path string) error    { return nil }
func (m *uiTimeFilterMock) SetIgnoreTypes(types []string)                     {}
func (m *uiTimeFilterMock) SetIgnoreDirPaths(paths []string)                  {}
func (m *uiTimeFilterMock) SetIgnoreDirPatterns(paths []string) error         { return nil }
func (m *uiTimeFilterMock) SetIgnoreFromFile(ignoreFile string) error         { return nil }
func (m *uiTimeFilterMock) SetIgnoreHidden(value bool)                        {}
func (m *uiTimeFilterMock) SetIncludeTypes(types []string)                    {}
func (m *uiTimeFilterMock) SetFollowSymlinks(value bool)                      {}
func (m *uiTimeFilterMock) SetShowAnnexedSize(value bool)                     {}
func (m *uiTimeFilterMock) SetAnalyzer(analyzer common.Analyzer)              {}
func (m *uiTimeFilterMock) SetTimeFilter(timeFilter common.TimeFilter) {
	m.timeFilter = timeFilter
}
func (m *uiTimeFilterMock) SetArchiveBrowsing(value bool)                       {}
func (m *uiTimeFilterMock) SetCollapsePath(value bool)                          {}
func (m *uiTimeFilterMock) SetExportThreshold(threshold int64)                  {}
func (m *uiTimeFilterMock) SetSaveSnapshot(dir string, threshold int64)         {}
func (m *uiTimeFilterMock) SetAutoCompact(value bool)                           {}
func (m *uiTimeFilterMock) SetSnapshotSelector(spec, root string)               {}
func (m *uiTimeFilterMock) SetSnapshotIdentity(root, host string, ts time.Time) {}
func (m *uiTimeFilterMock) StartUILoop() error                                  { return nil }

func TestSetTimeFiltersInvalid(t *testing.T) {
	a := &App{Flags: &Flags{Since: "not-a-date"}}
	ui := &uiTimeFilterMock{}

	err := a.setTimeFilters(ui)

	assert.ErrorContains(t, err, "invalid time filter")
}

func TestSetTimeFiltersSetsFilter(t *testing.T) {
	futureDate := time.Now().Add(48 * time.Hour).Format("2006-01-02")
	a := &App{Flags: &Flags{Since: futureDate}}
	ui := &uiTimeFilterMock{}

	err := a.setTimeFilters(ui)

	assert.Nil(t, err)
	if assert.NotNil(t, ui.timeFilter) {
		assert.False(t, ui.timeFilter(time.Now()))
		assert.True(t, ui.timeFilter(time.Now().Add(72*time.Hour)))
	}
}

func TestCompactSnapshotsEmptyArchive(t *testing.T) {
	buff := bytes.NewBufferString("")
	a := &App{Flags: &Flags{SnapshotsDir: t.TempDir(), LogFile: "/dev/null"}, Writer: buff}

	assert.Nil(t, a.CompactSnapshots(false))
	assert.Contains(t, buff.String(), "Nothing to compact.")
}

func TestCompactSnapshotsDryRunEmptyArchive(t *testing.T) {
	buff := bytes.NewBufferString("")
	a := &App{Flags: &Flags{SnapshotsDir: t.TempDir(), LogFile: "/dev/null"}, Writer: buff}

	assert.Nil(t, a.CompactSnapshots(true))
	assert.Contains(t, buff.String(), "Nothing to compact.")
}

// TestSaveSnapshotsAutoCompactsByDefault: auto-compaction now rides on every
// snapshot save unless --no-auto-compact.
func TestSaveSnapshotsAutoCompactsByDefault(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	snapshotsDir := t.TempDir()
	// A loose daily from January 2000 — a month closed forever — so the
	// post-save auto-compaction has work to do.
	assert.Nil(t, testanalyze.WriteClosedMonthSnapshot(snapshotsDir))

	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", SaveSnapshots: "always", SnapshotsDir: snapshotsDir},
		[]string{"test_dir"}, false, testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)

	entries, err := os.ReadDir(snapshotsDir)
	assert.Nil(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Contains(t, names, "monthly_2000-01_data.parquet")
	assert.NotContains(t, names, "snapshot_20000115T120000_data.parquet")
	assert.Len(t, names, 2) // the monthly + today's snapshot
}

// TestNoAutoCompactLeavesArchiveAlone: --no-auto-compact opts out of the
// post-save compaction; the closed-month daily must survive.
func TestNoAutoCompactLeavesArchiveAlone(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	snapshotsDir := t.TempDir()
	assert.Nil(t, testanalyze.WriteClosedMonthSnapshot(snapshotsDir))

	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", SaveSnapshots: "always", NoAutoCompact: true, SnapshotsDir: snapshotsDir},
		[]string{"test_dir"}, false, testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)

	entries, err := os.ReadDir(snapshotsDir)
	assert.Nil(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Contains(t, names, "snapshot_20000115T120000_data.parquet")
	assert.Len(t, names, 2) // the untouched daily + today's snapshot
}

// TestSaveSnapshotsNonInteractiveTruthTable pins the tri-state end-to-end for the
// non-interactive path: only "always" writes a snapshot; "auto" (the default),
// an empty value, and "never" leave the archive untouched.
func TestSaveSnapshotsNonInteractiveTruthTable(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		wantSaved  bool
	}{
		{"auto", saveSnapshotsAuto, false},
		{"never", saveSnapshotsNever, false},
		{"empty", "", false},
		{"always", saveSnapshotsAlways, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fin := testdir.CreateTestDir()
			defer fin()

			snapshotsDir := filepath.Join(t.TempDir(), "snapshots")
			_, err := runApp(t,
				&Flags{LogFile: "/dev/null", SaveSnapshots: tc.mode, SnapshotsDir: snapshotsDir},
				[]string{"test_dir"}, false, testdev.DevicesInfoGetterMock{},
			)
			assert.Nil(t, err)

			entries, err := os.ReadDir(snapshotsDir)
			if tc.wantSaved {
				assert.Nil(t, err)
				assert.Len(t, entries, 1, "save-snapshots=%q must write a snapshot", tc.mode)
			} else {
				assert.True(t, os.IsNotExist(err),
					"save-snapshots=%q must not even create the snapshots dir", tc.mode)
			}
		})
	}
}

// TestSaveSnapshotsAlwaysForcesFullAnalyzer: the non-interactive default
// TopDirAnalyzer keeps only top-level totals, so enabling the save must swap
// in the full-tree analyzer (stdout.SetSaveSnapshot does it), and a non-saving
// auto run keeps the constant-memory default.
func TestSaveSnapshotsAlwaysForcesFullAnalyzer(t *testing.T) {
	makeUI := func(mode string) *stdout.UI {
		a := &App{Flags: &Flags{SaveSnapshots: mode}, Istty: false, Writer: bytes.NewBufferString("")}
		ui, err := a.createUI("/")
		assert.Nil(t, err)
		stdoutUI, ok := ui.(*stdout.UI)
		assert.True(t, ok)
		if a.Flags.SaveSnapshotsEnabled(a.Istty) {
			// what Run does when saving is on
			ui.SetSaveSnapshot(t.TempDir(), 1)
		}
		return stdoutUI
	}

	assert.IsType(t, &analyze.ParallelAnalyzer{}, makeUI("always").Analyzer)
	assert.IsType(t, &analyze.TopDirAnalyzer{}, makeUI("auto").Analyzer)
}

// TestSaveSnapshotsRejectsInvalidValue checks the tri-state is validated before
// any scan runs: an unknown value is a usage error, not silently treated as off.
func TestSaveSnapshotsRejectsInvalidValue(t *testing.T) {
	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", SaveSnapshots: "sometimes"},
		[]string{}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.ErrorContains(t, err, "invalid --save-snapshots")
}

// TestRunAppSandboxesSnapshotsDir is the regression guard for Finding 1: because
// save-snapshots defaults to "auto" (which records interactive scans), the
// shared runApp helper must divert an unset SnapshotsDir to a per-test temp dir.
// If that sandbox is ever removed, an interactive scan would save into — and
// auto-compact — the user's real ~/.local/share/gdu/snapshots archive. Assert
// the helper never leaves an interactive run pointed at the real archive.
func TestRunAppSandboxesSnapshotsDir(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	realDir, err := (&App{Flags: &Flags{}}).resolveSnapshotsDir()
	assert.NoError(t, err)

	// Default flags: save-snapshots is "auto" and SnapshotsDir is unset.
	flags := &Flags{LogFile: "/dev/null"}
	_, err = runApp(t, flags, []string{"test_dir"}, true, testdev.DevicesInfoGetterMock{})
	assert.NoError(t, err)

	assert.NotEmpty(t, flags.SnapshotsDir, "runApp must divert an unset SnapshotsDir to a sandbox temp dir")
	assert.NotEqual(t, realDir, flags.SnapshotsDir,
		"interactive scan under save-snapshots=auto must not target the real archive %s", realDir)
}

// runApp builds and runs an App for a test and returns its captured output.
//
// It sandboxes snapshot recording: unless the test sets SnapshotsDir itself,
// it is defaulted to a per-test t.TempDir(). This matters because
// save-snapshots defaults to "auto", which saves interactive (istty) scans —
// so without this an interactive test would write a real snapshot into the
// user's ~/.local/share/gdu/snapshots archive (and auto-compact it). Defaulting
// here means no test can regress into touching the real archive.
//
// nolint: unparam // Why: it's used in linux tests
func runApp(t *testing.T, flags *Flags, args []string, istty bool, getter device.DevicesInfoGetter) (output string, err error) {
	t.Helper()
	if flags.SnapshotsDir == "" {
		flags.SnapshotsDir = t.TempDir()
	}
	buff := bytes.NewBufferString("")

	app := App{
		Flags:       flags,
		Args:        args,
		Istty:       istty,
		Writer:      buff,
		TermApp:     testapp.CreateMockedApp(false),
		Getter:      getter,
		PathChecker: testdir.MockedPathChecker,
	}
	err = app.Run()

	return strings.TrimSpace(buff.String()), err
}
