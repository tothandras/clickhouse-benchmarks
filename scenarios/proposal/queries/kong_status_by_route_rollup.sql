-- kong.api_request COUNT grouped by status + route, served from the
-- DIMS-BOUNDED rollup (proposal_api_request_rollup) instead of the base table.
-- Direct A/B partner to kong_status_by_route.sql (same GROUP BY, base table):
-- this measures whether carrying the 16 bounded dims in the rollup actually
-- beats re-reading the JSON payload on proposal_events for a grouped api query.
--
-- Hour-aligned slice: the rollup is keyed by toStartOfHour(time), so for
-- whole-hour windows the rollup is billing-exact on its own (no 3-part hybrid
-- needed — that machinery is only for arbitrary, non-hour-aligned from/to; see
-- kong_api_request_total_hybrid.sql). Callers passing non-aligned from/to should
-- use the hybrid pattern. count() state → countMerge at read time (exact integer,
-- no decimal cast).
-- Window labels use the same tumbleStart/tumbleEnd expressions as the base-table
-- kong_status_by_route.sql so the two are byte-comparable; window_start is already
-- toStartOfHour-aligned, so tumbleStart over it is a no-op that just matches the
-- label format.
SELECT
  tumbleStart(window_start, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(window_start, toIntervalHour(1), 'UTC') AS windowend,
  countMerge(value) AS value,
  response_http_status AS status,
  route_name AS route
FROM proposal_api_request_rollup
WHERE namespace = {namespace:String}
  AND subject IN {subjects:Array(String)}
  AND window_start >= {from:DateTime}
  AND window_start < {to:DateTime}
GROUP BY windowstart, windowend, status, route
ORDER BY windowstart;
