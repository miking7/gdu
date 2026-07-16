package common

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

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

// rootUsername is the superuser name; an invoking user reported as "root" means
// there is no unprivileged user to defer ownership to (a direct root login or a
// scheduler, not a sudo hand-off).
const rootUsername = "root"

// CollectScanIdentity gathers the host and user identity of the current scan.
func CollectScanIdentity() ScanIdentity {
	id := ScanIdentity{Username: currentUsername(), Host: HostnameBestEffort()}
	if su := os.Getenv("SUDO_USER"); su != "" && su != rootUsername {
		id.SudoUser = su
	}
	return id
}

// HostnameBestEffort returns the local hostname, or "" when it can't be
// determined — the same value CollectScanIdentity stamps into every snapshot.
// A reader recognizes "this machine" by an exact match against it (used by the
// listing surfaces' foreign-host rule and compaction's lock-owner check).
func HostnameBestEffort() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}

// HostIsForeign reports whether host names a machine other than local — the
// rule the snapshot-listing surfaces use to show a Host column only for
// snapshots taken elsewhere. An empty host, or one equal to the local
// hostname, is "this machine". Comparison is exact: a since-renamed machine's
// older snapshots read as foreign, which is informative rather than wrong.
func HostIsForeign(host, local string) bool {
	return host != "" && host != local
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
	if name == "" || name == rootUsername {
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

// SnapshotDirAnnouncement is the one-line notice shown (and logged) the first
// time a save has to create the snapshot archive directory, so zero-config
// recording never starts silently. Callers prefix/print it per their medium
// (stderr line, TUI header notice).
func SnapshotDirAnnouncement(dir string) string {
	return fmt.Sprintf("saving snapshots to %s (set save-snapshots: never to disable)", abbreviateHome(dir))
}

// abbreviateHome renders path with the leading home directory as "~" for
// display. Under sudo the invoking user's home is tried first, since that is
// where the default snapshots-dir lives.
func abbreviateHome(path string) string {
	if _, _, realHome, ok := RealUser(); ok && realHome != "" {
		if short, done := abbreviatePrefix(path, realHome); done {
			return short
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if short, done := abbreviatePrefix(path, home); done {
			return short
		}
	}
	return path
}

func abbreviatePrefix(path, home string) (string, bool) {
	if rest, found := strings.CutPrefix(path, home+string(os.PathSeparator)); found {
		return filepath.Join("~", rest), true
	}
	return path, false
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
