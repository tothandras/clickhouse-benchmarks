SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(ifNotFinite(toFloat64OrNull(JSON_VALUE(om_events.data, '$.value')), NULL)) AS value,
  om_events.subject AS subject,
  JSON_VALUE(om_events.data, '$.group1') AS group1,
  JSON_VALUE(om_events.data, '$.group2') AS group2
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = {type:String}
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
  AND JSON_VALUE(om_events.data, '$.group1') = {group1:String}
  AND JSON_VALUE(om_events.data, '$.group2') = {group2:String}
GROUP BY windowstart, windowend, subject, group1, group2
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
