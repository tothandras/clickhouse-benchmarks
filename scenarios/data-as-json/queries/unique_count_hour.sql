SELECT
  tumbleStart(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowend,
  uniqExact(nullIf(toString(data_as_json_events.data.value.:Float64), 'null')) AS value,
  data_as_json_events.subject AS subject
FROM data_as_json_events
WHERE data_as_json_events.namespace = {namespace:String}
  AND data_as_json_events.type = {type:String}
  AND data_as_json_events.subject IN {subjects:Array(String)}
  AND data_as_json_events.time >= {from:DateTime}
  AND data_as_json_events.time < {to:DateTime}
GROUP BY windowstart, windowend, subject
ORDER BY windowstart;
