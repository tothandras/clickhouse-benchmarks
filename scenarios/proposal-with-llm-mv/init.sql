-- proposal-with-llm-mv — the proposal table PLUS a live materialized view that
-- maintains a pre-aggregate rollup for ONE known meter (llm_request, SUM tokens).
--
-- This is a SEPARATE scenario, not a change to the generic `proposal` table.
-- The generic table stays meter-agnostic (MergeTree, data JSON) for the hundreds
-- of unknown meters; this scenario adds a dedicated rollup + MV for a single
-- known-schema meter. That is the sanctioned exception to the "no per-meter MV
-- fan-out" rule (which governs PRODUCTION at 1000s of meters); demonstrating the
-- mechanism for 1-2 known meters does not collapse ingest.
--
-- What this scenario proves that the prod (backfill-only) scenario could not:
-- INCREMENTAL maintenance — the MV fires per insert block and AggregatingMergeTree
-- merges partial sumStates asynchronously. The queries/ verify exactness
-- including the two-batch-same-window merge case.

-- ── base table: identical to the generic proposal schema ──
CREATE TABLE IF NOT EXISTS proposal_with_llm_mv_events (
  namespace String,
  id String,
  type LowCardinality(String),
  subject String,
  source String,
  time DateTime,
  data JSON CODEC(ZSTD(3)),
  ingested_at DateTime,
  stored_at DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,
  INDEX om_events_id_bloom  id        TYPE bloom_filter(0.01) GRANULARITY 1,
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));

-- ── rollup target: dims-free, hour grain ──
-- Keyed (namespace, subject, window_start) ONLY — matches the total-period meter
-- query (GROUP BY subject). Including the meter's high-card dims would make the
-- rollup ~1 row/event (the false "1×" result from earlier analysis).
CREATE TABLE IF NOT EXISTS llm_tokens_rollup (
  namespace    String,
  subject      String,
  window_start DateTime,
  tokens       AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree
ORDER BY (namespace, subject, window_start);

-- ── the LIVE materialized view: maintains the rollup on every insert ──
-- Unqualified data.tokens (table-qualified JSON access is an UNKNOWN_IDENTIFIER
-- error). sumState keeps it billing-exact for integer tokens.
CREATE MATERIALIZED VIEW IF NOT EXISTS llm_tokens_mv TO llm_tokens_rollup AS
SELECT
  namespace, subject,
  toStartOfHour(time) AS window_start,
  sumState(toUInt64OrZero(toString(data.tokens))) AS tokens
FROM proposal_with_llm_mv_events
WHERE type = 'llm_request'
GROUP BY namespace, subject, window_start;

-- ── one-time backfill of rows that existed BEFORE the MV (guarded) ──
-- The MV only captures NEW inserts. Seeding inserts after init, so at init time
-- the table is empty and this is a no-op; the guard makes re-running safe and
-- ensures backfill never overlaps the MV's forward coverage (no double-count).
INSERT INTO llm_tokens_rollup
SELECT namespace, subject, toStartOfHour(time) AS window_start,
       sumState(toUInt64OrZero(toString(data.tokens)))
FROM proposal_with_llm_mv_events
WHERE type = 'llm_request'
  AND (SELECT count() FROM llm_tokens_rollup) = 0
GROUP BY namespace, subject, window_start;
