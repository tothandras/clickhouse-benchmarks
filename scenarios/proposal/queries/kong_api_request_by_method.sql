-- kong.api_request COUNT grouped by a single low-cardinality dimension
-- (data.request_method, ~5 values), hourly window. Base-table read.
-- PREWHERE-eligible filter on namespace/type/subject; group-by reads one
-- JSON path. Cardinality-ladder companion to kong_api_request_by_service.sql /
-- kong_status_by_route.sql / kong_api_request_by_all_dims.sql.
SELECT
  tumbleStart(proposal_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(proposal_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  toString(proposal_events.data.request_method.:String) AS request_method
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = 'kong.api_request'
  AND proposal_events.subject IN {subjects:Array(String)}
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
GROUP BY windowstart, windowend, request_method
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
