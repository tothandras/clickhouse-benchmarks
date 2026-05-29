-- Variant: data-as-json + a bloom_filter skip index on `id`.
--
-- One-variable delta vs. scenarios/data-as-json: the added
-- `INDEX om_events_id_bloom`. Everything else (data JSON, the minmax skip
-- index on stored_at, PARTITION BY, ORDER BY) is identical so any latency /
-- rows-read difference on the point-lookup query is attributable to the
-- bloom filter alone.
--
-- Motivation: OpenMeter's event_query_v2 lookup-by-id is
--   WHERE namespace = ? AND id = ?
-- with no time bound. Against ORDER BY (namespace, type, subject, hour) the
-- primary index prunes on `namespace` only, then scans every granule in the
-- namespace looking for `id`. `id` is high-cardinality and never part of the
-- sort key, so a bloom_filter is the textbook generic lever: it names no
-- meter path (fully generic), adds no row duplication (cheap at merge time,
-- unlike a projection), and lets the reader skip granules that provably do
-- not contain the id.
--
-- `id` has NO fixed format (CloudEvents id is an arbitrary producer-supplied
-- string: UUID, ULID, hex, etc.). A bloom_filter is format-agnostic by
-- construction -- it hashes the whole string value -- so the index is valid
-- regardless of id shape. The seeder's 16-hex-digit id is only a stand-in;
-- nothing here assumes that format.
--
-- A projection cannot help this access pattern: event_query_v2 always
-- SELECTs `data`, so a covering projection would have to carry `data` ->
-- whole-row duplication -> the insert-side write amplification OpenMeter's
-- no-fan-out rule forbids. The bloom index is the only generic win here.

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
  INDEX om_events_id_bloom id TYPE bloom_filter(0.01) GRANULARITY 1,
  store_row_id String
) ENGINE = MergeTree
PARTITION BY toYYYYMM(time)
ORDER BY (namespace, type, subject, toStartOfHour(time));
