# ClickHouse Benchmarks for OpenMeter

A small harness for figuring out the best ClickHouse table design for
[OpenMeter](https://openmeter.io) usage metering. Each scenario is one variant
of the `om_events` table (schema, indexes, ordering key) plus the same set of
meter queries; the harness runs them head-to-head on a single ClickHouse node
and writes both a JSON result and a readable markdown report under
`bench/results/<scenario>/`.

What we're optimizing for: low query-time CPU and latency, under three
constraints that come from the OpenMeter use case — one schema for all meters
(they're user-defined, so the base table can't know any specific meter's paths),
no per-meter fan-out on insert *across the thousands of unknown meters*, and
exact billing arithmetic. The recommended table and query templates that come
out the other side are in [Findings](#findings) below.

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
go build -o bin/bench ./bench/cmd/bench   # build from inside the devenv shell
./bin/bench --scenario baseline-openmeter
```

> Build from **inside** the devenv shell so the Go toolchain matches its GOROOT.
> A Homebrew `go` on PATH can mismatch the Nix-pinned stdlib and fail with
> `compile: version "go1.26.x" does not match go tool version`.

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

## 10M Benchmark Evaluation

Head-to-head evaluation of the two table designs on an identical workload.

**Setup.** 10,000,000 heterogeneous events per scenario, 10 measured iterations
per query, single-node ClickHouse 26.2.5.45, `--seed 42`, `--time-end
2026-06-01T00:00:00Z` (pinned so both scenarios seed a byte-identical event
stream — required for the value-parity check below and for reproducibility).
Two scenarios:

- **`baseline-openmeter`** — `data String`, queried via `JSON_VALUE(data, '$.path')` (upstream OpenMeter shape).
- **`proposal`** — `data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id`, queried via native subcolumns.

Both tables hold the same events over the same window
(`2026-05-29 02:00:00 → 2026-06-01 01:59:59`).

### Query performance — baseline vs proposal

CPU is `OSCPUVirtualTimeMicroseconds` p50 (summed across query threads); p50 is
wall-clock. **Proposal is faster on every meter query** — median **−43% p50 /
−46% CPU** across the 22 shared queries.

| query | base p50 | prop p50 | base CPU | prop CPU | CPU Δ |
| --- | ---: | ---: | ---: | ---: | ---: |
| `kong_status_by_route` | 87 ms | 15 ms | 1475 ms | 186 ms | **−87%** |
| `kong_llm_tokens_total` | 99 ms | 22 ms | 1713 ms | 339 ms | **−80%** |
| `llm_tokens_by_model` | 68 ms | 17 ms | 1128 ms | 226 ms | **−80%** |
| `sum_hour_group1_group2_no_prewhere` | 72 ms | 27 ms | 1193 ms | 394 ms | −67% |
| `sum_hour_group1_group2` | 69 ms | 29 ms | 1150 ms | 437 ms | −62% |
| `sum_hour_group1` | 59 ms | 29 ms | 988 ms | 437 ms | −56% |
| `workload_seconds_by_region` | 17 ms | 10 ms | 241 ms | 106 ms | −56% |
| `sum_hour_group1_no_prewhere` | 61 ms | 30 ms | 1008 ms | 458 ms | −55% |
| `sum_no_window` | 68 ms | 37 ms | 1159 ms | 591 ms | −49% |
| `sum_month` / `max` / `min` / `sum_day` / `avg_hour` / `sum_hour` / `sum_hour_tz` | ~70 ms | ~40 ms | ~1180 ms | ~645 ms | ≈−45% |
| `sum_minute` | 83 ms | 52 ms | 1382 ms | 835 ms | −40% |
| `latest_hour` | 93 ms | 58 ms | 1602 ms | 982 ms | −39% |
| `unique_count_hour` | 65 ms | 45 ms | 1101 ms | 737 ms | −33% |
| `agent_runs_by_name` | 8 ms | 7 ms | 70 ms | 50 ms | −28% |
| `count_hour` | 14 ms | 12 ms | 184 ms | 151 ms | −18% |
| `kong_api_request_total` | 8 ms | 8 ms | 92 ms | 90 ms | −3% |

Every query improves, the more so the wider the `data` read: the per-meter path
queries (`kong_status_by_route`, `llm_tokens_by_model`, `kong_llm_tokens_total`)
win **−80% to −87%** because native JSON reads only the named subcolumn while
String parses the whole document per row. `kong_api_request_total` — a plain
`count(*)` that never reads `data` — is the smallest mover (−3% CPU, 92→90 ms):
the JSON column can neither help nor meaningfully hurt a key-only scan.

**`proposal`-only queries** (the extra grouped api variants + the lookup path;
no baseline equivalent):

| query | p50 | CPU p50 | notes |
| --- | ---: | ---: | --- |
| `kong_api_request_by_method` | 11 ms | 126 ms | grouped (1 dim), base table |
| `kong_api_request_by_service` | 14 ms | 176 ms | grouped (2 dims), base table |
| `kong_api_request_by_all_dims` | 187 ms | 2180 ms | grouped (all 19 dims), base table — worst-case fan-out |
| `lookup_by_id` | 52 ms | 768 ms | point-lookup path (bloom-pruned; see Findings) |

### Storage

| table | rows | on-disk |
| --- | ---: | ---: |
| `baseline_openmeter_events` (`data String`) | 10.0 M | 946.7 MiB |
| `proposal_events` (`data JSON CODEC(ZSTD(3))`) | 10.0 M | 649.7 MiB |

Proposal is **−31% on disk** (947 → 650 MiB) despite native JSON's per-path
subcolumn overhead — ZSTD(3) on the payload more than pays for it.

### Correctness — value parity across designs

Every run records a normalized **result digest** per query (a hash over the rows
with float/decimal cells rounded and rows sorted, so GROUP-BY tie-ordering and
String-vs-JSON float-summation order don't matter — see `bench/runner/digest.go`).
`bench compare <base> <cand>` then diffs those digests with no extra DB round-trip,
gated on both runs covering an identical seeded window:

```bash
./bin/bench compare baseline-openmeter proposal   # perf deltas + a Value-parity table
```

**22/22 base-table queries MATCH** between `data String` and `data JSON` — every
aggregation (SUM/COUNT/AVG/MIN/MAX/UNIQUE/LATEST, grouped and total) is
value-identical, so the two designs compute the same billing numbers.

`latest_hour` (`argMax(value, time)`) initially differed: the seed has windows
with multiple events sharing the exact maximum timestamp, and a bare
`argMax(value, time)` breaks such ties by physical read order — which the String
and JSON layouts resolve differently. That was a **query-determinism gap, not a
data or design difference** (the tied rows are byte-identical in both tables). The
fix is a deterministic tiebreaker, `argMax(value, (time, store_row_id))`: the
ingest-ordered ULID makes "latest" genuine last-write-wins, and `store_row_id` is
a top-level column identical in both tables. With it, `latest_hour` matches.

(The extra grouped api queries are proposal-only, so they appear as
_candidate-only_ in the parity table.)

### Verdict

**`proposal` (`data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id`) is the
recommended design**: −44% median p50 / −46% median CPU on meter queries, −31% on
disk, and identical billing values to the baseline. The only regression is a plain
no-`data` `count(*)` (+23 ms absolute).

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

Latest run: fresh 10M rows, 10 iterations, single-node ClickHouse 26.2.5.45.
Two table-design scenarios, 22 shared type-agnostic decimal meter queries
(`proposal` runs 26: the shared set + `lookup_by_id.sql` + the 3 extra
grouped Kong api variants). Per-run JSON + markdown reports are under
[`bench/results/<scenario>/`](bench/results/). The delta tables below are
`bench compare baseline-openmeter <variant>` output, not hand-transcribed.

**Median Δ across the 22 shared meter queries (vs baseline `data String`):**

| Variant | DDL change vs baseline | Median p50 Δ | Median CPU Δ | Ingest Δ |
| --- | --- | ---: | ---: | ---: |
| **`proposal`** | `data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id` | **−43%** | **−46%** | −30% |

(The −30% ingest Δ is native JSON's write-time cost — parsing each payload into
typed subcolumns on insert — now that the materialized views are gone; it is the
price paid for the query-side wins above.)

(Earlier runs also benched `data-as-json`, `order-by-extended-time`, and
`with-id-bloom` as standalone scenarios. They were folded in / retired:
`proposal` *is* data-as-json + ZSTD + the id bloom; the ORDER-BY-extended-time
lever measured run-dependent in direction across two seed-42 runs — not a
dependable win, costs ingest — so it was dropped. See git history if you want
to re-measure it on your own hardware.)

**The `proposal` scenario stacks the composable wins:** `data JSON` (the
table-type), `CODEC(ZSTD(3))` on `data` (compression — measured −43% disk on the
`data` column vs LZ4), and a `bloom_filter` skip index on `id`. The whole
`proposal` table is **−31% on disk** vs the String baseline (947 → 650 MiB). It is
the fastest table design — **−43% median p50 / −46% CPU** vs the String baseline.

**Per-meter-path queries** (read fields from large multi-field payloads — where
touching the whole `data` value costs most), CPU vs baseline (fresh 10M):

| query | proposal CPU Δ |
| --- | ---: |
| `kong_status_by_route` | **−87%** |
| `llm_tokens_by_model` | **−80%** |
| `workload_seconds_by_region` | **−56%** |

Native JSON wins by reading only the named typed subcolumn, while a String column
parses the whole document per row. The wider the payload, the bigger the gap.

**`count_hour` / `agent_runs_by_name`** (no `data` read in the agg) move within
run-to-run noise.

**Tradeoff summary.** `proposal` is the recommended default: fastest p50, −43%
disk on the data column, and the bloom on `id` essentially free.

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

**Seed realism.** `route_name`/`service_name` are deterministic 1:1 labels of
`route_id`/`service_id` (a route has one name), not independent draws — matching
real Kong data, so grouped Kong queries compress realistically.

### What we deliberately didn't pick

- **Fixed `MATERIALIZED value`/`group1`/`group2` columns** — only help the
  meters they're hand-cut for, so they violate the "any meter, any path"
  rule and need DDL + backfill on every new meter.
- **JSON typed-path hints** (`data JSON(value Float64, …)`) — a big win
  for *known* hot paths, but they hardcode those paths. Useful as
  per-deployment tuning, not as the shipped schema.
- **Skip indexes on `subject`** — redundant with the ORDER BY prefix.
- **Per-meter materialized views** — per-insert fan-out across user-defined
  meters collapses ingest, and per-meter MVs contradict the meter-agnostic base
  table. (A pair of known-schema Kong rollups was tried and removed: at this
  seed's dimension cardinality they compressed 1.0× — one rollup row per event —
  so they didn't pay off. See git history.)

### Query-time settings we tried

> From an earlier dedicated A/B sweep, **not re-measured in the current
> run** — the standard `bench` sweep above does not vary these settings.
> Directional, not exact; re-run with the relevant `SETTINGS` on your own
> hardware before relying on the magnitudes.

A quick A/B sweep on a JSON-`data` 10M table (each setting vs default,
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
