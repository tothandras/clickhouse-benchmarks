-- proposal-with-llm-mv — the proposal table PLUS a live materialized view that
-- maintains a pre-aggregate rollup for the canonical Kong LLM-token meter.
--
-- Meter: kong_konnect_llm_tokens — aggregation SUM, eventType "kong.llm_request",
-- valueProperty $.tokens, with 14 groupBy dimensions (all carried below so the
-- rollup can be filtered/grouped on any of them).
--
-- Separate scenario, not a change to the generic `proposal` table. Sanctioned
-- exception to the no-per-meter-MV rule (1-2 known meters, not 1000s-meter
-- fan-out). Demonstrates INCREMENTAL maintenance the backfill-only prod scenario
-- could not. Seedable (synthetic data, no customer namespaces).

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

-- ── rollup target: all 14 meter groupBy dims as typed columns, hour grain ──
-- Token counts are integers -> rollup STATE is AggregateFunction(sum, UInt64)
-- (compact, exact, fast merge); the billing-decimal cast happens at QUERY time
-- via toDecimal128(sumMerge(tokens), 19). ORDER BY leads with the always-present
-- filter prefix, then dims low-card-first for pruning.
CREATE TABLE IF NOT EXISTS llm_tokens_rollup (
  namespace               String,
  subject                 String,
  window_start            DateTime,
  model                   LowCardinality(String),
  provider                LowCardinality(String),
  http_status             LowCardinality(String),
  cache_status            LowCardinality(String),
  ai_plugin_name          LowCardinality(String),
  ai_plugin_id            String,
  api_id                  String,
  api_product_id          String,
  api_product_version_id  String,
  application_id          String,
  consumer_id             String,
  control_plane_id        String,
  route_id                String,
  service_id              String,
  tokens                  AggregateFunction(sum, UInt64)
) ENGINE = AggregatingMergeTree
ORDER BY (namespace, subject, window_start,
          model, provider, http_status, cache_status, ai_plugin_name,
          ai_plugin_id, api_id, api_product_id, api_product_version_id,
          application_id, consumer_id, control_plane_id, route_id, service_id);

-- ── the LIVE materialized view: maintains the rollup on every insert ──
-- Unqualified data.x (table-qualified JSON access is UNKNOWN_IDENTIFIER).
CREATE MATERIALIZED VIEW IF NOT EXISTS llm_tokens_mv TO llm_tokens_rollup AS
SELECT
  namespace, subject,
  toStartOfHour(time) AS window_start,
  toString(data.model)                  AS model,
  toString(data.provider)               AS provider,
  toString(data.http_status)            AS http_status,
  toString(data.cache_status)           AS cache_status,
  toString(data.ai_plugin_name)         AS ai_plugin_name,
  toString(data.ai_plugin_id)           AS ai_plugin_id,
  toString(data.api_id)                 AS api_id,
  toString(data.api_product_id)         AS api_product_id,
  toString(data.api_product_version_id) AS api_product_version_id,
  toString(data.application_id)         AS application_id,
  toString(data.consumer_id)            AS consumer_id,
  toString(data.control_plane_id)       AS control_plane_id,
  toString(data.route_id)               AS route_id,
  toString(data.service_id)             AS service_id,
  sumState(toUInt64OrZero(toString(data.tokens))) AS tokens
FROM proposal_with_llm_mv_events
WHERE type = 'kong.llm_request'
GROUP BY namespace, subject, window_start,
         model, provider, http_status, cache_status, ai_plugin_name,
         ai_plugin_id, api_id, api_product_id, api_product_version_id,
         application_id, consumer_id, control_plane_id, route_id, service_id;

-- ── one-time backfill of rows that existed BEFORE the MV (guarded no-op when seeded after init) ──
INSERT INTO llm_tokens_rollup
SELECT namespace, subject, toStartOfHour(time) AS window_start,
       toString(data.model), toString(data.provider), toString(data.http_status),
       toString(data.cache_status), toString(data.ai_plugin_name),
       toString(data.ai_plugin_id), toString(data.api_id), toString(data.api_product_id),
       toString(data.api_product_version_id), toString(data.application_id),
       toString(data.consumer_id), toString(data.control_plane_id),
       toString(data.route_id), toString(data.service_id),
       sumState(toUInt64OrZero(toString(data.tokens)))
FROM proposal_with_llm_mv_events
WHERE type = 'kong.llm_request'
  AND (SELECT count() FROM llm_tokens_rollup) = 0
GROUP BY namespace, subject, window_start,
         toString(data.model), toString(data.provider), toString(data.http_status),
         toString(data.cache_status), toString(data.ai_plugin_name),
         toString(data.ai_plugin_id), toString(data.api_id), toString(data.api_product_id),
         toString(data.api_product_version_id), toString(data.application_id),
         toString(data.consumer_id), toString(data.control_plane_id),
         toString(data.route_id), toString(data.service_id);
