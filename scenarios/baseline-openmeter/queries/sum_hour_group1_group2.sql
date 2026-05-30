SELECT
  tumbleStart(baseline_openmeter_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(baseline_openmeter_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(JSON_VALUE(baseline_openmeter_events.data, '$.value'), 19)) AS value,
  baseline_openmeter_events.subject AS subject,
  JSON_VALUE(baseline_openmeter_events.data, '$.group1') AS group1,
  JSON_VALUE(baseline_openmeter_events.data, '$.group2') AS group2
FROM baseline_openmeter_events
WHERE baseline_openmeter_events.namespace = {namespace:String}
  AND baseline_openmeter_events.type = {type:String}
  AND baseline_openmeter_events.subject IN {subjects:Array(String)}
  AND baseline_openmeter_events.time >= {from:DateTime}
  AND baseline_openmeter_events.time < {to:DateTime}
  AND JSON_VALUE(baseline_openmeter_events.data, '$.group1') = {group1:String}
  AND JSON_VALUE(baseline_openmeter_events.data, '$.group2') = {group2:String}
GROUP BY windowstart, windowend, subject, group1, group2
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
