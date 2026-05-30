## ADDED Requirements

### Requirement: Mixed JSON storage for the value path

The seeder SHALL support emitting the canonical `$.value` path in mixed JSON storage under a single path — a deterministic split of JSON number, JSON-stringified number (`"123.4"`), and a bigint that overflows `Float64` — so the harness actually exercises the README's headline correctness fix on the dominant meter path. Today `value` is a uniform JSON `Float64` (`buildBaseline` emits `rng.Float64() * 1000`), which makes `data.value.:Float64` and `toDecimal128OrNull(toString(data.value))` provably identical on the seeded data, so the type-agnostic fix is never tested on the path that carries 50% of rows. Mixed storage SHALL be gated behind config, defaulting to the current uniform distribution so committed result files stay comparable until intentionally re-run.

#### Scenario: Value emitted in three storage forms
- **WHEN** the seeder runs with mixed value-storage enabled
- **THEN** rows carry `value` as JSON number, JSON-stringified number, and Float64-overflowing bigint in a deterministic split, such that `toDecimal128OrNull(toString(data.value))` reads every form while `data.value.:Float64` reads NULL on the string and bigint rows

#### Scenario: Uniform distribution remains the default
- **WHEN** mixed value-storage is not requested
- **THEN** the seeder emits `value` as a uniform JSON Float64 exactly as before, so existing committed results remain reproducible

### Requirement: Multi-namespace seeding

The seeder SHALL support assigning rows across a configurable number of namespaces (default 1 — current behavior), deterministically from the RNG seed and row index. Real OpenMeter is multi-tenant via `namespace`. Because `namespace` is the first ORDER BY key, the primary sparse index prunes to one tenant's contiguous range before the `bloom_filter` on `id` runs; the current single-namespace bench (whole table = one namespace) is therefore plausibly the bloom's best case and may overstate its production benefit. Measuring across multiple namespaces isolates the bloom's marginal benefit on top of the namespace-prefix pruning, which a single-namespace table cannot.

#### Scenario: Rows distributed deterministically across namespaces
- **WHEN** the seeder runs with a namespace count greater than 1
- **THEN** rows are assigned namespaces deterministically from `(seed, index)`, the distribution matches the requested count, and reruns with the same seed produce identical assignments

#### Scenario: Single namespace remains the default
- **WHEN** no namespace count is configured
- **THEN** the seeder writes a single namespace exactly as before, and the default query parameter binding continues to target it
