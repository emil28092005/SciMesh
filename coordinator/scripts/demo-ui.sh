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
ui_token=${DEMO_UI_TOKEN:-demo-ui-secret}
worker_token=${DEMO_WORKER_TOKEN:-demo-worker-token}
workers=${DEMO_WORKERS:-2}
demo_dir=${DEMO_DIR:-.demo}
case "$demo_dir" in
  /*) ;;
  *) demo_dir="$coordinator_dir/$demo_dir" ;;
esac
worker_bin=${SCIMESH_WORKER_BIN:-"$repo_dir/.venv/bin/scimesh-worker"}
pid_file="$demo_dir/workers.pids"
logs_dir="$demo_dir/logs"

compose() {
  POSTGRES_PORT="$postgres_port" \
  COORDINATOR_PORT="$coordinator_port" \
  UI_AUTH_TOKEN="$ui_token" \
  WORKER_AUTH_TOKEN="$worker_token" \
  docker compose -p "$project" -f "$coordinator_dir/docker-compose.yml" "$@"
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

wait_for_workers() {
  local attempt=0 registered overview
  until false; do
    overview=$(curl --fail --silent --show-error --user "operator:$ui_token" \
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

  UI:       http://localhost:$coordinator_port/ui
  Username: operator
  Password: $ui_token
  Workers:  $workers local reference workers

Upload a small ChEMBL TSV through “New similarity search”, then watch the job
page update. Worker logs are in $logs_dir. Stop everything with:

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
  logs)
    echo "Worker logs: $logs_dir"
    compose logs -f coordinator
    ;;
  *)
    echo "Usage: $0 {start|stop|logs}" >&2
    exit 2
    ;;
esac
