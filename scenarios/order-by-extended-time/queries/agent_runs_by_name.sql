SELECT
  tumbleStart(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(order_by_extended_time_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  toString(order_by_extended_time_events.data.agent_name.:String) AS agent_name
FROM order_by_extended_time_events
WHERE order_by_extended_time_events.namespace = {namespace:String}
  AND order_by_extended_time_events.type = 'agent_run'
  AND order_by_extended_time_events.subject IN {subjects:Array(String)}
  AND order_by_extended_time_events.time >= {from:DateTime}
  AND order_by_extended_time_events.time < {to:DateTime}
GROUP BY windowstart, windowend, agent_name
ORDER BY windowstart;
