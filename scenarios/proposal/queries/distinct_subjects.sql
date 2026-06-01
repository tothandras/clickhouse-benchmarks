SELECT DISTINCT proposal_events.subject AS subject
FROM proposal_events
WHERE proposal_events.namespace = {namespace:String}
  AND proposal_events.type = {type:String}
  AND proposal_events.time >= {from:DateTime}
  AND proposal_events.time < {to:DateTime}
ORDER BY subject;
