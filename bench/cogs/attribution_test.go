package cogs

import (
	"math"
	"testing"

	"github.com/openmeterio/ch-playground/bench/accounting"
	"github.com/openmeterio/ch-playground/bench/pricing"
)

// fixtureAccounting is a hand-checkable snapshot: a 1-hour measure window on
// a 2x4-vCPU shape (28800 available cpu-sec) with round CPU numbers.
func fixtureAccounting() Accounting {
	return Accounting{
		QueryLog: accounting.QueryLogStats{
			CPUSource: "os_cpu",
			Groups: []accounting.QueryGroup{
				{Component: "ingest", N: 3600, CPUSec: 900, WrittenRows: 18_000_000},
				{Component: "query", Class: "meter_agg", Cache: "warm", N: 12_000, CPUSec: 2400, ResultBytes: 1_000_000_000, PeakMemBytes: 512 << 20},
				{Component: "query", Class: "meter_agg", Cache: "cold", N: 1_000, CPUSec: 600, ResultBytes: 200_000_000, PeakMemBytes: 700 << 20},
				{Component: "query", Class: "key_only", Cache: "warm", N: 2_000, CPUSec: 100, ResultBytes: 50_000_000, PeakMemBytes: 64 << 20},
			},
		},
		Merges: accounting.MergeStats{Merges: 120, CPUSec: 300},
		Capacity: accounting.Capacity{Replicas: 2, VCPUsPerReplica: 4, TotalVCPUs: 8, Source: "CGroupMaxCPU"},
		Storage: StorageTimeline{
			Prepare:  accounting.StorageSnapshot{CompressedBytes: 1_000_000_000},
			DrainEnd: accounting.StorageSnapshot{CompressedBytes: 2_188_000_000},
		},
		MeasureSeconds: 3600,
		EventsIngested: 18_000_000,
	}
}

func fixtureProfile() pricing.Profile {
	return pricing.Profile{
		Name: "fixture", Currency: "USD",
		Service: pricing.Service{Replicas: 2, GiBPerReplica: 16, VCPUsPerReplica: 4, GiBPerComputeUnit: 8},
		Rates: pricing.Rates{
			ComputeUnitHour: 0.2985, StorageTBMonth: 25.30,
			BackupMultiplier: 1.0, EgressPerGBPublic: 0.1152,
		},
	}
}

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Fatalf("%s = %.12f, want %.12f", name, got, want)
	}
}

func TestAttributeSplitsComponents(t *testing.T) {
	a := Attribute(fixtureAccounting())

	near(t, "insert cpu", a.InsertCPUSec, 900)
	near(t, "merge cpu", a.MergeCPUSec, 300)
	near(t, "meter_agg warm", a.QueryCPUSec["meter_agg"]["warm"], 2400)
	near(t, "meter_agg cold", a.QueryCPUSec["meter_agg"]["cold"], 600)
	near(t, "key_only warm", a.QueryCPUSec["key_only"]["warm"], 100)

	near(t, "available", a.AvailableCPUSec, 8*3600)
	near(t, "attributed", a.AttributedCPUSec, 900+300+2400+600+100) // 4300
	near(t, "coverage", a.Coverage, 4300.0/28800.0)
	near(t, "idle", a.IdleCPUSec, 28800-4300)

	// (2_188_000_000 - 1_000_000_000) / 18M = 66 bytes/event.
	near(t, "bytes/event", a.BytesPerEventSettled, 66)
	near(t, "result bytes", a.TotalResultBytes, 1_250_000_000)
	if a.QueryCounts["meter_agg"]["warm"] != 12_000 {
		t.Fatalf("counts wrong: %+v", a.QueryCounts)
	}
}

func TestAttributeAddsAsyncFlushCPUToInsert(t *testing.T) {
	acc := fixtureAccounting()
	acc.Async = &accounting.AsyncInsertStats{CPUSec: 100, Flushes: 50, FlushesMatched: 50}
	a := Attribute(acc)
	near(t, "insert cpu incl. async flush", a.InsertCPUSec, 1000)
}

func TestPriceBilledShapeSumsToWindow(t *testing.T) {
	acc := fixtureAccounting()
	a := Attribute(acc)
	c := Price(fixtureProfile(), acc, a)

	// 4 units x 1h x 0.2985.
	near(t, "window", c.BilledShape.WindowUSD, 1.194)

	sum := c.BilledShape.InsertUSD + c.BilledShape.MergeUSD + c.BilledShape.IdleFloorUSD
	for _, byCache := range c.BilledShape.QueryUSD {
		for _, usd := range byCache {
			sum += usd
		}
	}
	near(t, "components sum to window", sum, c.BilledShape.WindowUSD)

	// Hand-computed component: insert = 1.194 x 900/28800.
	near(t, "insert usd", c.BilledShape.InsertUSD, 1.194*900/28800)
	near(t, "meter_agg cold usd", c.BilledShape.QueryUSD["meter_agg"]["cold"], 1.194*600/28800)
}

func TestPriceUnitCard(t *testing.T) {
	acc := fixtureAccounting()
	a := Attribute(acc)
	c := Price(fixtureProfile(), acc, a)

	// $/1M events, billed shape: (insert+merge USD) / 18M x 1e6.
	billedIngest := 1.194 * (900 + 300) / 28800
	near(t, "usd/1M events billed", c.Unit.USDPer1MEvents.BilledShape, billedIngest/18e6*1e6)
	near(t, "insert share", c.Unit.USDPer1MEvents.InsertShare, 900.0/1200.0)
	near(t, "merge share", c.Unit.USDPer1MEvents.MergeShare, 300.0/1200.0)

	// cpu-linear: $/cpu_sec = 0.2985/7200; ingest cpu 1200s over 18M events.
	near(t, "usd/1M events linear", c.Unit.USDPer1MEvents.CPULinear, 1200*0.2985/7200/18e6*1e6)

	// $/1k queries, meter_agg warm: billed 1.194x2400/28800 over 12k queries.
	near(t, "usd/1k meter_agg warm billed",
		c.Unit.USDPer1KQueries["meter_agg"]["warm"].BilledShape, 1.194*2400/28800/12000*1e3)
	// Cold per-query must price higher than warm here (more CPU per query).
	mw := c.Unit.USDPer1KQueries["meter_agg"]["warm"].CPULinear
	mc := c.Unit.USDPer1KQueries["meter_agg"]["cold"].CPULinear
	if mc <= mw {
		t.Fatalf("cold (%.9f) must out-price warm (%.9f) per query in this fixture", mc, mw)
	}

	// Storage: 66 bytes/event -> 66 MB per 1M events per month.
	near(t, "storage usd/1M events-month", c.Unit.USDPer1MEventsMonthStorage, 66e6/1e12*25.30)
	// Egress estimate: 1.25 GB at 0.1152.
	near(t, "egress estimate", c.Unit.EgressUSDEstimate, 1.25*0.1152)
	// Idle floor bound: 4 units x 730 h.
	near(t, "idle floor / month", c.Unit.IdleFloorUSDPerServiceMonth, 4*730*0.2985)
}

func TestPriceZeroProfileDecouplesAccounting(t *testing.T) {
	acc := fixtureAccounting()
	a := Attribute(acc)
	zero := fixtureProfile()
	zero.Rates = pricing.Rates{}
	c := Price(zero, acc, a)

	if c.BilledShape.WindowUSD != 0 || c.Unit.USDPer1MEvents.BilledShape != 0 || c.Unit.EgressUSDEstimate != 0 {
		t.Fatal("zero rates must price to $0")
	}
	// Resource accounting stays fully populated.
	near(t, "coverage unaffected", a.Coverage, 4300.0/28800.0)
	near(t, "bytes/event unaffected", c.Unit.BytesPerEventSettled, 66)
}
