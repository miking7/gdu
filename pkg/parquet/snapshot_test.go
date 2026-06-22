package parquet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSnapshotFileName(t *testing.T) {
	// Local time (the filename is human-facing); the scan_ts column stays UTC.
	now := time.Date(2026, 6, 19, 5, 30, 15, 0, time.Local)
	assert.Equal(t, "scan_20260619T053015_users_michael.parquet",
		SnapshotFileName(now, "/Users/michael"))
}

func TestRootSlug(t *testing.T) {
	cases := []struct {
		root, want string
	}{
		{"/", "root"},
		{"", "root"},
		{"///", "root"},
		{"/Volumes/SD", "volumes_sd"},
		{"/Users/michael", "users_michael"},
		{"/Users/michael/", "users_michael"},        // trailing separator trimmed
		{"/Volumes/My Disk!", "volumes_my_disk"},    // spaces+punctuation collapse
		{"/Volumes/SD-Card_2", "volumes_sd_card_2"}, // digits and dashes
		{`C:\Users\me`, "c_users_me"},               // Windows drive + backslashes
		{"/srv/data//logs", "srv_data_logs"},        // doubled separator collapses
		{"/Médiá", "m_di"},                          // non-ASCII degrades to "_", trimmed
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, rootSlug(c.root), "rootSlug(%q)", c.root)
	}
}

func TestRootSlugCapsLength(t *testing.T) {
	root := "/" + strings.Repeat("abcdefghij/", 20) // 220 chars of path
	slug := rootSlug(root)
	assert.LessOrEqual(t, len(slug), rootSlugMaxLen)
	assert.NotEmpty(t, slug)
}

func TestSaveSnapshotRoundTrip(t *testing.T) {
	root := sampleTree()
	dir := t.TempDir()
	now := time.Date(2026, 6, 19, 5, 30, 0, 0, time.Local)

	path, err := SaveSnapshot(root, dir, 0, now)
	require.NoError(t, err)
	// Root is /tmp/root, so the filename carries the "tmp_root" slug.
	assert.Equal(t, filepath.Join(dir, "scan_20260619T053000_tmp_root.parquet"), path)

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

func TestSaveSnapshotCollision(t *testing.T) {
	root := sampleTree()
	dir := t.TempDir()
	now := time.Date(2026, 6, 19, 5, 30, 0, 0, time.Local)

	first, err := SaveSnapshot(root, dir, 0, now)
	require.NoError(t, err)
	second, err := SaveSnapshot(root, dir, 0, now) // same second must not overwrite
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	assert.Equal(t, filepath.Join(dir, "scan_20260619T053000_tmp_root.parquet"), first)
	assert.Equal(t, filepath.Join(dir, "scan_20260619T053000_tmp_root_1.parquet"), second)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
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
