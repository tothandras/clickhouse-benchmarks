SELECT
  tumbleStart(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(data_as_json_events.data.value), 19)) AS value,
  data_as_json_events.subject AS subject,
  toString(data_as_json_events.data.group1.:String) AS group1,
  toString(data_as_json_events.data.group2.:String) AS group2
FROM data_as_json_events
WHERE data_as_json_events.namespace = {namespace:String}
  AND data_as_json_events.type = {type:String}
  AND data_as_json_events.subject IN {subjects:Array(String)}
  AND data_as_json_events.time >= {from:DateTime}
  AND data_as_json_events.time < {to:DateTime}
  AND toString(data_as_json_events.data.group1.:String) = {group1:String}
  AND toString(data_as_json_events.data.group2.:String) = {group2:String}
GROUP BY windowstart, windowend, subject, group1, group2
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
