# Building a SciMesh worker

A worker is a process that pulls tasks from the coordinator, runs them, and
returns results. It talks to the coordinator **only over HTTP** — it never sees
the database, and it needs no inbound port (all requests are outbound). This
guide is what you need to implement one (the reference is a Python daemon, but
nothing here is Python-specific).

**Read alongside:**
[`api-contract.md`](api-contract.md) (the contract in prose) and
[`openapi.yaml`](openapi.yaml) (machine-readable — generate a typed client from
it, see the bottom).

---

## The one loop

A worker is essentially this loop:

```text
register once
loop forever:
    task = POST /tasks/claim
    if no task (204): sleep, continue
    download the task's input, verify its checksum
    run the workload  ── while running, POST heartbeat before the lease expires
    upload the result artifact  (PUT)
    POST /tasks/{id}/result   with the artifact id
    on any failure:  POST /tasks/{id}/failure
```

Everything below fills in the details.

## 0. Auth

Every request except `GET /health` carries a shared bearer token:

```
Authorization: Bearer <COORDINATOR_TOKEN>
```

The token is handed to you out of band (env var / secret) — the same string the
coordinator was started with. Never log it, never send it in an error body.

## 1. Register (once, at startup)

```http
POST /workers/register
{ "name": "lab-worker-01", "capabilities": ["similarity_search"] }
```

Response: `{ "worker_id": "<uuid>", "heartbeat_interval_seconds": 15 }`.

- `capabilities` are the workload names you can run — the coordinator only hands
  you matching tasks.
- **Keep `worker_id`**. Use it as your identity in every later call. Using the
  registered UUID is what lets the coordinator track your liveness (it marks
  workers offline after they go silent).
- Current coordinator jobs use `similarity_search` / `similarity_graph`; the
  reference Python worker also accepts the public CLI spellings with hyphens.

## 2. Claim a task

```http
POST /tasks/claim
{ "worker_id": "<uuid>", "capabilities": ["similarity_search"] }
```

- `200` → a leased task (below).
- `204` → nothing to do; back off a little and poll again.

```json
{
  "task_id": "<uuid>",
  "attempt": 1,
  "lease_expires_at": "2026-07-22T12:05:00Z",
  "workload": "similarity_search",
  "input": { "uri": "/tasks/<uuid>/input", "sha256": "<hex>" },
  "parameters": { "query_id": "CHEMBL939", "top_k": 20 }
}
```

**`attempt` matters.** Every later call for this task must echo the exact
`attempt` you were handed. A task requeued after a lost lease comes back with a
higher attempt; an old attempt is rejected with `409`.

## 3. Download the input, verify it

```http
GET {input.uri}          # e.g. GET /tasks/<uuid>/input
```

Stream it to disk and **check the SHA-256 against `input.sha256`** before
running. A mismatch means a corrupt shard — fail the task with a clear code,
don't process garbage.

> If `input.uri` ever redirects to another host (object storage), **strip the
> `Authorization` header** on the redirect — never send the coordinator token to
> a third party.

## 4. Run — and heartbeat while you run

Long tasks must prove they are alive, or the coordinator's reaper reclaims the
lease and hands the task to someone else.

```http
POST /tasks/{task_id}/heartbeat
{ "worker_id": "<uuid>", "attempt": 1 }
```

Response: `{ "lease_expires_at": "<new deadline>" }`.

- Schedule the next heartbeat at **less than half** the remaining TTL — don't
  rely on a fixed interval. If `lease_expires_at` is 2 minutes out, heartbeat
  every ~45s.
- The first heartbeat also moves the task from `leased` to `running` on the
  server; you don't have to do anything special for that.

If you miss the deadline, your lease expires: a later `heartbeat`/`result` will
come back `409`, and the task is already back in the queue.

## 5. Upload the result artifact

The coordinator owns results — you upload the bytes, it stores them and computes
the checksum. Identity travels in **headers** here, not the body:

```http
PUT /tasks/{task_id}/artifacts/result.csv
Content-Type: text/csv
X-Worker-ID: <uuid>
X-Task-Attempt: 1

<streamed result bytes>
```

Response: `{ "artifact_id": "<uuid>", "uri": "...", "sha256": "<hex>", "size_bytes": 1234 }`.

Keep the returned `artifact_id`.

## 6. Complete the task

```http
POST /tasks/{task_id}/result
{ "worker_id": "<uuid>", "attempt": 1,
  "result": { "artifact_id": "<uuid>" },
  "metrics": { "elapsed_seconds": 12.4, "processed_rows": 10000 } }
```

- Reference the `artifact_id` you just uploaded **for this task**. The
  coordinator verifies it belongs to this task; another task's artifact → `409`.
- **Idempotent:** if your network dropped and you retry the same `artifact_id`,
  you get `200` again, not a conflict. Safe to retry.

## 7. …or fail it

```http
POST /tasks/{task_id}/failure
{ "worker_id": "<uuid>", "attempt": 1,
  "error_code": "download_failed", "error_message": "checksum mismatch",
  "retryable": true }
```

- `retryable: true` → the task returns to the queue while attempts remain (a new
  worker gets it at a higher `attempt`).
- `retryable: false` → it fails terminally.
- Send only a short, sanitized `error_code`/`error_message`. **Never** a Python
  traceback, a token, or an absolute local path.

---

## Status-code cheat sheet

| Code | Meaning for the worker |
| --- | --- |
| `204` | claim: queue empty — back off and retry |
| `400` | your request is malformed (bad UUID, unknown field) |
| `401` | bad/missing token |
| `404` | task/job/artifact doesn't exist |
| `409` | you don't hold the lease, or your `attempt` is stale, or a different result was already recorded — **stop working on this task**, it's no longer yours |

A `409` is normal, not a crash: it means the coordinator gave the task to
someone else (usually because your lease expired). Log it and move on to the
next claim.

## Config the worker should expose

Per the worker contract, at minimum:

- `SCIMESH_COORDINATOR_URL` (e.g. `http://coordinator:8080`)
- worker name (the coordinator returns its `worker_id` at registration;
  `SCIMESH_WORKER_ID` is only a legacy/test override)
- the bearer token
- poll interval and request timeout
- a working directory for downloaded inputs and generated outputs

## Generate a client from the spec

Instead of hand-writing request code, generate it:

```sh
# typed async client
openapi-python-client generate --path docs/openapi.yaml

# or just the Pydantic models
datamodel-codegen --input docs/openapi.yaml --output scimesh_models.py
```

## Try the endpoints by hand first

`coordinator/api/requests.http` walks the whole flow one request at a time
(register → claim → heartbeat → upload → result), and
`coordinator/scripts/smoke.sh` runs it end to end. Read those to see real
request/response bodies before writing code.

## The rules you must not break

1. Never touch the database — HTTP only.
2. Every mutating call carries `worker_id` **and** `attempt`.
3. Verify the input checksum before running.
4. Upload the result artifact **before** calling `/result`.
5. Never persist a `worker://` or local path as a result — the coordinator owns
   artifacts.
6. Strip the bearer token on any cross-origin redirect.
7. Sanitize error output — no tracebacks, tokens, or absolute paths.
