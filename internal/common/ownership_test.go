package common

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRealUser(t *testing.T) {
	t.Run("not under sudo", func(t *testing.T) {
		t.Setenv("SUDO_USER", "")
		_, _, _, ok := RealUser()
		assert.False(t, ok)
	})

	t.Run("sudo to root is treated as no invoker", func(t *testing.T) {
		t.Setenv("SUDO_USER", "root")
		_, _, _, ok := RealUser()
		assert.False(t, ok)
	})

	t.Run("sudo from a user resolves numeric ids", func(t *testing.T) {
		// Numeric SUDO_UID/GID are authoritative; the name need not resolve.
		t.Setenv("SUDO_USER", "someuser")
		t.Setenv("SUDO_UID", "12345")
		t.Setenv("SUDO_GID", "678")
		uid, gid, _, ok := RealUser()
		require.True(t, ok)
		assert.Equal(t, 12345, uid)
		assert.Equal(t, 678, gid)
	})
}

func TestCollectScanIdentity(t *testing.T) {
	t.Run("no sudo", func(t *testing.T) {
		t.Setenv("SUDO_USER", "")
		id := CollectScanIdentity()
		assert.NotEmpty(t, id.Username) // falls back to $USER / uid
		assert.Empty(t, id.SudoUser)
	})

	t.Run("with sudo", func(t *testing.T) {
		t.Setenv("SUDO_USER", "alice")
		id := CollectScanIdentity()
		assert.Equal(t, "alice", id.SudoUser)
	})

	t.Run("sudo to root has no invoker", func(t *testing.T) {
		t.Setenv("SUDO_USER", "root")
		id := CollectScanIdentity()
		assert.Empty(t, id.SudoUser)
	})
}

func TestApplyOwnerOverride(t *testing.T) {
	cur, err := user.Current()
	if err != nil || cur.Username == "" {
		t.Skip("cannot resolve current user")
	}
	// Isolate the env vars the override writes (restored after the test).
	t.Setenv("SUDO_USER", "sentinel")
	t.Setenv("SUDO_UID", "sentinel")
	t.Setenv("SUDO_GID", "sentinel")

	require.NoError(t, ApplyOwnerOverride(cur.Username))
	assert.Equal(t, cur.Username, os.Getenv("SUDO_USER"))
	assert.Equal(t, cur.Uid, os.Getenv("SUDO_UID"))
	assert.Equal(t, cur.Gid, os.Getenv("SUDO_GID"))

	// And the resolver downstream now reports that user (when we are root the chown
	// would fire; here we just confirm RealUser consumes the injected env).
	uid, _, _, ok := RealUser()
	if cur.Username != "root" {
		require.True(t, ok)
		assert.Equal(t, cur.Uid, fmtInt(uid))
	}
}

func TestApplyOwnerOverrideUnknownUser(t *testing.T) {
	err := ApplyOwnerOverride("definitely-not-a-real-user-9c3f")
	assert.Error(t, err)
}

func fmtInt(i int) string { return strconv.Itoa(i) }

func TestChownToInvokerNoopWhenNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chown would actually apply")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "snap")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	t.Setenv("SUDO_USER", "someuser")
	t.Setenv("SUDO_UID", "12345")
	t.Setenv("SUDO_GID", "678")

	ChownToInvoker(path) // must be a no-op (we're not root) and never panic
	_, err := os.Stat(path)
	assert.NoError(t, err)
}
