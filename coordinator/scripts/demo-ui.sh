#!/usr/bin/env bash
# Start a self-contained local UI demo with coordinator, PostgreSQL, and local
# reference workers. It is intentionally for a developer's machine only.
set -euo pipefail

action=${1:-start}
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
coordinator_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
repo_dir=$(CDPATH= cd -- "$coordinator_dir/.." && pwd)

project=${DEMO_PROJECT:-scimesh-demo}
postgres_port=${DEMO_POSTGRES_PORT:-55432}
coordinator_port=${DEMO_COORDINATOR_PORT:-18080}
userservice_port=${DEMO_USERSERVICE_PORT:-18081}
prometheus_port=${DEMO_PROMETHEUS_PORT:-19090}
grafana_port=${DEMO_GRAFANA_PORT:-13000}
ui_token=${DEMO_UI_TOKEN:-demo-ui-secret}
worker_token=${DEMO_WORKER_TOKEN:-demo-worker-token}
# Shared HS256 secret; the coordinator verifies userservice tokens with it. Must
# be at least 32 bytes (both services refuse a shorter one).
jwt_secret=${DEMO_JWT_SECRET:-demo-jwt-secret-please-change-me-0123456789}
# The first admin, seeded into the userservice on first boot.
admin_email=${DEMO_ADMIN_EMAIL:-root@scimesh.local}
admin_password=${DEMO_ADMIN_PASSWORD:-rootpassword}
workers=${DEMO_WORKERS:-2}
demo_dir=${DEMO_DIR:-.demo}
case "$demo_dir" in
  /*) ;;
  *) demo_dir="$coordinator_dir/$demo_dir" ;;
esac
worker_bin=${SCIMESH_WORKER_BIN:-"$repo_dir/.venv/bin/scimesh-worker"}
pid_file="$demo_dir/workers.pids"
logs_dir="$demo_dir/logs"

# The built MkDocs site is mounted into the demo coordinator so the UI can
# serve it at /ui/docs/. When site/ is missing (make docs), the docs route
# shows a build hint instead.
docs_compose_file="$demo_dir/docker-compose.docs.yml"
docs_compose_files=""

prepare_docs_override() {
  mkdir -p "$demo_dir"
  if [[ -d "$repo_dir/site" ]]; then
    cat > "$docs_compose_file" <<DOCS_OVERRIDE_EOF
services:
  coordinator:
    volumes:
      - $repo_dir/site:/site:ro
    environment:
      SCIMESH_DOCS_DIR: /site
DOCS_OVERRIDE_EOF
    docs_compose_files="-f $docs_compose_file"
  else
    docs_compose_files=""
  fi
}

compose() {
  POSTGRES_PORT="$postgres_port" \
  COORDINATOR_PORT="$coordinator_port" \
  USERSERVICE_PORT="$userservice_port" \
  PROMETHEUS_PORT="$prometheus_port" \
  GRAFANA_PORT="$grafana_port" \
  UI_AUTH_TOKEN="$ui_token" \
  WORKER_AUTH_TOKEN="$worker_token" \
  JWT_SECRET="$jwt_secret" \
  BOOTSTRAP_ADMIN_EMAIL="$admin_email" \
  BOOTSTRAP_ADMIN_PASSWORD="$admin_password" \
  docker compose -p "$project" \
    -f "$coordinator_dir/docker-compose.yml" \
    -f "$coordinator_dir/docker-compose.users.yml" \
    -f "$coordinator_dir/docker-compose.monitoring.yml" \
    $docs_compose_files "$@"
}

stop_workers() {
  [[ -f "$pid_file" ]] || return 0
  while IFS= read -r pid; do
    [[ "$pid" =~ ^[0-9]+$ ]] || continue
    command_line=$(ps -p "$pid" -o args= 2>/dev/null || true)
    # Never kill a recycled PID or a worker launched outside this demo.
    if [[ "$command_line" == *"$demo_dir/worker-"* ]]; then
      kill "$pid" 2>/dev/null || true
    fi
  done < "$pid_file"
  rm -f "$pid_file"
}

wait_for_coordinator() {
  local attempt=0
  until curl --fail --silent --show-error "http://localhost:$coordinator_port/health" >/dev/null; do
    attempt=$((attempt + 1))
    if (( attempt >= 45 )); then
      echo "Coordinator did not become ready. Recent logs:" >&2
      compose logs --tail=80 coordinator >&2 || true
      exit 1
    fi
    sleep 1
  done
}

wait_for_userservice() {
  local attempt=0
  until curl --fail --silent --show-error "http://localhost:$userservice_port/health" >/dev/null; do
    attempt=$((attempt + 1))
    if (( attempt >= 45 )); then
      echo "Userservice did not become ready. Recent logs:" >&2
      compose logs --tail=80 userservice >&2 || true
      exit 1
    fi
    sleep 1
  done
}

wait_for_workers() {
  local attempt=0 registered overview cookie="$demo_dir/session.cookies"
  # The dashboard API is behind a userservice session now, not basic auth. Log in
  # as the seeded admin (who sees every worker) to obtain a session cookie.
  curl --fail --silent -c "$cookie" \
    --data-urlencode "email=$admin_email" \
    --data-urlencode "password=$admin_password" \
    "http://localhost:$coordinator_port/ui/login" >/dev/null 2>&1 || true
  until false; do
    overview=$(curl --fail --silent --show-error -b "$cookie" \
      "http://localhost:$coordinator_port/ui/api/overview" 2>/dev/null || true)
    # The overview contains no jobs at demo startup, so every `id` belongs to
    # a registered worker. Avoid adding jq just for this local helper.
    registered=$(printf '%s' "$overview" | grep -o '"id"' | wc -l | tr -d ' ' || true)
    if [[ "$registered" =~ ^[0-9]+$ ]] && (( registered >= workers )); then
      return 0
    fi
    attempt=$((attempt + 1))
    if (( attempt >= 20 )); then
      echo "Only $registered of $workers demo workers registered. Recent worker logs:" >&2
      tail -n 40 "$logs_dir"/worker-*.log 2>/dev/null >&2 || true
      exit 1
    fi
    sleep 1
  done
}

start() {
  prepare_docs_override
  if ! [[ "$workers" =~ ^[1-9][0-9]*$ ]]; then
    echo "DEMO_WORKERS must be a positive integer (got $workers)." >&2
    exit 2
  fi
  if [[ ! -x "$worker_bin" ]]; then
    echo "Reference worker not found: $worker_bin" >&2
    echo "Create it first from the repository root: python3 -m venv .venv && .venv/bin/pip install -e '.[dev]'" >&2
    exit 2
  fi
  command -v docker >/dev/null || { echo "Docker is required." >&2; exit 2; }
  command -v curl >/dev/null || { echo "curl is required." >&2; exit 2; }

  stop_workers
  mkdir -p "$logs_dir"
  compose up -d --build
  echo "Waiting for the coordinator on http://localhost:$coordinator_port ..."
  wait_for_coordinator
  echo "Waiting for the userservice on http://localhost:$userservice_port ..."
  wait_for_userservice

  : > "$pid_file"
  for index in $(seq 1 "$workers"); do
    work_dir="$demo_dir/worker-$index"
    mkdir -p "$work_dir"
    SCIMESH_COORDINATOR_URL="http://localhost:$coordinator_port" \
    SCIMESH_BEARER_TOKEN="$worker_token" \
    "$worker_bin" \
      --worker-name "demo-worker-$index" \
      --work-dir "$work_dir" \
      >"$logs_dir/worker-$index.log" 2>&1 &
    echo "$!" >> "$pid_file"
  done
  wait_for_workers

  cat <<EOF

SciMesh manual demo is ready.

  UI:          http://localhost:$coordinator_port/ui   (shows a login page)
  Admin login: $admin_email / $admin_password
  Userservice: http://localhost:$userservice_port
  Grafana:     http://localhost:$grafana_port   (anonymous view; admin/${GRAFANA_PASSWORD:-admin} to edit)
  Prometheus:  http://localhost:$prometheus_port
  Workers:     $workers local reference workers

Sign in with the admin above, or register a new account from the login page.
The admin sees every job; a plain user sees only their own. Upload a small
ChEMBL TSV through “New similarity search”, then watch the job page update.
Worker logs are in $logs_dir. Stop everything with:

  make demo-down
EOF
}

case "$action" in
  start) start ;;
  stop)
    stop_workers
    compose down
    echo "SciMesh manual demo stopped."
    ;;
  reset)
    # Like stop, but also drops the data volumes so the next start is pristine
    # (empty Postgres, no leftover workers/jobs/tasks, no cached artifacts).
    stop_workers
    compose down -v
    echo "SciMesh manual demo stopped and data volumes removed."
    ;;
  logs)
    echo "Worker logs: $logs_dir"
    compose logs -f coordinator
    ;;
  *)
    echo "Usage: $0 {start|stop|reset|logs}" >&2
    exit 2
    ;;
esac
