# SciMesh coordinator ↔ worker API contract (v1)

**Status marker:** `v1`. This document is the single source of truth for the Go
coordinator and the Python Worker Daemon. It is derived from `PLAN.md` §5 and
must be updated in the same change as any behaviour it describes.

- **Auth:** every endpoint except readiness requires `Authorization: Bearer <token>`.
- **Identity:** every mutating worker request carries `worker_id` and `attempt`;
  they are checked against the current task lease in PostgreSQL. A stale attempt
  gets `409`.
- **Timestamps:** UTC, RFC 3339 (e.g. `2026-07-22T12:05:00Z`).
- **Unknown JSON fields are rejected** with `400`.

## Implementation status

| Endpoint | Contract | Coordinator |
| --- | --- | --- |
| `GET /health` | readiness incl. DB | ✅ done |
| `POST /workers/register` | register + capabilities | ✅ done |
| `POST /tasks/claim` | atomic lease | ✅ done |
| `POST /tasks/{id}/heartbeat` | renew lease | ✅ done |
| `POST /tasks/{id}/result` | complete | ✅ done, references `artifact_id` |
| `POST /tasks/{id}/failure` | fail | ✅ done |
| `GET /jobs/{id}` | progress | ✅ done |
| `PUT /tasks/{id}/artifacts/{name}` | upload partial | ✅ done |
| `GET /artifacts/{id}/download` | download by id | ✅ done |
| `POST /jobs/upload` | upload dataset, coordinator chunks it | ✅ done |
| `GET /tasks/{id}/input` | download shard | ✅ done |

---

## Readiness

```http
GET /health
```

`200 {"status":"ok"}` when the database is reachable; `503 {"status":"unavailable"}`
otherwise. Unauthenticated.

## Submit a dataset (submitter-side)

```http
POST /jobs/upload
Authorization: Bearer <token>
Content-Type: multipart/form-data
```

Fields, in order (text fields first, file last — the file is streamed):
`workload`, `parameters` (JSON), `chunk_rows` (int, default 1000), and the file
part `file`. The coordinator stores the input, splits the TSV into shard
artifacts (header repeated per shard), and creates one task per shard.

`201`:

```json
{ "job_id": "uuid", "task_count": 3, "input_artifact_id": "uuid" }
```

Each resulting task's claim response carries `input.uri = /tasks/{id}/input`,
served by §5.4.

## Register worker

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

`201`:

```json
{ "worker_id": "uuid", "heartbeat_interval_seconds": 15 }
```

`cpu_count`/`memory_mb` are accepted for forward compatibility and not yet
persisted. `capabilities` must be non-empty (an allowlisted workload set).

## Claim task

```http
POST /tasks/claim
Authorization: Bearer <token>
Content-Type: application/json

{ "worker_id": "uuid", "capabilities": ["similarity-search"], "max_concurrency": 1 }
```

- `204 No Content`: no compatible task.
- `200 OK`: a task is leased atomically.

```json
{
  "task_id": "uuid",
  "attempt": 1,
  "lease_expires_at": "2026-07-22T12:05:00Z",
  "workload": "similarity-search",
  "input": { "uri": "https://coordinator/tasks/uuid/input", "sha256": "hex" },
  "parameters": { "query_id": "CHEMBL939", "top_k": 20 }
}
```

`max_concurrency` is accepted; the coordinator leases one task per call for now.

## Renew lease (heartbeat)

```http
POST /tasks/{task_id}/heartbeat
Authorization: Bearer <token>
Content-Type: application/json

{ "worker_id": "uuid", "attempt": 1 }
```

Response **must** contain a renewed deadline:

```json
{ "lease_expires_at": "2026-07-22T12:10:00Z" }
```

The worker schedules the next heartbeat before half of the returned TTL, never
on a fixed interval alone.

## Download input or shard (CTX-05)

`GET /tasks/{task_id}/input` returns the artifact owned by the current task. The
worker verifies its SHA-256 before execution. If the URI redirects to another
origin, the worker removes the coordinator bearer token.

## Upload a partial artifact (CTX-05)

```http
PUT /tasks/{task_id}/artifacts/{filename}
Authorization: Bearer <token>
Content-Type: text/csv
X-Worker-ID: uuid
X-Task-Attempt: 1

<streamed bytes>
```

`200`:

```json
{ "artifact_id": "uuid", "uri": "https://coordinator/artifacts/uuid/download",
  "sha256": "hex", "size_bytes": 1234 }
```

## Complete or fail task

```http
POST /tasks/{task_id}/result
Authorization: Bearer <token>
Content-Type: application/json

{
  "worker_id": "uuid",
  "attempt": 1,
  "result": { "artifact_id": "uuid", "sha256": "hex", "content_type": "text/csv" },
  "metrics": { "elapsed_seconds": 12.4, "processed_rows": 10000 }
}
```

The worker uploads its partial result first (§5.5), then completes with that
`artifact_id`. The coordinator verifies the artifact was stored for this exact
task before accepting it — a worker cannot complete one task with another task's
artifact. No worker-supplied URI is ever persisted.

```http
POST /tasks/{task_id}/failure
```

Same identity fields, plus sanitized `error_code`, `error_message`, `retryable`.
Never a traceback, token, or absolute worker path.

## Idempotency and errors

| Situation | Response |
| --- | --- |
| No compatible task | `204` |
| Worker/attempt does not own lease | `409` |
| Artifact does not belong to task/attempt | `409` |
| Same completion, same manifest | `200` idempotent |
| Same attempt, different manifest | `409` |
| Invalid parameters/input | `400` |
| Auth failure | `401` |
| Unknown job/task | `404` |
