package common

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	log "github.com/sirupsen/logrus"
)

// ApplyOwnerOverride resolves name to a user account and exports
// SUDO_USER/SUDO_UID/SUDO_GID into the process environment, so the rest of gdu
// treats written output (snapshots, -o exports, TUI exports) as belonging to that
// user — the same path a real sudo invocation takes. It backs the --owner flag,
// letting a scheduler hand output to a chosen user without setting environment
// variables by hand. Returns an error if the user cannot be resolved.
//
// Chowning still requires gdu to be running as root; for a non-root user it is a
// no-op for any account other than their own.
func ApplyOwnerOverride(name string) error {
	u, err := user.Lookup(name)
	if err != nil {
		return fmt.Errorf("looking up owner %q: %w", name, err)
	}
	_ = os.Setenv("SUDO_USER", u.Username)
	_ = os.Setenv("SUDO_UID", u.Uid)
	_ = os.Setenv("SUDO_GID", u.Gid)
	return nil
}

// ScanIdentity captures who ran a scan and on which host, for snapshot metadata.
type ScanIdentity struct {
	Host     string // os.Hostname(), best-effort ("" if unavailable)
	Username string // effective user the scan ran as (e.g. "root" under sudo)
	SudoUser string // invoking user when run via sudo; "" otherwise
}

// CollectScanIdentity gathers the host and user identity of the current scan.
func CollectScanIdentity() ScanIdentity {
	id := ScanIdentity{Username: currentUsername()}
	if h, err := os.Hostname(); err == nil {
		id.Host = h
	}
	if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
		id.SudoUser = su
	}
	return id
}

// currentUsername returns the effective user name, falling back to $USER or the
// numeric uid. os/user works under CGO_ENABLED=0 on macOS (libSystem) and on Linux
// for local /etc/passwd accounts, but can still fail for LDAP/SSSD-only accounts,
// so this never depends on it succeeding.
func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if env := os.Getenv("USER"); env != "" {
		return env
	}
	return strconv.Itoa(os.Geteuid())
}

// RealUser returns the user who invoked sudo: their uid, gid and home directory.
// ok is false when not running under sudo (su -, a direct root login, or a
// scheduler running as root) — i.e. there is no invoking user to defer to.
//
// Numeric SUDO_UID/SUDO_GID are preferred for the ids (no name lookup needed, so it
// still works for accounts os/user can't resolve, e.g. LDAP/SSSD-only on Linux);
// user.Lookup is used only to resolve the home dir.
func RealUser() (uid, gid int, home string, ok bool) {
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		return 0, 0, "", false
	}
	uid, errUID := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, errGID := strconv.Atoi(os.Getenv("SUDO_GID"))

	u, errLookup := user.Lookup(name)
	if errLookup != nil {
		if errUID != nil || errGID != nil {
			return 0, 0, "", false // nothing usable
		}
		return uid, gid, "", true // ids only, no home
	}
	if errUID != nil {
		if n, e := strconv.Atoi(u.Uid); e == nil {
			uid = n
		}
	}
	if errGID != nil {
		if n, e := strconv.Atoi(u.Gid); e == nil {
			gid = n
		}
	}
	return uid, gid, u.HomeDir, true
}

// ChownToInvoker hands path back to the user who invoked sudo, best-effort. It is
// a no-op when not running as root, not under sudo, or on platforms without chown
// (os.Geteuid returns -1 on Windows). Failures are logged, never fatal.
func ChownToInvoker(path string) {
	if os.Geteuid() != 0 {
		return
	}
	uid, gid, _, ok := RealUser()
	if !ok {
		return
	}
	if err := os.Chown(path, uid, gid); err != nil {
		log.Printf("could not chown %s to the invoking user: %s", path, err)
	}
}
