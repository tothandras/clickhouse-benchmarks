## ADDED Requirements

### Requirement: Concurrency sweep

The harness SHALL accept more than one concurrency level in a single invocation (e.g. `--concurrency 1,8,16`) and measure every query at each level, recording each level as its own result entry tagged with its concurrency. This makes the "p99 under concurrency roughly halves" class of claim — currently asserted in prose for the bloom-on-`id` index — a recorded, per-scenario number. A single level SHALL remain the default so existing one-level runs are unchanged.

#### Scenario: Each concurrency level is measured and recorded
- **WHEN** the harness is given multiple concurrency levels
- **THEN** every query is measured once per level and each measurement's result entry records the concurrency it was run at, so latency/CPU can be read as a function of concurrency

#### Scenario: Single level remains the default
- **WHEN** no concurrency list is given
- **THEN** the harness measures at concurrency 1 exactly as before, producing result files identical in shape to prior runs
