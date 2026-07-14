# Contributing to slowlane

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22, plus bash and curl for the smoke script; nothing else.

```bash
git clone https://github.com/JaydenCJ/slowlane && cd slowlane
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, validates and plans the example
scenario, then runs a real proxy and the built-in echo upstream on
loopback ephemeral ports and asserts the injected faults land exactly
where the plan predicted; it must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (89 deterministic tests, loopback only).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (`scenario`, `engine`, `seeded`, `match` never touch the
   network — only `proxy` and the `run`/`echo` commands do).

## Ground rules

- Keep dependencies at zero — slowlane is standard library only, and
  that is a feature. Adding one needs strong justification in the PR.
- No telemetry, no outbound calls except to the user-configured upstream.
  Examples and tests bind `127.0.0.1`.
- **The seeded hash is a format contract.** Anything that changes a draw
  for an existing coordinate (see `docs/determinism.md`) is a breaking
  change: it needs a scenario `version` bump and updated golden tests —
  never change the pinned values to make a test pass.
- Scenario parsing stays strict: new fields must be validated with a
  located error message, and rejected when unknown.
- Code comments and doc comments are written in English.

## Reporting bugs

Include the output of `slowlane version`, the scenario file (redacted if
needed), the command you ran, and — for determinism bugs — the relevant
`slowlane plan` slice next to the observed `run` log lines, since those
two are supposed to be identical.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
