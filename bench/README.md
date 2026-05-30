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

go build -o .devenv/bin/bench ./bench/cmd/bench
./.devenv/bin/bench --scenario baseline-openmeter
```

## CLI

| Flag              | Default          | Purpose                                                          |
| ----------------- | ---------------- | ---------------------------------------------------------------- |
| `--dsn`           | `$CLICKHOUSE_DSN`| Override the connection DSN.                                     |
| `--scenario`      | all              | Repeatable. Run only the named scenario(s).                      |
| `--scenarios-dir` | `scenarios`      | Where to discover scenarios.                                     |
| `--results-dir`   | `bench/results`  | Where to write result JSON files.                                |
| `--iterations`    | `10`             | Measured iterations per query (`clickhouse-benchmark -i`).       |
| `--concurrency`   | `1`              | Concurrent query streams (`clickhouse-benchmark -c`).            |
| `--rows`          | `1_000_000`      | Rows for the Go seeder.                                          |
| `--batch-size`    | `10_000`         | INSERT batch size for the Go seeder.                             |
| `--async-insert`  | `false`          | Set `async_insert=1` on seed inserts.                            |
| `--wait-async`    | `false`          | Set `wait_for_async_insert=1` (only with `--async-insert`).      |
| `--seed`          | `42`             | RNG seed for deterministic data generation.                      |
| `--skip-seed`     | `false`          | Skip seeding (run queries against existing data).                |

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
