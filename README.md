# ch-playground

ClickHouse table-design benchmark harness. Each scenario is a self-contained
variant (table schema + indexes + partitioning + ordering key) for the same
use-case, so ingest throughput and query latency can be compared head-to-head.

Inspired by [`clickhouse-playground`](../clickhouse-playground). Targets a
single ClickHouse node so table-design variants can be compared head-to-head
on the same host.

## Layout

- `devenv.nix` / `devenv.yaml` — pinned toolchain (clickhouse-client, Go,
  openspec).
- `scenarios/<name>/` — one table-design variant: `init.sql`, optional `seed.sql`,
  `queries/`.
- `bench/` — Go benchmark driver and result writers. Each run writes a JSON
  result file and a human-readable markdown report to `bench/results/<scenario>/`
  (both tracked in git).
- `openspec/` — spec-driven change proposals managed by
  [OpenSpec](https://openspec.dev).

## Getting started

```bash
# Enter the dev shell (auto-installs openspec + restores skills from skills-lock.json).
direnv allow            # or: devenv shell

# Point at any reachable ClickHouse and run the baseline benchmark:
export CLICKHOUSE_DSN="clickhouse://default:@127.0.0.1:9100/default"
go build -o .devenv/bin/bench ./bench/cmd/bench
./.devenv/bin/bench --scenario baseline-openmeter
```

See `bench/README.md` for the harness flags and result-file format.

## Workflow

New table variants and benchmark methodology changes are proposed via OpenSpec:

```bash
openspec list                 # see open changes / specs
openspec new <change-name>    # draft a new change
openspec validate             # check the current proposal
openspec archive <change>     # land it into specs/ after implementation
```

## Findings — optimal ClickHouse table + query

**Objective:** find the table design + query shape with the lowest **query-time CPU and
latency** for OpenMeter usage metering. Ingest cost is recorded but is *not* part of the
ranking — the optimum is the cost paid every time a query runs, not the one-time insert.

**Hard constraints (from the OpenMeter use case — these gate every candidate):**

1. **Generic** — one schema for all tenants/meters; no per-meter knowledge in the raw events
   table. Meters are user-defined: each carries its own `valueProperty` and `groupBy` as JSON
   paths resolved *at query time*, not a fixed `$.value`.
2. **No insert-side fan-out on the raw table** — no per-meter materialized views or `SELECT *`
   projections; thousands of meters would collapse ingest.
3. **Exact billing** — `uniqExact` for UNIQUE_COUNT (never approximate `uniq`), and
   `toDecimal128OrNull(...,19)` for exact-arithmetic meters. No precision shortcuts.

### Verdict

**Optimal generic table: `data JSON CODEC(ZSTD(3))`** with the upstream OpenMeter columns and
`ORDER BY (namespace, type, subject, toStartOfHour(time))` unchanged, plus a `bloom_filter` skip
index on `id` for the lookup-by-id path. **Optimal query:** the canonical meter aggregation
reading the meter's path via native-JSON typed subcolumns (`data.<valueProperty>` /
`data.<groupBy>`), `uniqExact` for UNIQUE_COUNT, `toDecimal128OrNull(...,19)` for exact meters.
One table, all meters, no per-meter DDL, no insert-side fan-out. Copy-paste DDL and query
templates are in the [Deliverable](#deliverable) section. The reasoning chain that gets here:

- **Native `JSON` over `String`+`JSON_VALUE` is the table-type answer.** A meter reads an
  arbitrary, tenant-defined path; native JSON accelerates *any* path via typed subcolumns with no
  per-field DDL, while `String`+`JSON_VALUE` re-parses the whole document on every scan. Measured
  on two paths no benchmark query had touched (5M rows, identical data): `$.latency_ms` float
  **4.6×**, nested `$.meta.region` **8.8×**, `$.latency_ms` decimal **3.1×**. Cost: ~21% lower
  ingest throughput, accepted under a query-time objective.
- **Fixed `MATERIALIZED value`/`group1`/`group2` columns are disqualified** (constraint 1). They
  only accelerate meters that read exactly those hardcoded paths; a new meter needs a new column
  + backfill. Their early "win" was an artifact of the benchmark hardcoding `$.value`. A composite
  "JSON + fixed materialized hot columns" was prototyped and rejected — same per-meter limitation,
  just `data-as-json` plus a manual-tuning burden.
- **JSON typed-path hints** (`data JSON(value Float64, …)`) give a measured **11.4×** on the
  *known* hot path (717→63 ms/5M) with non-hinted paths unaffected — but they hardcode `value`, so
  they are non-generic. Demoted to optional per-deployment tuning, not the shipped schema.
- **`CODEC(ZSTD(3))` on the whole `data` column is the one robust generic query-time lever:**
  ~**−11% CPU, −21% memory, −18% disk**, uniform across float / decimal / non-value paths (it
  names no path). Costs insert CPU (compression, not fan-out → survives the no-MV rule).
- **A `bloom_filter` on `id`** serves the one structurally weak access pattern — `event_query_v2`
  lookup-by-id (`WHERE namespace = ? AND id = ?`, no time bound), which the ORDER BY can't prune.
  Generic, format-agnostic, no row duplication. Prunes `611 → 3` granules at 5M; ≈2× better p99
  under concurrency. A tail-latency / I/O-scaling win for multi-tenant, not-fully-cached
  production — not a single-node median win.
- **Rejected/no-op levers:** `bloom_filter` on `subject` (redundant — `subject` is already the 3rd
  ORDER BY prefix, so the primary sparse index does the pruning; only ~8% when subjects are
  insert-clustered, nothing when scattered). Explicit `PREWHERE` (the optimizer already moves
  filters with `optimize_move_to_prewhere=1`). Projections for listing/lookup (the list always
  `SELECT`s `data`, so a covering projection duplicates the whole row → insert fan-out).

### The per-meter overlay (PR #3764) — a different question, measured

The OpenMeter team's "Meter query experimentation framework" ([PR #3764](https://github.com/openmeterio/openmeter/pull/3764))
reframes the problem: leave `om_events` frozen as the source-of-truth fallback, and add per-meter
**side tables** (e.g. `numeric_meter_v1`: `value Decimal(38,20)`,
`group_by_filters Map(LowCardinality(String),String)`, ORDER BY
`(namespace, query_engine_id, subject, hour)`) populated async off Kafka, computing value+groupBy
into native types at write time. A lifecycle (`created→reconciling→active→failed→deleting`) +
backfill cron + ~0.01% consistency check + streaming fallback neutralizes the MV-rebuild/backfill
objections — so the overlay legitimately relaxes the no-MV / generic / no-precompute rules, which
are scoped to the *raw* table only.

Measured here (`map-columns` overlay vs `data-as-json`, 500K api_request slice, fresh single-node
CH, `os_cpu`): the overlay won **−58% query CPU, −81% memory, ~2× fewer rows/bytes read** across 9
shared queries — reproducing and exceeding PR #3764's synthetic ~50%/~50% claim. Two structural
effects: (1) meter-scoped ORDER BY → tighter granule pruning (57K vs 116K rows; 7 vs 13 marks);
(2) native pre-cast `value Decimal128` + typed `Map` groupBy → zero per-row JSON work (biggest CPU
win is the decimal path, −71%; biggest memory win is groupBy, −92/−93%). `Decimal128` vs `Float64`
≈ 1–2%, confirming exact-billing decimal is effectively free.

**Two caveats keep this out of the shipped recommendation for the *current* system:**

1. **Design fit.** Today a meter def carries `valueProperty`/`groupBy` as JSON paths resolved at
   *query* time. The overlay pre-extracts them at *write* time, requiring the ingest pipeline to
   know each meter's paths — the per-meter-knowledge-at-write-time the current architecture avoids.
   Part 9 measures the *ceiling write-time pre-extraction buys*, not a drop-in for the shipped
   system.
2. **Write amplification.** One event feeds *every* meter whose `eventType` matches, each reading a
   different path from the same row. With a single native `value` column per overlay table, one
   event becomes **N physical overlay rows** (N = meters on that event type), each re-storing
   subject/time + its extracted value + a copy of the groupBy `Map`. Write and storage scale with
   *meter count*, not event count — the fan-out the raw-table constraints exist to prevent. The
   −58%/−81% figure is per-meter and query-time-only; it omits the N× write amplification.

**Net:** the `data JSON CODEC(ZSTD(3))` raw table is the optimal *generic raw table*, and the
answer for the current query-time meter-def model and the streaming fallback. The overlay is the
optimal *system* only if/when the product moves to compiling each meter's paths into side tables — a
different architecture, not this one. A native-`JSON` file-descriptor-exhaustion concern (raised by
the team) should be verified before putting `data JSON` on the main table; the overlay sidesteps it
(native typed columns + `Map`, no JSON column).

### Deliverable

The shipped recommendation is a minimal delta from upstream OpenMeter DDL: the *only* change to the
events table is `data String` → `data JSON CODEC(ZSTD(3))`, plus the `id` bloom index.

#### Events table

```sql
CREATE TABLE IF NOT EXISTS om_events (
  namespace   String,
  id          String,
  type        LowCardinality(String),
  subject     String,
  source      String,
  time        DateTime,
  data        JSON CODEC(ZSTD(3)),          -- ← table-type + generic compression lever
  ingested_at DateTime,
  stored_at   DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,   -- upstream; backfill/replay range scans
  INDEX om_events_id_bloom  id TYPE bloom_filter(0.01) GRANULARITY 1,  -- lookup-by-id path
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));   -- upstream; low→high cardinality, = the meter-query filter prefix
```

#### Query templates

All meter queries share one skeleton; the only per-meter variation is (a) which path is read for
the value, (b) which paths are read/filtered for `groupBy`, (c) which aggregate wraps the value.
OpenMeter interpolates `<valueProperty>`/`<groupBy[i]>` from the meter def; path reads use the
native-JSON typed-subcolumn form (`toString(data.<path>.:String)` / `.:Float64`), which is what
makes the template path-agnostic. Filters align 1:1 with the ORDER BY prefix → primary-index
pruning.

```sql
SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC')   AS windowend,
  <AGG>                                                 AS value,
  om_events.subject                                     AS subject
  -- , toString(om_events.data.<groupBy[i]>.:String) AS <alias_i>
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type      = {type:String}
  AND om_events.subject  IN {subjects:Array(String)}
  AND om_events.time     >= {from:DateTime}
  AND om_events.time      < {to:DateTime}
  -- AND toString(om_events.data.<groupBy[i]>.:String) = {<alias_i>:String}
GROUP BY windowstart, windowend, subject /*, <alias_i> */
ORDER BY windowstart;
```

`<AGG>` per OpenMeter aggregation type (path = the meter's `valueProperty`):

| Aggregation | `<AGG>` expression |
| --- | --- |
| `SUM` (float) | `sum(ifNotFinite(toFloat64OrNull(toString(data.<vp>.:Float64)), NULL))` |
| `SUM` (exact/decimal) | `sum(toDecimal128OrNull(nullIf(toString(data.<vp>.:Float64), 'null'), 19))` |
| `AVG` / `MIN` / `MAX` | `avg(…)` / `min(…)` / `max(…)` over the same float extraction |
| `COUNT` | `count(*)` |
| `UNIQUE_COUNT` | `uniqExact(nullIf(toString(data.<vp>.:String), 'null'))` — **never `uniq`** |
| `LATEST` | `argMax(toFloat64OrNull(toString(data.<vp>.:Float64)), om_events.time)` |

`windowSize` selects the granularity: `MINUTE`→`toIntervalMinute(1)`, `HOUR`→`toIntervalHour(1)`,
etc.; timezone-aware meters pass the tz string to `tumbleStart/tumbleEnd`. The
`scenarios/data-as-json/queries/*.sql` files are the reference implementation — `sum_hour` /
`sum_hour_decimal` (SUM float/decimal on `$.value`), `unique_count_hour` (`uniqExact`),
`sum_hour_group1[_group2]` (groupBy paths + filters), and the per-type queries
(`llm_tokens_by_model`, `kong_status_by_route`, `agent_runs_by_name`,
`workload_seconds_by_region`) which read different per-meter paths through the **same** template,
proving path-genericity.

#### Deliberately excluded (and why)

- **Fixed/`MATERIALIZED value`/`group1`/`group2` columns** — paths are per-meter; serve only the
  meters hand-cut for them, need DDL + backfill per new meter.
- **JSON typed-path hints in the shipped schema** — 11× on a *known* hot path but non-generic;
  optional per-deployment tuning only.
- **Materialized views / projections on the raw table** — per-insert fan-out across 1000s of
  meters collapses ingest; projections for listing duplicate the whole row (list always
  `SELECT data`).
- **`subject` bloom_filter** — redundant with the `subject` ORDER BY prefix.
- **`uniq` / approximate cardinality / float shortcuts on the decimal path** — billing exactness.
- **Explicit `PREWHERE` rewriting** — `optimize_move_to_prewhere=1` already does it (no-op).
