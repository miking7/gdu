package parquet

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNeedsCompaction(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, NeedsCompaction(dir, compactNow), "empty archive")
	assert.False(t, NeedsCompaction(filepath.Join(dir, "missing"), compactNow), "missing dir")

	touch := func(name string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	touch("monthly_2026-05_data.parquet")          // compaction output, never a trigger
	touch("snapshot_20260705T100000_data.parquet") // open month
	touch("snapshot_20260801T100000_data.parquet") // future month (clock skew)
	touch("notes.txt")                             // unrelated file
	touch("scanner_20200101T000000.parquet")       // not the snapshot pattern
	assert.False(t, NeedsCompaction(dir, compactNow))

	touch("snapshot_20260510T080000_data.parquet") // closed month: loose daily
	assert.True(t, NeedsCompaction(dir, compactNow))
}

func TestAutoCompactCompactsClosedMonth(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_20260510T080000_tmp_root.parquet",
		time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")

	res, err := AutoCompact(context.Background(), dir, compactNow)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Groups, 1)
	require.NoError(t, res.Groups[0].Err)
	assert.Equal(t, []string{"monthly_2026-05_tmp_root.parquet"}, dirEntries(t, dir))
}

func TestAutoCompactNothingToDo(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_20260705T100000_tmp_root.parquet",
		time.Date(2026, 7, 5, 10, 0, 0, 0, time.UTC), "h1")

	res, err := AutoCompact(context.Background(), dir, compactNow)
	require.NoError(t, err)
	assert.Nil(t, res, "open month only: the predicate must not fire")
	assert.Len(t, dirEntries(t, dir), 1)
}

func TestAutoCompactSkipsSilentlyWhenLocked(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_20260510T080000_tmp_root.parquet",
		time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")

	release, err := acquireCompactionLock(dir, compactNow)
	require.NoError(t, err)
	defer release()

	res, err := AutoCompact(context.Background(), dir, compactNow)
	require.NoError(t, err, "a held lock is not an error for the opportunistic path")
	assert.Nil(t, res)
	assert.Contains(t, dirEntries(t, dir), "snapshot_20260510T080000_tmp_root.parquet")
}

func TestRunCompactionContextCanceledBeforeWork(t *testing.T) {
	dir := t.TempDir()
	writeDaily(t, dir, "snapshot_20260510T080000_tmp_root.parquet",
		time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC), "h1")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := RunCompactionContext(ctx, dir, compactNow, nil)
	assert.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, res)
	assert.Empty(t, res.Groups, "no group may start after cancellation")
	assert.Equal(t, []string{"snapshot_20260510T080000_tmp_root.parquet"}, dirEntries(t, dir),
		"sources intact, no tmp left behind")
}

func TestRunCompactionContextCancelMidRun(t *testing.T) {
	dir := t.TempDir()
	ts := time.Date(2026, 5, 10, 8, 0, 0, 0, time.UTC).UnixMilli()
	for name, rows := range map[string][]Row{
		"snapshot_data.parquet": rawScanRows("/data", "f1", "h1", ts, 100),
		"snapshot_home.parquet": rawScanRows("/home", "f2", "h1", ts, 200),
	} {
		buf := writeRawRows(t, rows)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o600))
	}

	// Cancel when the second group announces itself: the first group completes,
	// the second must abort with its sources intact.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	groups := 0
	progress := func(format string, args ...interface{}) {
		if strings.HasPrefix(format, "compacting") {
			groups++
			if groups == 2 {
				cancel()
			}
		}
	}

	res, err := RunCompactionContext(ctx, dir, compactNow, progress)
	require.NoError(t, err)
	require.Len(t, res.Groups, 2)
	require.NoError(t, res.Groups[0].Err)
	assert.Equal(t, "/data", res.Groups[0].Group.ScanRoot)
	assert.ErrorIs(t, res.Groups[1].Err, context.Canceled)

	names := dirEntries(t, dir)
	assert.Contains(t, names, "monthly_2026-05_data.parquet", "first group compacted")
	assert.Contains(t, names, "snapshot_home.parquet", "aborted group's source intact")
	for _, n := range names {
		assert.NotContains(t, n, ".tmp")
	}
}
