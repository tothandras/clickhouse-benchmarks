# ClickHouse Benchmarks for OpenMeter

A small harness for figuring out the best ClickHouse table design for
[OpenMeter](https://openmeter.io) usage metering. Each scenario is one variant
of the `om_events` table (schema, indexes, ordering key) plus the same set of
meter queries; the harness runs them head-to-head on a single ClickHouse node
and writes both a JSON result and a readable markdown report under
`bench/results/<scenario>/`.

What we're optimizing for: low query-time CPU and latency, under three
constraints that come from the OpenMeter use case — one schema for all meters
(they're user-defined, so the table can't know any specific meter's paths),
no per-meter fan-out on insert, and exact billing arithmetic. The recommended
table and query templates that come out the other side are in
[Findings](#findings) below.

## Layout

- `scenarios/<name>/` — one table-design variant: `init.sql`, optional
  `seed.sql`, and a `queries/` directory.
- `bench/` — the Go benchmark driver. Each run produces a `<timestamp>.json`
  and a sibling `<timestamp>.md` under `bench/results/<scenario>/`.
- `openspec/` — proposals and specs ([OpenSpec](https://openspec.dev)).
- `devenv.nix` — pinned toolchain (Go, ClickHouse client, OpenSpec).

## Getting started

```bash
# Enter the dev shell.
direnv allow            # or: devenv shell

# Point at a reachable ClickHouse and run the baseline.
export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9100/default"
go build -o .devenv/bin/bench ./bench/cmd/bench
./.devenv/bin/bench --scenario baseline-openmeter
```

See [`bench/README.md`](bench/README.md) for the harness flags and the result-file
format.

## Workflow

New scenarios and methodology changes go through OpenSpec:

```bash
openspec list                 # see open changes / specs
openspec new <change-name>    # draft a new change
openspec validate <change>    # check it
openspec archive <change>     # land it after implementation
```

## Findings

**The optimum, for the OpenMeter use case as it works today:** keep the
upstream `om_events` columns and ORDER BY, change `data String` →
`data JSON CODEC(ZSTD(3))`, and add a `bloom_filter` skip index on `id` for
the lookup-by-id path. Queries read each meter's path via native-JSON typed
subcolumns (`data.<valueProperty>`), use `uniqExact` for `UNIQUE_COUNT`, and
`toDecimal128OrNull(...,19)` for exact-arithmetic meters.

```sql
CREATE TABLE IF NOT EXISTS om_events (
  namespace   String,
  id          String,
  type        LowCardinality(String),
  subject     String,
  source      String,
  time        DateTime,
  data        JSON CODEC(ZSTD(3)),  -- ← the only column change vs upstream
  ingested_at DateTime,
  stored_at   DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,
  INDEX om_events_id_bloom  id       TYPE bloom_filter(0.01) GRANULARITY 1,
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));
```

### What this run measured

Latest run: 10M rows, 10 iterations, seed 42, single-node ClickHouse
26.2.19.43. Full reports under
[`bench/results/`](bench/results/).

**Native `JSON` vs `String`+`JSON_VALUE` (baseline → data-as-json)** across
the 25 meter queries:

| Slice | Median p50 Δ | Median CPU Δ |
| --- | ---: | ---: |
| All 25 queries | **−40%** | **−38%** |
| Per-meter-path queries (LLM tokens, Kong status, …) | **−73% to −80%** | **−73% to −79%** |
| Float aggregations on `$.value` (sum/avg/min/max…) | **−37% to −43%** | **−37% to −42%** |
| Decimal aggregations | **−32% to −33%** | **−31% to −33%** |
| `count_hour` (no JSON read) | 0% | 0% |

The reason the per-meter-path queries (`kong_status_by_route`,
`llm_tokens_by_model`, `agent_runs_by_name`, `workload_seconds_by_region`)
win the hardest: `JSON_VALUE` on a `String` column re-parses the whole
document on every row, while a native `JSON` typed subcolumn reads only
that path. The benefit grows with how much of `data` you don't need.

**Cost on ingest:** baseline 108K events/s → data-as-json 93K events/s,
**−13.6%**. Accepted under a query-time objective.

**`bloom_filter` on `id`** for the lookup-by-id path (`WHERE namespace = ?
AND id = ?`, no time bound — the one access pattern the ORDER BY can't
prune). `EXPLAIN indexes=1` on a literal id at 10M rows:

| | Parts | Granules |
| --- | ---: | ---: |
| Without the bloom (`use_skip_indexes=0`) | 11/11 | 1224/1224 |
| With the bloom | **3/11** | **11/1224** |

That's ~110× fewer granules and ~4× fewer parts touched. Single-query
wall-clock at this size doesn't show it cleanly because the scan is fast
when everything is in page cache; the payoff is multi-tenant, not-fully-cached,
concurrent load — where the I/O reduction binds.

### What we deliberately didn't pick

- **Fixed `MATERIALIZED value`/`group1`/`group2` columns** — only help the
  meters they're hand-cut for, so they violate the "any meter, any path"
  rule and need DDL + backfill on every new meter.
- **JSON typed-path hints** (`data JSON(value Float64, …)`) — a big win
  for *known* hot paths, but they hardcode those paths. Useful as
  per-deployment tuning, not as the shipped schema.
- **Skip indexes on `subject`** — redundant with the ORDER BY prefix.
- **Materialized views / projections on the raw table** — per-insert
  fan-out across thousands of meters collapses ingest.

### Query-time settings we tried

A quick A/B sweep on the `data-as-json` 10M table (each setting vs default,
10 iterations, cache-warm, OS CPU from `system.query_log`):

| Setting | Effect on CPU | Verdict |
| --- | --- | --- |
| `max_threads=4` (default `auto(N)`) | **−5% to −33%**, ~50% less memory, same p50 | Real win, but host-dependent |
| `optimize_aggregation_in_order=1` | +7% to +19% | Hurts |
| `allow_aggregate_partitions_independently=1` | ±5% | Neutral |
| `allow_prefetched_read_pool_for_local_filesystem=1` | ±5% | Neutral |
| Disabling JIT / `optimize_read_in_order` | Slight regression | Defaults are right |
| Doubling/halving `max_block_size` | ±2% | Neutral |

`optimize_move_to_prewhere=1` and `allow_reorder_prewhere_conditions=1` are
already on by default in CH ≥24.x, so the explicit `SETTINGS` clauses on
two of the queries are redundant (kept for documentation).

**`max_threads` is the only real lever, and it's a host-property knob, not a
query property.** The default `auto(N)` over-provisions threads for
moderate-row meter queries on this 16-CPU host; for any specific deployment
the sweet spot will be different. Set it in your client config or per query,
not in the shipped schema. None of the other settings explored were worth
baking into the queries; the defaults are the right defaults.

### The per-meter overlay (a different question)

The OpenMeter team's
[meter-query-engine direction](https://github.com/openmeterio/openmeter/pull/3764)
leaves `om_events` frozen and adds per-meter *side tables* that pre-extract
`value` and `groupBy` at write time. That's a different shape (not in this
run) but a real query-time win — at the cost of write/storage scaling
with meter count rather than event count, and the ingest pipeline needing
to know each meter's paths up front. The recommendation above is the
optimum for the shared raw table that exists today; the side-table
direction is the optimum for a different system you'd build alongside it.
