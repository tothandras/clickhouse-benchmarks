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
-- Carries ALL the meter's groupBy dimensions as typed columns, so dim-filtered
-- meter queries (WHERE model=X / GROUP BY provider, …) route to the rollup and
-- avoid per-row JSON parsing on the base table. ORDER BY leads with the
-- always-present filter prefix (namespace, subject, window_start), then dims
-- low-card-first (model/provider/http_status) before the high-card IDs.
-- LowCardinality on the genuinely-low-card dims; plain String on the IDs.
CREATE TABLE IF NOT EXISTS llm_tokens_rollup (
  namespace        String,
  subject          String,
  window_start     DateTime,
  model            LowCardinality(String),
  provider         LowCardinality(String),
  http_status      LowCardinality(String),
  route_id         String,
  service_id       String,
  ai_plugin_id     String,
  control_plane_id String,
  tokens           AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree
ORDER BY (namespace, subject, window_start,
          model, provider, http_status,
          route_id, service_id, ai_plugin_id, control_plane_id);

-- ── the LIVE materialized view: maintains the rollup on every insert ──
-- Unqualified data.x (table-qualified JSON access is an UNKNOWN_IDENTIFIER
-- error). Token counts are integers, so the rollup STATE is UInt64 (compact,
-- exact); queries cast the sumMerge result to toDecimal128(...,19) at read time
-- to match the base table's billing-decimal aggregation. The GROUP BY includes
-- every dim so the rollup can be filtered/grouped on any of them.
CREATE MATERIALIZED VIEW IF NOT EXISTS llm_tokens_mv TO llm_tokens_rollup AS
SELECT
  namespace, subject,
  toStartOfHour(time) AS window_start,
  toString(data.model)            AS model,
  toString(data.provider)         AS provider,
  toString(data.http_status)      AS http_status,
  toString(data.route_id)         AS route_id,
  toString(data.service_id)       AS service_id,
  toString(data.ai_plugin_id)     AS ai_plugin_id,
  toString(data.control_plane_id) AS control_plane_id,
  sumState(toUInt64OrZero(toString(data.tokens))) AS tokens
FROM proposal_with_llm_mv_events
WHERE type = 'llm_request'
GROUP BY namespace, subject, window_start,
         model, provider, http_status,
         route_id, service_id, ai_plugin_id, control_plane_id;

-- ── one-time backfill of rows that existed BEFORE the MV (guarded) ──
-- The MV only captures NEW inserts. Seeding inserts after init, so at init time
-- the table is empty and this is a no-op; the guard makes re-running safe and
-- ensures backfill never overlaps the MV's forward coverage (no double-count).
INSERT INTO llm_tokens_rollup
SELECT namespace, subject, toStartOfHour(time) AS window_start,
       toString(data.model), toString(data.provider), toString(data.http_status),
       toString(data.route_id), toString(data.service_id),
       toString(data.ai_plugin_id), toString(data.control_plane_id),
       sumState(toUInt64OrZero(toString(data.tokens)))
FROM proposal_with_llm_mv_events
WHERE type = 'llm_request'
  AND (SELECT count() FROM llm_tokens_rollup) = 0
GROUP BY namespace, subject, window_start,
         toString(data.model), toString(data.provider), toString(data.http_status),
         toString(data.route_id), toString(data.service_id),
         toString(data.ai_plugin_id), toString(data.control_plane_id);
