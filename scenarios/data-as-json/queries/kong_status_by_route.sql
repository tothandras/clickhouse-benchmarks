SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  toString(om_events.data.response_http_status.:String) AS status,
  toString(om_events.data.route_name.:String) AS route
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = 'kong_api_request'
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
GROUP BY windowstart, windowend, status, route
ORDER BY windowstart;
