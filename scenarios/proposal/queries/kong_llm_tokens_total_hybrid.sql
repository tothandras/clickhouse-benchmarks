-- kong_konnect_llm_tokens (SUM $.tokens) total-period, billing-exact for
-- ARBITRARY from/to: raw head + rollup interior (whole hours) + raw tail.
-- Rollup tokens are UInt64; cast to toDecimal128(...,19) at query time to match
-- the base table's billing-decimal aggregation. {from}/{to} wrapped in
-- toDateTime() (harness substitutes bare string literals). ifNull(...,0) per
-- slice; 4th branch is the interior-empty fallback (single raw read).
SELECT sum(value) AS value
FROM
(
  SELECT ifNull(sum(toDecimal128OrNull(toString(data.tokens), 19)), 0) AS value
  FROM proposal_events
  WHERE type = 'kong.llm_request'
    AND time >= toDateTime({from:DateTime})
    AND time < if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR)
    AND if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  SELECT ifNull(toDecimal128(sumMerge(value), 19), 0) AS value
  FROM proposal_llm_tokens_rollup
  WHERE window_start >= if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR)
    AND window_start < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  SELECT ifNull(sum(toDecimal128OrNull(toString(data.tokens), 19)), 0) AS value
  FROM proposal_events
  WHERE type = 'kong.llm_request'
    AND time >= toStartOfHour(toDateTime({to:DateTime}))
    AND time < toDateTime({to:DateTime})
    AND if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  SELECT ifNull(sum(toDecimal128OrNull(toString(data.tokens), 19)), 0) AS value
  FROM proposal_events
  WHERE type = 'kong.llm_request'
    AND time >= toDateTime({from:DateTime}) AND time < toDateTime({to:DateTime})
    AND NOT (if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime})))
);
