-- The proposal: the all-in-one best model from our measured learnings.
-- Combines the composable table-design wins (data JSON + ZSTD(3) + id bloom)
-- with live materialized-view rollups for the two known-schema Kong meters
-- (kong.llm_request SUM tokens, kong.api_request COUNT). Recommended default
-- for OpenMeter usage metering on a single ClickHouse node.
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
--     pick one, not both. See scenarios/data-as-map/ if write throughput
--     dominates.
--   - ORDER BY extension with raw `time` (appending `time` to the sort key,
--     i.e. ORDER BY (namespace, type, subject, toStartOfHour(time), time)) —
--     does NOT reliably beat data-as-json: one run showed a small win
--     (median CPU −7%), a later 10M run showed a small regression (+6% CPU,
--     18/20 queries slower). Two seed-42 runs disagreeing in direction means
--     the extra in-granule sort is not a dependable lever (its sign is
--     run/host/version dependent), and it costs ingest. Not worth stacking.
--     The standalone scenario for this was removed; re-add the `, time`
--     suffix to a copy of this table to measure it on your own hardware.
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

-- ════════════════════════════════════════════════════════════════════════════
-- Pre-aggregate rollups for the two KNOWN-SCHEMA Kong meters, maintained by live
-- materialized views. The base table stays meter-agnostic for the hundreds of
-- unknown meters; these two MVs are the sanctioned exception (1-2 known meters,
-- not 1000s-meter fan-out). In production the MVs must consume the *deduped*
-- event stream. Asymmetric by design, from measured learnings:
--
--   • kong.llm_request (SUM $.tokens): DIMS-FULL rollup — carries all 14 groupBy
--     dims so it serves BOTH the total-period token sum AND dim-filtered queries
--     (by model/provider/route…). Token counts are integers → UInt64 sum state
--     (compact/fast); queries cast to toDecimal128(...,19) at read time.
--
--   • kong.api_request (COUNT): DIMS-FREE rollup — keyed only
--     (namespace, subject, window). Measured: a dims-full api rollup is ~1×
--     (its 19 dims incl. client_ip/request_uri are ~unique per event), so it
--     would be pure cost. The dominant total-period COUNT-by-subject query needs
--     no dims, and dims-free compresses 34× hourly / 625× daily. Dim-filtered
--     api queries (by route/status) stay on the base table.
--
-- Both total-period queries use arbitrary from/to → the 3-part hybrid (raw head
-- + rollup interior + raw tail) is the billing-exact read; see queries/.
-- ════════════════════════════════════════════════════════════════════════════

-- ── llm: dims-full rollup (SUM tokens, 14 dims), UInt64 state ──
CREATE TABLE IF NOT EXISTS proposal_llm_tokens_rollup (
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

CREATE MATERIALIZED VIEW IF NOT EXISTS proposal_llm_tokens_mv TO proposal_llm_tokens_rollup AS
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
FROM proposal_events
WHERE type = 'kong.llm_request'
GROUP BY namespace, subject, window_start,
         model, provider, http_status, cache_status, ai_plugin_name,
         ai_plugin_id, api_id, api_product_id, api_product_version_id,
         application_id, consumer_id, control_plane_id, route_id, service_id;

INSERT INTO proposal_llm_tokens_rollup
SELECT namespace, subject, toStartOfHour(time) AS window_start,
       toString(data.model), toString(data.provider), toString(data.http_status),
       toString(data.cache_status), toString(data.ai_plugin_name),
       toString(data.ai_plugin_id), toString(data.api_id), toString(data.api_product_id),
       toString(data.api_product_version_id), toString(data.application_id),
       toString(data.consumer_id), toString(data.control_plane_id),
       toString(data.route_id), toString(data.service_id),
       sumState(toUInt64OrZero(toString(data.tokens)))
FROM proposal_events
WHERE type = 'kong.llm_request'
  AND (SELECT count() FROM proposal_llm_tokens_rollup) = 0
GROUP BY namespace, subject, window_start,
         toString(data.model), toString(data.provider), toString(data.http_status),
         toString(data.cache_status), toString(data.ai_plugin_name),
         toString(data.ai_plugin_id), toString(data.api_id), toString(data.api_product_id),
         toString(data.api_product_version_id), toString(data.application_id),
         toString(data.consumer_id), toString(data.control_plane_id),
         toString(data.route_id), toString(data.service_id);

-- ── api: dims-free rollup (COUNT) ──
CREATE TABLE IF NOT EXISTS proposal_api_request_rollup (
  namespace    String,
  subject      String,
  window_start DateTime,
  cnt          AggregateFunction(count)
) ENGINE = AggregatingMergeTree
ORDER BY (namespace, subject, window_start);

CREATE MATERIALIZED VIEW IF NOT EXISTS proposal_api_request_mv TO proposal_api_request_rollup AS
SELECT
  namespace, subject,
  toStartOfHour(time) AS window_start,
  countState() AS cnt
FROM proposal_events
WHERE type = 'kong.api_request'
GROUP BY namespace, subject, window_start;

INSERT INTO proposal_api_request_rollup
SELECT namespace, subject, toStartOfHour(time) AS window_start, countState()
FROM proposal_events
WHERE type = 'kong.api_request'
  AND (SELECT count() FROM proposal_api_request_rollup) = 0
GROUP BY namespace, subject, window_start;
