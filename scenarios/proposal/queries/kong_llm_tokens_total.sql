-- kong.llm_request SUM($.tokens) total over [from, to), read directly from the
-- base table. Pairs 1:1 with baseline-openmeter/queries/kong_llm_tokens_total.sql.
-- Type-agnostic decimal read.
SELECT sum(toDecimal128OrNull(toString(proposal_events.data.tokens), 19)) AS value
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = 'kong.llm_request'
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
