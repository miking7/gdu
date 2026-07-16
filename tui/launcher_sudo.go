package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Restart-elevated: a real "restart gdu with sudo" from the launcher. The
// passive sudo tip advertises the manual R key everywhere (non-root Unix); a
// forced-but-cancelable interstitial fires only when the chosen scan root is
// the whole root volume (/), where a non-root scan almost always misses
// folders. Either way the terminal is handed to sudo via app.Suspend (the same
// mechanism shell-spawn and Ctrl-Z use) and the process is replaced with
// `sudo -- <self> <args>` — so ownership hand-back, config resolution, and the
// launcher itself all come up fresh under root.

// sudoModalPage is the page shared by the two restart-with-sudo modals (manual R
// confirm and the /-scan interstitial); only one is ever up at a time.
const sudoModalPage = "sudo-modal"

// SetConfigFilePath records the resolved config-file path so a restart-elevated
// can forward it to the root instance — sudo's env reset would otherwise resolve
// root's config, not the user's. Empty means none was loaded.
func (ui *UI) SetConfigFilePath(path string) {
	ui.configFilePath = path
}

// confirmRestartElevated shows the manual (R-key) restart-with-sudo confirmation.
// The user asked for it, so Restart is the default button; Cancel returns to the
// launcher.
func (ui *UI) confirmRestartElevated() {
	if ui.pages.HasPage(sudoModalPage) {
		return
	}
	modal := tview.NewModal().
		SetText("Restart gdu with sudo? You'll be prompted for your password.").
		AddButtons([]string{"Restart with sudo", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			ui.pages.RemovePage(sudoModalPage)
			if buttonIndex == 0 {
				ui.restartElevated()
				return
			}
			if ui.launcher != nil {
				ui.app.SetFocus(ui.launcher.table)
			}
		})
	ui.styleSudoModal(modal)
	ui.pages.AddPage(sudoModalPage, modal, true, true)
}

// confirmScanElevated is the forced interstitial shown when the chosen scan root
// is the whole root volume (/). Scan anyway is the *default* button — with
// image-replacement, an accidental Restart the user then Ctrl-C's at the password
// prompt would quit gdu outright — and proceeds with the unprivileged scan;
// Restart hands off to sudo.
func (ui *UI) confirmScanElevated(r *launcherRow) {
	if ui.pages.HasPage(sudoModalPage) {
		return
	}
	modal := tview.NewModal().
		SetText("Scanning / without sudo will miss folders you don't have permission to read" +
			fdaCaveat() + ".\n\nRestart with sudo, or scan anyway?").
		AddButtons([]string{"Scan anyway", "Restart with sudo"}).
		SetDoneFunc(func(buttonIndex int, _ string) {
			ui.pages.RemovePage(sudoModalPage)
			if buttonIndex == 1 {
				ui.restartElevated()
				return
			}
			ui.launcherRunScan(r)
		})
	ui.styleSudoModal(modal)
	ui.pages.AddPage(sudoModalPage, modal, true, true)
}

// styleSudoModal applies the launcher's usual modal colors (matching the quit and
// baseline modals): default border, gray/black background by color mode.
func (ui *UI) styleSudoModal(modal *tview.Modal) {
	if !ui.UseColors {
		modal.SetBackgroundColor(tcell.ColorGray)
	} else {
		modal.SetBackgroundColor(tcell.ColorBlack)
	}
	modal.SetBorderColor(tcell.ColorDefault)
}

// restartElevated hands the terminal to sudo via app.Suspend (which restores
// cooked mode first) and replaces the process with `sudo -- <self> <original
// args>`, forwarding the config file when needed. On success execve never
// returns; a failure (no sudo, not a sudoer, unresolved executable) resumes the
// TUI with an error.
func (ui *UI) restartElevated() {
	self, err := os.Executable()
	if err != nil {
		ui.showErr("Cannot restart with sudo", fmt.Errorf("locating the gdu executable: %w", err))
		return
	}
	argv := buildSudoArgv(self, os.Args[1:], ui.configFilePath)
	var reexecErr error
	ui.app.Suspend(func() {
		// Reached only if the handoff fails — execve replaces the image on success.
		reexecErr = ui.reexec(argv, os.Environ())
	})
	if reexecErr != nil {
		ui.showErr("Could not restart with sudo", reexecErr)
	}
}

// buildSudoArgv assembles the argv for a restart-elevated: `sudo -- <self>`
// followed by the original arguments verbatim, then the resolved --config-file
// when one was loaded and not already named (sudo's env reset can otherwise hide
// the user's config from the root instance). It goes through no shell, so values
// need no quoting.
func buildSudoArgv(self string, args []string, cfgFile string) []string {
	argv := make([]string, 0, len(args)+5)
	argv = append(argv, "sudo", "--", self)
	argv = append(argv, args...)
	if cfgFile != "" && !argsNameConfigFile(args) {
		argv = append(argv, "--config-file", cfgFile)
	}
	return argv
}

// argsNameConfigFile reports whether --config-file is already present in args (as
// a separate token or the --config-file=… form), so a forwarded copy is not added
// twice.
func argsNameConfigFile(args []string) bool {
	for _, a := range args {
		if a == "--config-file" || strings.HasPrefix(a, "--config-file=") {
			return true
		}
	}
	return false
}
