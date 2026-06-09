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

## Measuring COGS

`bench cogs` measures what the OpenMeter ClickHouse workload *costs* — the
question the perf tables imply but cannot state. It runs workload cells
(`cells/*.json`: paced ingest + weighted query replay) against a service,
attributes resource consumption to `{insert, merge, query, idle}` from system
tables, prices it with a checked-in pricing profile (`pricing/*.json`), and
writes a unit-cost card per run. See `bench/README.md` for the CLI.

```bash
# Smoke (devenv ClickHouse, zero rates, short phases):
bench cogs --cell mixed-5keps-4qps --profile ci --pricing-profile local-zero --preload-rows 500000

# Real measurement (dedicated ClickHouse Cloud service, pinned autoscaling):
bench cogs --cell ingest-5k
bench cogs --cell query-4qps --skip-init
bench cogs compare bench/results/proposal/cogs/<a>.json bench/results/proposal/cogs/<b>.json
bench cogs validate <ingest-only.json> <query-only.json> <mixed.json>
```

### Methodology

- **Merge lag.** Merges run after inserts; the drain phase (default 15m)
  catches merges triggered by measure-window inserts and books them to ingest.
  Symmetrically, soak-triggered merges completing during measure are counted —
  at steady state the two leakages net out, which is why the soak phase exists
  (its gate: active part count stable ±10% over 5 polls; `parts_plateau:
  false` in the result means steady state was not reached, treat $/1M-events
  with suspicion).
- **Multi-replica accounting.** `system.query_log`/`system.part_log` are
  per-replica and Cloud load-balances connections, so all collectors read
  `clusterAllReplicas(default, ...)` and flush logs cluster-wide. Available
  CPU = reachable replicas × per-replica vCPUs (cgroup limit, falling back to
  `max_threads`).
- **Live event time.** The ingest driver stamps wall-clock event times
  (payloads stay deterministic per seed); the replayer binds a sliding
  `[now-3d, now)` window per arrival, so queries scan fresh data as it lands
  and scan size stays stationary.
- **Cache state.** A configurable fraction of replayed queries runs with
  `enable_filesystem_cache=0` and is costed separately (`warm`/`cold`).
- **`max_threads`** and any other per-query settings come from the cell
  manifest and are recorded in the result; don't compare runs with different
  settings.
- **Idle cell semantics.** The `idle` cell measures the *awake-but-unloaded*
  floor. It does not measure Cloud idling behavior: the harness's own polling
  keeps the service awake, and idling economics need the usage export.
- **Dedicated service required.** Foreign databases mean foreign merges and
  queries polluting coverage; the runner warns (`foreign_databases` flag).
- **Do not extrapolate across tiers/shapes.** Costs are priced on the pinned
  service shape in the pricing profile; a different tier has different rates
  AND different cache/memory behavior. The runner flags `shape_mismatch` when
  the detected shape differs from the profile.
- **Reconciliation deltas** (`--usage-export` at run time or `bench cogs
  reconcile` afterwards; >20% flags the run): expected causes are autoscaling
  movement during the window, a shared service, system background work,
  idling between phases — and, with the Cloud usage-statement CSV, its daily
  granularity: pro-rating a sub-day measure window out of a daily dollar
  total assumes uniform usage, so statement-based reconciliation is
  indicative unless the cell spans most of the day.
- **Two prices, always.** `billed_shape` reconciles with the invoice (window
  cost split by CPU shares; the remainder is the idle floor — at low
  utilization the floor dominates, which is the point). `cpu_linear` is the
  marginal cost on an already-busy service. When the detected capacity matches
  the profile shape the per-component figures coincide; the difference is the
  idle floor.

### First measurement: 100M events on ClickHouse Cloud (2026-06-09)

`mixed-5keps-4qps` against a dedicated 3-replica × 8 GiB / 2 vCPU Scale
service (eu-central-1, 3 compute units): 100M preloaded events, 30m soak
(parts plateau reached), 1h measure with 5k events/sec live ingest (achieved
4996, rate satisfied) + 4 qps production-mix replay (achieved 4.01, zero
queue pressure), 15m drain. Zero harness errors; cluster-wide log flush and
native merge ProfileEvents on Cloud (no estimation fallbacks). Result:
`bench/results/proposal/cogs/2026-06-09T19-14-53Z.{json,md}`.

| Unit cost (billed-shape) | Value |
| --- | --- |
| $ / 1M events ingested | **$0.0013** — insert 22% / **merge 78%** |
| $ / 1k queries: meter_agg | $0.0049 warm / $0.0085 cold |
| $ / 1k queries: payload_heavy | $0.0052 warm / $0.0102 cold |
| $ / 1k queries: key_only | $0.0036 warm / $0.0065 cold |
| $ / 1k queries: lookup | $0.0133 warm / $0.0227 cold |
| Storage (53.4 bytes/event settled) | $0.00135 / 1M events / month |
| Idle floor (this shape, 100%-active bound) | $654 / month |

CPU attribution over the 1h window (6 vCPUs × 3600s = 21,602 available
cpu-seconds, coverage 11.2%): insert 128s, merge 441s, queries 1,840s, idle
remainder. Window cost $0.896, of which $0.796 was idle floor.

**Findings:**

1. **Merges are 3.4× the insert CPU** — 78% of the ingest-path cost lives in
   background merges, which `system.query_log` structurally cannot see. Any
   per-event cost model derived from insert timings alone undercounts ~4.5×.
2. **The idle floor dominates at this scale.** 5k events/sec + 4 qps consumed
   ~$0.10/h of an $0.90/h window: the service was 89% idle. Utilization
   (consolidation, idling) is the COGS lever here, not query optimization.
3. **Cold queries cost ~1.7–2× warm** across every class — the filesystem
   cache is worth roughly half the query bill.

Caveats attached to this run: mix weights are placeholders (flagged in the
report), the eu-central-1 rate in the pricing profile is copied from
us-east-1 pending calibration against the usage statement, and reconciliation
awaits a statement that covers the run window (the daily-granularity CSV
makes sub-day reconciliation indicative either way).

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
(`2026-05-29 00:00:00 → 2026-06-01 00:00:00`).

### Query performance — baseline vs proposal

CPU is `OSCPUVirtualTimeMicroseconds` p50 (summed across query threads); p50 is
wall-clock. **Proposal is faster on every query that reads a `data` path** —
median **−42% p50 / −44.5% CPU** across the 30 shared queries.

| query | base p50 | prop p50 | base CPU | prop CPU | CPU Δ |
| --- | ---: | ---: | ---: | ---: | ---: |
| `kong_status_by_route` | 91 ms | 15 ms | 1559 ms | 193 ms | **−88%** |
| `llm_tokens_by_model` | 67 ms | 15 ms | 1148 ms | 196 ms | **−83%** |
| `kong_llm_tokens_total` | 101 ms | 22 ms | 1783 ms | 342 ms | **−81%** |
| `sum_day_by_dim` | 51 ms | 14 ms | 835 ms | 176 ms | **−79%** |
| `sum_hour_group1_group2` | 74 ms | 26 ms | 1227 ms | 372 ms | −70% |
| `sum_hour_group1_group2_no_prewhere` | 71 ms | 25 ms | 1184 ms | 373 ms | −69% |
| `sum_total_filter2` | 55 ms | 23 ms | 903 ms | 343 ms | −62% |
| `sum_hour_group1` | 65 ms | 29 ms | 1098 ms | 445 ms | −60% |
| `sum_total_filter1` | 57 ms | 26 ms | 954 ms | 403 ms | −58% |
| `sum_hour_group1_no_prewhere` | 60 ms | 30 ms | 1008 ms | 450 ms | −55% |
| `workload_seconds_by_region` | 17 ms | 10 ms | 236 ms | 113 ms | −52% |
| `sum_total` | 65 ms | 35 ms | 1095 ms | 563 ms | −49% |
| `sum_total_by_subject` | 67 ms | 37 ms | 1116 ms | 596 ms | −47% |
| `sum_month` / `max` / `min` / `sum_day` / `sum_hour` / `sum_day_by_subject` / `sum_hour_tz` / `sum_no_window` | ~68 ms | ~40 ms | ~1160 ms | ~650 ms | ≈−44% |
| `avg_hour` | 72 ms | 43 ms | 1214 ms | 704 ms | −42% |
| `latest_hour` | 98 ms | 61 ms | 1692 ms | 1015 ms | −40% |
| `sum_minute` | 83 ms | 52 ms | 1385 ms | 838 ms | −39% |
| `unique_count_hour` | 64 ms | 44 ms | 1077 ms | 708 ms | −34% |
| `count_total` | 11 ms | 8 ms | 127 ms | 89 ms | −30% |
| `agent_runs_by_name` | 8 ms | 7 ms | 76 ms | 55 ms | −28% |
| `kong_api_request_total` | 9 ms | 9 ms | 112 ms | 99 ms | −11% |
| `count_hour` | 14 ms | 14 ms | 193 ms | 172 ms | −11% |
| `distinct_subjects` | 24 ms | 25 ms | 367 ms | 379 ms | +3% |

The win scales with how much `data` a query reads: the per-meter path queries
(`kong_status_by_route`, `llm_tokens_by_model`, `kong_llm_tokens_total`,
`sum_day_by_dim`) win **−79% to −88%** because native JSON reads only the named
subcolumn while String parses the whole document per row. Queries that touch no
`data` path are near-neutral: `count_hour`/`kong_api_request_total` (key-only
`count(*)`) move −11%, and `distinct_subjects` (a `DISTINCT subject` scan, no
`data` read) is the one query slightly slower on proposal (+3% CPU, 367→379 ms) —
the JSON column adds marginal primary-key-scan overhead it can't offset without a
payload read.

**`proposal`-only queries** (the extra grouped api variants + the lookup path;
no baseline equivalent):

| query | p50 | CPU p50 | notes |
| --- | ---: | ---: | --- |
| `kong_api_request_by_method` | 11 ms | 125 ms | grouped (1 dim), base table |
| `kong_api_request_by_service` | 14 ms | 178 ms | grouped (2 dims), base table |
| `kong_api_request_by_all_dims` | 193 ms | 2259 ms | grouped (all 19 dims), base table — worst-case fan-out |
| `lookup_by_id` | 52 ms | 766 ms | point-lookup path (bloom-pruned; see Findings) |

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

**30/30 shared queries MATCH** between `data String` and `data JSON` — every
aggregation (SUM/COUNT/AVG/MIN/MAX/UNIQUE/LATEST, grouped and total, including
the production-shaped range-total queries) is value-identical, so the two designs
compute the same billing numbers.

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
recommended design**: −42% median p50 / −44.5% median CPU across 30 shared meter
queries, −31% on disk, and identical billing values to the baseline (30/30 value
parity). The only query that regresses is `distinct_subjects` (+3% CPU) — a
`DISTINCT subject` scan that reads no `data` path, so JSON can't help it; every
query that touches `data` improves.

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
Two table-design scenarios, 30 shared type-agnostic decimal meter queries
(`proposal` runs 34: the shared set + `lookup_by_id.sql` + the 3 extra
grouped Kong api variants). The shared set includes the production-shaped
range-total queries (`sum_total*`, `count_total`, `distinct_subjects`,
`sum_day_by_*`) mapped from the live OpenMeter slow-query log. Per-run JSON +
markdown reports are under [`bench/results/<scenario>/`](bench/results/). The
delta tables below are `bench compare baseline-openmeter <variant>` output, not
hand-transcribed.

**Median Δ across the 30 shared meter queries (vs baseline `data String`):**

| Variant | DDL change vs baseline | Median p50 Δ | Median CPU Δ | Ingest Δ |
| --- | --- | ---: | ---: | ---: |
| **`proposal`** | `data JSON CODEC(ZSTD(3))` + `bloom_filter` on `id` | **−42%** | **−44.5%** | −31% |

(The −31% ingest Δ is native JSON's write-time cost — parsing each payload into
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
the fastest table design — **−42% median p50 / −44.5% CPU** vs the String baseline.

**Per-meter-path queries** (read fields from large multi-field payloads — where
touching the whole `data` value costs most), CPU vs baseline (fresh 10M):

| query | proposal CPU Δ |
| --- | ---: |
| `kong_status_by_route` | **−88%** |
| `llm_tokens_by_model` | **−83%** |
| `workload_seconds_by_region` | **−52%** |

Native JSON wins by reading only the named typed subcolumn, while a String column
parses the whole document per row. The wider the payload, the bigger the gap.

**`count_hour` / `kong_api_request_total`** (no `data` read in the agg) move
within run-to-run noise (−11%).

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
