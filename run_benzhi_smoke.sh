#!/usr/bin/env bash
# Deterministic local smoke test for the shield-tunnel service.
#
# Builds the binary, starts the service against a fresh temporary SQLite
# database, probes the real HTTP health/API behavior, then tears everything
# down. No external network access; fails fast on any error.
set -euo pipefail

PORT="${SHIELD_SMOKE_PORT:-18080}"
ADDR="127.0.0.1:${PORT}"
BASE="http://${ADDR}"

WORKDIR_TMP="$(mktemp -d)"
BIN="${WORKDIR_TMP}/shieldtunnel"
DB="${WORKDIR_TMP}/smoke.db"

cleanup() {
  if [[ -n "${PID:-}" ]]; then
    kill "${PID}" 2>/dev/null || true
    wait "${PID}" 2>/dev/null || true
  fi
  rm -rf "${WORKDIR_TMP}"
}
trap cleanup EXIT

echo "building service binary..."
go build -o "${BIN}" ./cmd/server

echo "starting service on ${ADDR}..."
SHIELD_DB="${DB}" SHIELD_ADDR="${ADDR}" "${BIN}" &
PID=$!

# Wait for the health endpoint to come up.
health=""
for _ in $(seq 1 100); do
  if health="$(curl -sf "${BASE}/api/health" 2>/dev/null)"; then
    break
  fi
  sleep 0.05
done

if [[ -z "${health}" ]]; then
  echo "error: service did not become healthy" >&2
  exit 1
fi

# Assert the health envelope without piping curl into grep (avoids SIGPIPE).
if [[ "${health}" != *'"code":"ok"'* ]]; then
  echo "error: unexpected health response: ${health}" >&2
  exit 1
fi
echo "health: ${health}"

# The frontend page is served by the Go process.
index="$(curl -sf "${BASE}/")"
if [[ "${index}" != *"盾构隧道"* ]]; then
  echo "error: frontend page missing expected content" >&2
  exit 1
fi
echo "frontend page served (${#index} bytes)"

# Exercise a real API endpoint: an invalid lock returns a stable error envelope.
resp="$(curl -s -w $'\n%{http_code}' \
  -X POST "${BASE}/api/rings" \
  -H 'Content-Type: application/json' \
  -d '{"operation_id":"smoke-1","section":"澄江路—望塔站","ring_no":1,"ring_type":"通用楔形环","generation":1,"rule_summary":"stale","logical_time":0,"segments":[],"joints":[]}')"
err_code="${resp##*$'\n'}"
err_content="${resp%$'\n'*}"

if [[ "${err_code}" != "422" ]]; then
  echo "error: expected 422 for stale lock, got ${err_code}" >&2
  exit 1
fi
if [[ "${err_content}" != *'"code":"stale_summary"'* ]]; then
  echo "error: unexpected error envelope: ${err_content}" >&2
  exit 1
fi
echo "invalid lock rejected with stable envelope"

echo "smoke test passed"
