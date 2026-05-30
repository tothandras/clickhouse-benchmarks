package main

import (
	"testing"

	"github.com/openmeterio/ch-playground/bench/runner"
)

func TestIndexQueriesPicksWarmLowestConcurrency(t *testing.T) {
	cpu := 100.0
	qs := []runner.BenchResult{
		{Name: "sum_hour", Concurrency: 8, CacheState: "warm", P50Sec: 0.030},
		{Name: "sum_hour", Concurrency: 1, CacheState: "warm", P50Sec: 0.018, CPUp50Us: &cpu},
		{Name: "sum_hour", Concurrency: 1, CacheState: "cold", P50Sec: 0.025}, // cold ignored
		{Name: "broken", Concurrency: 1, Error: "boom"},                       // errors ignored
	}
	got := indexQueries(qs)
	if len(got) != 1 {
		t.Fatalf("want 1 indexed query (warm, c=1), got %d: %v", len(got), got)
	}
	q, ok := got["sum_hour"]
	if !ok {
		t.Fatal("sum_hour missing")
	}
	if q.Concurrency != 1 || q.CacheState != "warm" {
		t.Errorf("picked wrong variant: c=%d cache=%s", q.Concurrency, q.CacheState)
	}
	if q.P50Sec != 0.018 {
		t.Errorf("picked wrong p50: %v", q.P50Sec)
	}
}

func TestPctDelta(t *testing.T) {
	cases := []struct {
		base, cand float64
		want       string
	}{
		{100, 59, "-41%"}, // the README's headline proposal p50 win
		{100, 130, "+30%"},
		{100, 100, "+0%"},
		{0, 50, "—"}, // undefined baseline
	}
	for _, c := range cases {
		if got := pctDelta(c.base, c.cand); got != c.want {
			t.Errorf("pctDelta(%v,%v) = %q, want %q", c.base, c.cand, got, c.want)
		}
	}
}

func TestCPUDeltaNilSafe(t *testing.T) {
	a := 200.0
	withCPU := runner.BenchResult{CPUp50Us: &a}
	noCPU := runner.BenchResult{}
	if got := cpuDelta(noCPU, withCPU); got != "n/a" {
		t.Errorf("cpuDelta with nil base = %q, want n/a", got)
	}
	b := 100.0
	if got := cpuDelta(runner.BenchResult{CPUp50Us: &a}, runner.BenchResult{CPUp50Us: &b}); got != "-50%" {
		t.Errorf("cpuDelta 200→100 = %q, want -50%%", got)
	}
}
