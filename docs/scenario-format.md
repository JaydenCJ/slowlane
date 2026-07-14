# The slowlane scenario format

A scenario is a single JSON file. It is the *entire* configuration
surface: there is no admin API, no runtime toggles, no client library.
`slowlane check` validates a file; parsing is strict — **unknown fields
are rejected**, so a typoed `jitters_ms` fails loudly instead of silently
never firing.

## Top level

| Key | Required | Type | Meaning |
|---|---|---|---|
| `version` | yes | int | Format version. Must be `1`. |
| `seed` | no (default `0`) | uint64 | Keys every probabilistic decision. Same seed + same request sequence ⇒ identical faults. |
| `proxies` | yes, ≥1 | array | Listeners to open. |

## Proxy

| Key | Required | Type | Meaning |
|---|---|---|---|
| `name` | yes | string | Identifier (lowercase letters, digits, hyphens; unique). Appears in logs, headers, and hash coordinates. |
| `listen` | yes | string | `host:port` to bind. Use `127.0.0.1` to stay loopback-only; port `0` picks a free port (printed at startup). |
| `upstream` | yes | string | `http://` or `https://` origin to forward to. A base path is allowed; query/fragment are not. |
| `rules` | no | array | Fault rules, evaluated top to bottom per request. |

## Rule

| Key | Required | Type | Meaning |
|---|---|---|---|
| `name` | yes | string | Identifier (same alphabet as proxy names; unique per proxy). Part of the hash coordinate — renaming a rule reshuffles *its* draws only. |
| `match` | no | object | Request selector; absent = all requests. |
| `window` | no | object | Request-counter selector; absent = always. |
| `rate` | no (default `1`) | float 0–1 | Probability the rule fires when match+window pass, decided deterministically from the seed. |
| `fault` | yes | object | What the rule does. |

### `match`

| Key | Type | Meaning |
|---|---|---|
| `methods` | array of string | HTTP methods, case-insensitive (`["GET", "delete"]`). |
| `path` | string | Segment glob. `*` matches within one segment (`/users/*`, `/api/v*/items`); `**` spans segments (`/api/**`). Must start with `/`. |
| `headers` | object | Header name → required exact value. Names are case-insensitive; any occurrence of a multi-valued header may carry the value. |

All present criteria must hold. An empty `match` block is a validation
error — drop it instead.

### `window`

Counters are **per proxy**, start at 1, and increment on every request.
They are the deterministic alternative to wall-clock phases: "requests
11–20" replays identically in every CI run; "the second ten seconds"
does not.

| Key | Default | Meaning |
|---|---|---|
| `from` | `1` | First counter the rule applies to. |
| `to` | `0` (open) | Last counter the rule applies to. |
| `every` | `1` | Fire on every N-th request counted from `from` (`from`, `from+every`, …). |

### `fault`

| Key | Type | Meaning |
|---|---|---|
| `delay_ms` | int ≥0 | Fixed injected latency, before anything else. |
| `jitter_ms` | int ≥0 | Extra latency in `[0, jitter_ms]` ms, drawn per request from the seed. |
| `status` | int 100–599 | Short-circuit with this HTTP status; the upstream is never contacted. Response carries `X-Slowlane-Injected: <rule>`. |
| `body` | string | Plain-text body for an injected `status` (requires `status`). |
| `drop` | bool | Close the client connection abruptly, before any response bytes. |
| `throttle_bps` | int ≥1 | Cap the upstream response body copy rate, bytes/second. |

Constraints: a fault must set at least one field; `status` and `drop` are
mutually exclusive; `throttle_bps` shapes the *upstream* response and
cannot combine with either terminal.

## Composition laws

For each request, rules are evaluated in file order:

1. **Delays accumulate** — every matching delay/jitter rule adds latency.
2. **The first matching terminal (`status` or `drop`) wins** and stops
   evaluation; rules below it never fire for that request.
3. **The first matching throttle wins**; later throttles are ignored.

`slowlane plan` prints the composed result per request, so you never have
to simulate these laws in your head.

## Response annotations

slowlane annotates the responses it touches, so test assertions never
have to guess whether a fault came from slowlane or from a genuinely
broken upstream:

| Header | On | Value |
|---|---|---|
| `X-Slowlane-Request` | every response | The per-proxy request counter. |
| `X-Slowlane-Injected` | injected `status` responses | The rule name. |
| `X-Slowlane-Delay` | delayed responses | Total injected latency, e.g. `230ms`. |
| `X-Slowlane-Throttled` | throttled responses | The rule name. |
| `X-Slowlane-Upstream-Error` | slowlane-generated 502s | `1` (upstream unreachable). |

## Full example

See [`examples/flaky-upstream.json`](../examples/flaky-upstream.json) —
warm-up passes, a 50 % 503 brownout for requests 11–30, a dropped
connection every 10th request from 31 on, and seeded write latency —
and [`examples/slow-network.json`](../examples/slow-network.json) for a
latency + throughput profile.
