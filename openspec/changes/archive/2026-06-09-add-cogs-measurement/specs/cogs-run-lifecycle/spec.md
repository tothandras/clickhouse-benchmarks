# cogs-run-lifecycle

## ADDED Requirements

### Requirement: Phased workload-cell execution
The harness SHALL run a workload cell through the phases prepare, soak, measure, drain, collect, price, report, and SHALL only attribute and price resource consumption observed between measure start and measure end (queries, inserts) or measure start and drain end (merges).

#### Scenario: merges after ingest stops
- **WHEN** merges triggered by measure-window inserts complete during the drain phase
- **THEN** their CPU is attributed to the ingest component of the same run

#### Scenario: pre-measure activity excluded
- **WHEN** the soak phase ingests events before measure start
- **THEN** soak-window insert CPU is not attributed or priced in the measure window

### Requirement: Parts-plateau soak gate
The soak phase SHALL poll the scenario table's active part count and MAY end early once the count is stable within ±10% over 5 consecutive polls. The run SHALL record whether the plateau was reached and SHALL NOT fail on it.

#### Scenario: plateau not reached
- **WHEN** the soak phase ends without the active part count stabilizing
- **THEN** the run completes, the result records `parts_plateau: false`, and the markdown report flags it

### Requirement: Saturation flagging
The result SHALL record whether the ingest driver held its target rate. The driver SHALL NOT burst to catch up after falling behind.

#### Scenario: ingest cannot hold target rate
- **WHEN** achieved events/sec falls more than 5% below target over the measure window
- **THEN** the result records `rate_satisfied: false` and the report marks the cell saturated

### Requirement: Interrupted runs still report
A run interrupted during the measure phase SHALL still produce a result over the truncated window, flagged as truncated.

#### Scenario: SIGINT during measure
- **WHEN** the process receives SIGINT mid-measure
- **THEN** collect, price, and report run over the truncated window and the result is flagged as truncated
