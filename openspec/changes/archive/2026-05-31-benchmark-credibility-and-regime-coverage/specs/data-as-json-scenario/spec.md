## MODIFIED Requirements

### Requirement: Queries adapted to JSON column access

The scenario's `queries/` directory SHALL provide one query file per file in `scenarios/baseline-openmeter/queries/`, with each query rewritten to access JSON fields. All meter queries SHALL read the path through the **untyped JSON root** via `toString(data.<path>)` (converting to an exact `Decimal128` with `toDecimal128OrNull(toString(data.<path>), 19)` for numeric aggregations), NOT the typed `data.<path>.:Float64` subcolumn accessor. This reflects the correctness fix the scenario's queries already adopted: a meter's `valueProperty` can be stored as a JSON string or a `Float64`-overflowing bigint, and the typed `.:Float64` accessor silently returns NULL for every non-`Float64`-stored value, whereas `toString(data.<path>)` renders any stored JSON type to its canonical text for exact conversion. Window functions, filters, GROUP BY columns, parameter placeholders, and ORDER BY clauses MUST otherwise be identical to the baseline counterpart so that any latency difference is attributable to the column type and access path alone.

This requirement is extended to `unique_count_hour`, which is the last query still reading `uniqExact(nullIf(toString(data.value.:Float64), 'null'))`; it SHALL use the type-agnostic `toString(data.value)` form like every other meter query. On uniform-`Float64` seeded data the two forms are equivalent (verified: `uniqExact = 49883` either way over all `api_request` rows), so on today's data this is a production-faithfulness consistency fix; once the seeder emits `value` in mixed JSON storage, the type-agnostic form is the only one that counts the string- and bigint-stored rows.

`JSON_VALUE` is unavailable on native `JSON` columns in ClickHouse 26.x (the server rejects it with `ILLEGAL_TYPE_OF_ARGUMENT`), so byte-identical reuse of baseline queries is not an option for this variant; the untyped-root `toString(data.<path>)` form is the JSON-column equivalent of the baseline's `JSON_VALUE`, type-agnostic on both sides.

#### Scenario: Query file coverage matches baseline
- **WHEN** the scenario's `queries/` directory is enumerated
- **THEN** the set of `.sql` filenames matches `scenarios/baseline-openmeter/queries/` exactly

#### Scenario: Parameter placeholders match baseline
- **WHEN** any query file in the scenario is parsed
- **THEN** it uses the same `{namespace:String}`, `{type:String}`, `{from:DateTime}`, `{to:DateTime}`, `{subjects:Array(String)}` placeholders that the baseline queries use, so the harness's default parameter set binds without modification

#### Scenario: All meter queries use the type-agnostic accessor
- **WHEN** any meter query in the scenario (including `unique_count_hour`) is parsed
- **THEN** it reads its path via `toString(data.<path>)` through the untyped JSON root, with no `data.<path>.:Float64` typed-subcolumn accessor remaining

#### Scenario: Equivalent on uniform-Float64 data, correct on mixed
- **WHEN** `unique_count_hour` runs against uniform-`Float64` seeded data
- **THEN** its `uniqExact` result is identical to the prior `.:Float64` form; **and WHEN** it runs against mixed-storage data, it counts the string- and bigint-stored `value` rows the `.:Float64` form would have read as NULL
