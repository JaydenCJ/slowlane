# slowlane examples

Two scenario files and one script, all runnable from a fresh clone with
nothing but Go and curl.

## Scenarios

- **`flaky-upstream.json`** — the README scenario: POST/PUT writes get
  150–250 ms of seeded latency, requests 11–30 hit a 50 % 503 brownout,
  and from request 31 every 10th connection is dropped. Seed 42.
- **`slow-network.json`** — an everything-is-slow profile: 80–120 ms of
  latency on every request, plus a 16 KiB/s throttle on `/assets/**`
  downloads. Seed 7.

Try one against the built-in echo upstream (no real backend needed):

```bash
go build -o slowlane ./cmd/slowlane
./slowlane echo --listen 127.0.0.1:18081 &   # the upstream
./slowlane run examples/flaky-upstream.json  # the proxy, on :18080
# elsewhere: curl -i http://127.0.0.1:18080/users/7
```

Preview what either scenario will do before running it:

```bash
./slowlane plan --requests 30 examples/flaky-upstream.json
./slowlane plan --requests 5 --method POST --path /api/v1/items examples/flaky-upstream.json
```

## `ci-gate.sh`

The CI pattern in one file: validate the scenario, capture the plan as the
contract, run the proxy against the echo upstream, and assert the client
saw exactly the planned faults. Exits non-zero on any mismatch, so it can
gate a pipeline as-is:

```bash
bash examples/ci-gate.sh
```
