# cogs-query-mix

## ADDED Requirements

### Requirement: Per-scenario mix manifest
Each scenario participating in cogs runs SHALL provide `scenarios/<name>/queries/mix.json` defining named mixes of weighted query classes. Every `*.sql` file in the scenario's queries directory SHALL appear in exactly one class or in the mix's `exclude` list; loading SHALL fail otherwise. Class membership SHALL reference only queries that exist on disk for that scenario.

#### Scenario: unclassified query rejected
- **WHEN** a query file exists on disk but appears in no class and not in `exclude`
- **THEN** mix loading fails naming the unclassified query

#### Scenario: nonexistent query rejected
- **WHEN** a class references a query name with no matching `.sql` file in that scenario
- **THEN** mix loading fails naming the missing query

#### Scenario: diagnostic queries excluded
- **WHEN** the `exclude` list contains diagnostic variants (e.g. `*_no_prewhere`)
- **THEN** they are never selected by the replayer and do not fail validation

### Requirement: Weighted class selection
The replayer SHALL select a class per arrival proportionally to class weights and SHALL select uniformly among queries within the class. Arrivals SHALL follow the configured process (Poisson or uniform) at the configured qps, with cold arrivals (probability `cold_fraction`) issued with `enable_filesystem_cache = 0` and tagged cold.

#### Scenario: deterministic selection under seeded RNG
- **WHEN** the replayer runs with a seeded RNG against a fake client
- **THEN** class selection frequencies converge to the configured weights and cold tagging matches the configured fraction

### Requirement: Placeholder weights carry a caveat
WHILE mix weights are placeholders (not measured production frequencies), the mix manifest SHALL carry a `notes` field and reports SHALL render it in the header.

#### Scenario: caveat rendered
- **WHEN** a cogs report is generated from a mix with a `notes` field
- **THEN** the markdown header includes the note
