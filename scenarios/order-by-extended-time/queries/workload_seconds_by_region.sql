SELECT
  tumbleStart(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(order_by_extended_time_events.data.duration_seconds), 19)) AS value,
  toString(order_by_extended_time_events.data.region.:String) AS region
FROM order_by_extended_time_events
WHERE order_by_extended_time_events.namespace = {namespace:String}
  AND order_by_extended_time_events.type = 'workload'
  AND order_by_extended_time_events.subject IN {subjects:Array(String)}
  AND order_by_extended_time_events.time >= {from:DateTime}
  AND order_by_extended_time_events.time < {to:DateTime}
GROUP BY windowstart, windowend, region
ORDER BY windowstart;
