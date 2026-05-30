SELECT
  tumbleStart(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_map_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(data_as_map_events.data['tokens'], 19)) AS value,
  data_as_map_events.data['model'] AS model,
  data_as_map_events.data['provider'] AS provider
FROM data_as_map_events
WHERE data_as_map_events.namespace = {namespace:String}
  AND data_as_map_events.type = 'llm_request'
  AND data_as_map_events.subject IN {subjects:Array(String)}
  AND data_as_map_events.time >= {from:DateTime}
  AND data_as_map_events.time < {to:DateTime}
GROUP BY windowstart, windowend, model, provider
ORDER BY windowstart;
