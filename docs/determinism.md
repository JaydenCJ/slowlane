# How slowlane stays deterministic

slowlane's core promise: **`slowlane plan` output and live proxy behavior
are the same computation.** This document specifies that computation
precisely. The algorithm is part of the scenario file format contract —
changing it changes what every existing scenario does, and therefore
requires a format `version` bump.

## No RNG state

Traditional fault injectors draw from a stateful RNG: whether request N
gets a fault depends on how many draws happened before it, which makes
behavior dependent on request interleaving and impossible to predict.

slowlane has no RNG state. Every probabilistic decision is a **pure hash
of a coordinate**:

```
(seed, proxy name, rule name, purpose, request counter)
```

- `seed` — the scenario's `seed` field (uint64, default 0).
- `proxy name`, `rule name` — from the scenario file.
- `purpose` — a fixed label: `"rate"` for should-this-rule-fire draws,
  `"jitter"` for extra-latency draws.
- `request counter` — the per-proxy counter, starting at 1.

Consequences worth designing tests around:

- The decision for request N is independent of every other request.
- Adding, removing, or reordering *other* rules never changes a rule's
  decisions (draws are keyed by name, not position).
- Renaming a rule reshuffles that rule's draws only.
- Restarting the proxy and replaying the same request sequence reproduces
  the same faults, byte for byte.

## The hash, exactly

Textual coordinate parts are folded with 64-bit FNV-1a, joined by NUL
separators (so `("ab","c")` and `("a","bc")` cannot collide):

```
label = fnv1a64(proxy + "\x00" + rule + "\x00" + purpose)
```

The final word mixes seed, label, and counter through SplitMix64's
output permutation (`mix`):

```
mix(x):
    x += 0x9e3779b97f4a7c15
    x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
    x = (x ^ (x >> 27)) * 0x94d049bb133111eb
    return x ^ (x >> 31)

word = mix(seed XOR mix(label XOR mix(counter)))
```

A uniform float in `[0, 1)` takes the top 53 bits (the standard exact-
double construction):

```
draw = float64(word >> 11) / 2^53
```

- **Rate**: a rule with `rate: r` fires iff `draw < r` (purpose `"rate"`).
- **Jitter**: extra delay is `floor(draw * (jitter_ms + 1))` milliseconds,
  clamped to `jitter_ms` (purpose `"jitter"`).

Everything is integer and IEEE-754 double arithmetic with no
platform-dependent operations, so results are identical across
architectures and Go versions.

## Pinned reference values

These are frozen in `internal/seeded/seeded_test.go`; a failing golden
test means a format-breaking change:

| seed | proxy | rule | purpose | counter | value |
|---|---|---|---|---|---|
| 42 | `api` | `brownout` | `rate` | 1 | `0.77062817083341006` |
| 42 | `api` | `brownout` | `rate` | 2 | `0.36894825553352428` |
| 42 | `api` | `brownout` | `rate` | 3 | `0.83571265025676944` |
| 0 | `p` | `r` | `jitter` | 10 | `0.017442580445358624` |

So with seed 42, a `rate: 0.5` rule named `brownout` on proxy `api` would
skip request 1 (0.77 ≥ 0.5), fire on request 2, and skip request 3. In
`examples/flaky-upstream.json` that same rule is windowed to requests
11–30, where the identical construction fires on counters 11 and 13 and
skips 12 — the schedule the README quickstart prints with `plan` and the
smoke test asserts against a live proxy.

## What is *not* deterministic

Honesty section. slowlane pins **which** faults happen at **which
request counters**. It does not pin:

- wall-clock timing — injected delays are real sleeps with OS-scheduler
  precision, and throttles pace writes in 100 ms ticks;
- counter assignment under concurrency — parallel in-flight requests
  receive counters in arrival order, so a racing client can observe
  faults in a different order than a serial one. For byte-exact CI runs,
  drive requests serially (as `examples/ci-gate.sh` and the smoke test do).
