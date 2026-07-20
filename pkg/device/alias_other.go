//go:build !darwin

package device

// ScanRootAliasesMounts reports whether a scan rooted at path would reach the
// same bytes through more than one path, so it must exclude nested mount points
// even when the user did not ask to stay on one filesystem.
//
// It is always false here. Only macOS splices one volume into another's
// hierarchy by default (firmlinks from the data volume into /); elsewhere a
// scan reaches each file once and crossing a mount point is the user's call.
func ScanRootAliasesMounts(path string) bool {
	return false
}
