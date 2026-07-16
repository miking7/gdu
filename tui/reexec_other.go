//go:build !windows

package tui

import (
	"os/exec"
	"syscall"
)

// reexecSudo replaces the current process image with argv (which starts with
// "sudo"), so sudo inherits the already-restored controlling terminal and can
// prompt for a password inline. It must be called from inside app.Suspend, after
// tcell has handed the terminal back to cooked mode. On success it never returns
// (execve replaces the image); it returns an error only when sudo cannot be
// located on PATH or execve itself fails.
func reexecSudo(argv, envv []string) error {
	sudoPath, err := exec.LookPath(argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(sudoPath, argv, envv)
}
