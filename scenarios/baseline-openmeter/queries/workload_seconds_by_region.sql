SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(ifNotFinite(toFloat64OrNull(JSON_VALUE(om_events.data, '$.duration_seconds')), NULL)) AS value,
  JSON_VALUE(om_events.data, '$.region') AS region
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = 'workload'
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
GROUP BY windowstart, windowend, region
ORDER BY windowstart;
