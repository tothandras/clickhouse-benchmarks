// Package seed generates synthetic OpenMeter-shaped events for benchmark scenarios.
//
// The seeder produces events matching the baseline om_events table shape:
// namespace + type (LowCardinality) + subject + JSON-encoded data payload.
// To model real OpenMeter usage, where the data field is user-controlled and
// differs per event type, the seeder emits a weighted mix of heterogeneous
// event types (see DefaultConfig): a dominant baseline "api_request" carrying
// {value, group1, group2} (which the canonical meter queries read), plus
// kong_api_request, llm_request, workload, and agent_run types that each carry
// their own distinct field-set. Numeric payload fields are emitted as JSON
// strings (e.g. "tokens":"1") to match real producers, so queries must apply
// toFloat64OrNull(JSON_VALUE(...)) exactly like upstream OpenMeter.
//
// RNG is seedable so reruns across table-design variants compare against
// identical data (same per-row type assignment and identical payloads).
package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/oklog/ulid/v2"
)

// EventType is one entry in the seeder's weighted event-type catalog. Each
// type writes its Name into the row's type column and the JSON marshaling of
// BuildData's return into the data column. Weight controls the relative share
// of rows assigned this type (selection is by cumulative weight).
type EventType struct {
	Name     string
	Weight   int
	BuildData func(rng *rand.Rand) map[string]any
}

// Config controls the synthetic event distribution. Defaults satisfy the
// baseline-openmeter-scenario cardinality requirement: ≥1 namespace,
// ≥2 types, ≥100 subjects, ≥3 days of time spread.
type Config struct {
	Namespace string        // Single namespace; baseline uses one.
	Types     []string      // Type names; Types[0] is the baseline type the default {type} param binds to.
	Subjects  int           // Subject pool size; ≥100 for baseline.
	Group1    []string      // Categorical group1 values (baseline api_request payload).
	Group2    []string      // Categorical group2 values (baseline api_request payload).
	EventTypes []EventType  // Weighted catalog of event types; each emits its own data shape.
	Rows      int           // Count of events to insert.
	TimeSpan  time.Duration // Time window the events span, ending at TimeEnd.
	TimeEnd   time.Time     // Newest event time; events are uniform in [TimeEnd-TimeSpan, TimeEnd).
	Seed      uint64        // RNG seed for reproducibility.
	BatchSize int           // Rows per batch insert; defaults to 10k.

	// AsyncInsert sets the async_insert SETTING on each batch.
	AsyncInsert bool
	// WaitAsyncInsert sets wait_for_async_insert when AsyncInsert is true.
	WaitAsyncInsert bool
}

// DefaultConfig returns a Config with sane baseline defaults.
// TimeEnd defaults to time.Now() rounded to the previous minute so reruns
// in quick succession overlap (idempotent partition coverage).
//
// EventTypes is a weighted mix modeling realistic OpenMeter usage. The
// baseline "api_request" type carries {value, group1, group2} and dominates
// (weight 50 of 100) so the canonical meter queries keep scanning a large,
// representative population; the other four types each carry their own shape.
func DefaultConfig() Config {
	group1 := []string{"us-east-1", "us-west-2", "eu-central-1"}
	group2 := []string{"free", "pro", "enterprise"}
	return Config{
		Namespace: "default",
		Types:     []string{"api_request", "kong_api_request", "llm_request", "workload", "agent_run"},
		Subjects:  100,
		Group1:    group1,
		Group2:    group2,
		EventTypes: []EventType{
			{Name: "api_request", Weight: 50, BuildData: buildBaseline(group1, group2)},
			{Name: "kong_api_request", Weight: 25, BuildData: buildKongAPIRequest},
			{Name: "llm_request", Weight: 15, BuildData: buildLLMRequest},
			{Name: "workload", Weight: 7, BuildData: buildWorkload},
			{Name: "agent_run", Weight: 3, BuildData: buildAgentRun},
		},
		Rows:      1_000_000,
		TimeSpan:  3 * 24 * time.Hour,
		TimeEnd:   time.Now().Truncate(time.Minute),
		Seed:      42,
		BatchSize: 10_000,
	}
}

// selectEventType picks an event type by cumulative weight using one rng draw.
// cumWeights[i] is the running sum of weights through index i; total is the
// final sum. Linear scan — the catalog is tiny, so binary search buys nothing.
func selectEventType(rng *rand.Rand, types []EventType, cumWeights []int, total int) EventType {
	r := rng.IntN(total)
	for i, c := range cumWeights {
		if r < c {
			return types[i]
		}
	}
	return types[len(types)-1] // unreachable: r < total always lands above
}

// pick returns a deterministic element of vs using one rng draw.
func pick(rng *rand.Rand, vs []string) string { return vs[rng.IntN(len(vs))] }

// hex16 returns a random 16-hex-digit id (one rng draw). Used for the
// user-provided `id` column: arbitrary, format-free, no time correlation.
func hex16(rng *rand.Rand) string { return fmt.Sprintf("%016x", rng.Uint64()) }

// rngReader adapts a math/rand/v2 source to io.Reader so ULID entropy is
// drawn from the seeder's deterministic stream (reproducible across runs).
type rngReader struct{ rng *rand.Rand }

func (r rngReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r.rng.Uint32())
	}
	return len(p), nil
}

// storeRowID models OpenMeter's controlled, time-ordered store_row_id: a real
// ULID (github.com/oklog/ulid/v2) whose 48-bit millisecond timestamp prefix is
// the event time, so values sort lexicographically in time order. This time
// correlation is what makes a minmax skip index on store_row_id effective,
// unlike the random user-provided `id`. Entropy comes from the seeder's RNG so
// output stays deterministic per seed. Distinct from `id`.
func storeRowID(t time.Time, entropy rngReader) string {
	return ulid.MustNew(ulid.Timestamp(t), entropy).String()
}

// buildBaseline returns the baseline payload builder carrying the numeric
// value plus two categorical group fields the canonical meter queries read.
func buildBaseline(group1, group2 []string) func(rng *rand.Rand) map[string]any {
	return func(rng *rand.Rand) map[string]any {
		return map[string]any{
			"value":  rng.Float64() * 1000,
			"group1": pick(rng, group1),
			"group2": pick(rng, group2),
		}
	}
}

// Heterogeneous payloads. Numeric fields are emitted as JSON strings to match
// real OpenMeter producers, so queries must parse them via toFloat64OrNull.
func buildKongAPIRequest(rng *rand.Rand) map[string]any {
	statuses := []string{"200", "200", "200", "201", "400", "404", "500"} // skewed to 200
	return map[string]any{
		"request_host":         pick(rng, []string{"localhost", "api.example.com", "gw.internal"}),
		"request_method":       pick(rng, []string{"GET", "POST", "PUT", "DELETE"}),
		"request_uri":          pick(rng, []string{"/llm", "/v1/chat", "/health", "/metrics"}),
		"response_http_status": pick(rng, statuses),
		"route_name":           pick(rng, []string{"llm-route", "chat-route", "health-route"}),
		"client_ip":            fmt.Sprintf("172.66.%d.%d", rng.IntN(256), rng.IntN(256)),
		"upstream_status":      pick(rng, statuses),
		"route_id":             hex16(rng),
		"control_plane_id":     hex16(rng),
		"service_id":           hex16(rng),
		"service_name":         pick(rng, []string{"ai-service", "auth-service", "billing-service"}),
		"service_port":         pick(rng, []string{"80", "443", "8080"}),
		"service_protocol":     pick(rng, []string{"http", "https"}),
	}
}

func buildLLMRequest(rng *rand.Rand) map[string]any {
	return map[string]any{
		"tokens":           fmt.Sprintf("%d", 1+rng.IntN(4000)),
		"http_status":      pick(rng, []string{"200", "200", "429", "500"}),
		"model":            pick(rng, []string{"gpt-5-nano", "gpt-5", "claude-opus", "claude-haiku"}),
		"provider":         pick(rng, []string{"openai", "anthropic", "google"}),
		"type":             pick(rng, []string{"input", "output"}),
		"control_plane_id": hex16(rng),
		"service_id":       hex16(rng),
		"route_id":         hex16(rng),
		"ai_plugin_id":     hex16(rng),
	}
}

func buildWorkload(rng *rand.Rand) map[string]any {
	regions := []string{"us-east-1", "us-west-2", "eu-central-1", "ap-southeast-1"}
	return map[string]any{
		"duration_seconds": fmt.Sprintf("%.3f", rng.Float64()*3600),
		"region":           pick(rng, regions),
		"zone":             pick(rng, []string{"a", "b", "c"}),
		"instance_type":    pick(rng, []string{"c5.4xlarge", "c5.2xlarge", "m5.large", "r5.xlarge"}),
	}
}

func buildAgentRun(rng *rand.Rand) map[string]any {
	return map[string]any{
		"agent_name": pick(rng, []string{"example-agent", "research-agent", "coder-agent", "triage-agent"}),
	}
}

// eventRow is one fully-realized event ready to Append.
type eventRow struct {
	ID         string    // user-provided idempotency id
	Type       string    // event type / type column
	Subject    string    // subject column
	Time       time.Time // event time
	Data       string    // JSON-encoded payload
	StoreRowID string    // OpenMeter-controlled ULID
	StoredAt   time.Time // persist time
}

// genCtx holds the precomputed, seed-independent inputs genEvent needs so they
// are built once per Run, not per event.
type genCtx struct {
	cfg         Config
	subjects    []string
	cumWeights  []int
	totalWeight int
	timeStart   time.Time
	spanNanos   int64
}

// genEvent deterministically materializes the event at index idx. It is a PURE
// function of (cfg.Seed, idx): each event draws from its own PCG stream seeded
// by (Seed, idx), so the stream is reproducible run-to-run for a given seed.
// Draw order: time, type-pick, payload, subject, id, store_row_id.
func (g *genCtx) genEvent(idx int) eventRow {
	rng := rand.New(rand.NewPCG(g.cfg.Seed, uint64(idx)))
	t := g.timeStart.Add(time.Duration(rng.Int64N(g.spanNanos)))
	et := selectEventType(rng, g.cfg.EventTypes, g.cumWeights, g.totalWeight)
	payload := et.BuildData(rng)
	data, _ := json.Marshal(payload)
	subject := g.subjects[rng.IntN(len(g.subjects))]
	id := hex16(rng)
	srid := storeRowID(t, rngReader{rng})
	return eventRow{
		ID:         id,
		Type:       et.Name,
		Subject:    subject,
		Time:       t,
		Data:       string(data),
		StoreRowID: srid,
		StoredAt:   t,
	}
}

// Result reports what the seeder did.
type Result struct {
	Rows            int // total rows inserted
	Duration        time.Duration
	EventsPerSecond float64
	BatchSize       int
	AsyncInsert     bool
}

// Run executes the seed against conn, inserting Config.Rows events into om_events.
func Run(ctx context.Context, conn driver.Conn, cfg Config) (Result, error) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10_000
	}
	if cfg.Rows <= 0 {
		return Result{}, fmt.Errorf("seed: Rows must be > 0")
	}
	if len(cfg.Types) == 0 || cfg.Subjects <= 0 || len(cfg.Group1) == 0 || len(cfg.Group2) == 0 {
		return Result{}, fmt.Errorf("seed: Types, Subjects, Group1, Group2 must be non-empty")
	}
	if len(cfg.EventTypes) == 0 {
		return Result{}, fmt.Errorf("seed: EventTypes must be non-empty")
	}
	// Cumulative-weight table for O(log n) type selection, built once.
	cumWeights := make([]int, len(cfg.EventTypes))
	totalWeight := 0
	for i, et := range cfg.EventTypes {
		if et.Weight <= 0 || et.BuildData == nil {
			return Result{}, fmt.Errorf("seed: EventType %q needs Weight > 0 and a BuildData fn", et.Name)
		}
		totalWeight += et.Weight
		cumWeights[i] = totalWeight
	}

	subjects := make([]string, cfg.Subjects)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("subject-%05d", i)
	}

	timeStart := cfg.TimeEnd.Add(-cfg.TimeSpan)
	g := &genCtx{
		cfg:         cfg,
		subjects:    subjects,
		cumWeights:  cumWeights,
		totalWeight: totalWeight,
		timeStart:   timeStart,
		spanNanos:   cfg.TimeSpan.Nanoseconds(),
	}
	start := time.Now()

	// Build the SETTINGS-aware INSERT statement once.
	insertSQL := "INSERT INTO om_events (namespace, id, type, subject, source, time, data, ingested_at, stored_at, store_row_id)"
	settings := map[string]any{}
	if cfg.AsyncInsert {
		settings["async_insert"] = 1
		if cfg.WaitAsyncInsert {
			settings["wait_for_async_insert"] = 1
		} else {
			settings["wait_for_async_insert"] = 0
		}
	}

	inserted := 0
	for inserted < cfg.Rows {
		batchSize := cfg.BatchSize
		if remaining := cfg.Rows - inserted; remaining < batchSize {
			batchSize = remaining
		}

		bctx := ctx
		if len(settings) > 0 {
			bctx = clickhouseSettingsCtx(ctx, settings)
		}
		batch, err := conn.PrepareBatch(bctx, insertSQL)
		if err != nil {
			return Result{}, fmt.Errorf("seed: PrepareBatch: %w", err)
		}

		for i := 0; i < batchSize; i++ {
			row := g.genEvent(inserted + i)
			err := batch.Append(
				cfg.Namespace,    // namespace
				row.ID,           // id
				row.Type,         // type
				row.Subject,      // subject
				"bench-seed",     // source
				row.Time,         // time
				row.Data,         // data
				row.StoredAt,     // ingested_at (tracks stored_at)
				row.StoredAt,     // stored_at
				row.StoreRowID,   // store_row_id
			)
			if err != nil {
				return Result{}, fmt.Errorf("seed: batch.Append: %w", err)
			}
		}
		if err := batch.Send(); err != nil {
			return Result{}, fmt.Errorf("seed: batch.Send: %w", err)
		}
		inserted += batchSize
	}

	dur := time.Since(start)
	return Result{
		Rows:            inserted,
		Duration:        dur,
		EventsPerSecond: float64(inserted) / dur.Seconds(),
		BatchSize:       cfg.BatchSize,
		AsyncInsert:     cfg.AsyncInsert,
	}, nil
}

// Subjects returns the deterministic subject pool for the given size.
// The harness uses this to pick the {subjects:Array(String)} parameter binding
// so queries hit the same subjects the seeder generated.
func Subjects(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("subject-%05d", i)
	}
	return out
}
