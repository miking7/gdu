package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dundee/gdu/v5/cmd/gdu/app"
)

func TestNoViewFileFlagRegistered(t *testing.T) {
	flag := rootCmd.Flags().Lookup("no-view-file")
	if flag == nil {
		t.Fatal("expected no-view-file flag to be registered")
	}
}

func TestNoViewFileFlagCanBeSet(t *testing.T) {
	t.Cleanup(func() {
		_ = rootCmd.Flags().Set("no-view-file", "false")
	})

	err := rootCmd.Flags().Set("no-view-file", "true")
	if err != nil {
		t.Fatalf("expected setting no-view-file flag to succeed: %v", err)
	}

	if !af.NoViewFile {
		t.Fatal("expected NoViewFile to be true after setting flag")
	}
}

func TestInteractiveFlagRegistered(t *testing.T) {
	flag := rootCmd.Flags().Lookup("interactive")
	if flag == nil {
		t.Fatal("expected interactive flag to be registered")
	}
}

func TestInteractiveFlagCanBeSet(t *testing.T) {
	t.Cleanup(func() {
		_ = rootCmd.Flags().Set("interactive", "false")
	})

	err := rootCmd.Flags().Set("interactive", "true")
	if err != nil {
		t.Fatalf("expected setting interactive flag to succeed: %v", err)
	}

	if !af.Interactive {
		t.Fatal("expected Interactive to be true after setting flag")
	}
}

func TestDeletedFlagsAreGone(t *testing.T) {
	// Clean break: the standalone-verb flags were replaced by the snapshots
	// subcommand and must be unknown-flag errors, not hidden aliases.
	for _, name := range []string{"list-scans", "compact-scans", "dry-run", "auto-compact", "baseline-scan"} {
		if rootCmd.Flags().Lookup(name) != nil {
			t.Errorf("expected flag --%s to be deleted from the root command", name)
		}
	}
}

func TestSnapshotFlagsRegistered(t *testing.T) {
	// --snapshots-dir moved to the persistent set; see TestSnapshotsSubcommandWiring.
	for _, name := range []string{"no-auto-compact", "baseline-root", "snapshot", "save-snapshots"} {
		if rootCmd.Flags().Lookup(name) == nil {
			t.Errorf("expected flag --%s to be registered", name)
		}
	}
}

func TestSnapshotsSubcommandWiring(t *testing.T) {
	if snapshotsCmd.Name() != "snapshots" || !snapshotsCmd.HasAlias("snaps") {
		t.Fatalf("expected the snapshots command with alias snaps, got %q %v",
			snapshotsCmd.Name(), snapshotsCmd.Aliases)
	}

	var haveList, haveCompact bool
	for _, sub := range snapshotsCmd.Commands() {
		switch sub.Name() {
		case "list":
			haveList = true
			if !sub.HasAlias("ls") {
				t.Error("expected `snapshots list` to have alias ls")
			}
		case "compact":
			haveCompact = true
			if sub.Flags().Lookup("dry-run") == nil {
				t.Error("expected `snapshots compact` to own the --dry-run flag")
			}
		}
	}
	if !haveList || !haveCompact {
		t.Fatalf("expected list and compact subcommands, got list=%v compact=%v", haveList, haveCompact)
	}

	// The shared flags are persistent on the root, so subcommands inherit them.
	for _, name := range []string{"snapshots-dir", "owner", "max-cores", "config-file", "log-file"} {
		if rootCmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("expected --%s to be a persistent root flag", name)
		}
	}
}

// TestSnapshotsSubcommandExecute runs the real cobra dispatch end-to-end for
// the list and compact verbs (and the snaps/ls aliases) on an empty archive.
func TestSnapshotsSubcommandExecute(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"list default", []string{"snapshots"}, "No snapshots found."},
		{"list alias", []string{"snaps", "ls"}, "No snapshots found."},
		{"compact dry-run", []string{"snapshots", "compact", "--dry-run"}, "Nothing to compact."},
		{"compact", []string{"snapshots", "compact"}, "Nothing to compact."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origDir := af.SnapshotsDir
			t.Cleanup(func() {
				af.SnapshotsDir = origDir
				snapshotsDryRun = false
				rootCmd.SetArgs(nil)
				rootCmd.SetOut(nil)
			})

			var out strings.Builder
			rootCmd.SetOut(&out)
			rootCmd.SetArgs(append(tc.args, "--snapshots-dir", t.TempDir()))
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("Execute(%v): %v", tc.args, err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("Execute(%v) output %q, want it to contain %q", tc.args, out.String(), tc.want)
			}
		})
	}
}

func TestWriteConfigTargetFreshHome(t *testing.T) {
	home := t.TempDir()

	path, err := writeConfigTarget(home)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "gdu", "gdu.yaml")
	if path != want {
		t.Errorf("fresh home: got %q, want %q", path, want)
	}
	// The XDG directory must exist afterwards so the write itself succeeds.
	if _, err := os.Stat(filepath.Dir(want)); err != nil {
		t.Errorf("expected config dir to be created: %v", err)
	}
}

func TestWriteConfigTargetPrefersExistingXDG(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, ".config", "gdu", "gdu.yaml")
	legacy := filepath.Join(home, ".gdu.yaml")
	if err := os.MkdirAll(filepath.Dir(xdg), 0o700); err != nil {
		t.Fatal(err)
	}
	// Both exist: the XDG path (read first) wins.
	for _, p := range []string{xdg, legacy} {
		if err := os.WriteFile(p, []byte("log-file: /dev/null\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	path, err := writeConfigTarget(home)
	if err != nil {
		t.Fatal(err)
	}
	if path != xdg {
		t.Errorf("got %q, want the existing XDG config %q", path, xdg)
	}
}

func TestWriteConfigTargetKeepsLegacyInPlace(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".gdu.yaml")
	if err := os.WriteFile(legacy, []byte("log-file: /dev/null\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := writeConfigTarget(home)
	if err != nil {
		t.Fatal(err)
	}
	if path != legacy {
		t.Errorf("got %q, want the existing legacy config %q", path, legacy)
	}
}

func TestInitConfigMalformedSystemConfig(t *testing.T) {
	// Write invalid YAML to a temp file and point systemConfigPath at it.
	tmp := filepath.Join(t.TempDir(), "gdu.yaml")
	if err := os.WriteFile(tmp, []byte(":\tinvalid: yaml: {"), 0o600); err != nil {
		t.Fatalf("could not write temp config: %v", err)
	}

	origPath := systemConfigPath
	origErr := configErr
	origAf := af
	t.Cleanup(func() {
		systemConfigPath = origPath
		configErr = origErr
		af = origAf
	})

	systemConfigPath = tmp
	af = &app.Flags{}
	configErr = nil

	initConfig()

	if configErr == nil {
		t.Fatal("expected configErr to be set for malformed system config, got nil")
	}
}

func TestInitConfigMalformedUserConfig(t *testing.T) {
	// Write invalid YAML to a temp file and pass it via --config-file.
	tmp := filepath.Join(t.TempDir(), "user.yaml")
	if err := os.WriteFile(tmp, []byte(":\tinvalid: yaml: {"), 0o600); err != nil {
		t.Fatalf("could not write temp config: %v", err)
	}

	origArgs := os.Args
	origPath := systemConfigPath
	origErr := configErr
	origAf := af
	t.Cleanup(func() {
		os.Args = origArgs
		systemConfigPath = origPath
		configErr = origErr
		af = origAf
	})

	// Point system config at a nonexistent path so it is skipped cleanly.
	systemConfigPath = filepath.Join(t.TempDir(), "nonexistent.yaml")
	os.Args = []string{"gdu", "--config-file=" + tmp}
	af = &app.Flags{}
	configErr = nil

	initConfig()

	if configErr == nil {
		t.Fatal("expected configErr to be set for malformed user config, got nil")
	}
}
