package tui

import (
	"bytes"
	"compress/gzip"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"

	"github.com/dundee/gdu/v5/internal/testapp"
	"github.com/dundee/gdu/v5/internal/testdir"
	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
)

// TestShowFileRefusesCloudPlaceholder covers the 'v' guard: a '~' file's bytes
// live in a cloud provider, and opening it would make the kernel download them —
// which is not what a key that only reads should do.
func TestShowFileRefusesCloudPlaceholder(t *testing.T) {
	fin := testdir.CreateTestDir()
	defer fin()
	simScreen := testapp.CreateSimScreen()
	defer simScreen.Fini()

	app := testapp.CreateMockedApp(true)
	ui := CreateUI(app, simScreen, &bytes.Buffer{}, false, true, false, false)
	ui.done = make(chan struct{})
	require.NoError(t, ui.AnalyzePath("test_dir", nil))

	<-ui.done // wait for analyzer

	for _, f := range ui.app.(*testapp.MockedApp).GetUpdateDraws() {
		f()
	}

	ui.table.Select(0, 0)
	ui.keyPressed(tcell.NewEventKey(tcell.KeyRight, 'l', 0))
	ui.table.Select(2, 0)

	selected, ok := ui.table.GetCell(2, 0).GetReference().(fs.Item)
	require.True(t, ok)
	require.False(t, selected.IsDir())
	selected.(*analyze.File).Flag = '~'

	assert.Nil(t, ui.showFile())
	assert.False(t, ui.pages.HasPage("file"), "a cloud placeholder must not be opened")
	assert.Contains(t, ui.headerNotice, "Cloud placeholder")
}

func TestGetScannerForEmptyString(t *testing.T) {
	r := bytes.NewReader([]byte{})
	_, err := getScanner(r)
	assert.ErrorContains(t, err, "EOF")
}

func TestGetScannerForPlainString(t *testing.T) {
	r := bytes.NewReader([]byte("hello"))
	s, err := getScanner(r)
	assert.Nil(t, err)

	assert.Equal(t, true, s.Scan())
	assert.Equal(t, "hello", s.Text())
	assert.Equal(t, nil, s.Err())
}

func TestGetScannerForGzipped(t *testing.T) {
	b := bytes.NewBuffer([]byte{})
	w := gzip.NewWriter(b)

	_, err := w.Write([]byte("hello world"))
	assert.Nil(t, err)

	err = w.Close()
	assert.Nil(t, err)

	r := bytes.NewReader(b.Bytes())
	s, err := getScanner(r)
	assert.Nil(t, err)

	assert.Equal(t, true, s.Scan())
	assert.Equal(t, "hello world", s.Text())
	assert.Equal(t, nil, s.Err())
}

func TestGetScannerForBzipped(t *testing.T) {
	r := bytes.NewReader([]byte{
		// bzip2 header
		0x42, 0x5A, 0x68, 0x39,
		// bzip2 compressed data: "hello"
		0x31, 0x41, 0x59, 0x26,
		0x53, 0x59, 0xC1, 0xC0,
		0x80, 0xE2, 0x00, 0x00,
		0x01, 0x41, 0x00, 0x00,
		0x10, 0x02, 0x44, 0xA0,
		0x00, 0x30, 0xCD, 0x00,
		0xC3, 0x46, 0x29, 0x97,
		0x17, 0x72, 0x45, 0x38,
		0x50, 0x90, 0xC1, 0xC0,
		0x80, 0xE2,
	})
	s, err := getScanner(r)
	assert.Nil(t, err)

	assert.Equal(t, true, s.Scan())
	assert.Equal(t, "hello", s.Text())
	assert.Equal(t, nil, s.Err())
}

func TestGetScannerForXzipped(t *testing.T) {
	b := bytes.NewBuffer([]byte{})
	w, err := xz.NewWriter(b)
	assert.Nil(t, err)

	_, err = w.Write([]byte("hello world"))
	assert.Nil(t, err)

	err = w.Close()
	assert.Nil(t, err)

	r := bytes.NewReader(b.Bytes())
	s, err := getScanner(r)
	assert.Nil(t, err)

	assert.Equal(t, true, s.Scan())
	assert.Equal(t, "hello world", s.Text())
	assert.Equal(t, nil, s.Err())
}
