#!/usr/bin/env bash
# ci-gate.sh — the slowlane CI pattern, minimally.
#
# 1. validate the scenario file;
# 2. print the plan (the deterministic contract for this run);
# 3. start the built-in echo upstream and the proxy on ephemeral ports;
# 4. drive requests through and assert the faults landed exactly where
#    the plan said.
#
# Everything binds 127.0.0.1 and exits non-zero on the first mismatch, so
# this file can gate a pipeline unchanged.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCENARIO="$ROOT/examples/flaky-upstream.json"
WORKDIR="$(mktemp -d)"
PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  done
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

wait_for_line() {
  for _ in $(seq 1 50); do
    grep -q "$2" "$1" 2>/dev/null && return 0
    sleep 0.1
  done
  echo "gate: timed out waiting for '$2'" >&2
  exit 1
}

BIN="$WORKDIR/slowlane"
(cd "$ROOT" && go build -o "$BIN" ./cmd/slowlane)

echo "== 1. validate =="
"$BIN" check "$SCENARIO"

echo
echo "== 2. the contract for this run =="
"$BIN" plan --requests 12 "$SCENARIO"

echo
echo "== 3. run proxy + echo upstream =="
"$BIN" echo --listen 127.0.0.1:0 > "$WORKDIR/echo.log" 2>&1 &
PIDS+=($!)
wait_for_line "$WORKDIR/echo.log" "echo listening on "
ECHO_ADDR="$(sed -n 's/^echo listening on //p' "$WORKDIR/echo.log")"

sed -e 's|127.0.0.1:18080|127.0.0.1:0|' \
    -e "s|http://127.0.0.1:18081|http://$ECHO_ADDR|" \
    "$SCENARIO" > "$WORKDIR/scenario.json"
"$BIN" run "$WORKDIR/scenario.json" > "$WORKDIR/run.log" 2>&1 &
PIDS+=($!)
wait_for_line "$WORKDIR/run.log" "proxy api listening on "
ADDR="$(sed -n 's/^proxy api listening on \([^ ]*\) ->.*/\1/p' "$WORKDIR/run.log")"

echo
echo "== 4. assert the client saw the planned faults =="
# Per the plan above (seed 42): requests 1-10 pass, request 11 is a 503.
for i in $(seq 1 10); do
  status="$(curl -s -o /dev/null -w '%{http_code}' "http://$ADDR/users/$i")"
  if [ "$status" != "200" ]; then
    echo "gate: request $i expected 200, got $status" >&2
    exit 1
  fi
done
status="$(curl -s -o /dev/null -w '%{http_code}' "http://$ADDR/users/11")"
if [ "$status" != "503" ]; then
  echo "gate: request 11 expected the planned 503, got $status" >&2
  exit 1
fi
echo "requests 1-10 passed, request 11 was the planned 503"
echo "GATE OK"
