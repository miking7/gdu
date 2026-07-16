package report

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dundee/gdu/v5/pkg/parquet"
)

func TestPrintCompactionPlanEmpty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, PrintCompactionPlan(&buf, &parquet.CompactionPlan{}))
	assert.Contains(t, buf.String(), "Nothing to compact.")
}

func TestPrintCompactionPlanTable(t *testing.T) {
	plan := &parquet.CompactionPlan{
		Groups: []parquet.CompactionGroup{{
			Host:       "mbp",
			ScanRoot:   "/home",
			Month:      "2026-05",
			Inputs:     []string{"/a/snapshot_1.parquet", "/a/snapshot_2.parquet"},
			Snapshots:  make([]parquet.SnapshotInfo, 2),
			InputBytes: 3 << 20,
			OutputPath: "/a/monthly_2026-05_home.parquet",
		}},
		Skipped: []parquet.SkippedFile{{Path: "/a/bad.parquet", Reason: "unreadable: nope"}},
	}

	var buf bytes.Buffer
	require.NoError(t, PrintCompactionPlan(&buf, plan))
	out := buf.String()
	assert.Contains(t, out, "2026-05")
	assert.Contains(t, out, "/home")
	assert.Contains(t, out, "mbp")
	assert.Contains(t, out, "monthly_2026-05_home.parquet")
	assert.Contains(t, out, "3.0 MiB")
	assert.Contains(t, out, "dry run")
	assert.Contains(t, out, "skipped bad.parquet: unreadable: nope")
}

func TestPrintCompactionPlanHidesHostColumnWhenUnset(t *testing.T) {
	plan := &parquet.CompactionPlan{
		Groups: []parquet.CompactionGroup{{
			ScanRoot: "/home", Month: "2026-05",
			Inputs: []string{"a"}, OutputPath: "/a/monthly_2026-05_home.parquet",
		}},
	}
	var buf bytes.Buffer
	require.NoError(t, PrintCompactionPlan(&buf, plan))
	assert.NotContains(t, buf.String(), "HOST")
}

func TestPrintCompactionResult(t *testing.T) {
	res := &parquet.CompactionResult{
		Plan: &parquet.CompactionPlan{},
		Groups: []parquet.GroupResult{
			{
				Group:       parquet.CompactionGroup{Month: "2026-05", ScanRoot: "/home"},
				OutputBytes: 1 << 20,
				Deleted:     []string{"/a/snapshot_1.parquet", "/a/snapshot_2.parquet"},
			},
			{
				Group: parquet.CompactionGroup{Month: "2026-06", ScanRoot: "/data"},
				Err:   errors.New("verification failed"),
			},
		},
		InputBytes:  4 << 20,
		OutputBytes: 1 << 20,
	}

	var buf bytes.Buffer
	require.NoError(t, PrintCompactionResult(&buf, res))
	out := buf.String()
	assert.Contains(t, out, "Compacted 1 group(s): 4.0 MiB -> 1.0 MiB (removed 2 source file(s)).")
	assert.Contains(t, out, "failed 2026-06 /data: verification failed (sources kept)")
	assert.Contains(t, out, "1 group(s) failed")
}

func TestPrintCompactionResultNothingToDo(t *testing.T) {
	var buf bytes.Buffer
	res := &parquet.CompactionResult{Plan: &parquet.CompactionPlan{}}
	require.NoError(t, PrintCompactionResult(&buf, res))
	assert.Contains(t, buf.String(), "Nothing to compact.")
}
