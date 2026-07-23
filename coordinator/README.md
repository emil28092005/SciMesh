# SciMesh Coordinator

Durable task-queue server for SciMesh, in Go on PostgreSQL. It owns all database
access; workers talk to it only over HTTP and never receive DB credentials.

Built as a **modular monolith following Clean Architecture** — one binary, four
layers, dependencies pointing strictly inward. See
`docs/database-integration-task.md` and `docs/worker-daemon-task.md` in the repo
root for the full contract.

## Layers

```
        infra       config, pgxpool, http.Server, clock    ← frameworks & drivers
        transport   http handlers      ← inbound: who calls us
        storage     sql repositories   ← outbound: who we call
        usecase     business operations + PORTS            ← application rules
        domain      Task, Job + their invariants           ← enterprise rules

                            ┌── transport ──┐
        domain ◄── usecase ◄┤               ├◄── infra
                            └── storage ────┘
```

`transport` and `storage` are one layer — the "interface adapters" ring — split
by direction rather than by category, so a file's path tells you its role.

The rule that matters: **source dependencies point only inward**. `domain`
imports nothing from this module; `usecase` sees only `domain`; `transport` and
`storage` know nothing of each other. Verify it at any time with:

```sh
go list -f '{{range .Imports}}{{.}}{{"\n"}}{{end}}' ./internal/domain | grep internal   # must be empty
```

## Layout

```
coordinator/
  cmd/coordinator/main.go      # composition root: the only place with concrete types
  internal/
    domain/                    # entities + rules, no I/O
      task.go                    Task, lease/complete/fail/expire transitions
      job.go                     Job, chunk fan-out, status derivation
      errors.go                  business-rule violations
    usecase/                   # one type per operation, dependencies injected
      ports.go                   TaskRepository, JobRepository, TxManager, Clock
      dto.go                     use-case boundary inputs
      task.go                    claim, renew, complete, fail, expire
      job.go                     create, status, results, stitch
    transport/http/            # routing, DTOs, middleware, error mapping
    storage/postgres/          # SQL behind the ports; TxManager via context
    infra/                     # config.go db.go clock.go server.go
  migrations/                  # golang-migrate SQL, run as an explicit command
```

A full map — file-by-file table, a request traced through every layer, and a
"where do I add X" guide — lives in [ARCHITECTURE.md](ARCHITECTURE.md).

## Quickstart

### With Docker (nothing to install but Docker)

```sh
make up                       # Postgres → migrations → coordinator
curl localhost:8080/health
make logs                     # follow the coordinator
make down                     # stop (add down-clean to drop the DB volume)
```

`up` starts three services in order: Postgres waits until `pg_isready` passes, a
one-shot `migrate` container applies the schema and exits, and only then does the
coordinator start — so it never queries a database that has no tables.

> **Needs BuildKit.** The Dockerfile uses `RUN --mount=type=cache` to reuse the
> Go module and compiler caches between builds. If the build fails with
> *"the --mount option requires BuildKit"*, install the buildx plugin —
> `pacman -S docker-buildx` on Arch, `apt install docker-buildx-plugin` on Debian.

### Locally, against your own Postgres

```sh
cp .env.example .env          # then edit DATABASE_URL / WORKER_AUTH_TOKEN
                              # it is loaded automatically — no export needed

make tidy                     # fetch deps (needs network once)
make migrate-up               # apply schema (needs the migrate CLI)
make run                      # start the server
```

## Configuration

Settings come from the environment. A `.env` file is loaded at startup via
`godotenv` as a local-dev convenience (override its path with `ENV_FILE`):

- a missing `.env` is not an error — production injects real env vars;
- **real environment variables always win** over the file, so an orchestrator's
  values are never shadowed by a stale `.env` baked into an image.

See `.env.example`; only `DATABASE_URL` is required.

## Endpoints

| Method | Path                               | Purpose                                       |
| ------ | ---------------------------------- | --------------------------------------------- |
| POST   | `/workers/register`                | Register a worker, get its id                 |
| POST   | `/jobs`                            | Create job + tasks from chunk URIs            |
| POST   | `/jobs/upload`                     | Upload a dataset; coordinator chunks it       |
| GET    | `/jobs/{job_id}`                   | Aggregate job progress                        |
| POST   | `/tasks/claim`                     | Atomically lease one task (`204` if none)     |
| GET    | `/tasks/{task_id}/input`           | Download the task's input shard               |
| POST   | `/tasks/{task_id}/heartbeat`       | Renew the caller's lease (→ `running`)        |
| PUT    | `/tasks/{task_id}/artifacts/{name}`| Upload a partial-result artifact              |
| POST   | `/tasks/{task_id}/result`          | Complete with an artifact id (idempotent)     |
| POST   | `/tasks/{task_id}/failure`         | Record failure / retryable state              |
| GET    | `/artifacts/{artifact_id}/download`| Download an artifact by id                    |
| GET    | `/health`                          | Readiness incl. database (unauthenticated)    |

The full contract is in [`docs/api-contract.md`](../docs/api-contract.md) and
[`docs/openapi.yaml`](../docs/openapi.yaml); a worker-author guide is in
[`docs/building-workers.md`](../docs/building-workers.md).

## Poking the API

Two ways, both checked in:

```sh
make smoke                    # every endpoint, asserted; non-zero exit on failure
```

`api/requests.http` runs the same calls one at a time from an editor with a REST
client (VSCodium/VS Code "REST Client", JetBrains HTTP Client). Later requests
reuse ids captured from earlier responses, so it doubles as API documentation.

## Status

Works end to end: a worker registers, a dataset is uploaded and chunked into
shard tasks (or a job is created from chunk URIs), tasks are leased one at a
time, downloaded, heartbeated (`leased → running`), completed via uploaded
result artifacts, and reflected in job progress. A reaper reclaims expired
leases and marks silent workers offline.

Done: schema + migrations, atomic claim (`FOR UPDATE SKIP LOCKED`), optimistic
concurrency, result/failure paths, lease expiry, worker registry + liveness,
artifact storage, dataset upload + chunking, request-size limits.

Still stubbed: `StitchJob.Execute` — merging per-chunk top-k into the final CSV
is workload semantics that belongs to the Python side (reducer).

## Tests

Unit tests need **no database** — domain rules, use-case orchestration (over
in-memory `internal/memstore`), and HTTP handlers (via `httptest`):

```sh
make test           # go test ./...
make vet
make lint
go test -race ./...
```

Integration tests run against a **real PostgreSQL** (the spec forbids mocks
here — they verify `FOR UPDATE SKIP LOCKED`, optimistic concurrency, rollback):

```sh
docker compose up -d
make test-integration TEST_DATABASE_URL='postgres://scimesh:scimesh@localhost:5432/scimesh?sslmode=disable'
```

CI (`.github/workflows/coordinator.yml`) runs vet, gofmt, race tests, lint, and
the integration suite against a Postgres service on every push and PR.
