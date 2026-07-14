# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-12

### Added

- Declarative scenario file format (version 1): proxies with rules
  combining `match` (methods, segment-glob paths, exact headers),
  request-counter `window` phases (`from`/`to`/`every`), a seeded `rate`,
  and faults — `delay_ms` + `jitter_ms`, injected `status` with `body`,
  connection `drop`, and `throttle_bps` response shaping.
- Strict validation: unknown fields rejected, every finding reported in
  one pass with a JSON-path-like location (`proxies[0].rules[2].rate`).
- Stateless seeded determinism: every rate/jitter decision is a pure
  SplitMix64 hash of (seed, proxy, rule, purpose, counter), pinned by
  golden tests and specified in `docs/determinism.md`.
- `run` subcommand: HTTP reverse proxy applying the composed decision per
  request (delays accumulate, first terminal wins, first throttle wins),
  with text/JSON request logs, `X-Slowlane-*` response annotations,
  hop-by-hop header handling, `X-Forwarded-For`, ephemeral-port support,
  and graceful SIGINT shutdown.
- `plan` subcommand: prints the exact per-request fault schedule for a
  request shape before any proxy starts, in text or JSON — identical to
  live behavior by construction.
- `check` subcommand: scenario validation and summary, text or JSON,
  exit 1 with findings for CI gates.
- `echo` subcommand: built-in deterministic upstream so scenarios can be
  exercised with the slowlane binary alone.
- Runnable examples (`examples/flaky-upstream.json`,
  `examples/slow-network.json`, `examples/ci-gate.sh`) and format
  references (`docs/scenario-format.md`, `docs/determinism.md`).
- 89 deterministic offline tests (unit + in-process CLI + loopback proxy
  integration) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/slowlane/releases/tag/v0.1.0
