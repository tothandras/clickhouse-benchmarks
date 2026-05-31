-- BASE: raw total-period llm-token meter on the events table (baseline).
SELECT subject, sum(toDecimal128OrNull(toString(data.tokens), 19)) AS value
FROM proposal_with_llm_mv_events
WHERE type = 'llm_request'
  AND time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY subject
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
