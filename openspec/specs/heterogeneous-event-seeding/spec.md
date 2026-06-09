# heterogeneous-event-seeding Specification

## Purpose

Define the seeder's ability to populate `om_events` with events of multiple distinct `type` values, each carrying its own user-shaped JSON `data` field-set, selected per-row by a deterministic, configurable weighted mix. This models production OpenMeter data — where `data` is user-controlled and heterogeneous across types — so that table-design tuning experiments are evaluated against realistic payload variety while remaining byte-reproducible across runs and table-design variants.

## Requirements

### Requirement: Multiple event types with distinct payloads

The seeder SHALL support generating events of multiple distinct `type` values, where each type produces its own JSON `data` field-set rather than a single uniform shape. The catalog SHALL include at least four heterogeneously-shaped types modeling realistic OpenMeter usage (e.g. an HTTP/API-request type with request/response fields, an LLM-request type with token/model/provider fields, a workload type with duration/region fields, and an agent-run type with an agent-name field), in addition to a retained baseline type carrying a numeric `value` plus categorical group fields. Each type's `data` SHALL be a parseable JSON object using that type's own keys.

#### Scenario: Each type emits its own field-set
- **WHEN** the seeder runs with the default event-type catalog
- **THEN** rows of the LLM-request type contain LLM fields (e.g. `tokens`, `model`, `provider`) and NOT the baseline `value`/`group1`/`group2`, and rows of the workload type contain workload fields (e.g. `duration_seconds`, `region`) — each type's `data` carries only its own keys

#### Scenario: Baseline type retained
- **WHEN** the default catalog is used
- **THEN** at least one type emits `data` containing a numeric `value` and categorical `group1`/`group2`, so the canonical baseline meter queries remain exercisable

### Requirement: Weighted, deterministic type selection

The seeder SHALL assign each row an event type by a configurable weighted distribution, so the type mix can model realistic cardinality skew (dominant types plus rarer ones). Selection SHALL be driven by the same seedable RNG used for payloads, such that two runs with the same seed and the same catalog produce byte-identical rows (same types in the same order with the same payloads).

#### Scenario: Weights control the mix
- **WHEN** the catalog assigns the baseline type a higher weight than the others
- **THEN** over a large seed run the baseline type accounts for a correspondingly larger share of rows than each lower-weighted type

#### Scenario: Reproducible across runs
- **WHEN** the seeder runs twice with identical seed, row count, and catalog
- **THEN** the two runs produce identical rows — same per-row type assignment and identical `data` payloads — so table-design variants compare against the same data

### Requirement: Numeric payload fields preserve query-shape fidelity

Numeric-valued payload fields SHALL be emitted as JSON strings (e.g. `"tokens": "1"`, `"response_http_status": "200"`), matching the shape real OpenMeter producers emit, so that queries reading them MUST apply the same `toFloat64OrNull(JSON_VALUE(...))` extraction the upstream meter-query path uses. This preserves the JSON-parse cost that the table-design experiment measures.

#### Scenario: Numeric fields are string-typed in JSON
- **WHEN** an LLM-request or workload row is generated
- **THEN** its numeric fields (`tokens`, `duration_seconds`, status codes) appear as quoted JSON strings, requiring `toFloat64OrNull`/`JSON_VALUE` to aggregate — not as bare JSON numbers

### Requirement: Mixed JSON storage for the value path

The seeder SHALL support emitting the canonical `$.value` path in mixed JSON storage under a single path — a deterministic split of JSON number, JSON-stringified number (`"123.4"`), and a bigint that overflows `Float64` — so the harness actually exercises the README's headline correctness fix on the dominant meter path. Today `value` is a uniform JSON `Float64` (`buildBaseline` emits `rng.Float64() * 1000`), which makes `data.value.:Float64` and `toDecimal128OrNull(toString(data.value))` provably identical on the seeded data, so the type-agnostic fix is never tested on the path that carries 50% of rows. Mixed storage SHALL be gated behind config, defaulting to the current uniform distribution so committed result files stay comparable until intentionally re-run.

#### Scenario: Value emitted in three storage forms
- **WHEN** the seeder runs with mixed value-storage enabled
- **THEN** rows carry `value` as JSON number, JSON-stringified number, and Float64-overflowing bigint in a deterministic split, such that `toDecimal128OrNull(toString(data.value))` reads every form while `data.value.:Float64` reads NULL on the string and bigint rows

#### Scenario: Uniform distribution remains the default
- **WHEN** mixed value-storage is not requested
- **THEN** the seeder emits `value` as a uniform JSON Float64 exactly as before, so existing committed results remain reproducible

### Requirement: Multi-namespace seeding

The seeder SHALL support assigning rows across a configurable number of namespaces (default 1 — current behavior), deterministically from the RNG seed and row index. Real OpenMeter is multi-tenant via `namespace`. Because `namespace` is the first ORDER BY key, the primary sparse index prunes to one tenant's contiguous range before the `bloom_filter` on `id` runs; the current single-namespace bench (whole table = one namespace) is therefore plausibly the bloom's best case and may overstate its production benefit. Measuring across multiple namespaces isolates the bloom's marginal benefit on top of the namespace-prefix pruning, which a single-namespace table cannot.

#### Scenario: Rows distributed deterministically across namespaces
- **WHEN** the seeder runs with a namespace count greater than 1
- **THEN** rows are assigned namespaces deterministically from `(seed, index)`, the distribution matches the requested count, and reruns with the same seed produce identical assignments

#### Scenario: Single namespace remains the default
- **WHEN** no namespace count is configured
- **THEN** the seeder writes a single namespace exactly as before, and the default query parameter binding continues to target it

### Requirement: Streaming generator with preserved determinism
The seeder's event generation SHALL be exposed as a streaming generator usable by the ingest driver, producing the event at any index as a pure function of (seed, index). The bulk seeder SHALL be reimplemented on top of it, and seeded output SHALL remain byte-identical for identical flags (verified via the existing digest machinery). The generator SHALL support an event-time override that performs and discards the stream's time draw so all other fields remain deterministic.

#### Scenario: refactor preserves seeded output
- **WHEN** a table is seeded with identical flags before and after the generator extraction
- **THEN** the seeded-table digests are identical

#### Scenario: time override preserves payloads
- **WHEN** the generator produces event (seed=42, idx=N) with and without the time override
- **THEN** type, payload, subject, and id are identical; only time-derived fields differ
