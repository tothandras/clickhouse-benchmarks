SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(om_events.data['duration_seconds'], 19)) AS value,
  om_events.data['region'] AS region
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = 'workload'
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
GROUP BY windowstart, windowend, region
ORDER BY windowstart;
