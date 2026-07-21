//go:build darwin

package device

import "path/filepath"

// ScanRootAliasesMounts reports whether a scan rooted at path would reach the
// same bytes through more than one path, so it must exclude nested mount points
// even when the user did not ask to stay on one filesystem.
//
// macOS splits the startup disk into a read-only system volume mounted at / and
// a writable data volume mounted at /System/Volumes/Data. The system volume
// carries firmlinks — listed in /usr/share/firmlinks — that splice the data
// volume's /Users, /Applications, /Library and /private into the / hierarchy.
// A scan of / therefore walks all of those files twice: once through the
// firmlink and once under /System/Volumes/Data. Both spellings report the same
// device id AND the same inode, so neither a stay-on-one-device check nor the
// hard-link bookkeeping can tell the two visits apart; skipping the data
// volume's mount point is what stops the double count.
func ScanRootAliasesMounts(path string) bool {
	return filepath.Clean(path) == "/"
}
