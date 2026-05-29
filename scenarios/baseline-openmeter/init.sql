-- Baseline OpenMeter events table.
--
-- Verbatim reproduction of the DDL emitted by OpenMeter's ClickHouse
-- connector. Source of truth:
--   openmeter/streaming/clickhouse/event_query.go:20-49
-- Captured from openmeter commit 05ee77008ac1be1bdbf7139a506afaf52df3d65d.
--
-- Drift check: if the upstream string-template diverges from this file,
-- update this file and bump the commit SHA above (a CI check is a future
-- change — see openspec/changes/drift-detect-openmeter-ddl/).

CREATE TABLE IF NOT EXISTS om_events (
  namespace String,
  id String,
  type LowCardinality(String),
  subject String,
  source String,
  time DateTime,
  data String,
  ingested_at DateTime,
  stored_at DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));
