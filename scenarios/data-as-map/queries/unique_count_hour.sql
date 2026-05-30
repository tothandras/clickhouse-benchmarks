SELECT
  tumbleStart(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowend,
  uniqExact(nullIf(data_as_map_events.data['value'], 'null')) AS value,
  data_as_map_events.subject AS subject
FROM data_as_map_events
WHERE data_as_map_events.namespace = {namespace:String}
  AND data_as_map_events.type = {type:String}
  AND data_as_map_events.subject IN {subjects:Array(String)}
  AND data_as_map_events.time >= {from:DateTime}
  AND data_as_map_events.time < {to:DateTime}
GROUP BY windowstart, windowend, subject
ORDER BY windowstart;
