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

### Why this and not the alternatives

- **Native `JSON` beats `String`+`JSON_VALUE`.** Meters define their own
  paths, so the table can't pre-extract them. Native JSON gives typed
  subcolumns on any path; `String` re-parses the whole document each scan.
  On paths the benchmark had never touched, the speedup was **3–9×**
  (including a nested path and a decimal path). The cost is ~21% lower
  ingest throughput, which we accept under a query-time objective.
- **`CODEC(ZSTD(3))` on the `data` column** is the one robust generic
  query-time win: ~−11% CPU, −21% memory, −18% disk, uniform across float,
  decimal, and non-value paths. It compresses every path without naming
  any, so it survives the "no per-meter knowledge" rule.
- **`bloom_filter` on `id`** fixes the one access pattern the ORDER BY
  can't help: `WHERE namespace = ? AND id = ?` with no time bound. Prunes
  611→3 granules at 5M rows, ≈2× better p99 under concurrency. Generic and
  no row duplication.
- **Fixed `MATERIALIZED` columns** for `value`/`group1`/`group2` only win
  for the meters they were hand-cut for, so they're disqualified by the
  per-meter constraint — any new meter would need new DDL and a backfill.
- **JSON typed-path hints** (`data JSON(value Float64, …)`) give a real
  11× on a known hot path, but they hardcode that path. Useful as
  per-deployment tuning when the hot paths are known, not as the shipped
  schema.
- **A `bloom_filter` on `subject`** is mostly redundant: `subject` is
  already the third ORDER BY prefix, so the primary index does the
  pruning. Skip it.

### The per-meter overlay (a different question)

The OpenMeter team's
[meter-query-engine direction](https://github.com/openmeterio/openmeter/pull/3764)
leaves `om_events` frozen and adds per-meter *side tables* that pre-extract
`value` and `groupBy` at write time. Measured here (`map-columns` vs
`data-as-json`, same data), the side table won **−58% CPU, −81% memory**
on the per-meter slice — a real win.

It's not the answer for the current system, though, because (1) it requires
the ingest pipeline to know each meter's paths up front (the architecture
deliberately doesn't today), and (2) one event becomes N physical rows when
N meters watch its event type, so write and storage scale with meter count
rather than event count. The recommendation above is the optimum for the
shared raw table that exists today; the side-table direction is the
optimum for a different system you'd have to build alongside it.
