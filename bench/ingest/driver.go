// Package ingest is the cogs steady-rate ingest driver. It reproduces the
// OpenMeter sink's part-creation pattern — paced inserts batched by
// max-rows OR flush-interval, whichever hits first — because that pattern is
// what drives merge amplification; the bulk seeder (huge batches, no pacing)
// deliberately does not.
package ingest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/openmeterio/ch-playground/bench/seed"
)

// Inserter sends one batch of events. The production implementation wraps the
// ClickHouse native client; tests use a fake.
type Inserter interface {
	Insert(ctx context.Context, batch []seed.Event) error
}

// Config controls one driver run.
type Config struct {
	EventsPerSec  int           // target rate; must be > 0
	BatchMaxRows  int           // flush threshold (rows)
	FlushInterval time.Duration // flush threshold (age)
	Duration      time.Duration // how long to drive

	Gen      *seed.Generator // event source; events are stamped with wall-clock time (NextAt)
	Inserter Inserter

	// Test hooks; default to the real clock.
	Now   func() time.Time
	Sleep func(time.Duration)
}

// Result reports what the driver achieved.
type Result struct {
	TargetEPS     int     `json:"target_eps"`
	AchievedEPS   float64 `json:"achieved_eps"`
	RateSatisfied bool    `json:"rate_satisfied"` // achieved >= 95% of target
	Events        int     `json:"events"`
	Batches       int     `json:"batches"`
	Errors        int     `json:"errors"`
	// Client-observed insert round-trip latency.
	InsertP50Ms float64 `json:"insert_p50_ms"`
	InsertP95Ms float64 `json:"insert_p95_ms"`
}

// Run drives inserts at the target rate until cfg.Duration elapses or ctx is
// cancelled. Backpressure is no-burst by construction: event generation is
// paced by bounded time slices, so time lost inside a slow insert round-trip
// is never made up by bursting afterwards — it surfaces as achieved < target
// and, past 5%, as a saturation flag. That is itself a finding: the service
// shape cannot absorb the rate.
func Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.EventsPerSec <= 0 || cfg.BatchMaxRows <= 0 || cfg.FlushInterval <= 0 || cfg.Duration <= 0 {
		return Result{}, fmt.Errorf("ingest: EventsPerSec, BatchMaxRows, FlushInterval, Duration must be > 0")
	}
	if cfg.Gen == nil || cfg.Inserter == nil {
		return Result{}, fmt.Errorf("ingest: Gen and Inserter are required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sleep := cfg.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	start := now()
	deadline := start.Add(cfg.Duration)

	res := Result{TargetEPS: cfg.EventsPerSec}
	var latencies []time.Duration
	batch := make([]seed.Event, 0, cfg.BatchMaxRows)
	batchBorn := start // when the oldest row in the current batch was generated

	flush := func() {
		if len(batch) == 0 {
			return
		}
		t0 := now()
		err := cfg.Inserter.Insert(ctx, batch)
		latencies = append(latencies, now().Sub(t0))
		if err != nil {
			res.Errors++
		} else {
			res.Events += len(batch)
		}
		res.Batches++
		batch = batch[:0]
	}

	// Generation is paced by a fractional-carry token count. dt is capped at
	// FlushInterval so a stall (slow insert, scheduler hiccup) forfeits the
	// events that would have been generated during it instead of bursting.
	carry := 0.0
	prev := start
	// The poll quantum bounds pacing granularity; small enough that even
	// 1-row batches at low rates flush close to their due time.
	quantum := min(cfg.FlushInterval/4, 50*time.Millisecond)
	if quantum <= 0 {
		quantum = time.Millisecond
	}

	for {
		if err := ctx.Err(); err != nil {
			flush()
			break
		}
		t := now()
		if !t.Before(deadline) {
			flush()
			break
		}

		dt := t.Sub(prev)
		prev = t
		if dt > cfg.FlushInterval {
			dt = cfg.FlushInterval // no-burst: forfeit, don't catch up
		}
		carry += float64(cfg.EventsPerSec) * dt.Seconds()

		for carry >= 1 {
			if len(batch) == 0 {
				batchBorn = t
			}
			batch = append(batch, cfg.Gen.NextAt(t))
			carry--
			if len(batch) >= cfg.BatchMaxRows {
				flush()
			}
		}
		if len(batch) > 0 && t.Sub(batchBorn) >= cfg.FlushInterval {
			flush()
		}

		sleep(quantum)
	}

	elapsed := now().Sub(start).Seconds()
	if elapsed > 0 {
		res.AchievedEPS = float64(res.Events) / elapsed
	}
	res.RateSatisfied = res.AchievedEPS >= 0.95*float64(res.TargetEPS)
	res.InsertP50Ms, res.InsertP95Ms = percentilesMs(latencies)
	return res, ctx.Err()
}

func percentilesMs(ds []time.Duration) (p50, p95 float64) {
	if len(ds) == 0 {
		return 0, 0
	}
	sorted := append([]time.Duration(nil), ds...)
	slices.Sort(sorted)
	at := func(q float64) float64 {
		idx := int(q * float64(len(sorted)-1))
		return float64(sorted[idx]) / float64(time.Millisecond)
	}
	return at(0.50), at(0.95)
}

// CHInserter inserts batches into a ClickHouse table via the native client,
// tagging every INSERT with the run's log_comment and applying the cell's
// insert settings (async_insert etc.).
type CHInserter struct {
	Conn       driver.Conn
	Table      string
	LogComment string
	Settings   map[string]any
}

func (c *CHInserter) Insert(ctx context.Context, batch []seed.Event) error {
	settings := make(map[string]any, len(c.Settings)+1)
	maps.Copy(settings, c.Settings)
	if c.LogComment != "" {
		settings["log_comment"] = c.LogComment
	}
	bctx := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings(settings)))

	insertSQL := fmt.Sprintf("INSERT INTO %s (namespace, id, type, subject, source, time, data, ingested_at, stored_at, store_row_id)", c.Table)
	b, err := c.Conn.PrepareBatch(bctx, insertSQL)
	if err != nil {
		return fmt.Errorf("ingest: PrepareBatch: %w", err)
	}
	for _, row := range batch {
		if err := b.Append(
			row.Namespace, row.ID, row.Type, row.Subject, "bench-cogs",
			row.Time, row.Data, row.StoredAt, row.StoredAt, row.StoreRowID,
		); err != nil {
			return fmt.Errorf("ingest: Append: %w", err)
		}
	}
	if err := b.Send(); err != nil {
		return fmt.Errorf("ingest: Send: %w", err)
	}
	return nil
}
