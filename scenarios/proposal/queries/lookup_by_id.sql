-- Functional check for OpenMeter event_query_v2 lookup-by-id:
--   WHERE namespace = ? AND id = ?   (no time bound)
--
-- This query verifies the lookup PATH works end to end and returns exactly the
-- one matching row. It is NOT a clean latency benchmark: `id` is a
-- user-provided, format-free string and the harness binds no id parameter, so
-- the query must resolve a real id at runtime via the inner subquery -- and
-- that resolver scan dominates the reported wall-clock. Do not read the
-- harness p50/p95 for this query as the cost of a point lookup.
--
-- The real signal is the query PLAN, not its wall-clock. Run:
--   EXPLAIN indexes = 1
--   SELECT ... FROM proposal_events
--   WHERE namespace = '<ns>' AND id = '<a literal id from the table>';
-- and confirm the `om_events_id_bloom` skip index prunes the scan (measured:
-- 611 -> 3 granules, 9 -> 1 parts at 5M rows). Against a no-bloom table
-- (e.g. scenarios/baseline-openmeter) the same lookup touches all granules
-- in the namespace.
--
-- Measured wall-clock (literal id, 5M rows, single node): the bloom reads
-- ~14-42x fewer rows; median is near break-even when everything is in page
-- cache, but p99 under concurrency (c=16) roughly halves (85ms -> 41ms) and
-- the I/O reduction is what scales on cold cache / many-namespace tables.
-- See the Findings section in the top-level README.md.
--
-- SELECT list matches event_query_v2 (includes `data`) so row shape and read
-- cost reflect the real query, not a key-only shortcut.
SELECT
  proposal_events.id AS id,
  proposal_events.type AS type,
  proposal_events.subject AS subject,
  proposal_events.source AS source,
  proposal_events.time AS time,
  proposal_events.data AS data,
  proposal_events.ingested_at AS ingested_at,
  proposal_events.stored_at AS stored_at,
  proposal_events.store_row_id AS store_row_id
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.id = (
    SELECT id FROM proposal_events
    WHERE namespace = {namespace:String}
    ORDER BY namespace, type, subject, time
    LIMIT 1 OFFSET 100000
  )
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
