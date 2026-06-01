# proposal-scenario Specification

## Purpose

Define the `proposal` scenario: the repo's recommended all-in-one ClickHouse
table design for OpenMeter usage metering on a single node. It stacks the
table-design levers measured here as composable wins (`data JSON` +
`CODEC(ZSTD(3))` + a `bloom_filter` on `id`) on top of the unchanged upstream
OpenMeter schema, and adds live materialized-view rollups for the two
known-schema Kong meters — the sanctioned per-meter exception to an otherwise
meter-agnostic base table. The scenario is the head-to-head candidate the
benchmark harness compares against `baseline-openmeter`, and `meters.yaml` is
the source of truth its rollups implement.

## Requirements
### Requirement: Recommended all-in-one events table

The `proposal` scenario SHALL define a single MergeTree events table
(`proposal_events`) that stacks the table-design levers this repo measured as
composable wins, while keeping the upstream OpenMeter column set, `PARTITION BY
toYYYYMM(time)`, `ORDER BY (namespace, type, subject, toStartOfHour(time))`, and
the minmax skip index on `stored_at`. The stacked levers SHALL be: the `data`
column stored as native `JSON` (rather than `String`), compressed with
`CODEC(ZSTD(3))`, and a `bloom_filter` skip index on `id` for point lookups. The
table SHALL remain meter-agnostic — it SHALL NOT encode per-meter knowledge
(typed value columns, per-meter paths) — so it serves the unbounded set of
unknown-schema meters uniformly.

#### Scenario: Proposal table stacks the measured levers
- **WHEN** `SHOW CREATE TABLE proposal_events` is run after `init.sql` applies
- **THEN** the `data` column is `JSON` with `CODEC(ZSTD(3))`, a `bloom_filter`
  skip index exists on `id`, and the column set, partition expression, order-by
  tuple, and `stored_at` minmax index match upstream OpenMeter

### Requirement: Known-meter materialized-view rollups

The scenario SHALL include live materialized-view rollups for exactly the two
known-schema Kong meters, maintained incrementally as events arrive — the
sanctioned per-meter exception to the meter-agnostic base table (two known
meters, not a per-meter fan-out across thousands of meters). The
`kong.llm_request` rollup SHALL be dims-full: it carries every declared groupBy
dimension as a typed column plus a `sumState` of `$.tokens` (integer token
counts accumulated in `UInt64` state, cast to `Decimal128` at read time), so it
serves both the total-period token SUM and dimension-filtered queries. The
`kong.api_request` rollup SHALL be a dims-bounded `countState` rollup keyed by
namespace/subject/window plus its bounded groupBy dimensions, excluding the
genuinely high-cardinality dimensions (`client_ip`, `request_uri`,
`request_user_agent`); grouped queries needing an excluded dimension fall back
to the base table. The base `proposal_events` table SHALL remain the source the
rollups read from.

#### Scenario: LLM rollup serves total and dim-filtered token sums
- **WHEN** the LLM-token rollup is queried for a subject over a window
- **THEN** it returns the summed `$.tokens` from `sumState`/`sumMerge` (cast to
  `Decimal128`), and the same rollup can be grouped by any of its carried
  dimensions without scanning the base table

#### Scenario: API-request rollup is retained as a documented negative result
- **WHEN** the `kong.api_request` dims-bounded rollup is materialized over the
  seeded events
- **THEN** it does not meaningfully compress (its row count approaches the
  api_request event count, because the bounded ID dimensions cross-multiply past
  the number of events), and this outcome is recorded as a deliberate negative
  result — the dims-free rollup keyed on (namespace, subject, window) alone is
  noted as the design that actually pays off

### Requirement: Billing-exact hybrid reads for arbitrary windows

The scenario SHALL provide, for the two rolled-up meters, a 3-part hybrid query
that reads an arbitrary, non-hour-aligned `from`/`to` exactly. The hybrid query
SHALL read the raw base table for the sub-hour head, the hour-granular rollup
for the aligned interior, and the raw base table for the sub-hour tail. This
makes the rollup-accelerated read billing-exact for arbitrary boundaries while
still using the pre-aggregated interior for the bulk of the range.

#### Scenario: Hybrid query is exact across non-aligned boundaries
- **WHEN** a hybrid query runs for a `from`/`to` that does not fall on hour
  boundaries
- **THEN** it sums the raw events in the partial head and tail hours and the
  rollup state for the fully-covered interior hours, producing the same total a
  raw scan of the base table over the identical window would produce

### Requirement: Canonical meter definitions as the rollups' source of truth

The scenario SHALL declare its two known meters in `scenarios/proposal/meters.yaml`
in OpenMeter meter-definition form (`key`, `aggregation`, `eventType`,
`valueProperty` where applicable, and the full `groupBy` path map). This file is
the source of truth the materialized-view rollups in `init.sql` implement: each
rollup pre-aggregates exactly that meter's `aggregation` over its `valueProperty`,
keyed so the declared `groupBy` paths are queryable.

#### Scenario: meters.yaml matches the rollups it documents
- **WHEN** `meters.yaml` and `init.sql` are compared
- **THEN** the `kong_konnect_llm_tokens` meter (SUM `$.tokens`, 14 groupBy
  dimensions) corresponds to the dims-full LLM rollup and the
  `kong_konnect_api_request` meter (COUNT, 19 groupBy dimensions) corresponds to
  the dims-bounded api-request rollup, with the rollups' carried columns drawn
  from the meters' declared `groupBy` paths

