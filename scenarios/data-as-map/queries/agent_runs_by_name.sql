SELECT
  tumbleStart(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  data_as_map_events.data['agent_name'] AS agent_name
FROM data_as_map_events
WHERE data_as_map_events.namespace = {namespace:String}
  AND data_as_map_events.type = 'agent_run'
  AND data_as_map_events.subject IN {subjects:Array(String)}
  AND data_as_map_events.time >= {from:DateTime}
  AND data_as_map_events.time < {to:DateTime}
GROUP BY windowstart, windowend, agent_name
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
