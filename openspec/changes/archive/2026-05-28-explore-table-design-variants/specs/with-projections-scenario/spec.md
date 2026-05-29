## ADDED Requirements

### Requirement: Events table carries two projections

The scenario SHALL create an `om_events` MergeTree table identical to `baseline-openmeter-scenario`'s table PLUS two projections defined as part of the table:

- `proj_by_stored_at`: `SELECT * ORDER BY (stored_at)` — accelerates ingestion-time lookups.
- `proj_by_store_row_id`: `SELECT * ORDER BY (store_row_id)` — accelerates point lookups by upstream row id.

Column definitions, the minmax skip index on `stored_at`, `PARTITION BY toYYYYMM(time)`, and the base `ORDER BY (namespace, type, subject, toStartOfHour(time))` MUST be unchanged so the variant isolates only the projection additions. Both projections MUST be `SELECT *` so the variant tests pure access-pattern acceleration without conflating it with column selection.

#### Scenario: Schema parity except projection definitions
- **WHEN** `SHOW CREATE TABLE om_events` is run after `init.sql` applies
- **THEN** every column, the skip index, the PARTITION BY, and the base ORDER BY match the baseline, and two `PROJECTION` clauses named `proj_by_stored_at` and `proj_by_store_row_id` are present with the documented `SELECT *` and ORDER BY definitions

#### Scenario: Projections materialize after seeding
- **WHEN** the seed completes for this scenario
- **THEN** `system.projection_parts` contains parts for both `proj_by_stored_at` and `proj_by_store_row_id` for the `om_events` table

### Requirement: Queries reused verbatim from baseline

The scenario's `queries/` directory SHALL contain the same query files as `scenarios/baseline-openmeter/queries/`, byte-for-byte identical. The query planner SHALL pick a projection automatically when appropriate; reusing baseline queries unchanged ensures any latency difference is attributable to projection availability rather than query rewriting.

#### Scenario: Query file equivalence with baseline
- **WHEN** any query file in the scenario is diffed against its counterpart in `scenarios/baseline-openmeter/queries/`
- **THEN** the files are byte-for-byte identical

### Requirement: Ingest cost is measurable

The scenario's result file SHALL include an `ingest` block (the same shape baseline emits) so the storage and write-amplification cost of carrying two projections is directly comparable to baseline. The harness's existing `IngestResult` reporting satisfies this without code changes; the requirement here is that the scenario MUST be exercised by the same harness invocation that exercises baseline (same `--rows`, same `--seed`, same `--batch-size`) so the comparison is fair.

#### Scenario: Identical seed parameters to baseline run
- **WHEN** the scenario is run as part of a comparison sweep
- **THEN** it is invoked with the same `--rows`, `--seed`, `--batch-size`, `--async-insert`, and `--wait-async` flags as the baseline run in the same sweep
