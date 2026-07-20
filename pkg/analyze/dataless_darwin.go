package analyze

import (
	"os"
	"syscall"
)

// sfDataless is SF_DATALESS from macOS's <sys/stat.h>. The kernel sets it in
// st_flags on files and directories whose contents have been evicted to a cloud
// provider; userspace cannot set or clear it. The value is reused for unrelated
// flags on the other BSDs (NetBSD spends it on SF_LOG), so this test must stay
// in darwin-only code and never move into the shared unix helpers.
const sfDataless = 0x40000000

func statIsDataless(stat *syscall.Stat_t) bool {
	return stat.Flags&sfDataless != 0
}

// dirIsDatalessPath reports whether the directory at path is a cloud
// placeholder. A failed stat reports false so the scan proceeds normally and any
// real problem surfaces through the usual read-error path.
func dirIsDatalessPath(path string) bool {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return false
	}
	return statIsDataless(&stat)
}

// fileInfoIsDataless reports whether a file's contents have been evicted to the
// cloud. Such a file keeps its real apparent size while occupying (near) zero
// blocks, so it is still counted honestly; the flag is what tells the user the
// bytes are not actually here. It reads the stat the caller already holds, so it
// costs no extra syscall.
func fileInfoIsDataless(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && statIsDataless(stat)
}
