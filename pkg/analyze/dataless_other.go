//go:build !darwin

package analyze

import "os"

// dirIsDatalessPath always reports false away from macOS: cloud placeholders are
// marked by a macOS-specific kernel attribute with no portable equivalent.
func dirIsDatalessPath(string) bool { return false }

// fileInfoIsDataless always reports false away from macOS, for the same reason.
func fileInfoIsDataless(os.FileInfo) bool { return false }
