-- kong.api_request COUNT total over [from, to) — BASE-TABLE oracle, the
-- same-output sibling of kong_api_request_total_hybrid (which serves this from
-- the rollup). Pairs 1:1 with baseline-openmeter/queries/kong_api_request_total.sql.
SELECT count(*) AS value
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = 'kong.api_request'
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
