package cogs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openmeterio/ch-playground/bench/accounting"
	"github.com/openmeterio/ch-playground/bench/ingest"
	"github.com/openmeterio/ch-playground/bench/pricing"
	"github.com/openmeterio/ch-playground/bench/replay"
	"github.com/openmeterio/ch-playground/bench/runner"
)

// PhaseInfo is one executed phase's window.
type PhaseInfo struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Seconds float64   `json:"seconds"`
}

// Phases records the run's executed phase timeline.
type Phases struct {
	Profile      string    `json:"profile,omitempty"` // run profile ("ci"), not the pricing profile
	Soak         PhaseInfo `json:"soak"`
	Measure      PhaseInfo `json:"measure"`
	Drain        PhaseInfo `json:"drain"`
	PartsPlateau bool      `json:"parts_plateau"`
}

// Flags surfaces every caveat a reader must see before trusting the numbers.
type Flags struct {
	Truncated               bool   `json:"truncated,omitempty"`                 // SIGINT shortened the measure window
	Saturated               bool   `json:"saturated,omitempty"`                 // ingest fell >5% behind target
	ShapeMismatch           bool   `json:"shape_mismatch,omitempty"`            // detected shape != pricing profile shape
	MergeCPUEstimated       bool   `json:"merge_cpu_estimated,omitempty"`       // part_log CPU fell back to a proxy
	AsyncAttributionPartial bool   `json:"async_attribution_partial,omitempty"` // async flush correlation incomplete
	LogFlush                string `json:"log_flush"`                           // "cluster" | "local-only"
	CPUSource               string `json:"cpu_source"`                          // "os_cpu" | "real_time"
	MixNotes                string `json:"mix_notes,omitempty"`                 // placeholder-weights caveat
	ForeignDatabases        int    `json:"foreign_databases,omitempty"`         // shared-service contamination warning
}

// Reconciliation is the model-vs-billed comparison from a Cloud usage export.
type Reconciliation struct {
	BilledComputeUnitHours float64 `json:"billed_compute_unit_hours"`
	ModelComputeUnitHours  float64 `json:"model_compute_unit_hours"`
	DeltaPct               float64 `json:"delta_pct"`
	BilledStorageTB        float64 `json:"billed_storage_tb"`
	ModelStorageTB         float64 `json:"model_storage_tb"`
	StorageDeltaPct        float64 `json:"storage_delta_pct"`
	Flagged                bool    `json:"flagged"` // |delta| > 20%
	// Granularity is the coarsest record duration in the export. When it is
	// much coarser than the measure window (daily statement vs 1h window) the
	// pro-rated comparison is indicative only.
	Granularity string `json:"granularity,omitempty"`
}

// Result is one cogs run's self-describing record (kind cogs/v1). The full
// cell manifest and pricing profile are embedded, same principle as
// harness_commit: a result file must be readable in isolation.
type Result struct {
	Kind          string                    `json:"kind"` // "cogs/v1"
	RunID         string                    `json:"run_id"`
	Cell          Cell                      `json:"cell"`
	Scenario      string                    `json:"scenario"`
	HarnessCommit string                    `json:"harness_commit"`
	StartedAt     time.Time                 `json:"started_at"`
	FinishedAt    time.Time                 `json:"finished_at"`
	Cluster       runner.ClusterFingerprint `json:"cluster"`
	Pricing       pricing.Profile           `json:"pricing_profile"`

	Phases Phases         `json:"phases"`
	Ingest *ingest.Result `json:"ingest,omitempty"`
	Replay *replay.Result `json:"replay,omitempty"`

	Accounting  Accounting  `json:"accounting"`
	Attribution Attribution `json:"attribution"`
	Costs       Costs       `json:"costs"`

	Reconciliation *Reconciliation `json:"reconciliation"`
	Flags          Flags           `json:"flags"`
	Errors         []string        `json:"errors"`
}

// Write writes the JSON result under <resultsRoot>/<scenario>/cogs/, matching
// the perf path's timestamp naming.
func Write(resultsRoot string, r Result) (string, error) {
	dir := filepath.Join(resultsRoot, r.Scenario, "cogs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, r.StartedAt.UTC().Format("2006-01-02T15-04-05Z")+".json")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return "", err
	}
	return path, nil
}

// WriteReport renders and writes the sibling markdown report.
func WriteReport(resultsRoot string, r Result) (string, error) {
	dir := filepath.Join(resultsRoot, r.Scenario, "cogs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, r.StartedAt.UTC().Format("2006-01-02T15-04-05Z")+".md")
	if err := os.WriteFile(path, []byte(RenderMarkdown(r)), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func sortedClasses(m ClassCache) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cacheStates(byCache map[string]float64) []string {
	out := make([]string, 0, len(byCache))
	for k := range byCache {
		out = append(out, k)
	}
	sort.Strings(out) // "cold" < "warm"
	return out
}

func usd(v float64) string {
	switch {
	case v == 0:
		return "$0"
	case v < 0.01:
		return fmt.Sprintf("$%.6f", v)
	default:
		return fmt.Sprintf("$%.4f", v)
	}
}

func flagList(f Flags, plateau bool) string {
	var fs []string
	if f.Truncated {
		fs = append(fs, "TRUNCATED (SIGINT mid-measure)")
	}
	if f.Saturated {
		fs = append(fs, "SATURATED (ingest >5% behind target)")
	}
	if !plateau {
		fs = append(fs, "NO PARTS PLATEAU (soak ended unstable)")
	}
	if f.ShapeMismatch {
		fs = append(fs, "SHAPE MISMATCH (detected != pricing profile)")
	}
	if f.MergeCPUEstimated {
		fs = append(fs, "merge CPU estimated")
	}
	if f.AsyncAttributionPartial {
		fs = append(fs, "async insert attribution partial")
	}
	if f.ForeignDatabases > 0 {
		fs = append(fs, fmt.Sprintf("%d foreign database(s) on service", f.ForeignDatabases))
	}
	if len(fs) == 0 {
		return "none"
	}
	return strings.Join(fs, "; ")
}

// RenderMarkdown renders the human-readable report: header with caveats, the
// unit-cost card as the lead table, attribution split, per-class costs,
// storage, and reconciliation when present.
func RenderMarkdown(r Result) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("# COGS run: %s on %s\n\n", r.Cell.Name, r.Scenario)
	w("| | |\n|---|---|\n")
	w("| run id | `%s` |\n", r.RunID)
	w("| harness | `%s` |\n", r.HarnessCommit)
	w("| started | %s |\n", r.StartedAt.UTC().Format(time.RFC3339))
	w("| cluster | %s |\n", r.Cluster.Version)
	w("| pricing profile | %s (as of %s) |\n", r.Pricing.Name, r.Pricing.AsOf)
	w("| service shape | %d replicas x %.0f GiB / %.0f vCPU = %.1f compute units |\n",
		r.Pricing.Service.Replicas, r.Pricing.Service.GiBPerReplica, r.Pricing.Service.VCPUsPerReplica,
		r.Pricing.Service.ComputeUnits())
	w("| detected capacity | %d replicas x %.1f vCPU (%s) |\n",
		r.Accounting.Capacity.Replicas, r.Accounting.Capacity.VCPUsPerReplica, r.Accounting.Capacity.Source)
	w("| phases | soak %.0fs / measure %.0fs / drain %.0fs%s |\n",
		r.Phases.Soak.Seconds, r.Phases.Measure.Seconds, r.Phases.Drain.Seconds,
		map[bool]string{true: " (profile: " + r.Phases.Profile + ")", false: ""}[r.Phases.Profile != ""])
	w("| coverage | %.1f%% of available CPU attributed (source: %s, log flush: %s) |\n",
		r.Attribution.Coverage*100, r.Flags.CPUSource, r.Flags.LogFlush)
	w("| flags | %s |\n", flagList(r.Flags, r.Phases.PartsPlateau))
	if r.Flags.MixNotes != "" {
		w("\n> **Mix caveat:** %s\n", r.Flags.MixNotes)
	}

	w("\n## Unit costs\n\n")
	w("| metric | billed-shape | cpu-linear |\n|---|---|---|\n")
	if r.Accounting.EventsIngested > 0 {
		w("| $ / 1M events ingested | %s (insert %.0f%% / merge %.0f%%) | %s |\n",
			usd(r.Costs.Unit.USDPer1MEvents.BilledShape),
			r.Costs.Unit.USDPer1MEvents.InsertShare*100, r.Costs.Unit.USDPer1MEvents.MergeShare*100,
			usd(r.Costs.Unit.USDPer1MEvents.CPULinear))
	}
	classes := make([]string, 0, len(r.Costs.Unit.USDPer1KQueries))
	for c := range r.Costs.Unit.USDPer1KQueries {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	for _, class := range classes {
		byCache := r.Costs.Unit.USDPer1KQueries[class]
		caches := make([]string, 0, len(byCache))
		for c := range byCache {
			caches = append(caches, c)
		}
		sort.Strings(caches)
		for _, cache := range caches {
			mp := byCache[cache]
			w("| $ / 1k queries: %s (%s) | %s | %s |\n", class, cache, usd(mp.BilledShape), usd(mp.CPULinear))
		}
	}
	if r.Accounting.EventsIngested > 0 {
		w("| storage $ / 1M events / month (incl. backup x%.1f, estimate) | %s | |\n",
			r.Pricing.Rates.BackupMultiplier, usd(r.Costs.Unit.USDPer1MEventsMonthStorage))
	}
	w("| egress estimate (result bytes) | %s | |\n", usd(r.Costs.Unit.EgressUSDEstimate))
	w("| idle floor $ / service / month (100%%-active bound) | %s | |\n", usd(r.Costs.Unit.IdleFloorUSDPerServiceMonth))

	w("\n## CPU attribution\n\n")
	w("| component | cpu sec | share of available | billed | cpu-linear |\n|---|---|---|---|---|\n")
	share := func(sec float64) string {
		if r.Attribution.AvailableCPUSec == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", sec/r.Attribution.AvailableCPUSec*100)
	}
	w("| insert | %.1f | %s | %s | %s |\n", r.Attribution.InsertCPUSec, share(r.Attribution.InsertCPUSec),
		usd(r.Costs.BilledShape.InsertUSD), usd(r.Costs.CPULinear.InsertUSD))
	w("| merge | %.1f | %s | %s | %s |\n", r.Attribution.MergeCPUSec, share(r.Attribution.MergeCPUSec),
		usd(r.Costs.BilledShape.MergeUSD), usd(r.Costs.CPULinear.MergeUSD))
	for _, class := range sortedClasses(r.Attribution.QueryCPUSec) {
		for _, cache := range cacheStates(r.Attribution.QueryCPUSec[class]) {
			sec := r.Attribution.QueryCPUSec[class][cache]
			w("| query: %s (%s) | %.1f | %s | %s | %s |\n", class, cache, sec, share(sec),
				usd(r.Costs.BilledShape.QueryUSD[class][cache]), usd(r.Costs.CPULinear.QueryUSD[class][cache]))
		}
	}
	w("| idle residual | %.1f | %s | %s | |\n", r.Attribution.IdleCPUSec, share(r.Attribution.IdleCPUSec),
		usd(r.Costs.BilledShape.IdleFloorUSD))
	w("| **window total** | %.1f available | 100%% | %s | |\n", r.Attribution.AvailableCPUSec,
		usd(r.Costs.BilledShape.WindowUSD))

	if r.Ingest != nil || r.Replay != nil {
		w("\n## Workload\n\n| | target | achieved |\n|---|---|---|\n")
		if r.Ingest != nil {
			w("| ingest events/sec | %d | %.0f (satisfied: %v, %d batches, %d errors, insert p50 %.1fms p95 %.1fms) |\n",
				r.Ingest.TargetEPS, r.Ingest.AchievedEPS, r.Ingest.RateSatisfied, r.Ingest.Batches, r.Ingest.Errors,
				r.Ingest.InsertP50Ms, r.Ingest.InsertP95Ms)
		}
		if r.Replay != nil {
			w("| query qps | %.2f | %.2f (queued p50 %.1fms p95 %.1fms, %d errors) |\n",
				r.Replay.TargetQPS, r.Replay.AchievedQPS, r.Replay.QueuedP50Ms, r.Replay.QueuedP95Ms, r.Replay.Errors)
		}
	}

	w("\n## Storage\n\n| snapshot | rows | parts | partitions | compressed |\n|---|---|---|---|---|\n")
	for _, s := range []struct {
		name string
		snap accounting.StorageSnapshot
	}{
		{"prepare", r.Accounting.Storage.Prepare},
		{"soak end", r.Accounting.Storage.SoakEnd},
		{"drain end", r.Accounting.Storage.DrainEnd},
	} {
		w("| %s | %d | %d | %d | %s |\n", s.name, s.snap.Rows, s.snap.Parts, s.snap.Partitions, humanBytes(s.snap.CompressedBytes))
	}
	if r.Accounting.EventsIngested > 0 {
		w("\nSettled bytes/event over the run: **%.1f** (%d events ingested).\n",
			r.Attribution.BytesPerEventSettled, r.Accounting.EventsIngested)
	}

	if r.Reconciliation != nil {
		rec := r.Reconciliation
		w("\n## Reconciliation (Cloud usage export)\n\n")
		w("| | billed | model | delta |\n|---|---|---|---|\n")
		w("| compute unit-hours | %.3f | %.3f | %.1f%% |\n", rec.BilledComputeUnitHours, rec.ModelComputeUnitHours, rec.DeltaPct)
		w("| storage TB | %.6f | %.6f | %.1f%% |\n", rec.BilledStorageTB, rec.ModelStorageTB, rec.StorageDeltaPct)
		if rec.Granularity != "" {
			w("\nExport record granularity: %s. When records are much coarser than the measure window (daily statement vs a 1h window), pro-rating assumes uniform usage — treat the comparison as indicative.\n", rec.Granularity)
		}
		if rec.Flagged {
			w("\n> **FLAGGED:** model-vs-billed delta exceeds 20%%. See the methodology README for known causes.\n")
		}
	}

	if len(r.Errors) > 0 {
		w("\n## Errors\n\n")
		for _, e := range r.Errors {
			w("- %s\n", e)
		}
	}
	return b.String()
}

func humanBytes(b uint64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
