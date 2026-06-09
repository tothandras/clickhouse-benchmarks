# cogs-reconciliation Specification

## Purpose

Define input-file-based reconciliation of modeled consumption against ClickHouse Cloud billing exports (versioned parsers, never a Cloud API dependency), with delta flagging and granularity caveats.

## Requirements

### Requirement: Reconciliation is input-driven
Reconciliation against ClickHouse Cloud billing SHALL consume an exported usage file via `--usage-export` (or `cogs reconcile <run.json> <usage.json>`) and SHALL NOT require Cloud API access. The parser SHALL be versioned so export-schema drift fails loudly rather than misparsing.

#### Scenario: unsupported export version
- **WHEN** the usage file does not match a known parser version
- **THEN** reconciliation fails with a versioned-parser error and the run result is otherwise unaffected

### Requirement: Model-vs-billed delta flagging
Reconciliation SHALL select usage rows overlapping the measure window and emit billed vs model compute-unit-hours and storage TB with percentage deltas. A delta above 20% in magnitude SHALL flag the run.

#### Scenario: large delta flagged
- **WHEN** billed compute-unit-hours differ from the model by more than 20%
- **THEN** the reconciliation block flags the run and the report references the documented delta sources
