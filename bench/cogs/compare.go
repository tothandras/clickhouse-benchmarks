package cogs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadResult reads a cogs result by path, or by `<scenario>` shorthand which
// resolves to the latest run under <resultsRoot>/<scenario>/cogs/ (same
// convention as the perf path's compare).
func LoadResult(resultsRoot, ref string) (Result, string, error) {
	path := ref
	if !strings.HasSuffix(ref, ".json") {
		dir := filepath.Join(resultsRoot, ref, "cogs")
		entries, err := os.ReadDir(dir)
		if err != nil {
			return Result{}, "", fmt.Errorf("cogs compare: resolve %q: %w", ref, err)
		}
		var jsons []string
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				jsons = append(jsons, e.Name())
			}
		}
		if len(jsons) == 0 {
			return Result{}, "", fmt.Errorf("cogs compare: no cogs results for scenario %q in %s", ref, dir)
		}
		sort.Strings(jsons) // timestamp naming sorts chronologically
		path = filepath.Join(dir, jsons[len(jsons)-1])
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return Result{}, "", err
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, "", fmt.Errorf("cogs compare: parse %s: %w", path, err)
	}
	if r.Kind != "cogs/v1" {
		return Result{}, "", fmt.Errorf("cogs compare: %s is %q, want cogs/v1", path, r.Kind)
	}
	return r, path, nil
}

// Compare diffs two runs' unit costs. Cross-profile price comparison is
// refused unless allowProfileMismatch, in which case only resource lines
// (CPU seconds, bytes/event, coverage) are compared — prices from different
// rate cards are not comparable numbers.
func Compare(a, b Result, allowProfileMismatch bool) (string, error) {
	var out strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&out, format, args...) }

	sameProfile := a.Pricing.Name == b.Pricing.Name
	if !sameProfile && !allowProfileMismatch {
		return "", fmt.Errorf("cogs compare: pricing profiles differ (%q vs %q); cross-profile prices are not comparable — pass --allow-profile-mismatch to compare resource lines only",
			a.Pricing.Name, b.Pricing.Name)
	}

	w("# cogs compare: %s/%s vs %s/%s\n\n", a.Scenario, a.Cell.Name, b.Scenario, b.Cell.Name)
	w("| | run A | run B |\n|---|---|---|\n")
	w("| run id | `%s` | `%s` |\n", a.RunID, b.RunID)
	w("| harness | `%s` | `%s` |\n", a.HarnessCommit, b.HarnessCommit)
	w("| profile | %s | %s |\n", a.Pricing.Name, b.Pricing.Name)
	w("| coverage | %.1f%% | %.1f%% |\n\n", a.Attribution.Coverage*100, b.Attribution.Coverage*100)

	// Class-set difference is reported, not fatal: baseline-openmeter has no
	// lookup class, and that asymmetry is itself a finding.
	onlyA, onlyB := classSetDiff(a, b)
	if len(onlyA) > 0 || len(onlyB) > 0 {
		w("Class sets differ: only in A: [%s], only in B: [%s]; matching classes compared below.\n\n",
			strings.Join(onlyA, ", "), strings.Join(onlyB, ", "))
	}

	w("## Resource lines\n\n| line | A | B | delta |\n|---|---|---|---|\n")
	num := func(name string, va, vb float64, format string) {
		delta := "n/a"
		if va != 0 {
			delta = fmt.Sprintf("%+.1f%%", (vb-va)/va*100)
		}
		w("| %s | "+format+" | "+format+" | %s |\n", name, va, vb, delta)
	}
	num("insert cpu sec", a.Attribution.InsertCPUSec, b.Attribution.InsertCPUSec, "%.1f")
	num("merge cpu sec", a.Attribution.MergeCPUSec, b.Attribution.MergeCPUSec, "%.1f")
	for _, class := range sharedClasses(a, b) {
		for _, cache := range cacheStates(a.Attribution.QueryCPUSec[class]) {
			if _, ok := b.Attribution.QueryCPUSec[class][cache]; !ok {
				continue
			}
			num("query cpu sec: "+class+" ("+cache+")",
				a.Attribution.QueryCPUSec[class][cache], b.Attribution.QueryCPUSec[class][cache], "%.1f")
		}
	}
	num("bytes/event settled", a.Attribution.BytesPerEventSettled, b.Attribution.BytesPerEventSettled, "%.1f")

	if sameProfile {
		w("\n## Unit costs (%s)\n\n| line | A | B | delta |\n|---|---|---|---|\n", a.Pricing.Name)
		if a.Accounting.EventsIngested > 0 || b.Accounting.EventsIngested > 0 {
			num("$/1M events (billed)", a.Costs.Unit.USDPer1MEvents.BilledShape, b.Costs.Unit.USDPer1MEvents.BilledShape, "%.6f")
			num("$/1M events (cpu-linear)", a.Costs.Unit.USDPer1MEvents.CPULinear, b.Costs.Unit.USDPer1MEvents.CPULinear, "%.6f")
			num("storage $/1M events-month", a.Costs.Unit.USDPer1MEventsMonthStorage, b.Costs.Unit.USDPer1MEventsMonthStorage, "%.6f")
		}
		for _, class := range sharedClasses(a, b) {
			for _, cache := range []string{"cold", "warm"} {
				pa, okA := a.Costs.Unit.USDPer1KQueries[class][cache]
				pb, okB := b.Costs.Unit.USDPer1KQueries[class][cache]
				if okA && okB {
					num("$/1k queries: "+class+" ("+cache+")", pa.BilledShape, pb.BilledShape, "%.6f")
				}
			}
		}
	} else {
		w("\n(prices omitted: profiles differ)\n")
	}
	return out.String(), nil
}

func classSetDiff(a, b Result) (onlyA, onlyB []string) {
	for class := range a.Attribution.QueryCPUSec {
		if _, ok := b.Attribution.QueryCPUSec[class]; !ok {
			onlyA = append(onlyA, class)
		}
	}
	for class := range b.Attribution.QueryCPUSec {
		if _, ok := a.Attribution.QueryCPUSec[class]; !ok {
			onlyB = append(onlyB, class)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return onlyA, onlyB
}

func sharedClasses(a, b Result) []string {
	var out []string
	for class := range a.Attribution.QueryCPUSec {
		if _, ok := b.Attribution.QueryCPUSec[class]; ok {
			out = append(out, class)
		}
	}
	sort.Strings(out)
	return out
}
