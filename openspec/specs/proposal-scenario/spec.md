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

