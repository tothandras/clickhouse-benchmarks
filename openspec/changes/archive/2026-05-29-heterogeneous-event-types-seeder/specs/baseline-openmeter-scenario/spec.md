## MODIFIED Requirements

### Requirement: Synthetic event generation

The scenario SHALL provide a seed mechanism that populates `om_events` with synthetic events. The seed MAY emit multiple event `type` values whose `data` payloads are heterogeneous — each type carrying its own JSON field-set, modeling user-controlled production data. The seed SHALL guarantee that at least one type's `data` is a JSON object containing a numeric `value` field and at least one categorical group-by field (e.g. `group1`, `group2`), and that this baseline type holds a substantial share of rows, so the canonical meter queries continue to exercise namespace/type/subject filtering and JSON extraction realistically. The seed SHALL distribute events across at least one namespace, at least two distinct `type` values, and at least 100 distinct `subject` values over a multi-day time range. Every `data` row SHALL be parseable JSON.

#### Scenario: Seed populates sufficient cardinality
- **WHEN** the seed completes
- **THEN** `om_events` contains at least 1,000,000 rows spanning ≥2 types, ≥100 subjects, and ≥3 days of `time` values, with parseable JSON in every `data` row

#### Scenario: Baseline type remains queryable under a heterogeneous mix
- **WHEN** the seed emits a heterogeneous event-type mix
- **THEN** at least one type's rows carry a numeric `value` plus `group1`/`group2`, and that type holds enough rows that the canonical `value`-aggregating meter queries still scan a large, representative population
