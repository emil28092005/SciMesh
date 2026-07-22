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

| Method | Path                          | Purpose                                    |
| ------ | ----------------------------- | ------------------------------------------ |
| POST   | `/jobs`                       | Create job + pending tasks transactionally |
| POST   | `/tasks/claim`                | Atomically lease one task (`204` if none)  |
| POST   | `/tasks/{task_id}/heartbeat`  | Renew the caller's lease                   |
| POST   | `/tasks/{task_id}/result`     | Record a completed result (idempotent)     |
| POST   | `/tasks/{task_id}/failure`    | Record failure / retryable state           |
| GET    | `/jobs/{job_id}`              | Aggregate job progress                     |
| GET    | `/health`                     | Liveness (unauthenticated)                 |

## Status

Scaffold with a **complete, tested domain**. Layers, wiring, routing, auth,
access logging, error mapping, transactions, migrations, and graceful shutdown
are in place. Repository methods are stubs returning `ErrNotImplemented`
(→ HTTP 501); the SQL for claiming and lease expiry is written and ready to wire.

Roadmap:

1. schema + migrations ✅
2. `ClaimNext`, `InsertBatch` — atomic claim via `FOR UPDATE SKIP LOCKED`
3. `GetForUpdate`, `Update`, `CountByStatus` — completes the result/failure paths
4. file upload / chunk download
5. `ExpireLeases` — SQL is written, needs wiring
6. stitcher: merge per-chunk top-k into the final CSV
7. integration tests against real Postgres via `TEST_DATABASE_URL`

## Tests

`internal/domain` is covered by unit tests that need **no database** — lease
ownership, stale attempts, idempotent replays, retry budgets, and expiry are all
pure functions of entity state:

```sh
go test ./...
go vet ./...
```

Integration tests (concurrent claiming, migrations) come in phase 7 and require
a real PostgreSQL instance supplied through `TEST_DATABASE_URL`.
