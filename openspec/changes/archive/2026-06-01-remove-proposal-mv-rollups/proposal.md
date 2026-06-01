## Why

The `proposal` scenario's two materialized-view rollups did not earn their
keep: the `kong.api_request` rollup was already a documented negative result
(≈1.0× compression — one rollup row per event), and carrying per-meter MVs
contradicts the project's core constraint that the table stay meter-agnostic
for the unbounded set of unknown-schema meters. Removing them simplifies the
recommended design to the table-level levers that did reproduce as wins
(`data JSON` + `CODEC(ZSTD(3))` + `bloom_filter` on `id`), measured uniformly
against the base table with no per-meter machinery.

## What Changes

- **BREAKING** Remove both materialized-view rollups from the `proposal`
  scenario: drop the `proposal_llm_tokens_rollup` + `proposal_llm_tokens_mv`
  (dims-full LLM SUM rollup) and the `proposal_api_request_rollup` +
  `proposal_api_request_mv` (dims-bounded api COUNT rollup) from
  `scenarios/proposal/init.sql`. The base `proposal_events` table (JSON +
  ZSTD(3) + bloom-on-id) is unchanged.
- Delete the three queries that read from the dropped rollups:
  `kong_llm_tokens_total_hybrid.sql`, `kong_api_request_total_hybrid.sql`, and
  `kong_status_by_route_rollup.sql`.
- Keep all base-table Kong queries (`kong_llm_tokens_total`,
  `kong_api_request_total`, `kong_api_request_by_method`,
  `kong_api_request_by_service`, `kong_api_request_by_all_dims`,
  `kong_status_by_route`, `lookup_by_id`), so the proposal still benchmarks the
  JSON+ZSTD+bloom design on the Kong workloads — just without rollup
  acceleration. Update their comments that reference the now-removed rollup/
  hybrid siblings.
- Delete `scenarios/proposal/meters.yaml` (its stated purpose was to be the
  rollups' source of truth, which no longer exists).
- Refresh `scenarios/README.md` (and any proposal-scenario prose in the
  top-level `README.md`) to describe the proposal as the table-level design
  only, dropping the rollup / 3-part-hybrid narrative.

## Capabilities

### New Capabilities
<!-- none -->

### Modified Capabilities
- `proposal-scenario`: remove the requirements describing the known-meter MV
  rollups, the 3-part hybrid billing-exact reads, and `meters.yaml` as the
  rollups' source of truth. Retain the "Recommended all-in-one events table"
  requirement (JSON + ZSTD(3) + bloom-on-id, meter-agnostic base table).

## Impact

- Scenario files: `scenarios/proposal/init.sql` (drop 2 tables + 2 MVs),
  `scenarios/proposal/queries/` (delete 3 rollup-served `.sql` files, edit
  comments on the surviving Kong queries), `scenarios/proposal/meters.yaml`
  (delete), `scenarios/README.md`, top-level `README.md`.
- Specs: `proposal-scenario` (remove 3 of its 4 requirements).
- No harness/Go code changes: the rollups were pure scenario DDL/SQL; the
  runner, seeder, value-parity, and compare paths are untouched. Existing
  committed `bench/results/proposal/*` files that measured rollup queries
  remain as historical record; a fresh run will simply no longer emit those
  query rows.
