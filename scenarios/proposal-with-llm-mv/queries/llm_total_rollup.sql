-- ROLLUP-SERVED: same total-period meter answered from the MV-maintained rollup.
-- (Aligned-boundary form: when from/to fall on hour boundaries the whole range is
--  interior, so it's a pure sumMerge — the hybrid head/tail collapse to empty.)
SELECT subject, sumMerge(tokens) AS value
FROM llm_tokens_rollup
WHERE window_start >= {from:DateTime} AND window_start < {to:DateTime}
GROUP BY subject;
