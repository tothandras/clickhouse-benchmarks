package cogs

import (
	"github.com/openmeterio/ch-playground/bench/accounting"
	"github.com/openmeterio/ch-playground/bench/pricing"
)

// Accounting is the assembled collector output the attribution prices.
type Accounting struct {
	QueryLog accounting.QueryLogStats         `json:"query_log"`
	Merges   accounting.MergeStats            `json:"merges"`
	Async    *accounting.AsyncInsertStats     `json:"async,omitempty"`
	Capacity accounting.Capacity              `json:"capacity"`
	Storage  StorageTimeline                  `json:"storage"`
	MeasureSeconds float64                    `json:"measure_seconds"`
	EventsIngested int                        `json:"events_ingested"`
}

// StorageTimeline are the run's three storage snapshots.
type StorageTimeline struct {
	Prepare  accounting.StorageSnapshot `json:"prepare"`
	SoakEnd  accounting.StorageSnapshot `json:"soak_end"`
	DrainEnd accounting.StorageSnapshot `json:"drain_end"`
}

// ClassCache keys per-class query figures by cache state ("warm"/"cold").
type ClassCache map[string]map[string]float64

// ClassCacheCounts mirrors ClassCache for arrival counts.
type ClassCacheCounts map[string]map[string]uint64

// Attribution is the CPU split and derived ratios.
type Attribution struct {
	InsertCPUSec float64    `json:"insert_cpu_sec"` // tagged ingest statements + async flush CPU
	MergeCPUSec  float64    `json:"merge_cpu_sec"`
	QueryCPUSec  ClassCache `json:"query_cpu_sec"` // class -> cache -> cpu_sec

	QueryCounts      ClassCacheCounts `json:"query_counts"`
	QueryPeakMem     ClassCache       `json:"query_peak_mem_bytes"`
	TotalResultBytes float64          `json:"total_result_bytes"`

	AvailableCPUSec float64 `json:"available_cpu_sec"`
	AttributedCPUSec float64 `json:"attributed_cpu_sec"`
	Coverage        float64 `json:"coverage"`
	IdleCPUSec      float64 `json:"idle_cpu_sec"`

	BytesPerEventSettled float64 `json:"bytes_per_event_settled"`
}

// Attribute folds collector output into the component CPU split.
func Attribute(acc Accounting) Attribution {
	a := Attribution{
		QueryCPUSec:  ClassCache{},
		QueryCounts:  ClassCacheCounts{},
		QueryPeakMem: ClassCache{},
	}

	for _, g := range acc.QueryLog.Groups {
		switch g.Component {
		case "ingest":
			a.InsertCPUSec += g.CPUSec
		case "query":
			class, cache := g.Class, g.Cache
			if cache == "" {
				cache = "warm"
			}
			if a.QueryCPUSec[class] == nil {
				a.QueryCPUSec[class] = map[string]float64{}
				a.QueryCounts[class] = map[string]uint64{}
				a.QueryPeakMem[class] = map[string]float64{}
			}
			a.QueryCPUSec[class][cache] += g.CPUSec
			a.QueryCounts[class][cache] += g.N
			a.QueryPeakMem[class][cache] = max(a.QueryPeakMem[class][cache], float64(g.PeakMemBytes))
			a.TotalResultBytes += float64(g.ResultBytes)
		}
	}
	if acc.Async != nil {
		a.InsertCPUSec += acc.Async.CPUSec
	}
	a.MergeCPUSec = acc.Merges.CPUSec

	queryCPU := 0.0
	for _, byCache := range a.QueryCPUSec {
		for _, cpu := range byCache {
			queryCPU += cpu
		}
	}
	a.AttributedCPUSec = a.InsertCPUSec + a.MergeCPUSec + queryCPU
	a.AvailableCPUSec = acc.Capacity.TotalVCPUs * acc.MeasureSeconds
	if a.AvailableCPUSec > 0 {
		a.Coverage = a.AttributedCPUSec / a.AvailableCPUSec
	}
	a.IdleCPUSec = max(0, a.AvailableCPUSec-a.AttributedCPUSec)

	if acc.EventsIngested > 0 {
		delta := float64(acc.Storage.DrainEnd.CompressedBytes) - float64(acc.Storage.Prepare.CompressedBytes)
		a.BytesPerEventSettled = delta / float64(acc.EventsIngested)
	}
	return a
}

// CostSet is one pricing mode's component costs.
type CostSet struct {
	WindowUSD    float64    `json:"window_usd,omitempty"` // billed-shape only
	InsertUSD    float64    `json:"insert_usd"`
	MergeUSD     float64    `json:"merge_usd"`
	QueryUSD     ClassCache `json:"query_usd"`
	IdleFloorUSD float64    `json:"idle_floor_usd,omitempty"` // billed-shape only
}

// ModePair is one unit cost in both pricing modes.
type ModePair struct {
	BilledShape float64 `json:"billed_shape"`
	CPULinear   float64 `json:"cpu_linear"`
}

// UnitCosts is the unit-cost card.
type UnitCosts struct {
	USDPer1MEvents struct {
		BilledShape float64 `json:"billed_shape"`
		CPULinear   float64 `json:"cpu_linear"`
		// Shares split the billed-shape figure between insert and merge.
		InsertShare float64 `json:"insert_share"`
		MergeShare  float64 `json:"merge_share"`
	} `json:"usd_per_1m_events"`
	USDPer1KQueries map[string]map[string]ModePair `json:"usd_per_1k_queries"` // class -> cache -> $
	BytesPerEventSettled        float64 `json:"bytes_per_event_settled"`
	USDPer1MEventsMonthStorage  float64 `json:"usd_per_1m_events_month_storage"` // includes backup multiplier (estimate)
	EgressUSDEstimate           float64 `json:"egress_usd_estimate"`             // result bytes x egress rate (estimate)
	IdleFloorUSDPerServiceMonth float64 `json:"idle_floor_usd_per_service_month"`
}

// Costs is the priced result: both modes plus the unit-cost card.
type Costs struct {
	BilledShape CostSet   `json:"billed_shape"`
	CPULinear   CostSet   `json:"cpu_linear"`
	Unit        UnitCosts `json:"unit"`
}

// queryKey flattens (class, cache) into the component key the pricing
// functions split over.
func queryKey(class, cache string) string { return "query/" + class + "/" + cache }

// Price applies a profile to the attribution in both modes and derives the
// unit-cost card.
func Price(p pricing.Profile, acc Accounting, a Attribution) Costs {
	cpu := map[string]float64{
		"insert": a.InsertCPUSec,
		"merge":  a.MergeCPUSec,
	}
	for class, byCache := range a.QueryCPUSec {
		for cache, sec := range byCache {
			cpu[queryKey(class, cache)] = sec
		}
	}

	windowUSD, billedPer, idleUSD := pricing.BilledShape(p, acc.MeasureSeconds, cpu, a.AvailableCPUSec)
	linearPer := pricing.CPULinear(p, cpu)

	extract := func(per map[string]float64) CostSet {
		cs := CostSet{InsertUSD: per["insert"], MergeUSD: per["merge"], QueryUSD: ClassCache{}}
		for class, byCache := range a.QueryCPUSec {
			cs.QueryUSD[class] = map[string]float64{}
			for cache := range byCache {
				cs.QueryUSD[class][cache] = per[queryKey(class, cache)]
			}
		}
		return cs
	}

	c := Costs{BilledShape: extract(billedPer), CPULinear: extract(linearPer)}
	c.BilledShape.WindowUSD = windowUSD
	c.BilledShape.IdleFloorUSD = idleUSD

	if acc.EventsIngested > 0 {
		ev := float64(acc.EventsIngested)
		billedIngest := c.BilledShape.InsertUSD + c.BilledShape.MergeUSD
		c.Unit.USDPer1MEvents.BilledShape = billedIngest / ev * 1e6
		c.Unit.USDPer1MEvents.CPULinear = (c.CPULinear.InsertUSD + c.CPULinear.MergeUSD) / ev * 1e6
		if billedIngest > 0 {
			c.Unit.USDPer1MEvents.InsertShare = c.BilledShape.InsertUSD / billedIngest
			c.Unit.USDPer1MEvents.MergeShare = c.BilledShape.MergeUSD / billedIngest
		}
	}

	c.Unit.USDPer1KQueries = map[string]map[string]ModePair{}
	for class, byCache := range a.QueryCounts {
		c.Unit.USDPer1KQueries[class] = map[string]ModePair{}
		for cache, n := range byCache {
			if n == 0 {
				continue
			}
			c.Unit.USDPer1KQueries[class][cache] = ModePair{
				BilledShape: c.BilledShape.QueryUSD[class][cache] / float64(n) * 1e3,
				CPULinear:   c.CPULinear.QueryUSD[class][cache] / float64(n) * 1e3,
			}
		}
	}

	c.Unit.BytesPerEventSettled = a.BytesPerEventSettled
	if a.BytesPerEventSettled > 0 {
		c.Unit.USDPer1MEventsMonthStorage = pricing.StorageUSDMonth(p, a.BytesPerEventSettled*1e6)
	}
	c.Unit.EgressUSDEstimate = pricing.EgressUSD(p, a.TotalResultBytes)
	c.Unit.IdleFloorUSDPerServiceMonth = pricing.IdleFloorUSDMonth(p)
	return c
}
