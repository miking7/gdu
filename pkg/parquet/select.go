package parquet

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/parquet-go/parquet-go"
	log "github.com/sirupsen/logrus"

	"github.com/dundee/gdu/v5/internal/common"
	"github.com/dundee/gdu/v5/pkg/analyze"
)

// SnapshotTimeLayout is the canonical local-time form of a snapshot timestamp,
// used both for display and for --snapshot matching. It matches the timestamp in
// snapshot filenames (minus the separators), so a value copied from a listing
// pastes straight back into --snapshot.
const SnapshotTimeLayout = "2006-01-02T15:04:05"

// SnapshotSelector picks one snapshot from a file that may hold several (a
// compacted archive). The zero value selects the most recent snapshot.
type SnapshotSelector struct {
	// Spec is the --snapshot value: "" or "latest" (newest), "earliest" (oldest),
	// or a local-time prefix such as "2026-06-19T15:30:05", "2026-06-19" (a day)
	// or "2026-06" (a month). A prefix must match exactly one snapshot.
	Spec string
	// Root optionally restricts matching to snapshots of this scan_root, to
	// disambiguate a timestamp shared by several roots in one file.
	Root string
	// ExactTs (with ExactHost) pins selection to one full snapshot identity —
	// (host, Root, ExactTs) — bypassing the textual grammar. Set by archive
	// resolution, which has already chosen a snapshot: a formatted-time spec
	// would be lossy (second precision, DST-fold collisions) where the identity
	// is not. Zero means the textual Spec applies.
	ExactTs   time.Time
	ExactHost string
}

// selectsLatest reports whether the selector takes the newest snapshot by default.
func (s SnapshotSelector) selectsLatest() bool {
	return s.Spec == "" || s.Spec == "latest"
}

// FormatSnapshotTime renders a snapshot's timestamp in the canonical local-time form.
func FormatSnapshotTime(s *SnapshotInfo) string {
	return s.ScanTs.Local().Format(SnapshotTimeLayout)
}

// SelectSnapshot resolves sel against snapshots (as returned by ListSnapshots,
// ordered oldest-first) and returns the chosen snapshot. It returns a descriptive
// error — listing the candidates in --snapshot-ready form — when the selector
// matches no snapshot or is ambiguous, so a caller can surface it to the user.
func SelectSnapshot(snapshots []SnapshotInfo, sel SnapshotSelector) (SnapshotInfo, error) {
	if len(snapshots) == 0 {
		return SnapshotInfo{}, errors.New("file contains no snapshots")
	}

	cands := snapshots
	if sel.Root != "" {
		cands = filterSnapshotsByRoot(snapshots, sel.Root)
		if len(cands) == 0 {
			return SnapshotInfo{}, fmt.Errorf(
				"no snapshot with root %q; available snapshots:\n%s", sel.Root, FormatSnapshotList(snapshots))
		}
	}

	if !sel.ExactTs.IsZero() {
		for i := range cands {
			if cands[i].ScanTs.Equal(sel.ExactTs) && cands[i].Host == sel.ExactHost {
				return cands[i], nil
			}
		}
		return SnapshotInfo{}, fmt.Errorf(
			"snapshot of %s at %s (host %s) not found in this file; it holds:\n%s",
			sel.Root, sel.ExactTs.Local().Format(SnapshotTimeLayout), sel.ExactHost, FormatSnapshotList(cands))
	}

	switch {
	case sel.selectsLatest():
		return cands[len(cands)-1], nil // oldest-first ⇒ last is newest
	case sel.Spec == "earliest":
		return cands[0], nil
	default:
		return selectByPrefix(cands, sel.Spec)
	}
}

// selectByPrefix matches snapshots whose local-time timestamp starts with spec,
// requiring exactly one hit.
func selectByPrefix(scans []SnapshotInfo, spec string) (SnapshotInfo, error) {
	var matched []SnapshotInfo
	for i := range scans {
		if strings.HasPrefix(FormatSnapshotTime(&scans[i]), spec) {
			matched = append(matched, scans[i])
		}
	}
	switch len(matched) {
	case 1:
		return matched[0], nil
	case 0:
		return SnapshotInfo{}, fmt.Errorf(
			"no snapshot matching --snapshot %q; available snapshots:\n%s", spec, FormatSnapshotList(scans))
	default:
		return SnapshotInfo{}, fmt.Errorf(
			"--snapshot %q is ambiguous (%d snapshots match); use a longer timestamp or --snapshot-root:\n%s",
			spec, len(matched), FormatSnapshotList(matched))
	}
}

// filterSnapshotsByRoot keeps only snapshots of the given root, preserving order.
func filterSnapshotsByRoot(scans []SnapshotInfo, root string) []SnapshotInfo {
	var out []SnapshotInfo
	for _, s := range scans {
		if s.ScanRoot == root {
			out = append(out, s)
		}
	}
	return out
}

// FormatSnapshotList renders snapshots (one per line, indented) as timestamp + root
// (+ host only for snapshots from another machine), the form a user pastes
// into --snapshot / --snapshot-root.
func FormatSnapshotList(scans []SnapshotInfo) string {
	local := common.HostnameBestEffort()
	var b strings.Builder
	for i := range scans {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "  %s  %s", FormatSnapshotTime(&scans[i]), scans[i].ScanRoot)
		if common.HostIsForeign(scans[i].Host, local) {
			fmt.Fprintf(&b, "  (%s)", scans[i].Host)
		}
	}
	return b.String()
}

// ReadTreeSelected reconstructs the analyze.Dir tree for the snapshot chosen by sel.
// It is the selector-aware form of ReadTree; see ReadTree for the streaming and
// memory characteristics.
func ReadTreeSelected(r io.ReaderAt, size int64, sel SnapshotSelector) (*analyze.Dir, error) {
	pf, err := parquet.OpenFile(r, size)
	if err != nil {
		return nil, err
	}

	scans, err := listSnapshots(pf)
	if err != nil {
		return nil, err
	}
	if len(scans) == 0 {
		return nil, errors.New("parquet snapshot contains no rows")
	}

	selected, err := SelectSnapshot(scans, sel)
	if err != nil {
		return nil, err
	}
	if len(scans) > 1 && sel.selectsLatest() && sel.Root == "" {
		log.Printf("Parquet file contains %d snapshots; loaded the most recent (%s scanned %s). "+
			"Use --snapshot to choose another or `gdu snapshots <file>` to inspect.",
			len(scans), selected.ScanRoot, FormatSnapshotTime(&selected))
	}

	return readTreeSnapshot(pf, &selected)
}
