package parquet

import (
	"context"
	"errors"
	"os"
	"regexp"
	"time"

	log "github.com/sirupsen/logrus"
)

// snapshotNamePattern matches gdu daily snapshot filenames
// (snapshot_<YYYYMMDDTHHMMSS>…parquet) and captures the local-time month digits.
// Monthly outputs (monthly_…) deliberately don't match: they are compaction's
// own products and never make a month "loose".
var snapshotNamePattern = regexp.MustCompile(`^snapshot_(\d{6})\d{2}T\d{6}.*\.parquet$`)

// NeedsCompaction is the cheap auto-compaction trigger: it reports whether any
// closed local-time month appears to have loose daily snapshots, judging by
// filenames alone (one ReadDir, no file opens). Snapshot filenames carry the
// scan's local timestamp, so the month is read straight out of the name. The
// answer is a hint, not a promise: RunCompaction re-plans from file footers
// and decides for real, so a false positive costs one no-op run and a false
// negative (e.g. renamed files) just means waiting for an explicit
// `gdu snapshots compact`.
func NeedsCompaction(snapshotsDir string, now time.Time) bool {
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return false // missing/unreadable archive: nothing to do
	}
	currentMonth := now.Format("200601")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := snapshotNamePattern.FindStringSubmatch(e.Name())
		if len(m) > 1 && m[1] < currentMonth {
			return true
		}
	}
	return false
}

// AutoCompact runs one opportunistic compaction of snapshotsDir when the cheap
// NeedsCompaction predicate fires. It is the engine behind auto-compaction:
// a held archive lock is skipped silently (some other gdu is already doing
// the work), and cancelling ctx aborts safely mid-run (see
// RunCompactionContext). It returns (nil, nil) when there was nothing to do
// or the lock was held. Callers enforce the at-most-one-run-per-process rule
// (common.UI.ClaimAutoCompactRun).
func AutoCompact(ctx context.Context, snapshotsDir string, now time.Time) (*CompactionResult, error) {
	if !NeedsCompaction(snapshotsDir, now) {
		return nil, nil
	}
	res, err := RunCompactionContext(ctx, snapshotsDir, now, nil)
	if errors.Is(err, ErrCompactionLocked) {
		log.Printf("auto-compact: %s; skipping this run", err)
		return nil, nil
	}
	logAutoCompact(res)
	return res, err
}

// logAutoCompact records an auto-compaction outcome in the log (the only
// place auto runs report: stdout stays clean for piping and the TUI shows
// just its indicator). Run-level errors are the callers' to log.
func logAutoCompact(res *CompactionResult) {
	if res == nil {
		return
	}
	failed := 0
	for i := range res.Groups {
		if gerr := res.Groups[i].Err; gerr != nil {
			failed++
			log.Printf("auto-compact: group %s %s failed: %s (sources kept)",
				res.Groups[i].Group.Month, res.Groups[i].Group.ScanRoot, gerr)
		}
	}
	log.Printf("auto-compact: %d group(s) compacted, %d failed", len(res.Groups)-failed, failed)
}
