-- ROLLUP-SERVED dim-filtered: same query off the dim-keyed rollup. model/provider
-- are typed columns — no JSON parse, no data blob scanned. This is why the dims
-- belong in the rollup (the win survives even at ~1× row compression).
SELECT provider, sumMerge(tokens) AS value
FROM llm_tokens_rollup
WHERE model = {model:String}
  AND window_start >= {from:DateTime} AND window_start < {to:DateTime}
GROUP BY provider;
