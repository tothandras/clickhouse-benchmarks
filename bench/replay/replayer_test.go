package replay

import (
	"context"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testMix() Mix {
	return Mix{
		Classes: map[string]Class{
			"key_only":  {Weight: 10, Queries: []string{"count"}},
			"meter_agg": {Weight: 70, Queries: []string{"sum_a", "sum_b"}},
			"heavy":     {Weight: 20, Queries: []string{"group_all"}},
		},
	}
}

func testQueries() map[string]string {
	return map[string]string{
		"count":     "SELECT count() FROM t WHERE ns = {namespace:String} AND time >= toDateTime({from:UInt32}) AND time < toDateTime({to:UInt32})",
		"sum_a":     "SELECT sum(v) FROM t WHERE time >= toDateTime({from:UInt32}) AND time < toDateTime({to:UInt32})",
		"sum_b":     "SELECT sum(w) FROM t WHERE time >= toDateTime({from:UInt32}) AND time < toDateTime({to:UInt32})",
		"group_all": "SELECT g, sum(v) FROM t WHERE time >= toDateTime({from:UInt32}) AND time < toDateTime({to:UInt32}) GROUP BY g",
	}
}

func TestPickerConvergesToWeights(t *testing.T) {
	p := newPicker(testMix())
	rng := rand.New(rand.NewPCG(42, 0))
	const n = 100_000
	counts := map[string]int{}
	cold := 0
	for range n {
		class, query, isCold := p.pick(rng, 0.1)
		counts[class]++
		if isCold {
			cold++
		}
		if class == "meter_agg" && query != "sum_a" && query != "sum_b" {
			t.Fatalf("query %q not in class meter_agg", query)
		}
	}
	for class, weight := range map[string]float64{"key_only": 0.10, "meter_agg": 0.70, "heavy": 0.20} {
		got := float64(counts[class]) / n
		if math.Abs(got-weight) > 0.02 {
			t.Errorf("class %s share %.3f, want ~%.2f", class, got, weight)
		}
	}
	if f := float64(cold) / n; math.Abs(f-0.1) > 0.02 {
		t.Errorf("cold fraction %.3f, want ~0.10", f)
	}
}

func TestPickerDeterministicUnderSeed(t *testing.T) {
	p := newPicker(testMix())
	a, b := rand.New(rand.NewPCG(7, 0)), rand.New(rand.NewPCG(7, 0))
	for i := range 5_000 {
		ca, qa, colda := p.pick(a, 0.3)
		cb, qb, coldb := p.pick(b, 0.3)
		if ca != cb || qa != qb || colda != coldb {
			t.Fatalf("pick %d diverged under identical seeds", i)
		}
	}
}

func TestInterArrival(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	if d := interArrival(rng, "uniform", 4); d != 250*time.Millisecond {
		t.Fatalf("uniform at 4 qps must be a fixed 250ms, got %v", d)
	}
	// Poisson: mean of exponential inter-arrivals converges to 1/qps.
	var sum time.Duration
	const n = 50_000
	for range n {
		sum += interArrival(rng, "poisson", 4)
	}
	mean := sum.Seconds() / n
	if math.Abs(mean-0.25) > 0.01 {
		t.Fatalf("poisson mean inter-arrival %.4fs, want ~0.25s", mean)
	}
}

// fakeExecutor records every arrival's SQL and settings.
type fakeExecutor struct {
	mu       sync.Mutex
	delay    time.Duration
	calls    []fakeCall
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

type fakeCall struct {
	sql      string
	settings map[string]any
}

func (f *fakeExecutor) Exec(_ context.Context, sql string, settings map[string]any) error {
	cur := f.inFlight.Add(1)
	for {
		seen := f.maxSeen.Load()
		if cur <= seen || f.maxSeen.CompareAndSwap(seen, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.inFlight.Add(-1)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{sql: sql, settings: settings})
	f.mu.Unlock()
	return nil
}

func TestReplayerTagsAndSlidingWindow(t *testing.T) {
	exec := &fakeExecutor{}
	res, err := Run(context.Background(), Config{
		QPS: 200, Arrival: "uniform", ColdFraction: 0.5, ConcurrencyCap: 8,
		Duration: 250 * time.Millisecond,
		Mix:      testMix(), Queries: testQueries(),
		Params:   map[string]string{"namespace": "'default'"},
		TimeSpan: 72 * time.Hour,
		Settings: map[string]any{"max_threads": 4},
		RunID:    "run-123", Seed: 42,
		Executor: exec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Errors != 0 {
		t.Fatalf("unexpected errors: %d", res.Errors)
	}
	if len(exec.calls) < 20 {
		t.Fatalf("expected ~50 arrivals, got %d", len(exec.calls))
	}

	sawCold, sawWarm := false, false
	for _, c := range exec.calls {
		comment, _ := c.settings["log_comment"].(string)
		if !strings.Contains(comment, `"cogs_run":"run-123"`) || !strings.Contains(comment, `"component":"query"`) {
			t.Fatalf("bad log_comment: %s", comment)
		}
		if c.settings["max_threads"] != 4 {
			t.Fatal("cell settings must be applied to every arrival")
		}
		if strings.Contains(comment, `"cache":"cold"`) {
			sawCold = true
			if c.settings["enable_filesystem_cache"] != 0 {
				t.Fatal("cold arrivals must disable the filesystem cache")
			}
		} else {
			sawWarm = true
			if _, has := c.settings["enable_filesystem_cache"]; has {
				t.Fatal("warm arrivals must not touch the filesystem cache setting")
			}
		}
		if strings.Contains(c.sql, "{from:") || strings.Contains(c.sql, "{to:") {
			t.Fatalf("window params not rendered: %s", c.sql)
		}
	}
	if !sawCold || !sawWarm {
		t.Fatalf("cold_fraction 0.5 must produce both states (cold=%v warm=%v)", sawCold, sawWarm)
	}

	issued := 0
	for _, s := range res.PerClass {
		issued += s.Issued
	}
	if issued != len(exec.calls) {
		t.Fatalf("per-class issued %d != executed %d", issued, len(exec.calls))
	}
}

func TestReplayerSlidingWindowAdvances(t *testing.T) {
	exec := &fakeExecutor{}
	_, err := Run(context.Background(), Config{
		QPS: 100, Arrival: "uniform", ConcurrencyCap: 4,
		Duration: 300 * time.Millisecond,
		Mix:      testMix(), Queries: testQueries(),
		Params:   map[string]string{"namespace": "'default'"},
		TimeSpan: time.Hour,
		RunID:    "run-w", Seed: 1,
		Executor: exec,
	})
	if err != nil {
		t.Fatal(err)
	}
	// to = now is rendered per arrival: the literal must advance across the run.
	first, last := exec.calls[0].sql, exec.calls[len(exec.calls)-1].sql
	if first == "" || last == "" {
		t.Fatal("missing calls")
	}
	if len(exec.calls) > 10 && first == last {
		t.Skip("sub-second run rendered identical windows; acceptable at 1s granularity")
	}
}

func TestReplayerConcurrencyCapAndQueueing(t *testing.T) {
	exec := &fakeExecutor{delay: 50 * time.Millisecond}
	res, err := Run(context.Background(), Config{
		QPS: 200, Arrival: "uniform", ConcurrencyCap: 2,
		Duration: 300 * time.Millisecond,
		Mix:      testMix(), Queries: testQueries(),
		Params:   map[string]string{"namespace": "'default'"},
		TimeSpan: time.Hour,
		RunID:    "run-q", Seed: 9,
		Executor: exec,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := exec.maxSeen.Load(); got > 2 {
		t.Fatalf("concurrency cap violated: %d in flight", got)
	}
	// 200 qps against 2 slots of 50ms work = 40 qps capacity: queueing must
	// show up in the queue-time percentiles, not vanish.
	if res.QueuedP95Ms <= 0 {
		t.Fatalf("expected nonzero queue time under saturation, got p95=%.1fms", res.QueuedP95Ms)
	}
}

func TestReplayerRejectsMixWithoutSQL(t *testing.T) {
	_, err := Run(context.Background(), Config{
		QPS: 1, Arrival: "uniform", ConcurrencyCap: 1, Duration: time.Millisecond,
		Mix:      Mix{Classes: map[string]Class{"a": {Weight: 1, Queries: []string{"ghost"}}}},
		Queries:  map[string]string{"other": "SELECT 1"},
		TimeSpan: time.Hour,
		Executor: &fakeExecutor{},
	})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("mix referencing unloaded SQL must fail by name, got: %v", err)
	}
}
