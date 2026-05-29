## ADDED Requirements

### Requirement: Scenario directory layout

A scenario SHALL be a single directory under `scenarios/` whose name is the scenario identifier (kebab-case). Every scenario directory SHALL contain at minimum an `init.sql` file and a `queries/` subdirectory. A `seed.sql` file or `seed/` subdirectory MAY be present; if absent, the scenario is assumed to load data via an external mechanism (the benchmark driver or a pre-populated table).

#### Scenario: Discoverable scenario
- **WHEN** the benchmark driver scans `scenarios/`
- **THEN** every direct subdirectory containing `init.sql` is treated as a runnable scenario

#### Scenario: Scenario missing init.sql
- **WHEN** a directory under `scenarios/` does not contain `init.sql`
- **THEN** the driver SHALL skip it and log a warning (it is treated as documentation or work-in-progress, not a runnable scenario)

### Requirement: init.sql contract

`init.sql` SHALL contain ClickHouse DDL that creates all tables, materialized views, projections, and dictionaries the scenario requires, against a database the driver provides via `{{database}}` template substitution. It MUST be idempotent (use `CREATE TABLE IF NOT EXISTS` / `CREATE MATERIALIZED VIEW IF NOT EXISTS`) so re-running a scenario does not error.

#### Scenario: Re-running init.sql
- **WHEN** the driver applies `init.sql` against a database that already has the scenario's objects
- **THEN** every statement SHALL succeed without error and the existing objects SHALL be unchanged

### Requirement: queries directory contract

`queries/` SHALL contain one `.sql` file per benchmark query. Each file's basename (without extension) is the query identifier reported in results. Each file SHALL contain exactly one SELECT statement and MAY use parameter placeholders in ClickHouse's `{name:Type}` syntax which the driver binds at runtime.

#### Scenario: Single query per file
- **WHEN** the driver loads a file from `queries/`
- **THEN** it parses exactly one SELECT statement; multiple statements in one file SHALL produce an error and skip that query

#### Scenario: Parameter binding
- **WHEN** a query contains a `{namespace:String}` placeholder
- **THEN** the driver binds it from its parameter source (env var, fixed default, or scenario manifest) before execution
