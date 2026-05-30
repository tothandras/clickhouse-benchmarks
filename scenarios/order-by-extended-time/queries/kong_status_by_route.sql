SELECT
  tumbleStart(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  toString(order_by_extended_time_events.data.response_http_status.:String) AS status,
  toString(order_by_extended_time_events.data.route_name.:String) AS route
FROM order_by_extended_time_events
WHERE order_by_extended_time_events.namespace = {namespace:String}
  AND order_by_extended_time_events.type = 'kong_api_request'
  AND order_by_extended_time_events.subject IN {subjects:Array(String)}
  AND order_by_extended_time_events.time >= {from:DateTime}
  AND order_by_extended_time_events.time < {to:DateTime}
GROUP BY windowstart, windowend, status, route
ORDER BY windowstart;
