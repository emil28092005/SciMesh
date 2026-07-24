# Task: Worker Daemon

## Goal

Implement a standalone **Worker Daemon** that repeatedly obtains one task from
the central coordinator, runs it locally, and submits a result manifest. The
worker must not access the database directly.

The target flow is:

```text
Worker Daemon -> coordinator: claim task
coordinator -> Worker Daemon: task metadata + input location
Worker Daemon -> local SciMesh Core / CV runner: execute
Worker Daemon -> coordinator: upload result artifact, then submit result manifest
```

The architecture sketch uses video chunks and CV, while the current SciMesh
repository contains local molecular workloads. Therefore the daemon must use a
small runner adapter: the first adapter may invoke a SciMesh CLI workload, and
future adapters may run a CV/video chunk processor. Do not put workload logic
inside the daemon.

## Deliverables

1. A Python module/package for the daemon and a console command, for example
   `scimesh-worker`.
2. Configuration via environment variables and CLI overrides:
   - `SCIMESH_COORDINATOR_URL` (required);
   - `SCIMESH_WORKER_NAME` (optional; defaults to the hostname);
   - `SCIMESH_WORKER_ID` (optional legacy/test override; production identity is
     returned by registration);
   - working directory for downloaded inputs and generated outputs;
   - poll interval and request timeout;
   - optional bearer token.
3. A `Runner` protocol and one `SciMeshRunner` implementation. The protocol
   must make a future `VideoRunner` possible without changing daemon control
   flow.
4. Structured logs containing `worker_id`, `task_id`, attempt number, state,
   elapsed time, and error type.
5. Unit tests using a mocked HTTP coordinator and a fake runner.

## Coordinator contract

Use JSON over HTTPS. Claiming a task changes its state, so use `POST`, even if
the initial diagram labels the endpoint as `GET /get_task`.

`docs/api-contract.md` is the authoritative API schema. This document explains
the daemon workflow and must not introduce a different request or response
shape.

### Register worker

At daemon startup, register the worker capabilities before claiming tasks:

```http
POST /workers/register
Content-Type: application/json

{
  "name": "lab-worker-01",
  "capabilities": ["similarity-search"],
  "cpu_count": 8,
  "memory_mb": 16384
}
```

The `worker_id` returned by this endpoint is used for the daemon lifetime.

### Claim a task

```http
POST /tasks/claim
Content-Type: application/json

{
  "worker_id": "<registered-uuid>",
  "capabilities": ["similarity-search"],
  "max_concurrency": 1
}
```

When no task is available, the coordinator returns `204 No Content`.

When a task is available, it returns `200 OK`:

```json
{
  "task_id": "0d2d5a53-4c7e-467e-93d2-45ed2dc18e46",
  "attempt": 1,
  "lease_expires_at": "2026-07-21T12:05:00Z",
  "workload": "similarity-search",
  "input": {
    "uri": "https://coordinator.example/tasks/0d2d/input",
    "sha256": "..."
  },
  "parameters": {
    "query_smiles": "CCO",
    "top_k": 20
  }
}
```

`input.uri` may initially point to a coordinator download endpoint. Keep input
retrieval behind an `ArtifactClient` abstraction so it can later be replaced by
object storage without changing the daemon state machine.

### Submit a result

```http
POST /tasks/{task_id}/result
Content-Type: application/json

{
  "worker_id": "worker-01",
  "attempt": 1,
  "result": {
    "artifact_id": "0d2d5a53-4c7e-467e-93d2-45ed2dc18e46",
    "uri": "https://coordinator.example/tasks/0d2d/result.csv",
    "sha256": "...",
    "content_type": "text/csv"
  },
  "metrics": {
    "elapsed_seconds": 12.4,
    "scanned_rows": 10000,
    "valid_molecules": 9876,
    "invalid_smiles": 124,
    "matches_emitted": 20
  }
}
```

The `result.uri` must be the durable URI returned by the artifact upload
endpoint below; a worker-local `file://` or `worker://` path is invalid.

### Upload a result artifact

```http
PUT /tasks/{task_id}/artifacts/{filename}
Content-Type: text/csv
X-Worker-ID: worker-01
X-Task-Attempt: 1

<CSV bytes>
```

The coordinator streams the artifact to its configured storage and responds:

```json
{
  "artifact_id": "0d2d5a53-4c7e-467e-93d2-45ed2dc18e46",
  "uri": "https://coordinator.example/tasks/0d2d/artifacts/result.csv",
  "sha256": "...",
  "size_bytes": 1234
}
```

For a failed execution, send a short, sanitized `error_code` and
`error_message` to `POST /tasks/{task_id}/failure`. Never send a Python
traceback, access token, or local path outside the worker directory.

## Required state machine

```text
idle -> claiming -> downloading -> running -> uploading -> submitting -> idle
                         |              |             |              |
                         +------------> failed <--------------------+
```

- Poll only after a `204` response or a transient failure; use exponential
  backoff with jitter and an upper bound.
- Verify the input checksum before running.
- Create one isolated task directory: `<work-dir>/<task-id>/<attempt>/`.
- Invoke the runner with an explicit argument list, never `shell=True`.
- Upload the produced result artifact before submitting its manifest.
- Version 1 produces exactly one CSV partial result. Multi-artifact manifests
  require an explicit future API-contract change.
- Do not mark a task completed until every submitted artifact has a durable
  coordinator-provided URI.
- A timeout, network error, or rejected submission must leave the local task
  directory available for diagnostics until a configurable cleanup period.
- Treat a duplicate successful submission as success when the coordinator
  returns an idempotent response for the same `task_id` and `attempt`.

## Runner interface

The daemon owns task orchestration; the runner owns only local execution.

```python
class Runner(Protocol):
    def run(self, task: ClaimedTask, task_dir: Path) -> RunResult:
        """Run one task and return output artifacts plus safe metrics."""
```

`SciMeshRunner` maps an allowlisted workload and validated parameters to the
local SciMesh reference functions. A planned `similarity-search` task contains
a resolved `query_smiles` (never `query_id`) and writes one exact local top-k
partial CSV plus the metrics above. Legacy single-shard tasks may still use the
CLI compatibility path:

```text
scimesh similarity-search <local-input> --query-id ... --output <task-dir>/result.csv
```

Do not accept an arbitrary command from the coordinator. Maintain an allowlist
of registered workload names and validate every parameter before invocation.

## Acceptance criteria

- With a fake coordinator, the daemon claims one task, downloads a fixture,
  invokes the fake runner once, and submits its CSV manifest.
- A `204` response does not create a task directory and waits before the next
  poll.
- A bad input checksum prevents runner execution and reports a failed task.
- A transient claim/submit failure retries with bounded backoff.
- Two workers cannot both complete the same leased attempt; the daemon handles
  a lease/submission conflict without corrupting local results.
- The daemon has no database driver or SQL queries.

## Out of scope

- FastAPI coordinator implementation;
- database schema and migrations;
- video segmentation, CV inference, and trajectory stitching;
- multiprocessing, distributed scheduling, and autoscaling.
