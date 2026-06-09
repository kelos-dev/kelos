# Cody Aikido Security Runtime And Fallbacks

Status: implementation note
Date: 2026-06-09

This note traces the current Aikido security babysitter runtime across Kelos,
`cody-tools`, k8s-platform GitOps, and the skills repo. It focuses on the
runtime path and every fallback/degraded behavior currently present in code or
configuration.

Relevant implementation areas:

- `cmd/cody-tools/main.go`
- `internal/source/aikido.go`
- `cmd/kelos-spawner/main.go`
- `internal/controller/taskspawner_deployment_builder.go`
- `internal/reporting/slack_turn.go`
- `skills/cody/security/taskspawner-cody-aikido-security-main.yaml`
- `skills/cody/security/agentconfig-cody-aikido-security-main.yaml`
- `k8s-platform-gitops/non-prod/kelos/deployment-cody-tools.yaml`
- `k8s-platform-gitops/non-prod/kelos/helmrelease-patch.yaml`

## Intended Runtime

1. `cody-aikido-security-main` is a scheduled Aikido `TaskSpawner`.
2. The TaskSpawner is configured in the skills repo with:
   - schedule: `0 7 * * *`
   - branch: `main`
   - repositories: currently `template-nestjs-be`
   - statuses: `open`
   - severities: `critical`, `high`
   - issue types: `open_source`, `docker_container`, `sast`
   - session mode enabled
   - scope template:
     `aikido/{{.Branch}}/{{.RunDate}}/{{ index .Metadata "aikido.kelos.dev/issue-group-id" }}`
   - max age: `25h`
   - idle timeout: `25h`
   - max queued turns: `1`
3. The Kelos controller renders this TaskSpawner as a Kubernetes `CronJob`.
4. The CronJob runs `kelos-spawner` in `--one-shot` mode.
5. The CronJob has:
   - `concurrencyPolicy: Forbid`
   - `startingDeadlineSeconds` from the TaskSpawner, currently `300`
   - `backoffLimit: 0`
   - successful job history limit `3`
   - failed job history limit `1`
6. The one-shot spawner builds an `AikidoSource`.
7. Because the TaskSpawner has Aikido session mode enabled and no priority
   labels, the spawner uses streaming Aikido discovery.
8. Aikido API calls go through `cody-tools`, not directly to Aikido.
9. The Aikido source:
   - fetches active code repositories for the configured branch;
   - fetches `/issues/export` rows by repository, status, and issue type;
   - filters rows by repository ID, status, issue type, and severity;
   - dedupes rows by issue row ID;
   - groups rows by Aikido issue group ID;
   - sorts group IDs deterministically;
   - emits one Kelos `WorkItem` per group.
10. The spawner creates or reuses an `AgentSession` per emitted issue group.
11. The spawner creates one `AgentTurn` for that issue group and run minute.
12. The Codex App Server runs the turn using the configured security
    AgentConfig.
13. The agent uses the controller-provided Aikido snapshot plus GitHub,
    repository, package, image, CI, and build evidence.
14. The agent is explicitly instructed not to call Aikido.
15. Slack reporting uses the `session-summary-root` layout:
    - one session-owned root message;
    - concise additive summary;
    - latest active work;
    - full terminal details in the root thread.

## cody-tools Runtime

`cody-tools` hosts the internal Aikido proxy at `/aikido`. The Aikido security
controller calls this proxy.

Configured in k8s-platform GitOps:

- `CODY_TOOLS_AIKIDO_API_BASE_URL=https://app.aikido.dev/api/public/v1`
- `CODY_TOOLS_AIKIDO_CLIENT_CREDENTIALS` from `cody-aikido-api`
- `CODY_TOOLS_AIKIDO_RATE_LIMIT_PER_MINUTE=18`
- `CODY_TOOLS_AIKIDO_RATE_LIMIT_BURST=1`
- `CODY_TOOLS_AIKIDO_MAX_WAIT=20s`
- `CODY_TOOLS_AIKIDO_UPSTREAM_TIMEOUT=30s`
- `CODY_TOOLS_AIKIDO_RETRY_AFTER_CAP=90s`

The spawner/source sends Cody metadata headers:

- `X-Cody-Aikido-Client: discovery`
- `X-Cody-Aikido-Budget-Seconds`
- `X-Cody-TaskSpawner`
- `X-Cody-Aikido-Run-Date`

`cody-tools` logs these fields for Aikido proxy requests:

- client class
- TaskSpawner name
- run date
- request path
- HTTP status
- local rate-limit wait
- retry-after seconds
- local rate-limited flag
- upstream rate-limited flag
- cooldown-until timestamp when present

## cody-tools Fallbacks

### Configuration Defaults

- Missing `CODY_TOOLS_ADDR` defaults to the standard listen address.
- Missing Aikido API base URL defaults to the built-in public Aikido API URL.
- Missing Aikido token URL defaults to the built-in token endpoint.
- Missing Aikido rate settings default to code defaults.
- Invalid or non-positive rate settings fail startup instead of falling back.

### Aikido Authentication

- Preferred path is OAuth client credentials.
- If OAuth client credentials are not configured, cody-tools falls back to
  static `CODY_TOOLS_AIKIDO_AUTHORIZATION` or Aikido API key style auth.
- Static API key auth auto-prefixes `Bearer` unless the value already starts
  with a recognized auth scheme such as `Bearer` or `Basic`.
- `CODY_TOOLS_AIKIDO_CLIENT_CREDENTIALS` cannot be combined with separate
  client ID/client secret env vars. That fails startup.
- Separate client ID/client secret must be set together. Partial config fails
  startup.
- If no OAuth and no static auth are available, Aikido proxy requests return
  `503`.

### OAuth Token Cache

- OAuth access tokens are cached.
- Token expiry uses `expires_in` minus a skew.
- If `expires_in` is missing or non-positive, token TTL falls back to 5 minutes.
- If the TTL is shorter than the normal skew, the skew is reduced.
- If an upstream Aikido request returns `401`, cody-tools invalidates the token
  and retries once with a fresh token.
- If the refresh fails, the proxy returns `503`.

### Local Rate Limiting

- A local token bucket limits aggregate Aikido calls through cody-tools.
- Current configured rate is 18 requests per minute with burst 1.
- Request-specific budget comes from `X-Cody-Aikido-Budget-Seconds`.
- Missing, invalid, non-positive, or too-large budget falls back to configured
  `CODY_TOOLS_AIKIDO_MAX_WAIT`.
- If the local token bucket delay fits inside the request budget, cody-tools
  waits and then calls Aikido.
- If the local token bucket delay does not fit, cody-tools returns local `429`.
- Local `429` includes a `Retry-After` header and JSON body.

### Upstream Aikido Rate Limiting

- If upstream Aikido returns `429`, cody-tools parses upstream `Retry-After`.
- If upstream `Retry-After` is missing or invalid, cody-tools internally falls
  back to 1 minute.
- The recorded cooldown is capped by `CODY_TOOLS_AIKIDO_RETRY_AFTER_CAP`,
  currently `90s`.
- The cooldown is shared across subsequent Aikido proxy calls.
- If the upstream retry-after can be waited within the remaining request
  budget, cody-tools waits and retries the same request once inline.
- If the retry-after cannot fit in the remaining budget, cody-tools returns
  `429` to the spawner/source.
- If upstream returns another `429` after the inline retry and lacks
  `Retry-After`, cody-tools supplies a `Retry-After` based on the internal
  cooldown.

### Upstream Request Safety

- Aikido proxy only allows `GET`.
- Non-GET returns `405`.
- Inbound `Authorization` and `Cookie` headers are stripped.
- Cody internal Aikido headers are stripped before forwarding upstream.
- Upstream Aikido requests are bounded by
  `CODY_TOOLS_AIKIDO_UPSTREAM_TIMEOUT`, currently `30s`.
- Redirects are allowed only to the configured upstream host.
- On cross-host redirect, the request fails.
- On upstream request failure, cody-tools returns `502`.

### GitHub Tool Fallbacks Used By Agents

The security agent does not receive Aikido proxy env vars, but it does receive
the GitHub cody-tools base URL.

- GitHub App credentials are optional at cody-tools startup.
- If a GitHub token is requested and credentials are absent, cody-tools returns
  `503`.
- GitHub packages token path prefers explicit `CODY_TOOLS_GITHUB_PACKAGES_TOKEN`.
- If the explicit packages token is absent, cody-tools falls back to a GitHub
  App installation token.
- Git credential helper returns empty credentials for unsupported protocol/host
  instead of failing the whole request.

## Aikido Source Runtime

The current security workflow uses export-based discovery because branch and
issue type filters are configured.

Normal export flow:

1. Resolve branch, defaulting to `main`.
2. Fetch active Aikido code repositories for that branch.
3. For each repo/status/issue type combination, fetch `/issues/export` rows.
4. Filter rows locally.
5. Dedupe rows by issue row ID.
6. Group rows by issue group ID.
7. Sort group IDs.
8. Build a WorkItem from grouped rows.
9. Emit the WorkItem immediately.

The WorkItem includes metadata:

- issue group ID
- branch
- severity
- status
- issue type
- repositories
- code repositories
- affected packages
- CVE IDs
- Aikido URL when available

The WorkItem body includes:

- issue group ID
- branch
- title
- severity
- status
- issue type
- repositories
- affected packages
- CVE IDs
- bounded scoped issue rows
- constraints for the agent

## Aikido Source Fallbacks

- Branch defaults to `main`.
- Status defaults to `open`.
- If issue types are omitted, discovery queries without an issue type filter.
- If repositories are omitted, discovery includes all active code repositories
  for the selected branch.
- A repository with missing `active` value is treated as active.
- Repository names are exact-matched when configured.
- Missing repo ID fails discovery because the source cannot query export rows.
- Missing issue row ID fails discovery because dedupe would be unsafe.
- Missing issue group ID fails discovery because no session scope can be built.
- Duplicate issue row IDs are ignored after the first occurrence.
- Rows that do not match requested repo/status/type/severity are ignored.
- Group IDs are sorted for deterministic processing.
- Title falls back to the first non-empty row title/name/summary/rule.
- If no title is present, title falls back to `Aikido issue group <id>`.
- Severity uses the highest severity from grouped rows by rank:
  critical > high > medium > low > unknown.
- Missing severity/status/type falls back to `unknown`.
- Repositories, packages, and CVEs are aggregated and sorted.
- Aikido URL falls back to row-level URL fields when group-level URL is absent.
- Export-based discovery intentionally does not call `/issues/groups/{id}` for
  every group. This is a deliberate fallback to the bounded export snapshot.
- Any Aikido proxy `429` with `Retry-After` is retried by the source.
- Source-level rate-limit retry allows up to 120 attempts.
- Source-level total retry wait defaults to 2 hours unless overridden.
- If the retry wait exceeds the source budget, discovery returns the `429`
  error to the spawner.
- If no active repos are returned, discovery returns zero items rather than
  erroring.
- Legacy non-export discovery still exists for Aikido sources without branch
  or issue-type filtering.

## Spawner Runtime

The one-shot spawner handles the TaskSpawner as follows:

1. Fetch TaskSpawner.
2. If suspended, write suspended status and exit.
3. Build source.
4. Detect session mode.
5. Detect Aikido streaming support.
6. If Aikido session mode is enabled and no priority labels are configured,
   run streaming Aikido session discovery.
7. For each emitted WorkItem:
   - render template variables;
   - add Aikido variables including `RunDate`;
   - render the TaskTemplate into a Task object;
   - copy source annotations;
   - find/create an AgentSession;
   - create an AgentTurn.
8. Update TaskSpawner status after streaming discovery finishes or errors.

## Spawner Fallbacks

- Suspended TaskSpawner exits before discovery.
- Aikido session mode without streaming support falls back to batch discovery.
- Aikido session mode with priority labels falls back to batch discovery so
  priority ordering remains honored.
- Non-session sources keep the existing Task creation path.
- Non-session source Tasks are deduped by Task name.
- Non-session completed Tasks can be retriggered if source trigger time is
  newer than completion time.
- In session mode, all discovered items are candidates; dedupe happens at
  session/turn level.
- Context source fetch failure for an item logs and skips that item.
- TaskBuilder creation failure logs and skips that item.
- Task template render/build failure logs and skips that item.
- AgentTurn creation failure logs and skips that item.
- If one item fails to create a turn, streaming discovery continues.
- If discovery errors after earlier items were emitted, earlier sessions/turns
  remain created.
- TaskSpawner status is updated with discovered count and created count before
  returning the streaming discovery error.
- `maxTotalTasks` stops streaming discovery gracefully and marks task budget
  exhausted.
- `maxConcurrency` is not enforced for session-mode Aikido turns; session
  queue limits are the control mechanism.

## Session And Turn Runtime

Session scope:

```text
aikido/<branch>/<run-date>/<issue-group-id>
```

Turn source ID:

```text
<work-item-id>-<YYYYMMDD-HHMM>
```

Session behavior:

- Find sessions by source `aikido`, TaskSpawner name, and scope hash.
- Reuse the newest active session for the scope.
- Create a new generation if no active session exists.
- Store TaskTemplate snapshot on the session.
- Set idle timeout, max age, and max queued turns from Aikido session config.

Turn behavior:

- Check for an existing turn in the session with the same Aikido source ID.
- If present, do nothing.
- Count queued/running turns.
- If queued/running count is at `maxQueuedTurns`, do not create another turn.
- Otherwise create the next sequence-numbered AgentTurn.

## Session And Turn Fallbacks

- Session scope template falls back to the built-in default if omitted.
- Aikido max age falls back to 25 hours if omitted.
- Aikido idle timeout falls back to 25 hours if omitted.
- Aikido max queued turns falls back to 1 if omitted.
- Branch in template vars falls back from WorkItem branch to TaskSpawner branch
  to `main`.
- Schedule in template vars falls back from item schedule to TaskSpawner
  schedule.
- If scope template renders empty, turn creation fails for that item.
- If same turn source ID already exists, turn creation is a no-op.
- If AgentTurn name collision is for the same session/source, it is treated as
  already created.
- If AgentTurn name collision is for a different session/source, it is an
  error.
- Terminal sessions (`Closed` or `Error`) are not reused.
- Existing active sessions are reused even if older than desired max age until
  their status has been marked terminal by the session controller.

## Agent Runtime

The security AgentConfig instructs the agent to:

- focus on latest `main`;
- use the controller-provided Aikido snapshot as the Aikido source of truth;
- not call Aikido;
- ignore other shared config that mentions Aikido reads;
- not inspect env vars, mounted files, Kubernetes secrets, or cody-tools
  endpoints for Aikido credentials;
- search GitHub for existing remediation PRs before creating new ones;
- avoid duplicate PRs at prompt level;
- create or update PRs only when fixes are concrete and reviewable;
- handle shared package/image propagation by continuing in future turns;
- not mutate live infrastructure;
- not merge PRs.

The agent pod currently receives GitHub cody-tools access, not Aikido cody-tools
access.

## Agent-Level Fallbacks

These are prompt-level fallbacks, not deterministic controller locks:

- If a matching remediation PR exists, continue that PR instead of opening a
  duplicate.
- If no safe fix is possible, explain the blocker and next human action.
- If a shared package/image fix needs downstream propagation, wait across
  future turns for publish/build evidence.
- If the issue is already fixed or fully covered, start the final answer with
  `NO_SLACK:` so Kelos can suppress unnecessary terminal Slack noise.

## Slack Runtime

The security TaskSpawner uses:

- `kelos.dev/slack-reporting: deferred`
- `kelos.dev/slack-destination: cody-security`
- `kelos.dev/slack-layout: session-summary-root`

For non-Slack-originated sessions, Slack channel resolution happens in this
order:

1. Existing `AgentSession.Status.Slack.ChannelID`.
2. `AgentSession.Spec.Source.ChannelID`.
3. Turn annotation `kelos.dev/slack-channel`.
4. Turn annotation `kelos.dev/slack-destination`, resolved through the Slack
   route map.

`session-summary-root` behavior:

- Create or update one root message per AgentSession.
- Root title comes from the session source display name, falling back to the
  TaskSpawner name, then `Cody session`.
- Summary starts as `Session started.` if there is no stored summary.
- Progress updates set only Latest from current activity.
- Terminal updates append a compact line to Summary.
- Terminal details are posted as thread replies when present.

## Slack Fallbacks

- If no channel resolves, Slack reporting no-ops.
- If the root has not been posted yet, Slack posts a top-level root.
- If the root already exists, Slack updates it in place.
- If session status lacks Slack state, it is initialized after posting/updating.
- Summary lines are deduped by normalized text.
- Summary keeps only the last 8 lines.
- Summary and Latest are compacted to Slack-safe limits.
- Empty activity does not post a progress update.
- Repeated same activity does not update Slack again.
- Terminal turn reporting is skipped if `SlackAgentMessageTS` is already set.
- Terminal deferred/session-summary reporting is suppressed if result text
  starts with `NO_SLACK:`.
- If posting terminal details to the thread fails, the error is logged and the
  root update remains.

## Important Non-Fallbacks And Known Limits

- There is no in-day non-Aikido heartbeat source yet.
- There is no controller-owned PR dedupe or lock.
- There is no controller-owned Aikido rescan after a fix.
- Aikido verification happens on the next discovery run.
- The Aikido discovery CronJob has `startingDeadlineSeconds`, but no explicit
  Job `activeDeadlineSeconds`.
- Long discovery waits are bounded by source retry wait and cody-tools request
  budgets, not by a dedicated Job deadline.
- `concurrencyPolicy: Forbid` prevents overlapping CronJob runs, but a very
  long run can cause later scheduled runs to be skipped rather than queued.
- Agent duplicate prevention depends on prompt compliance and GitHub search
  quality.
- Export-row snapshots may lack richer group-level fields such as full Aikido
  remediation guidance because `/issues/groups/{id}` detail fan-out is skipped.
- The current skills config is scoped to `template-nestjs-be`; broader repo
  coverage requires changing the TaskSpawner repository list or removing it.

## Merge And Rollout Implications

Runtime code changes live in:

- `kelos-spawner`: streaming discovery, daily Aikido scope, session turn
  creation.
- `cody-tools`: Aikido rate limiting, OAuth/static auth, retry-after handling.

Configuration changes live in:

- k8s-platform GitOps: cody-tools deployment env and Kelos image wiring.
- skills: Aikido TaskSpawner and AgentConfig.

No CRD or chart schema change is required by the discovery simplification
layer. The runtime image changes still require rebuilding/publishing the
affected Kelos images after the Kelos PR merges.
