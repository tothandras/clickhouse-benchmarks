SELECT
  tumbleStart(proposal_events.time, toIntervalHour(1), 'UTC') AS windowstart,
  tumbleEnd(proposal_events.time, toIntervalHour(1), 'UTC') AS windowend,
  sum(toDecimal128OrNull(toString(proposal_events.data.duration_seconds), 19)) AS value,
  toString(proposal_events.data.region.:String) AS region
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = 'workload'
  AND proposal_events.subject IN {subjects:Array(String)}
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
GROUP BY windowstart, windowend, region
ORDER BY windowstart
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
