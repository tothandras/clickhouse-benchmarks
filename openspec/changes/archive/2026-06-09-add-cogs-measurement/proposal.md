# COGS Measurement for OpenMeter on ClickHouse Cloud

## Why

The harness answers query-performance questions (CPU p50, value parity) but cannot answer the COGS questions the OpenMeter cloud product needs: what ingesting 1M events costs **including background merge amplification** (merges run outside `system.query_log`, so current results structurally cannot see them), what a production-mix meter query costs per class (the 30-query set spans ~20x CPU spread, so a blended number is useless), what the idle floor is (ClickHouse Cloud bills compute per active minute in 8 GiB RAM increments; at low utilization the floor dominates unit economics), and whether COGS scales linearly with events/sec and QPS or whether inserts, merges, and queries interfere.

## What Changes

- New subcommand `bench cogs --cell <name>`: runs one **workload cell** (steady-rate ingest driver + weighted query replayer) through phases prepare → soak → measure → drain → collect → price → report, writing `bench/results/<scenario>/cogs/<timestamp>.{json,md}`.
- New subcommands `bench cogs compare <run-a> <run-b>` (unit-cost diff) and `bench cogs validate` (CPU-additivity check across ingest-only / query-only / mixed cells).
- New packages under `bench/`: `ingest/` (rate-controlled ingest driver), `replay/` (weighted query replayer), `accounting/` (system-table collectors, **multi-replica aware** via `clusterAllReplicas`), `pricing/` (cost model), `cogs/` (cell lifecycle + report).
- New on-disk contracts: `cells/<name>.json` (workload cell manifests), `scenarios/<name>/queries/mix.json` (per-scenario query class/weight/exclude manifest), `pricing/<profile>.json` (pricing profiles).
- Seeder refactor: expose the existing pure indexed generator (`genEvent(idx)`) as a streaming `seed.Generator` with a policy-driven event-time override; byte-identical seeding for identical flags is preserved.
- Optional reconciliation: `--usage-export <file>` reconciles model-priced cost against a ClickHouse Cloud usage export (input-file based; no Cloud API client).
- No changes to existing scenarios' `init.sql`/`queries/*.sql` semantics; the existing `bench --scenario ...` perf path is untouched.

### Non-goals

- No ClickHouse Cloud service provisioning/resizing automation (operator pins autoscaling manually; follow-up change).
- No ClickPipes cost modeling (OpenMeter's sink is its own Kafka→ClickHouse connector); pricing config reserves a field.
- No measured data-transfer/egress in v1 (estimated from `result_bytes` at a config-declared rate, clearly marked estimate).
- No backup-bytes measurement in v1 (no system-table source on Cloud); backup cost is a config-declared multiplier on compressed bytes, clearly marked estimate.
- No GitHub CI (the repo has none); the smoke test is a devenv-run script.

## Capabilities

### New Capabilities

- `cogs-run-lifecycle`: phased workload-cell execution (prepare/soak/measure/drain/collect/price/report), attribution windows, plateau and saturation flags, clean SIGINT truncation.
- `cogs-resource-accounting`: complete CPU attribution to {insert, merge, query, idle} from `system.query_log` + `system.part_log` across **all replicas**, structured `log_comment` tagging, coverage metric, storage and capacity collectors.
- `cogs-workload-cells`: cell manifest contract (ingest rate/batching/async, query qps/arrival/mix/cold-fraction), event-time policy for live ingest, default cell matrix.
- `cogs-query-mix`: per-scenario `mix.json` contract — classes, weights, explicit excludes for diagnostic queries; strict validation against the on-disk query set.
- `cogs-pricing`: named pricing profiles, billed-shape and cpu-linear modes, unit-cost outputs ($/1M events with insert/merge split, $/1k queries per class warm/cold, storage $/GB-month, idle floor), service-shape cross-check.
- `cogs-comparison`: `cogs compare` across runs (profile-mismatch guard) and `cogs validate` additivity check.
- `cogs-reconciliation`: input-file-based reconciliation against Cloud usage exports with delta flagging.

### Modified Capabilities

- `heterogeneous-event-seeding`: the seeder's generator is exposed for streaming use with an event-time override; the requirement that identical flags produce byte-identical seeded data is unchanged and re-verified.

## Impact

- `bench/cmd/bench/main.go`: registers `cogs` subcommands; existing flags/output untouched (acceptance: perf result schema bit-for-bit unchanged).
- `bench/seed/`: extract `Generator`; seeder becomes a consumer of it.
- New top-level dirs: `cells/`, `pricing/`; new `scenarios/<name>/queries/mix.json` (safe: query discovery only globs `*.sql`).
- Results tree gains `bench/results/<scenario>/cogs/`.
- Runs target the devenv ClickHouse (smoke) and a dedicated ClickHouse Cloud service (real measurements); accounting must work on both single-node and multi-replica deployments.
