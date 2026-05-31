-- BASE-TABLE other meter (no rollup): api_request COUNT — the production majority
-- of meters have no rollup and query the base table directly.
SELECT subject, count() AS value
FROM proposal_with_llm_mv_events
WHERE type = 'kong_api_request'
  AND time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY subject
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
