-- BASE: dim-filtered meter query — sum tokens for one model, grouped by provider.
-- Parses data.model/provider/tokens per row on the base table.
SELECT toString(data.provider) AS provider, sum(toDecimal128OrNull(toString(data.tokens), 19)) AS value
FROM proposal_with_llm_mv_events
WHERE type = 'kong.llm_request'
  AND toString(data.model) = {model:String}
  AND time >= {from:DateTime} AND time < {to:DateTime}
GROUP BY provider
SETTINGS optimize_move_to_prewhere = 1, allow_reorder_prewhere_conditions = 1;
