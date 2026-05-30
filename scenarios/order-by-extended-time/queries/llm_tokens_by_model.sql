SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(om_events.data.tokens), 19)) AS value,
  toString(om_events.data.model.:String) AS model,
  toString(om_events.data.provider.:String) AS provider
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = 'llm_request'
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
GROUP BY windowstart, windowend, model, provider
ORDER BY windowstart;
