# ClickHouse benchmarks for OpenMeter

Which `om_events` table design serves [OpenMeter](https://openmeter.io) meter
queries with the least CPU, latency, and cost? This repo answers that with two
measurement harnesses:

- **Perf** (`bench`) — head-to-head table-design scenarios on identical data.
- **COGS** (`bench cogs`) — what the workload costs on ClickHouse Cloud.

**Verdict:** change `data String` → `data JSON CODEC(ZSTD(3))` and add a
`bloom_filter` index on `id`. Measured at 10M events: **−45% median latency,
−50% median CPU, −31% disk**, identical billing values on all 30 shared
queries.

## The recommendation

```sql
-- Upstream om_events with one column change (data) and one new index (id bloom).
CREATE TABLE IF NOT EXISTS om_events (
  namespace   String,
  id          String,
  type        LowCardinality(String),
  subject     String,
  source      String,
  time        DateTime,
  data        JSON CODEC(ZSTD(3)),  -- ← was: data String
  ingested_at DateTime,
  stored_at   DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,
  INDEX om_events_id_bloom  id       TYPE bloom_filter(0.01) GRANULARITY 1,  -- ← new
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));
```

Query pattern for numeric meter aggregations:

```sql
sum(toDecimal128OrNull(toString(om_events.data.<path>), 19))
```

- Read the JSON path **untyped**, never `.:Float64`. A meter's `valueProperty`
  can arrive as a JSON number, a string (`"123"`), or a bigint. A typed read
  silently returns NULL for every non-matching shape — a billing total that
  quietly reads zero.
- `toString(...)` renders whatever the path holds to canonical text;
  `toDecimal128OrNull(..., 19)` parses it exactly. No precision loss on
  bigints, NULL on garbage.
- `UNIQUE_COUNT` uses `uniqExact` — billing data, never approximate.
- `LATEST` uses `argMax(value, (time, store_row_id))` — the ULID tiebreaker
  makes "latest" deterministic last-write-wins.

### Constraints (why simpler-sounding designs lose)

- **One schema for all meters.** Meters are user-defined JSON paths; the table
  can't know any of them. No `MATERIALIZED value` columns, no typed-path hints.
- **No per-meter insert fan-out.** Thousands of meters × per-meter MVs would
  collapse ingest.
- **Exact arithmetic.** Results feed invoices.

### What we deliberately didn't pick

- Fixed `MATERIALIZED` payload columns — per-meter knowledge in the schema.
- JSON typed-path hints (`data JSON(value Float64)`) — per-deployment tuning,
  not a shipped schema.
- Per-meter materialized views — fan-out on insert; a tried pair of
  known-schema rollups compressed 1.0× and was removed (see git history).
- Skip indexes on `subject` — redundant with the ORDER BY prefix.

## Results: 10M head-to-head (2026-06-09)

Setup: 10M heterogeneous events per scenario, byte-identical streams
(`--seed 42 --time-end 2026-06-09T00:00:00Z`), 10 iterations per query,
single-node ClickHouse 26.2.5.45. CPU is `OSCPUVirtualTimeMicroseconds` p50;
deltas from `bench compare baseline-openmeter proposal`.

| | baseline (`data String`) | proposal (`data JSON`) | Δ |
| --- | ---: | ---: | ---: |
| Median p50 (30 shared queries) | — | — | **−45%** |
| Median CPU (30 shared queries) | — | — | **−50%** |
| On-disk (10M rows) | 947 MiB | 650 MiB | **−31%** |
| Seed ingest throughput | 91.3k ev/s | 66.2k ev/s | −27% |
| Value parity | | | **30/30 MATCH** |

The win scales with how much `data` a query reads. Native JSON reads only the
named subcolumn; String parses the whole document per row.

| query | base p50 | prop p50 | CPU Δ |
| --- | ---: | ---: | ---: |
| `kong_llm_tokens_total` | 109 ms | 14 ms | **−90%** |
| `kong_status_by_route` | 83 ms | 12 ms | **−90%** |
| `llm_tokens_by_model` | 63 ms | 12 ms | **−86%** |
| `sum_day_by_dim` | 52 ms | 10 ms | **−86%** |
| `sum_hour_group1_group2` | 75 ms | 22 ms | −77% |
| `sum_total_filter2` | 55 ms | 22 ms | −67% |
| typical meter aggs (`sum`/`avg`/`min`/`max`/`unique` × hour/day/month) | 53–80 ms | 31–49 ms | −41% to −52% |
| `latest_hour` | 99 ms | 79 ms | −26% |
| `count_total` / `count_hour` (key-only, no `data` read) | 7–11 ms | 6–10 ms | −23% / −26% |
| `kong_api_request_total` (key-only) | 6 ms | 6 ms | −5% |
| `distinct_subjects` (no `data` read) | 15 ms | 16 ms | +7% |

`distinct_subjects` is the one regression: it reads no payload, so the JSON
column's overhead can't be offset. Full per-query numbers:
[`bench/results/`](bench/results/) or `./bin/bench compare baseline-openmeter proposal`.

Proposal-only queries (no baseline equivalent): `lookup_by_id` 27 ms,
`kong_api_request_by_method` 9 ms, `_by_service` 11 ms, `_by_all_dims`
(all 19 dims, worst-case fan-out) 176 ms.

### Value parity

Every run hashes each query's normalized result set
(`bench/runner/digest.go`); `bench compare` diffs the digests. **All 30 shared
queries MATCH** between `data String` and `data JSON` — both designs compute
identical billing numbers. (`latest_hour` needed the deterministic
`argMax` tiebreaker above; bare `argMax(value, time)` breaks timestamp ties by
physical read order, which differs across layouts.)

### Bloom filter on `id`

For the lookup-by-id path (`WHERE namespace = ? AND id = ?`, no time bound —
the one access pattern the ORDER BY can't prune), captured via
`EXPLAIN indexes=1` in each run:

| | granules | parts |
| --- | ---: | ---: |
| without bloom | 1222 | 17 |
| with bloom | **12** | **4** |

~100× fewer granules touched. `id` doesn't correlate with the primary key
(the skipping-indexes doc's heuristic would predict failure), but for a point
lookup most granules genuinely lack the id, so pruning works anyway. The
payoff is concurrent, not-fully-cached load, not single-query wall-clock.

### Query-time settings

From an earlier A/B sweep (directional; re-measure on your own hardware):
**`max_threads` is the only real lever** — capping it cut CPU 5–33% and halved
memory on a 16-CPU host, but the sweet spot is host-specific. Set it in client
config, not the schema. Everything else tried
(`optimize_aggregation_in_order`, partition-parallel aggregation, prefetched
read pool, JIT toggles, block-size changes) was neutral or worse. Defaults are
right.

## Results: COGS at 100M events on ClickHouse Cloud (2026-06-09)

`bench cogs` runs a workload cell (paced ingest + weighted query replay)
against a dedicated service, attributes CPU to `{insert, merge, query, idle}`
from system tables, and prices it with a checked-in profile. Methodology and
CLI: [`bench/README.md`](bench/README.md).

Run: `mixed-5keps-4qps` on a 3-replica × 8 GiB / 2 vCPU Scale service
(eu-central-1): 100M preload, 30m soak (parts plateau reached), 1h measure at
5k events/s + 4 qps production-mix replay, 15m drain. Zero harness errors.
Result: [`bench/results/proposal/cogs/`](bench/results/proposal/cogs/).
The service was deleted after the run.

| Unit cost (billed-shape) | Value |
| --- | --- |
| $ / 1M events ingested | **$0.0013** — insert 22% / **merge 78%** |
| $ / 1k queries: meter_agg | $0.0049 warm / $0.0085 cold |
| $ / 1k queries: payload_heavy | $0.0052 warm / $0.0102 cold |
| $ / 1k queries: key_only | $0.0036 warm / $0.0065 cold |
| $ / 1k queries: lookup | $0.0133 warm / $0.0227 cold |
| Storage (53.4 bytes/event settled) | $0.00135 / 1M events / month |
| Idle floor (this shape) | $654 / month |

Findings:

1. **Merges are 3.4× the insert CPU.** 78% of ingest cost is background
   merges, invisible to `system.query_log`. Insert-timing cost models
   undercount ~4.5×.
2. **The idle floor dominates.** This load used ~$0.10/h of a $0.90/h window
   — 89% idle. Utilization is the COGS lever, not query optimization.
3. **Cold queries cost 1.7–2× warm.** The filesystem cache is worth about
   half the query bill.

Caveats: query-mix weights are placeholders (flagged in the report).
Reconciliation against the usage statement confirmed the model — billed 9.93
compute unit-hours vs ~9.6–9.9 modeled over the service's active time (1–3%);
the windowed reconciliation block stays flagged only because the statement is
daily-granular. Billed egress (1.77 GB) was far below the 12.48 GB
`result_bytes` estimate: Cloud bills *compressed* transfer, so the v1 egress
estimate is an uncompressed upper bound.

## Getting started

```bash
direnv allow            # or: devenv shell — pins Go, ClickHouse, OpenSpec

export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"
go build -o bin/bench ./bench/cmd/bench
./bin/bench --scenario baseline-openmeter --scenario proposal --rows 10000000
./bin/bench compare baseline-openmeter proposal
```

Build from **inside** the devenv shell — a Homebrew `go` on PATH can mismatch
the Nix-pinned stdlib. `clickhouse-client` and `clickhouse-benchmark` read
`CLICKHOUSE_*` env vars; clear stale ones when switching targets.

COGS (zero-rate local smoke; real runs need a dedicated Cloud service):

```bash
bench cogs --cell mixed-5keps-4qps --profile ci --pricing-profile local-zero --preload-rows 500000
```

## Layout

- [`scenarios/`](scenarios/README.md) — one directory per table-design
  variant: `init.sql` + `queries/` (+ `mix.json` for cogs replay).
- [`bench/`](bench/README.md) — the Go harness: perf runner, cogs runner,
  seeder, comparison. Results land in `bench/results/<scenario>/`.
- [`cells/`](cells/) — cogs workload cells (rates, phases, mix).
- [`pricing/`](pricing/) — pricing profiles (rates + service shape).
- [`openspec/`](openspec/) — specs and change proposals
  ([OpenSpec](https://openspec.dev)).

The dev shell also installs an `mcp-clickhouse` MCP server (`.mcp.json`,
read-only, local ClickHouse) for ad-hoc table inspection from Claude Code.

## Workflow

Scenario and methodology changes go through OpenSpec:

```bash
openspec list / new <name> / validate <name> / archive <name>
```

## Related: the per-meter overlay

The OpenMeter
[meter-query-engine direction](https://github.com/openmeterio/openmeter/pull/3764)
freezes `om_events` and adds per-meter side tables that pre-extract values at
write time. Different shape, real query-time win — but write/storage cost
scales with meter count and the ingest pipeline must know each meter's paths.
This repo's recommendation is the optimum for the shared raw table that exists
today.
