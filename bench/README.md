# bench

Go-based benchmark driver. Discovers scenarios under `scenarios/`, runs each
scenario's `init.sql`, seeds via `bench/seed/`, then delegates per-query
measurement to the official `clickhouse-benchmark` CLI and writes a structured
JSON result file to `bench/results/<scenario>/`.

The Go binary handles scenario lifecycle (discovery, DDL apply, seeding,
parameter rendering, result file assembly). `clickhouse-benchmark` handles
the actual timing — server-side, with real concurrency, and richer percentiles
than a hand-rolled loop would give us.

## Quick start

```bash
# Any reachable ClickHouse + the `clickhouse-benchmark` binary on PATH
# (the devenv provides both via the `clickhouse` package):
export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"

go build -o bin/bench ./bench/cmd/bench
./bin/bench --scenario baseline-openmeter
```

## CLI

| Flag              | Default          | Purpose                                                          |
| ----------------- | ---------------- | ---------------------------------------------------------------- |
| `--dsn`           | `$CLICKHOUSE_DSN`| Override the connection DSN.                                     |
| `--scenario`      | all              | Repeatable. Run only the named scenario(s).                      |
| `--scenarios-dir` | `scenarios`      | Where to discover scenarios.                                     |
| `--results-dir`   | `bench/results`  | Where to write result JSON files.                                |
| `--iterations`    | `10`             | Measured iterations per query (`clickhouse-benchmark -i`).       |
| `--concurrency`   | `1`              | Concurrency level(s), e.g. `1,8,16`; each measured separately.   |
| `--cold-paired`   | `false`          | Measure each query warm AND cold (`enable_filesystem_cache=0`).  |
| `--repeat`        | `1`              | Run the query set N times (reuses seed) for run-to-run variance. |
| `--require-clean` | `false`          | Refuse to run if the harness git tree is dirty.                  |
| `--rows`          | `1_000_000`      | Rows for the Go seeder.                                          |
| `--batch-size`    | `10_000`         | INSERT batch size for the Go seeder.                             |
| `--async-insert`  | `false`          | Set `async_insert=1` on seed inserts.                            |
| `--wait-async`    | `false`          | Set `wait_for_async_insert=1` (only with `--async-insert`).      |
| `--seed`          | `42`             | RNG seed for deterministic data generation.                      |
| `--namespaces`    | `1`              | Spread seeded rows across N namespaces (multi-tenant table).     |
| `--mixed-value`   | `false`          | Emit baseline `value` as mixed JSON (number/string/bigint).      |
| `--skip-seed`     | `false`          | Skip seeding (run queries against existing data).                |

`bench compare <baseline> <candidate>` reads the latest result JSON from each
scenario and prints the per-query p50/CPU delta table (the comparison tables in
the top-level README are otherwise maintained by hand). It compares the warm,
lowest-concurrency measurement on each side so the diff stays apples-to-apples.

## Dependency: `clickhouse-benchmark`

The harness shells out to `clickhouse-benchmark` for every query measurement.
The binary ships with the `clickhouse` package the devenv installs, so
`direnv allow` (or `devenv shell`) is enough. The harness checks for it at
startup and fails fast with a pointer to the dev shell if missing.

Why delegate instead of timing in-process:

- **Server-side timing.** We measure what ClickHouse spent, not the Go
  driver's deserialization overhead. For the baseline meter queries, the
  difference was ~30-50% — clickhouse-benchmark p50 ≈ 4ms where the Go
  loop reported ≈ 6ms.
- **Real concurrency.** `-c N` measures throughput under realistic concurrent
  load, not single-stream best-case.
- **Richer percentiles.** p0/p10/.../p90/p95/p99/p99.9/p99.99 plus QPS,
  RPS, MiB/s, result RPS, result MiB/s — all reported, all recorded.
- **Production-hardened.** Years of use, with richer statistics than a
  hand-rolled timing loop.

The cost is one shell-out per query. Negligible compared to the query's own
runtime.

## Where results land

```
bench/results/
  <scenario>/
    2026-01-15T08-42-13Z.json
    2026-01-15T09-12-44Z.json
    ...
```

Each file contains:

- `scenario`, `harness_commit` (with `-dirty` suffix on uncommitted trees), `started_at`, `finished_at`, `concurrency`
- `cluster` — ClickHouse `version()`, `is_single_node` flag, list of clusters found in `system.clusters`
- `ingest` — rows, duration, events/sec, batch settings (omitted if `--skip-seed`)
- `queries[]` — each entry:
  - `name`, `concurrency`, `iterations`
  - `p0_sec` ... `p99_99_sec` — server-side latency percentiles in seconds
  - `qps`, `rps`, `mib_per_sec`, `result_rps`, `result_mib_per_sec` — throughput
  - `error` (only on failure)

Result files are self-describing — a reviewer can determine which scenario
produced them, against which ClickHouse version, from which harness commit
(clean or dirty), at what concurrency, and when, without needing any other
file.

## Authoring scenarios

See `scenarios/README.md` for the on-disk contract. The harness binds a
fixed v1 parameter set defined in `bench/cmd/bench/main.go` (`defaultParams`),
rendered to SQL literals before each `clickhouse-benchmark` invocation.
Scenarios that conform to the baseline OpenMeter event shape (namespace +
type + subject + JSON `data.value`) work out of the box. A per-scenario
`params.json` manifest is a future change once a second variant needs
different bindings.

## COGS runs (`bench cogs`)

The perf path above answers *which design is fastest*; `bench cogs` answers
*what the workload costs*. It runs one **workload cell** — a steady-rate
ingest driver (paced, sink-style batching) plus a weighted query replayer —
through prepare → soak → measure → drain → collect → price → report, and
writes `bench/results/<scenario>/cogs/<timestamp>.{json,md}` with a unit-cost
card ($/1M events with insert/merge split, $/1k queries per class warm/cold,
storage $/GB-month, idle floor).

| Flag                | Default          | Purpose                                                            |
| ------------------- | ---------------- | ------------------------------------------------------------------ |
| `--cell`            | (required)       | Cell name resolved in `--cells-dir`, or a manifest path.           |
| `--profile`         | (manifest)       | `ci` shortens soak/measure/drain to 2m/3m/1m for smoke runs.       |
| `--skip-init`       | `false`          | Reuse the existing table and data (skips `init.sql` and preload).  |
| `--require-clean`   | `false`          | Refuse to run if the harness git tree is dirty.                    |
| `--cells-dir`       | `cells`          | Where cell manifests live.                                         |
| `--pricing-dir`     | `pricing`        | Where pricing profiles live.                                       |
| `--pricing-profile` | (manifest)       | Override the cell's profile (e.g. `local-zero`).                   |
| `--preload-rows`    | (manifest)       | Override preload size (smoke runs).                                |
| `--preload-workers` | `4`              | Parallel preload connections (disjoint generator index ranges, deterministic). Remote targets also want `compress=lz4` in the DSN — bulk seeding is usually uplink-bound. |
| `--usage-export`    | —                | Reconcile against a Cloud usage export (`cogs-usage/v1`).          |
| `--results-dir`     | `bench/results`  | Where to write result files.                                       |
| `--scenarios-dir`   | `scenarios`      | Where to discover scenarios.                                       |

Subcommands:

- `bench cogs compare <run-a> <run-b>` — diff two runs' unit costs (args are
  result paths or scenario names resolving to the latest run). Cross-profile
  price comparison is refused unless `--allow-profile-mismatch`, which limits
  the diff to resource lines (CPU sec, bytes/event, coverage).
- `bench cogs validate <ingest-only> <query-only> <mixed>` — check cpu-linear
  additivity (±15% per component); PASS means a linear per-tenant COGS formula
  holds at those rates, FAIL names the interfering component.
- `bench cogs reconcile <run> <usage-file> [--write]` — after-the-fact
  reconciliation of a finished run against a Cloud usage export: either
  `cogs-usage/v1` JSON (interval unit-hours) or the Cloud console's
  usage-statement CSV (daily dollar rows, converted via the run's pricing
  profile rate; daily granularity makes sub-day windows indicative only).
  `--write` embeds the block into the result JSON and regenerates the
  markdown.

Unlike the perf path, the cogs replayer does NOT shell out to
`clickhouse-benchmark`: it needs per-query tagging (`log_comment`), weighted
mixes, and Poisson arrivals, and its latency/CPU source of truth is
server-side `system.query_log`, so the client-timing-skew argument for
delegating does not apply.

### Methodology

- **Merge lag.** Merges run after inserts. The drain phase (default 15m)
  catches merges triggered by measure-window inserts and books them to
  ingest. Soak-triggered merges completing during measure are counted too —
  at steady state the two leakages net out. That is what the soak phase is
  for (gate: active part count stable ±10% over 5 polls). `parts_plateau:
  false` in the result means steady state was not reached; treat $/1M-events
  with suspicion.
- **Multi-replica accounting.** `system.query_log` / `system.part_log` are
  per-replica and Cloud load-balances connections. All collectors read
  `clusterAllReplicas(default, ...)` and flush logs cluster-wide. Available
  CPU = reachable replicas × per-replica vCPUs (cgroup limit, falling back
  to `max_threads`).
- **Live event time.** The ingest driver stamps wall-clock event times
  (payloads stay deterministic per seed). The replayer binds a sliding
  `[now-3d, now)` window per arrival, so scan size stays stationary.
- **Cache state.** A configurable fraction of replayed queries runs with
  `enable_filesystem_cache=0` and is costed separately (`warm`/`cold`).
- **Per-query settings** (`max_threads`, ...) come from the cell manifest and
  are recorded in the result. Don't compare runs with different settings.
- **Idle cell semantics.** The `idle` cell measures the awake-but-unloaded
  floor, not Cloud idling behavior — the harness's own polling keeps the
  service awake.
- **Dedicated service required.** Foreign databases mean foreign merges and
  queries polluting coverage; the runner warns (`foreign_databases` flag).
- **Do not extrapolate across tiers/shapes.** A different tier has different
  rates AND different cache/memory behavior. The runner flags
  `shape_mismatch` when the detected shape differs from the profile.
- **Reconciliation deltas** >20% flag the run. Expected causes: autoscaling
  movement, a shared service, background system work, idling between phases
  — and daily-granular usage statements, which make sub-day windows
  indicative only.
- **Two prices, always.** `billed_shape` reconciles with the invoice (window
  cost split by CPU shares; the remainder is the idle floor). `cpu_linear`
  is the marginal cost on an already-busy service.
- **Egress estimate** is derived from uncompressed `result_bytes`; Cloud
  bills compressed transfer, so it is an upper bound.
