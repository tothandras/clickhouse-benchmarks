SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(ifNotFinite(toFloat64OrNull(toString(om_events.data.value.:Float64)), NULL)) AS value,
  om_events.subject AS subject
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = {type:String}
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
GROUP BY windowstart, windowend, subject
ORDER BY windowstart;
