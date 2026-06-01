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

The one sanctioned exception to "no per-meter fan-out": for a *handful of
known-schema meters* whose paths/types are fixed (here, the two Kong meters in
[`scenarios/proposal/meters.yaml`](scenarios/proposal/meters.yaml)), `proposal`
ships dedicated materialized-view rollups. Two MVs for two known meters don't
collapse ingest the way per-meter fan-out across thousands would; the base table
stays generic for everything else. See [Known-meter rollups](#known-meter-rollups).

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
- **`proposal`** — `data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id`, queried via native subcolumns; plus the two known-meter Kong rollups.

Both tables hold the same events over the same window
(`2026-05-29 02:00:00 → 2026-06-01 01:59:59`).

### Query performance — baseline vs proposal

CPU is `OSCPUVirtualTimeMicroseconds` p50 (summed across query threads); p50 is
wall-clock. **Proposal is faster on every meter query** — median **−44% p50 /
−46% CPU** across the 21 shared queries.

| query | base p50 | prop p50 | base CPU | prop CPU | CPU Δ |
| --- | ---: | ---: | ---: | ---: | ---: |
| `kong_llm_tokens_total` | 96 ms | 14 ms | 1694 ms | 208 ms | **−88%** |
| `kong_status_by_route` | 79 ms | 14 ms | 1366 ms | 175 ms | **−87%** |
| `llm_tokens_by_model` | 60 ms | 14 ms | 1008 ms | 181 ms | **−82%** |
| `sum_hour_group1_group2_no_prewhere` | 67 ms | 22 ms | 1127 ms | 332 ms | −71% |
| `sum_hour_group1_group2` | 67 ms | 23 ms | 1129 ms | 339 ms | −70% |
| `sum_hour_group1` | 58 ms | 27 ms | 978 ms | 426 ms | −56% |
| `workload_seconds_by_region` | 16 ms | 9 ms | 222 ms | 100 ms | −55% |
| `sum_no_window` | 65 ms | 35 ms | 1086 ms | 570 ms | −48% |
| `latest_hour` | 68 ms | 38 ms | 1167 ms | 621 ms | −47% |
| `avg_hour` / `min` / `max` / `sum_hour` / `sum_day` / `sum_month` / `sum_hour_tz` | ~67 ms | ~38 ms | ~1140 ms | ~620 ms | ≈−46% |
| `sum_minute` | 80 ms | 49 ms | 1349 ms | 798 ms | −41% |
| `unique_count_hour` | 60 ms | 38 ms | 1024 ms | 622 ms | −39% |
| `count_hour` | 13 ms | 10 ms | 179 ms | 135 ms | −24% |
| `agent_runs_by_name` | 6 ms | 6 ms | 60 ms | 44 ms | −27% |
| `kong_api_request_total` | 7 ms | 9 ms | 86 ms | 109 ms | +28% |

The single regression — `kong_api_request_total` (a plain `count(*)`, +28% CPU
but only 86→109 ms in absolute terms) — is a query that never reads `data`, so
the JSON column's only effect is a slightly larger primary-key scan; it is the
one case where the table type can't help and adds marginal overhead. Everything
that touches a `data` path improves, the more so the wider the read: the per-meter
path queries (`kong_status_by_route`, `llm_tokens_by_model`) win **−82% to −88%**
because native JSON reads only the named subcolumn while String parses the whole
document per row.

**`proposal`-only queries** (rollup-served + the extra grouped variants; no
baseline equivalent):

| query | p50 | CPU p50 | notes |
| --- | ---: | ---: | --- |
| `kong_llm_tokens_total_hybrid` | 9 ms | 50 ms | rollup-served total SUM (vs base-table 208 ms) |
| `kong_api_request_total_hybrid` | 10 ms | 62 ms | rollup-served total COUNT (vs base-table 109 ms) |
| `kong_status_by_route_rollup` | 13 ms | 178 ms | grouped via 16-dim rollup — ≈ the 175 ms base table (no win) ⚠ |
| `kong_api_request_by_method` | 10 ms | 120 ms | grouped (1 dim), base table |
| `kong_api_request_by_service` | 14 ms | 183 ms | grouped (2 dims), base table |
| `kong_api_request_by_all_dims` | 185 ms | 2186 ms | grouped (all 19 dims), base table — worst-case fan-out |
| `lookup_by_id` | 49 ms | 729 ms | point-lookup path (bloom-pruned; see Findings) |

### Storage

| table | rows | on-disk |
| --- | ---: | ---: |
| `baseline_openmeter_events` (`data String`) | 10.0 M | 946.7 MiB |
| `proposal_events` (`data JSON CODEC(ZSTD(3))`) | 10.0 M | 649.7 MiB |

Proposal is **−31% on disk** (947 → 650 MiB) despite native JSON's per-path
subcolumn overhead — ZSTD(3) on the payload more than pays for it.

### Known-meter rollups

| rollup | rows | on-disk | compression vs base events | billing-exact? |
| --- | ---: | ---: | ---: | --- |
| `proposal_llm_tokens_rollup` (SUM tokens, 14 dims) | 1.50 M | 47.3 MiB | **1.0×** (from 1.50 M llm events) | ✅ `sumMerge == base` (3,005,145,740) |
| `proposal_api_request_rollup` (COUNT, 16 dims) | 2.50 M | 65.4 MiB | **1.0×** (from 2.50 M api events) | ✅ `countMerge == base` (2,501,717) |

Both rollups are **billing-exact** (rollup state == direct base-table aggregate,
to the unit). But both also compress **1.0×** — one rollup row per source event —
because their dimension sets are high-enough cardinality that `GROUP BY` collapses
nothing. The rollup-served *total* queries are still the fastest path (they read a
narrow pre-aggregated state, 50–62 ms CPU vs 109–208 ms on the base table), but the
*grouped* api rollup query (`kong_status_by_route_rollup`, 178 ms) is **no faster
than the base-table version** (175 ms): a 1.0× rollup is just a second copy of the
data. See [Known-meter rollups](#known-meter-rollups) for the dims-bounded-vs-dims-free
analysis; the dims-free design (which compresses ~343×) remains the correct one.

### Correctness — value parity across designs

`bench/compare_values.py` runs every paired query against both tables with
identical parameters and asserts value-equal results. **24/25 PASS.** All
deterministic aggregations (SUM/COUNT/AVG/MIN/MAX/UNIQUE, grouped and total, plus
all three rollup-vs-base-oracle checks) are byte-identical between `data String`
and `data JSON` — the two designs compute the same billing numbers. The single
non-match is `latest_hour` (`argMax(value, time)`): 24 windows in the seed have
multiple events sharing the exact maximum timestamp, and `argMax` breaks such ties
nondeterministically, so String and JSON can pick different rows. This is an
`argMax`-tie artifact, **not** a divergence between the table designs — every
order-independent aggregate matches exactly.

### Verdict

**`proposal` (`data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id`) is the
recommended design**: −44% median p50 / −46% median CPU on meter queries, −31% on
disk, and identical billing values to the baseline. The only regression is a plain
no-`data` `count(*)` (+23 ms absolute). The two known-meter rollups serve the
total-period COUNT/SUM fast and exactly, but at this seed's dimension cardinality
they don't compress (1.0×), so the **dims-bounded `kong.api_request` rollup does
not pay off** for grouped queries — dim-grouped api queries should run on the base
table, and the rollup should stay **dims-free** (keyed only on
namespace/subject/window) as originally designed.

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

`proposal` additionally ships two materialized-view rollups for the known-schema
Kong meters on top of this base table (see [Known-meter rollups](#known-meter-rollups)
and the full [`scenarios/proposal/init.sql`](scenarios/proposal/init.sql)).

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
Three table-design scenarios, 20 type-agnostic decimal meter queries each
(`proposal` runs 23: the meter set + `lookup_by_id.sql` + the two Kong rollup
hybrids). Full reports under [`bench/results/`](bench/results/) and the
authoritative summary in [`bench/results/README.md`](bench/results/README.md).
The delta tables below are `bench compare baseline-openmeter <variant>` output,
not hand-transcribed.

**Median Δ across the 20 meter queries (vs baseline `data String`):**

| Variant | DDL change vs baseline | Median p50 Δ | Median CPU Δ | Ingest Δ |
| --- | --- | ---: | ---: | ---: |
| `data-as-map` | `data Map(String, String)` | −22% | −23% | −9% |
| **`proposal`** | `data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id` | **−41%** | **−43%** | −27% |

(Earlier runs also benched `data-as-json`, `order-by-extended-time`, and
`with-id-bloom` as standalone scenarios. They were folded in / retired:
`proposal` *is* data-as-json + ZSTD + the id bloom; the ORDER-BY-extended-time
lever measured run-dependent in direction across two seed-42 runs — not a
dependable win, costs ingest — so it was dropped. See git history if you want
to re-measure it on your own hardware.)

**The `proposal` scenario stacks the composable wins:** `data JSON` (the
table-type), `CODEC(ZSTD(3))` on `data` (compression — measured −43% disk on the
`data` column vs LZ4), and a `bloom_filter` skip index on `id`. It is the fastest
table design — **−41% median p50 / −43% CPU** vs the String baseline.

**Per-meter-path queries** (read fields from large multi-field payloads — where
touching the whole `data` value costs most), CPU vs baseline (fresh 10M):

| query | data-as-map | proposal |
| --- | ---: | ---: |
| `kong_status_by_route` | −25% | **−88%** |
| `llm_tokens_by_model` | −40% | **−82%** |
| `workload_seconds_by_region` | −7% | **−58%** |

Native JSON wins by reading only the named typed subcolumn; a String column
parses the whole document per row, and a Map column materializes every key/value
pair to find the named key. The wider the payload, the worse Map looks vs JSON.
`data-as-map` is the middle option — roughly half the JSON speedup at half the
ingest cost.

**`count_hour` / `agent_runs_by_name`** (no `data` read in the agg) move within
run-to-run noise across both variants.

**Tradeoff summary.** `proposal` is the recommended default: fastest p50, −43%
disk on the data column, the bloom on `id` essentially free, plus the known-meter
rollups below. `data-as-map` is the middle option — choose it if write throughput
dominates *and* payloads are flat string→string (it does not match native JSON on
read latency).

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

### Known-meter rollups

For the handful of meters whose schema is fixed and known, `proposal` ships
dedicated **materialized-view rollups** on top of the base table. Two canonical
Kong meters are modeled here, declared verbatim in
[`scenarios/proposal/meters.yaml`](scenarios/proposal/meters.yaml):

- `kong_konnect_llm_tokens` — SUM `$.tokens`, eventType `kong.llm_request`, 14 groupBy dims.
- `kong_konnect_api_request` — COUNT, eventType `kong.api_request`, 19 groupBy dims.

The rollup design carries the **bounded** groupBy dims of each meter:

- **llm → dims-full rollup** (`AggregatingMergeTree`, all 14 dims as typed
  columns + `sumState(tokens)`). Token state is `UInt64` (compact); queries cast
  to `toDecimal128(…, 19)` at read time. Serves both the total-period SUM and
  dim-filtered queries (by model/provider/route/…).
- **api → dims-bounded rollup** (`countState` keyed `namespace, subject, window`
  plus **16 of the 19** groupBy dims as typed columns). Currently shipped, but
  **measured not to pay off** — see the failure note below. The 3 highest-card
  dims (`client_ip`, `request_uri`, `request_user_agent`) are excluded; grouped
  queries needing one of those fall back to the base table
  (see `kong_api_request_by_all_dims.sql`).

  > 🔴 **MEASURED FAILURE (20M, 2026-05-31) — kept as a documented negative result.**
  > Dropping the 3 highest-card dims did **not** recover compression: the rollup is
  > **5,002,087 rows from 5,002,087 api_request events = 1.0× (no compression)**,
  > 131.8 MiB on disk. Root cause: the 8 *ID* dims (`api_id`≈30, `route_id`≈60,
  > `service_id`≈40, `application_id`≈50, `api_product_id`, `api_product_version_id`,
  > `control_plane_id`, `portal_id`) are each individually bounded but their
  > **cross-product far exceeds 20M**, so `GROUP BY` collapses nothing (one rollup
  > row per event). The grouped query it serves is barely faster:
  > `kong_status_by_route_rollup` **223 ms** CPU vs `kong_status_by_route` **318 ms**
  > on the base table (~1.3×) — nowhere near the 10–100× a real rollup gives. Only
  > **7,300** distinct `(namespace, subject, hour)` keys exist; the original
  > **dims-free** rollup keyed on those alone (~343× at 10M) was the correct design.
  > **Recommendation:** revert to dims-free and run dim-grouped api queries on the
  > base table. Rollups verified billing-exact regardless (api COUNT and llm SUM
  > both equal the base table to the row).

Total-period meter queries use arbitrary, non-hour-aligned `from`/`to`, so the
rollup is read via a **3-part hybrid** — raw events for the partial first/last
hour + rollup `sumMerge`/`countMerge` for the whole hours in between — which is
**billing-exact for any boundaries** (verified rollup == base at 10M: llm
3,005,145,740 tokens; api 2,501,717 count). The MVs must consume the *deduped*
event stream in production.

**Scenario pairing & value parity.** Only two scenarios remain — `baseline-openmeter`
(`data String`, `JSON_VALUE` access) and `proposal` (`data JSON`, native subcolumns);
the `data-as-map` scenario was removed. Every query has a same-output sibling in
both scenarios, plus base-table oracle queries (`kong_api_request_total`,
`kong_llm_tokens_total`) so each rollup-served query has a direct value check.
`bench/compare_values.py` runs all paired queries on both tables with identical
params and asserts value-equal results: **25/25 PASS** (all 22 base-table queries
byte-identical baseline↔proposal; all 3 rollup queries equal to their base-table
oracle). Note: value parity requires a shared `--time-end` (the seeder otherwise
captures `time.Now()` per scenario, shifting hour buckets ~minutes); the harness
now takes `--time-end RFC3339` to pin one window across scenarios.

**Seed realism.** `route_name`/`service_name` are deterministic 1:1 labels of
`route_id`/`service_id` (a route has one name), not independent draws — matching
real Kong data and removing artificial cross-product inflation in the rollup analysis.

**Measured (fresh 20M, corrected seed, single-node CH 26.2.5.45), baseline vs proposal CPU p50:**

| query | base CPU | proposal CPU | Δ |
| --- | ---: | ---: | ---: |
| `kong_status_by_route` (status+route) | 1487 ms | 326 ms | **−78%** |
| `llm_tokens_by_model` | 4050 ms | 590 ms | **−85%** |
| `sum_hour_group1_group2` | 1114 ms | 454 ms | −59% |
| `workload_seconds_by_region` | 751 ms | 317 ms | −58% |
| `sum_hour` | 1103 ms | 800 ms | −27% |
| `count_hour` (no `data` read) | 217 ms | 184 ms | −15% |

Proposal (native JSON) is faster on every paired query (−15% to −85% CPU); the
wider the payload read, the bigger the win. `proposal`-only rollup/extra queries:

| query | p50 | CPU p50 | notes |
| --- | ---: | ---: | --- |
| `kong_api_request_total_hybrid` | 15 ms | 144 ms | rollup-served total COUNT |
| `kong_llm_tokens_total_hybrid` | 18 ms | 210 ms | rollup-served total SUM |
| `kong_status_by_route_rollup` | 17 ms | 242 ms | grouped via 16-dim rollup — only ~1.3× vs the 326 ms base table ⚠ |
| `kong_api_request_by_method` | 17 ms | 244 ms | grouped (1 dim), base table |
| `kong_api_request_by_service` | 22 ms | 325 ms | grouped (2 dims), base table |
| `kong_api_request_by_all_dims` | 361 ms | 5432 ms | grouped (all 19 dims), base table — worst-case fan-out |

> The rollup-served **total** queries (COUNT / SUM) remain the fast path. The
> **grouped** api rollup query (`kong_status_by_route_rollup`) is only ~1.3× faster
> than the equivalent base-table query because the 16-dim rollup does not compress
> (1.0× — see the failure note above). Compression ratios this run: api rollup
> **1.0×** (5.0M rows from 5.0M events), llm rollup **~1.0×** (3.0M from 3.0M).

### What we deliberately didn't pick

- **Fixed `MATERIALIZED value`/`group1`/`group2` columns** — only help the
  meters they're hand-cut for, so they violate the "any meter, any path"
  rule and need DDL + backfill on every new meter.
- **JSON typed-path hints** (`data JSON(value Float64, …)`) — a big win
  for *known* hot paths, but they hardcode those paths. Useful as
  per-deployment tuning, not as the shipped schema.
- **Skip indexes on `subject`** — redundant with the ORDER BY prefix.
- **Per-meter materialized views across *all* meters** — per-insert fan-out
  across thousands of user-defined meters collapses ingest. (Dedicated rollups
  for a *few known-schema* meters are different and *are* shipped — see
  [Known-meter rollups](#known-meter-rollups).)

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
