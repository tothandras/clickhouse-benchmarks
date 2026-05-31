-- ROLLUP-SERVED total-period: re-aggregate the dim-keyed rollup back to per-subject.
-- (sumMerge collapses all dim combos within each (subject,window).) Aligned-boundary
-- form; arbitrary boundaries use the 3-part hybrid (see prod-llm-rollup scenario).
SELECT subject, sumMerge(tokens) AS value
FROM llm_tokens_rollup
WHERE window_start >= {from:DateTime} AND window_start < {to:DateTime}
GROUP BY subject;
