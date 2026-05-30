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
26.2.5.45. Five table-design variants, 20 type-agnostic decimal meter
queries each (`scenarios/proposal/` runs 21, the meter set plus
`lookup_by_id.sql`). Full reports under [`bench/results/`](bench/results/).
The per-variant delta tables below are generated by `bench compare
baseline-openmeter <variant>`, not hand-transcribed.

**Median Δ across the 20 meter queries (vs baseline `data String`):**

| Variant | DDL change vs baseline | Median p50 Δ | Median CPU Δ | Ingest Δ |
| --- | --- | ---: | ---: | ---: |
| `data-as-json` | `data JSON` | −32% | −34% | −12% |
| `data-as-map` | `data Map(String, String)` | −24% | −24% | −7% |
| `order-by-extended-time` | `data JSON` + ORDER BY extends with raw `time` (PK still `…toStartOfHour(time)`) | −25% | −30% | −12% |
| **`proposal`** | `data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id` | **−39%** | −41% | −15% |

**The `proposal` scenario stacks three composable wins:** `data JSON` (the
table-type), `CODEC(ZSTD(3))` on `data` (compression: 359.4 → 204.7 MiB on
the `data` column — measured from `system.columns`, **−43% disk** vs
`data-as-json`'s LZ4), and a `bloom_filter` skip index on `id` (point
lookups prune 1223 → 17 granules, ~72×; see the EXPLAIN table below). It's
the recommended default on the two metrics that are stable across runs:
**best p50** (−39% vs baseline; ~6–7 points ahead of plain `data-as-json`
in both this run and the prior one — the smaller ZSTD column means less I/O
even cache-warm) and **−43% disk**.

The bloom does nothing for the 20 meter queries (they filter
namespace/type/subject/time, never `id`), so the proposal-vs-`data-as-json`
delta is the ZSTD codec alone. ZSTD's effect on *query* CPU is run-variable
and roughly neutral — measured at +5%, 0%, and −8% across three runs (this
run: −8% median vs json). Treat the disk and p50 wins as the reason to
adopt ZSTD; do not count on a query-CPU win from it. (`unique_count_hour`
is the one query that regresses vs plain json, +10%, within that noise.)

**Per-meter-path queries** (read fields from large multi-field payloads —
where the cost of touching the whole `data` value is highest), CPU vs baseline:

| query | json | map | ext | proposal |
| --- | ---: | ---: | ---: | ---: |
| `kong_status_by_route` | **−83%** | −40% | **−83%** | **−84%** |
| `llm_tokens_by_model` | **−75%** | −36% | **−73%** | **−76%** |
| `workload_seconds_by_region` | −59% | −25% | **−55%** | **−55%** |

Native JSON wins by reading only the named typed subcolumn; a String
column has to parse the whole document on every row, and a Map column has
to materialize every key/value pair to find the named key. The wider the
payload, the worse Map looks against JSON. `data-as-map` is the middle
option — roughly half the JSON speedup at half the JSON ingest cost.

**`order-by-extended-time` does not reliably beat `data-as-json`.** In
this run it *regressed* against plain json on 18 of 20 queries (`bench
compare data-as-json order-by-extended-time`: median ≈ +6% CPU, e.g.
`avg_hour` +13%, `max_hour` +6%, `sum_hour_group1` +6%; per-meter-path
queries were a wash, ±1%). An earlier run on different hardware and a
newer ClickHouse build (26.2.19.43 vs this run's 26.2.5.45) showed the
opposite — ext beating json by a few percent. Two 10M/10-iteration/seed-42
runs disagreeing in *direction* is the verdict: the extra in-granule sort
on raw `time` is not a dependable lever; its sign is run/host/version
dependent. This is exactly why `proposal` does **not** stack the ORDER BY
extension (see `scenarios/proposal/init.sql`). Treat ext as an experiment
to verify on your own hardware, not a recommended default.

**`count_hour` / `agent_runs_by_name`** (no `data` read in the agg) move
within run-to-run noise across all variants.

**Tradeoff summary.** `proposal` is the recommended default: best p50 of
the four variants (stable across runs), −43% disk for the data column, and
the bloom on `id` is essentially free. Its query-CPU edge over plain
`data-as-json` is ZSTD-driven and run-variable — don't bank on it. `data-as-map` is the middle option —
choose it if write throughput dominates *and* payloads are flat
string→string (it does not match native JSON on read latency).
`order-by-extended-time` is not recommended: it does not reliably differ
from plain `data-as-json`, and the direction of the (small) difference
flipped between runs.

**`bloom_filter` on `id`** for the lookup-by-id path (`WHERE namespace = ?
AND id = ?`, no time bound — the one access pattern the ORDER BY can't
prune). The harness now captures this automatically via `EXPLAIN
indexes=1` on a literal id and records it in each result's `index_pruning`
block; for `proposal` at 10M rows:

| | Parts | Granules |
| --- | ---: | ---: |
| Without the bloom (`use_skip_indexes=0`) | 13/13 | 1223/1223 |
| With the bloom | **5/13** | **17/1223** |

That's ~72× fewer granules touched (the parts number depends on the
bloom's false-positive rate for the particular id). Single-query
wall-clock at this size doesn't show it cleanly because the scan is fast
when everything is in page cache; the payoff is multi-tenant, not-fully-cached,
concurrent load — where the I/O reduction binds.

The ClickHouse
[skipping-indexes doc](https://clickhouse.com/docs/guides/best-practices/skipping-indexes)
notes that skip indexes work best when the indexed column correlates with the
primary key. `id` doesn't (CloudEvents ids are arbitrary per event), so the
doc's heuristic would predict the bloom is ineffective. The measured
1223→17 result is the case where the heuristic is too conservative: for a
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

> From an earlier dedicated A/B sweep, **not re-measured in the current
> run** — the standard `bench` sweep above does not vary these settings.
> Directional, not exact; re-run with the relevant `SETTINGS` on your own
> hardware before relying on the magnitudes.

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
