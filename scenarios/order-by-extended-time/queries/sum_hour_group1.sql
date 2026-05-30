SELECT
  tumbleStart(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(order_by_extended_time_events.data.value), 19)) AS value,
  order_by_extended_time_events.subject AS subject,
  toString(order_by_extended_time_events.data.group1.:String) AS group1
FROM order_by_extended_time_events
WHERE order_by_extended_time_events.namespace = {namespace:String}
  AND order_by_extended_time_events.type = {type:String}
  AND order_by_extended_time_events.subject IN {subjects:Array(String)}
  AND order_by_extended_time_events.time >= {from:DateTime}
  AND order_by_extended_time_events.time < {to:DateTime}
  AND toString(order_by_extended_time_events.data.group1.:String) = {group1:String}
GROUP BY windowstart, windowend, subject, group1
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
