package ingest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openmeterio/ch-playground/bench/seed"
)

// fakeClock drives the loop deterministically: Sleep advances time instead of
// blocking, and a fake Inserter can advance it too (simulating slow inserts).
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time        { return c.t }
func (c *fakeClock) Sleep(d time.Duration) { c.t = c.t.Add(d) }

type fakeInserter struct {
	clock   *fakeClock
	perCall time.Duration // simulated round-trip; advances the clock
	failAll bool
	batches [][]seed.Event
}

func (f *fakeInserter) Insert(_ context.Context, batch []seed.Event) error {
	cp := append([]seed.Event(nil), batch...)
	f.batches = append(f.batches, cp)
	if f.perCall > 0 {
		f.clock.Sleep(f.perCall)
	}
	if f.failAll {
		return errors.New("boom")
	}
	return nil
}

func testGen(t *testing.T) *seed.Generator {
	t.Helper()
	cfg := seed.DefaultConfig()
	cfg.TimeEnd = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	g, err := seed.NewGenerator(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func runDriver(t *testing.T, eps, batchMax int, flush, dur, insertLatency time.Duration, failAll bool) (Result, *fakeInserter) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)}
	ins := &fakeInserter{clock: clock, perCall: insertLatency, failAll: failAll}
	res, err := Run(context.Background(), Config{
		EventsPerSec:  eps,
		BatchMaxRows:  batchMax,
		FlushInterval: flush,
		Duration:      dur,
		Gen:           testGen(t),
		Inserter:      ins,
		Now:           clock.Now,
		Sleep:         clock.Sleep,
	})
	if err != nil {
		t.Fatal(err)
	}
	return res, ins
}

func TestDriverHoldsTargetRate(t *testing.T) {
	res, ins := runDriver(t, 1000, 500, 100*time.Millisecond, 2*time.Second, 0, false)
	if res.Events < 1900 || res.Events > 2100 {
		t.Fatalf("2s at 1000 eps should yield ~2000 events, got %d", res.Events)
	}
	if !res.RateSatisfied {
		t.Fatalf("instant inserter must satisfy the rate: achieved %.0f of %d", res.AchievedEPS, res.TargetEPS)
	}
	for _, b := range ins.batches {
		if len(b) > 500 {
			t.Fatalf("batch exceeded batch_max_rows: %d", len(b))
		}
	}
}

func TestDriverFlushesOnRowThreshold(t *testing.T) {
	// 10k eps with a 100-row cap: size threshold dominates the 1s interval.
	res, ins := runDriver(t, 10_000, 100, time.Second, 500*time.Millisecond, 0, false)
	if res.Events == 0 || len(ins.batches) < 10 {
		t.Fatalf("expected many size-triggered flushes, got %d batches / %d events", len(ins.batches), res.Events)
	}
	full := 0
	for _, b := range ins.batches {
		if len(b) == 100 {
			full++
		}
	}
	if full < len(ins.batches)/2 {
		t.Fatalf("size threshold should produce mostly full batches: %d of %d full", full, len(ins.batches))
	}
}

func TestDriverFlushesOnAgeThreshold(t *testing.T) {
	// 10 eps with a 1000-row cap: only the 100ms age threshold can flush.
	res, ins := runDriver(t, 10, 1000, 100*time.Millisecond, 2*time.Second, 0, false)
	if res.Events == 0 {
		t.Fatal("no events generated")
	}
	for i, b := range ins.batches {
		if len(b) >= 1000 {
			t.Fatalf("batch %d hit the row cap; age threshold never fired", i)
		}
	}
	if len(ins.batches) < 10 {
		t.Fatalf("2s / 100ms flush interval should yield ~20 batches, got %d", len(ins.batches))
	}
}

func TestDriverNoBurstUnderSlowInserts(t *testing.T) {
	// Each insert burns 400ms against a 100ms flush interval: the driver loses
	// ~4 flush-intervals of generation per round-trip and must NOT make them
	// up by bursting afterwards.
	res, ins := runDriver(t, 1000, 10_000, 100*time.Millisecond, 5*time.Second, 400*time.Millisecond, false)
	if res.RateSatisfied {
		t.Fatalf("a 400ms-per-insert backend cannot satisfy 1000 eps; achieved %.0f", res.AchievedEPS)
	}
	if res.AchievedEPS > 0.6*float64(res.TargetEPS) {
		t.Fatalf("no-burst violated: achieved %.0f eps suggests catch-up generation", res.AchievedEPS)
	}
	// No-burst also bounds single batches: one capped dt of generation, not
	// the whole stall's worth.
	for i, b := range ins.batches {
		if len(b) > 200 { // 100ms cap at 1000 eps = 100 rows + one quantum slack
			t.Fatalf("batch %d has %d rows: stall was made up in a burst", i, len(b))
		}
	}
}

func TestDriverStampsWallClockPreservingPayloads(t *testing.T) {
	_, ins := runDriver(t, 100, 50, 100*time.Millisecond, time.Second, 0, false)
	want := testGen(t)
	idx := 0
	var last time.Time
	for _, b := range ins.batches {
		for _, e := range b {
			ref := want.At(idx)
			if e.ID != ref.ID || e.Data != ref.Data || e.Type != ref.Type || e.Subject != ref.Subject {
				t.Fatalf("event %d payload diverged from the deterministic stream", idx)
			}
			if e.Time.Before(last) {
				t.Fatalf("event %d time went backwards: %v < %v", idx, e.Time, last)
			}
			if e.Time.Year() != 2026 || e.Time.Month() != 6 || e.Time.Day() != 9 {
				t.Fatalf("event %d not stamped with driver wall clock: %v", idx, e.Time)
			}
			last = e.Time
			idx++
		}
	}
	if idx == 0 {
		t.Fatal("no events")
	}
}

func TestDriverCountsErrors(t *testing.T) {
	res, _ := runDriver(t, 100, 10, 50*time.Millisecond, time.Second, 0, true)
	if res.Errors == 0 {
		t.Fatal("failing inserter must be counted")
	}
	if res.Events != 0 {
		t.Fatalf("failed batches must not count as ingested events, got %d", res.Events)
	}
}
