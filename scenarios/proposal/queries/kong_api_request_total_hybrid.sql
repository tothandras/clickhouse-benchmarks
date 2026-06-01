-- kong_konnect_api_request (COUNT) total-period, billing-exact for ARBITRARY
-- from/to: raw head count + rollup interior (countMerge) + raw tail count.
-- count is exact integer — no decimal cast. {from}/{to} wrapped in toDateTime().
-- ifNull(...,0) per slice; 4th branch is the interior-empty fallback.
SELECT sum(value) AS value
FROM
(
  SELECT ifNull(count(), 0) AS value
  FROM proposal_events
  WHERE type = 'kong.api_request'
    AND time >= toDateTime({from:DateTime})
    AND time < if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR)
    AND if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  SELECT ifNull(countMerge(value), 0) AS value
  FROM proposal_api_request_rollup
  WHERE window_start >= if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR)
    AND window_start < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  SELECT ifNull(count(), 0) AS value
  FROM proposal_events
  WHERE type = 'kong.api_request'
    AND time >= toStartOfHour(toDateTime({to:DateTime}))
    AND time < toDateTime({to:DateTime})
    AND if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime}))
  UNION ALL
  SELECT ifNull(count(), 0) AS value
  FROM proposal_events
  WHERE type = 'kong.api_request'
    AND time >= toDateTime({from:DateTime}) AND time < toDateTime({to:DateTime})
    AND NOT (if(toDateTime({from:DateTime}) = toStartOfHour(toDateTime({from:DateTime})), toDateTime({from:DateTime}), toStartOfHour(toDateTime({from:DateTime})) + INTERVAL 1 HOUR) < toStartOfHour(toDateTime({to:DateTime})))
);
