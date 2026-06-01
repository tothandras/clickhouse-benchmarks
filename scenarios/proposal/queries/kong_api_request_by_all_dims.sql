-- kong.api_request COUNT grouped by ALL 19 canonical groupBy dims of the
-- kong_konnect_api_request meter, hourly window. Worst-case high-cardinality
-- group-by on the base table: three of the dims (client_ip, request_uri,
-- request_user_agent) are ~unique per event. Measures the cost of materializing
-- every declared dim from the JSON payload. All paths use the untyped
-- toString(data.<path>.:String) access mandated by the README, matching
-- kong_status_by_route.sql so the read is apples-to-apples.
SELECT
  tumbleStart(proposal_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(proposal_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  toString(proposal_events.data.api_id.:String)                 AS api_id,
  toString(proposal_events.data.api_product_id.:String)         AS api_product_id,
  toString(proposal_events.data.api_product_version_id.:String) AS api_product_version_id,
  toString(proposal_events.data.application_id.:String)         AS application_id,
  toString(proposal_events.data.client_ip.:String)             AS client_ip,
  toString(proposal_events.data.control_plane_id.:String)       AS control_plane_id,
  toString(proposal_events.data.portal_id.:String)             AS portal_id,
  toString(proposal_events.data.request_host.:String)           AS request_host,
  toString(proposal_events.data.request_method.:String)         AS request_method,
  toString(proposal_events.data.request_uri.:String)           AS request_uri,
  toString(proposal_events.data.request_user_agent.:String)     AS request_user_agent,
  toString(proposal_events.data.response_http_status.:String)   AS response_http_status,
  toString(proposal_events.data.route_id.:String)               AS route_id,
  toString(proposal_events.data.route_name.:String)             AS route_name,
  toString(proposal_events.data.service_id.:String)             AS service_id,
  toString(proposal_events.data.service_name.:String)           AS service_name,
  toString(proposal_events.data.service_port.:String)           AS service_port,
  toString(proposal_events.data.service_protocol.:String)       AS service_protocol,
  toString(proposal_events.data.upstream_status.:String)        AS upstream_status
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = 'kong.api_request'
  AND proposal_events.subject IN {subjects:Array(String)}
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
GROUP BY
  windowstart, windowend,
  api_id, api_product_id, api_product_version_id, application_id, client_ip,
  control_plane_id, portal_id, request_host, request_method, request_uri,
  request_user_agent, response_http_status, route_id, route_name, service_id,
  service_name, service_port, service_protocol, upstream_status
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
