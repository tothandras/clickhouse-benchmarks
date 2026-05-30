SELECT
  tumbleStart(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(data_as_json_events.data.duration_seconds), 19)) AS value,
  toString(data_as_json_events.data.region.:String) AS region
FROM data_as_json_events
WHERE data_as_json_events.namespace = {namespace:String}
  AND data_as_json_events.type = 'workload'
  AND data_as_json_events.subject IN {subjects:Array(String)}
  AND data_as_json_events.time >= {from:DateTime}
  AND data_as_json_events.time < {to:DateTime}
GROUP BY windowstart, windowend, region
ORDER BY windowstart;
