# Standalone (split) setup

The single-binary `coordinator serve` mode is the default for a scientist on
one machine. The **standalone setup** splits the platform into separate
processes — the coordinator, the userservice, and any number of workers —
typically on different machines, backed by PostgreSQL. This is the cluster
deployment.

```text
Browser (operator)
   │
   ▼
┌──────────────────────────────┐       ┌───────────────────────────┐
│  coordinator  (port 8080)    │       │  userservice (port 8081)  │
│  jobs · tasks · artifacts    │◄─────►│  users · roles · keys     │
│  PostgreSQL DB "scimesh"     │  JWT  │  PostgreSQL DB             │
└──────────────┬───────────────┘ secret │  "scimesh_users"          │
               │                        └───────────────────────────┘
               │  HTTP (workers connect out)
               ▼
      worker-agent × N  (COORDINATOR_URL, WORKER_AUTH_TOKEN)
        └─ python -m scimesh.worker.task   (needs Python + scimesh)
```

Each component is a separate process; workers never touch a database — they
only talk to the coordinator over HTTP.

## What each component needs

| Component | Binary | Configuration |
| --- | --- | --- |
| Coordinator | `coordinator` (`SCIMESH_DB=postgres`) | `DATABASE_URL`, `COORDINATOR_ADDR`, `COORDINATOR_TOKEN`, `COORDINATOR_STORAGE_DIR`, `JWT_SECRET`, `USERSERVICE_URL`, `PUBLIC_COORDINATOR_URL` |
| Userservice | the `users/` service | `USERSERVICE_ADDR`, `DATABASE_URL` (its own DB), `JWT_SECRET` (must match the coordinator), `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD` |
| Worker | `worker-agent` | `COORDINATOR_URL`, `WORKER_AUTH_TOKEN`, `WORK_DIR`, `TASK_RUNNER` (JSON array), `CPU_COUNT`, `MEMORY_MB` |

`JWT_SECRET` is the one secret shared between the coordinator and the
userservice: the userservice signs tokens with it, the coordinator verifies
them. It must be at least 32 bytes and identical on both.

## Option A — Docker Compose (fastest)

The repository ships compose files for the whole split stack: PostgreSQL for
both services, migrations, the coordinator, and the userservice:

```bash
cd coordinator
JWT_SECRET='change-me-32-bytes-minimum' \
BOOTSTRAP_ADMIN_EMAIL='root@scimesh.local' \
BOOTSTRAP_ADMIN_PASSWORD='choose-a-strong-password' \
  docker compose -f docker-compose.yml -f docker-compose.users.yml up -d --build
```

Then install workers on any machines with Python. The worker's own setup
wizard walks through the rest — URL, token or worker key, work directory —
and starts the worker for you:

```bash
curl -fsSL https://raw.githubusercontent.com/emil28092005/SciMesh/main/install.sh | bash -s worker
worker-agent setup          # local wizard at http://127.0.0.1:12700
```

Or configure by hand:

```bash
export COORDINATOR_URL=http://COORDINATOR_HOST:8080
export WORKER_AUTH_TOKEN="$COORDINATOR_TOKEN"   # the coordinator's shared token
export WORK_DIR=~/scimesh-worker
worker-agent
```

## Option B — Manual binaries

1. **Provision PostgreSQL** (two databases, or one server and `CREATE
   DATABASE`):

   ```bash
   ./coordinator setup --yes \
     --db 'postgres://scimesh:scimesh@db-host:5432/scimesh?sslmode=disable' \
     --env-file /etc/scimesh/coordinator.env
   ```

   The wizard creates the database when missing and writes the `.env` with a
   generated `JWT_SECRET`. The userservice needs its own database — create it
   and apply `users/migrations` (e.g. with the migrate CLI):

   ```bash
   createdb scimesh_users
   migrate -path users/migrations \
     -database 'postgres://scimesh:scimesh@db-host:5432/scimesh_users?sslmode=disable' up
   ```

2. **Run the userservice** with the *same* `JWT_SECRET`:

   ```bash
   cd users && make build   # builds the binary into users/bin/
   USERSERVICE_ADDR=':8081' \
   DATABASE_URL='postgres://scimesh:scimesh@db-host:5432/scimesh_users?sslmode=disable' \
   JWT_SECRET='<same secret>' \
   BOOTSTRAP_ADMIN_EMAIL='root@scimesh.local' \
   BOOTSTRAP_ADMIN_PASSWORD='choose-a-strong-password' \
     ./bin/userservice
   ```

3. **Run the coordinator**:

   ```bash
   ENV_FILE=/etc/scimesh/coordinator.env ./coordinator
   # or, without the .env:
   SCIMESH_DB=postgres \
   DATABASE_URL='postgres://scimesh:scimesh@db-host:5432/scimesh?sslmode=disable' \
   COORDINATOR_ADDR=':8080' \
   COORDINATOR_TOKEN='a-worker-token' \
   COORDINATOR_STORAGE_DIR='/var/lib/scimesh/artifacts' \
   JWT_SECRET='<same secret>' \
   USERSERVICE_URL='http://127.0.0.1:8081' \
   PUBLIC_COORDINATOR_URL='http://coordinator.example:8080' \
     ./coordinator
   ```

   The binary applies its embedded schema migrations on startup
   (`AUTO_MIGRATE=false` to disable when you manage them out of band).

4. **Attach workers** as in Option A (or via `worker-agent setup`). The UI
   login uses the userservice session; the workers use `COORDINATOR_TOKEN` or
   a worker key. The coordinator's admin console (`/ui/admin`) shows the
   whole cluster: jobs, worker fleet with trust controls, accounts and keys,
   workload switches and metrics.

## Notes

- The coordinator and the userservice each keep their own PostgreSQL database
  — different bounded contexts, deliberately not shared.
- A worker can join by hostname or IP; only outbound HTTP from the worker to
  the coordinator is required (no inbound firewall rules on workers).
- For a quick all-in-one alternative, `coordinator serve` embeds all of this
  on one machine — see the [home page](index.md).
