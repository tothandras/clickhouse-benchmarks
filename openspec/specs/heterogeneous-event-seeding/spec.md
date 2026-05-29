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
