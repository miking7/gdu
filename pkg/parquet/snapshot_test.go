package parquet

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotFileName(t *testing.T) {
	now := time.Date(2026, 6, 19, 5, 30, 15, 0, time.UTC)
	assert.Equal(t, "scan_20260619T053015Z.parquet", SnapshotFileName(now))
}

func TestSaveSnapshotRoundTrip(t *testing.T) {
	root := sampleTree()
	dir := t.TempDir()
	now := time.Date(2026, 6, 19, 5, 30, 0, 0, time.UTC)

	path, err := SaveSnapshot(root, dir, 0, now)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "scan_20260619T053000Z.parquet"), path)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(8))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	got, err := ReadTree(f, info.Size())
	require.NoError(t, err)
	assert.Equal(t, "root", got.GetName())
}

func TestSaveSnapshotCreatesDir(t *testing.T) {
	root := sampleTree()
	dir := filepath.Join(t.TempDir(), "nested", "scans")

	_, err := SaveSnapshot(root, dir, 10*mib, time.Unix(1700000000, 0).UTC())
	require.NoError(t, err)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1)
}
