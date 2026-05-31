SELECT
  tumbleStart(baseline_openmeter_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(baseline_openmeter_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(JSON_VALUE(baseline_openmeter_events.data, '$.tokens'), 19)) AS value,
  JSON_VALUE(baseline_openmeter_events.data, '$.model') AS model,
  JSON_VALUE(baseline_openmeter_events.data, '$.provider') AS provider
FROM baseline_openmeter_events
WHERE baseline_openmeter_events.namespace = {namespace:String}
  AND baseline_openmeter_events.type = 'llm_request'
  AND baseline_openmeter_events.subject IN {subjects:Array(String)}
  AND baseline_openmeter_events.time >= {from:DateTime}
  AND baseline_openmeter_events.time < {to:DateTime}
GROUP BY windowstart, windowend, model, provider
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
