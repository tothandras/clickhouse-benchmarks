package cogs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openmeterio/ch-playground/bench/accounting"
)

// variantResult derives a second fixture run with scaled costs and an extra
// query class, to exercise deltas and class-set reporting.
func variantResult() Result {
	r := fixtureResult()
	r.RunID = "01JX1111111111111111111111"
	r.Scenario = "baseline-openmeter"
	r.Cell.Scenario = "baseline-openmeter"
	r.StartedAt = r.StartedAt.Add(time.Hour)
	// Drop key_only (class-set diff), inflate meter_agg CPU 2x, insert +22%.
	acc := fixtureAccounting()
	acc.QueryLog.Groups = []accounting.QueryGroup{
		{Component: "ingest", N: 3600, CPUSec: 1100, WrittenRows: 18_000_000},
		{Component: "query", Class: "meter_agg", Cache: "warm", N: 12_000, CPUSec: 4800, ResultBytes: 1_000_000_000},
		{Component: "query", Class: "meter_agg", Cache: "cold", N: 1_000, CPUSec: 1200, ResultBytes: 200_000_000},
	}
	r.Accounting = acc
	r.Attribution = Attribute(acc)
	r.Costs = Price(fixtureProfile(), acc, r.Attribution)
	return r
}

func TestCompareSameProfile(t *testing.T) {
	a := fixtureResult()
	b := variantResult()
	out, err := Compare(a, b, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Class sets differ: only in A: [key_only]",
		"insert cpu sec",
		"+22.2%", // 900 -> 1100
		"query cpu sec: meter_agg (warm)",
		"+100.0%",
		"## Unit costs",
		"$/1k queries: meter_agg (warm)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compare output missing %q\n%s", want, out)
		}
	}
}

func TestCompareProfileMismatchGuard(t *testing.T) {
	a := fixtureResult()
	b := variantResult()
	b.Pricing.Name = "other-region"
	if _, err := Compare(a, b, false); err == nil || !strings.Contains(err.Error(), "allow-profile-mismatch") {
		t.Fatalf("cross-profile compare must be refused with guidance, got: %v", err)
	}
	out, err := Compare(a, b, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "prices omitted") || strings.Contains(out, "## Unit costs") {
		t.Fatalf("override must compare resource lines only:\n%s", out)
	}
	if !strings.Contains(out, "insert cpu sec") {
		t.Fatal("resource lines must still be compared")
	}
}

func TestLoadResultScenarioShorthand(t *testing.T) {
	root := t.TempDir()
	r1 := fixtureResult()
	r2 := fixtureResult()
	r2.RunID = "01JX2222222222222222222222"
	r2.StartedAt = r1.StartedAt.Add(2 * time.Hour)
	for _, r := range []Result{r1, r2} {
		if _, err := Write(root, r); err != nil {
			t.Fatal(err)
		}
	}
	got, path, err := LoadResult(root, "proposal")
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != r2.RunID {
		t.Fatalf("shorthand must resolve to the LATEST run, got %s from %s", got.RunID, path)
	}
	byPath, _, err := LoadResult(root, filepath.Join(root, "proposal", "cogs", r1.StartedAt.UTC().Format("2006-01-02T15-04-05Z")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if byPath.RunID != r1.RunID {
		t.Fatal("explicit path must load that exact run")
	}
}

func ingestOnlyResult() Result {
	r := fixtureResult()
	r.Cell.Name = "ingest-5k"
	r.Cell.Query.QPS = 0
	r.Replay = nil
	acc := fixtureAccounting()
	acc.QueryLog.Groups = []accounting.QueryGroup{{Component: "ingest", N: 3600, CPUSec: 900}}
	r.Accounting = acc
	r.Attribution = Attribute(acc)
	return r
}

func queryOnlyResult() Result {
	r := fixtureResult()
	r.Cell.Name = "query-4qps"
	r.Cell.Ingest.EventsPerSec = 0
	r.Ingest = nil
	acc := fixtureAccounting()
	acc.EventsIngested = 0
	acc.Merges = accounting.MergeStats{}
	acc.QueryLog.Groups = []accounting.QueryGroup{
		{Component: "query", Class: "meter_agg", Cache: "warm", N: 12_000, CPUSec: 2400},
		{Component: "query", Class: "meter_agg", Cache: "cold", N: 1_000, CPUSec: 600},
		{Component: "query", Class: "key_only", Cache: "warm", N: 2_000, CPUSec: 100},
	}
	r.Accounting = acc
	r.Attribution = Attribute(acc)
	return r
}

func TestValidateAdditivityPass(t *testing.T) {
	// Mixed fixture = ingest 900 + merge 300 + query 3100, identical rates to
	// the components: perfectly additive.
	out, err := ValidateAdditivity(ingestOnlyResult(), queryOnlyResult(), fixtureResult())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PASS: cpu-linear COGS composes") {
		t.Fatalf("expected PASS:\n%s", out)
	}
	// The ingest-only fixture has no merge CPU; mixed does — that line alone
	// is a 100% residual and must FAIL... unless we built it additive. The
	// fixture sets merge only in mixed, so check what the table says:
	if !strings.Contains(out, "| merge |") {
		t.Fatalf("merge line missing:\n%s", out)
	}
}

func TestValidateAdditivityFailNamesComponent(t *testing.T) {
	mixed := fixtureResult()
	// Inflate the mixed run's query CPU 2x: interference.
	acc := mixed.Accounting
	for i := range acc.QueryLog.Groups {
		if acc.QueryLog.Groups[i].Component == "query" {
			acc.QueryLog.Groups[i].CPUSec *= 2
		}
	}
	mixed.Attribution = Attribute(acc)
	out, err := ValidateAdditivity(ingestOnlyResult(), queryOnlyResult(), mixed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "FAIL") || !strings.Contains(out, "query") {
		t.Fatalf("inflated query CPU must FAIL naming the query component:\n%s", out)
	}
}

func TestValidateAdditivityRejectsWrongShapes(t *testing.T) {
	if _, err := ValidateAdditivity(queryOnlyResult(), queryOnlyResult(), fixtureResult()); err == nil {
		t.Fatal("first run must be required to be ingest-only")
	}
	if _, err := ValidateAdditivity(ingestOnlyResult(), ingestOnlyResult(), fixtureResult()); err == nil {
		t.Fatal("second run must be required to be query-only")
	}
	other := fixtureResult()
	other.Scenario = "baseline-openmeter"
	if _, err := ValidateAdditivity(ingestOnlyResult(), queryOnlyResult(), other); err == nil {
		t.Fatal("cross-scenario validation must be rejected")
	}
}

func TestReconcileFixture(t *testing.T) {
	dir := t.TempDir()
	export := filepath.Join(dir, "usage.json")
	// Measure window 12:30-13:30 (3600s); the export bills 13:00-14:00 at 4
	// unit-hours -> overlap 30min -> 2.0 billed unit-hours in window... plus
	// 12:00-13:00 at 4 -> another 2.0. Total billed = 4.0; model = 4 units x 1h = 4.0.
	body := `{
	  "version": "cogs-usage/v1",
	  "service_id": "test",
	  "records": [
	    {"from": "2026-06-09T12:00:00Z", "to": "2026-06-09T13:00:00Z", "compute_unit_hours": 4.0, "storage_tb": 0.002},
	    {"from": "2026-06-09T13:00:00Z", "to": "2026-06-09T14:00:00Z", "compute_unit_hours": 4.0, "storage_tb": 0.002}
	  ]
	}`
	if err := os.WriteFile(export, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	measure := PhaseInfo{
		Start:   time.Date(2026, 6, 9, 12, 30, 0, 0, time.UTC),
		End:     time.Date(2026, 6, 9, 13, 30, 0, 0, time.UTC),
		Seconds: 3600,
	}
	rec, err := ReconcileFile(export, fixtureProfile(), measure, 0.002)
	if err != nil {
		t.Fatal(err)
	}
	if rec.BilledComputeUnitHours != 4.0 || rec.ModelComputeUnitHours != 4.0 {
		t.Fatalf("billed/model unit-hours: %+v", rec)
	}
	if rec.DeltaPct != 0 || rec.Flagged {
		t.Fatalf("perfect match must not flag: %+v", rec)
	}

	// A model 30% above billed must flag.
	rec2, err := ReconcileFile(export, fixtureProfile(), PhaseInfo{
		Start: measure.Start, End: measure.End, Seconds: 3600 * 1.3,
	}, 0.002)
	if err != nil {
		t.Fatal(err)
	}
	if !rec2.Flagged {
		t.Fatalf("30%% compute delta must flag: %+v", rec2)
	}

	// Unknown version fails loudly.
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"version":"cloud-export/v7","records":[]}`), 0o644)
	if _, err := ReconcileFile(bad, fixtureProfile(), measure, 0); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("unknown export version must fail with the versioned-parser error, got: %v", err)
	}
}

// TestReconcileStatementCSV exercises the real Cloud usage-statement format
// (header captured from a 2026-06 export): daily dollar rows, converted to
// unit-hours via the profile rate.
func TestReconcileStatementCSV(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "usage-statement.csv")
	const body = `Date,Organization Plan,Entity Name,Entity Type,Warehouse ID,Service ID,ClickPipe ID,CSP,Region,ClickPipe Data Transfer ($),ClickPipe Compute ($),ClickPipe Initial Load and Resyncs ($),Service Compute ($),Service Data Transfer Public Internet ($),Service Data Transfer Inter-Region Tier 1 ($),Service Data Transfer Inter-Region Tier 2 ($),Service Data Transfer Inter-Region Tier 3 ($),Service Data Transfer Inter-Region Tier 4 ($),Warehouse Storage ($),Warehouse Backups ($),Total ($)
2026-06-09,SCALE,test-service,service,00000000-0000-0000-0000-000000000001,00000000-0000-0000-0000-000000000002,,aws,eu-central-1,,,,28.656,,,,,,,,28.656
`
	if err := os.WriteFile(csvPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Measure window: 1h inside the day. The day billed $28.656 at the
	// fixture rate 0.2985 -> 96 unit-hours for the day -> pro-rated 1/24 = 4
	// unit-hours in the window. Model: 4 units x 1h = 4. Perfect match.
	measure := PhaseInfo{
		Start:   time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 6, 9, 13, 0, 0, 0, time.UTC),
		Seconds: 3600,
	}
	rec, err := ReconcileFile(csvPath, fixtureProfile(), measure, 0.001)
	if err != nil {
		t.Fatal(err)
	}
	near(t, "billed unit-hours", rec.BilledComputeUnitHours, 4.0)
	near(t, "model unit-hours", rec.ModelComputeUnitHours, 4.0)
	if rec.Flagged {
		t.Fatalf("perfect pro-rated match must not flag: %+v", rec)
	}
	if rec.Granularity != "24h0m0s" {
		t.Fatalf("daily granularity must be surfaced, got %q", rec.Granularity)
	}
	// Statement CSVs carry no storage TB: compute-only, storage unflagged.
	if rec.StorageDeltaPct != 0 {
		t.Fatalf("CSV reconciliation must skip storage, got %+v", rec)
	}

	// A renamed compute column = format drift = loud failure.
	drift := filepath.Join(dir, "drift.csv")
	os.WriteFile(drift, []byte(strings.Replace(body, "Service Compute ($)", "Compute ($)", 1)), 0o644)
	if _, err := ReconcileFile(drift, fixtureProfile(), measure, 0); err == nil || !strings.Contains(err.Error(), "Service Compute") {
		t.Fatalf("statement drift must fail naming the missing column, got: %v", err)
	}
}
