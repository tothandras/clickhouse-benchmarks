-- kong.api_request COUNT grouped by two bounded dimensions
-- (data.service_name ~4 values, data.request_method ~5 values), hourly window.
-- Base-table read. PREWHERE-eligible filter; group-by reads two JSON paths.
-- Two-dim companion to the single-dim kong_api_request_by_method.sql and the
-- full-fan kong_api_request_by_all_dims.sql.
SELECT
  tumbleStart(proposal_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(proposal_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  toString(proposal_events.data.service_name.:String) AS service_name,
  toString(proposal_events.data.request_method.:String) AS request_method
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = 'kong.api_request'
  AND proposal_events.subject IN {subjects:Array(String)}
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
GROUP BY windowstart, windowend, service_name, request_method
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
