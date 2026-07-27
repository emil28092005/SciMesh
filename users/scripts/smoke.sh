#!/usr/bin/env bash
# End-to-end smoke test against a running userservice. Exercises the full auth
# flow and exits non-zero on the first unexpected status.
#
#   HOST=http://localhost:8081 ./scripts/smoke.sh
set -euo pipefail

HOST="${HOST:-http://localhost:8081}"
EMAIL="smoke-$(date +%s)-$RANDOM@example.com"
PASSWORD="password123"

pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1" >&2; exit 1; }

# expect METHOD PATH WANT_STATUS [JSON_BODY] [BEARER]
# Prints the response body to stdout so callers can parse it.
expect() {
	local method="$1" path="$2" want="$3" body="${4:-}" token="${5:-}"
	local args=(-s -o /tmp/smoke_body -w '%{http_code}' -X "$method" "$HOST$path")
	[ -n "$body" ] && args+=(-H 'Content-Type: application/json' -d "$body")
	[ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
	local code
	code="$(curl "${args[@]}")"
	if [ "$code" != "$want" ]; then
		printf 'body: %s\n' "$(cat /tmp/smoke_body)" >&2
		fail "$method $path -> $code (want $want)"
	fi
	pass "$method $path -> $code"
	cat /tmp/smoke_body
}

echo "smoke: $HOST  (user $EMAIL)"

expect GET  /health  200 >/dev/null

expect POST /register 201 "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" >/dev/null
expect POST /register 409 "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" >/dev/null
expect POST /register 400 "{\"email\":\"$EMAIL\",\"password\":\"short\"}"     >/dev/null

# Login and capture the token (extract the "token" JSON string field).
login_body="$(expect POST /login 200 "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")"
TOKEN="$(printf '%s' "$login_body" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
[ -n "$TOKEN" ] || fail "login returned no token"
pass "captured token"

expect POST /login 401 "{\"email\":\"$EMAIL\",\"password\":\"wrongpass1\"}" >/dev/null

expect GET /me 200 "" "$TOKEN"        >/dev/null
expect GET /me 401 ""                 >/dev/null
expect GET /me 401 "" "not-a-token"   >/dev/null

echo "smoke: all checks passed ✓"
