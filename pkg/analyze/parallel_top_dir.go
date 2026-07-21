package analyze

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/fs"
	log "github.com/sirupsen/logrus"
)

var pathSep = string(os.PathSeparator)

// emptyDirSize is the apparent size reported for a directory with nothing to
// count — one that is genuinely empty, or a cloud placeholder gdu refuses to
// enumerate. Neither holds data blocks, so the disk usage stays zero.
const emptyDirSize = 512

var _ common.Analyzer = (*TopDirAnalyzer)(nil)

// TopDirAnalyzer implements Analyzer
// It doesn't return the full directory structure, only the top level directory,
// thus is suitable only for non-interactive mode.
// It tries to use only stack for storing state and results.
type TopDirAnalyzer struct {
	BaseAnalyzer
	linkedItems sync.Map
}

// CreateTopDirAnalyzer returns Analyzer
func CreateTopDirAnalyzer() *TopDirAnalyzer {
	a := &TopDirAnalyzer{
		BaseAnalyzer: BaseAnalyzer{
			ignoreFileType:      func(string) bool { return false },
			matchesTimeFilterFn: func(time.Time) bool { return true },
		},
	}
	a.Init()
	return a
}

// AnalyzeDir analyzes given path
func (a *TopDirAnalyzer) AnalyzeDir(
	path string, ignore common.ShouldDirBeIgnored, fileTypeFilter common.ShouldFileBeIgnored,
) fs.Item {
	a.ignoreDir = ignore
	if fileTypeFilter != nil {
		a.ignoreFileType = fileTypeFilter
	}

	var subDirChan = make(chan struct{})

	go a.UpdateProgress()

	// Checked before ReadDir: listing a cloud placeholder is what drags its
	// whole subtree down from the provider, so a placeholder root stays a leaf.
	var (
		files    []os.DirEntry
		err      error
		dataless = dirIsDataless(path)
	)
	if !dataless {
		files, err = os.ReadDir(path)
		if err != nil {
			log.Print(err.Error())
		}
	}

	dir := SimpleDir{
		SimpleFile: SimpleFile{
			Name:      filepath.Base(path),
			Flag:      getDirFlag(err, len(files)),
			IsDir:     true,
			ItemCount: 1,
		},
		Files: make([]SimpleFile, 0, len(files)),
	}
	if dataless {
		dir.Flag = datalessFlag
	}

	var (
		topDirs []*TopDir
		scanned int
	)

	for _, f := range files {
		name := f.Name()
		entryPath := filepath.Join(path, name)
		if f.IsDir() {
			if a.ignoreDir(name, entryPath) {
				continue
			}
			topDir := &TopDir{
				Name: name,
				Flag: ' ',
			}
			topDirs = append(topDirs, topDir)

			// A cloud placeholder is reported like an empty directory and never
			// descended into, so no scan goroutine is started for it.
			if dirIsDataless(entryPath) {
				topDir.Flag = datalessFlag
				topDir.AddUsage(emptyDirSize, 0, 0)
				continue
			}

			scanned++
			go func(entryPath string) {
				a.processSubDir(entryPath, topDir)
				subDirChan <- struct{}{}
			}(entryPath)
		} else {
			var info os.FileInfo
			// Apply file type filter if set
			if a.ignoreFileType(name) {
				continue // Skip this file
			}

			info, err = f.Info()
			if err != nil {
				log.Print(err.Error())
				dir.Flag = '!'
				continue
			}

			// Apply time filter if set
			if !a.matchesTimeFilterFn(info.ModTime()) {
				continue // Skip this file
			}

			if a.followSymlinks && info.Mode()&os.ModeSymlink != 0 {
				infoF, err := followSymlink(entryPath, a.gitAnnexedSize)
				if err != nil {
					log.Print(err.Error())
					dir.Flag = '!'
					continue
				}
				if infoF != nil {
					info = infoF
				}
			}

			file := SimpleFile{
				Name: name,
				Flag: getFlag(info),
				Size: info.Size(),
			}

			usage, mli := getPlatformSpecificUsageAndMli(info)
			file.Usage = usage

			// Hard-link accounting wins the flag column: it changes what the
			// number means, while "evicted to the cloud" only explains why the
			// disk usage is nil.
			switch {
			case mli > 0:
				file.Flag = 'H'
			case fileIsDataless(info):
				file.Flag = datalessFlag
			}

			dir.Files = append(dir.Files, file)
		}
	}

	for i := 0; i < scanned; i++ {
		<-subDirChan
	}

	a.wait.Wait()

	for _, topDir := range topDirs {
		size, usage, itemCount := topDir.GetUsage()
		dir.Files = append(dir.Files, SimpleFile{
			Name:      topDir.Name,
			Flag:      topDir.Flag,
			Size:      size,
			Usage:     usage,
			ItemCount: itemCount,
			IsDir:     true,
		})
	}

	dir.BasePath = filepath.Dir(path)

	a.progressDoneChan <- struct{}{}
	a.doneChan.Broadcast()

	return &dir
}

func (a *TopDirAnalyzer) processSubDir(path string, topDir *TopDir) {
	var (
		err        error
		totalSize  int64
		totalUsage int64
		totalCount int64
		info       os.FileInfo
	)

	// Checked before ReadDir: listing a cloud placeholder is what drags its
	// whole subtree down from the provider. It contributes what an empty
	// directory does; the enclosing top-level dir keeps its own flag, because a
	// placeholder somewhere below it does not make it one.
	if dirIsDataless(path) {
		topDir.AddUsage(emptyDirSize, 0, 0)
		return
	}

	files, err := os.ReadDir(path)
	if err != nil {
		log.Print(err.Error())
		topDir.SetFlag('.')
	}

	for _, f := range files {
		name := f.Name()
		entryPath := path + pathSep + name
		if f.IsDir() {
			if a.ignoreDir(name, entryPath) {
				continue
			}

			totalCount++

			select {
			case concurrencyLimit <- struct{}{}:
				a.wait.Add(1)
				go func(entryPath string) {
					a.processSubDir(entryPath, topDir)
					<-concurrencyLimit
					a.wait.Done()
				}(entryPath)
			default:
				a.processSubDir(entryPath, topDir)
			}
		} else {
			// Apply file type filter if set
			if a.ignoreFileType(name) {
				continue // Skip this file
			}

			info, err = f.Info()
			if err != nil {
				log.Print(err.Error())
				topDir.SetFlag('.')
				continue
			}

			// Apply time filter if set
			if !a.matchesTimeFilterFn(info.ModTime()) {
				continue // Skip this file
			}

			totalCount++

			if a.followSymlinks && info.Mode()&os.ModeSymlink != 0 {
				infoF, err := followSymlink(entryPath, a.gitAnnexedSize)
				if err != nil {
					log.Print(err.Error())
					topDir.SetFlag('.')
					continue
				}
				if infoF != nil {
					info = infoF
				}
			}

			usage, mli := getPlatformSpecificUsageAndMli(info)

			if mli > 0 {
				if _, loaded := a.linkedItems.LoadOrStore(mli, struct{}{}); loaded {
					continue
				}
			}

			totalUsage += usage
			totalSize += info.Size()
		}
	}

	if len(files) == 0 {
		totalSize = emptyDirSize
		totalUsage = 0
	}

	a.progressItemCount.Add(totalCount)
	a.progressTotalUsage.Add(totalUsage)

	topDir.AddUsage(totalSize, totalUsage, totalCount)
}
