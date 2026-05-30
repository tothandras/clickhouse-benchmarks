-- Variant: data column declared as Map(String, String) instead of String/JSON.
--
-- One-variable delta vs. scenarios/baseline-openmeter and scenarios/data-as-json:
-- the `data` column type. Every other column, the minmax skip index on
-- `stored_at`, the PARTITION BY, and the ORDER BY are unchanged so latency
-- and ingest differences are attributable to the column type alone.
--
-- This scenario requires the seeder to emit flat (top-level-only) payloads
-- since Map(String, String) cannot represent nested objects or arrays. The
-- seeder asserts this with TestSeedNoNestedPayloads; bench/seed/seed.go's
-- resolveDataFormat detects this column type and switches the writer to
-- Append a map directly.
--
-- Queries under queries/ access fields by Map key (`data['value']`) instead
-- of by JSON path (`JSON_VALUE(data, '$.value')` or `data.value`). See
-- queries/*.sql. Map key access avoids JSON parsing at query time entirely,
-- at the cost of losing native JSON typed-subcolumn access for numeric paths
-- (every value is a String). Queries convert to Decimal128 with
-- toDecimal128OrNull(data['<path>'], 19) for billing-exact arithmetic.

CREATE TABLE IF NOT EXISTS om_events (
  namespace String,
  id String,
  type LowCardinality(String),
  subject String,
  source String,
  time DateTime,
  data Map(String, String),
  ingested_at DateTime,
  stored_at DateTime,
  INDEX om_events_stored_at stored_at TYPE minmax GRANULARITY 4,
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));
