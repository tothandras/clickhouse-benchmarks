-- kong.llm_request SUM($.tokens) total over [from, to) — baseline twin of
-- proposal/queries/kong_llm_tokens_total.sql. Same events + predicate →
-- identical scalar. Type-agnostic decimal read via JSON_VALUE.
SELECT sum(toDecimal128OrNull(JSON_VALUE(baseline_openmeter_events.data, '$.tokens'), 19)) AS value
FROM baseline_openmeter_events
WHERE baseline_openmeter_events.namespace = {namespace:String}
  AND baseline_openmeter_events.type = 'kong.llm_request'
  AND baseline_openmeter_events.time >= {from:DateTime}
  AND baseline_openmeter_events.time < {to:DateTime}
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
