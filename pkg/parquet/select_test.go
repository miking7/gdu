package parquet

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/fs"
)

// scanAt builds a SnapshotInfo at a fixed UTC instant so local-time formatting in
// tests is deterministic relative to the test machine's zone.
func scanAt(root string, t time.Time) SnapshotInfo {
	return SnapshotInfo{ScanRoot: root, Host: "h", ScanTs: t.UTC()}
}

func TestSelectSnapshotLatestEarliest(t *testing.T) {
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.Local)
	scans := []SnapshotInfo{
		scanAt("/a", base),
		scanAt("/a", base.Add(time.Hour)),
		scanAt("/a", base.Add(2*time.Hour)),
	}

	got, err := SelectSnapshot(scans, SnapshotSelector{}) // default = latest
	require.NoError(t, err)
	assert.True(t, got.ScanTs.Equal(base.Add(2*time.Hour)))

	got, err = SelectSnapshot(scans, SnapshotSelector{Spec: "latest"})
	require.NoError(t, err)
	assert.True(t, got.ScanTs.Equal(base.Add(2*time.Hour)))

	got, err = SelectSnapshot(scans, SnapshotSelector{Spec: "earliest"})
	require.NoError(t, err)
	assert.True(t, got.ScanTs.Equal(base))
}

func TestSelectSnapshotExactAndPrefix(t *testing.T) {
	scans := []SnapshotInfo{
		scanAt("/a", time.Date(2026, 6, 18, 9, 0, 0, 0, time.Local)),
		scanAt("/a", time.Date(2026, 6, 19, 15, 30, 5, 0, time.Local)),
		scanAt("/a", time.Date(2026, 7, 1, 2, 0, 0, 0, time.Local)),
	}

	// Exact local timestamp.
	got, err := SelectSnapshot(scans, SnapshotSelector{Spec: "2026-06-19T15:30:05"})
	require.NoError(t, err)
	assert.Equal(t, "/a", got.ScanRoot)
	assert.Equal(t, 30, got.ScanTs.Local().Minute())

	// Day prefix (unique).
	got, err = SelectSnapshot(scans, SnapshotSelector{Spec: "2026-06-18"})
	require.NoError(t, err)
	assert.Equal(t, 18, got.ScanTs.Local().Day())

	// Month prefix (unique here).
	got, err = SelectSnapshot(scans, SnapshotSelector{Spec: "2026-07"})
	require.NoError(t, err)
	assert.Equal(t, time.July, got.ScanTs.Local().Month())
}

func TestSelectSnapshotAmbiguousPrefix(t *testing.T) {
	day := time.Date(2026, 6, 19, 0, 0, 0, 0, time.Local)
	scans := []SnapshotInfo{
		scanAt("/a", day.Add(8*time.Hour)),
		scanAt("/a", day.Add(20*time.Hour)),
	}
	_, err := SelectSnapshot(scans, SnapshotSelector{Spec: "2026-06-19"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "2 snapshots")
}

func TestSelectSnapshotNoMatch(t *testing.T) {
	scans := []SnapshotInfo{scanAt("/a", time.Date(2026, 6, 19, 8, 0, 0, 0, time.Local))}
	_, err := SelectSnapshot(scans, SnapshotSelector{Spec: "2025-01-01"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshot matching")
}

func TestSelectSnapshotRootFilter(t *testing.T) {
	ts := time.Date(2026, 6, 19, 12, 0, 0, 0, time.Local)
	scans := []SnapshotInfo{
		scanAt("/home", ts),
		scanAt("/var", ts),
	}

	// A shared timestamp is ambiguous without a root...
	_, err := SelectSnapshot(scans, SnapshotSelector{Spec: "2026-06-19"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")

	// ...and unambiguous once the root narrows it.
	got, err := SelectSnapshot(scans, SnapshotSelector{Spec: "2026-06-19", Root: "/var"})
	require.NoError(t, err)
	assert.Equal(t, "/var", got.ScanRoot)

	// Unknown root reports the available snapshots.
	_, err = SelectSnapshot(scans, SnapshotSelector{Root: "/nope"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshot with root")
}

func TestSelectSnapshotEmpty(t *testing.T) {
	_, err := SelectSnapshot(nil, SnapshotSelector{})
	require.Error(t, err)
}

func TestReadTreeSelectedPicksSnapshot(t *testing.T) {
	rows := append(
		rawScanRows("/r", "oldfile", "h1", 1000, 111),
		rawScanRows("/r", "newfile", "h1", 5000, 222)...,
	)
	buf := writeRawRows(t, rows)

	// Explicitly select the older scan by its timestamp prefix.
	older := time.UnixMilli(1000).Local().Format(SnapshotTimeLayout)
	got, err := ReadTreeSelected(bytes.NewReader(buf.Bytes()), int64(buf.Len()),
		SnapshotSelector{Spec: older})
	require.NoError(t, err)
	got.UpdateStats(make(fs.HardLinkedItems))

	kids := childItems(got)
	assert.Contains(t, kids, "oldfile")
	assert.NotContains(t, kids, "newfile")
}

func TestReadTreeSelectedBadSelectorErrors(t *testing.T) {
	buf := writeRawRows(t, rawScanRows("/r", "f", "h1", 1000, 111))
	_, err := ReadTreeSelected(bytes.NewReader(buf.Bytes()), int64(buf.Len()),
		SnapshotSelector{Spec: "1999-01-01"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no snapshot matching")
}

// TestSelectSnapshotExactIdentity: an identity pin (host + precise scan_ts)
// resolves between snapshots whose local-time rendering is identical — two
// saves within the same second — where any textual spec would be ambiguous.
func TestSelectSnapshotExactIdentity(t *testing.T) {
	rows := append(
		rawScanRows("/r", "first", "h1", 1000, 111),
		rawScanRows("/r", "second", "h1", 1500, 222)..., // same formatted second
	)
	buf := writeRawRows(t, rows)

	scans, err := ListSnapshots(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, scans, 2)

	// The full formatted timestamp cannot tell them apart…
	spec := time.UnixMilli(1000).Local().Format(SnapshotTimeLayout)
	_, err = SelectSnapshot(scans, SnapshotSelector{Spec: spec})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")

	// …but the identity pin can.
	got, err := SelectSnapshot(scans, SnapshotSelector{
		Root: "/r", ExactTs: time.UnixMilli(1500).UTC(), ExactHost: "h1",
	})
	require.NoError(t, err)
	assert.True(t, got.ScanTs.Equal(time.UnixMilli(1500)))

	// A pin naming a snapshot the file doesn't hold errors clearly.
	_, err = SelectSnapshot(scans, SnapshotSelector{
		Root: "/r", ExactTs: time.UnixMilli(9999).UTC(), ExactHost: "h1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in this file")
}
