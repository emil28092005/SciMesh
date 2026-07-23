#!/usr/bin/env bash
#
# End-to-end smoke test against a running coordinator.
#
#   ./scripts/smoke.sh                        # localhost:8080, token from .env
#   HOST=http://1.2.3.4:8080 TOKEN=x ./scripts/smoke.sh
#
# Exits non-zero on the first unexpected status, so it is usable in CI.

set -uo pipefail

HOST="${HOST:-http://localhost:8080}"
TOKEN="${TOKEN:-$(grep -s '^WORKER_AUTH_TOKEN=' .env | cut -d= -f2- || echo change-me)}"

pass=0
fail=0

# check <label> <expected-status> <curl args...>
check() {
	local label="$1" want="$2"
	shift 2
	local body status
	body=$(curl -sS -w '\n%{http_code}' "$@" 2>&1)
	status=$(printf '%s' "$body" | tail -n1)

	if [[ "$status" == "$want" ]]; then
		printf '  \033[32m✓\033[0m %-46s %s\n' "$label" "$status"
		pass=$((pass + 1))
	else
		printf '  \033[31m✗\033[0m %-46s got %s, want %s\n' "$label" "$status" "$want"
		printf '      %s\n' "$(printf '%s' "$body" | head -n-1)"
		fail=$((fail + 1))
	fi
}

json() { printf '%s' "$1" | head -n-1; }

auth=(-H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json')

echo "coordinator: ${HOST}"
echo

echo "health & auth"
check "GET /health"                     200 "${HOST}/health"
check "claim without a token → 401"     401 -X POST "${HOST}/tasks/claim" \
	-H 'Content-Type: application/json' -d '{"worker_id":"w1"}'

echo
echo "worker registry"
check "register worker"                 201 -X POST "${HOST}/workers/register" "${auth[@]}" \
	-d '{"name":"smoke-worker","capabilities":["similarity_search"],"cpu_count":4,"memory_mb":8192}'
check "register without capabilities → 400" 400 -X POST "${HOST}/workers/register" "${auth[@]}" \
	-d '{"name":"bad"}'

echo
echo "job lifecycle"
job=$(curl -sS "${auth[@]}" -X POST "${HOST}/jobs" -d '{
  "workload":"similarity_search","input_uri":"s3://chembl","parameters":{"top_k":10},
  "chunks":[{"chunk_index":0,"input_uri":"s3://c0","input_sha256":"aaa"},
            {"chunk_index":1,"input_uri":"s3://c1","input_sha256":"bbb"}]}')
job_id=$(printf '%s' "$job" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])' 2>/dev/null)

if [[ -z "${job_id:-}" ]]; then
	echo "  ✗ could not create a job: $job"
	exit 1
fi
printf '  \033[32m✓\033[0m %-46s %s\n' "POST /jobs" "$job_id"
pass=$((pass + 1))

# The database may hold pending tasks from earlier runs, so claim until we have
# both of *our* chunks rather than assuming the queue starts empty. The attempt
# number comes from the response too: a task requeued by an expired lease is
# handed out with attempt 2 or 3, and hard-coding 1 would fail the lease check.
declare -A our_chunks
task_id=""
attempt=""
for _ in $(seq 1 40); do
	claim=$(curl -sS "${auth[@]}" -X POST "${HOST}/tasks/claim" -d '{"worker_id":"w1"}')
	[[ -z "$claim" ]] && break # 204: queue drained

	read -r c_job c_task c_chunk c_attempt < <(printf '%s' "$claim" |
		python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["job_id"],d["task_id"],d["chunk_index"],d["attempt"])' 2>/dev/null)
	[[ "$c_job" != "$job_id" ]] && continue # someone else's leftover task

	our_chunks["$c_chunk"]=1
	if [[ -z "$task_id" ]]; then
		task_id="$c_task"
		attempt="$c_attempt"
	fi
	[[ "${#our_chunks[@]}" -eq 2 ]] && break
done

if [[ "${#our_chunks[@]}" -eq 2 ]]; then
	printf '  \033[32m✓\033[0m %-46s chunks %s\n' "POST /tasks/claim × 2 (distinct)" "${!our_chunks[*]}"
	pass=$((pass + 1))
else
	printf '  \033[31m✗\033[0m %-46s got %d distinct chunks, want 2\n' "claim" "${#our_chunks[@]}"
	fail=$((fail + 1))
	exit 1
fi

check "heartbeat"                          200 -X POST "${HOST}/tasks/${task_id}/heartbeat" "${auth[@]}" \
	-d "{\"worker_id\":\"w1\",\"attempt\":${attempt}}"

# --- artifacts + result (uploads happen while the task is still leased) ---
bearer=(-H "Authorization: Bearer ${TOKEN}")

# upload <filename> -> prints the artifact_id
upload() {
	curl -sS -X PUT "${HOST}/tasks/${task_id}/artifacts/$1" "${bearer[@]}" \
		-H 'Content-Type: text/csv' -H 'X-Worker-ID: w1' -H "X-Task-Attempt: ${attempt}" \
		--data-binary $'query,match,score\nA,B,0.9\n' |
		python3 -c 'import json,sys;print(json.load(sys.stdin)["artifact_id"])' 2>/dev/null
}

check "upload artifact"                    200 -X PUT "${HOST}/tasks/${task_id}/artifacts/result.csv" "${bearer[@]}" \
	-H 'Content-Type: text/csv' -H 'X-Worker-ID: w1' -H "X-Task-Attempt: ${attempt}" \
	--data-binary $'query,match,score\nA,B,0.9\n'
check "foreign worker upload → 409"        409 -X PUT "${HOST}/tasks/${task_id}/artifacts/x.csv" "${bearer[@]}" \
	-H 'Content-Type: text/csv' -H 'X-Worker-ID: impostor' -H "X-Task-Attempt: ${attempt}" \
	--data-binary 'x'

# Two result artifacts, uploaded now while the lease is held: one to complete
# with, a second to prove a different manifest is rejected after completion.
art_id=$(upload primary.csv)
art_id2=$(upload secondary.csv)
check "download artifact"                  200 "${HOST}/artifacts/${art_id}/download" "${bearer[@]}"

check "foreign worker submits → 409"       409 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"impostor\",\"attempt\":${attempt},\"result\":{\"artifact_id\":\"${art_id}\"}}"
check "submit result"                      200 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"w1\",\"attempt\":${attempt},\"result\":{\"artifact_id\":\"${art_id}\"}}"
check "replay same result → idempotent"    200 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"w1\",\"attempt\":${attempt},\"result\":{\"artifact_id\":\"${art_id}\"}}"
check "different result → 409"             409 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"w1\",\"attempt\":${attempt},\"result\":{\"artifact_id\":\"${art_id2}\"}}"
check "GET /jobs/{id}"                     200 "${HOST}/jobs/${job_id}" "${auth[@]}"

echo
echo "input validation"
check "malformed uuid → 400"               400 -X POST "${HOST}/tasks/not-a-uuid/result" "${auth[@]}" \
	-d '{"worker_id":"w1","attempt":1,"result":{"artifact_id":"00000000-0000-0000-0000-000000000000"}}'
# Note: Go's encoding/json matches field names case-insensitively, so
# "worker_ID" would be accepted as "worker_id". Only a genuinely unknown key
# trips DisallowUnknownFields.
check "unknown json field → 400"           400 -X POST "${HOST}/tasks/claim" "${auth[@]}" \
	-d '{"worker_id":"w1","totally_unknown":1}'
check "unknown job → 404"                  404 "${HOST}/jobs/00000000-0000-0000-0000-000000000000" "${auth[@]}"

echo
echo "dataset upload → chunking"
# Upload a 5-row TSV split at 2 rows/shard → expect 3 shard tasks. The text
# fields precede the file part, which the coordinator streams.
up=$(curl -sS "${bearer[@]}" -X POST "${HOST}/jobs/upload" \
	-F 'workload=similarity_search' \
	-F 'parameters={"top_k":10}' \
	-F 'chunk_rows=2' \
	-F 'file=@-;filename=chembl.tsv;type=text/tab-separated-values' <<'TSV'
id	smiles
A	CC
B	CCC
C	CCCC
D	CCCCC
E	CCCCCC
TSV
)
up_job=$(printf '%s' "$up" | python3 -c 'import json,sys;print(json.load(sys.stdin)["job_id"])' 2>/dev/null)
up_count=$(printf '%s' "$up" | python3 -c 'import json,sys;print(json.load(sys.stdin)["task_count"])' 2>/dev/null)

if [[ "$up_count" == "3" ]]; then
	printf '  \033[32m✓\033[0m %-46s task_count=3\n' "POST /jobs/upload (5 rows / 2)"
	pass=$((pass + 1))
else
	printf '  \033[31m✗\033[0m %-46s got task_count=%s, want 3\n' "POST /jobs/upload" "${up_count:-?}"
	printf '      %s\n' "$up"
	fail=$((fail + 1))
fi

# Claim one of this job's shard tasks and pull its input shard from the coordinator.
up_input=""
for _ in $(seq 1 30); do
	c=$(curl -sS "${bearer[@]}" -H 'Content-Type: application/json' -X POST "${HOST}/tasks/claim" \
		-d '{"worker_id":"up-w","capabilities":["similarity_search"]}')
	[[ -z "$c" ]] && break
	cj=$(printf '%s' "$c" | python3 -c 'import json,sys;print(json.load(sys.stdin)["job_id"])' 2>/dev/null)
	[[ "$cj" != "$up_job" ]] && continue
	up_input=$(printf '%s' "$c" | python3 -c 'import json,sys;print(json.load(sys.stdin)["input"]["uri"])' 2>/dev/null)
	break
done

if [[ "$up_input" == /tasks/*/input ]]; then
	printf '  \033[32m✓\033[0m %-46s %s\n' "claim → input.uri points at coordinator" "$up_input"
	pass=$((pass + 1))
else
	printf '  \033[31m✗\033[0m %-46s got %q\n' "claim shard input.uri" "$up_input"
	fail=$((fail + 1))
fi
check "download shard input"               200 "${HOST}${up_input}" "${bearer[@]}"

echo
curl -sS "${HOST}/jobs/${job_id}" "${auth[@]}"
echo
printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
