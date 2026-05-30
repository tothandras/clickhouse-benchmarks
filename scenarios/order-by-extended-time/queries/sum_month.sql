SELECT
  toDateTime(tumbleStart(order_by_extended_time_events.time, toIntervalMonth(1), 'UTC'), 'UTC') AS windowstart,
  toDateTime(tumbleEnd(order_by_extended_time_events.time, toIntervalMonth(1), 'UTC'), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(order_by_extended_time_events.data.value), 19)) AS value,
  order_by_extended_time_events.subject AS subject
FROM order_by_extended_time_events
WHERE order_by_extended_time_events.namespace = {namespace:String}
  AND order_by_extended_time_events.type = {type:String}
  AND order_by_extended_time_events.subject IN {subjects:Array(String)}
  AND order_by_extended_time_events.time >= {from:DateTime}
  AND order_by_extended_time_events.time < {to:DateTime}
GROUP BY windowstart, windowend, subject
ORDER BY windowstart;
