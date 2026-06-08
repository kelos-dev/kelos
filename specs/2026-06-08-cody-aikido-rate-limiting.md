# Cody Aikido Rate Limiting

Status: draft
Date: 2026-06-08

## Context

The first Aikido security workflow run failed during deterministic discovery before it could create any agent sessions:

```text
discovering items: fetching Aikido issue group 24589148: Aikido proxy returned status 429:
{"error":"You have reached the maximum number of calls per minute. (20 calls per minute)"}
```

The current implementation has two important properties:

- `cody-tools` proxies read-only Aikido API requests through `/aikido`, but does not rate limit, retry, or bound upstream request duration.
- `internal/source/aikido.go` can make a large number of Aikido calls in one discovery cycle, especially when using issue export, repository filters, issue type filters, and per-group detail enrichment.

Live Cody agents will also call Aikido through the same proxy while they babysit remediation. Rate limiting only the scheduled discovery path would not protect the shared Aikido account limit. The limit needs to sit at the proxy boundary used by both deterministic controllers and live agents.

## Goals

- Keep Aikido calls under the observed account limit of 20 calls per minute.
- Make rate limiting shared across deterministic discovery and live agent requests.
- Respect agent and spawner timeouts; do not let requests queue indefinitely.
- Handle upstream `429` responses gracefully with `Retry-After` support.
- Avoid failing an entire discovery run just because optional issue-group enrichment hit the rate limit.
- Produce enough logs to prove whether Cody is waiting locally, backing off from Aikido, or failing because no wait budget remains.

## Non-Goals

- Do not build a distributed rate limiter in the first implementation.
- Do not add write access to Aikido.
- Do not redesign the Aikido workflow lifecycle or session model.
- Do not persist Aikido rate-limit state across pod restarts.

## Proposed Design

### 1. Add a shared limiter in `cody-tools`

All outbound Aikido API calls made through `/aikido` should pass through one in-memory limiter inside `cody-tools`.

Default behavior:

```text
CODY_TOOLS_AIKIDO_RATE_LIMIT_PER_MINUTE=18
CODY_TOOLS_AIKIDO_RATE_LIMIT_BURST=1
CODY_TOOLS_AIKIDO_MAX_WAIT=20s
CODY_TOOLS_AIKIDO_UPSTREAM_TIMEOUT=30s
```

Rationale:

- A local limit of 18/minute leaves headroom under Aikido's observed 20/minute cap.
- Burst `1` prevents a discovery run from consuming the whole minute immediately.
- `MAX_WAIT` bounds live-agent user experience. If the proxy cannot acquire a token within the wait budget, it should return a controlled local rate-limit response instead of hanging.
- `UPSTREAM_TIMEOUT` bounds non-streaming Aikido GETs even when callers do not set an HTTP client timeout.

The limiter should be context-aware:

- Wait for a token using `r.Context()`.
- Also enforce the configured max wait by deriving a shorter context when the request context has no deadline.
- If the request context is canceled while waiting, return without calling Aikido.

Local failure response:

```text
HTTP 429
Retry-After: <seconds until next likely token>
```

Body should be concise and agent-friendly:

```json
{
  "error": "cody Aikido rate limit reached",
  "retryAfterSeconds": 12
}
```

### 2. Add upstream 429 cooldown handling

If Aikido itself returns `429`, `cody-tools` should parse `Retry-After` when present and apply a shared cooldown before allowing more upstream calls.

Behavior:

- Parse `Retry-After` as seconds or HTTP date.
- If absent or invalid, use a conservative fallback such as 60 seconds.
- Cap cooldown at a configurable value, for example `CODY_TOOLS_AIKIDO_RETRY_AFTER_CAP=90s`.
- Retry the same request at most once if the request still has enough local wait budget.
- If there is not enough budget, return the upstream `429` or a local `429` with `Retry-After`.

This prevents a single caller from repeatedly hitting the upstream API while the account is already throttled.

### 3. Classify proxy callers with headers

Discovery and live agent calls should share the same global bucket, but they have different patience levels. Add optional headers that Cody-owned clients can send:

```text
X-Cody-Aikido-Client: discovery | agent
X-Cody-Aikido-Budget-Seconds: <integer>
X-Cody-TaskSpawner: <name>
X-Cody-AgentSession: <name>
```

Initial use:

- `internal/source/aikido.go` sends `X-Cody-Aikido-Client: discovery`.
- Live agent calls can omit headers and default to `agent`.
- If `X-Cody-Aikido-Budget-Seconds` is present, `cody-tools` uses the lower of that budget and the configured max wait.

The first implementation does not need separate token buckets by class. Caller class is primarily for wait budget and logs.

### 4. Make Aikido discovery less expensive

Discovery should avoid spending most of the API quota on optional enrichment.

Current expensive pattern:

1. Fetch code repositories.
2. Fetch issue export pages for each repository/status/type filter.
3. Group rows by issue group.
4. Fetch `/issues/groups/{groupID}` for every discovered group.

v1 changes:

- Treat issue-export rows as the primary source of discovered work.
- Only fetch issue-group detail when required fields are missing from export rows.
- Add a small enrichment cap, for example `CODY_AIKIDO_DISCOVERY_MAX_GROUP_DETAIL_CALLS=5`.
- If enrichment hits local or upstream rate limit, keep the discovered issue group using export-row data and add a warning to logs.

This makes the deterministic controller resilient: it can still spawn useful sessions from export data instead of failing the whole cycle while trying to enrich details.

### 5. Bound discovery call volume and duration

Add discovery-side controls in `internal/source/aikido.go`:

```text
CODY_AIKIDO_DISCOVERY_TIMEOUT=3m
CODY_AIKIDO_DISCOVERY_MAX_API_CALLS=15
CODY_AIKIDO_DISCOVERY_MAX_GROUP_DETAIL_CALLS=5
```

Behavior:

- Every Aikido discovery cycle runs under a context deadline.
- Every proxied request increments a local discovery call counter.
- When the call budget is exhausted, discovery stops fetching optional pages/enrichment.
- If no issue groups were discovered and the limit was hit, return a transient error so the next schedule/manual run can retry.
- If some issue groups were discovered, return those partial results and log that discovery was truncated by budget/rate limit.

These values should be configurable because the right budget depends on how many repositories and issue types are enabled.

### 6. Agent behavior on local rate limit

The security agent prompt should include a short instruction:

- If the Aikido proxy returns `429` with `Retry-After`, do not loop or retry aggressively.
- Record that Aikido is rate limited, wait only if the retry window fits within the current turn budget, otherwise let the next heartbeat continue.

This keeps long-running security sessions compatible with the proxy limiter. A live agent should not consume its full turn repeatedly polling Aikido while the account is cooling down.

## Runtime Flow

### Scheduled discovery

1. Aikido TaskSpawner CronJob starts.
2. `internal/source/aikido.go` creates a discovery context with a bounded timeout.
3. It sends read-only requests through `cody-tools /aikido` with `X-Cody-Aikido-Client: discovery`.
4. `cody-tools` waits for the shared Aikido token bucket.
5. If Aikido returns data, discovery creates one session/turn per selected issue group.
6. If Aikido returns `429`, `cody-tools` applies shared cooldown and either retries once or returns a controlled `429`.
7. Discovery returns partial work when possible instead of failing after optional enrichment.

### Live agent heartbeat

1. Existing AgentSession heartbeat creates another AgentTurn.
2. The agent checks PR/build/Aikido state through the same `/aikido` proxy.
3. `cody-tools` applies the shared limiter.
4. If the proxy returns `429`, the agent records the rate-limit status and waits for a later heartbeat unless the retry window is small enough.

## Observability

Add structured logs in `cody-tools` for every Aikido proxy request:

```text
aikido.client_class
aikido.path
aikido.wait_ms
aikido.cooldown_until
aikido.status
aikido.retry_after_seconds
aikido.local_rate_limited
aikido.upstream_rate_limited
aikido.taskspawner
aikido.agentsession
```

Add structured logs in discovery:

```text
aikido.discovery.api_calls
aikido.discovery.groups_discovered
aikido.discovery.groups_enriched
aikido.discovery.truncated
aikido.discovery.rate_limited
```

This is enough for the first rollout. Metrics can follow once the behavior is proven.

## Implementation Plan

### Kelos

1. Add an Aikido limiter type in `cmd/cody-tools`.
2. Wire limiter config from environment.
3. Apply limiter and upstream timeout inside `forwardAikido` / `doAikido`.
4. Add upstream `429` parsing and shared cooldown.
5. Add proxy tests for local wait, context cancellation, upstream `429`, and retry-after behavior.
6. Add discovery headers, discovery timeout, and call-budget controls in `internal/source/aikido.go`.
7. Make issue-group detail enrichment optional and capped.
8. Add source tests for truncated discovery and 429-tolerant enrichment.
9. Update the Aikido security prompt to avoid aggressive retry loops on `429`.

### GitOps

1. Add `cody-tools` environment variables for rate-limit defaults.
2. Keep `cody-tools` at one replica for v1, or explicitly document that multiple replicas multiply the effective Aikido request rate.
3. Roll out the new `cody-tools` and `kelos-spawner` images.

### Skills

1. Update the Aikido security skill/prompt with the proxy `429` behavior.
2. Keep the existing session lifecycle and heartbeat setup unchanged.

## Tests

### Unit Tests

- `cody-tools` waits for a token before calling upstream.
- `cody-tools` returns local `429` when max wait expires.
- `cody-tools` respects request context cancellation while waiting.
- Upstream `429` with `Retry-After` creates cooldown.
- Upstream `429` retries at most once when budget allows.
- OAuth `401` refresh behavior still works with the limiter enabled.
- Aikido discovery sends Cody classification headers.
- Aikido discovery can return issue groups from export rows without group-detail calls.
- Aikido discovery truncates optional enrichment when call budget is exhausted.

### Manual Test

1. Deploy `cody-tools` with a very low temporary limit, for example 2/minute.
2. Trigger the Aikido TaskSpawner manually.
3. Confirm discovery logs show bounded waiting/truncation instead of crashing on raw upstream 429.
4. Confirm created agent turns do not repeatedly hammer Aikido when the proxy returns local 429.
5. Restore production defaults.

## Rollout

1. Merge Kelos changes.
2. Build and push updated images.
3. Package and push the Kelos chart if environment variables or chart defaults change.
4. Merge GitOps chart/image bump.
5. Merge Skills prompt update.
6. Trigger one manual Aikido run and monitor `cody-tools`, spawner job logs, and Slack output.

## Open Questions

- Should partial discovery create sessions immediately, or should discovery fail and retry if it cannot enrich issue groups?
- Is 18/minute the right default, or should it be lower to leave room for humans and other automations using the same Aikido account?
- Are there multiple `cody-tools` replicas in any environment? If yes, v1 in-memory limiting is not enough to enforce the global account cap.
- Should we add a CRD field for Aikido discovery budgets, or keep them as deployment-level environment variables?
- Should the proxy return `429` or `503` for local throttling? `429` is semantically clearer for agents, but `503` may better signal transient platform capacity.
