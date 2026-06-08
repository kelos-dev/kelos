# Cody Aikido Issue Babysitter High-Level Spec

Status: Draft
Date: 2026-06-08
Owner: Cody / Kelos / Platform

## Summary

Build a first-class Aikido security remediation workflow that starts one Cody
AgentSession per Aikido security issue group and lets that session babysit the
remediation lifecycle end to end.

This should follow the working devops babysitter shape:

- deterministic controller discovers work;
- one stable session owns one durable problem;
- scheduled heartbeats advance the session over time;
- Slack has one root message per session with concise summary/latest status;
- full turn details go in the Slack thread;
- Cody uses the Codex app-server session runtime and can use subagents/tools
  internally, but session creation and dedupe stay deterministic.

This workflow is intentionally different from a plain cron prompt that asks Cody
to "check Aikido". The controller should query Aikido itself, decide which issue
groups are in scope, and create or heartbeat exactly one session per issue.

## Scope

Initial scope:

- latest `main` branch security issues only;
- open Aikido issues, likely critical/high first;
- one AgentSession per Aikido issue group;
- Cody may open PRs in one or more GitHub repos needed for the fix;
- Cody may wait across multiple heartbeats for CI, package/image builds,
  consumer bumps, and Aikido verification;
- final verification should use Aikido again, ideally by retriggering or
  requesting a fresh scan through a controlled Cody tools path.

Out of scope for the first version:

- release-train-specific remediation;
- environment-specific deployed-image cleanup;
- auto-merge;
- broad report generation;
- replacing the existing infra-health babysitter;
- giving Aikido credentials directly to agent pods.

## Existing Implementation Checked

### Kelos `origin/main` at `5a37c99`

Kelos already has several Aikido building blocks:

- `cmd/cody-tools` exposes a read-only Aikido REST proxy at
  `http://cody-tools.kelos-system.svc.cluster.local:8080/aikido`.
- The proxy injects Aikido auth server-side and rejects write methods.
- OAuth client credentials are supported for Aikido auth.
- `TaskSpawner.spec.when.aikido` exists and is scheduled through a CronJob.
- `internal/source/aikido.go` discovers Aikido open issue groups and maps each
  issue group to a `WorkItem`.
- Aikido `WorkItem` metadata includes issue group ID, severity, status, issue
  type, repositories, and URL.
- The current Aikido source creates normal one-shot `Task`s. It does not yet
  create per-issue AgentSessions or heartbeat turns.

Relevant merged Kelos PRs:

- https://github.com/donchev7/kelos/pull/8 - Cody Aikido API proxy.
- https://github.com/donchev7/kelos/pull/9 - Aikido OAuth client credentials.
- https://github.com/donchev7/kelos/pull/10 - bearer auth normalization.
- https://github.com/donchev7/kelos/pull/26 - Aikido TaskSpawner source and
  cron context spec.

### `k8s-platform-gitops` `origin/main` at `abf39a4181`

Platform GitOps already wires the Aikido proxy into Cody runtime:

- `non-prod/kelos/deployment-cody-tools.yaml` sets
  `CODY_TOOLS_AIKIDO_API_BASE_URL`.
- `external-secret-cody-aikido-api.yaml` sources
  `cody-aikido-api-client-credentials`.
- `agentconfig-cody-debugger.yaml` documents how Cody should use the internal
  read-only Aikido proxy.

Relevant PRs:

- https://github.com/quantum-wealth/k8s-platform-gitops/pull/2091 - wire Cody
  Aikido API proxy.
- https://github.com/quantum-wealth/k8s-platform-gitops/pull/2130 - closed
  service-owned Cody Aikido workflow registration attempt.

### `skills` `origin/main` at `269abb4`

`origin/main` does not currently contain Aikido security remediation manifests.

There is an open WIP branch/PR:

- Branch: `origin/cody/security-remediation-automation`
- PR: https://github.com/quantum-wealth/skills/pull/64

That WIP adds a release-train QA remediation cron. It mirrors infra-health
session cron behavior, uses Slack session summary layout, and encodes strong
lessons from the v42 manual remediation. It is useful prior art, but it is not
the same workflow requested here:

- it is release-train and QA-image oriented;
- it is one train-level daily session, not one session per Aikido issue;
- it expects an Aikido MCP route at `/mcp/aikido`, which is not present in
  Kelos `origin/main` today;
- it targets release branches, while this spec targets latest `main`.

### `k8s-apps-gitops` `origin/main` at `b7498831a0a`

No relevant Aikido runtime manifests were found. It is still useful context for
image and repo mapping, but latest-main source remediation should not depend on
environment overlays in the first pass.

### `aikido-report-builder` `origin/main`

This repo is reporting/tooling prior art, not Cody orchestration:

- fetches Aikido SCA and leaked-secret findings through API exports;
- tracks state through a user-provided `snapshot.json`;
- can create/link Jira tasks;
- can call Aikido write endpoints for task tracking and SBOM generation;
- has useful API mapping for `/issues/export`, leaked-secret handling, and
  issue group IDs.

The important lesson for Cody is that Aikido writes exist, but the current Cody
runtime deliberately exposes only read-only Aikido REST access.

## Live Aikido API Shape Check

Date checked: 2026-06-08

Checked through the existing non-prod `cody-tools` proxy:

```text
http://cody-tools.kelos-system.svc.cluster.local:8080/aikido
```

I used a temporary local port-forward to `svc/cody-tools` in `kelos-system` and
queried the proxy. No direct public Aikido credentials were used.

### `/repositories/code`

Sample request:

```text
GET /aikido/repositories/code?filter_branch=main&per_page=5&page=0
```

Observed response shape: a top-level JSON array of code repository records.

Important fields:

```text
id
name
branch
active
url
last_scanned_at
provider
external_repo_id
external_repo_numeric_id
connectivity
sensitivity
```

Example selected fields:

```json
{
  "id": 1443254,
  "name": "template-nestjs-be",
  "branch": "main",
  "active": true,
  "url": "https://api.github.com/repos/quantum-wealth/template-nestjs-be",
  "last_scanned_at": 1780858288
}
```

Useful behavior confirmed:

- `filter_branch=main` works.
- `filter_name=<repo>` works.
- repository records are branch-scoped.
- a repository can be validated as an active `main` code repo before querying
  issues.

This is the deterministic entry point for latest-main scoping. The controller
should not ask Cody to infer "main" from prompts when Aikido exposes branch
scoped repository IDs.

### `/issues/export`

Sample request:

```text
GET /aikido/issues/export?filter_status=open&filter_code_repo_id=1443254&per_page=20&page=0
```

Observed response shape: a top-level JSON array of individual issue/location
records.

Important fields:

```text
id
group_id
status
type
severity
severity_score
code_repo_id
code_repo_name
container_repo_id
container_repo_name
affected_package
installed_version
patched_versions
cve_id
affected_file
start_line
end_line
first_detected_at
rule
rule_id
programming_language
attack_surface
exploitability
sla_days
sla_remediate_by
closed_at
ignored_at
snooze_until
```

Example selected fields:

```json
{
  "id": 299565698,
  "group_id": 17997122,
  "status": "open",
  "severity": "critical",
  "severity_score": 100,
  "type": "open_source",
  "code_repo_id": 1443254,
  "code_repo_name": "template-nestjs-be",
  "container_repo_id": 623332,
  "container_repo_name": "template-nestjs-be",
  "affected_package": "golang.org/x/crypto",
  "installed_version": "v0.49.0",
  "patched_versions": ["0.52.0"],
  "cve_id": "AIKIDO-2026-11022",
  "affected_file": "app/node_modules/.pnpm/@quantum-wealth+migrate@1.3.2/node_modules/@quantum-wealth/migrate/bin/alpheya-migrate-darwin-arm64",
  "first_detected_at": 1780724455
}
```

Useful behavior confirmed:

- `filter_status=open` works.
- `filter_code_repo_id=<id>` works.
- `filter_issue_type=<single-type>` works for values such as `open_source`,
  `sast`, and `leaked_secret`.
- a comma-separated value such as
  `filter_issue_type=open_source,docker_container` returned HTTP 400, so the
  controller should issue separate queries per issue type if it needs multiple
  types.
- `filter_issue_type=docker_container` returned records whose `type` was still
  `open_source` in the sampled response. Treat issue type filters as Aikido API
  hints, not proof; always inspect returned `type`.
- the issue row does not include `branch`; latest-main scope must come from
  the `code_repo_id` selected from `/repositories/code?filter_branch=main`.

This endpoint is the best primary discovery source for the babysitter because
it gives precise affected repo/container/package/file/version evidence and
includes `group_id` for grouping.

### `/open-issue-groups`

Sample request:

```text
GET /aikido/open-issue-groups?filter_status=open&filter_code_repo_id=1443254&per_page=20&page=0
```

Observed response shape: a top-level JSON array of grouped issue summaries.

Important fields:

```text
id
title
description
group_status
severity
severity_score
type
locations
related_cve_ids
how_to_fix
time_to_fix_minutes
```

Example selected fields for the same `group_id` seen in `/issues/export`:

```json
{
  "id": 17997122,
  "title": "golang.org/x/crypto",
  "description": "Authorization bypass possible",
  "group_status": "new",
  "severity": "critical",
  "severity_score": 100,
  "type": "open_source",
  "locations": [
    {
      "id": 1443254,
      "name": "template-nestjs-be",
      "type": "code_repository"
    },
    {
      "id": 623332,
      "name": "template-nestjs-be",
      "type": "container_repository"
    }
  ],
  "how_to_fix": "In order to fix all of these vulnerabilities, update golang.org/x/crypto to 0.52.0."
}
```

Useful behavior confirmed:

- `filter_status=open` works.
- `filter_code_repo_id=<id>` works.
- grouped summaries include a good title, how-to-fix text, and locations.
- group `locations` can include both code repositories and container
  repositories.
- one group can span many repositories and containers globally.

This endpoint is useful as enrichment for a session, but it should not be the
only discovery source for latest-main remediation because the grouped record is
too coarse for exact repo/file/package/version evidence.

### `/issues/groups/{group_id}`

Sample request:

```text
GET /aikido/issues/groups/17997122
```

Observed response shape: a single grouped issue object.

Important fields match `/open-issue-groups`:

```text
id
title
description
group_status
severity
severity_score
type
locations
related_cve_ids
how_to_fix
time_to_fix_minutes
```

Important nuance: group detail can include many locations across code
repositories and container repositories, not only the one repo used for the
issue export query. For example, a single `golang.org/x/crypto` group included
multiple code repositories and many container repositories. That is useful for
understanding blast radius, but it reinforces that the controller should attach
the exact in-scope `/issues/export` rows to the session.

### `/issues/{issue_id}`

Sample request:

```text
GET /aikido/issues/299565698
```

Observed response shape: a single issue object.

Important fields:

```text
id
group_id
status
type
severity
severity_score
code_repo_id
code_repo_name
container_repo_id
container_repo_name
affected_package
affected_file
cve_id
first_detected_at
how_to_fix
issue_type_metadata
reachability_status
ignore_reasons
snooze_reason
```

This endpoint is useful for deeper context on a specific affected row, but the
initial session creation should not require one detail request per issue unless
the export row is missing required fields.

## Issue vs Issue Group Decision

Use **Aikido issue group ID** as the Cody session owner, but build each session
from **issue export rows**.

Rationale:

- Aikido issue rows are the precise remediation evidence:
  repo/container/package/file/version/status.
- Aikido issue groups are the durable human-sized remediation unit:
  one package/CVE/rule can appear in multiple files/images/repos.
- One session per issue row would create duplicate Cody sessions for the same
  underlying root cause.
- One session per issue group lets Cody coordinate shared-package fixes,
  producer PRs, and consumer bumps under one owner.
- The session still needs all in-scope issue rows attached so Cody does not lose
  exact file/repo/container evidence.

Recommended controller flow:

1. Query active main code repositories:

   ```text
   GET /repositories/code?filter_branch=main&page=<n>
   ```

2. For each selected code repo ID, query open issue export rows:

   ```text
   GET /issues/export?filter_status=open&filter_code_repo_id=<code_repo_id>&page=<n>
   ```

   If filtering by type, issue separate requests per type rather than using a
   comma-separated list.

3. Filter returned rows deterministically:

   - `status == open`;
   - severity in configured severities, initially `critical` / `high`;
   - issue type in configured issue types if the TaskSpawner config limits
     types;
   - `code_repo_id` must be one of the active `main` code repo IDs discovered in
     step 1.

4. Group rows by `group_id`.

5. For each `group_id`, fetch group summary:

   ```text
   GET /issues/groups/<group_id>
   ```

6. Create/find one `AgentSession` keyed by:

   ```text
   aikido/main/<group_id>
   ```

7. Attach both layers of context to the first/heartbeat turn:

   - group summary from `/issues/groups/<group_id>`;
   - exact in-scope issue rows from `/issues/export`.

8. Create an `AgentTurn` only if the session has no queued/running turn and the
   issue group is still open/in scope.

Important caveat: because `/issues/export` rows do not include a `branch` field,
latest-main filtering must be based on branch-scoped code repo IDs from
`/repositories/code?filter_branch=main`. Do not rely on prompt-only filtering
for this.

### PR History Lessons

Recent org PR history shows manual Aikido remediation often spans multiple
repos and multiple days:

- app dependency bumps;
- backports/cherry-picks;
- shared package fixes followed by consumer bumps;
- image/base rebuilds;
- CI reruns;
- PRs that only trigger release checks or rebuilds;
- closed duplicate or superseded remediation PRs.

That supports the per-issue session model: the deterministic controller should
dedupe and keep one owner session alive, while Cody advances the issue through
the next useful step on each heartbeat.

Sample PR history checked:

- https://github.com/quantum-wealth/skills/pull/33 - closed Devin Aikido
  security playbook hardening attempt.
- https://github.com/quantum-wealth/skills/pull/34 - open replacement for the
  Devin Aikido playbook hardening branch.
- https://github.com/quantum-wealth/skills/pull/64 - open scheduled Aikido
  release-train remediation WIP.
- https://github.com/quantum-wealth/k8s-platform-gitops/pull/2130 - closed
  service-owned Cody Aikido workflow registration.
- https://github.com/quantum-wealth/alpheya-shared-ci/pull/910 - merged
  QA-driven Aikido reconcile in release orchestrator.
- https://github.com/quantum-wealth/alpheya-shared-ci/pull/911 - merged
  on-demand Aikido reconcile workflow.
- https://github.com/quantum-wealth/alpheya-shared-ci/pull/914 - merged
  per-release Jira summary tickets.
- https://github.com/quantum-wealth/nexus-adapter/pull/730 - closed/superseded
  Aikido release-v42 remediation PR.
- https://github.com/quantum-wealth/order-service/pull/709 - closed/superseded
  release-v42 check-trigger PR.
- Multiple merged remediation PRs across `platform-services`, `order-service`,
  `notification-service`, `asset-service`, `bank-ingestor`,
  `alpheya-common-packages`, `oauth2-proxy`, and other app repos in early
  June 2026.

## Proposed Runtime Shape

```text
Morning schedule
  -> Aikido controller queries Aikido open issue groups
  -> filter to in-scope latest-main critical/high issues
  -> for each issue group:
       find/create AgentSession keyed by Aikido issue group ID + branch scope
       enqueue one AgentTurn if no turn is already queued/running
  -> active issue sessions receive heartbeat turns until resolved/blocked
```

Each session should be keyed by a stable scope:

```text
aikido/main/<aikido_issue_group_id>
```

The controller, not the prompt, owns:

- discovery;
- issue group ID extraction;
- main-branch filtering where the Aikido API exposes enough metadata;
- dedupe;
- max queued turn protection;
- session creation;
- heartbeat turn creation;
- closing sessions when the issue is resolved or explicitly blocked.

Cody owns:

- issue triage and root-cause analysis;
- repository/package/image investigation;
- deciding whether a fix is safe;
- creating PRs;
- driving CI/builds where it has permission;
- waiting when human merge or asynchronous build is required;
- producing concise Slack status and final summaries.

## Session Lifecycle

Do not implement a rich remediation state machine in Kelos v1.

Kelos should keep the session lifecycle generic and reuse the existing
`AgentSession` / `AgentTurn` mechanics:

- `AgentSession` phases stay the platform lifecycle:
  `Pending`, `Starting`, `Idle`, `Running`, `Closing`, `Closed`, `Error`.
- `AgentTurn` phases stay the turn lifecycle:
  `Queued`, `Running`, `Succeeded`, `Failed`, `Canceled`.
- The deterministic Aikido controller owns only discovery, session identity,
  turn dedupe, heartbeat enqueueing, and terminal stop conditions.
- Cody owns remediation semantics: triage, fix PRs, waiting on review, waiting
  on build, consumer bumps, scan verification, and blocker explanations.

In other words, Kelos should know:

```text
this Aikido issue group is open
this AgentSession owns it
there is / is not an active queued or running turn
the issue is resolved or no longer in scope, so stop feeding the session
```

Kelos should not need to know:

```text
this fix is waiting on a shared package release
this PR needs codeowner approval
this downstream image has not rebuilt yet
this is blocked on a security exception decision
```

Those details should live in the Cody prompt, the session summary, Jira/PR
links, and the next heartbeat prompt.

The user-facing remediation "stage" can still be reported by Cody in Slack, but
it is descriptive, not a Kelos enum. Useful stage words for the agent summary:

- triaging;
- fix PR open;
- awaiting review/merge;
- awaiting build;
- awaiting consumer bump;
- awaiting Aikido verification;
- resolved;
- blocked on human decision.

Heartbeat behavior should be bounded and state-aware:

- no duplicate turn if one is queued or running;
- no backfilled missed ticks;
- daily discovery for new issues;
- slower heartbeats while waiting for human merge;
- faster heartbeats only while CI/builds are actively running;
- terminal sessions should not receive new turns unless the issue reopens or
  materially changes.

Implementation parallels:

- Infra-health cron sessions already find/create deterministic sessions,
  dedupe turns, enforce `maxQueuedTurns`, and create a new generation only after
  a terminal session. Aikido should follow that runtime pattern with scope =
  Aikido issue group, not env/day.
- Slack sessions already map an external conversation to one `AgentSession` and
  convert follow-up events into `AgentTurn`s. Aikido should map an external
  Aikido issue group to one `AgentSession` and convert scheduled heartbeats into
  `AgentTurn`s.
- One-shot `Task` TTL is the contrast case: deleting completed tasks allows a
  new independent run. Aikido should prefer session continuity instead, because
  the issue may require multiple turns over multiple days.

## Main Branch Handling

The first version should focus on latest `main`.

The controller should prefer deterministic API filtering, not prompt-only
filtering.

The live API check confirms that `/repositories/code` is branch-scoped and
supports `filter_branch=main`. The issue export rows do not include a `branch`
field, so latest-main filtering should be implemented in two steps:

1. Discover active main code repository IDs with:

   ```text
   /repositories/code?filter_branch=main
   ```

2. Query open issue rows by those code repo IDs with:

   ```text
   /issues/export?filter_status=open&filter_code_repo_id=<id>
   ```

Then group rows by `group_id` and fetch `/issues/groups/<group_id>` for summary
context.

Do not use `/open-issue-groups` as the primary latest-main discovery source in
v1. It is useful enrichment, but it does not carry the same precise issue row
evidence and a group can span many repositories/containers globally.

## Aikido Access Model

Keep Aikido credentials server-side in `cody-tools`.

For v1, the controller needs read access:

- list open issue groups or exported issues;
- fetch issue group details;
- fetch issue details if needed.

For end-to-end verification, we likely need a controlled write path:

- trigger or request a fresh scan;
- optionally link an external task/PR/Jira issue back to an Aikido issue group.

Current Kelos `cody-tools` rejects non-GET Aikido calls. That is good for the
existing debug/runtime safety model, but it means scan retriggering cannot be
implemented without one of:

- an allowlisted Aikido write proxy in `cody-tools`;
- a dedicated Aikido MCP broker in `cody-tools` with only approved tools;
- a separate controller-owned Aikido client that never exposes credentials to
  agent pods.

Recommendation: keep agents credentialless and put any Aikido write operations
behind `cody-tools` or a controller-owned client with a narrow allowlist.

## Slack UX

Use the existing session-owned Slack surface:

- one root Slack message per Aikido issue session;
- root message title: issue title/severity/repo;
- `Summary`: concise additive session summary, updated at session start and end
  of every turn;
- `Latest`: streaming active work/status from the current turn;
- full terminal turn details posted in the thread;
- no duplicate root messages within a live session;
- if a clean daily discovery finds no new issues, no Slack post.

Default destination should be a security/devops channel configured in the
TaskSpawner or AgentConfig, not hardcoded in Kelos.

## Minimal Implementation Direction

### Kelos

Extend the existing Aikido source path instead of adding a totally separate
workflow engine:

- add session-backed execution for `spec.when.aikido`, analogous to cron
  session execution;
- reuse `AgentSession`, `AgentTurn`, session runner, and Slack
  `session-summary-root`;
- key sessions by Aikido issue group ID plus branch scope;
- copy Aikido metadata to AgentSession/AgentTurn labels and annotations;
- ensure Aikido scheduled source honors missed-start protection and does not
  backfill stale discovery ticks;
- add tests for one session per issue group and no duplicate queued turns.

Potential API shape:

```yaml
spec:
  when:
    aikido:
      schedule: "0 5 * * *"
      statuses: ["open"]
      severities: ["critical", "high"]
      branch: main
      session:
        enabled: true
        scopeTemplate: "aikido/{{.Branch}}/{{ index .Metadata \"aikido.kelos.dev/issue-group-id\" }}"
        maxAge: 14d
        idleTimeout: 24h
        maxQueuedTurns: 3
```

The exact API should be revisited before implementation. A generic
source-session config may be better long-term, but Aikido-specific session
support is the lowest-risk first step because infra-health cron sessions already
proved the runtime path.

### Skills

Add a new skill/manifests area, separate from infra-health and separate from the
release-train WIP:

```text
cody/security-main-remediation/
  README.md
  agentconfig-cody-aikido-main-remediation.yaml
  taskspawner-cody-aikido-main-security.yaml
```

AgentConfig should instruct Cody to:

- work from latest `main`;
- validate the Aikido issue applies to main before editing;
- find existing PRs/Jira/issues by Aikido issue group ID before creating new
  ones;
- prefer minimal safe fixes;
- handle shared package fixes by opening the producer PR first, then consumer
  bumps in later heartbeats;
- never merge unless we explicitly grant that policy later;
- ask in Slack instead of guessing on suppress-vs-fix decisions;
- include the Aikido issue group ID in every PR body and branch/commit context.

### k8s-platform-gitops

Likely changes:

- ensure `cody-tools` Aikido credentials have the read scopes needed by the
  controller;
- add any controlled Aikido write/MCP route needed for scan retriggering;
- add NetworkPolicy access for the Aikido scheduled controller/spawner if it
  needs to call `cody-tools`;
- route Slack to the desired security/devops channel.

### k8s-apps-gitops

No required first-pass change for latest-main issue remediation.

Later, k8s-apps may help with image-to-repo mapping and deployed verification,
but that belongs to release/environment remediation, not the initial latest-main
workflow.

## Open Questions

1. Which Aikido issue types are in scope for the first version: open source,
   SAST, IaC, leaked secret, container, or all critical/high latest-main
   findings?
2. How do we map Aikido repository names to GitHub repositories for repos whose
   Aikido name does not match `quantum-wealth/<repo>` exactly?
3. Do we require Jira before PR creation, or is GitHub PR state enough for v1?
4. Should Cody be allowed to link Jira/PRs back into Aikido through
   `task_tracking/linkTaskToIssueGroup`?
5. What exact Aikido API/MCP operation can retrigger a scan, and what credential
   scope does it require?
6. What is the verification source of truth: Aikido issue closed, issue absent
   from fresh export, local scanner clean, or a combination?
7. How long should issue sessions live? A shared package plus consumer rollout
   can span several days, so 24 hours is probably too short.
8. Should multiple Aikido issue sessions be allowed to open separate PRs that
    touch the same shared package, or should the controller nominate one owner
    session and mark the others as linked/dependent?
9. Which Slack channel should receive security issue sessions by default?
10. Should low/medium issues be ignored, summarized silently, or create sessions
    with lower heartbeat frequency?
11. Should the first deployment be org-wide, repo-filtered, or limited to a
    small pilot set of repos?
12. Should an issue session close as `blocked` when it needs codeowner merge, or
    remain active and heartbeat daily until the human merge happens?
13. Should this new Aikido session support be genericized with cron/slack
    session behavior immediately, or should we keep it Aikido-specific until the
    workflow proves stable?

## Suggested First Spec Cut

For the first implementation spec, keep it small:

- extend `when.aikido` to create AgentSessions;
- use one global TaskSpawner for critical/high open latest-main issue groups;
- no Aikido writes yet except optional scan retrigger if a safe allowlisted API
  path is confirmed;
- no auto-merge;
- no release branches;
- no k8s-apps dependency;
- one Slack security channel;
- one session per issue group with a 7-14 day max age;
- heartbeat daily by default, with no duplicate queued turns.
