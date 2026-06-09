package cogs

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openmeterio/ch-playground/bench/ingest"
	"github.com/openmeterio/ch-playground/bench/replay"
	"github.com/openmeterio/ch-playground/bench/runner"
)

var update = flag.Bool("update", false, "rewrite golden files")

// fixtureResult builds a fully-populated deterministic Result from the
// attribution fixture.
func fixtureResult() Result {
	acc := fixtureAccounting()
	attr := Attribute(acc)
	costs := Price(fixtureProfile(), acc, attr)

	start := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	return Result{
		Kind:          "cogs/v1",
		RunID:         "01JX0000000000000000000000",
		Scenario:      "proposal",
		HarnessCommit: "abc1234",
		StartedAt:     start,
		FinishedAt:    start.Add(105 * time.Minute),
		Cluster:       runner.ClusterFingerprint{Version: "25.12.1.1606", IsSingleNode: false},
		Pricing:       fixtureProfile(),
		Cell: Cell{
			Name: "mixed-5keps-4qps", Scenario: "proposal", PreloadRows: 10_000_000,
			Soak: Duration(30 * time.Minute), Measure: Duration(time.Hour), Drain: Duration(15 * time.Minute),
			Ingest: IngestSpec{EventsPerSec: 5000, BatchMaxRows: 5000, FlushInterval: Duration(time.Second), Namespaces: 32, MixedValue: true, Seed: 42},
			Query:  QuerySpec{QPS: 4, Arrival: "poisson", Mix: "production", ColdFraction: 0.1, ConcurrencyCap: 16},
			PricingProfile: "fixture",
		},
		Phases: Phases{
			Soak:         PhaseInfo{Start: start, End: start.Add(30 * time.Minute), Seconds: 1800},
			Measure:      PhaseInfo{Start: start.Add(30 * time.Minute), End: start.Add(90 * time.Minute), Seconds: 3600},
			Drain:        PhaseInfo{Start: start.Add(90 * time.Minute), End: start.Add(105 * time.Minute), Seconds: 900},
			PartsPlateau: true,
		},
		Ingest: &ingest.Result{TargetEPS: 5000, AchievedEPS: 4987, RateSatisfied: true, Events: 18_000_000, Batches: 3600, InsertP50Ms: 12.5, InsertP95Ms: 30.1},
		Replay: &replay.Result{TargetQPS: 4, AchievedQPS: 3.98, QueuedP50Ms: 0.2, QueuedP95Ms: 1.4,
			PerClass: map[string]*replay.ClassStats{"meter_agg": {Issued: 13_000, Completed: 13_000, Cold: 1000}, "key_only": {Issued: 2000, Completed: 2000}}},
		Accounting:  acc,
		Attribution: attr,
		Costs:       costs,
		Flags: Flags{
			LogFlush:  "cluster",
			CPUSource: "os_cpu",
			MixNotes:  "class weights are placeholders; replace with measured production frequencies",
		},
		Errors: []string{},
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file (run: go test ./bench/cogs/ -run %s -update): %v", t.Name(), err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s diverged from golden; run with -update after verifying the change.\n--- got ---\n%s", name, got)
	}
}

func TestResultJSONGolden(t *testing.T) {
	r := fixtureResult()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "result.golden.json", append(b, '\n'))

	// The self-describing contract: kind, embedded cell + profile.
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["kind"] != "cogs/v1" {
		t.Fatalf("kind = %v", back["kind"])
	}
	if _, ok := back["cell"].(map[string]any)["ingest"]; !ok {
		t.Fatal("cell manifest must be embedded in full")
	}
	if back["pricing_profile"].(map[string]any)["name"] != "fixture" {
		t.Fatal("pricing profile must be embedded in full")
	}
}

func TestMarkdownGolden(t *testing.T) {
	md := RenderMarkdown(fixtureResult())
	golden(t, "report.golden.md", []byte(md))

	for _, want := range []string{
		"## Unit costs",
		"$ / 1M events ingested",
		"$ / 1k queries: meter_agg (cold)",
		"$ / 1k queries: meter_agg (warm)",
		"idle floor",
		"## CPU attribution",
		"Mix caveat",
		"Settled bytes/event over the run: **66.0**",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

func TestMarkdownFlagsRendered(t *testing.T) {
	r := fixtureResult()
	r.Flags.Truncated = true
	r.Flags.Saturated = true
	r.Flags.ShapeMismatch = true
	r.Phases.PartsPlateau = false
	md := RenderMarkdown(r)
	for _, want := range []string{"TRUNCATED", "SATURATED", "NO PARTS PLATEAU", "SHAPE MISMATCH"} {
		if !strings.Contains(md, want) {
			t.Errorf("flag %q not rendered", want)
		}
	}
}

func TestMarkdownReconciliation(t *testing.T) {
	r := fixtureResult()
	r.Reconciliation = &Reconciliation{
		BilledComputeUnitHours: 4.2, ModelComputeUnitHours: 4.0, DeltaPct: -4.8,
		BilledStorageTB: 0.002, ModelStorageTB: 0.0021, StorageDeltaPct: 5.0,
	}
	md := RenderMarkdown(r)
	if !strings.Contains(md, "## Reconciliation") || strings.Contains(md, "FLAGGED") {
		t.Fatal("unflagged reconciliation must render without the flag banner")
	}
	r.Reconciliation.Flagged = true
	if !strings.Contains(RenderMarkdown(r), "FLAGGED") {
		t.Fatal("flagged reconciliation must render the banner")
	}
}

func TestWriteFilesUnderScenarioCogsDir(t *testing.T) {
	root := t.TempDir()
	r := fixtureResult()
	jsonPath, err := Write(root, r)
	if err != nil {
		t.Fatal(err)
	}
	mdPath, err := WriteReport(root, r)
	if err != nil {
		t.Fatal(err)
	}
	wantDir := filepath.Join(root, "proposal", "cogs")
	if filepath.Dir(jsonPath) != wantDir || filepath.Dir(mdPath) != wantDir {
		t.Fatalf("results must land under <root>/<scenario>/cogs/: %s, %s", jsonPath, mdPath)
	}
	if filepath.Base(jsonPath) != "2026-06-09T12-00-00Z.json" {
		t.Fatalf("timestamp naming must match the perf path: %s", jsonPath)
	}
}
