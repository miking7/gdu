package tui

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/internal/testdir"
)

func TestAnalyzePathAutoCompactInBackground(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()

	snapshotsDir := t.TempDir()
	require.NoError(t, testanalyze.WriteClosedMonthSnapshot(snapshotsDir))

	app := testapp.CreateMockedApp(true)
	ui := CreateUI(app, simScreen, &bytes.Buffer{}, false, true, true, true)
	ui.SetSaveSnapshot(snapshotsDir, 0)
	ui.SetAutoCompact(true)
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("test_dir", nil))

	<-ui.done // scan finished; startAutoCompact has been called by now
	require.NotNil(t, ui.autoCompactDone, "the background run must have started")
	<-ui.autoCompactDone
	assert.False(t, ui.autoCompactRunning.Load())

	var names []string
	entries, err := os.ReadDir(snapshotsDir)
	require.NoError(t, err)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.Contains(t, names, "monthly_2000-01_data.parquet")
	assert.NotContains(t, names, "snapshot_20000115T120000_data.parquet")
	assert.Len(t, names, 2) // monthly + the fresh (open-month) snapshot
}

func TestStartAutoCompactDisabledIsNoOp(t *testing.T) {
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()
	app := testapp.CreateMockedApp(true)
	ui := CreateUI(app, simScreen, &bytes.Buffer{}, false, true, true, true)

	ui.startAutoCompact()

	assert.Nil(t, ui.autoCompactDone, "without --auto-compact nothing starts")
	assert.False(t, ui.autoCompactRunning.Load())
}

func TestQuitAppOffersModalWhileCompacting(t *testing.T) {
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()
	app := testapp.CreateMockedApp(true)
	ui := CreateUI(app, simScreen, &bytes.Buffer{}, false, true, true, true)

	ui.autoCompactRunning.Store(true)
	ui.autoCompactDone = make(chan struct{})
	ui.autoCompactCancel = func() {}

	ui.quitApp(false)
	assert.True(t, ui.pages.HasPage("autocompact-quit"),
		"quitting mid-compaction must ask wait/abort instead of exiting")
}

func TestQuitAppExitsWhenNotCompacting(t *testing.T) {
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()
	app := testapp.CreateMockedApp(true)
	output := &bytes.Buffer{}
	ui := CreateUI(app, simScreen, output, false, true, true, true)
	ui.currentDirPath = "/somewhere"

	ui.quitApp(true)
	assert.False(t, ui.pages.HasPage("autocompact-quit"))
	assert.Contains(t, output.String(), "/somewhere", "'Q' still prints the current dir")

	// finishQuit is once-only: a second quit (wait + abort racing) must not
	// duplicate the exit output.
	ui.quitApp(true)
	assert.Equal(t, 1, bytes.Count(output.Bytes(), []byte("/somewhere")))
}

func TestQuitAppReentryGuardWhileModalOpen(t *testing.T) {
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()
	app := testapp.CreateMockedApp(true)
	ui := CreateUI(app, simScreen, &bytes.Buffer{}, false, true, true, true)

	ui.autoCompactRunning.Store(true)
	ui.autoCompactDone = make(chan struct{})
	ui.autoCompactCancel = func() {}

	ui.quitApp(false)
	require.True(t, ui.pages.HasPage("autocompact-quit"))
	// Pressing q again with the modal up must not stack a second modal or panic.
	ui.quitApp(false)
	assert.True(t, ui.pages.HasPage("autocompact-quit"))
}

// mockDraws is the mocked app's queued-update accessor.
func mockDraws(ui *UI) *testapp.MockedApp { return ui.app.(*testapp.MockedApp) }

// runQueuedDraws executes whatever the mocked app has queued.
func runQueuedDraws(ui *UI) {
	for _, f := range mockDraws(ui).GetUpdateDraws() {
		f()
	}
}

func TestWaitThenQuitFinishesAfterDone(t *testing.T) {
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()
	app := testapp.CreateMockedApp(true)
	output := &bytes.Buffer{}
	ui := CreateUI(app, simScreen, output, false, true, true, true)
	ui.currentDirPath = "/done-here"
	ui.autoCompactDone = make(chan struct{})

	go ui.waitThenQuit(true)

	// waitThenQuit blocks until the compaction goroutine closes autoCompactDone.
	close(ui.autoCompactDone)
	require.Eventually(t, func() bool {
		return mockDraws(ui).PendingDrawCount() > 0
	}, time.Second, 5*time.Millisecond, "waitThenQuit must queue the quit once done closes")
	runQueuedDraws(ui)
	assert.Contains(t, output.String(), "/done-here")
}

func TestHandleShutdownSignalCancelsRunningCompaction(t *testing.T) {
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()
	app := testapp.CreateMockedApp(true)
	output := &bytes.Buffer{}
	ui := CreateUI(app, simScreen, output, false, true, true, true)

	canceled := make(chan struct{})
	ui.autoCompactRunning.Store(true)
	ui.autoCompactDone = make(chan struct{})
	ui.autoCompactCancel = func() { close(canceled) }

	ui.handleShutdownSignal()

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("signal must cancel the in-flight compaction")
	}

	// The compaction goroutine would now unwind and close done; simulate it.
	close(ui.autoCompactDone)
	require.Eventually(t, func() bool {
		return mockDraws(ui).PendingDrawCount() > 0
	}, time.Second, 5*time.Millisecond)
	assert.NotPanics(t, func() { runQueuedDraws(ui) })
}

func TestHandleShutdownSignalWhenIdle(t *testing.T) {
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()
	app := testapp.CreateMockedApp(true)
	output := &bytes.Buffer{}
	ui := CreateUI(app, simScreen, output, false, true, true, true)

	ui.handleShutdownSignal() // not compacting: queues finishQuit directly
	runQueuedDraws(ui)
	// Quitting again must not double-run finishQuit.
	ui.finishQuit(false)
	assert.NotPanics(t, func() { runQueuedDraws(ui) })
}
