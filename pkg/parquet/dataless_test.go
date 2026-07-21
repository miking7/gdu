package parquet

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	pq "github.com/parquet-go/parquet-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
)

// datalessTree returns root(/tmp/root){ local(1K), evicted(24M, '~'),
// placeholder('~', childless) } — one ordinary file plus the two shapes a cloud
// provider leaves behind: an evicted file that still has an apparent size, and a
// directory gdu refused to enumerate.
func datalessTree() *analyze.Dir {
	root := &analyze.Dir{File: &analyze.File{Name: "root"}, BasePath: "/tmp", ItemCount: 1}
	local := &analyze.File{Name: "local", Size: 1024, Usage: 1024, Parent: root}
	evicted := &analyze.File{Name: "evicted", Size: 24 * mib, Usage: 0, Flag: '~', Parent: root}
	placeholder := &analyze.Dir{
		File:      &analyze.File{Name: "placeholder", Flag: '~', Parent: root},
		ItemCount: 1,
	}

	root.AddFile(local)
	root.AddFile(evicted)
	root.AddFile(placeholder)
	root.UpdateStats(make(fs.HardLinkedItems))
	return root
}

// TestDatalessRoundTrip checks '~' survives a snapshot in both shapes: for files
// through the dataless column, for directories through the same column restored
// on read (UpdateStats cannot re-derive it — a placeholder has no children to
// derive anything from).
func TestDatalessRoundTrip(t *testing.T) {
	got := writeAndRead(t, datalessTree(), 0)

	kids := childItems(got)

	require.Contains(t, kids, "evicted")
	assert.Equal(t, '~', kids["evicted"].GetFlag())
	assert.Equal(t, int64(24*mib), kids["evicted"].GetSize(), "an evicted file keeps its apparent size")
	assert.Zero(t, kids["evicted"].GetUsage(), "an evicted file holds no blocks")

	require.Contains(t, kids, "placeholder")
	assert.Equal(t, '~', kids["placeholder"].GetFlag())
	assert.True(t, kids["placeholder"].IsDir())

	require.Contains(t, kids, "local")
	assert.Equal(t, ' ', kids["local"].GetFlag(), "an ordinary file is untouched")
}

// TestDatalessColumnIsWritten pins the on-disk representation the DuckDB-facing
// schema promises: one boolean column, true only for the placeholder rows.
func TestDatalessColumnIsWritten(t *testing.T) {
	root := datalessTree()
	meta := ScanMeta{ScanRoot: root.GetPath(), ScanTime: time.Unix(1700000000, 0).UTC()}

	var buf bytes.Buffer
	require.NoError(t, WriteTree(&buf, root, &meta))

	assert.Contains(t, pq.SchemaOf(Row{}).String(), "dataless")

	rows := readAllRows(t, &buf)
	byName := map[string]Row{}
	for _, r := range rows {
		byName[r.Name] = r
	}

	require.Contains(t, byName, "evicted")
	assert.True(t, byName["evicted"].Dataless)
	require.Contains(t, byName, "placeholder")
	assert.True(t, byName["placeholder"].Dataless)
	require.Contains(t, byName, "local")
	assert.False(t, byName["local"].Dataless)
	assert.False(t, byName["local"].ReadError, "the new flag must not disturb its neighbours")
}

// TestReadTreePreDatalessFile reads a snapshot written before the column
// existed. The absent column must decode as false, leaving every item unflagged
// rather than failing or inventing placeholders.
func TestReadTreePreDatalessFile(t *testing.T) {
	total := int64(333)
	rows := []legacyRow{
		{
			Path: "/old", Parent: "/", Name: "old", IsDir: true, Depth: 0,
			DirTotalDsize: &total, ScanRoot: "/old", ScanTs: 5000,
		},
		{
			Path: "/old/f", Parent: "/old", Name: "f", Depth: 1,
			Asize: 333, Dsize: 333, ScanRoot: "/old", ScanTs: 5000,
		},
	}
	var buf bytes.Buffer
	w := pq.NewGenericWriter[legacyRow](&buf)
	_, err := w.Write(rows)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	require.NotContains(t, pq.SchemaOf(legacyRow{}).String(), "dataless")

	got, err := ReadTree(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	assert.Equal(t, ' ', got.GetFlag())

	kids := childItems(got)
	require.Contains(t, kids, "f")
	assert.Equal(t, ' ', kids["f"].GetFlag())
}

// readAllRows decodes every row of a written snapshot.
func readAllRows(t *testing.T, buf *bytes.Buffer) []Row {
	t.Helper()

	pf, err := pq.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)

	reader := pq.NewGenericReader[Row](pf)
	defer reader.Close()

	out := make([]Row, pf.NumRows())
	n, err := reader.Read(out)
	if !errors.Is(err, io.EOF) {
		require.NoError(t, err)
	}
	return out[:n]
}
