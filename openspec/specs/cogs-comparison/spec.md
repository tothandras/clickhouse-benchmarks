# cogs-comparison Specification

## Purpose

Define comparison of COGS runs (unit-cost diffs with a cross-profile guard and class-set reporting) and the cpu-linear additivity validation that decides whether per-tenant COGS can be a linear formula.

## Requirements

### Requirement: Unit-cost comparison
`bench cogs compare` SHALL diff the unit costs of two cogs runs (by path or `<scenario>` shorthand resolving to the latest run under `bench/results/<scenario>/cogs/`) and SHALL refuse cross-profile price comparison unless explicitly overridden, in which case it SHALL compare resource lines only (CPU seconds, bytes/event, coverage).

#### Scenario: profile mismatch guard
- **WHEN** two runs with different pricing profiles are compared without the override flag
- **THEN** the command refuses with an explanation and suggests `--allow-profile-mismatch`

#### Scenario: cross-scenario class differences surfaced
- **WHEN** the two runs' scenarios have different query class sets (e.g. baseline has no lookup class)
- **THEN** matching classes are diffed and the class-set difference is reported explicitly instead of failing

### Requirement: Additivity validation
`bench cogs validate` SHALL check cpu-linear additivity across an ingest-only, a query-only, and a mixed run over the same scenario and rates: per component, `|mixed − (ingest + query + baseline_idle)| / mixed ≤ 0.15`. The output SHALL print PASS/FAIL with the residual and name the diverging component on FAIL.

#### Scenario: interference detected
- **WHEN** the mixed run's query CPU exceeds the additive prediction beyond tolerance
- **THEN** validate reports FAIL naming the query component and its residual
