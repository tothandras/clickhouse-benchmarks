## Why

The 10M-row sweep crowned `materialized-columns` the lowest query-CPU table design, but that result is contaminated by an unrealistic seeder: every event carries the identical `{value, group1, group2}` payload, so three fixed `MATERIALIZED` columns happen to serve every query. Real OpenMeter `data` is user-controlled and heterogeneous — each event `type` carries its own field-set (`kong.api_request` has `request_method`/`response_http_status`, `kong.llm_request` has `tokens`/`model`/`provider`, `workload` has `duration_seconds`/`region`, `agent_run` has `agent_name`). Under that reality a fixed set of materialized columns cannot cover all types, so the "optimal table" verdict must be re-tested against data shaped like production.

## What Changes

- Extend the Go seeder (`bench/seed/`) to emit **multiple event types**, each with its own distinct JSON `data` field-set, selected per-row by a **configurable weighted mix** (realistic cardinality skew — a dominant type plus rarer ones).
- **Retain a baseline type** that emits `{value, group1, group2}` so the existing 14 baseline queries keep running unchanged; the new types are added alongside it.
- Add **per-type benchmark queries** that aggregate over each new type's real fields (e.g. sum `tokens` grouped by `model`/`provider`; count by `response_http_status`; sum `duration_seconds` by `region`), filtered by that `type`.
- **Re-run the 10M-row sweep** across all single-node table-design variants under the heterogeneous data and **re-rank** — answering whether `materialized-columns` still wins when its fixed columns only cover a fraction of event types, and updating the analysis note accordingly.
- Make the event-type catalog and weights configurable in `seed.Config` with deterministic, seedable selection so reruns across table variants compare identical data.

## Capabilities

### New Capabilities
- `heterogeneous-event-seeding`: the seeder's ability to generate events of multiple types, each with a distinct user-shaped JSON `data` payload, chosen by a deterministic weighted mix, while remaining reproducible across runs.

### Modified Capabilities
- `baseline-openmeter-scenario`: the "Synthetic event generation" requirement is broadened — the seed MAY emit heterogeneous per-type payloads, but MUST still guarantee at least one type carrying a numeric `value` plus categorical group fields so the canonical meter queries remain exercisable.

## Impact

- Code: `bench/seed/seed.go` (event-type catalog, weighted selection, per-type payload builders, `Config` fields), `bench/seed/seed_test.go` if present.
- Scenarios: new per-type query files under each scenario's `queries/` (at minimum `baseline-openmeter`; the materialized-columns scenario gains queries that must read JSON for the non-materialized types, exposing its coverage gap).
- Results: re-run sweep produces new result files; `bench/results/ANALYSIS-optimal-table.md` updated with the heterogeneous-data ranking.
- Specs: `openspec/specs/baseline-openmeter-scenario/spec.md` (modified), new `openspec/specs/heterogeneous-event-seeding/spec.md`.
- No schema change to `om_events` (still `data String`); the change is in what the seeder writes into `data`, not the column.
