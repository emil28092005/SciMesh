# SciMesh Status

**Updated:** 2026-07-23  
**Branch baseline:** `planning` at `13f9a0b`

## Current state

The local Python molecular workloads are implemented and tested. They provide
the reference behaviour for future distributed execution:

- `similarity-search`: streaming ChEMBL TSV search, Morgan fingerprints,
  Tanimoto scoring, heap-based top-k, CSV and image output;
- `similarity-graph`: exact sparse graph, block-based pair comparisons,
  deterministic CSV output;
- Python Worker skeleton: claim, heartbeat, input checksum validation,
  artifact upload, completion and failure reporting.

The Go coordinator, PostgreSQL schema, coordinator artifact storage, planner,
reducer, and end-to-end distributed execution are **not implemented yet**.

## Milestone tracker

| CTX | Status | Notes |
| --- | --- | --- |
| CTX-00 API and error contract | Ready to implement | `docs/api-contract.md` created; needs owner review/freeze. |
| CTX-01 Go coordinator bootstrap | Not started | Depends on CTX-00. |
| CTX-02 PostgreSQL migrations | Not started | Depends on CTX-00 and CTX-01. |
| CTX-03 Transactional queue | Not started | Depends on CTX-02. |
| CTX-04 Worker registry and HTTP API | Not started | Depends on CTX-03. |
| CTX-05 Artifact storage | Not started | Depends on CTX-02 and CTX-04. |
| CTX-06 Python Worker live-contract alignment | Partially prepared | Worker skeleton exists; needs real Go contract tests. |
| CTX-07 Distributed workload protocol | Not started | Depends on artifact and Worker contracts. |
| CTX-08 Distributed similarity-search | Not started | Local reference exists. |
| CTX-09 Reducer and final-result API | Not started | Depends on CTX-07 and CTX-08. |
| CTX-10 Distributed similarity-graph | Not started | Local reference exists. |
| CTX-11 Dashboard/operator view | Not started | Deferred until API and reducer work. |
| CTX-12 Reliability, security, CI | Not started | Final milestone. |

## Next recommended assignment

Assign **CTX-00** to the coordinator role in `.agents/coordinator.md`: review
and freeze `docs/api-contract.md` against `PLAN.md`. Do not begin coordinator
or Worker API implementation until the contract owner accepts it.

## Known constraints

- Distributed execution is not available; use the local `scimesh` CLI.
- No Go module, PostgreSQL migrations, runtime configuration, or integration
  environment exists yet.
- Local worker unit tests do not prove interoperability with a live coordinator.

## Update rule

The integration role updates this file only after collecting command output,
test evidence, and accepted changes. State facts, revision hashes, blockers,
and the next unblocked CTX task; do not mark work complete based on plans alone.
