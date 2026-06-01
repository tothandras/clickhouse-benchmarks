## 1. Strip the rollups from the proposal DDL

- [x] 1.1 In `scenarios/proposal/init.sql`, remove the `proposal_llm_tokens_rollup` table and `proposal_llm_tokens_mv` materialized view (and their commented backfill INSERT).
- [x] 1.2 In `scenarios/proposal/init.sql`, remove the `proposal_api_request_rollup` table and `proposal_api_request_mv` materialized view (and their commented backfill INSERT).
- [x] 1.3 Rewrite the `init.sql` header comment so it describes only the base-table levers (data JSON + ZSTD(3) + bloom-on-id, PREWHERE note) — drop the rollup/MV section and the "known-meter rollup" prose.

## 2. Remove orphaned queries and fix surviving comments

- [x] 2.1 Delete `scenarios/proposal/queries/kong_llm_tokens_total_hybrid.sql`, `kong_api_request_total_hybrid.sql`, and `kong_status_by_route_rollup.sql`.
- [x] 2.2 Update the comments in `kong_llm_tokens_total.sql` and `kong_api_request_total.sql` to drop references to the now-deleted hybrid siblings (they are no longer "oracles" — they are the queries).
- [x] 2.3 Update the comments in `kong_api_request_by_method.sql`, `kong_api_request_by_service.sql`, and `kong_api_request_by_all_dims.sql` to drop the "dims-free / dims-bounded rollup" rationale, keeping the cardinality-ladder framing.

## 3. Delete meters.yaml

- [x] 3.1 Delete `scenarios/proposal/meters.yaml`.

## 4. Refresh docs

- [x] 4.1 Update `scenarios/README.md` to describe the proposal as the table-level design only (remove rollup / 3-part-hybrid narrative and any meters.yaml reference).
- [x] 4.2 Update the proposal-scenario prose in the top-level `README.md` to match (table-level levers only).

## 5. Verify

- [x] 5.1 Re-run the proposal scenario (or apply `init.sql` against a scratch DB) and confirm it creates only `proposal_events` with no rollup objects and no errors.
- [x] 5.2 Confirm no remaining file references the dropped objects: `grep -rn "rollup\|_mv\|meters.yaml\|hybrid" scenarios/proposal README.md scenarios/README.md` returns nothing live.
- [x] 5.3 `openspec validate remove-proposal-mv-rollups --strict` passes, then archive the change.
