# Worker integration

The **Go worker agent** (`coordinator/cmd/worker-agent`, built with
`make agent`) is the worker: a coordinator client, never a database client.
It polls the coordinator over HTTP, executes SDK workloads, and uploads
partial results through the coordinator — results never carry `file://` or
`worker://` URIs, and failures go to `/failure`.

The same scientific handlers run in three places: the local CLI cores, the
`LocalCoreBatchExecutor` conformance harness, and the agent — because the
agent spawns the workload's own SDK runner in a Python subprocess per task.

## Claim lifecycle

```text
register -> claim (one task) -> download input + verify sha256
         -> spawn python -m scimesh.worker.task -> upload partial -> submit
```

- **Register**: the agent advertises its capabilities
  (`CAPABILITIES`, default `similarity-search,similarity_search`).
- **Claim**: atomic lease of one task; `204` means idle.
- **Download**: the input is streamed and its SHA-256 verified; the bearer
  token is stripped on cross-origin redirects.
- **Heartbeat**: a background goroutine renews the lease from the returned
  deadline at less than half the remaining TTL.
- **Task execution**: a Python subprocess (`TASK_RUNNER`, default
  `python -m scimesh.worker.task`) runs the SDK workload; exit 0 writes the
  result manifest, exit 3 means permanent failure, exit 1 retryable.
- **Upload**: the partial CSV is streamed with `X-Worker-ID` /
  `X-Task-Attempt` headers, then completion references the
  coordinator-owned artifact id.
- **Failure**: sanitized `error_code` + message (≤300 chars, no local
  paths); transient errors are retried with backoff; lost leases stop
  quietly.

## Authentication

- `WORKER_AUTH_TOKEN` — a static bearer token (the shared service token).
- `WORKER_KEY` + `USERSERVICE_URL` — a long-lived worker key exchanged at
  the userservice for short-lived JWTs; the agent refreshes them before
  expiry and retries once after a 401.

## Configuration

| Variable | Meaning |
| --- | --- |
| `COORDINATOR_URL` | Coordinator base URL (required) |
| `WORKER_AUTH_TOKEN` | Static bearer token (when no worker key) |
| `WORKER_KEY` / `USERSERVICE_URL` | Worker-key authentication |
| `WORK_DIR` | Attempt directory root (default `./scimesh-agent-data`) |
| `WORKER_NAME` | Registered name (default: hostname) |
| `WORKER_ID` | Fixed identity override (tests) |
| `CPU_COUNT` / `MEMORY_MB` | Advertised capacity |
| `POLL_INTERVAL` / `REQUEST_TIMEOUT` / `HEARTBEAT_INTERVAL` | Timings |
| `CLEANUP_AFTER_SECONDS` | Delete attempt dirs older than this |
| `CAPABILITIES` | JSON array of advertised capabilities |
| `TASK_RUNNER` | JSON command array for the task subprocess |
| `MAX_TASKS` / `EXIT_WHEN_IDLE` | Lifecycle limits |

## Loading workloads

The task subprocess loads workloads from `SCIMESH_WORKLOAD_ALLOWLIST` (a
JSON array of `{distribution, name, version, digest}` entries matched
against installed `scimesh.workloads` entry points) or falls back to every
enabled built-in SDK workload of the installed package — the same library
the coordinator embeds as its catalog. Discovery measures the installed
package before and after importing and fails transactionally on any
mismatch. The agent advertises the enabled catalog workloads as its
capabilities unless `CAPABILITIES` is set explicitly. Generate an allowlist
for a non-editable install with `scimesh workload allowlist`.

```bash
make agent
COORDINATOR_URL=https://coordinator.example \
WORKER_AUTH_TOKEN=... \
TASK_RUNNER='["/opt/scimesh/.venv/bin/python","-m","scimesh.worker.task"]' \
./coordinator/bin/worker-agent
```

## v1 contract limits

The coordinator protocol v1 persists flat one-input/one-result tasks. Until
a versioned protocol rollout:

- map stages with more than one input port (for example
  `similarity-graph`'s block pairs) are **rejected by the task runner** —
  the coordinator does not create such tasks anyway;
- `max_rows` is a plan-time option and is rejected per task;
- workloads beyond the allowlisted set are rejected as unsupported.
