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
export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9000/default"
go build -o .devenv/bin/bench ./bench/cmd/bench
./.devenv/bin/bench --scenario baseline-openmeter
```

See [`bench/README.md`](bench/README.md) for the harness flags and the result-file
format.

The dev shell also installs [`mcp-clickhouse`](https://github.com/ClickHouse/mcp-clickhouse)
into `.devenv/state/venv/`; `.mcp.json` at the project root wires it to Claude
Code (project-scoped, read-only by default, points at the local ClickHouse on
`127.0.0.1:8123`). Run `claude` from the project to use the MCP server for
ad-hoc inspection of the scenario tables.

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
the lookup-by-id path. Numeric meter queries read each meter's path through
the *untyped* JSON root and convert via `toDecimal128OrNull(toString(data.<path>), 19)`
(string-round-trip → decimal, never `.:Float64`; see "The correctness fix"
below), `uniqExact` for `UNIQUE_COUNT`.

```sql
-- Apply as `om_events` in production; benched as `proposal_events` here so
-- scenarios coexist in the same database without clobbering each other.
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

### The correctness fix (the headline of this run)

A meter's `valueProperty` can be stored in `data` as **any** JSON-storable
type: a JSON number (`Float64`), a JSON string (`"123"` — common from
producers that JSON-stringify everything), or a JSON integer (bigint
counters that don't fit in `Float64`). The earlier queries used
`toString(om_events.data.value.:Float64)`, which **silently returned NULL**
for every meter whose path wasn't stored as `Float64` — most of them, in
our seeded data. A "billing total" that quietly reads zero is the worst
kind of bug.

The fix is to access the JSON path **untyped** and let the conversion
handle any stored type:

```sql
-- Numeric agg (sum / avg / min / max / latest / argMax):
sum(toDecimal128OrNull(toString(om_events.data.<path>), 19))
```

`toString(data.<path>)` renders whatever the JSON Variant holds (number,
string, integer, null) to its canonical text form; `toDecimal128OrNull`
parses that into an exact `Decimal128(19)`. NULL on un-parseable values,
no precision loss on bigints. Verified equal to the old `.:Float64`-based
form on `Float64`-stored paths down to the ULP, and the only form that
returns non-NULL on `String`/`Int`-stored paths.

The corollary: the float/decimal query split is gone — there's no reason
to keep a `sum_hour` (float) alongside a `sum_hour_decimal`. Both
scenarios now ship 20 queries (was 25), all using the type-agnostic
decimal pattern. The bench is now apples-to-apples on the **only**
billing-safe query shape.

### What this run measured

Latest run: 10M rows, 10 iterations, seed 42, single-node ClickHouse
26.2.19.43. Five table-design variants, 20 type-agnostic decimal meter
queries each (`scenarios/proposal/` runs 21, the meter set plus
`lookup_by_id.sql`). Full reports under [`bench/results/`](bench/results/).

**Median Δ across the 20 meter queries (vs baseline `data String`):**

| Variant | DDL change vs baseline | Median p50 Δ | Median CPU Δ | Ingest Δ |
| --- | --- | ---: | ---: | ---: |
| `data-as-json` | `data JSON` | −32% | −30% | −13% |
| `data-as-map` | `data Map(String, String)` | −21% | −21% | −5% |
| `order-by-extended-time` | `data JSON` + ORDER BY extends with raw `time` (PK still `…toStartOfHour(time)`) | −33% | **−35%** | −13% |
| **`proposal`** | `data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id` | **−41%** | −32% | −14% |

**The `proposal` scenario stacks three composable wins:** `data JSON` (the
table-type), `CODEC(ZSTD(3))` on `data` (compression: 359 → 205 MiB on
the `data` column, **−43% disk** vs `data-as-json`'s LZ4), and a
`bloom_filter` skip index on `id` (point lookups prune 1225 → 11
granules, ~111×). It's the recommended default: best p50 of the four
variants and roughly tied with `data-as-json` on CPU (proposal median CPU
0% vs plain json this run, with two clear CPU wins — `unique_count_hour`
−29% and `agent_runs_by_name` −16% — and the rest within ±5%). The big
disk win is essentially free at query time.

**Per-meter-path queries** (read fields from large multi-field payloads —
where the cost of touching the whole `data` value is highest), CPU vs baseline:

| query | json | map | ext | proposal |
| --- | ---: | ---: | ---: | ---: |
| `kong_status_by_route` | **−77%** | −35% | **−82%** | **−79%** |
| `llm_tokens_by_model` | **−71%** | −23% | **−77%** | **−72%** |
| `workload_seconds_by_region` | −53% | −28% | **−65%** | **−56%** |

Native JSON wins by reading only the named typed subcolumn; a String
column has to parse the whole document on every row, and a Map column has
to materialize every key/value pair to find the named key. The wider the
payload, the worse Map looks against JSON. `data-as-map` is the middle
option — roughly half the JSON speedup at half the JSON ingest cost.

**`order-by-extended-time` consistently beats `data-as-json`** in this
run — median CPU **−7% vs json**, 19 of 20 queries faster (only
`agent_runs_by_name` regresses, +16% on a 20ms baseline). The biggest
wins go to per-meter-path and groupBy queries:

| query | ext vs json |
| --- | ---: |
| `workload_seconds_by_region` | **−26%** |
| `kong_status_by_route` | **−24%** |
| `llm_tokens_by_model` | **−22%** |
| `sum_hour_group1[_group2][_no_prewhere]` | **−11% to −17%** |
| `unique_count_hour` | −15% |
| `$.value` aggregations | −3% to −8% |

Time-clustered rows let the columnar reader skip more granules on
time-range queries, and `uniqExact` benefits from tighter clusters.
Ingest cost is essentially the same as plain json (−13% vs baseline) in
this run — earlier runs showed up to −27% ingest, so the cost is
variable and worth re-checking on the target hardware. A genuine win,
but the gap to `proposal` (which adds ZSTD on top and includes the
bloom) is smaller than the gap from baseline.

**`count_hour` / `agent_runs_by_name`** (no `data` read in the agg) move
within run-to-run noise across all variants.

**Tradeoff summary.** `proposal` is the recommended default: best p50,
−43% disk for the data column, and the bloom on `id` is essentially
free. `order-by-extended-time` is the runner-up — consistently faster
than plain `data-as-json`, but the ingest cost varies run-to-run; verify
on target hardware before adopting. `data-as-map` is the middle option —
choose it if write throughput dominates *and* payloads are flat
string→string (it does not match native JSON on read latency).

**`bloom_filter` on `id`** for the lookup-by-id path (`WHERE namespace = ?
AND id = ?`, no time bound — the one access pattern the ORDER BY can't
prune). `EXPLAIN indexes=1` on a literal id at 10M rows:

| | Parts | Granules |
| --- | ---: | ---: |
| Without the bloom (`use_skip_indexes=0`) | 10/10 | 1224/1224 |
| With the bloom | **3/10** | **12/1224** |

That's ~110× fewer granules touched (the parts number depends on the
bloom's false-positive rate for the particular id). Single-query
wall-clock at this size doesn't show it cleanly because the scan is fast
when everything is in page cache; the payoff is multi-tenant, not-fully-cached,
concurrent load — where the I/O reduction binds.

The ClickHouse
[skipping-indexes doc](https://clickhouse.com/docs/guides/best-practices/skipping-indexes)
notes that skip indexes work best when the indexed column correlates with the
primary key. `id` doesn't (CloudEvents ids are arbitrary per event), so the
doc's heuristic would predict the bloom is ineffective. The measured
1224→11 result is the case where the heuristic is too conservative: for a
point lookup of a single id, the bloom probe is cheap and most granules
genuinely don't contain that id, so pruning works even without correlation.

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
not in the shipped schema. ClickHouse's
[query-parallelism doc](https://clickhouse.com/docs/guides/best-practices/query-parallelism)
also explicitly warns against tweaking the per-task thresholds
(`merge_tree_min_rows_for_concurrent_read`, …); `max_threads` is *the* knob.

> When investigating *why* a query is fast or slow, set
> `enable_filesystem_cache=0` per query to bypass the page cache — that
> exposes the real I/O cost the cache is hiding. It's a debugging knob, not
> a production setting. (Recommended by the
> [query-optimization doc](https://clickhouse.com/docs/guides/best-practices/query-optimization).)

None of the other settings explored were worth baking into the queries;
the defaults are the right defaults.

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
