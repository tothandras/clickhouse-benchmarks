package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/openmeterio/ch-playground/bench/runner"
)

// newCompareCmd builds the `bench compare A B` subcommand: it reads the latest
// result file from each scenario and prints the per-query delta table (p50,
// CPU, ingest) the README otherwise maintains by hand. Queries present in only
// one side are listed as such rather than dropped.
func newCompareCmd() *cobra.Command {
	var resultsDir string
	cmd := &cobra.Command{
		Use:   "compare <baseline-scenario> <candidate-scenario>",
		Short: "Diff the latest result of two scenarios into a delta table",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			return compare(resultsDir, args[0], args[1])
		},
	}
	cmd.Flags().StringVar(&resultsDir, "results-dir", "bench/results", "directory holding per-scenario result JSON files")
	cmd.SilenceUsage = true
	return cmd
}

func compare(resultsDir, baseName, candName string) error {
	base, err := latestRun(resultsDir, baseName)
	if err != nil {
		return err
	}
	cand, err := latestRun(resultsDir, candName)
	if err != nil {
		return err
	}

	fmt.Printf("# Compare: %s → %s\n\n", baseName, candName)
	fmt.Printf("baseline `%s` (commit %s) vs candidate `%s` (commit %s)\n\n",
		baseName, base.HarnessCommit, candName, cand.HarnessCommit)

	// Index the warm, lowest-concurrency measurement per query name on each side
	// so the comparison is apples-to-apples regardless of sweep/cold variants.
	baseByName := indexQueries(base.Queries)
	candByName := indexQueries(cand.Queries)

	names := unionNames(baseByName, candByName)
	fmt.Println("| Query | p50 Δ | CPU Δ | base p50 ms | cand p50 ms |")
	fmt.Println("| --- | ---: | ---: | ---: | ---: |")
	for _, n := range names {
		b, bok := baseByName[n]
		c, cok := candByName[n]
		switch {
		case bok && cok:
			fmt.Printf("| %s | %s | %s | %.1f | %.1f |\n",
				n, pctDelta(b.P50Sec, c.P50Sec), cpuDelta(b, c), b.P50Sec*1000, c.P50Sec*1000)
		case bok:
			fmt.Printf("| %s | _(baseline only)_ | | %.1f | — |\n", n, b.P50Sec*1000)
		default:
			fmt.Printf("| %s | _(candidate only)_ | | — | %.1f |\n", n, c.P50Sec*1000)
		}
	}

	fmt.Printf("\nIngest: base %s, candidate %s (%s)\n",
		ingestStr(base.Ingest), ingestStr(cand.Ingest), ingestDelta(base.Ingest, cand.Ingest))

	if err := compareValues(base, cand, baseName, candName); err != nil {
		return err
	}
	return nil
}

// compareValues diffs the per-query result digests recorded by each run, proving
// the two table designs compute the same meter values. It only compares when
// both runs covered an IDENTICAL seeded window — otherwise the events differ
// (e.g. an unpinned per-scenario time.Now() TimeEnd) and a digest mismatch would
// be a false alarm, not a design difference. Returns a non-nil error if any
// query's values genuinely differ, so the command exits non-zero (CI gate).
func compareValues(base, cand runner.Run, baseName, candName string) error {
	bp, cp := base.ValueParity, cand.ValueParity
	if bp == nil || cp == nil {
		fmt.Println("\n## Value parity\n\nskipped — one or both runs predate value-digest capture (re-run the bench to populate it).")
		return nil
	}
	if bp.From != cp.From || bp.To != cp.To {
		fmt.Printf("\n## Value parity\n\nskipped — runs cover different windows (%s: %s→%s, %s: %s→%s). "+
			"Re-run both with the same `--time-end` so they seed an identical event stream, then compare.\n",
			baseName, bp.From, bp.To, candName, cp.From, cp.To)
		return nil
	}

	names := make([]string, 0, len(bp.Digests))
	for n := range bp.Digests {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Printf("\n## Value parity (window %s → %s)\n\n", bp.From, bp.To)
	fmt.Println("| Query | result | rows |")
	fmt.Println("| --- | --- | ---: |")
	var match, differ, missing int
	var differing []string
	for _, n := range names {
		b := bp.Digests[n]
		c, ok := cp.Digests[n]
		switch {
		case !ok:
			fmt.Printf("| %s | _(candidate only)_ | %d |\n", n, b.Rows)
			missing++
		case b.Error != "" || c.Error != "":
			fmt.Printf("| %s | ERROR | — |\n", n)
			differ++
			differing = append(differing, n)
		case b.Hash == c.Hash:
			fmt.Printf("| %s | MATCH | %d |\n", n, b.Rows)
			match++
		default:
			fmt.Printf("| %s | **DIFFERS** | %d vs %d |\n", n, b.Rows, c.Rows)
			differ++
			differing = append(differing, n)
		}
	}
	// candidate-only queries (present in cand, absent in base)
	for n := range cp.Digests {
		if _, ok := bp.Digests[n]; !ok {
			fmt.Printf("| %s | _(candidate only)_ | %d |\n", n, cp.Digests[n].Rows)
			missing++
		}
	}
	fmt.Printf("\nMATCH=%d DIFFERS=%d (candidate-only=%d)\n", match, differ, missing)
	if differ > 0 {
		return fmt.Errorf("value parity: %d quer(y/ies) differ between %s and %s: %s",
			differ, baseName, candName, strings.Join(differing, ", "))
	}
	return nil
}

// indexQueries keeps, per query name, the warm result at the lowest concurrency
// (the canonical apples-to-apples measurement). A repeated name from a sweep
// keeps the first warm/lowest-c hit.
func indexQueries(qs []runner.BenchResult) map[string]runner.BenchResult {
	out := map[string]runner.BenchResult{}
	for _, q := range qs {
		if q.Error != "" {
			continue
		}
		if q.CacheState == "cold" {
			continue
		}
		if existing, ok := out[q.Name]; ok && existing.Concurrency <= q.Concurrency {
			continue
		}
		out[q.Name] = q
	}
	return out
}

func unionNames(a, b map[string]runner.BenchResult) []string {
	set := map[string]bool{}
	for n := range a {
		set[n] = true
	}
	for n := range b {
		set[n] = true
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func pctDelta(base, cand float64) string {
	if base == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.0f%%", (cand-base)/base*100)
}

func cpuDelta(b, c runner.BenchResult) string {
	if b.CPUp50Us == nil || c.CPUp50Us == nil {
		return "n/a"
	}
	return pctDelta(*b.CPUp50Us, *c.CPUp50Us)
}

func ingestStr(i *runner.IngestResult) string {
	if i == nil || i.EventsPerSecond == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.0f events/s", i.EventsPerSecond)
}

func ingestDelta(b, c *runner.IngestResult) string {
	if b == nil || c == nil || b.EventsPerSecond == 0 {
		return "n/a"
	}
	return "Δ " + pctDelta(b.EventsPerSecond, c.EventsPerSecond)
}

// latestRun reads the newest .json result file under resultsDir/<scenario>/.
// Timestamped filenames (RFC3339-ish, sorted lexically) make the last one the
// newest.
func latestRun(resultsDir, scenario string) (runner.Run, error) {
	dir := filepath.Join(resultsDir, scenario)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return runner.Run{}, fmt.Errorf("read results for %s: %w", scenario, err)
	}
	var jsons []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			jsons = append(jsons, e.Name())
		}
	}
	if len(jsons) == 0 {
		return runner.Run{}, fmt.Errorf("no result JSON files in %s", dir)
	}
	sort.Strings(jsons)
	path := filepath.Join(dir, jsons[len(jsons)-1])
	b, err := os.ReadFile(path)
	if err != nil {
		return runner.Run{}, err
	}
	var run runner.Run
	if err := json.Unmarshal(b, &run); err != nil {
		return runner.Run{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return run, nil
}
