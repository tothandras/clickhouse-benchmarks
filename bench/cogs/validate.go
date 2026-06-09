package cogs

import (
	"fmt"
	"math"
	"strings"
)

// additivityTolerance is the per-component relative residual above which the
// linear COGS model is considered broken by interference.
const additivityTolerance = 0.15

// ValidateAdditivity checks whether cpu-linear COGS composes: for each
// component, |mixed - (ingest-only + query-only)| / mixed <= 15%. PASS means
// per-tenant COGS can be a linear formula; FAIL names the diverging component
// (merge pressure inflating query CPU, cache contention, ...). All three runs
// must be over the same scenario.
func ValidateAdditivity(ingestOnly, queryOnly, mixed Result) (string, error) {
	for _, r := range []Result{ingestOnly, queryOnly, mixed} {
		if r.Scenario != mixed.Scenario {
			return "", fmt.Errorf("cogs validate: runs span scenarios %q and %q; additivity is only defined on one scenario", r.Scenario, mixed.Scenario)
		}
	}
	if ingestOnly.Cell.Ingest.EventsPerSec == 0 || ingestOnly.Cell.Query.QPS != 0 {
		return "", fmt.Errorf("cogs validate: first run must be ingest-only (eps>0, qps=0), got %s", ingestOnly.Cell.Name)
	}
	if queryOnly.Cell.Query.QPS == 0 || queryOnly.Cell.Ingest.EventsPerSec != 0 {
		return "", fmt.Errorf("cogs validate: second run must be query-only (qps>0, eps=0), got %s", queryOnly.Cell.Name)
	}
	if mixed.Cell.Ingest.EventsPerSec == 0 || mixed.Cell.Query.QPS == 0 {
		return "", fmt.Errorf("cogs validate: third run must be mixed (eps>0 and qps>0), got %s", mixed.Cell.Name)
	}

	// Normalize to per-second rates: the runs' measure windows may differ in
	// length (and the CI profile shortens them), so raw CPU seconds are not
	// comparable across runs.
	rate := func(r Result, sec float64) float64 {
		if r.Accounting.MeasureSeconds == 0 {
			return 0
		}
		return sec / r.Accounting.MeasureSeconds
	}
	queryCPU := func(r Result) float64 {
		total := 0.0
		for _, byCache := range r.Attribution.QueryCPUSec {
			for _, cpu := range byCache {
				total += cpu
			}
		}
		return total
	}

	type line struct {
		component string
		additive  float64 // ingest-only + query-only, per second
		mixed     float64 // mixed run, per second
	}
	lines := []line{
		{"insert", rate(ingestOnly, ingestOnly.Attribution.InsertCPUSec), rate(mixed, mixed.Attribution.InsertCPUSec)},
		{"merge", rate(ingestOnly, ingestOnly.Attribution.MergeCPUSec), rate(mixed, mixed.Attribution.MergeCPUSec)},
		{"query", rate(queryOnly, queryCPU(queryOnly)), rate(mixed, queryCPU(mixed))},
	}

	var out strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&out, format, args...) }
	w("# cogs validate: additivity of %s + %s vs %s (scenario %s)\n\n",
		ingestOnly.Cell.Name, queryOnly.Cell.Name, mixed.Cell.Name, mixed.Scenario)
	w("| component | additive cpu/s | mixed cpu/s | residual | verdict |\n|---|---|---|---|---|\n")

	pass := true
	var failing []string
	for _, l := range lines {
		if l.mixed == 0 {
			w("| %s | %.3f | 0 | n/a | skip (component absent in mixed run) |\n", l.component, l.additive)
			continue
		}
		residual := math.Abs(l.mixed-l.additive) / l.mixed
		verdict := "PASS"
		if residual > additivityTolerance {
			verdict = "FAIL"
			pass = false
			failing = append(failing, fmt.Sprintf("%s (%.0f%%)", l.component, residual*100))
		}
		w("| %s | %.3f | %.3f | %.1f%% | %s |\n", l.component, l.additive, l.mixed, residual*100, verdict)
	}

	if pass {
		w("\nPASS: cpu-linear COGS composes within %.0f%%; a linear per-tenant cost formula holds at these rates.\n",
			additivityTolerance*100)
	} else {
		w("\nFAIL: interference detected in %s — the mixed workload does not equal the sum of its parts. "+
			"Per-tenant COGS at these rates needs the mixed measurement, not the linear formula.\n",
			strings.Join(failing, ", "))
	}
	return out.String(), nil
}
