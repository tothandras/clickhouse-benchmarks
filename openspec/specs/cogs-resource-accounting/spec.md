# cogs-resource-accounting Specification

## Purpose

Define complete, multi-replica-correct attribution of CPU and storage consumption to {insert, merge, query, idle} from ClickHouse system tables, with structured statement tagging and an explicit coverage metric, so no component of the workload's cost is invisible (merges) or silently lost to load-balanced replicas.

## Requirements

### Requirement: Complete compute attribution
The harness SHALL attribute CPU seconds to insert, merge, and query components, sourcing merges from `system.part_log` (event type `MergeParts`), and SHALL report attribution coverage against available CPU seconds. Every statement issued by the harness during a run SHALL carry a structured `log_comment` containing the run id, component, and (for queries) class and cache state.

#### Scenario: tagged workload attribution
- **WHEN** a cell runs tagged inserts and queries against a ClickHouse service
- **THEN** the collected accounting shows nonzero insert and query CPU grouped by component, class, and cache state, and coverage in (0, 1]

#### Scenario: failed statements not priced
- **WHEN** a tagged statement finishes as `ExceptionBeforeStart` or `ExceptionWhileProcessing`
- **THEN** it is counted in the result's errors block and excluded from priced CPU

### Requirement: Multi-replica log collection
Log collectors SHALL read `system.query_log` and `system.part_log` across all replicas (via `clusterAllReplicas`) and SHALL flush logs on all replicas before collecting. Available CPU SHALL be computed as detected replica count times per-replica vCPUs times the measure window.

#### Scenario: merge scheduled on a non-connected replica
- **WHEN** a merge of the scenario table executes on a replica other than the one the harness is connected to
- **THEN** its CPU is included in the merge attribution

#### Scenario: cluster-wide flush unavailable
- **WHEN** `SYSTEM FLUSH LOGS ON CLUSTER` is not permitted on the target
- **THEN** the collector falls back to a local flush plus settle delay and the result records `log_flush: "local-only"`

### Requirement: Merge CPU fallback
WHEN `system.part_log` lacks ProfileEvents on the connected version, the collector SHALL estimate merge CPU from merge wall time and concurrency and SHALL set `merge_cpu_estimated: true` in the result.

#### Scenario: part_log without ProfileEvents
- **WHEN** the ProfileEvents column is absent or empty in `part_log`
- **THEN** merge CPU is estimated and the result and report carry the `merge_cpu_estimated` flag

### Requirement: Async-insert attribution
WHEN a cell enables `async_insert`, the collector SHALL attribute flush-query CPU to the insert component by correlating `system.asynchronous_insert_log` with `system.query_log`, and SHALL set `async_attribution_partial: true` when correlation is incomplete.

#### Scenario: async flush CPU booked to insert
- **WHEN** an async-insert cell's buffered rows are written by background flush queries
- **THEN** the flush CPU appears under the insert component, not under idle

### Requirement: Storage accounting
The harness SHALL record active-part rows, compressed bytes, uncompressed bytes, and part count for the scenario table at prepare, soak end, and drain end, and SHALL derive settled bytes/event from the prepare-to-drain-end compressed delta over events ingested.

#### Scenario: settled bytes per event
- **WHEN** an ingest cell completes its drain phase
- **THEN** the result contains `bytes_per_event_settled` computed from the compressed-bytes delta
