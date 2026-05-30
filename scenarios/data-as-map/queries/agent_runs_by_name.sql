SELECT
  tumbleStart(om_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(om_events.time, toIntervalHour(1), 'UTC') AS windowend,
  count(*) AS value,
  om_events.data['agent_name'] AS agent_name
FROM om_events
WHERE om_events.namespace = {namespace:String}
  AND om_events.type = 'agent_run'
  AND om_events.subject IN {subjects:Array(String)}
  AND om_events.time >= {from:DateTime}
  AND om_events.time < {to:DateTime}
GROUP BY windowstart, windowend, agent_name
ORDER BY windowstart;
