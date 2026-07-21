//go:build !darwin

package device

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScanRootAliasesMountsOffDarwin(t *testing.T) {
	assert.False(t, ScanRootAliasesMounts("/"), "only macOS aliases a volume into another's tree")
	assert.False(t, ScanRootAliasesMounts("/home"))
}
