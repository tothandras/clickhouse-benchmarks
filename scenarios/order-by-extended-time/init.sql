-- Variant: split PRIMARY KEY (truncated to hour) from ORDER BY (extended
-- with raw `time`).
--
-- One-variable delta vs. scenarios/data-as-json: the ORDER BY adds `time`
-- as a 5th sort column, while PRIMARY KEY stays the same 4-column prefix
-- ending in toStartOfHour(time). Everything else (data JSON, the minmax skip
-- index on stored_at, PARTITION BY) is identical, so the only thing this
-- isolates is the cost/benefit of in-granule sorting by raw time.
--
-- Motivation: an external usage-metering system was observed to use the same
-- split pattern (PK truncated to a coarse bucket for sparse-index efficiency;
-- ORDER BY extended with raw timestamp for in-granule sort). The sparse
-- primary index size depends only on PRIMARY KEY's cardinality (truncated to
-- hour, so ~1 entry per hour-bucket per subject), while ORDER BY controls
-- physical row order within parts. Adding raw `time` to ORDER BY means rows
-- within a granule are time-sorted, which can:
--   - Help time-range queries that read a partial hour by reducing the rows
--     a granule contributes;
--   - Improve compression of `time` itself (delta-encoded after sort);
--   - Make argMax-by-time / LATEST queries cheaper if they can take the last
--     row in a sorted segment.
-- Possible costs: more sort work at merge time; potentially worse
-- compression of trailing high-cardinality columns (we don't add `id` to
-- ORDER BY for exactly this reason).
--
-- Queries are byte-identical to scenarios/data-as-json/queries/ — only the
-- table layout changes.

CREATE TABLE IF NOT EXISTS order_by_extended_time_events (
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
PRIMARY KEY (namespace, type, subject, toStartOfHour(time))
ORDER BY    (namespace, type, subject, toStartOfHour(time), time);
