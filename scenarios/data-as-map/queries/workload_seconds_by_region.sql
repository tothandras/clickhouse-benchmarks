SELECT
  tumbleStart(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(data_as_map_events.data['duration_seconds'], 19)) AS value,
  data_as_map_events.data['region'] AS region
FROM data_as_map_events
WHERE data_as_map_events.namespace = {namespace:String}
  AND data_as_map_events.type = 'workload'
  AND data_as_map_events.subject IN {subjects:Array(String)}
  AND data_as_map_events.time >= {from:DateTime}
  AND data_as_map_events.time < {to:DateTime}
GROUP BY windowstart, windowend, region
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
