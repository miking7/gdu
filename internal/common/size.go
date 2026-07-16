// Package common contains commong logic and interfaces used across Gdu
// nolint: revive //Why: this is common package
package common

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// sizeUnits maps a (case-insensitive) unit suffix to its binary (1024-based)
// multiplier, matching ncdu/du and the ncdu_to_parquet.py threshold parser.
var sizeUnits = map[string]int64{
	"":    1,
	"B":   1,
	"K":   1 << 10,
	"KB":  1 << 10,
	"KIB": 1 << 10,
	"M":   1 << 20,
	"MB":  1 << 20,
	"MIB": 1 << 20,
	"G":   1 << 30,
	"GB":  1 << 30,
	"GIB": 1 << 30,
	"T":   1 << 40,
	"TB":  1 << 40,
	"TIB": 1 << 40,
}

var sizeRe = regexp.MustCompile(`^(\d*\.?\d+)\s*([a-zA-Z]*)$`)

// ParseSizeThreshold parses a human size like "10M", "500K", "2G", "1.5KiB",
// a plain byte count, or "0"/"" (both meaning "no threshold"). Units are binary
// (1M = 1048576). It returns the size in bytes, or an error for malformed input.
func ParseSizeThreshold(value string) (int64, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		return 0, nil
	}
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid size threshold: %q", value)
	}
	num, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size threshold: %q", value)
	}
	mult, ok := sizeUnits[strings.ToUpper(m[2])]
	if !ok {
		return 0, fmt.Errorf("unknown size unit in threshold: %q", value)
	}
	return int64(num * float64(mult)), nil
}
