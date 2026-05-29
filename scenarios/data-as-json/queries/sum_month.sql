SELECT
  toDateTime(tumbleStart(om_events.time, toIntervalMonth(1), 'UTC'), 'UTC') AS windowstart,
  toDateTime(tumbleEnd(om_events.time, toIntervalMonth(1), 'UTC'), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(om_events.data.value), 19)) AS value,
  om_events.subject AS subject
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = {type:String}
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
GROUP BY windowstart, windowend, subject
ORDER BY windowstart;
