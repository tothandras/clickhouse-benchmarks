SELECT
  tumbleStart(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(data_as_map_events.data['value'], 19)) AS value,
  data_as_map_events.subject AS subject,
  data_as_map_events.data['group1'] AS group1,
  data_as_map_events.data['group2'] AS group2
FROM data_as_map_events
WHERE data_as_map_events.namespace = {namespace:String}
  AND data_as_map_events.type = {type:String}
  AND data_as_map_events.subject IN {subjects:Array(String)}
  AND data_as_map_events.time >= {from:DateTime}
  AND data_as_map_events.time < {to:DateTime}
  AND data_as_map_events.data['group1'] = {group1:String}
  AND data_as_map_events.data['group2'] = {group2:String}
GROUP BY windowstart, windowend, subject, group1, group2
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
