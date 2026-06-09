# Tasks: add-cogs-measurement

Ordered; each task is independently committable and ends green (`go build ./... && go test ./...`).

## 1. Foundations

- [x] 1.1 Expose `seed.Generator`: thin streaming wrapper over the existing pure `genCtx.genEvent(idx)` (bench/seed/seed.go) with `Next() Event`; add the event-time override mode (perform and discard the stream's time draw, stamp caller-provided time, rebuild store_row_id from it); reimplement the bulk seeder on top; prove byte-identical seeding via a digest test at 100k rows, seed 42, and a payload-determinism test for the time override.
- [x] 1.2 Add `bench/cogs/cell.go`: manifest schema, strict JSON decode (unknown fields error), duration parsing, validation; unit tests incl. unknown-field rejection and the idle cell (`eps=0 && qps=0` legal).
- [x] 1.3 Add `bench/pricing/`: profile schema (replicas/vcpus/gib shape, rates incl. `backup_multiplier` and `egress_per_gb_public`), loader, billed-shape and cpu-linear modes as pure functions; table-driven tests with hand-computed expectations; ship `pricing/clickhouse-cloud-scale-aws-us-east-1.json` and `pricing/local-zero.json`.

## 2. Drivers

- [x] 2.1 `bench/ingest/driver.go`: token-bucket rate control, dual-threshold batching (`batch_max_rows` OR `flush_interval`), wall-clock event-time stamping via the Generator override, `log_comment` tagging, achieved-rate accounting, no-burst backpressure with `rate_satisfied`/`saturated` flags; unit tests for rate control and batching against a fake client; integration test gated on `CLICKHOUSE_DSN`.
- [x] 2.2 `bench/replay/mix.go` + `scenarios/proposal/queries/mix.json` + `scenarios/baseline-openmeter/queries/mix.json`: loader enforcing exactly-one-class-or-exclude over the on-disk `*.sql` set and erroring on names with no file; proposal manifest classifies all 34 queries (incl. `max_hour`/`min_hour`, `kong_api_request_by_method`/`by_service`/`by_all_dims`) with the two `*_no_prewhere` diagnostics in `exclude`; baseline manifest omits `lookup` class (no `lookup_by_id` on disk); both carry the placeholder-weights `notes`; tests cover unclassified, missing, and excluded queries for both scenarios' real file sets.
- [x] 2.3 `bench/replay/replayer.go`: Poisson/uniform arrivals, weighted class selection, cold fraction (`enable_filesystem_cache=0` + cold tag), concurrency cap with queue-time tracking, per-arrival sliding `from`/`to` window (UTC Unix seconds), tagging, per-class counters; deterministic tests with seeded RNG and a fake client.

## 3. Accounting

- [x] 3.1 `bench/accounting/querylog.go`: tagged aggregation from `clusterAllReplicas(default, system.query_log)` — CPU, wall, percentiles, read/written rows/bytes, `result_bytes`, peak memory, S3 gets, fs-cache hit/miss; missing-ProfileEvents tolerance; errors block from `ExceptionBeforeStart`/`ExceptionWhileProcessing`; integration test asserts nonzero CPU attribution for a tiny tagged workload.
- [x] 3.2 `bench/accounting/partlog.go`: `MergeParts` aggregation from `clusterAllReplicas(default, system.part_log)` over measure-start..drain-end; `Mutate`/`NewPart` diagnostic counts; `merge_cpu_estimated` fallback when ProfileEvents absent.
- [x] 3.3 `bench/accounting/asyncinsert.go`: flush-CPU attribution via `system.asynchronous_insert_log` correlation; `async_attribution_partial` flag on incomplete correlation; integration test with `async_insert=1`.
- [x] 3.4 `bench/accounting/flush.go` + `capacity.go` + `parts.go`: `SYSTEM FLUSH LOGS ON CLUSTER` with local-flush fallback (`log_flush: "local-only"`); capacity = replicas (from `system.clusters`) × per-replica vCPUs (`CGroupMaxCPU` with fallbacks); storage snapshots at prepare/soak-end/drain-end; non-harness-database size warning (shared-service contamination).
- [x] 3.5 Parts-plateau poller (±10% over 5 polls) with unit tests on the stability rule.

## 4. Orchestration and outputs

- [x] 4.1 `bench/cogs/runner.go`: full lifecycle wiring (prepare/soak/measure/drain/collect/price/report), `--cell`, `--profile ci`, `--skip-init`, `--require-clean`, `--usage-export`; preload with `TimeEnd` pinned to run start; service-shape cross-check vs pricing profile (`shape_mismatch` warning); SIGINT during measure produces a truncated, flagged report.
- [x] 4.2 `bench/cogs/attribution.go`: CPU split, coverage, idle residual, unit-cost card (insert/merge split, per-class warm/cold, storage with backup multiplier, egress estimate from `result_bytes`, idle-floor bound); golden tests from a fixture accounting snapshot, incl. billed-shape components summing to window cost.
- [x] 4.3 `bench/cogs/report.go`: `cogs/v1` JSON (full cell manifest + pricing profile inline, all flags) and markdown (unit-cost card lead table, attribution split, per-class warm/cold, storage, flags, mix `notes` caveat in header, reconciliation when present); golden-file tests for both.
- [x] 4.4 Register `bench cogs` / `bench cogs compare` / `bench cogs validate` in `bench/cmd/bench`; compare with latest-resolution under `bench/results/<scenario>/cogs/`, profile-mismatch guard, cross-scenario class-set reporting; validate with the 15% additivity rule naming the diverging component; fixture tests. Verify `bench --scenario` output schema is untouched.
- [x] 4.5 Ship the cell matrix in `cells/` (idle, ingest-1k/5k/25k, query-1/4/16qps, query-4qps-cold, mixed-5keps-4qps, ingest-5k-async), all defaulting to scenario `proposal`, `preload_rows` 10M, seed 42.

## 5. Reconciliation, docs, smoke

- [x] 5.1 `bench/accounting/usagefile.go` + reconcile wiring: versioned parser, measure-window row selection, billed-vs-model deltas, >20% flag; fixture test with a synthetic export.
- [x] 5.2 Docs: `bench/README.md` cogs section (CLI table in the existing style); top-level `README.md` "Measuring COGS" methodology (merge lag, cache state, `max_threads`, idle-cell semantics = awake-but-unloaded, soak/measure leakage netting, dedicated-service requirement, do-not-extrapolate-across-tiers, reconciliation delta sources, worked example); `scenarios/README.md` gains the `mix.json` contract.
- [x] 5.3 Smoke script (devenv task, not GitHub CI — repo has none): run `idle` and `mixed-5keps-4qps` with `--profile ci` and `local-zero` against the devenv ClickHouse; assert a valid `cogs/v1` result with coverage > 0 and no errors.

## 6. Acceptance verification

- [x] 6.1 `bench cogs --cell mixed-5keps-4qps --profile ci` on devenv produces JSON+md with nonzero insert, merge, and query CPU; coverage in (0,1]; complete unit-cost card; zero harness errors.
- [x] 6.2 Seeding determinism: post-refactor digest equals pre-refactor digest for identical flags.
- [x] 6.3 `local-zero`: all USD fields 0, all resource fields populated.
- [x] 6.4 Merge capture: an ingest-only cell shows merge CPU > 0 with collector merge count matching a manual `clusterAllReplicas` part_log query.
- [x] 6.5 On a 2-replica Cloud Scale service: collector results include both replicas' logs (no systematic coverage drop vs single-replica), shape cross-check passes, and `--usage-export` produces a reconciliation block.
- [x] 6.6 Perf path bit-for-bit unaffected: `bench --scenario proposal` output schema unchanged; `bench compare` unchanged.
