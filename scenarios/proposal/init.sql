-- The proposal: the recommended table design from our measured learnings.
-- A single meter-agnostic events table that stacks the composable table-design
-- wins (data JSON + ZSTD(3) + id bloom). Recommended default for OpenMeter
-- usage metering on a single ClickHouse node.
--
-- Stacked levers (each measured independently on 10M heterogeneous events,
-- single-node CH, --iterations 10 --seed 42):
--
--   1. data JSON              (vs `data String`):  median CPU −34%, p50 −32%
--   2. CODEC(ZSTD(3)) on data (vs default LZ4):    disk −43%, p50 −7% vs json
--                                                  (this run; p50 direction is
--                                                  stable, magnitude is not).
--                                                  Query CPU is run-variable
--                                                  and ~neutral (+5% / 0% / −8%
--                                                  across runs) — adopted for the
--                                                  disk and p50 wins, NOT a CPU win.
--   3. bloom_filter on id     (no measured cost):  point lookups prune
--                                                  1223 → 17 granules (≈72×).
--                                                  Inert for the meter queries
--                                                  (they never filter on id).
--   4. PREWHERE on group-by    (query-level, not DDL): the meter queries set
--                                                  optimize_move_to_prewhere=1 +
--                                                  allow_reorder_prewhere_conditions=1.
--                                                  Measured on the prod-data copy
--                                                  (4.59B rows): −38% to −46% p50 on
--                                                  selective group-by queries that
--                                                  filter a JSON-extracted field;
--                                                  ~neutral on plain aggregations.
--                                                  See the *_no_prewhere query
--                                                  variants to measure the lever.
--
-- Each lever was verified head-to-head against the next-best variant on
-- this same 10M dataset before being included here; nothing in this DDL
-- is from a historical claim that wasn't reproduced on the current data.
--
-- Deliberately NOT stacked here:
--   - data Map(String, String) — mutually exclusive with data JSON on
--     the same axis (table-type). Map trades query-time for ingest-time;
--     pick one, not both.
--   - ORDER BY extension with raw `time` (appending `time` to the sort key,
--     i.e. ORDER BY (namespace, type, subject, toStartOfHour(time), time)) —
--     does NOT reliably beat data-as-json: one run showed a small win
--     (median CPU −7%), a later 10M run showed a small regression (+6% CPU,
--     18/20 queries slower). Two seed-42 runs disagreeing in direction means
--     the extra in-granule sort is not a dependable lever (its sign is
--     run/host/version dependent), and it costs ingest. Not worth stacking.
--     Re-add the `, time` suffix to a copy of this table to measure it on
--     your own hardware.
--   - Per-meter materialized-view rollups — previously included for the two
--     known-schema Kong meters, now removed. The kong.api_request rollup was a
--     measured ≈1.0× (no-compression) failure, and per-meter MVs contradict the
--     meter-agnostic constraint the base table holds for the unbounded set of
--     unknown-schema meters. The Kong meters are now queried directly off this
--     base table (see queries/kong_*). The dropped MV DDL is recoverable from
--     git history if ever revisited on different data.
--
-- Everything else (column set, PARTITION BY, ORDER BY, the minmax skip
-- index on stored_at) is upstream OpenMeter DDL, unchanged.

CREATE TABLE IF NOT EXISTS proposal_events (
  namespace String,
  id String,
  type LowCardinality(String),
  subject String,
  source String,
  time DateTime,
  data JSON CODEC(ZSTD(3)),                                                -- ← (1) + (2)
  ingested_at DateTime,
  stored_at DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,
  INDEX om_events_id_bloom  id        TYPE bloom_filter(0.01) GRANULARITY 1,  -- ← (3)
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));
