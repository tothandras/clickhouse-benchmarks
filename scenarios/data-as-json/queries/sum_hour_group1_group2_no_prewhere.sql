SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(om_events.data.value), 19)) AS value,
  om_events.subject AS subject,
  toString(om_events.data.group1.:String) AS group1,
  toString(om_events.data.group2.:String) AS group2
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = {type:String}
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
  AND toString(om_events.data.group1.:String) = {group1:String}
  AND toString(om_events.data.group2.:String) = {group2:String}
GROUP BY windowstart, windowend, subject, group1, group2
ORDER BY windowstart;
