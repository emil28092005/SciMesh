# SciMesh Status

**Updated:** 2026-07-24
**Branch baseline:** `main` at `f953112` (distributed pipeline hardening)

## Current state

The local Python molecular workloads are implemented and tested. They provide
the reference behaviour for future distributed execution:

- `similarity-search`: streaming ChEMBL TSV search, Morgan fingerprints,
  Tanimoto scoring, heap-based top-k, CSV and image output;
- `similarity-graph`: exact sparse graph, block-based pair comparisons,
  deterministic CSV output;
- Python Worker skeleton: claim, heartbeat, input checksum validation,
  artifact upload, completion and failure reporting.

The Go coordinator and its PostgreSQL-backed task lifecycle are implemented:
registration, atomic claiming, lease renewal, artifact storage, dataset
chunking, result/failure reporting, and job progress. The Python worker now
uses the live coordinator contract; its HTTP path was exercised against a real
Docker PostgreSQL stack on 2026-07-23.

## Milestone tracker

| CTX | Status | Notes |
| --- | --- | --- |
| CTX-00 API and error contract | Implemented | Contract, OpenAPI, and request examples are in `docs/`. |
| CTX-01 Go coordinator bootstrap | Implemented | Go service and Docker runtime in `coordinator/`. |
| CTX-02 PostgreSQL migrations | Implemented | Applied by the Compose migration service. |
| CTX-03 Transactional queue | Implemented | Real-PostgreSQL integration tests cover atomic claims and concurrency. |
| CTX-04 Worker registry and HTTP API | Implemented | Registration, claim, heartbeat, result, failure, and status endpoints. |
| CTX-05 Artifact storage | Implemented | Coordinator-owned inputs/results, checksum verification, and upload flow. |
| CTX-06 Python Worker live-contract alignment | Implemented | Worker completed a real uploaded shard via HTTP on 2026-07-23. |
| CTX-07 Distributed workload protocol | Implemented | Versioned Python contract models, registry, strict plan validation, and deterministic reduction ordering are in `scimesh/distributed/`. The concrete molecular planner/reducer remains CTX-08/09. |
| CTX-08 Distributed similarity-search | Not started | Local reference exists. |
| CTX-09 Reducer and final-result API | Not started | Depends on CTX-07 and CTX-08. |
| CTX-10 Distributed similarity-graph | Not started | Local reference exists. |
| CTX-11 Dashboard/operator view | Implemented (diagnostic scope) | Protected local view: job/task/worker status, validated similarity-search upload, diagnostic partial-artifact download, and bounded polling. Final-result reduction remains CTX-09. |
| CTX-12 Reliability, security, CI | In progress | Unit, race, PostgreSQL integration, and smoke checks exist; CI hardening remains. |

## Next recommended assignment

Assign **CTX-08** to the workload role: implement the molecular
`similarity-search` planner and worker adapter on top of the accepted CTX-07
contract.

## Known constraints

- The CTX-07 protocol is implemented, but no concrete molecular planner or
  reducer is registered yet; the operator UI labels `partial_result` files as
  diagnostic and cannot present them as final output.
  Use the local `scimesh` CLI for complete workload results.
- The worker/coordinator flow currently accepts both underscore API workload
  names and hyphenated CLI names while the contract is consolidated.
- A real-stack worker test uses a small `query_smiles` shard. Resolving a
  `query_id` once and sharing it across shards belongs to CTX-07.
- The coordinator accepts uploaded distributed jobs only for
  `similarity-search` with `query_smiles`. It rejects `similarity-graph` until
  CTX-10 supplies cross-shard pair planning.

## Update rule

The integration role updates this file only after collecting command output,
test evidence, and accepted changes. State facts, revision hashes, blockers,
and the next unblocked CTX task; do not mark work complete based on plans alone.
