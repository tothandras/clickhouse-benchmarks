-- Variant: data column declared as native JSON instead of String.
--
-- One-variable delta vs. scenarios/baseline-openmeter: the `data` column
-- type. Every other column, the minmax skip index on `stored_at`, the
-- PARTITION BY, and the ORDER BY are unchanged so latency / ingest
-- differences are attributable to the column type alone.
--
-- Requires ClickHouse >= 24.8 (when the native JSON type became stable).
--
-- Queries under queries/ use the JSON dot-path form (`data.value`) instead
-- of `JSON_VALUE(data, '$.value')`. See queries/*.sql.

CREATE TABLE IF NOT EXISTS om_events (
  namespace String,
  id String,
  type LowCardinality(String),
  subject String,
  source String,
  time DateTime,
  data JSON,
  ingested_at DateTime,
  stored_at DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));
