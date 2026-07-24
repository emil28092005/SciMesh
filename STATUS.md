# SciMesh Status

**Updated:** 2026-07-24
**Branch baseline:** `main` at `6e67daa` (distributed similarity-search)

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
uses the live coordinator contract. Completed similarity-search shard results
are reduced once into a checksum-protected final CSV, which is downloadable
through the coordinator. The full Go checks (including a fresh migration and
real PostgreSQL smoke test) passed on 2026-07-24.

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
| CTX-07 Distributed workload protocol | Implemented | Versioned Python contract models, registry, strict plan validation, and deterministic reduction ordering are in `scimesh/distributed/`. |
| CTX-08 Distributed similarity-search | Implemented | Python planner resolves `query_id` once, creates deterministic shard plans, worker adapter emits exact partial top-k CSVs/metrics, and reducer matches the local reference. |
| CTX-09 Reducer and final-result API | Implemented | Atomic `reducing` claim, deterministic coordinator-side top-k reducer, sanitized reducer failure, final artifact persistence, `result_uri`, and final CSV download. |
| CTX-10 Distributed similarity-graph | Not started | Local reference exists. |
| CTX-11 Dashboard/operator view | Implemented | Protected local view: job/task/worker status, validated similarity-search upload, partial-artifact diagnostics, final-result download, and bounded polling. |
| CTX-12 Reliability, security, CI | In progress | Unit, race, PostgreSQL integration, and smoke checks exist; CI hardening remains. |

## Next recommended assignment

Assign **CTX-10** to the distributed-science role: implement deterministic
block-pair planning and reduction for `similarity-graph`.

## Known constraints

- The worker/coordinator flow currently accepts both underscore API workload
  names and hyphenated CLI names while the contract is consolidated.
- A real-stack worker test uses a small `query_smiles` shard. The Python
  planner resolves `query_id` once and shares `query_smiles`; the upload UI
  currently accepts `query_smiles` only.
- The coordinator accepts uploaded distributed jobs only for
  `similarity-search` with `query_smiles`. It rejects `similarity-graph` until
  CTX-10 supplies cross-shard pair planning.

## Update rule

The integration role updates this file only after collecting command output,
test evidence, and accepted changes. State facts, revision hashes, blockers,
and the next unblocked CTX task; do not mark work complete based on plans alone.
