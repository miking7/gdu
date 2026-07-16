package app

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/internal/testanalyze"
	"github.com/dundee/gdu/v5/internal/testdev"
)

// mustWriteSnapshot is the archive fixture: one snapshot file of root, scanned
// at ts, whose single file is named fileName so tests can tell which snapshot
// a command loaded.
func mustWriteSnapshot(t *testing.T, dir, name, root, fileName string, ts time.Time) {
	t.Helper()
	require.NoError(t, testanalyze.WriteSnapshot(dir, name, root, fileName, ts))
}

func TestSnapshotArchiveResolutionExactRoot(t *testing.T) {
	dir := t.TempDir()
	// Two snapshots of /tmp/x plus one covering root — exact-root matching must
	// pick /tmp/x, and "latest" the newer of its two.
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "old-file",
		time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local))
	mustWriteSnapshot(t, dir, "b.parquet", "/tmp/x", "new-file",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))
	mustWriteSnapshot(t, dir, "c.parquet", "/tmp", "root-file",
		time.Date(2026, 7, 1, 10, 0, 0, 0, time.Local))

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "latest", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.Nil(t, err)
	assert.Contains(t, out, "new-file")
	assert.NotContains(t, out, "old-file")
	assert.NotContains(t, out, "root-file")
}

func TestSnapshotArchiveResolutionEarliestAndPrefix(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "old-file",
		time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local))
	mustWriteSnapshot(t, dir, "b.parquet", "/tmp/x", "new-file",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "earliest", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)
	assert.Contains(t, out, "old-file")

	out, err = runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "2026-06", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)
	assert.Nil(t, err)
	assert.Contains(t, out, "new-file")
}

func TestSnapshotArchiveResolutionTop(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "big-file",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	// The scheduled-report acceptance shape: a non-interactive --top report served purely
	// from the archive (the scanned path does not even exist on disk).
	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "latest", SnapshotsDir: dir, Top: 20},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.Nil(t, err)
	assert.Contains(t, out, "/tmp/x/big-file")
}

func TestSnapshotArchiveResolutionCoveringRootHint(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "latest", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.ErrorContains(t, err, "no snapshots of /tmp/x")
	assert.ErrorContains(t, err, "covering roots exist")
	assert.ErrorContains(t, err, "/tmp")
}

func TestSnapshotArchiveResolutionListsRootsWhenNoneCover(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/srv/data", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "latest", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.ErrorContains(t, err, "no snapshots of /tmp/x")
	assert.ErrorContains(t, err, "newest per root")
	assert.ErrorContains(t, err, "/srv/data")
}

func TestSnapshotArchiveResolutionAmbiguousPrefix(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "f1",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))
	mustWriteSnapshot(t, dir, "b.parquet", "/tmp/x", "f2",
		time.Date(2026, 6, 2, 10, 0, 0, 0, time.Local))

	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "2026-06", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.ErrorContains(t, err, "ambiguous")
	assert.ErrorContains(t, err, "2026-06-01T10:00:00")
	assert.ErrorContains(t, err, "2026-06-02T10:00:00")
}

func TestSnapshotArchiveResolutionNoMatchListsCandidates(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "f1",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "2025-01", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.ErrorContains(t, err, `no archived snapshot matching --snapshot "2025-01"`)
	assert.ErrorContains(t, err, "2026-06-01T10:00:00")
}

func TestSnapshotArchiveResolutionEmptyArchive(t *testing.T) {
	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "latest", SnapshotsDir: t.TempDir()},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.ErrorContains(t, err, "holds no snapshots")
}

func TestSnapshotArchiveResolutionRootOverride(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/srv/data", "srv-file",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	// --snapshot-root replaces the positional path as the exact-root scope.
	out, err := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "latest", SnapshotRoot: "/srv/data", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.Nil(t, err)
	assert.Contains(t, out, "srv-file")
}

func TestResolveBaselineSelectorPicksCoveringRoot(t *testing.T) {
	dir := t.TempDir()
	// /tmp covers /tmp/x; /srv/data does not. The selector must scope to
	// covering snapshots only.
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp", "f",
		time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local))
	mustWriteSnapshot(t, dir, "b.parquet", "/srv/data", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	a := &App{Flags: &Flags{Baseline: "latest", SnapshotsDir: dir}}
	b, info, err := a.resolveBaseline("/tmp/x")

	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Equal(t, "/tmp", info.ScanRoot)
}

func TestResolveBaselineSelectorAmbiguityHintsBaselineRoot(t *testing.T) {
	dir := t.TempDir()
	// Two covering roots match the same month prefix.
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))
	mustWriteSnapshot(t, dir, "b.parquet", "/tmp/x", "f",
		time.Date(2026, 6, 2, 10, 0, 0, 0, time.Local))

	a := &App{Flags: &Flags{Baseline: "2026-06", SnapshotsDir: dir}}
	_, _, err := a.resolveBaseline("/tmp/x")

	assert.ErrorContains(t, err, "ambiguous")
	assert.ErrorContains(t, err, "--baseline-root")
}

func TestResolveBaselineRootOverride(t *testing.T) {
	dir := t.TempDir()
	// /srv/data does not cover /tmp/x, but --baseline-root selects it explicitly.
	mustWriteSnapshot(t, dir, "a.parquet", "/srv/data", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	a := &App{Flags: &Flags{Baseline: "latest", BaselineRoot: "/srv/data", SnapshotsDir: dir}}
	_, info, err := a.resolveBaseline("/tmp/x")

	require.NoError(t, err)
	assert.Equal(t, "/srv/data", info.ScanRoot)
}

func TestResolveBaselineSelectorNoCoveringSnapshot(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/srv/data", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	a := &App{Flags: &Flags{Baseline: "latest", SnapshotsDir: dir}}
	_, _, err := a.resolveBaseline("/tmp/x")

	assert.ErrorContains(t, err, "no archived snapshot on /tmp/x's volume covers it")
	assert.ErrorContains(t, err, "/srv/data")
}

func TestResolveBaselineFilePath(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "base.parquet", "/tmp/x", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))
	file := filepath.Join(dir, "base.parquet")

	// A value naming an existing file always loads that file, even though it
	// would also parse as a (non-matching) selector.
	a := &App{Flags: &Flags{Baseline: file}}
	b, info, err := a.resolveBaseline("/anywhere/else")

	require.NoError(t, err)
	assert.NotNil(t, b)
	assert.Equal(t, "/tmp/x", info.ScanRoot)
}

func TestResolveBaselineFileRejectsBaselineRoot(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "base.parquet", "/tmp/x", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))
	file := filepath.Join(dir, "base.parquet")

	a := &App{Flags: &Flags{Baseline: file, BaselineRoot: "/tmp/x"}}
	_, _, err := a.resolveBaseline("/tmp/x")

	assert.ErrorContains(t, err, "--baseline-root only scopes a selector")
}

func TestBaselineRootWithoutBaselineErrors(t *testing.T) {
	_, err := runApp(t,
		&Flags{LogFile: "/dev/null", BaselineRoot: "/tmp"},
		[]string{}, true, testdev.DevicesInfoGetterMock{},
	)

	assert.ErrorContains(t, err, "--baseline-root is only meaningful together with --baseline")
}

func TestResolveSnapshotsDirFlagWins(t *testing.T) {
	a := &App{Flags: &Flags{SnapshotsDir: "/explicit"}}
	dir, err := a.resolveSnapshotsDir()
	assert.Nil(t, err)
	assert.Equal(t, "/explicit", dir)
}

func TestResolveSnapshotsDirXDGDataHome(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("XDG_DATA_HOME", "/xdg-data")

	a := &App{Flags: &Flags{}}
	dir, err := a.resolveSnapshotsDir()
	assert.Nil(t, err)
	assert.Equal(t, filepath.Join("/xdg-data", "gdu", "snapshots"), dir)
}

func TestResolveSnapshotsDirDefaultsToLocalShare(t *testing.T) {
	t.Setenv("SUDO_USER", "")
	t.Setenv("XDG_DATA_HOME", "")

	a := &App{Flags: &Flags{}}
	dir, err := a.resolveSnapshotsDir()
	assert.Nil(t, err)

	home, err2 := os.UserHomeDir()
	assert.Nil(t, err2)
	assert.Equal(t, filepath.Join(home, ".local", "share", "gdu", "snapshots"), dir)
}

func TestResolveSnapshotsDirSudoIgnoresXDGEnv(t *testing.T) {
	u, err := user.Current()
	require.NoError(t, err)
	if u.Username == "root" {
		t.Skip("running as root; RealUser would reject SUDO_USER=root")
	}
	// Simulate sudo: the invoking user's home wins and their (unknowable)
	// XDG_DATA_HOME env is not consulted.
	t.Setenv("SUDO_USER", u.Username)
	t.Setenv("SUDO_UID", u.Uid)
	t.Setenv("SUDO_GID", u.Gid)
	t.Setenv("XDG_DATA_HOME", "/root-owned-xdg")

	a := &App{Flags: &Flags{}}
	dir, derr := a.resolveSnapshotsDir()
	assert.Nil(t, derr)
	assert.Equal(t, filepath.Join(u.HomeDir, ".local", "share", "gdu", "snapshots"), dir)
	assert.False(t, strings.HasPrefix(dir, "/root-owned-xdg"))
}

func TestListSnapshotsArchive(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	buff := bytes.NewBufferString("")
	a := &App{Flags: &Flags{SnapshotsDir: dir}, Writer: buff}

	assert.Nil(t, a.ListSnapshots(""))
	assert.Contains(t, buff.String(), "/tmp/x")
	assert.Contains(t, buff.String(), "2026-06-01T10:00:00")
}

func TestListSnapshotsSingleFile(t *testing.T) {
	dir := t.TempDir()
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "f",
		time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local))

	buff := bytes.NewBufferString("")
	a := &App{Flags: &Flags{}, Writer: buff}

	assert.Nil(t, a.ListSnapshots(filepath.Join(dir, "a.parquet")))
	assert.Contains(t, buff.String(), "/tmp/x")
}

func TestListSnapshotsEmptyArchive(t *testing.T) {
	buff := bytes.NewBufferString("")
	a := &App{Flags: &Flags{SnapshotsDir: t.TempDir()}, Writer: buff}

	assert.Nil(t, a.ListSnapshots(""))
	assert.Contains(t, buff.String(), "No snapshots found.")
}

// TestSnapshotArchiveResolutionDedupesInterruptedCompaction: a compaction
// interrupted between writing the monthly and deleting the covered daily
// leaves the same snapshot identity in two files; the resolver must treat
// them as one candidate, not an ambiguity.
func TestSnapshotArchiveResolutionDedupesInterruptedCompaction(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 6, 1, 10, 0, 0, 0, time.Local)
	mustWriteSnapshot(t, dir, "a.parquet", "/tmp/x", "f", ts)
	raw, err := os.ReadFile(filepath.Join(dir, "a.parquet"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.parquet"), raw, 0o600))

	out, rerr := runApp(t,
		&Flags{LogFile: "/dev/null", Snapshot: "2026-06-01T10:00:00", SnapshotsDir: dir},
		[]string{"/tmp/x"}, false, testdev.DevicesInfoGetterMock{},
	)

	assert.Nil(t, rerr)
	assert.Contains(t, out, "f")
}
