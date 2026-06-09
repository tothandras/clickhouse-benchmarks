# COGS run: mixed-5keps-4qps on proposal

| | |
|---|---|
| run id | `01JX0000000000000000000000` |
| harness | `abc1234` |
| started | 2026-06-09T12:00:00Z |
| cluster | 25.12.1.1606 |
| pricing profile | fixture (as of ) |
| service shape | 2 replicas x 16 GiB / 4 vCPU = 4.0 compute units |
| detected capacity | 2 replicas x 4.0 vCPU (CGroupMaxCPU) |
| phases | soak 1800s / measure 3600s / drain 900s |
| coverage | 14.9% of available CPU attributed (source: os_cpu, log flush: cluster) |
| flags | none |

> **Mix caveat:** class weights are placeholders; replace with measured production frequencies

## Unit costs

| metric | billed-shape | cpu-linear |
|---|---|---|
| $ / 1M events ingested | $0.002764 (insert 75% / merge 25%) | $0.002764 |
| $ / 1k queries: key_only (warm) | $0.002073 | $0.002073 |
| $ / 1k queries: meter_agg (cold) | $0.0249 | $0.0249 |
| $ / 1k queries: meter_agg (warm) | $0.008292 | $0.008292 |
| storage $ / 1M events / month (incl. backup x1.0, estimate) | $0.001670 | |
| egress estimate (result bytes) | $0.1440 | |
| idle floor $ / service / month (100%-active bound) | $871.6200 | |

## CPU attribution

| component | cpu sec | share of available | billed | cpu-linear |
|---|---|---|---|---|
| insert | 900.0 | 3.1% | $0.0373 | $0.0373 |
| merge | 300.0 | 1.0% | $0.0124 | $0.0124 |
| query: key_only (warm) | 100.0 | 0.3% | $0.004146 | $0.004146 |
| query: meter_agg (cold) | 600.0 | 2.1% | $0.0249 | $0.0249 |
| query: meter_agg (warm) | 2400.0 | 8.3% | $0.0995 | $0.0995 |
| idle residual | 24500.0 | 85.1% | $1.0157 | |
| **window total** | 28800.0 available | 100% | $1.1940 | |

## Workload

| | target | achieved |
|---|---|---|
| ingest events/sec | 5000 | 4987 (satisfied: true, 3600 batches, 0 errors, insert p50 12.5ms p95 30.1ms) |
| query qps | 4.00 | 3.98 (queued p50 0.2ms p95 1.4ms, 0 errors) |

## Storage

| snapshot | rows | parts | partitions | compressed |
|---|---|---|---|---|
| prepare | 0 | 0 | 0 | 953.67 MiB |
| soak end | 0 | 0 | 0 | 0 B |
| drain end | 0 | 0 | 0 | 2.04 GiB |

Settled bytes/event over the run: **66.0** (18000000 events ingested).
