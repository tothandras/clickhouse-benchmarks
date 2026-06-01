## REMOVED Requirements

### Requirement: Known-meter materialized-view rollups

**Reason**: The per-meter rollups did not earn their keep — the `kong.api_request`
rollup was already a documented ≈1.0× (no-compression) negative result, and
maintaining per-meter materialized views contradicts the project's standing
constraint that the events table stay meter-agnostic for the unbounded set of
unknown-schema meters.

**Migration**: Query the two Kong meters directly from the base
`proposal_events` table. The total/grouped Kong queries that already read the
base table (`kong_llm_tokens_total`, `kong_api_request_total`,
`kong_api_request_by_method`, `kong_api_request_by_service`,
`kong_api_request_by_all_dims`, `kong_status_by_route`) are unchanged and serve
these meters without a rollup. The dropped MV DDL and rollup tables remain
recoverable from git history if ever revisited.

### Requirement: Billing-exact hybrid reads for arbitrary windows

**Reason**: The 3-part hybrid reads existed only to serve total-period queries
from the rollups over arbitrary, non-hour-aligned windows. With the rollups
removed, there is no rollup interior to splice and the hybrid machinery has
nothing to accelerate.

**Migration**: Read totals directly from the base table over `[from, to)` (the
`kong_llm_tokens_total` / `kong_api_request_total` base-table queries), which are
exact for arbitrary windows without any head/interior/tail splicing.

### Requirement: Canonical meter definitions as the rollups' source of truth

**Reason**: `scenarios/proposal/meters.yaml` existed specifically to be the
source of truth the materialized-view rollups implemented. With the rollups
removed, the file documents no object the surviving queries depend on.

**Migration**: None required. The surviving base-table Kong queries encode their
own aggregation and groupBy directly in SQL; `meters.yaml` is deleted.
