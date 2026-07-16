package report

import (
	"fmt"
	"io"
	"path/filepath"
	"text/tabwriter"

	"github.com/dundee/gdu/v5/pkg/parquet"
)

// PrintCompactionPlan renders the `gdu snapshots compact --dry-run` view: one line per
// group that would be compacted, then any files the planner refuses to touch.
func PrintCompactionPlan(w io.Writer, plan *parquet.CompactionPlan) error {
	if len(plan.Groups) == 0 {
		if _, err := fmt.Fprintln(w, "Nothing to compact."); err != nil {
			return err
		}
	} else {
		if err := printPlanTable(w, plan); err != nil {
			return err
		}
	}
	return printSkipped(w, plan.Skipped)
}

func printPlanTable(w io.Writer, plan *parquet.CompactionPlan) error {
	showHost := false
	var files int
	var bytes int64
	for i := range plan.Groups {
		g := &plan.Groups[i]
		if g.Host != "" {
			showHost = true
		}
		files += len(g.Inputs) + len(g.Redundant)
		bytes += g.InputBytes
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "MONTH\tROOT"
	if showHost {
		header += "\tHOST"
	}
	header += "\tFILES\tSNAPSHOTS\tSIZE\tOUTPUT"
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}
	for i := range plan.Groups {
		g := &plan.Groups[i]
		row := g.Month + "\t" + g.ScanRoot
		if showHost {
			row += "\t" + g.Host
		}
		row += fmt.Sprintf("\t%d\t%d\t%s\t%s",
			len(g.Inputs)+len(g.Redundant), len(g.Snapshots),
			formatBinarySize(g.InputBytes), filepath.Base(g.OutputPath))
		if _, err := fmt.Fprintln(tw, row); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "Would compact %d files (%s) into %d monthly file(s). Nothing was written (dry run).\n",
		files, formatBinarySize(bytes), len(plan.Groups))
	return err
}

// PrintCompactionResult renders the summary after a real compaction run:
// totals for the groups that succeeded, plus every failure and every source
// that could not be deleted.
func PrintCompactionResult(w io.Writer, res *parquet.CompactionResult) error {
	if len(res.Groups) == 0 {
		if _, err := fmt.Fprintln(w, "Nothing to compact."); err != nil {
			return err
		}
		return printSkipped(w, res.Plan.Skipped)
	}

	var ok, failed, deleted int
	for i := range res.Groups {
		g := &res.Groups[i]
		if g.Err != nil {
			failed++
		} else {
			ok++
		}
		deleted += len(g.Deleted)
	}

	if ok > 0 {
		if _, err := fmt.Fprintf(w, "Compacted %d group(s): %s -> %s (removed %d source file(s)).\n",
			ok, formatBinarySize(res.InputBytes), formatBinarySize(res.OutputBytes), deleted); err != nil {
			return err
		}
	}
	for i := range res.Groups {
		g := &res.Groups[i]
		if g.Err != nil {
			if _, err := fmt.Fprintf(w, "failed %s %s: %s (sources kept)\n",
				g.Group.Month, g.Group.ScanRoot, g.Err); err != nil {
				return err
			}
		}
		for _, derr := range g.DeleteErrs {
			if _, err := fmt.Fprintf(w, "could not remove %s (will retry next run)\n", derr); err != nil {
				return err
			}
		}
	}
	if failed > 0 {
		if _, err := fmt.Fprintf(w, "%d group(s) failed; their source files were left untouched.\n", failed); err != nil {
			return err
		}
	}
	return printSkipped(w, res.Plan.Skipped)
}

func printSkipped(w io.Writer, skipped []parquet.SkippedFile) error {
	for _, s := range skipped {
		if _, err := fmt.Fprintf(w, "skipped %s: %s\n", filepath.Base(s.Path), s.Reason); err != nil {
			return err
		}
	}
	return nil
}
