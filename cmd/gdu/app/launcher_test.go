package app

import (
	"testing"

	"github.com/dundee/gdu/v5/internal/testdev"
	"github.com/dundee/gdu/v5/internal/testdir"
	"github.com/stretchr/testify/assert"
)

// TestLauncherEnabled pins the skip matrix: the launcher is the front
// door for a plain interactive run, and steps aside for launcher:false and for
// any flag that already names a source/target to open directly.
func TestLauncherEnabled(t *testing.T) {
	cases := []struct {
		name  string
		flags Flags
		want  bool
	}{
		{"plain", Flags{Launcher: true}, true},
		{"disks (interactive -d)", Flags{Launcher: true, ShowDisks: true}, true},
		{"launcher:false", Flags{Launcher: false}, false},
		{"-f import", Flags{Launcher: true, InputFile: "x.parquet"}, false},
		{"--snapshot", Flags{Launcher: true, Snapshot: "latest"}, false},
		{"--read-from-storage", Flags{Launcher: true, ReadFromStorage: true}, false},
		{"--db", Flags{Launcher: true, DbPath: "x.sqlite"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{Flags: &tc.flags}
			assert.Equal(t, tc.want, a.launcherEnabled())
		})
	}
}

// TestRunOpensLauncherInteractive checks bare `gdu` in a terminal lands in the
// launcher (no error, no output — the classic device-list/scan paths would each
// leave their own trace).
func TestRunOpensLauncherInteractive(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Launcher: true},
		[]string{},
		true,
		testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)
	assert.Empty(t, out)
}

// TestRunOpensLauncherWithPath checks `gdu <path>` also lands in the launcher.
func TestRunOpensLauncherWithPath(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Launcher: true},
		[]string{"test_dir"},
		true,
		testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)
	assert.Empty(t, out)
}

// TestRunLauncherDisksInteractive checks `gdu -d` in a terminal lands in the
// launcher (disks focused), not the standalone device table.
func TestRunLauncherDisksInteractive(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Launcher: true, ShowDisks: true},
		[]string{},
		true,
		testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)
	assert.Empty(t, out)
}

// TestRunLauncherSkippedNonInteractive checks a piped `-d` run never enters the
// launcher: it prints the device table to stdout as before.
func TestRunLauncherSkippedNonInteractive(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Launcher: true, ShowDisks: true},
		[]string{},
		false, // not a tty
		testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)
	assert.Contains(t, out, "Device", "non-interactive -d prints the device table, not the launcher")
}
