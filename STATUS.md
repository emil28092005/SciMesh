# SciMesh Status

**Updated:** 2026-08-03
**Branch baseline:** `main`; this revision adds the admin console, the worker
setup wizard, and release-shipped Python wheels.

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
chunking, result/failure reporting, and job progress. The Go worker agent now
uses the live coordinator contract. Completed shard results are reduced once
into a checksum-protected final CSV, downloadable through the coordinator.

**Single-binary platform (`coordinator serve`)**: the coordinator now ships
an embedded SQLite storage backend (`SCIMESH_DB=sqlite`), an embedded
userservice, and `serve`/`agent` subcommands, so one downloaded binary runs
the whole platform — coordinator, both databases, UI logins, and local
workers — with no PostgreSQL, no Docker, and no environment variables. The
first start provisions `~/.scimesh` (secrets, admin password printed once,
managed scientific-runtime venv) and opens the UI. `install.sh` / `install.ps1`
download the right release binary in one command and are release assets. The
PostgreSQL engine, the `setup` wizard, and the standalone `users/` service
remain fully supported for cluster deployments. The full E2E passes with zero
external services: health, UI login, job upload, local agent compute,
reduction, and a byte-exact final CSV.

The User Service is merged into `main`. It owns user accounts, authentication,
roles, and verified-contributor status; the coordinator scopes user jobs and
worker operations to the authenticated owner. Its documented v1 contract is in
[`docs/user-service-api-contract.md`](docs/user-service-api-contract.md).
Users can create and revoke worker keys for self-service Worker Agent
enrollment. Untrusted workers require quorum agreement from distinct owners on
the complete result-artifact SHA-256 before a task is accepted.

**Admin console** (`/ui/admin`, admin role only): system/storage/health,
paginated jobs with owner resolution, worker fleet with trust controls and
offline-worker removal, users and worker keys, workload enable/disable
(persisted in both engines, enforced at submit time), 7-day metrics, and
settings with an audited worker-token reveal and artifact pruning for old
finished jobs.

**Worker setup wizard** (`worker-agent setup`, loopback `127.0.0.1`): browser
wizard that configures the coordinator URL and credential, runs a preflight
(coordinator reachability, real credential probe, python, scimesh), installs
the version-locked `scimesh` wheel from the release into a managed venv
(`~/.scimesh-worker/venv`), pins the venv task runner, and starts/stops the
worker with live log and parsed claim/completed/failed counters. The release
pipeline now also ships the Python wheel (`scimesh-<ver>-py3-none-any.whl`,
version derived from the git tag via setuptools_scm), and both `coordinator
serve` and the wizard install it automatically.

The full pipeline was verified end-to-end in containers on release artifacts:
installer → serve → wizard → registration → real similarity-search job →
byte-exact result vs the local reference.

## Milestone tracker

| CTX | Status | Notes |
| --- | --- | --- |
| CTX-00 API and error contract | Implemented | Contract, OpenAPI, and request examples are in `docs/`. |
| CTX-01 Go coordinator bootstrap | Implemented | Go service and Docker runtime in `coordinator/`. |
| CTX-02 PostgreSQL migrations | Implemented | Embedded into the binary (`AUTO_MIGRATE`); the CLI path is still available for managed databases. |
| CTX-03 Transactional queue | Implemented | Real-PostgreSQL integration tests cover atomic claims and concurrency; the SQLite backend mirrors the semantics. |
| CTX-04 Worker registry and HTTP API | Implemented | Registration, claim, heartbeat, result, failure, and status endpoints. |
| CTX-05 Artifact storage | Implemented | Coordinator-owned inputs/results, checksum verification, and upload flow. |
| CTX-06 Python Worker live-contract alignment | Superseded | The Python worker daemon was removed; the Go worker agent (`coordinator/internal/agent/` + `cmd/worker-agent`, or `coordinator agent`) implements the lifecycle and executes SDK workloads via `scimesh/worker/task.py`. |
| CTX-07 Distributed workload protocol | Implemented | Versioned Python contract models, registry, strict plan validation, and deterministic reduction ordering. |
| CTX-08 Distributed similarity-search | Implemented | Planner/worker/reducer match the local reference byte-exactly. |
| CTX-09 Reducer and final-result API | Implemented | Atomic `reducing` claim, deterministic coordinator-side reducers (`top-k` and `ordered-concat`), sanitized failure, final artifact, `result_uri`. |
| CTX-10 Distributed similarity-graph | Not started | Local reference exists; the SDK-built local graph workload enforces the pair-coverage invariant. |
| CTX-11 Dashboard/operator view | Implemented | The demo control room was removed; `/ui` lands on the admin console. Job form/detail, workload library, add-machine and profile pages share the admin design system; MkDocs at `/ui/docs/`. |
| CTX-19 Coordinator Admin UI | Implemented | `/ui/admin` console (see above), bounded read models, admin-only routes, sqlite+postgres parity with integration tests. |
| CTX-20 Worker Setup UI | Implemented | `worker-agent setup` wizard with runtime installer, `--config`, `--check` (credential + venv probing), live status. |
| CTX-12 Reliability, security, CI | In progress | vet, gofmt, race tests, golangci-lint (0 issues), PostgreSQL integration, and smoke checks exist. |
| CTX-15 User Service and access control | Implemented | User/owner scoping, verified contributors, worker keys, quorum; also embeddable (`coordinator serve`). |
| CTX-16 Workload SDK foundation | Implemented | Strict immutable manifests/plans/artifacts, digest/trust-pinned tasks, negotiation, verifier primitives, conformance harness. |
| CTX-17 Self-provisioning + setup wizard | Implemented | Embedded migrations, `coordinator setup`, SQLite backend, embedded userservice, `serve` mode. |
| CTX-18 Single-binary platform | Implemented | `coordinator serve` (data dir, secrets, admin bootstrap, local agents, managed venv) + `install.sh`/`install.ps1`; full no-external-service E2E green. |
| SDK roadmap step 3: `descriptor-batch` | Implemented | Byte-identical local/distributed output, quorum verifier test. |
| SDK-built `similarity-search` and `similarity-graph` | Implemented | SDK-built packages, byte-identical to single-process references. |
| SDK-built `molwt-filter` | Implemented | Minimal authoring example; also the single-binary E2E workload. |
| SDK authoring scaffold | Implemented | `MapReduceWorkload` with `UIElement` declarations, `reduction`, `upload_ready`; generic `scimesh workload list|run|export|allowlist` CLI. |

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
