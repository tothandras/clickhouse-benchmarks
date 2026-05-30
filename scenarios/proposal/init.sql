-- The proposal: combines the three composable, measured wins from the
-- other scenarios into one DDL. Recommended default for OpenMeter
-- usage metering on a single ClickHouse node.
--
-- Stacked levers (each measured independently on 10M heterogeneous events,
-- single-node CH 26.2.19.43, --iterations 10 --seed 42):
--
--   1. data JSON              (vs `data String`):  median CPU −30%
--   2. CODEC(ZSTD(3)) on data (vs default LZ4):    median p50 −16%, disk −43%
--                                                  (+5% CPU regression accepted
--                                                   for the disk and p50 wins)
--   3. bloom_filter on id     (no measured cost):  point lookups prune
--                                                  1224 → 12 granules (≈100×)
--
-- Each lever was verified head-to-head against the next-best variant on
-- this same 10M dataset before being included here; nothing in this DDL
-- is from a historical claim that wasn't reproduced on the current data.
--
-- Deliberately NOT stacked here:
--   - data Map(String, String) — mutually exclusive with data JSON on
--     the same axis (table-type). Map trades query-time for ingest-time;
--     pick one, not both. See scenarios/data-as-map/ if write throughput
--     dominates.
--   - ORDER BY extension with raw `time` — standalone it gives a clean
--     win vs data-as-json (median CPU −7%), but the lever does NOT
--     compose on top of ZSTD: stacking it here regressed p50 +3% AND
--     CPU +3% vs this proposal without it (19/19 queries flat or worse),
--     and cost ~7% ingest. Likely because ZSTD already compresses the
--     time-clustered `data` column well, so the extra in-granule sort
--     adds cost without I/O reduction. See scenarios/order-by-extended-time/
--     to use it standalone (without ZSTD); not appropriate as a default.
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
