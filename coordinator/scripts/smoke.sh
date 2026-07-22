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
check "foreign worker submits → 409"       409 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"impostor\",\"attempt\":${attempt},\"result_uri\":\"s3://x\",\"result_sha256\":\"x\"}"
check "submit result"                      200 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"w1\",\"attempt\":${attempt},\"result_uri\":\"s3://r0\",\"result_sha256\":\"rrr\"}"
check "replay same result → idempotent"    200 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"w1\",\"attempt\":${attempt},\"result_uri\":\"s3://r0\",\"result_sha256\":\"rrr\"}"
check "different result → 409"             409 -X POST "${HOST}/tasks/${task_id}/result" "${auth[@]}" \
	-d "{\"worker_id\":\"w1\",\"attempt\":${attempt},\"result_uri\":\"s3://other\",\"result_sha256\":\"zzz\"}"
check "GET /jobs/{id}"                     200 "${HOST}/jobs/${job_id}" "${auth[@]}"

echo
echo "input validation"
check "malformed uuid → 400"               400 -X POST "${HOST}/tasks/not-a-uuid/result" "${auth[@]}" \
	-d '{"worker_id":"w1","attempt":1,"result_uri":"s3://x","result_sha256":"x"}'
# Note: Go's encoding/json matches field names case-insensitively, so
# "worker_ID" would be accepted as "worker_id". Only a genuinely unknown key
# trips DisallowUnknownFields.
check "unknown json field → 400"           400 -X POST "${HOST}/tasks/claim" "${auth[@]}" \
	-d '{"worker_id":"w1","totally_unknown":1}'
check "unknown job → 404"                  404 "${HOST}/jobs/00000000-0000-0000-0000-000000000000" "${auth[@]}"

echo
curl -sS "${HOST}/jobs/${job_id}" "${auth[@]}"
echo
printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ "$fail" -eq 0 ]]
