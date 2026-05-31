SELECT
  tumbleStart(baseline_openmeter_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(baseline_openmeter_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  JSON_VALUE(baseline_openmeter_events.data, '$.agent_name') AS agent_name
FROM baseline_openmeter_events
WHERE baseline_openmeter_events.namespace = {namespace:String}
  AND baseline_openmeter_events.type = 'agent_run'
  AND baseline_openmeter_events.subject IN {subjects:Array(String)}
  AND baseline_openmeter_events.time >= {from:DateTime}
  AND baseline_openmeter_events.time < {to:DateTime}
GROUP BY windowstart, windowend, agent_name
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
