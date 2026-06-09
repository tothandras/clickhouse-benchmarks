// Package seed generates synthetic OpenMeter-shaped events for benchmark scenarios.
//
// The seeder produces events matching the baseline events-table shape:
// namespace + type (LowCardinality) + subject + JSON-encoded data payload.
// Each scenario uses its own table name (e.g. data_as_json_events,
// proposal_events) so scenarios coexist without clobbering each other; the
// caller passes the target table via Config.Table.
// To model real OpenMeter usage, where the data field is user-controlled and
// differs per event type, the seeder emits a weighted mix of heterogeneous
// event types (see DefaultConfig): a dominant baseline "api_request" carrying
// {value, group1, group2} (which the agg-type sweep reads), plus the canonical
// Kong meters kong.api_request (COUNT, 19 groupBy dims) and kong.llm_request
// (SUM $.tokens, 14 groupBy dims), and workload / agent_run types that each
// carry their own distinct field-set. Numeric payload fields are emitted as JSON
// strings (e.g. "tokens":"1") to match real producers, so queries extract them
// with toDecimal128OrNull over the path's stringified form (exact billing,
// any JSON-stored type).
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
	Table     string        // Target table name (e.g. data_as_json_events); REQUIRED.
	Namespace string        // Primary namespace; the default {namespace} param binds to it.
	Namespaces int          // Total namespaces to spread rows across; ≤1 means single-namespace (just Namespace).
	Types     []string      // Type names; Types[0] is the baseline type the default {type} param binds to.
	Subjects  int           // Subject pool size; ≥100 for baseline.
	Group1    []string      // Categorical group1 values (baseline api_request payload).
	Group2    []string      // Categorical group2 values (baseline api_request payload).
	EventTypes []EventType  // Weighted catalog of event types; each emits its own data shape.
	Rows      int           // Count of events to insert.
	StartRow  int           // First generator index; Run emits [StartRow, StartRow+Rows). Lets N workers seed disjoint ranges in parallel with full determinism.
	TimeSpan  time.Duration // Time window the events span, ending at TimeEnd.
	TimeEnd   time.Time     // Newest event time; events are uniform in [TimeEnd-TimeSpan, TimeEnd).
	Seed      uint64        // RNG seed for reproducibility.
	BatchSize int           // Rows per batch insert; defaults to 10k.

	// AsyncInsert sets the async_insert SETTING on each batch.
	AsyncInsert bool
	// WaitAsyncInsert sets wait_for_async_insert when AsyncInsert is true.
	WaitAsyncInsert bool

	// MixedValueStorage, when true, emits the baseline `value` path in mixed JSON
	// storage (number / stringified number / Float64-overflowing bigint) so the
	// type-agnostic value-extraction correctness fix is actually exercised on the
	// dominant path. Default false = uniform Float64 (historical distribution).
	MixedValueStorage bool
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
		Types:     []string{"api_request", "kong.api_request", "kong.llm_request", "workload", "agent_run"},
		Subjects:  100,
		Group1:    group1,
		Group2:    group2,
		EventTypes: []EventType{
			// Generic baseline carrying {value, group1, group2} — drives the
			// synthetic aggregation-type sweep (SUM/AVG/MIN/MAX/…) on $.value.
			{Name: "api_request", Weight: 50, BuildData: buildBaseline(group1, group2, false)},
			// Canonical Kong meters (key kong_konnect_api_request / _llm_tokens),
			// full declared groupBy field sets, dotted eventType names.
			{Name: "kong.api_request", Weight: 25, BuildData: buildKongAPIRequest},
			{Name: "kong.llm_request", Weight: 15, BuildData: buildLLMRequest},
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

// boundedID picks a hex16 id from a fixed pool of n, so a dimension is realistic
// (looks like an opaque id) but BOUNDED in cardinality — the way real Kong
// route/service/plugin/api ids are (a namespace has a handful, not one per
// event). Generating ids per-event instead would collapse every grouped/rollup
// benchmark to ~1× (the cardinality artifact we measured). The pool is derived
// deterministically from the seed rng's first values via a fixed sub-rng so it's
// stable across the run.
func boundedID(rng *rand.Rand, pool []string) string { return pool[rng.IntN(len(pool))] }

// idPool builds a fixed pool of n deterministic hex16 ids from a dedicated rng
// seeded by `salt`, so pools are stable and independent of event-stream rng draws.
func idPool(salt uint64, n int) []string {
	r := rand.New(rand.NewPCG(salt, 0x9e3779b97f4a7c15))
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%016x", r.Uint64())
	}
	return out
}

// Bounded id pools for the Kong meters' dimensions (cardinality chosen to model
// a multi-tenant gateway: a few control planes, dozens of routes/services, etc.).
var (
	cpPool      = idPool(1, 8)    // control planes
	routePool   = idPool(2, 20)   // routes   (each maps 1:1 to a route_name)
	servicePool = idPool(3, 8)    // services (each maps 1:1 to a service_name)
	pluginPool  = idPool(4, 12)   // ai plugins
	apiPool     = idPool(5, 30)   // apis
	apiProdPool = idPool(6, 20)   // api products
	apiVerPool  = idPool(7, 40)   // api product versions
	appPool     = idPool(8, 50)   // applications
	portalPool  = idPool(9, 6)    // portals
)

// Friendly names are stable ATTRIBUTES of a route/service, not independent random
// draws: in real Kong data a route has exactly one name. route_name is a 1:1
// function of route_id (service_name of service_id), so grouping by id then name
// adds no groups (functional dependency) and grouping by name alone yields
// exactly len(routePool)/len(servicePool) groups, like a real dashboard.
var (
	routeNames   = namesFor("route", len(routePool))
	serviceNames = namesFor("service", len(servicePool))
)

func namesFor(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%02d", prefix, i)
	}
	return out
}

func labelFor(id string, pool, labels []string) string {
	for i, p := range pool {
		if p == id {
			return labels[i]
		}
	}
	return labels[0]
}

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
//
// When mixedStorage is true the `value` path is emitted in three rotating JSON
// storage forms under the SINGLE path — a JSON number, a JSON-stringified
// number ("123.4"), and a bigint that overflows Float64 — so the harness
// exercises the type-agnostic correctness fix (toDecimal128OrNull(toString(...))
// reads every form; the typed `.:Float64` accessor reads NULL on the string and
// bigint forms). When false (default) `value` is a uniform JSON Float64, the
// historical distribution, so existing results stay comparable.
func buildBaseline(group1, group2 []string, mixedStorage bool) func(rng *rand.Rand) map[string]any {
	return func(rng *rand.Rand) map[string]any {
		var value any
		if mixedStorage {
			switch rng.IntN(3) {
			case 0:
				value = rng.Float64() * 1000 // JSON number
			case 1:
				value = fmt.Sprintf("%.4f", rng.Float64()*1000) // JSON-stringified number
			default:
				value = fmt.Sprintf("%d", uint64(1)<<60+rng.Uint64()%1000) // bigint, overflows Float64 exactness
			}
		} else {
			value = rng.Float64() * 1000
		}
		return map[string]any{
			"value":  value,
			"group1": pick(rng, group1),
			"group2": pick(rng, group2),
		}
	}
}

// Heterogeneous payloads. Numeric fields are emitted as JSON strings to match
// real OpenMeter producers, so queries must parse them via toDecimal128OrNull.
// buildKongAPIRequest emits the kong.api_request meter's full 19-dim groupBy set
// (key kong_konnect_api_request, COUNT — no valueProperty). Bounded dims come
// from fixed id pools; client_ip / request_uri / request_user_agent are kept
// genuinely high-cardinality (per-event), matching real API-gateway traffic.
func buildKongAPIRequest(rng *rand.Rand) map[string]any {
	statuses := []string{"200", "200", "200", "201", "400", "404", "500"} // skewed to 200
	routeID := boundedID(rng, routePool)
	serviceID := boundedID(rng, servicePool)
	return map[string]any{
		"api_id":                 boundedID(rng, apiPool),
		"api_product_id":         boundedID(rng, apiProdPool),
		"api_product_version_id": boundedID(rng, apiVerPool),
		"application_id":         boundedID(rng, appPool),
		"client_ip":              fmt.Sprintf("%d.%d.%d.%d", rng.IntN(256), rng.IntN(256), rng.IntN(256), rng.IntN(256)), // high-card
		"control_plane_id":       boundedID(rng, cpPool),
		"portal_id":              boundedID(rng, portalPool),
		"request_host":           pick(rng, []string{"localhost", "api.example.com", "gw.internal", "edge.kong"}),
		"request_method":         pick(rng, []string{"GET", "POST", "PUT", "DELETE", "PATCH"}),
		"request_uri":            fmt.Sprintf("/v1/%s/%d", pick(rng, []string{"chat", "llm", "users", "orders", "search"}), rng.IntN(10000)), // high-card
		"request_user_agent":     pick(rng, []string{"curl/8.4", "Mozilla/5.0", "PostmanRuntime/7.36", "python-requests/2.31", "Go-http-client/2.0", "okhttp/4.12"}),
		"response_http_status":   pick(rng, statuses),
		"route_id":               routeID,
		"route_name":             labelFor(routeID, routePool, routeNames),
		"service_id":             serviceID,
		"service_name":           labelFor(serviceID, servicePool, serviceNames),
		"service_port":           pick(rng, []string{"80", "443", "8080"}),
		"service_protocol":       pick(rng, []string{"http", "https"}),
		"upstream_status":        pick(rng, statuses),
	}
}

// buildLLMRequest emits the kong.llm_request meter's value (tokens) plus its
// full 14-dim groupBy set (key kong_konnect_llm_tokens, SUM $.tokens). All dims
// are bounded (a gateway has finite models/providers/plugins/routes), so grouped
// and rolled-up token aggregations compress realistically.
func buildLLMRequest(rng *rand.Rand) map[string]any {
	return map[string]any{
		"tokens":                 fmt.Sprintf("%d", 1+rng.IntN(4000)),
		"ai_plugin_id":           boundedID(rng, pluginPool),
		"ai_plugin_name":         pick(rng, []string{"ai-proxy", "ai-prompt-guard", "ai-rate-limit", "ai-semantic-cache"}),
		"api_id":                 boundedID(rng, apiPool),
		"api_product_id":         boundedID(rng, apiProdPool),
		"api_product_version_id": boundedID(rng, apiVerPool),
		"application_id":         boundedID(rng, appPool),
		"cache_status":           pick(rng, []string{"Hit", "Miss", "Bypass", ""}),
		"consumer_id":            boundedID(rng, appPool), // ~per-application consumer; bounded
		"control_plane_id":       boundedID(rng, cpPool),
		"http_status":            pick(rng, []string{"200", "200", "200", "429", "500"}),
		"model":                  pick(rng, []string{"gpt-5-nano", "gpt-5", "claude-opus", "claude-haiku", "gemini-2.5"}),
		"provider":               pick(rng, []string{"openai", "anthropic", "google"}),
		"route_id":               boundedID(rng, routePool),
		"service_id":             boundedID(rng, servicePool),
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

// Event is one fully-realized event ready to insert. Data is the JSON-text
// payload the writer Appends into the `data` column (`data JSON` or `data String`).
type Event struct {
	ID         string    // user-provided idempotency id
	Namespace  string    // tenant namespace
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
	namespaces  []string
	cumWeights  []int
	totalWeight int
	timeStart   time.Time
	spanNanos   int64
}

// newGenCtx validates the generation-relevant parts of cfg and builds the
// per-index generation context. Insert-side concerns (Table, Rows, BatchSize)
// are validated by Run, not here, so a streaming Generator can be built
// without them.
func newGenCtx(cfg Config) (*genCtx, error) {
	if len(cfg.Types) == 0 || cfg.Subjects <= 0 || len(cfg.Group1) == 0 || len(cfg.Group2) == 0 {
		return nil, fmt.Errorf("seed: Types, Subjects, Group1, Group2 must be non-empty")
	}
	if len(cfg.EventTypes) == 0 {
		return nil, fmt.Errorf("seed: EventTypes must be non-empty")
	}
	if cfg.TimeSpan <= 0 {
		return nil, fmt.Errorf("seed: TimeSpan must be > 0")
	}

	// When mixed value-storage is requested, rebuild the baseline (api_request)
	// builder so `value` is emitted in mixed JSON storage. Matched by name so it
	// works regardless of catalog order or a custom catalog.
	if cfg.MixedValueStorage {
		cfg.EventTypes = withMixedBaseline(cfg.EventTypes, cfg.Group1, cfg.Group2)
	}

	cumWeights := make([]int, len(cfg.EventTypes))
	totalWeight := 0
	for i, et := range cfg.EventTypes {
		if et.Weight <= 0 || et.BuildData == nil {
			return nil, fmt.Errorf("seed: EventType %q needs Weight > 0 and a BuildData fn", et.Name)
		}
		totalWeight += et.Weight
		cumWeights[i] = totalWeight
	}

	return &genCtx{
		cfg:         cfg,
		subjects:    Subjects(cfg.Subjects),
		namespaces:  NamespaceList(cfg.Namespace, cfg.Namespaces),
		cumWeights:  cumWeights,
		totalWeight: totalWeight,
		timeStart:   cfg.TimeEnd.Add(-cfg.TimeSpan),
		spanNanos:   cfg.TimeSpan.Nanoseconds(),
	}, nil
}

// genEvent deterministically materializes the event at index idx. It is a PURE
// function of (cfg.Seed, idx): each event draws from its own PCG stream seeded
// by (Seed, idx), so the stream is reproducible run-to-run for a given seed.
// Draw order: time, type-pick, payload, subject, id, store_row_id.
func (g *genCtx) genEvent(idx int) Event {
	return g.genEventTime(idx, time.Time{}, false)
}

// genEventAt materializes the event at idx with its event time overridden to t
// (live-ingest mode). The stream's own time draw is still performed and
// discarded, so every subsequent draw — type, payload, subject, id — stays
// byte-identical to the seeder's event at the same (Seed, idx). Only the
// time-derived fields differ: Time, StoredAt, and the store_row_id's
// millisecond timestamp prefix (its entropy comes from the unchanged stream).
func (g *genCtx) genEventAt(idx int, t time.Time) Event {
	return g.genEventTime(idx, t, true)
}

func (g *genCtx) genEventTime(idx int, overrideTime time.Time, override bool) Event {
	rng := rand.New(rand.NewPCG(g.cfg.Seed, uint64(idx)))
	t := g.timeStart.Add(time.Duration(rng.Int64N(g.spanNanos)))
	if override {
		t = overrideTime
	}
	et := selectEventType(rng, g.cfg.EventTypes, g.cumWeights, g.totalWeight)
	payload := et.BuildData(rng)
	data, _ := json.Marshal(payload)
	subject := g.subjects[rng.IntN(len(g.subjects))]
	id := hex16(rng)
	srid := storeRowID(t, rngReader{rng})
	// Namespace draw is LAST and skipped entirely for the single-namespace case,
	// so multi-namespace seeding does not shift the RNG stream of any earlier
	// field — single-namespace data stays byte-identical to pre-namespace runs.
	namespace := g.cfg.Namespace
	if len(g.namespaces) > 1 {
		namespace = g.namespaces[rng.IntN(len(g.namespaces))]
	}
	return Event{
		ID:         id,
		Namespace:  namespace,
		Type:       et.Name,
		Subject:    subject,
		Time:       t,
		Data:       string(data),
		StoreRowID: srid,
		StoredAt:   t,
	}
}

// withMixedBaseline returns a copy of types with the "api_request" entry's
// BuildData replaced by a mixed-value-storage builder. The original slice is
// left untouched.
func withMixedBaseline(types []EventType, group1, group2 []string) []EventType {
	out := make([]EventType, len(types))
	copy(out, types)
	for i := range out {
		if out[i].Name == "api_request" {
			out[i].BuildData = buildBaseline(group1, group2, true)
		}
	}
	return out
}

// Generator streams the seeder's deterministic events one at a time. It is a
// thin cursor over the same pure per-index generation Run uses, so a Generator
// and the bulk seeder produce byte-identical events for identical Config.
// Not safe for concurrent use; give each goroutine its own Generator.
type Generator struct {
	ctx *genCtx
	idx int
}

// NewGenerator validates the generation-relevant parts of cfg (Table, Rows,
// and BatchSize are insert-side concerns and may be zero) and returns a
// streaming generator positioned at index 0.
func NewGenerator(cfg Config) (*Generator, error) {
	ctx, err := newGenCtx(cfg)
	if err != nil {
		return nil, err
	}
	return &Generator{ctx: ctx}, nil
}

// Next returns the event at the cursor and advances it.
func (g *Generator) Next() Event {
	e := g.ctx.genEvent(g.idx)
	g.idx++
	return e
}

// NextAt returns the event at the cursor with its event time overridden to t
// (live-ingest mode; see genEventAt for the determinism contract) and
// advances the cursor.
func (g *Generator) NextAt(t time.Time) Event {
	e := g.ctx.genEventAt(g.idx, t)
	g.idx++
	return e
}

// At returns the event at idx without moving the cursor (pure indexed access).
func (g *Generator) At(idx int) Event {
	return g.ctx.genEvent(idx)
}

// Index reports the cursor position (the index Next/NextAt will emit next).
func (g *Generator) Index() int { return g.idx }

// Seek moves the cursor so Next/NextAt emit from idx onward. Combined with
// disjoint ranges this lets parallel workers split a seed deterministically.
func (g *Generator) Seek(idx int) { g.idx = idx }

// Result reports what the seeder did.
type Result struct {
	Rows            int // total rows inserted
	Duration        time.Duration
	EventsPerSecond float64
	BatchSize       int
	AsyncInsert     bool
}

// Run executes the seed against conn, inserting Config.Rows events into Config.Table.
func Run(ctx context.Context, conn driver.Conn, cfg Config) (Result, error) {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 10_000
	}
	if cfg.Rows <= 0 {
		return Result{}, fmt.Errorf("seed: Rows must be > 0")
	}
	if cfg.Table == "" {
		return Result{}, fmt.Errorf("seed: Table must be set")
	}

	if cfg.StartRow < 0 {
		return Result{}, fmt.Errorf("seed: StartRow must be >= 0")
	}
	gen, err := NewGenerator(cfg)
	if err != nil {
		return Result{}, err
	}
	gen.Seek(cfg.StartRow)
	start := time.Now()

	// Build the SETTINGS-aware INSERT statement once.
	insertSQL := fmt.Sprintf("INSERT INTO %s (namespace, id, type, subject, source, time, data, ingested_at, stored_at, store_row_id)", cfg.Table)
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

		for range batchSize {
			row := gen.Next()
			err := batch.Append(
				row.Namespace,    // namespace
				row.ID,           // id
				row.Type,         // type
				row.Subject,      // subject
				"bench-seed",     // source
				row.Time,         // time
				row.Data,         // data (JSON text)
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

// NamespaceList returns the deterministic namespace pool of size n with primary
// at index 0. n ≤ 1 yields just [primary] (single-namespace, current behavior).
// The extra namespaces are named ns-00001, ns-00002, … so the primary the
// {namespace} param binds to stays a real, recognizable tenant in the mix.
func NamespaceList(primary string, n int) []string {
	if n <= 1 {
		return []string{primary}
	}
	out := make([]string, n)
	out[0] = primary
	for i := 1; i < n; i++ {
		out[i] = fmt.Sprintf("ns-%05d", i)
	}
	return out
}
