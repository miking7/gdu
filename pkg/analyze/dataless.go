package analyze

import (
	"path/filepath"

	"github.com/dundee/gdu/v5/pkg/fs"
)

// datalessFlag marks a cloud placeholder: a file or directory whose contents
// live only in a provider's cloud (iCloud Drive, Dropbox, Google Drive,
// OneDrive, …) and are represented locally by a stub. Listing a placeholder
// directory makes the kernel fault its whole subtree back in over the network,
// so gdu reports the placeholder and stops there — a disk usage tool must never
// materialise a cloud to measure it. Evicted files cost nothing on disk and are
// still counted; for them the flag is purely informational.
const datalessFlag = '~'

// dirIsDataless reports whether the directory at path is a cloud placeholder,
// and fileIsDataless the same for an already-stat'ed file. Both are variables so
// tests can stand in for them: the underlying attribute is set by the kernel and
// cannot be written from userspace, so no fixture can ever be genuinely
// dataless.
var (
	dirIsDataless  = dirIsDatalessPath
	fileIsDataless = fileInfoIsDataless
)

// datalessDir builds the leaf that stands in for a cloud placeholder directory:
// flagged, counted as a single item, never enumerated. It carries no children,
// so UpdateStats settles it at an empty directory's size, which is honest — a
// placeholder occupies no data blocks.
func datalessDir(path string) *Dir {
	dir := &Dir{
		File: &File{
			Name: filepath.Base(path),
			Flag: datalessFlag,
		},
		ItemCount: 1,
		Files:     make(fs.Files, 0),
	}
	setDirPlatformSpecificAttrs(dir, path)
	return dir
}
