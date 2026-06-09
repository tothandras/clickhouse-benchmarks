# cogs-pricing Specification

## Purpose

Define pricing of attributed resource consumption from named, checked-in profiles in two modes (billed-shape, which reconciles with the ClickHouse Cloud invoice and books an idle floor; cpu-linear, the marginal what-if), the unit-cost card every run produces, and the service-shape cross-check that keeps costs from being priced on the wrong shape.

## Requirements

### Requirement: Pricing from named profiles
Every priced value SHALL be derived from a pricing profile checked into `pricing/`, and the full profile SHALL be embedded in the result JSON. Rates and service shape SHALL live in profile data, never in code. Reports SHALL show both billed-shape and cpu-linear costs.

#### Scenario: zero-rate profile decouples accounting from pricing
- **WHEN** a run uses the `local-zero` profile
- **THEN** all USD values are 0 and all resource-accounting fields are still fully populated

### Requirement: Billed-shape and cpu-linear modes
Billed-shape cost SHALL price the measure window as `compute_units × window_hours × rate`, split proportionally by attributed CPU seconds across insert, merge, and query, with the remainder booked as idle floor. Cpu-linear cost SHALL price attributed CPU seconds directly at `rate / (vcpus_per_unit × 3600)`.

#### Scenario: attribution sums to window cost
- **WHEN** billed-shape costs are computed for a run
- **THEN** insert + merge + query + idle_floor equals the window cost within rounding

### Requirement: Unit-cost card
A cogs result SHALL include: $/1M events ingested with insert and merge shares; $/1k queries per query class split warm/cold; settled bytes/event with the implied $/1M-events-month storage cost (including the configured backup multiplier, marked estimate); an egress estimate derived from result bytes (marked estimate; an uncompressed upper bound — Cloud bills compressed transfer); and the idle-floor $/service-month bound shown next to the measured idle share.

#### Scenario: complete card
- **WHEN** a mixed cell completes
- **THEN** the result's unit block contains all card entries with estimates explicitly marked

### Requirement: Service-shape cross-check
The runner SHALL compare the detected service shape (replicas, per-replica vCPUs) against the pricing profile's declared shape and SHALL record `shape_mismatch: true` and warn when they differ.

#### Scenario: profile drift detected
- **WHEN** the connected service has a different replica count than the profile declares
- **THEN** the run completes, warns on stderr, and the result and report carry the mismatch flag
