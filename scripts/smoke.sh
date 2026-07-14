#!/usr/bin/env bash
# End-to-end smoke test for slowlane: builds the binary, validates and
# plans the example scenario, then runs a real proxy + the built-in echo
# upstream on loopback and asserts the injected faults land exactly where
# the plan said they would. Loopback only, idempotent, finishes in seconds.
# This script plus 'go test ./...' is the whole verification story — the
# repository intentionally ships no CI.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

# wait_for_line FILE PATTERN: poll (max ~5 s) until a log line appears.
wait_for_line() {
  for _ in $(seq 1 50); do
    grep -q "$2" "$1" 2>/dev/null && return 0
    sleep 0.1
  done
  fail "timed out waiting for '$2' in $1"
}

command -v curl >/dev/null || fail "curl is required for the smoke test"
BIN="$WORKDIR/slowlane"

echo "[1/9] build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/slowlane) || fail "go build failed"

echo "[2/9] --version matches the manifest version"
[ "$("$BIN" --version)" = "slowlane 0.1.0" ] || fail "--version mismatch"

echo "[3/9] check accepts the example scenario"
"$BIN" check "$ROOT/examples/flaky-upstream.json" | grep -q "OK (version 1, seed 42" \
  || fail "check did not accept examples/flaky-upstream.json"

echo "[4/9] check rejects a typoed field with exit 1"
printf '{"version": 1, "sead": 9, "proxies": []}' > "$WORKDIR/broken.json"
set +e
"$BIN" check "$WORKDIR/broken.json" >/dev/null 2>"$WORKDIR/check.err"
CODE=$?
set -e
[ "$CODE" -eq 1 ] || fail "broken scenario should exit 1, got $CODE"
grep -q "sead" "$WORKDIR/check.err" || fail "typo not named in the error"

echo "[5/9] plan is deterministic and shows the pinned schedule"
"$BIN" plan --requests 30 "$ROOT/examples/flaky-upstream.json" > "$WORKDIR/plan1.txt"
"$BIN" plan --requests 30 "$ROOT/examples/flaky-upstream.json" > "$WORKDIR/plan2.txt"
cmp -s "$WORKDIR/plan1.txt" "$WORKDIR/plan2.txt" || fail "plan output not byte-identical"
grep -qE '^\s+11\s+503 \(brownout\)$' "$WORKDIR/plan1.txt" || fail "request 11 should plan a 503"
grep -qE '^\s+10\s+pass$' "$WORKDIR/plan1.txt" || fail "request 10 should plan a pass"

echo "[6/9] start the echo upstream and a proxy on ephemeral ports"
"$BIN" echo --listen 127.0.0.1:0 > "$WORKDIR/echo.log" 2>&1 &
PIDS+=($!)
wait_for_line "$WORKDIR/echo.log" "echo listening on "
ECHO_ADDR="$(sed -n 's/^echo listening on //p' "$WORKDIR/echo.log")"

sed -e 's|127.0.0.1:18080|127.0.0.1:0|' \
    -e "s|http://127.0.0.1:18081|http://$ECHO_ADDR|" \
    "$ROOT/examples/flaky-upstream.json" > "$WORKDIR/scenario.json"
"$BIN" run "$WORKDIR/scenario.json" > "$WORKDIR/run.log" 2>&1 &
RUN_PID=$!
PIDS+=($!)
wait_for_line "$WORKDIR/run.log" "proxy api listening on "
PROXY_ADDR="$(sed -n 's/^proxy api listening on \([^ ]*\) ->.*/\1/p' "$WORKDIR/run.log")"

echo "[7/9] the live proxy follows the plan exactly"
for i in $(seq 1 10); do
  STATUS="$(curl -s -o /dev/null -w '%{http_code}' "http://$PROXY_ADDR/users/$i")"
  [ "$STATUS" = "200" ] || fail "request $i should pass, got $STATUS"
done
curl -si "http://$PROXY_ADDR/users/11" > "$WORKDIR/resp11.txt"
grep -q "^HTTP/1.1 503" "$WORKDIR/resp11.txt" || fail "request 11 should be an injected 503"
grep -qi "^X-Slowlane-Injected: brownout" "$WORKDIR/resp11.txt" || fail "503 should name the rule"
grep -q "injected outage" "$WORKDIR/resp11.txt" || fail "503 should carry the scenario body"

echo "[8/9] passing responses cross the echo upstream, injected ones do not"
curl -si "http://$PROXY_ADDR/users/12" > "$WORKDIR/resp12.txt"
grep -q "^HTTP/1.1 200" "$WORKDIR/resp12.txt" || fail "request 12 should pass, per the plan"
grep -qi "^X-Slowlane-Echo: 1" "$WORKDIR/resp12.txt" || fail "passing response should come from echo"
grep -qi "^X-Slowlane-Echo:" "$WORKDIR/resp11.txt" && fail "injected response must not reach echo"

echo "[9/9] clean shutdown on SIGINT, with a full request log"
kill -INT "$RUN_PID"
wait "$RUN_PID" || fail "run should exit 0 on SIGINT"
grep -q "api #11 GET /users/11 \[503 (brownout)\] -> 503" "$WORKDIR/run.log" \
  || fail "run log missing the injected request line"
grep -q "slowlane: shut down" "$WORKDIR/run.log" || fail "shutdown line missing"

echo "SMOKE OK"
