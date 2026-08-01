# SciMesh Status

**Updated:** 2026-08-01
**Branch baseline:** `main`; this revision adds the Workload SDK foundation.

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

The User Service is merged into `main`. It owns user accounts, authentication,
roles, and verified-contributor status; the coordinator scopes user jobs and
worker operations to the authenticated owner. Its documented v1 contract is in
[`docs/user-service-api-contract.md`](docs/user-service-api-contract.md).
Users can create and revoke worker keys for self-service Worker Agent
enrollment. Untrusted workers require quorum agreement from distinct owners on
the complete result-artifact SHA-256 before a task is accepted.

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
| CTX-10 Distributed similarity-graph | Not started | Local reference exists; the SDK-built local graph workload already enforces the pair-coverage invariant. |
| CTX-11 Dashboard/operator view | Implemented | Protected live control room: recent-run/worker overview, real pipeline-stage visualization, shard attempts and safe failures, validated similarity-search upload, coordinator artifacts, final-result download, bounded polling, and a Workload library page rendering the embedded catalog from `scimesh workload export` (`/ui/workloads`, regenerated via `make workloads-export`). |
| CTX-12 Reliability, security, CI | In progress | Unit, race, PostgreSQL integration, and smoke checks exist; CI hardening remains. |
| CTX-15 User Service and access control | Implemented | User/owner scoping, verified contributors, worker keys, self-service enrollment, and quorum-backed untrusted workers are merged; local Go/Python and Docker/PostgreSQL checks passed. |
| CTX-16 Workload SDK foundation | Implemented | `scimesh.sdk` provides strict immutable manifests/plans/artifacts, digest/trust-pinned tasks, typed DAGs, compatibility negotiation, verifier primitives with owner/binding-safe quorum inputs, resource eligibility/local allocation, measured package discovery, a trusted local core-batch conformance harness, and strict package discovery. Enforcing coordinator/Worker profiles remain fail-closed. |
| SDK roadmap step 3: `descriptor-batch` | Implemented | The first SDK-built reference workload (`scimesh/workloads/descriptors/`): pinned 81-name RDKit 2D descriptor set, canonical one-row-per-input CSV, deterministic row-bounded shards, shard-index concatenation with one header, byte-identical local/distributed output, and a two-worker `untrusted_quorum` verifier test. |
| SDK-built `similarity-search` and `similarity-graph` | Implemented | Both workloads are SDK-built packages (`scimesh/workloads/search/`, `scimesh/workloads/graph/`) built on the `MapReduceWorkload` authoring scaffold (`scimesh/sdk/batch.py`); they reuse the local scientific cores and are byte-identical to the single-process references (search; graph for both threshold directions and any block size). The graph reducer enforces the CTX-10 pair-coverage invariant. `scimesh/workloads/library.py` composes the built-in registry/runtime. |
| SDK-built `molwt-filter` | Implemented | The minimal authoring example (`scimesh/workloads/molwt_filter/`): filters molecules by exact RDKit molecular weight with only one scientific hook, using the scaffold's new default sharding and concatenation hooks. Registered in the built-in library and as a `scimesh.workloads` entry point. |
| SDK authoring scaffold | Implemented | `MapReduceWorkload` (exported from `scimesh.sdk`) assembles manifest, map/reduce stages, workflow, and digest-pinned handlers from three scientific hooks (partition/compute/merge), with overridable hooks for domain validation, plan-time resolution, custom task planning, and partial-key policy. The generic `scimesh workload list|run` CLI and the worker's allowlist-driven loading (`SCIMESH_WORKLOAD_ALLOWLIST`, `SCIMESH_CAPABILITIES`) let new workloads run without touching other code. |

## Next recommended assignment

Assign **CTX-10** to the distributed-science role: implement deterministic
block-pair planning and reduction for `similarity-graph`.

## Known constraints

- The worker/coordinator flow accepts both underscore API workload names and
  hyphenated names at the runner boundary; the runner normalizes them.
- The worker executes SDK-built workloads through `scimesh/worker/runners.py`
  (a workload-generic v1-wire bridge over `TaskSpec`/`LocalTaskContext`);
  `query_id` resolution and parameter validation live in the workload itself.
  `max_rows` is a plan-time option and is rejected per task by the stage
  projection.
- A real-stack worker test uses a small `query_smiles` shard. The Python
  planner resolves `query_id` once and shares `query_smiles`; the upload UI
  currently accepts `query_smiles` only.
- The coordinator accepts uploaded distributed jobs only for
  `similarity-search` with `query_smiles`. It rejects `similarity-graph` until
  CTX-10 supplies cross-shard pair planning.
- The SDK can execute `core-batch-v1` locally, but the protocol-v1 coordinator
  still has flat single-input/single-result tasks and no package/resource
  leases. General DAG, concurrent-Agent, GPU, stream, and gang execution needs
  a versioned coordinator/Worker rollout; unsupported features fail before
  planner invocation.
- The local SDK executor is intentionally trusted and in-process. It does not
  enforce process/network/timeout/credential isolation and rejects declarations
  that would require those guarantees.

## Update rule

The integration role updates this file only after collecting command output,
test evidence, and accepted changes. State facts, revision hashes, blockers,
and the next unblocked CTX task; do not mark work complete based on plans alone.
