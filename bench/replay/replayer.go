package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/openmeterio/ch-playground/bench/runner"
)

// Executor runs one rendered query to completion (results drained, so the
// server does the full work query_log will account). The production
// implementation wraps the native client; tests use a fake.
type Executor interface {
	Exec(ctx context.Context, sql string, settings map[string]any) error
}

// Config controls one replayer run.
type Config struct {
	QPS            float64
	Arrival        string // "poisson" | "uniform"
	ColdFraction   float64
	ConcurrencyCap int
	Duration       time.Duration

	Mix     Mix
	Queries map[string]string // query name -> SQL template ({param:Type} placeholders)
	// Params are the base bindings (namespace, type, subjects, ...) WITHOUT
	// from/to: the replayer binds a sliding window per arrival — to = now,
	// from = now - TimeSpan, rendered as UTC Unix seconds — so scan size is
	// stationary while ingest writes fresh rows (production meter queries
	// read data as it lands).
	Params   map[string]string
	TimeSpan time.Duration

	Settings map[string]any // cell query settings, applied to every arrival
	RunID    string
	Seed     uint64 // RNG seed for arrivals/selection/cold draws

	Executor Executor

	// Test hooks; default to the real clock.
	Now   func() time.Time
	Sleep func(time.Duration)
}

// ClassStats counts one class's arrivals, split by cache state.
type ClassStats struct {
	Issued    int `json:"issued"`
	Completed int `json:"completed"`
	Errors    int `json:"errors"`
	Cold      int `json:"cold"`
}

// Result reports what the replayer achieved.
type Result struct {
	TargetQPS   float64                `json:"target_qps"`
	AchievedQPS float64                `json:"achieved_qps"`
	PerClass    map[string]*ClassStats `json:"per_class"`
	QueuedP50Ms float64                `json:"queued_p50_ms"`
	QueuedP95Ms float64                `json:"queued_p95_ms"`
	Errors      int                    `json:"errors"`
}

// picker makes the per-arrival random choices: class by weight, query uniform
// within class, cold with probability ColdFraction. Pure given its RNG, so
// selection is deterministic under a seeded RNG.
type picker struct {
	classes []string // sorted for deterministic iteration
	cum     []int
	total   int
	mix     Mix
}

func newPicker(m Mix) *picker {
	p := &picker{mix: m}
	p.classes = make([]string, 0, len(m.Classes))
	for name := range m.Classes {
		p.classes = append(p.classes, name)
	}
	sort.Strings(p.classes)
	p.cum = make([]int, len(p.classes))
	for i, name := range p.classes {
		p.total += m.Classes[name].Weight
		p.cum[i] = p.total
	}
	return p
}

func (p *picker) pick(rng *rand.Rand, coldFraction float64) (class, query string, cold bool) {
	r := rng.IntN(p.total)
	idx := len(p.classes) - 1
	for i, c := range p.cum {
		if r < c {
			idx = i
			break
		}
	}
	class = p.classes[idx]
	qs := p.mix.Classes[class].Queries
	query = qs[rng.IntN(len(qs))]
	cold = rng.Float64() < coldFraction
	return class, query, cold
}

// interArrival draws the next gap: exponential for poisson, fixed for uniform.
func interArrival(rng *rand.Rand, arrival string, qps float64) time.Duration {
	mean := 1 / qps
	switch arrival {
	case "uniform":
		return time.Duration(mean * float64(time.Second))
	default: // poisson
		return time.Duration(rng.ExpFloat64() * mean * float64(time.Second))
	}
}

type tag struct {
	CogsRun   string `json:"cogs_run"`
	Component string `json:"component"`
	Class     string `json:"class"`
	Query     string `json:"query"`
	Cache     string `json:"cache"`
}

// Run replays the mix at the target rate until cfg.Duration elapses or ctx is
// cancelled. Arrivals beyond ConcurrencyCap queue; their wait is recorded
// separately from server time so saturation shows up as queueing, not as
// inflated query cost.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.QPS <= 0 || cfg.Duration <= 0 || cfg.ConcurrencyCap <= 0 {
		return Result{}, fmt.Errorf("replay: QPS, Duration, ConcurrencyCap must be > 0")
	}
	if cfg.Executor == nil || len(cfg.Queries) == 0 {
		return Result{}, fmt.Errorf("replay: Executor and Queries are required")
	}
	if cfg.TimeSpan <= 0 {
		return Result{}, fmt.Errorf("replay: TimeSpan must be > 0 (sliding query window width)")
	}
	if err := validateMixQueries(cfg.Mix, cfg.Queries); err != nil {
		return Result{}, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	rng := rand.New(rand.NewPCG(cfg.Seed, 0xc065))
	p := newPicker(cfg.Mix)

	res := Result{TargetQPS: cfg.QPS, PerClass: map[string]*ClassStats{}}
	for _, class := range p.classes {
		res.PerClass[class] = &ClassStats{}
	}

	var (
		mu         sync.Mutex
		queueWaits []time.Duration
		wg         sync.WaitGroup
	)
	sem := make(chan struct{}, cfg.ConcurrencyCap)

	start := now()
	deadline := start.Add(cfg.Duration)
	next := start

	for {
		if ctx.Err() != nil || !next.Before(deadline) {
			break
		}
		if wait := next.Sub(now()); wait > 0 {
			sleep(wait)
		}

		class, query, cold := p.pick(rng, cfg.ColdFraction)
		sql, settings, err := renderArrival(cfg, query, class, cold, now())
		stats := res.PerClass[class]
		if err != nil {
			// A render failure is a harness bug, not a workload signal; count
			// and continue so one bad template doesn't kill the cell.
			mu.Lock()
			stats.Issued++
			stats.Errors++
			res.Errors++
			mu.Unlock()
			next = next.Add(interArrival(rng, cfg.Arrival, cfg.QPS))
			continue
		}

		mu.Lock()
		stats.Issued++
		if cold {
			stats.Cold++
		}
		mu.Unlock()

		wg.Add(1)
		queuedAt := now()
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			waited := now().Sub(queuedAt)

			execErr := cfg.Executor.Exec(ctx, sql, settings)

			mu.Lock()
			queueWaits = append(queueWaits, waited)
			if execErr != nil {
				stats.Errors++
				res.Errors++
			} else {
				stats.Completed++
			}
			mu.Unlock()
		}()

		next = next.Add(interArrival(rng, cfg.Arrival, cfg.QPS))
	}
	wg.Wait()

	elapsed := now().Sub(start).Seconds()
	completed := 0
	for _, s := range res.PerClass {
		completed += s.Completed
	}
	if elapsed > 0 {
		res.AchievedQPS = float64(completed) / elapsed
	}
	res.QueuedP50Ms, res.QueuedP95Ms = percentilesMs(queueWaits)
	return res, ctx.Err()
}

func validateMixQueries(m Mix, queries map[string]string) error {
	for name, c := range m.Classes {
		for _, q := range c.Queries {
			if _, ok := queries[q]; !ok {
				return fmt.Errorf("replay: mix class %q references query %q with no loaded SQL", name, q)
			}
		}
	}
	return nil
}

func renderArrival(cfg Config, query, class string, cold bool, at time.Time) (string, map[string]any, error) {
	params := make(map[string]string, len(cfg.Params)+2)
	maps.Copy(params, cfg.Params)
	to := at.UTC()
	params["to"] = strconv.FormatInt(to.Unix(), 10)
	params["from"] = strconv.FormatInt(to.Add(-cfg.TimeSpan).Unix(), 10)

	sql, err := runner.RenderParams(cfg.Queries[query], params)
	if err != nil {
		return "", nil, fmt.Errorf("replay: render %s: %w", query, err)
	}

	cache := "warm"
	if cold {
		cache = "cold"
	}
	comment, err := json.Marshal(tag{
		CogsRun: cfg.RunID, Component: "query", Class: class, Query: query, Cache: cache,
	})
	if err != nil {
		return "", nil, err
	}

	settings := make(map[string]any, len(cfg.Settings)+2)
	maps.Copy(settings, cfg.Settings)
	settings["log_comment"] = string(comment)
	if cold {
		settings["enable_filesystem_cache"] = 0
	}
	return sql, settings, nil
}

func percentilesMs(ds []time.Duration) (p50, p95 float64) {
	if len(ds) == 0 {
		return 0, 0
	}
	sorted := append([]time.Duration(nil), ds...)
	slices.Sort(sorted)
	at := func(q float64) float64 {
		idx := int(math.Round(q * float64(len(sorted)-1)))
		return float64(sorted[idx]) / float64(time.Millisecond)
	}
	return at(0.50), at(0.95)
}
