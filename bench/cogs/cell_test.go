package cogs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCell(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cell.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const validMixedCell = `{
  "name": "mixed-5keps-4qps",
  "scenario": "proposal",
  "preload_rows": 10000000,
  "soak": "30m", "measure": "60m", "drain": "15m",
  "ingest": {
    "events_per_sec": 5000, "batch_max_rows": 5000, "flush_interval": "1s",
    "namespaces": 32, "mixed_value": true, "seed": 42
  },
  "query": {
    "qps": 4.0, "arrival": "poisson", "mix": "production",
    "cold_fraction": 0.1, "concurrency_cap": 16, "settings": {"max_threads": 4}
  },
  "pricing_profile": "clickhouse-cloud-scale-aws-us-east-1"
}`

func TestLoadCellValid(t *testing.T) {
	c, err := LoadCell(writeCell(t, validMixedCell))
	if err != nil {
		t.Fatal(err)
	}
	if c.Soak.D() != 30*time.Minute || c.Measure.D() != time.Hour || c.Drain.D() != 15*time.Minute {
		t.Fatalf("durations parsed wrong: %v %v %v", c.Soak.D(), c.Measure.D(), c.Drain.D())
	}
	if c.Ingest.EventsPerSec != 5000 || c.Query.QPS != 4.0 {
		t.Fatalf("rates parsed wrong: %d eps, %v qps", c.Ingest.EventsPerSec, c.Query.QPS)
	}
}

func TestLoadCellRejectsUnknownField(t *testing.T) {
	body := strings.Replace(validMixedCell, `"preload_rows"`, `"preload_rowz"`, 1)
	_, err := LoadCell(writeCell(t, body))
	if err == nil || !strings.Contains(err.Error(), "preload_rowz") {
		t.Fatalf("unknown field must be rejected by name, got: %v", err)
	}
}

func TestLoadCellIdleIsLegal(t *testing.T) {
	const idle = `{
	  "name": "idle", "scenario": "proposal", "preload_rows": 0,
	  "soak": "0s", "measure": "60m", "drain": "0s",
	  "ingest": {"events_per_sec": 0},
	  "query": {"qps": 0},
	  "pricing_profile": "local-zero"
	}`
	c, err := LoadCell(writeCell(t, idle))
	if err != nil {
		t.Fatalf("idle cell (eps=0 && qps=0) must be legal: %v", err)
	}
	if c.Ingest.EventsPerSec != 0 || c.Query.QPS != 0 {
		t.Fatal("idle cell rates must be zero")
	}
}

func TestValidateCatchesInvalidCells(t *testing.T) {
	cases := map[string]struct {
		mutate  func(*Cell)
		wantErr string
	}{
		"missing scenario":      {func(c *Cell) { c.Scenario = "" }, "scenario is required"},
		"missing profile":       {func(c *Cell) { c.PricingProfile = "" }, "pricing_profile is required"},
		"zero measure":          {func(c *Cell) { c.Measure = 0 }, "measure duration"},
		"ingest without batch":  {func(c *Cell) { c.Ingest.BatchMaxRows = 0 }, "batch_max_rows"},
		"ingest without flush":  {func(c *Cell) { c.Ingest.FlushInterval = 0 }, "flush_interval"},
		"preload without seed":  {func(c *Cell) { c.Ingest.Seed = 0 }, "ingest.seed"},
		"bad arrival":           {func(c *Cell) { c.Query.Arrival = "burst" }, "arrival"},
		"missing mix":           {func(c *Cell) { c.Query.Mix = "" }, "query.mix"},
		"cold fraction over 1":  {func(c *Cell) { c.Query.ColdFraction = 1.5 }, "cold_fraction"},
		"zero concurrency cap":  {func(c *Cell) { c.Query.ConcurrencyCap = 0 }, "concurrency_cap"},
		"negative preload rows": {func(c *Cell) { c.PreloadRows = -1 }, "preload_rows"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := LoadCell(writeCell(t, validMixedCell))
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&c)
			err = c.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestApplyProfile(t *testing.T) {
	c, err := LoadCell(writeCell(t, validMixedCell))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyProfile("ci"); err != nil {
		t.Fatal(err)
	}
	if c.Soak.D() != 2*time.Minute || c.Measure.D() != 3*time.Minute || c.Drain.D() != time.Minute {
		t.Fatalf("ci profile durations wrong: %v %v %v", c.Soak.D(), c.Measure.D(), c.Drain.D())
	}
	if err := c.ApplyProfile("nightly"); err == nil {
		t.Fatal("unknown profile must be rejected")
	}
	d, _ := LoadCell(writeCell(t, validMixedCell))
	if err := d.ApplyProfile(""); err != nil || d.Measure.D() != time.Hour {
		t.Fatalf("empty profile must keep manifest durations: %v %v", err, d.Measure.D())
	}
}

func TestDurationRoundTrip(t *testing.T) {
	d := Duration(90 * time.Second)
	b, err := d.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Duration
	if err := back.UnmarshalJSON(b); err != nil {
		t.Fatal(err)
	}
	if back != d {
		t.Fatalf("round trip: %v != %v", back, d)
	}
	var bad Duration
	if err := bad.UnmarshalJSON([]byte(`60`)); err == nil {
		t.Fatal("bare numbers must be rejected (ambiguous unit)")
	}
}
