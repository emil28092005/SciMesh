# SciMesh: master implementation plan

> **Purpose.** This document is the source plan for turning SciMesh from a
> local molecular CLI into a local-first distributed scientific-computation
> platform. It is intentionally detailed enough to split into independent task
> briefs for developers or coding agents.
>
> **Planning baseline.** This branch starts from `Workers`: the Python package
> has local `similarity-search` and `similarity-graph` workloads plus a Worker
> Daemon client. The coordinator and PostgreSQL implementation do not yet
> exist. The Worker contract and the Go/PostgreSQL design briefs in `docs/` are
> part of this plan.

---

## 1. Product goal

SciMesh accepts a scientific run, turns it into independent tasks, dispatches
them to polling workers, persists task state and artifacts, combines partial
results, and exposes the final result and progress to a user.

The first production-oriented vertical slice is molecular computation:

- `similarity-search`: exact top-k Tanimoto search over ChEMBL shards;
- `similarity-graph`: exact sparse Tanimoto graph, where each pair is compared
  once and only edges satisfying the chosen threshold are retained.

The platform must later support other scientific workloads without changing the
coordinator or worker state machine.

```text
User / simple UI / CLI
        |
        v
Go coordinator + PostgreSQL + coordinator artifact storage
        |
        +-- creates Job/Run -> Tasks -> leases one task at a time
        |
        v
Python Worker Daemons (outbound HTTP only)
        |
        +-- download artifact -> execute allowlisted workload -> upload partial result
        |
        v
Coordinator reducer -> final artifact -> download/status API
```

---

## 2. Scope, non-goals, and decisions

### 2.1 In scope

- Go 1.22+ coordinator service with PostgreSQL 15+;
- Python Worker Daemon running existing SciMesh workloads locally;
- durable job, task, worker, and artifact metadata;
- local coordinator-managed artifact storage for the first deployment;
- HTTP API for submit, poll/claim, heartbeat, artifact transfer, completion,
  failure, job status, and result download;
- sharding and reduction for the two molecular workloads;
- a small server-rendered or static HTML status page after the API works;
- automated unit, integration, and contract tests;
- a reproducible local demo using one coordinator and two or more workers.

### 2.2 Explicit non-goals for the first release

- cloud object storage, Kubernetes, autoscaling, and multi-region operation;
- arbitrary shell commands sent by coordinator to workers;
- user accounts, multi-tenancy, billing, or sophisticated authorization;
- GPU scheduling and multiprocessing inside a worker;
- Docker as a required runtime dependency;
- video/CV processing implementation;
- a React/Vue frontend;
- exact resumability of a subprocess after host power loss.

### 2.3 Architectural decisions already made

| Decision | Choice | Rationale |
| --- | --- | --- |
| Coordinator | Go + `net/http` | One durable service for API, queue, artifacts, and reducer orchestration. |
| Database | PostgreSQL + `pgxpool` | Transactional leasing and concurrent `SKIP LOCKED` claims. |
| Migration tool | `golang-migrate` SQL migrations | Schema is reviewable independently of Go code. |
| Workers | Python | Reuses RDKit and the existing SciMesh workload code. |
| Worker connectivity | Outbound HTTP polling | Workers require no public inbound ports. |
| Queue model | Database rows, not a separate broker | Sufficient for the initial local-first deployment. |
| Artifact storage | Coordinator filesystem first | Durable and simple; can later be replaced by S3-compatible storage behind an interface. |
| Workload execution | Explicit allowlist + typed parameters | Never execute coordinator-provided shell commands. |
| Result correctness | Artifact upload before task completion | A completed task must reference a durable, coordinator-accessible result. |

### 2.4 Rules that must never be violated

1. Workers never use PostgreSQL credentials or execute SQL.
2. A task is leased atomically to at most one worker attempt.
3. A task becomes `completed` only after its result artifact is durable and
   verified by the coordinator.
4. Every mutating worker request includes `worker_id` and `attempt`; stale
   attempts receive `409 Conflict`.
5. Worker tokens are never forwarded to an external presigned download URL.
6. The worker runs an explicit Python command list, never `shell=True`.
7. Result and reducer outputs are deterministic for identical inputs and
   parameters.
8. Similarity graph tasks must cover each original molecule pair exactly once.
9. No workload may create or retain a dense N×N similarity matrix.
10. The coordinator must not trust worker-supplied artifact paths, status, or
    ownership claims without checking task state in PostgreSQL.

---

## 3. Glossary and canonical lifecycle

| Term | Meaning |
| --- | --- |
| **Worker** | A registered process/machine capable of claiming tasks. |
| **Job** | A user-requested full computation. The UI may call it a **Run**; the database/API use `job`. |
| **Task** | One independently executable unit of a job. |
| **Attempt** | A monotonically increasing execution lease for a task. |
| **Lease** | Temporary exclusive assignment of a task to one worker. |
| **Artifact** | A durable input, shard, partial result, final result, or log file. |
| **Planner** | Workload code that validates a job and emits task payloads. |
| **Runner** | Worker-side code that executes one typed task locally. |
| **Reducer** | Coordinator-side code that merges all completed partial results into a final artifact. |

### 3.1 Job state machine

```text
CREATED -> PLANNING -> RUNNING -> REDUCING -> COMPLETED
                 |         |          |
                 +-------> FAILED <---+
RUNNING -> CANCELLED
```

### 3.2 Task state machine

```text
PENDING -> LEASED -> RUNNING -> COMPLETED
              |        |          |
              |        +--------> FAILED
              +-----> PENDING       |
                   lease expiry      +-> PENDING (attempts remain)
```

`LEASED` means the coordinator has returned a task. `RUNNING` means the worker
has successfully sent its first heartbeat/start acknowledgement. A lease expiry
may return either `LEASED` or `RUNNING` tasks to `PENDING` when attempts remain.

### 3.3 Artifact lifecycle

```text
upload input -> INPUT artifact -> planner creates SHARD artifacts
worker downloads SHARD -> executes -> uploads PARTIAL_RESULT artifact
reducer reads partial artifacts -> writes FINAL_RESULT artifact
user downloads final artifact
```

No `file://` or `worker://` URI is valid in persisted result metadata.

---

## 4. Target repository layout

Keep the existing Python package at the repository root and add a Go
coordinator as a self-contained subproject.

```text
SciMesh/
  PLAN.md
  docs/
    worker-daemon-task.md
    database-integration-task.md
    api-contract.md                 # created in Phase 0
  scimesh/                          # Python local workloads and worker daemon
    chemistry/
    core/
    distributed/                    # planner/reducer contracts, Phase 3
    worker/
    workloads/
  tests/
    unit/
    integration/
    contract/
  coordinator/
    go.mod
    cmd/coordinator/main.go
    internal/
      config/
      domain/
      httpapi/
      queue/
      reducer/
      storage/
      store/postgres/
    migrations/
    tests/
  scripts/
    dev-start.sh                    # optional convenience script, not required runtime
```

Do not move the mature local workload code merely to satisfy this layout. Move
only when a distributed contract requires a clear shared module.

---

## 5. Cross-service API contract

The API contract is a compatibility boundary. Before either side is implemented,
copy this section into `docs/api-contract.md` and treat it as versioned.

All worker endpoints require bearer authentication. Worker identity and attempt
are checked against the current task lease in PostgreSQL.

### 5.1 Register worker

```http
POST /workers/register
Authorization: Bearer <token>
Content-Type: application/json

{
  "name": "lab-worker-01",
  "capabilities": ["similarity-search", "similarity-graph"],
  "cpu_count": 8,
  "memory_mb": 16384
}
```

Response:

```json
{
  "worker_id": "uuid",
  "heartbeat_interval_seconds": 15
}
```

### 5.2 Claim task

```http
POST /tasks/claim
Authorization: Bearer <token>
Content-Type: application/json

{
  "worker_id": "uuid",
  "capabilities": ["similarity-search", "similarity-graph"],
  "max_concurrency": 1
}
```

- `204 No Content`: no compatible task is available.
- `200 OK`: a task is leased atomically.

```json
{
  "task_id": "uuid",
  "attempt": 1,
  "lease_expires_at": "2026-07-22T12:05:00Z",
  "workload": "similarity-search",
  "input": {
    "uri": "https://coordinator.example/tasks/uuid/input",
    "sha256": "hex-sha256"
  },
  "parameters": {
    "query_id": "CHEMBL939",
    "top_k": 20
  }
}
```

### 5.3 Renew lease

```http
POST /tasks/{task_id}/heartbeat
Authorization: Bearer <token>
Content-Type: application/json

{"worker_id": "uuid", "attempt": 1}
```

The response **must** contain a renewed deadline:

```json
{"lease_expires_at": "2026-07-22T12:10:00Z"}
```

The worker schedules the next heartbeat before half of this returned TTL, never
using only a fixed interval.

### 5.4 Download input or shard

`GET /tasks/{task_id}/input` returns an artifact owned by the current task. The
worker verifies its SHA-256 before execution. If the returned URI redirects to
another origin, the worker must remove the coordinator bearer token.

### 5.5 Upload a partial artifact

```http
PUT /tasks/{task_id}/artifacts/{filename}
Authorization: Bearer <token>
Content-Type: text/csv
X-Worker-ID: uuid
X-Task-Attempt: 1

<streamed bytes>
```

The coordinator streams the body to its artifact storage, verifies ownership,
stores metadata and checksum, then returns:

```json
{
  "artifact_id": "uuid",
  "uri": "https://coordinator.example/artifacts/uuid/download",
  "sha256": "hex-sha256",
  "size_bytes": 1234
}
```

### 5.6 Complete or fail task

```http
POST /tasks/{task_id}/result
Authorization: Bearer <token>
Content-Type: application/json

{
  "worker_id": "uuid",
  "attempt": 1,
  "result": {
    "artifact_id": "uuid",
    "uri": "https://coordinator.example/artifacts/uuid/download",
    "sha256": "hex-sha256",
    "content_type": "text/csv"
  },
  "metrics": {"elapsed_seconds": 12.4, "processed_rows": 10000}
}
```

`POST /tasks/{task_id}/failure` uses the same identity fields and contains only
sanitized `error_code` and `error_message` values. No Python traceback, token,
or absolute worker path may be sent.

### 5.7 Idempotency and errors

| Situation | Required response |
| --- | --- |
| No compatible task | `204` |
| Worker/attempt does not own lease | `409` |
| Artifact does not belong to task/attempt | `409` |
| Same completion, same manifest | `200`/`202` idempotent success |
| Same attempt, different manifest | `409` |
| Invalid parameters/input | `400` |
| Worker authentication failure | `401`/`403` |

---

## 6. PostgreSQL data model

The existing brief describes `jobs` and `tasks`. Add first-class worker and
artifact records before implementation. The database is the source of truth for
state; files are referenced by metadata rather than discovered from directories.

### 6.1 Tables

#### `workers`

| Field | Notes |
| --- | --- |
| `id UUID PK` | Returned on registration |
| `name text` | Human-readable; unique only if desired |
| `capabilities jsonb` | Allowlisted workload names |
| `status` | `online`, `busy`, `offline` |
| `last_heartbeat_at timestamptz` | Liveness visibility |
| `created_at`, `updated_at timestamptz` | Audit |

#### `jobs`

| Field | Notes |
| --- | --- |
| `id UUID PK` | User-visible identifier |
| `workload text` | Registered distributed workload name |
| `status` | `created`, `planning`, `running`, `reducing`, `completed`, `failed`, `cancelled` |
| `parameters jsonb` | Validated job parameters |
| `input_artifact_id UUID` | Original uploaded data |
| `result_artifact_id UUID nullable` | Final result |
| `total_tasks`, `completed_tasks`, `failed_tasks` | Progress counters, updated transactionally |
| timestamps and `error_message` | Audit and failure status |

#### `tasks`

| Field | Notes |
| --- | --- |
| `id UUID PK`, `job_id UUID FK` | Identity and ownership |
| `chunk_index int` | Unique within job; deterministic reducer order |
| `workload text`, `parameters jsonb` | Typed task payload |
| `input_artifact_id UUID` | Dataset or shard |
| `status` | `pending`, `leased`, `running`, `completed`, `failed`, `cancelled` |
| `attempt`, `max_attempts` | Retry accounting |
| `lease_owner UUID nullable`, `lease_expires_at nullable` | Exclusive lease |
| `result_artifact_id UUID nullable` | Uploaded partial output |
| `metrics jsonb`, errors, timestamps, `version int` | Audit and concurrency |

#### `artifacts`

| Field | Notes |
| --- | --- |
| `id UUID PK` | Stable artifact identity |
| `job_id UUID FK`, `task_id UUID FK nullable` | Ownership |
| `kind` | `input`, `shard`, `partial_result`, `final_result`, `log` |
| `filename`, `storage_key`, `content_type` | Storage metadata |
| `size_bytes`, `sha256` | Integrity metadata |
| `created_at` | Audit |

### 6.2 Required constraints and queries

- unique `(job_id, chunk_index)` for tasks;
- unique `(task_id, kind)` for single-result task workloads;
- claim index: `(status, lease_expires_at, created_at)`;
- `attempt >= 0`, `max_attempts > 0`;
- a leased/running task must have owner and expiry;
- a completed task must reference a `partial_result` artifact;
- a completed job must reference a `final_result` artifact;
- `list_completed_results(job_id)` orders by `chunk_index`, never insertion time.

Atomic claim uses one PostgreSQL transaction with `FOR UPDATE SKIP LOCKED`.
Never write a `SELECT pending task` followed by an unguarded later `UPDATE`.

---

## 7. Workload evolution

The current local `Workload` CLI interface is intentionally small. Distributed
execution needs a second, explicit contract. Do not force every CLI helper into
the distributed interface; adapt only workloads that can be planned and reduced.

```python
class DistributedWorkload(Protocol):
    name: str
    version: str

    def validate_job(self, input_path: Path, parameters: dict[str, object]) -> None: ...
    def plan(self, input_path: Path, parameters: dict[str, object], workspace: Path) -> list[TaskPlan]: ...
    def execute_task(self, task: TaskPlan, workspace: Path) -> RunResult: ...
    def reduce(self, partial_results: list[Path], parameters: dict[str, object], workspace: Path) -> FinalResult: ...
    def describe(self) -> dict[str, object]: ...
```

`TaskPlan` is JSON-serializable and contains only validated parameters plus
artifact IDs/URIs. It never contains shell commands or local paths from another
machine.

### 7.1 Distributed similarity-search

1. Validate either `query_id` or `query_smiles`, never both.
2. Resolve a `query_id` once during planning and persist the canonical query
   SMILES/identity in the job metadata.
3. Split the input TSV into deterministic shard artifacts. Every shard keeps a
   header and a stable `chunk_index`.
4. Each worker streams one shard, skips invalid SMILES and the query molecule,
   and writes a sorted local top-k CSV.
5. The reducer merges all local top-k outputs with a bounded heap using the same
   deterministic tie-breaker as local SciMesh.
6. The final CSV must equal the current single-process result for the same input
   and options.

Important: a local shard top-k must retain at least the global requested `k`.
The reducer cannot recover a candidate discarded by every shard.

### 7.2 Distributed similarity-graph

1. Parse valid molecules once during planning or produce deterministic molecule
   block artifacts with stable block indices.
2. Emit one task for every block pair `(i, j)` where `i <= j`.
3. A diagonal task compares only pairs inside one block with local index `a < b`.
4. An off-diagonal task compares every molecule in block `i` with every molecule
   in block `j`.
5. Each task emits an edge-list CSV only for pairs satisfying the chosen
   `threshold_direction` and threshold.
6. The reducer merges edge files, verifies no duplicate unordered pair, and
   writes a deterministic sort order.

Correctness invariant:

```text
union(task-pairs) = all unordered molecule pairs
intersection(task-pairs) = empty
```

For a small fixture, the distributed result must exactly equal the local
brute-force graph for both `greater` and `less` threshold directions.

### 7.3 Future workload policy

A new workload is accepted only when it supplies:

- an input/parameter validator;
- an explicit sharding strategy;
- bounded-memory task execution;
- deterministic reduction semantics;
- fixture-based local and distributed correctness tests;
- a `describe()` payload for UI/API discovery.

---

## 8. Milestones and dependency order

```text
M0 contracts + test fixtures
  -> M1 Go coordinator skeleton + migrations
    -> M2 transactional queue + worker registry
      -> M3 artifact storage + worker contract integration
        -> M4 distributed similarity-search vertical slice
          -> M5 reducer + status/result API
            -> M6 distributed similarity-graph
              -> M7 UI, observability, CI hardening
```

Do not start graph distribution before the search vertical slice proves the
full artifact/lease/reducer lifecycle.

---

## 9. Task briefs for implementation

Each section below is deliberately self-contained. When assigning work, copy
the task block plus the **Shared context** section and any listed dependency.

### Shared context for every assignee

```text
Project: SciMesh
Architecture: Go/PostgreSQL coordinator; Python workers; local artifact storage.
Hard rules: workers have no DB credentials; task claims are atomic; artifacts
must be durable before completion; no arbitrary commands; deterministic output.
Read first: PLAN.md sections 2, 3, 5, and the task's dependencies.
Do not refactor unrelated files. Add or update tests with every behavior change.
```

### CTX-00 — Freeze API and error contract

**Goal:** Create `docs/api-contract.md` from section 5 and make it the single
source of truth for the Go coordinator and Python Worker.

**Depends on:** none.

**Deliverables:** endpoint table, JSON schemas/examples, headers, artifact
upload ownership rule, error mapping, retry/idempotency policy, and an explicit
version marker (`v1`).

**Acceptance criteria:**

- all worker endpoints and their paths/methods are listed;
- heartbeat response includes `lease_expires_at`;
- completion references only coordinator-uploaded artifacts;
- failure endpoint is distinct from result endpoint;
- document states whether the coordinator uses UTC RFC 3339 timestamps.

**Out of scope:** implementing HTTP handlers.

### CTX-01 — Bootstrap Go coordinator

**Goal:** Add the `coordinator/` Go module and a minimal healthy HTTP service.

**Depends on:** CTX-00.

**Deliverables:** `go.mod`; config parser; structured logger; `GET /healthz`;
graceful shutdown; `pgxpool` lifecycle; migration command documentation.

**Inputs:** `DATABASE_URL`, `COORDINATOR_STORAGE_DIR`, `COORDINATOR_ADDR`,
`COORDINATOR_TOKEN`, request timeout, pool size.

**Acceptance criteria:**

- `go test ./...` passes;
- service refuses to start with missing/invalid required configuration;
- `/healthz` returns database readiness without exposing secrets;
- shutdown closes HTTP server and `pgxpool` cleanly;
- migration execution is explicit, not implicit on production startup.

**Out of scope:** queue endpoints and UI.

### CTX-02 — PostgreSQL schema and migration set

**Goal:** Implement versioned SQL migrations for workers, jobs, tasks, and
artifacts from section 6.

**Depends on:** CTX-01, CTX-00.

**Deliverables:** up/down SQL migrations; enum/check constraints; indexes;
repository-domain structs as needed for scanning rows.

**Acceptance criteria:**

- empty PostgreSQL database migrates up and down in integration tests;
- constraints reject invalid state combinations;
- migration test uses `TEST_DATABASE_URL`, never SQLite;
- `tasks(job_id, chunk_index)` uniqueness and claim index exist;
- no application code creates tables dynamically.

**Out of scope:** HTTP handlers and lease operations.

### CTX-03 — Transactional queue and lease repository

**Goal:** Implement PostgreSQL repository operations for create, claim, renew,
complete, fail, expiry, and job status.

**Depends on:** CTX-02.

**Required operations:**

```go
CreateJobWithTasks(ctx, input)
ClaimNextTask(ctx, workerID, capabilities, leaseDuration)
RenewLease(ctx, taskID, workerID, attempt, leaseDuration)
CompleteTask(ctx, input)
FailTask(ctx, input)
ExpireLeases(ctx, now)
GetJobStatus(ctx, jobID)
ListCompletedResults(ctx, jobID)
```

**Acceptance criteria:**

- concurrent claim test proves one task/attempt has one owner;
- renewal returns a new `lease_expires_at` timestamp;
- stale attempt cannot renew, upload, fail, or complete;
- expired task returns to pending or fails after final attempt;
- completion is idempotent for identical manifest and conflicts for a different
  manifest;
- all operations are context-aware and parameterized.

**Out of scope:** artifact byte storage and Python worker changes.

### CTX-04 — Worker registry and coordinator HTTP handlers

**Goal:** Expose CTX-03 through versioned `net/http` handlers and add worker
registration/liveness state.

**Depends on:** CTX-00, CTX-03.

**Endpoints:** `POST /workers/register`, `POST /tasks/claim`,
`POST /tasks/{id}/heartbeat`, `GET /jobs/{id}`, and readiness endpoints.

**Acceptance criteria:**

- request DTOs are validated before calling services;
- no raw PostgreSQL errors leave the process;
- `204`, `400`, `401/403`, and `409` match CTX-00;
- heartbeat response returns renewed deadline;
- tests cover an offline worker and an expired lease;
- logs include request ID, worker ID, task ID, attempt, operation.

### CTX-05 — Coordinator artifact storage

**Goal:** Store input, shard, partial-result, and final-result files durably in
the coordinator filesystem and persist their metadata.

**Depends on:** CTX-02, CTX-04.

**Endpoints:** authenticated input upload/download and
`PUT /tasks/{id}/artifacts/{filename}`.

**Implementation rules:**

- stream request bodies to a staging file; never `ReadAll` a result;
- use sanitized generated storage keys, not user paths;
- calculate SHA-256 while streaming;
- atomically rename staging file only after successful write;
- verify worker lease owner and attempt before accepting task output;
- return a coordinator-owned durable URI and artifact ID.

**Acceptance criteria:**

- uploaded artifact is downloadable after coordinator restart;
- a foreign worker receives `409`;
- large-file test proves bounded-memory streaming behavior;
- checksum and size are persisted;
- failed upload leaves no visible artifact or orphan staging file.

### CTX-06 — Align Python Worker with the live Go contract

**Goal:** Adapt `scimesh/worker/` to CTX-00 through CTX-05 without changing
local workload algorithms.

**Depends on:** CTX-00, CTX-04, CTX-05.

**Required behavior:**

- register worker capabilities at startup;
- claim one task at a time;
- remove bearer token when input redirect changes origin;
- verify input checksum;
- renew lease from returned `lease_expires_at`;
- stream uploaded output with worker/attempt headers;
- submit the returned artifact manifest only after upload;
- send errors to `/failure`;
- preserve task directory until configured cleanup;
- reject unknown workload parameters.

**Acceptance criteria:**

- Python contract tests run against the real Go service in CI;
- no result references `worker://` or local paths;
- long fake runner causes multiple lease renewals;
- lost lease stops successful completion and reports conflict cleanly;
- worker CLI documents all environment variables and exits non-zero on invalid
  configuration.

### CTX-07 — Distributed workload protocol and job planner

**Goal:** Add the Python `DistributedWorkload` adapter protocol and coordinator
planner bridge for registered workloads.

**Depends on:** CTX-05, CTX-06.

**Deliverables:** typed task-plan JSON; validator; planner registry; reducer
registry; workload descriptions; no direct coordinator dependency in local
chemistry helpers.

**Acceptance criteria:**

- unknown workload rejected before a job/task is written;
- every plan payload is JSON-serializable and uses artifact references;
- planner failure leaves no partial job/tasks transaction;
- test fixture demonstrates planning a two-shard dummy workload;
- reducer receives completed artifacts ordered by `chunk_index`.

### CTX-08 — Distributed similarity-search vertical slice

**Goal:** Implement planner, worker execution adapter, reducer, and end-to-end
tests for distributed exact top-k similarity search.

**Depends on:** CTX-07.

**Acceptance criteria:**

- query ID is resolved before shards execute, or query SMILES is validated once;
- shards are deterministic and contain valid TSV headers;
- each shard produces local top-k CSV and reports invalid-SMILES counts;
- reducer output matches local single-process SciMesh byte-for-byte apart from
  permitted elapsed-time metadata;
- query molecule and duplicate canonical query SMILES are excluded;
- tie order is deterministic across worker completion order;
- test uses at least two workers and one retry.

### CTX-09 — Job reducer orchestration and final result API

**Goal:** When all task results are complete, run the appropriate reducer once,
persist a final artifact, update job state, and expose download/status.

**Depends on:** CTX-07, CTX-08.

**Acceptance criteria:**

- only one reducer process may transition a job into `reducing`;
- reducer is idempotent or protected by state/version transaction;
- reducer failure marks job failed with a sanitized error;
- `GET /jobs/{id}` reports counters and state correctly;
- final artifact has stored checksum and downloadable URI;
- integration test covers complete job lifecycle.

### CTX-10 — Distributed similarity-graph

**Goal:** Implement block-pair planning, execution, and deterministic reduction
for the exact sparse similarity graph.

**Depends on:** CTX-08, CTX-09.

**Acceptance criteria:**

- each block pair is planned once with stable `(left_block, right_block)`;
- diagonal and off-diagonal comparisons obey the pair invariant in section 7.2;
- no task creates a dense matrix;
- both `greater` and `less` threshold directions are preserved;
- reducer detects duplicate unordered pairs and fails safely;
- distributed output equals local brute-force output on a small fixture;
- result is invariant to block size and worker completion order.

### CTX-11 — Minimal dashboard and operator views

**Goal:** Add a small server-rendered or static HTML UI to inspect jobs, tasks,
workers, and download final artifacts.

**Depends on:** CTX-04, CTX-09.

**Acceptance criteria:**

- show worker name/capabilities/status/last heartbeat;
- show job status and completed/total task progress;
- show task attempt, lease owner, and sanitized error;
- refresh with simple polling; no frontend framework required;
- final result link is available only in `completed` state;
- HTML escapes user-controlled values.

### CTX-12 — Reliability, security, and CI hardening

**Goal:** Make the vertical slice safe to demo and difficult to regress.

**Depends on:** CTX-06 through CTX-11.

**Work items:**

- CI jobs for Python tests, Go tests, `go vet`, migrations, and contract tests;
- coordinator request-size limits and timeouts;
- token configuration/rotation documentation;
- structured logs and correlation IDs;
- cleanup policy for failed worker directories and stale staging files;
- metrics/status endpoint suitable for local monitoring;
- retry/backoff test matrix;
- release checklist and local two-worker demo script.

**Acceptance criteria:**

- clean checkout can run the documented demo;
- all quality gates execute in CI;
- no secret appears in logs/tests/errors;
- failure/retry scenarios have automated coverage;
- README contains architecture diagram, security caveat, and troubleshooting.

---

## 10. Suggested assignment bundles

These bundles minimize overlap. Do not run tasks from the same bundle in
parallel unless one engineer owns integration.

| Bundle | Tasks | Recommended owner |
| --- | --- | --- |
| Contract and data | CTX-00, CTX-02, CTX-03 | Go/PostgreSQL engineer |
| Coordinator API | CTX-01, CTX-04, CTX-05 | Go backend engineer |
| Python transport | CTX-06 | Python worker engineer |
| Distributed computation | CTX-07, CTX-08, CTX-10 | Scientific Python engineer |
| Product surface | CTX-09, CTX-11 | Full-stack/backend engineer |
| Quality gate | CTX-12 | DevOps/QA engineer |

Suggested order for a small team:

```text
Week 1: CTX-00 + CTX-01 + CTX-02
Week 2: CTX-03 + CTX-04
Week 3: CTX-05 + CTX-06
Week 4: CTX-07 + CTX-08
Week 5: CTX-09 + CTX-10
Week 6: CTX-11 + CTX-12
```

This is an ordering aid, not a deadline commitment. Start the next milestone
only after its predecessor's acceptance criteria are demonstrably met.

---

## 11. Test strategy

### 11.1 Python unit tests

- SMILES parsing, fingerprints, deterministic top-k and graph ordering;
- Worker parameter allowlist and CLI argument mapping;
- checksum verification and cross-origin auth stripping;
- heartbeat rescheduling from returned TTL;
- artifact upload manifest generation;
- runner failure sanitization and cleanup behavior.

### 11.2 Go unit tests

- configuration parser and error mapping;
- storage-key sanitization;
- request DTO validation;
- service state transition guards;
- reducer invocation selection.

### 11.3 PostgreSQL integration tests

- migrations up/down;
- concurrent `ClaimNextTask` without duplicate lease;
- lease expiry/retry/exhaustion;
- stale attempt conflict;
- artifact ownership and task completion idempotency;
- deterministic partial-result retrieval order.

### 11.4 Contract tests

Run Python Worker tests against a real Go coordinator and PostgreSQL instance.
At minimum prove:

1. worker registers;
2. worker claims one task;
3. worker receives a renewed lease;
4. input checksum is verified;
5. result is uploaded and available from coordinator storage;
6. completion persists the correct artifact;
7. bad checksum or runner failure reaches `/failure`;
8. a stale worker cannot complete after its lease has expired.

### 11.5 End-to-end tests

- two-worker similarity-search run equals local CLI output;
- retry one failed shard and still finish deterministically;
- graph on small fixture has all and only brute-force threshold edges;
- stopping a worker mid-task requeues its task after lease expiry;
- final result remains downloadable after coordinator restart.

---

## 12. Review gates

Before merging a task, reviewer checks:

### Every task

- scope matches one CTX block;
- tests cover new behavior and pass;
- no unrelated refactor or generated data is committed;
- errors are sanitized and parameters validated;
- documentation/API contract updated when behavior changes.

### Worker changes

- no SQL, DB credentials, or arbitrary command execution;
- no worker-local URI persisted as a result;
- artifact transfer is streamed and checksum verified;
- heartbeat uses returned deadline;
- cross-origin requests do not receive coordinator token.

### Coordinator changes

- mutating actions are transactional;
- lease/task ownership checked at every mutation;
- PostgreSQL operations are parameterized;
- result completion is idempotent;
- artifact ownership and storage keys are validated;
- state transitions cannot skip required intermediate conditions.

### Scientific workload changes

- local reference result exists;
- distributed result is compared to local result on fixtures;
- reduction ordering is deterministic;
- pair coverage/duplicate invariants are tested for graphs;
- memory remains bounded as specified.

---

## 13. Deferred backlog

Do not start these before CTX-12 is accepted.

- Replace local artifact storage with S3/MinIO behind an `ArtifactStore` API.
- Add worker labels/capacity-aware scheduling and concurrency > 1.
- Add cancellation propagation to workers.
- Add image outputs and final PDF reporting to job artifacts.
- Add CV/video workloads using the same planner/runner/reducer contract.
- Add observability export (Prometheus/OpenTelemetry).
- Add per-user/project authorization and signed artifact URLs.
- Add shard caching and content-addressed input deduplication.
- Add job priority and fair scheduling.
- Add a CLI for submitting and monitoring remote jobs.

---

## 14. Definition of the first usable distributed release

The release is complete when all of the following are true:

1. A user uploads a small ChEMBL TSV and starts a similarity-search job.
2. The Go coordinator creates PostgreSQL job/task/artifact records.
3. Two Python workers register, claim distinct shards, renew leases, and upload
   partial CSVs.
4. The coordinator reduces partial results into a deterministic final CSV.
5. The user observes progress and downloads the final CSV.
6. Killing one worker requeues only its lease after expiry; the job still
   completes within its attempt budget.
7. The same fixture run matches local SciMesh output.
8. CI executes Python, Go, PostgreSQL, and contract tests successfully.

Until these eight conditions are met, SciMesh is a promising set of components,
not yet a complete distributed platform.
