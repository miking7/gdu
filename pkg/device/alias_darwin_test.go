//go:build darwin

package device

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScanRootAliasesMountsOnDarwin(t *testing.T) {
	assert.True(t, ScanRootAliasesMounts("/"), "firmlinks alias the data volume into /")
	assert.True(t, ScanRootAliasesMounts("/."), "the path is cleaned first")
	assert.False(t, ScanRootAliasesMounts("/Users"), "a firmlinked dir is reached once")
	assert.False(t, ScanRootAliasesMounts("/Volumes/SD"))
	assert.False(t, ScanRootAliasesMounts("/System/Volumes/Data"))
}
