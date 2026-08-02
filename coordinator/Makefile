.DEFAULT_GOAL := help

.PHONY: help build run test test-integration vet lint tidy check migrate-up migrate-down up down down-clean logs ps rebuild psql smoke agent coordinator workloads-export demo-ui demo-down demo-reset demo-logs

# `check` deliberately uses its own Compose project and host ports. This keeps
# it from connecting to or replacing a developer's local PostgreSQL instance.
CHECK_PROJECT ?= scimesh-check
CHECK_POSTGRES_PORT ?= 55432
CHECK_COORDINATOR_PORT ?= 18080
CHECK_HOST ?= http://localhost:$(CHECK_COORDINATOR_PORT)
CHECK_TOKEN ?= dev-token
CHECK_DATABASE_URL ?= postgres://scimesh:scimesh@localhost:$(CHECK_POSTGRES_PORT)/scimesh?sslmode=disable
CHECK_COMPOSE = POSTGRES_PORT=$(CHECK_POSTGRES_PORT) COORDINATOR_PORT=$(CHECK_COORDINATOR_PORT) docker compose -p $(CHECK_PROJECT)

# --- local manual demo ---------------------------------------------------
# A separate project and ports mean this demo cannot collide with the normal
# `make up` stack or a developer's local PostgreSQL on 5432.
DEMO_PROJECT ?= scimesh-demo
DEMO_POSTGRES_PORT ?= 55432
DEMO_COORDINATOR_PORT ?= 18080
DEMO_UI_TOKEN ?= demo-ui-secret
DEMO_WORKER_TOKEN ?= demo-worker-token
DEMO_WORKERS ?= 2
# Short public knob for `make demo-ui WORKERS=3`; DEMO_WORKERS remains useful
# for scripts and backwards-compatible documentation.
WORKERS ?= $(DEMO_WORKERS)
DEMO_DIR ?= .demo

# workloads.json is the UI workload catalog, generated from the Python SDK
# workload library. It is checked in so the binary embeds it; regenerate it
# whenever workloads or their manifests change (requires the Python venv).
WORKLOADS_JSON := internal/workloads/workloads.json

# The Go worker agent: a static coordinator client that executes SDK
# workloads in a Python subprocess per claimed task.
agent:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/worker-agent ./cmd/worker-agent
	@printf '%s\n' 	  'Built bin/worker-agent. Configure via environment:' 	  '  COORDINATOR_URL, WORKER_AUTH_TOKEN, WORK_DIR, CPU_COUNT, MEMORY_MB,' 	  '  POLL_INTERVAL, REQUEST_TIMEOUT, HEARTBEAT_INTERVAL, CAPABILITIES,' 	  '  TASK_RUNNER, MAX_TASKS, EXIT_WHEN_IDLE, WORKER_NAME, WORKER_ID'

# The coordinator server as a static binary, the same way the Docker image
# builds it (CGO_ENABLED=0, trimmed). Requires PostgreSQL at runtime.
coordinator:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/coordinator ./cmd/coordinator
	@printf '%s\n' \
	  'Built bin/coordinator. Configure via environment:' \
	  '  DATABASE_URL, COORDINATOR_ADDR, COORDINATOR_TOKEN, UI_AUTH_TOKEN,' \
	  '  COORDINATOR_STORAGE_DIR, SCIMESH_DOCS_DIR, JWT_SECRET, USERSERVICE_URL'

workloads-export:
	cd .. && .venv/bin/scimesh workload export -o coordinator/$(WORKLOADS_JSON)

help:
	@printf '%s\n' \
	  'SciMesh coordinator commands:' \
	  '  make up / make down          Start or stop the normal coordinator stack.' \
	  '  make demo-ui [WORKERS=3]     Start isolated UI demo services and local workers.' \
	  '  make demo-logs               Follow coordinator logs for the UI demo.' \
	  '  make demo-down               Stop the demo services and workers.' \
	  '  make demo-reset              Stop the demo and wipe its data volumes.' \
	  '  make workloads-export        Regenerate the embedded UI workload catalog.' \
	  '  make test / make vet         Run Go verification.' \
	  '' \
	  'Demo UI: http://localhost:18080/ui (login page; admin root@scimesh.local / rootpassword).'

demo-ui:
	@DEMO_PROJECT="$(DEMO_PROJECT)" \
	DEMO_POSTGRES_PORT="$(DEMO_POSTGRES_PORT)" \
	DEMO_COORDINATOR_PORT="$(DEMO_COORDINATOR_PORT)" \
	DEMO_UI_TOKEN="$(DEMO_UI_TOKEN)" \
	DEMO_WORKER_TOKEN="$(DEMO_WORKER_TOKEN)" \
	DEMO_WORKERS="$(WORKERS)" \
	DEMO_DIR="$(DEMO_DIR)" \
	./scripts/demo-ui.sh start

demo-down:
	@DEMO_PROJECT="$(DEMO_PROJECT)" \
	DEMO_POSTGRES_PORT="$(DEMO_POSTGRES_PORT)" \
	DEMO_COORDINATOR_PORT="$(DEMO_COORDINATOR_PORT)" \
	DEMO_UI_TOKEN="$(DEMO_UI_TOKEN)" \
	DEMO_WORKER_TOKEN="$(DEMO_WORKER_TOKEN)" \
	DEMO_DIR="$(DEMO_DIR)" \
	./scripts/demo-ui.sh stop

demo-reset:
	@DEMO_PROJECT="$(DEMO_PROJECT)" \
	DEMO_POSTGRES_PORT="$(DEMO_POSTGRES_PORT)" \
	DEMO_COORDINATOR_PORT="$(DEMO_COORDINATOR_PORT)" \
	DEMO_UI_TOKEN="$(DEMO_UI_TOKEN)" \
	DEMO_WORKER_TOKEN="$(DEMO_WORKER_TOKEN)" \
	DEMO_DIR="$(DEMO_DIR)" \
	./scripts/demo-ui.sh reset

demo-logs:
	@DEMO_PROJECT="$(DEMO_PROJECT)" \
	DEMO_POSTGRES_PORT="$(DEMO_POSTGRES_PORT)" \
	DEMO_COORDINATOR_PORT="$(DEMO_COORDINATOR_PORT)" \
	DEMO_UI_TOKEN="$(DEMO_UI_TOKEN)" \
	DEMO_WORKER_TOKEN="$(DEMO_WORKER_TOKEN)" \
	DEMO_DIR="$(DEMO_DIR)" \
	./scripts/demo-ui.sh logs

# --- build / run ---------------------------------------------------------
build:
	go build ./...

run:
	go run ./cmd/coordinator

test:
	go test ./...

# Needs a running PostgreSQL; the spec forbids mocks for these guarantees.
#   make test-integration TEST_DATABASE_URL='postgres://...'
test-integration:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -tags=integration ./... -v

vet:
	go vet ./...

# One command that runs everything: unit tests + vet + lint, then brings up the
# stack and runs the integration suite and the end-to-end smoke test.
# Needs Docker. Hand this to a reviewer.
check: vet lint
	go test -race ./...
	$(CHECK_COMPOSE) up -d --build
	@echo "waiting for the coordinator to be ready..."
	@attempt=0; until curl -fsS "$(CHECK_HOST)/health" >/dev/null; do \
		attempt=$$((attempt + 1)); \
		if [ $$attempt -ge 30 ]; then $(CHECK_COMPOSE) logs coordinator; exit 1; fi; \
		sleep 1; \
	done
	TEST_DATABASE_URL="$(CHECK_DATABASE_URL)" \
		go test -tags=integration ./internal/storage/postgres/ -v
	HOST="$(CHECK_HOST)" TOKEN="$(CHECK_TOKEN)" ./scripts/smoke.sh
	@echo "\nall checks passed ✓"

# Runs golangci-lint without installing it system-wide. Install it for speed:
#   pacman -S golangci-lint   (Arch)
LINT_VERSION := v2.12.2
lint:
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run --build-tags=integration ./... \
		|| go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION) run --build-tags=integration ./...

tidy:
	go mod tidy

# --- migrations ----------------------------------------------------------
# Requires the golang-migrate CLI:
#   go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
# DATABASE_URL must be set, e.g.:
#   export DATABASE_URL='postgres://scimesh:scimesh@localhost:5432/scimesh?sslmode=disable'
migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

# --- docker --------------------------------------------------------------
# `up` starts Postgres, applies migrations, then launches the coordinator.
up:
	docker compose up -d --build

down:
	docker compose down

# Also drops the database volume — use when the schema is beyond repair.
down-clean:
	docker compose down -v

logs:
	docker compose logs -f coordinator

ps:
	docker compose ps

rebuild:
	docker compose up -d --build --force-recreate coordinator

psql:
	docker compose exec postgres psql -U scimesh -d scimesh

# --- api ------------------------------------------------------------------
# Exercises every endpoint against a running coordinator; exits non-zero on the
# first unexpected status. See also api/requests.http for clicking through them
# one at a time in an editor.
smoke:
	./scripts/smoke.sh
