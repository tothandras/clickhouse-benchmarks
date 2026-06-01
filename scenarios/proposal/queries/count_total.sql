SELECT
  tumbleStart(min(proposal_events.time), toIntervalMinute(1)) AS windowstart,
  tumbleEnd(max(proposal_events.time), toIntervalMinute(1)) AS windowend,
  count(*) AS value
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = {type:String}
  AND proposal_events.subject IN {subjects:Array(String)}
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
