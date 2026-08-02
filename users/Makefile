.DEFAULT_GOAL := help

.PHONY: help build run test test-integration vet lint tidy check migrate-up migrate-down up down down-clean logs ps psql smoke

# `check` uses its own Compose project and host ports so it never touches a
# developer's local PostgreSQL or the normal `make up` stack.
CHECK_PROJECT ?= scimesh-users-check
CHECK_POSTGRES_PORT ?= 55433
CHECK_USERSERVICE_PORT ?= 18081
CHECK_HOST ?= http://localhost:$(CHECK_USERSERVICE_PORT)
CHECK_DATABASE_URL ?= postgres://scimesh:scimesh@localhost:$(CHECK_POSTGRES_PORT)/scimesh_users?sslmode=disable
CHECK_COMPOSE = POSTGRES_PORT=$(CHECK_POSTGRES_PORT) USERSERVICE_PORT=$(CHECK_USERSERVICE_PORT) docker compose -p $(CHECK_PROJECT)

help:
	@printf '%s\n' \
	  'SciMesh userservice commands:' \
	  '  make up / make down     Start or stop the userservice stack (Postgres + migrate + service).' \
	  '  make check              One command: vet, lint, race tests, integration, smoke (needs Docker).' \
	  '  make test / make vet    Run Go verification.' \
	  '  make smoke              Exercise the live API against a running service.'

# --- build / run ---------------------------------------------------------
build:
	mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o bin/userservice ./cmd/userservice

run:
	go run ./cmd/userservice

test:
	go test ./...

# Needs a running PostgreSQL with the migrations applied:
#   make test-integration TEST_DATABASE_URL='postgres://...'
test-integration:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -tags=integration ./... -v

vet:
	go vet ./...

# Runs golangci-lint without installing it system-wide.
LINT_VERSION := v2.12.2
lint:
	@command -v golangci-lint >/dev/null 2>&1 \
		&& golangci-lint run --build-tags=integration ./... \
		|| go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(LINT_VERSION) run --build-tags=integration ./...

tidy:
	go mod tidy

# One command for a reviewer: unit tests + vet + lint, then the stack up and the
# integration suite and the end-to-end smoke test. Needs Docker.
check: vet lint
	go test -race ./...
	$(CHECK_COMPOSE) up -d --build
	@echo "waiting for the userservice to be ready..."
	@attempt=0; until curl -fsS "$(CHECK_HOST)/health" >/dev/null; do \
		attempt=$$((attempt + 1)); \
		if [ $$attempt -ge 30 ]; then $(CHECK_COMPOSE) logs userservice; exit 1; fi; \
		sleep 1; \
	done
	TEST_DATABASE_URL="$(CHECK_DATABASE_URL)" \
		go test -tags=integration ./internal/storage/postgres/ -v
	HOST="$(CHECK_HOST)" ./scripts/smoke.sh
	@echo "\nall checks passed ✓"

# --- migrations ----------------------------------------------------------
# Requires the golang-migrate CLI and DATABASE_URL, e.g.:
#   export DATABASE_URL='postgres://scimesh:scimesh@localhost:5433/scimesh_users?sslmode=disable'
migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

# --- docker --------------------------------------------------------------
up:
	docker compose up -d --build

down:
	docker compose down

down-clean:
	docker compose down -v

logs:
	docker compose logs -f userservice

ps:
	docker compose ps

psql:
	docker compose exec postgres psql -U scimesh -d scimesh_users

# --- api ------------------------------------------------------------------
smoke:
	./scripts/smoke.sh
