SELECT
  toDateTime(tumbleStart(data_as_json_events.time, toIntervalMonth(1), 'UTC'), 'UTC') AS windowstart,
  toDateTime(tumbleEnd(data_as_json_events.time, toIntervalMonth(1), 'UTC'), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(data_as_json_events.data.value), 19)) AS value,
  data_as_json_events.subject AS subject
FROM data_as_json_events
WHERE data_as_json_events.namespace = {namespace:String}
  AND data_as_json_events.type = {type:String}
  AND data_as_json_events.subject IN {subjects:Array(String)}
  AND data_as_json_events.time >= {from:DateTime}
  AND data_as_json_events.time < {to:DateTime}
GROUP BY windowstart, windowend, subject
ORDER BY windowstart;
