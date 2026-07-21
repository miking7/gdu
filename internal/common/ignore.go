// Package common contains commong logic and interfaces used across Gdu
// nolint: revive //Why: this is common package
package common

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	log "github.com/sirupsen/logrus"
)

// CreateIgnorePattern creates one pattern from all path patterns
func CreateIgnorePattern(paths []string) (compiled *regexp.Regexp, err error) {
	for i, path := range paths {
		if _, err = regexp.Compile(path); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(path) {
			absPath, err := filepath.Abs(path)
			if err == nil {
				paths = append(paths, absPath)
			}
		} else {
			relPath, err := filepath.Rel("/", path)
			if err == nil {
				paths = append(paths, relPath)
			}
		}
		paths[i] = "(" + path + ")"
	}

	ignore := `^` + strings.Join(paths, "|") + `$`
	return regexp.Compile(ignore)
}

// pathVariants builds the lookup set for exact-path matching: each path plus
// its absolute/root-relative counterpart, so a path configured either way
// matches the paths the analyzer actually walks.
func pathVariants(paths []string) map[string]struct{} {
	set := make(map[string]struct{}, len(paths)*2)
	for _, path := range paths {
		set[path] = struct{}{}
		if !filepath.IsAbs(path) {
			if absPath, err := filepath.Abs(path); err == nil {
				set[absPath] = struct{}{}
			}
		} else {
			if relPath, err := filepath.Rel("/", path); err == nil {
				set[relPath] = struct{}{}
			}
		}
	}
	return set
}

// SetIgnoreDirPaths sets paths to ignore
func (ui *UI) SetIgnoreDirPaths(paths []string) {
	log.Printf("Ignoring dirs %s", strings.Join(paths, ", "))
	ui.IgnoreDirPaths = pathVariants(paths)
}

// SetNestedMountPaths records the mount points nested under the root about to be
// scanned — the directories a scan must not descend into to stay on one
// filesystem, and (on macOS) to avoid counting the data volume a second time
// through a firmlink. It replaces the whole set; nil or empty clears it, so
// every scan starts from its own mount boundary and the previous scan's mounts
// never leak into it.
//
// This is separate storage from SetIgnoreDirPaths/SetIgnoreDirPatterns, which
// hold what the *user* asked to ignore and must survive any number of scans
// untouched. Matching exact paths rather than a compiled pattern also means a
// mount point containing regex metacharacters — a volume named "Backups (old)"
// — is matched literally.
func (ui *UI) SetNestedMountPaths(paths []string) {
	if len(paths) == 0 {
		ui.nestedMountPaths = nil
		return
	}
	log.Printf("Ignoring mount points: %s", strings.Join(paths, ", "))
	ui.nestedMountPaths = pathVariants(paths)
}

// SetIgnoreDirPatterns sets regular patterns of dirs to ignore
func (ui *UI) SetIgnoreDirPatterns(paths []string) error {
	var err error
	log.Printf("Ignoring dir patterns %s", strings.Join(paths, ", "))
	ui.IgnoreDirPathPatterns, err = CreateIgnorePattern(paths)
	return err
}

// SetIgnoreFromFile sets regular patterns of dirs to ignore
func (ui *UI) SetIgnoreFromFile(ignoreFile string) error {
	var err error
	var paths []string
	log.Printf("Reading ignoring dir patterns from file '%s'", ignoreFile)

	file, err := os.Open(ignoreFile)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		paths = append(paths, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	ui.IgnoreDirPathPatterns, err = CreateIgnorePattern(paths)
	return err
}

// SetIgnoreTypes sets file types to ignore
func (ui *UI) SetIgnoreTypes(types []string) {
	log.Printf("Ignoring file types: %s", strings.Join(types, ", "))
	ui.IgnoreTypes = types
}

// SetIncludeTypes sets file types to include (whitelist)
func (ui *UI) SetIncludeTypes(types []string) {
	log.Printf("Including only file types: %s", strings.Join(types, ", "))
	ui.IncludeTypes = types
}

// SetIgnoreHidden sets flags if hidden dirs should be ignored
func (ui *UI) SetIgnoreHidden(value bool) {
	log.Printf("Ignoring hidden dirs")
	ui.IgnoreHidden = value
}

// ShouldDirBeIgnored returns true if given path should be ignored
func (ui *UI) ShouldDirBeIgnored(name, path string) bool {
	_, shouldIgnore := ui.IgnoreDirPaths[path]
	if shouldIgnore {
		log.Printf("Directory %s ignored", path)
	}
	return shouldIgnore
}

// ShouldDirBeIgnoredAsNestedMount returns true if given path is a mount point
// nested under the root being scanned (see SetNestedMountPaths).
func (ui *UI) ShouldDirBeIgnoredAsNestedMount(name, path string) bool {
	_, shouldIgnore := ui.nestedMountPaths[path]
	if shouldIgnore {
		log.Printf("Mount point %s ignored", path)
	}
	return shouldIgnore
}

// ShouldDirBeIgnoredUsingPattern returns true if given path should be ignored
func (ui *UI) ShouldDirBeIgnoredUsingPattern(name, path string) bool {
	shouldIgnore := ui.IgnoreDirPathPatterns.MatchString(path)
	if shouldIgnore {
		log.Printf("Directory %s ignored", path)
	}
	return shouldIgnore
}

// IsHiddenDir returns if the dir name begins with dot
func (ui *UI) IsHiddenDir(name, path string) bool {
	shouldIgnore := name[0] == '.'
	if shouldIgnore {
		log.Printf("Directory %s ignored", path)
	}
	return shouldIgnore
}

// ShouldFileBeIgnoredByType returns true if file should be ignored based on its extension
func (ui *UI) ShouldFileBeIgnoredByType(name string) bool {
	if len(ui.IgnoreTypes) == 0 {
		return false
	}

	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false // No extension, don't ignore
	}

	// Remove leading dot from extension
	ext = strings.TrimPrefix(ext, ".")

	for _, ignoreType := range ui.IgnoreTypes {
		// Remove leading dot from ignoreType
		cleanIgnoreType := strings.TrimPrefix(strings.ToLower(ignoreType), ".")
		if cleanIgnoreType == ext {
			log.Printf("File %s ignored by type", name)
			return true
		}
	}
	return false
}

// ShouldFileBeIncludedByType returns true if file should be included based on its extension
func (ui *UI) ShouldFileBeIncludedByType(name string) bool {
	if len(ui.IncludeTypes) == 0 {
		return true // No include filter, include all
	}

	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return false // No extension, don't include if we have include filter
	}

	// Remove leading dot from extension
	ext = strings.TrimPrefix(ext, ".")

	for _, includeType := range ui.IncludeTypes {
		// Remove leading dot from includeType
		cleanIncludeType := strings.TrimPrefix(strings.ToLower(includeType), ".")
		if cleanIncludeType == ext {
			return true
		}
	}

	log.Printf("File %s excluded by type filter", name)
	return false
}

// CreateIgnoreFunc returns function for detecting if dir should be ignored.
// The active checks are OR'd together, composed rather than enumerated so that
// adding one costs a single branch instead of doubling a combination table.
func (ui *UI) CreateIgnoreFunc() ShouldDirBeIgnored {
	var checks []ShouldDirBeIgnored
	if len(ui.IgnoreDirPaths) > 0 {
		checks = append(checks, ui.ShouldDirBeIgnored)
	}
	if len(ui.nestedMountPaths) > 0 {
		checks = append(checks, ui.ShouldDirBeIgnoredAsNestedMount)
	}
	if ui.IgnoreDirPathPatterns != nil {
		checks = append(checks, ui.ShouldDirBeIgnoredUsingPattern)
	}
	if ui.IgnoreHidden {
		checks = append(checks, ui.IsHiddenDir)
	}

	switch len(checks) {
	case 0:
		return func(name, path string) bool { return false }
	case 1:
		return checks[0]
	default:
		return func(name, path string) bool {
			for _, check := range checks {
				if check(name, path) {
					return true
				}
			}
			return false
		}
	}
}

// CreateFileTypeFilter returns function for detecting if file should be ignored based on type
func (ui *UI) CreateFileTypeFilter() ShouldFileBeIgnored {
	// If we have include types, use whitelist mode
	if len(ui.IncludeTypes) > 0 {
		return func(name string) bool {
			return !ui.ShouldFileBeIncludedByType(name)
		}
	}

	// If we have ignore types, use blacklist mode
	if len(ui.IgnoreTypes) > 0 {
		return func(name string) bool {
			return ui.ShouldFileBeIgnoredByType(name)
		}
	}

	// No type filtering - return nil to indicate no filtering is needed
	return nil
}

// IsFilteringFiles returns true if we have any file type filters set
func (ui *UI) IsFilteringFiles() bool {
	return len(ui.IgnoreTypes) > 0 || len(ui.IncludeTypes) > 0 || ui.FilteringFiles
}
