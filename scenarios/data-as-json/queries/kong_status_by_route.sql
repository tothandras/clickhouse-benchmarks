SELECT
  tumbleStart(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  toString(data_as_json_events.data.response_http_status.:String) AS status,
  toString(data_as_json_events.data.route_name.:String) AS route
FROM data_as_json_events
WHERE data_as_json_events.namespace = {namespace:String}
  AND data_as_json_events.type = 'kong_api_request'
  AND data_as_json_events.subject IN {subjects:Array(String)}
  AND data_as_json_events.time >= {from:DateTime}
  AND data_as_json_events.time < {to:DateTime}
GROUP BY windowstart, windowend, status, route
ORDER BY windowstart;
