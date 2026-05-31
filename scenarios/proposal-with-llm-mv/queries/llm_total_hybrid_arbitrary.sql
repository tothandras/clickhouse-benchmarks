-- ROLLUP-SERVED total, ARBITRARY (non-hour-aligned) from/to — billing-exact.
--
-- A coarse rollup bucket straddling from/to would over/under-count, so the
-- range is split into three disjoint, complete pieces and only WHOLE buckets
-- fully inside [from,to) come from the rollup; the partial edges come from raw
-- events:
--   [from, from_ceil)     raw head  (partial first bucket)
--   [from_ceil, to_floor) rollup    (whole buckets)
--   [to_floor, to)        raw tail  (partial last bucket)
-- from_ceil/to_floor are computed INLINE from {from}/{to} (no precomputed param).
-- Interior-empty guard: when no whole bucket fits (from_ceil >= to_floor) it
-- falls back to a single raw read of [from,to). ifNull(...,0) so an empty
-- slice contributes 0, not NULL. Verified exact vs base across edge cases
-- (mid-hour, on-boundary, same-hour, spans-one-boundary).
SELECT
  sum(value) AS value
FROM
(
  -- raw head: [from, from_ceil)
  SELECT ifNull(sum(toDecimal128OrNull(toString(data.tokens), 19)), 0) AS value
  FROM proposal_with_llm_mv_events
  WHERE type = 'llm_request'
    AND time >= toDateTime({from:DateTime})
    AND time < if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR)
    AND if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  -- rollup interior: whole hours [from_ceil, to_floor)
  SELECT ifNull(sumMerge(tokens), 0) AS value
  FROM llm_tokens_rollup
  WHERE window_start >= if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR)
    AND window_start < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  -- raw tail: [to_floor, to)
  SELECT ifNull(sum(toDecimal128OrNull(toString(data.tokens), 19)), 0) AS value
  FROM proposal_with_llm_mv_events
  WHERE type = 'llm_request'
    AND time >= toStartOfHour(toDateTime({to:DateTime}))
    AND time < toDateTime({to:DateTime})
    AND if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  -- interior-empty fallback: no whole bucket between the boundaries -> single raw read
  SELECT ifNull(sum(toDecimal128OrNull(toString(data.tokens), 19)), 0) AS value
  FROM proposal_with_llm_mv_events
  WHERE type = 'llm_request'
    AND time >= toDateTime({from:DateTime}) AND time < toDateTime({to:DateTime})
    AND NOT (if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime})))
);
