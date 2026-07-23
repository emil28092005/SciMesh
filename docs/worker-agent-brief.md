# Brief: build the SciMesh worker against the coordinator

You are implementing the **worker side**. The **coordinator** (Go/PostgreSQL) is
already built, tested, and running on branch `feat/coordinator`. This brief tells
you what exists, where the contract is, and what to deliver.

## What the coordinator already does (done — do not reimplement)

A durable task-queue server. Over HTTP only (workers never touch the database):

- **Worker registry** — `POST /workers/register` returns a `worker_id`; the
  coordinator tracks liveness and marks silent workers offline.
- **Jobs** — created from chunk URIs (`POST /jobs`) or by uploading a dataset
  (`POST /jobs/upload`), which the coordinator splits into shard tasks itself.
- **Queue** — atomic claim (`FOR UPDATE SKIP LOCKED`), leases, heartbeats
  (`leased → running`), a reaper that requeues expired leases, retry budget.
- **Artifacts** — the worker uploads a partial result (`PUT`), the coordinator
  stores it (streamed, checksummed) and owns it; completion references an
  `artifact_id`, not a worker URI.
- **Input delivery** — `GET /tasks/{id}/input` streams a task's shard.

Full endpoint list and status: `coordinator/README.md`.

## The contract (read these first)

| File | What it is |
| --- | --- |
| `docs/openapi.yaml` | OpenAPI 3.0 — **generate your client from this** |
| `docs/building-workers.md` | step-by-step guide: the claim→heartbeat→upload→complete loop, auth, lease semantics, status codes, and the rules you must not break |
| `docs/api-contract.md` | the same contract in prose |
| `coordinator/api/requests.http` | real request/response examples for every endpoint |

Generate a typed client instead of hand-writing HTTP:

```sh
openapi-python-client generate --path docs/openapi.yaml
# or just models:
datamodel-codegen --input docs/openapi.yaml --output scimesh_models.py
```

## Run the coordinator locally to develop against it

```sh
cd coordinator && docker compose up -d      # listens on :8080, migrations auto-applied
make smoke                                   # exercises the whole flow (should pass)
```

Auth: every request except `GET /health` needs `Authorization: Bearer <token>`
(the compose default is `dev-token`; check `coordinator/.env.example`).

## Your deliverable (CTX-06 in PLAN.md)

A worker daemon that:

1. registers at startup and reuses its `worker_id`;
2. claims one task at a time; backs off on `204`;
3. downloads the input via `input.uri` and **verifies its `sha256`** before running;
4. heartbeats before half the lease TTL elapses;
5. uploads the result artifact, then completes the task with that `artifact_id`;
6. reports failures to `/failure` with sanitized error fields;
7. is configured by env: coordinator URL, worker id, token, poll interval, work dir.

## Acceptance criteria

- A worker registers, claims a shard, downloads and checksum-verifies its input,
  heartbeats through a long run, uploads a result, and completes it — end to end
  against the real coordinator.
- A lost lease (missed heartbeats) surfaces as a clean `409` and the worker moves
  on rather than crashing.
- No result ever references a `worker://` or local path — only uploaded artifacts.
- Contract tests run the worker against the real Go coordinator + Postgres in CI.

## Rules you must not break

1. HTTP only — never the database.
2. Every mutating call carries `worker_id` **and** `attempt`; a stale attempt is `409`.
3. Verify the input checksum before executing.
4. Upload the result artifact **before** calling `/result`.
5. Strip the bearer token on any cross-origin redirect.
6. Sanitize errors — never send a traceback, token, or absolute path.

Anything about the coordinator's behavior that isn't clear here is answered by
`docs/openapi.yaml` (authoritative shapes) and `docs/building-workers.md`.
