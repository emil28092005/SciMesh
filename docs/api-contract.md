# SciMesh Coordinator API Contract

**Status:** draft, version 1. This document is the compatibility boundary
between the Go coordinator and the Python Worker. Change it only in the same
pull request as both implementation and contract tests.

## General rules

- All worker endpoints require `Authorization: Bearer <token>`.
- Times use UTC RFC 3339, for example `2026-07-23T12:05:00Z`.
- JSON requests and responses use `application/json`.
- `worker_id` and `attempt` identify a lease. The coordinator validates them
  transactionally on every task mutation.
- A task becomes `completed` only after a coordinator-owned artifact is durable.
- Identical repeated completion is successful; a different result for the same
  attempt is a conflict.
- Coordinator API calls do not follow redirects. Artifact downloads may follow
  redirects only after removing the coordinator bearer token on origin change.

## Worker registration

```http
POST /workers/register

{"name":"lab-worker-01","capabilities":["similarity-search"],"cpu_count":8,"memory_mb":16384}
```

Returns `200 OK`:

```json
{"worker_id":"uuid","heartbeat_interval_seconds":15}
```

## Task lifecycle

### Claim

```http
POST /tasks/claim

{"worker_id":"uuid","capabilities":["similarity-search"],"max_concurrency":1}
```

Returns `204 No Content` when no compatible task exists. A successful atomic
claim returns `200 OK`:

```json
{
  "task_id":"uuid",
  "attempt":1,
  "lease_expires_at":"2026-07-23T12:05:00Z",
  "workload":"similarity-search",
  "input":{"uri":"https://coordinator.example/tasks/uuid/input","sha256":"hex-sha256"},
  "parameters":{"query_id":"CHEMBL939","top_k":20}
}
```

The claim is one PostgreSQL transaction using `FOR UPDATE SKIP LOCKED`.

### Heartbeat

```http
POST /tasks/{task_id}/heartbeat

{"worker_id":"uuid","attempt":1}
```

Returns `200 OK` and the renewed deadline:

```json
{"lease_expires_at":"2026-07-23T12:10:00Z"}
```

The Worker schedules its next heartbeat before half of the returned TTL.

### Input download

`GET /tasks/{task_id}/input` returns the claimed task input. The Worker verifies
its SHA-256 before execution. On a redirect to another origin, it removes the
coordinator bearer token.

## Artifact upload

```http
PUT /tasks/{task_id}/artifacts/{filename}
Content-Type: text/csv
X-Worker-ID: uuid
X-Task-Attempt: 1

<streamed bytes>
```

The coordinator streams the body to storage, checks lease ownership, records
the checksum and returns `201 Created`:

```json
{
  "artifact_id":"uuid",
  "uri":"https://coordinator.example/artifacts/uuid/download",
  "sha256":"hex-sha256",
  "size_bytes":1234
}
```

The returned URI is the only URI the Worker may send in task completion.
`worker://` and `file://` are invalid.

## Completion and failure

```http
POST /tasks/{task_id}/result

{
  "worker_id":"uuid",
  "attempt":1,
  "result":{
    "artifact_id":"uuid",
    "uri":"https://coordinator.example/artifacts/uuid/download",
    "sha256":"hex-sha256",
    "content_type":"text/csv"
  },
  "metrics":{"elapsed_seconds":12.4,"processed_rows":10000}
}
```

The coordinator returns `200`, `201`, or `202` for a valid completion. It must
verify that the artifact belongs to that task and attempt before completing it.

Use `POST /tasks/{task_id}/failure` only for a failed attempt:

```json
{"worker_id":"uuid","attempt":1,"error_code":"ValueError","error_message":"input checksum mismatch"}
```

Messages are sanitised: no token, traceback, absolute local path, or raw input.

## Error responses

| Situation | Response |
| --- | --- |
| Invalid JSON, field, or parameter | `400 Bad Request` |
| Missing or invalid authentication | `401 Unauthorized` / `403 Forbidden` |
| Worker/attempt does not own an active lease | `409 Conflict` |
| Artifact does not belong to the task/attempt | `409 Conflict` |
| Same attempt, different completion manifest | `409 Conflict` |
| Unexpected coordinator failure | `500` without internal details |

## Compatibility tests

Contract tests must cover: registration, `204` claim, successful claim,
heartbeat renewal, foreign worker and stale attempt conflicts, streamed upload,
checksum mismatch, success after upload, failure through `/failure`, and
idempotent completion.
