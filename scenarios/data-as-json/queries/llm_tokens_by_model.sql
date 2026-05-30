SELECT
  tumbleStart(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(data_as_json_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(data_as_json_events.data.tokens), 19)) AS value,
  toString(data_as_json_events.data.model.:String) AS model,
  toString(data_as_json_events.data.provider.:String) AS provider
FROM data_as_json_events
WHERE data_as_json_events.namespace = {namespace:String}
  AND data_as_json_events.type = 'llm_request'
  AND data_as_json_events.subject IN {subjects:Array(String)}
  AND data_as_json_events.time >= {from:DateTime}
  AND data_as_json_events.time < {to:DateTime}
GROUP BY windowstart, windowend, model, provider
ORDER BY windowstart;
