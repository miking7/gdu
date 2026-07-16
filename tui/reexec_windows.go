//go:build windows

package tui

import "errors"

// reexecSudo is unreachable on Windows: the restart-elevated offer is gated
// behind a non-root effective-uid check (os.Geteuid returns -1 there, so
// sudoTipRelevant is false). The stub keeps the package building — syscall.Exec
// has no Windows implementation.
func reexecSudo(_, _ []string) error {
	return errors.New("restarting with sudo is not supported on Windows")
}
