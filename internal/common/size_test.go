package common_test

import (
	"testing"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSizeThreshold(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"", 0},
		{"  ", 0},
		{"1024", 1024},
		{"10M", 10 << 20},
		{"500K", 500 << 10},
		{"2G", 2 << 30},
		{"1.5K", 1536},
		{"10MiB", 10 << 20},
		{"5GB", 5 << 30},
		{"1T", 1 << 40},
		{"100 M", 100 << 20},
		{"10m", 10 << 20},
	}
	for _, c := range cases {
		got, err := common.ParseSizeThreshold(c.in)
		require.NoError(t, err, "input %q", c.in)
		assert.Equal(t, c.want, got, "input %q", c.in)
	}
}

func TestParseSizeThresholdErrors(t *testing.T) {
	for _, in := range []string{"bogus", "12X", "M", "1.2.3", "-5"} {
		_, err := common.ParseSizeThreshold(in)
		assert.Error(t, err, "input %q", in)
	}
}
