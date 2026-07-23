.PHONY: build run test test-integration vet lint tidy check migrate-up migrate-down up down down-clean logs ps rebuild psql smoke swagger

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
	docker compose up -d --build
	@echo "waiting for the coordinator to be ready..."
	@sleep 6
	TEST_DATABASE_URL='postgres://scimesh:scimesh@localhost:5432/scimesh?sslmode=disable' \
		go test -tags=integration ./internal/storage/postgres/ -v
	./scripts/smoke.sh
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

# --- docs ----------------------------------------------------------------
# Serve the OpenAPI spec in Swagger UI at http://localhost:8081. Mounts the
# spec and a fixed initializer so it loads locally with no internet.
swagger:
	docker run --rm -p 8081:8080 \
		-v $(realpath ../docs)/openapi.yaml:/usr/share/nginx/html/openapi.yaml:ro \
		-v $(realpath ../docs)/swagger-initializer.js:/usr/share/nginx/html/swagger-initializer.js:ro \
		swaggerapi/swagger-ui
