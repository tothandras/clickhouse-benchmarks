// Package cogs orchestrates COGS measurement runs: workload cells that pair a
// rate-controlled ingest driver with a weighted query replayer, attribute
// resource consumption from system tables, and price it into unit costs.
package cogs

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Duration is a time.Duration that (un)marshals as a Go duration string
// (e.g. "30m"), the format cell manifests use.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"30m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d Duration) D() time.Duration { return time.Duration(d) }

// IngestSpec configures the cell's ingest driver. Its generator fields
// (namespaces, mixed_value, seed) also drive the preload seeding, so they are
// required whenever the cell preloads history even if events_per_sec is 0.
type IngestSpec struct {
	EventsPerSec  int      `json:"events_per_sec"`           // 0 = no live ingest (query-only / idle cells)
	BatchMaxRows  int      `json:"batch_max_rows,omitempty"` // flush when either threshold hits,
	FlushInterval Duration `json:"flush_interval,omitempty"` //   mirroring the OpenMeter sink semantics
	AsyncInsert   bool     `json:"async_insert,omitempty"`
	Namespaces    int      `json:"namespaces,omitempty"`
	MixedValue    bool     `json:"mixed_value,omitempty"`
	Seed          uint64   `json:"seed,omitempty"` // generator determinism, same contract as the seeder
}

// QuerySpec configures the cell's query replayer.
type QuerySpec struct {
	QPS            float64        `json:"qps"`                       // 0 = ingest-only / idle cells
	Arrival        string         `json:"arrival,omitempty"`         // "poisson" | "uniform"
	Mix            string         `json:"mix,omitempty"`             // key into scenarios/<scenario>/queries/mix.json
	ColdFraction   float64        `json:"cold_fraction,omitempty"`   // fraction issued with enable_filesystem_cache=0
	ConcurrencyCap int            `json:"concurrency_cap,omitempty"` // max in-flight; excess arrivals queue
	Settings       map[string]any `json:"settings,omitempty"`        // applied to every replayed query, recorded in results
}

// Cell is one workload-cell manifest (cells/<name>.json).
type Cell struct {
	Name           string     `json:"name"`
	Scenario       string     `json:"scenario"`
	PreloadRows    int        `json:"preload_rows"`
	Soak           Duration   `json:"soak"`
	Measure        Duration   `json:"measure"`
	Drain          Duration   `json:"drain"`
	Ingest         IngestSpec `json:"ingest"`
	Query          QuerySpec  `json:"query"`
	PricingProfile string     `json:"pricing_profile"`
}

// LoadCell reads and validates a cell manifest. Decoding is strict: unknown
// fields are an error, so typos in manifests fail loudly instead of silently
// running a different workload.
func LoadCell(path string) (Cell, error) {
	f, err := os.Open(path)
	if err != nil {
		return Cell{}, err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var c Cell
	if err := dec.Decode(&c); err != nil {
		return Cell{}, fmt.Errorf("cell %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Cell{}, fmt.Errorf("cell %s: %w", path, err)
	}
	return c, nil
}

// Validate checks manifest invariants. events_per_sec == 0 && qps == 0 is
// legal: that is the idle-floor cell.
func (c *Cell) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if c.Name == "" {
		add("name is required")
	}
	if c.Scenario == "" {
		add("scenario is required")
	}
	if c.PricingProfile == "" {
		add("pricing_profile is required")
	}
	if c.PreloadRows < 0 {
		add("preload_rows must be >= 0")
	}
	if c.Measure.D() <= 0 {
		add("measure duration must be > 0")
	}
	if c.Soak.D() < 0 || c.Drain.D() < 0 {
		add("soak and drain durations must be >= 0")
	}

	if c.Ingest.EventsPerSec < 0 {
		add("ingest.events_per_sec must be >= 0")
	}
	if c.Ingest.EventsPerSec > 0 {
		if c.Ingest.BatchMaxRows <= 0 {
			add("ingest.batch_max_rows must be > 0 when ingesting")
		}
		if c.Ingest.FlushInterval.D() <= 0 {
			add("ingest.flush_interval must be > 0 when ingesting")
		}
	}
	if (c.Ingest.EventsPerSec > 0 || c.PreloadRows > 0) && c.Ingest.Seed == 0 {
		add("ingest.seed is required when ingesting or preloading (deterministic data is the harness contract)")
	}

	if c.Query.QPS < 0 {
		add("query.qps must be >= 0")
	}
	if c.Query.QPS > 0 {
		switch c.Query.Arrival {
		case "poisson", "uniform":
		case "":
			add("query.arrival is required when querying (\"poisson\" or \"uniform\")")
		default:
			add("query.arrival must be \"poisson\" or \"uniform\", got %q", c.Query.Arrival)
		}
		if c.Query.Mix == "" {
			add("query.mix is required when querying")
		}
		if c.Query.ColdFraction < 0 || c.Query.ColdFraction > 1 {
			add("query.cold_fraction must be in [0, 1]")
		}
		if c.Query.ConcurrencyCap <= 0 {
			add("query.concurrency_cap must be > 0 when querying")
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid manifest: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ApplyProfile overrides phase durations for a named run profile without
// editing the manifest. "ci" is the smoke profile; "" or "full" keeps the
// manifest durations.
func (c *Cell) ApplyProfile(profile string) error {
	switch profile {
	case "", "full":
		return nil
	case "ci":
		c.Soak = Duration(2 * time.Minute)
		c.Measure = Duration(3 * time.Minute)
		c.Drain = Duration(1 * time.Minute)
		return nil
	default:
		return fmt.Errorf("unknown run profile %q (want \"ci\" or \"full\")", profile)
	}
}
