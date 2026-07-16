package testanalyze

import (
	"os"
	"path/filepath"
	"time"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
	"github.com/dundee/gdu/v5/pkg/parquet"
)

// WriteSnapshot writes a tiny gdu Parquet snapshot of root — holding one
// 100-byte file named fileName — scanned at ts, into dir/name. It is the
// shared fixture for the archive-resolution tests: distinct fileName values
// let a test tell which snapshot a command actually loaded. The snapshot is
// stamped with the fixed host "h1" (foreign to any real test machine).
func WriteSnapshot(dir, name, root, fileName string, ts time.Time) error {
	return WriteSnapshotAs(dir, name, root, fileName, "h1", ts)
}

// WriteSnapshotAs is WriteSnapshot with an explicit host, for tests exercising
// the foreign-vs-local host display rule — pass the local hostname to
// simulate a same-machine snapshot.
func WriteSnapshotAs(dir, name, root, fileName, host string, ts time.Time) error {
	tree := &analyze.Dir{
		File:      &analyze.File{Name: filepath.Base(root)},
		BasePath:  filepath.Dir(root),
		ItemCount: 1,
	}
	tree.AddFile(&analyze.File{Name: fileName, Size: 100, Usage: 100, Parent: tree})
	tree.UpdateStats(make(fs.HardLinkedItems))

	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	meta := parquet.ScanMeta{ScanRoot: root, ScanTime: ts.UTC(), Host: host, Username: "u"}
	if err := parquet.WriteTree(f, tree, &meta); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteClosedMonthSnapshot writes a tiny gdu Parquet snapshot dated
// 2000-01-15 — a month closed forever — into dir, named per the snapshot
// convention (snapshot_<ts>_<root>.parquet) so the auto-compaction predicate
// treats it as a loose daily with work to do. It is the shared fixture for
// the auto-compaction tests across the stdout, tui and app packages. Returns
// an error rather than taking *testing.T so this shipped internal package
// needn't import "testing".
func WriteClosedMonthSnapshot(dir string) error {
	root := &analyze.Dir{File: &analyze.File{Name: "data"}, BasePath: "/", ItemCount: 1}
	root.AddFile(&analyze.File{Name: "f", Size: 100, Usage: 100, Parent: root})
	root.UpdateStats(make(fs.HardLinkedItems))

	f, err := os.Create(filepath.Join(dir, "snapshot_20000115T120000_data.parquet"))
	if err != nil {
		return err
	}
	meta := parquet.ScanMeta{
		ScanRoot: "/data",
		ScanTime: time.Date(2000, 1, 15, 12, 0, 0, 0, time.UTC),
		Host:     "h1", Username: "u",
	}
	if err := parquet.WriteTree(f, root, &meta); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
