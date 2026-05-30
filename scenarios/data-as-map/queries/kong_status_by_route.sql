SELECT
  tumbleStart(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  data_as_map_events.data['response_http_status'] AS status,
  data_as_map_events.data['route_name'] AS route
FROM data_as_map_events
WHERE data_as_map_events.namespace = {namespace:String}
  AND data_as_map_events.type = 'kong_api_request'
  AND data_as_map_events.subject IN {subjects:Array(String)}
  AND data_as_map_events.time >= {from:DateTime}
  AND data_as_map_events.time < {to:DateTime}
GROUP BY windowstart, windowend, status, route
ORDER BY windowstart;
