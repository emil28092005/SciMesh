#!/usr/bin/env bash
#
# End-to-end check: two Python workers process separate coordinator shards.
#
# Requires Docker, curl, python3, and an installed scimesh-worker (normally
# from this repository's .venv). The test uses its own Compose project, ports,
# volumes, and temporary worker directories, leaving a developer stack alone.

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
COORDINATOR_DIR="$ROOT_DIR/coordinator"
COMPOSE_PROJECT=${COMPOSE_PROJECT:-scimesh-two-worker-smoke}
COORDINATOR_PORT=${COORDINATOR_PORT:-18081}
POSTGRES_PORT=${POSTGRES_PORT:-55434}
HOST="http://127.0.0.1:${COORDINATOR_PORT}"
TOKEN=${SCIMESH_SMOKE_TOKEN:-two-worker-smoke-token}
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/scimesh-two-worker-smoke.XXXXXX")
WORKER_ONE_PID=""
WORKER_TWO_PID=""
WORKER_PYTHON=${SCIMESH_WORKER_PYTHON:-"$ROOT_DIR/.venv/bin/python"}

cleanup() {
    local exit_code=$?
    if [[ "$exit_code" -ne 0 ]]; then
        printf '\nTwo-worker smoke failed; worker logs follow.\n' >&2
        sed -n '1,200p' "$WORK_DIR/worker-a.log" >&2 || true
        sed -n '1,200p' "$WORK_DIR/worker-b.log" >&2 || true
        (
            cd "$COORDINATOR_DIR"
            POSTGRES_PORT="$POSTGRES_PORT" COORDINATOR_PORT="$COORDINATOR_PORT" \
                docker compose -p "$COMPOSE_PROJECT" logs coordinator >&2 || true
        )
    fi
    if [[ -n "$WORKER_ONE_PID" ]]; then kill "$WORKER_ONE_PID" 2>/dev/null || true; fi
    if [[ -n "$WORKER_TWO_PID" ]]; then kill "$WORKER_TWO_PID" 2>/dev/null || true; fi
    if [[ -n "$WORKER_ONE_PID" ]]; then wait "$WORKER_ONE_PID" 2>/dev/null || true; fi
    if [[ -n "$WORKER_TWO_PID" ]]; then wait "$WORKER_TWO_PID" 2>/dev/null || true; fi
    (
        cd "$COORDINATOR_DIR"
        POSTGRES_PORT="$POSTGRES_PORT" COORDINATOR_PORT="$COORDINATOR_PORT" \
            docker compose -p "$COMPOSE_PROJECT" down -v --remove-orphans >/dev/null 2>&1 || true
    )
    rm -rf "$WORK_DIR"
    exit "$exit_code"
}
trap cleanup EXIT INT TERM

require() {
    command -v "$1" >/dev/null || {
        printf 'missing required command: %s\n' "$1" >&2
        exit 2
    }
}

for command in docker curl python3; do require "$command"; done
[[ -x "$WORKER_PYTHON" ]] || {
    printf 'worker Python is not executable: %s\n' "$WORKER_PYTHON" >&2
    printf 'Set SCIMESH_WORKER_PYTHON to a Python environment with SciMesh and RDKit.\n' >&2
    exit 2
}

printf 'Starting isolated coordinator on %s (project %s)\n' "$HOST" "$COMPOSE_PROJECT"
(
    cd "$COORDINATOR_DIR"
    POSTGRES_PORT="$POSTGRES_PORT" COORDINATOR_PORT="$COORDINATOR_PORT" \
        WORKER_AUTH_TOKEN="$TOKEN" UI_AUTH_TOKEN= \
        docker compose -p "$COMPOSE_PROJECT" up -d --build
)

for _ in $(seq 1 45); do
    if curl -fsS "$HOST/health" >/dev/null; then break; fi
    sleep 1
done
curl -fsS "$HOST/health" >/dev/null || {
    printf 'coordinator did not become healthy\n' >&2
    exit 1
}

start_worker() {
    local worker_name=$1
    local worker_dir=$2
    SCIMESH_COORDINATOR_URL="$HOST" \
        SCIMESH_BEARER_TOKEN="$TOKEN" \
        SCIMESH_WORKER_NAME="$worker_name" \
        SCIMESH_POLL_INTERVAL=0.2 \
        "$WORKER_PYTHON" -m scimesh.worker.cli --work-dir "$worker_dir" --max-tasks 2 >"$worker_dir.log" 2>&1 &
    STARTED_WORKER_PID=$!
}

start_worker two-worker-smoke-a "$WORK_DIR/worker-a"
WORKER_ONE_PID=$STARTED_WORKER_PID
start_worker two-worker-smoke-b "$WORK_DIR/worker-b"
WORKER_TWO_PID=$STARTED_WORKER_PID

for _ in $(seq 1 30); do
    registered=$(docker compose -p "$COMPOSE_PROJECT" -f "$COORDINATOR_DIR/docker-compose.yml" \
        exec -T postgres psql -U scimesh -d scimesh -Atc "SELECT count(*) FROM workers")
    [[ "$registered" == "2" ]] && break
    sleep 1
done
[[ "${registered:-0}" == "2" ]] || {
    printf 'workers did not register; logs follow\n' >&2
    sed -n '1,160p' "$WORK_DIR/worker-a.log" >&2 || true
    sed -n '1,160p' "$WORK_DIR/worker-b.log" >&2 || true
    exit 1
}

DATASET="$WORK_DIR/fixture.tsv"
printf '%s\n' \
    $'chembl_id\tcanonical_smiles' \
    $'TEST001\tCC' $'TEST002\tCCC' $'TEST003\tCCCC' $'TEST004\tCCCO' $'TEST005\tCCN' \
    $'TEST006\tCCCl' $'TEST007\tCCBr' $'TEST008\tCCF' $'TEST009\tCC=O' $'TEST010\tCC#N' \
    $'TEST011\tCO' $'TEST012\tCOC' $'TEST013\tCOCC' $'TEST014\tCN' $'TEST015\tCNC' \
    $'TEST016\tO=C=O' $'TEST017\tC1CC1' $'TEST018\tc1ccccc1' $'TEST019\tCC(C)O' $'TEST020\tCC(C)N' \
    >"$DATASET"

response=$(curl -fsS -H "Authorization: Bearer $TOKEN" -X POST "$HOST/jobs/upload" \
    -F 'workload=similarity-search' \
    -F 'parameters={"query_smiles":"CCO","top_k":5,"progress_every":0}' \
    -F 'chunk_rows=5' \
    -F 'max_rows=20' \
    -F "file=@${DATASET};type=text/tab-separated-values")
job_id=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["job_id"])')
task_count=$(printf '%s' "$response" | python3 -c 'import json,sys; print(json.load(sys.stdin)["task_count"])')
[[ "$task_count" == "4" ]] || {
    printf 'expected four shards, got response: %s\n' "$response" >&2
    exit 1
}

printf 'Submitted job %s with four shards\n' "$job_id"
for _ in $(seq 1 90); do
    job=$(curl -fsS -H "Authorization: Bearer $TOKEN" "$HOST/jobs/$job_id")
    status=$(printf '%s' "$job" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')
    [[ "$status" == "completed" || "$status" == "failed" || "$status" == "cancelled" ]] && break
    sleep 1
done

printf '%s' "$job" | python3 -c '
import json, sys
job = json.load(sys.stdin)
assert job["status"] == "completed", job
assert job["total"] == 4, job
assert job["completed"] == 4, job
assert job["failed"] == 0, job
'

task_check=$(docker compose -p "$COMPOSE_PROJECT" -f "$COORDINATOR_DIR/docker-compose.yml" \
    exec -T postgres psql -U scimesh -d scimesh -Atc \
    "SELECT count(*) FROM tasks WHERE job_id = '$job_id'::uuid AND status = 'completed' AND attempt = 1 AND result_artifact_id IS NOT NULL")
[[ "$task_check" == "4" ]] || {
    printf 'expected four first-attempt tasks with coordinator artifacts, got %s\n' "$task_check" >&2
    exit 1
}

worker_one_results=$(find "$WORK_DIR/worker-a" -name result.csv -type f | wc -l | tr -d ' ')
worker_two_results=$(find "$WORK_DIR/worker-b" -name result.csv -type f | wc -l | tr -d ' ')
[[ "$worker_one_results" -ge 1 && "$worker_two_results" -ge 1 ]] || {
    printf 'both workers must process at least one shard (a=%s, b=%s)\n' \
        "$worker_one_results" "$worker_two_results" >&2
    exit 1
}

printf 'PASS: 4/4 shards completed; worker-a=%s, worker-b=%s\n' \
    "$worker_one_results" "$worker_two_results"
