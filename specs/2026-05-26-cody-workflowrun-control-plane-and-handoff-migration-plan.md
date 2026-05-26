# Cody WorkflowRun Control Plane And Handoff Migration Plan

## Status

Draft implementation plan.

Created: 2026-05-26

This plan supersedes the Slack-mediated handoff direction in:

- `specs/2026-05-22-12-00-cody-personas-and-handoffs.md`
- `specs/2026-05-22-14-30-cody-personas-phase-2-handoffs-implementation.md`
- `specs/2026-05-22-15-20-cody-personas-phase-2-slack-mediated-handoffs.md`

Those specs correctly identified the need for scoped Cody personas and
result-driven handoffs. This plan adds the missing durable read model and
Slack control-plane behavior so humans can ask what is running, what happened,
and what the next step is without spawning another LLM task.

## Problem Statement

Cody is moving away from one broad Slack agent that classifies and performs
every kind of work. The target model is a small AI team:

- incident debugger / RCA investigator
- ticket creator
- feature builder
- PR reviewer
- live verifier
- RCA writer

Slack remains the human control surface, but Slack messages should not be the
workflow control plane. Today, some handoffs are implemented by having Cody
write a standalone Slack command such as `@cody !dev ...`; Kelos then parses
that bot-authored Slack message and routes it as if a human sent it. That works
as an incremental bridge, but it couples workflow orchestration to Slack text
formatting and hard-coded Cody persona names.

Humans also need to ask:

- what is Cody doing right now?
- what happened in this incident thread?
- which Jira ticket or PR did Cody create?
- why is this workflow blocked?
- what recent AI work ran in this channel or for me?

Those answers should come from Kelos state, not from another agent guessing
from Slack history.

## Goals

- Make each Cody persona a scoped operational route with its own trigger,
  AgentConfig stack, permissions, concurrency, and output contract.
- Keep Slack as the operator console for triggering, approving, and querying
  workflows.
- Add a durable `WorkflowRun` read model that indexes AI work across Tasks,
  handoffs, Jira refs, PR refs, Slack threads, and verification results.
- Add deterministic Slack commands for status and history.
- Replace Slack self-handoff with result-driven Kelos handoffs.
- Keep Jira and PR creation inside specialist agents and tools, not inside
  Kelos core.
- Preserve a migration path that lets existing Cody routes keep working while
  the new primitives roll out.

## Non-Goals

- Do not build a general workflow engine, DAG system, or Temporal replacement.
- Do not add Jira business logic to Kelos Go code.
- Do not let agents create arbitrary Kubernetes Tasks directly.
- Do not make status/history queries call an LLM.
- Do not keep Slack bot-authored handoff lines as the long-term control plane.
- Do not require a new Slack app for the first implementation.
- Do not add emergency hotfix workflows in the first implementation.

## Resolved Decisions

- Create `WorkflowRun` for Slack-origin Cody Tasks first. The CRD should be
  source-agnostic, but initial controller backfill and status/history UX should
  focus on Slack-created work.
- Broad `!history` responses do not require Slack ephemeral messages before the
  non-prod rollout. They may post compact channel/thread responses in non-prod;
  production should either use ephemeral messages or enforce narrow
  channel/user scoping.
- Retain terminal `WorkflowRun` resources for one week. Active workflows are
  not deleted.
- Do not add emergency hotfix shortcuts. PR-producing work must go through the
  ticket creator step first.
- Query-by-Jira and query-by-PR outside the current Slack channel is allowed
  only for configured Slack users or groups.

## Existing State

### Useful Kelos primitives

Kelos already has the core task and status surfaces:

- `Task.status.results` stores structured key-value outputs produced by an
  agent.
- `internal/controller/output_parser.go` parses `key: value` lines between
  `---KELOS_OUTPUTS_START---` and `---KELOS_OUTPUTS_END---`.
- `TaskTemplate` already carries type, credentials, image, workspace,
  AgentConfigs, branch, prompt, metadata, TTL, context sources, and pod
  overrides.
- `Task.spec.dependsOn` and dependency prompt templating can read prior task
  outputs through `.Deps`.
- Slack-origin Tasks already carry annotations:
  - `kelos.dev/slack-reporting`
  - `kelos.dev/slack-channel`
  - `kelos.dev/slack-thread-ts`
  - `kelos.dev/slack-user-id`
- `AgentSession` and `AgentTurn` already model thread-scoped interactive
  Slack sessions with source, route, status, queued turns, last activity, and
  terminal result text.

### Current Cody GitOps routes

`k8s-platform-gitops/non-prod/kelos` already has separate Slack routes:

- `cody-debug-slack`
- `cody-ticket-slack`
- `cody-dev-slack`
- `cody-pr-reviewer-slack`
- `cody-pr-babysitter-slack`
- `cody-session-slack`

This is directionally correct. The remaining architectural issue is that
handoff still relies on prompt instructions and Slack text routing.

### Current Slack self-handoff Go code

The current self-handoff implementation lives in Kelos core:

| File | Current role |
| --- | --- |
| `internal/slack/self_handoff.go` | Parses Cody-authored Slack lines and allows hard-coded targets `ticket`, `dev`, `review`, and `babysit`. |
| `internal/slack/self_handoff_test.go` | Unit tests for parsing, loop caps, and stop notices. |
| `internal/slack/handler.go` | Calls `handleSelfHandoffEvent` from message/app-mention paths and from reported terminal messages. |
| `internal/reporting/watcher.go` | `SlackTaskReporter` calls `TerminalMessageHandler` after terminal Slack updates so the self-handoff parser can see final Cody replies. |
| `cmd/kelos-slack-server/main.go` | Wires `TerminalMessageHandler: handler.HandleReportedTerminalMessage`. |
| `internal/manifests/charts/kelos/values.yaml` | Exposes `slackServer.selfHandoff.enabled` and `maxPerThread`. |
| `internal/manifests/charts/kelos/templates/slack-server.yaml` | Maps Helm values to `SLACK_SELF_HANDOFF_ENABLED` and `SLACK_SELF_HANDOFF_MAX_PER_THREAD`. |

This should be treated as a temporary bridge. It is Cody-specific policy inside
generic Slack routing, and it uses Slack text as the control plane.

## Proposed Architecture

### Control-plane split

Slack is the human interface:

- start an incident/debug workflow
- ask for status/history
- approve or manually trigger follow-up work
- receive progress and final summaries

Kelos is the workflow state machine:

- owns Task lineage
- owns structured handoff routing
- owns workflow status/history aggregation
- exposes deterministic status/history through Slack

Jira and GitHub are durable external records:

- Jira ticket key and URL
- PR URL, branch, commit, checks
- RCA or Confluence links

Agents are specialists:

- debugger investigates and emits evidence
- ticket creator creates or updates Jira
- feature builder opens PRs
- reviewer reviews PRs
- verifier proves behavior
- RCA writer writes final incident knowledge

### New `WorkflowRun` CRD

Add a new read-model CRD:

```go
type WorkflowRunSpec struct {
    Source WorkflowRunSource `json:"source"`
    Title string `json:"title,omitempty"`
    WorkflowKind string `json:"workflowKind,omitempty"`
    RootTaskRef *TaskReference `json:"rootTaskRef,omitempty"`
}

type WorkflowRunSource struct {
    Type string `json:"type"` // SlackThread, GitHub, Cron, Webhook
    TeamID string `json:"teamID,omitempty"`
    ChannelID string `json:"channelID,omitempty"`
    RootTS string `json:"rootTS,omitempty"`
    ThreadURL string `json:"threadURL,omitempty"`
    UserID string `json:"userID,omitempty"`
}

type WorkflowRunStatus struct {
    Phase WorkflowRunPhase `json:"phase,omitempty"` // Running, Succeeded, Failed, Blocked
    CurrentStep string `json:"currentStep,omitempty"`
    CurrentPersona string `json:"currentPersona,omitempty"`
    Refs WorkflowRunRefs `json:"refs,omitempty"`
    Steps []WorkflowRunStep `json:"steps,omitempty"`
    Summary string `json:"summary,omitempty"`
    LastActivityAt *metav1.Time `json:"lastActivityAt,omitempty"`
    CompletedAt *metav1.Time `json:"completedAt,omitempty"`
}

type WorkflowRunRefs struct {
    JiraKey string `json:"jiraKey,omitempty"`
    JiraURL string `json:"jiraURL,omitempty"`
    PRURL string `json:"prURL,omitempty"`
    Branch string `json:"branch,omitempty"`
    RCAURL string `json:"rcaURL,omitempty"`
}

type WorkflowRunStep struct {
    Name string `json:"name"`
    Persona string `json:"persona,omitempty"`
    TaskName string `json:"taskName,omitempty"`
    Phase string `json:"phase,omitempty"`
    StartedAt *metav1.Time `json:"startedAt,omitempty"`
    CompletedAt *metav1.Time `json:"completedAt,omitempty"`
    Summary string `json:"summary,omitempty"`
    Results map[string]string `json:"results,omitempty"`
}
```

This CRD is intentionally a denormalized status index. The source of truth for
execution remains `Task`, `AgentSession`, and `AgentTurn`.

### New lineage metadata

Every workflow Task should carry:

- `kelos.dev/workflow-id`
- `kelos.dev/workflow-kind`
- `kelos.dev/root-task`
- `kelos.dev/parent-task`
- `kelos.dev/handoff`
- `kelos.dev/persona`
- `kelos.dev/lineage-depth`

Slack-origin workflows also keep:

- `kelos.dev/slack-reporting`
- `kelos.dev/slack-channel`
- `kelos.dev/slack-thread-ts`
- `kelos.dev/slack-user-id`

### New result-driven handoff API

Add `spec.handoffs[]` to `TaskSpawnerSpec`.

```go
type TaskHandoff struct {
    Name string `json:"name"`
    TerminalPhases []TaskPhase `json:"terminalPhases,omitempty"`
    When TaskHandoffWhen `json:"when,omitempty"`
    Inherit TaskHandoffInherit `json:"inherit,omitempty"`
    Loop *TaskHandoffLoop `json:"loop,omitempty"`
    TaskTemplate TaskTemplate `json:"taskTemplate"`
}

type TaskHandoffWhen struct {
    Results []TaskHandoffResultPredicate `json:"results,omitempty"`
}

type TaskHandoffResultPredicate struct {
    Key string `json:"key"`
    Operator string `json:"operator,omitempty"` // Equals, NotEquals, Exists, Matches
    Value string `json:"value,omitempty"`
}

type TaskHandoffInherit struct {
    SlackThread bool `json:"slackThread,omitempty"`
    SourceMetadata bool `json:"sourceMetadata,omitempty"`
    Workflow bool `json:"workflow,omitempty"`
    ParentResults bool `json:"parentResults,omitempty"`
}

type TaskHandoffLoop struct {
    Name string `json:"name,omitempty"`
    MaxAttempts int32 `json:"maxAttempts,omitempty"`
    IncrementAttempt bool `json:"incrementAttempt,omitempty"`
    SubjectResultKey string `json:"subjectResultKey,omitempty"` // e.g. pr.url
}
```

The controller creates a child Task only when:

- parent Task is terminal
- parent phase matches the rule
- all result predicates match
- no child already exists for `(parent UID, handoff name)`
- loop/depth limits pass
- TaskSpawner is not suspended
- concurrency and total-task budgets allow it

### Handoff template variables

When rendering a child handoff `TaskTemplate`, expose:

```text
.Parent.Name
.Parent.Phase
.Parent.Results
.Parent.Outputs
.Parent.Labels
.Parent.Annotations
.Workflow.ID
.Workflow.Kind
.Workflow.Refs
.Lineage.Depth
.Lineage.RootTask
.Handoff.Name
.Handoff.Attempt
```

Do not require child prompts to scrape Slack thread text to understand what
happened. They should receive structured parent results and workflow refs.

### Deterministic Slack status/history commands

Add built-in command handling in `kelos-slack-server` before normal TaskSpawner
routing:

```text
@cody !status
@cody !status ALPM-123
@cody !status <PR URL>
@cody !history recent
@cody !history me
@cody !history service <name>
@cody !timeline ALPM-123
@cody !active
```

Rules:

- `!status` inside a thread resolves by Slack channel/root timestamp.
- `!status ALPM-123` resolves by `WorkflowRun.status.refs.jiraKey`.
- `!status <PR URL>` resolves by `WorkflowRun.status.refs.prURL`.
- `!history recent` lists recent workflows for the same Slack channel.
- `!history me` lists workflows started by the requesting Slack user.
- `!active` lists active workflows visible in the current channel.
- Broad history replies may be compact channel/thread responses in non-prod.
  Ephemeral responses are preferred before production exposure, but are not a
  blocker for the first non-prod release.
- Thread-local status can post into the thread.
- No status/history command should spawn an agent Task.

## Implementation Plan

### Phase 1: WorkflowRun read model

Kelos code:

- Add `api/v1alpha1/workflowrun_types.go`.
- Regenerate deepcopy and CRD manifests.
- Add controller RBAC for `workflowruns` and `workflowruns/status`.
- Add `internal/controller/workflowrun_controller.go`.
- Watch `Task`, `AgentSession`, and `AgentTurn`.
- Aggregate workflow status from labels, annotations, task phase, session
  phase, turn phase, and structured results.
- Backfill a `WorkflowRun` for Slack-origin Tasks that do not yet have one.
- Do not backfill non-Slack TaskSpawner sources in the first release.
- Delete terminal `WorkflowRun` resources one week after `status.completedAt`.

Tests:

- creates WorkflowRun from first Slack-origin Task
- does not create WorkflowRun for non-Slack sources in the first release
- updates phase when child Tasks run/succeed/fail
- extracts `jira.key`, `jira.url`, `pr.url`, `branch`, `rca.url`
- preserves history after child Tasks complete
- deletes terminal WorkflowRuns after one week
- does not expose secret-looking result keys in summaries

### Phase 2: Slack status/history control plane

Kelos code:

- Add `internal/slack/controlplane.go`.
- Add command parser for `!status`, `!history`, `!timeline`, and `!active`.
- Add `WorkflowRunQuery` helper that queries by:
  - Slack thread
  - Jira key
  - PR URL
  - requesting Slack user
  - channel
  - active phase
- Add Slack block/text renderers for compact status and expanded timeline.
- Call the control-plane handler at the start of `routeMessage`.
- Add `PostEphemeral` to the Slack messenger abstraction before production
  exposure of broad history. Non-prod rollout can use compact channel/thread
  responses while scoped by channel and user.
- Add configured Slack user/group allowlist for cross-channel PR/Jira lookup.

Tests:

- `!status` in a thread returns the matching WorkflowRun
- `!status ALPM-123` returns matching Jira workflow
- `!status <PR URL>` returns matching PR workflow
- `!history me` scopes to requester
- `!history recent` scopes to current channel
- query-by-Jira/PR outside the current channel is denied unless the requester
  is allowlisted
- unknown workflow returns a deterministic "not found" response
- control-plane commands do not create Tasks

### Phase 3: Result-driven handoff API

Kelos code:

- Extend `api/v1alpha1/taskspawner_types.go` with `Handoffs []TaskHandoff`.
- Add validation for handoff names, max item counts, predicate operators, and
  loop limits.
- Add `internal/controller/task_handoff_controller.go` or an adjacent
  reconciler path in the Task controller.
- Reuse `internal/taskbuilder.BuildTask` for child Task creation.
- Add child naming:

  ```text
  <parent-task-name>-<handoff-name>-<hash>
  ```

- Add idempotency by labels/annotations:
  - parent UID
  - handoff name
  - handoff template hash
- Inherit Slack reporting annotations when configured.
- Inherit workflow labels when configured.
- Set child `dependsOn` to the parent by default unless explicitly disabled.
- Update the relevant `WorkflowRun` when a child is created.

Tests:

- no handoff when `spec.handoffs` is empty
- handoff fires once on matching terminal result
- no duplicate child on repeated reconciles
- failed parent does not hand off unless configured
- inherited Slack annotations continue reporting in same thread
- loop max attempts stops babysitter cycles
- subject hash prevents a loop from switching PRs
- suspended TaskSpawner blocks new handoff children
- concurrency and total-task limits include handoff children

### Phase 4: Agent result-output helper

Kelos/runtime code:

- Add or document a deterministic helper for agents to emit additional results:

  ```text
  kelos-output set handoff.target ticket
  kelos-output set incident.fix_required true
  kelos-output set jira.key ALPM-123
  kelos-output set pr.url https://github.com/org/repo/pull/123
  kelos-output set-file incident.summary /tmp/summary.txt
  ```

- Ensure output keys use a restricted character set.
- Ensure multiline values are safely encoded or written through files.
- Keep existing `response` behavior for Slack terminal replies.

Tests:

- helper emits parseable `key: value` lines
- duplicate keys use last value
- multiline value handling does not break output markers
- response text remains backward-compatible

### Phase 5: Cody GitOps migration

`k8s-platform-gitops/non-prod/kelos` changes:

- Change `cody-debug-slack` from catch-all general Cody to explicit
  `^!debug\b`, or add a separate low-privilege `!ask` route if general
  question answering remains useful.
- Add or refine AgentConfigs:
  - `cody-incident-debugger`
  - `cody-ticket-creator`
  - `cody-dev`
  - `cody-pr-reviewer`
  - `cody-live-verifier`
  - `cody-rca-writer`
- Update each persona prompt to emit structured result keys instead of
  standalone Slack next-command lines.
- Add handoff rules:

| Parent persona | Matching result | Child persona |
| --- | --- | --- |
| debugger | `handoff.target=ticket` and `incident.fix_required=true` | ticket creator |
| ticket creator | `handoff.target=dev` and `jira.key` exists | feature builder |
| feature builder | `handoff.target=review` and `pr.url` exists | PR reviewer |
| PR reviewer | `handoff.target=verify` and `review.result=clean` | live verifier |
| live verifier | `handoff.target=rca` and `verification.result=passed` | RCA writer |
| PR babysitter reviewer | `handoff.target=dev-fix` and same `pr.url` | feature builder fix pass |

- Keep reviewer-to-dev automatic remediation only inside explicit babysitter
  workflows.
- Ensure Jira ticket creation happens before PR creation. No emergency hotfix
  workflow is part of the first implementation.

Validation:

- `kubectl kustomize non-prod/kelos`
- apply to non-prod
- trigger `@cody !debug ...`
- confirm WorkflowRun created
- confirm `@cody !status` works in thread
- confirm child Tasks inherit Slack thread and workflow labels
- confirm no Slack self-handoff is needed

### Phase 6: Remove Slack self-handoff bridge

After result-driven handoffs have run successfully in non-prod, remove the
Slack self-handoff code path.

Kelos code removals:

| File | Change |
| --- | --- |
| `internal/slack/self_handoff.go` | Delete. |
| `internal/slack/self_handoff_test.go` | Delete. |
| `internal/slack/handler.go` | Remove `selfHandoffEnabled`, `selfHandoffMaxPerThread`, `handleSelfHandoffEvent`, `HandleReportedTerminalMessage`, stop notice code, and self-handoff calls from message/app-mention handling. |
| `internal/reporting/watcher.go` | Remove `TerminalMessageHandler`, `handleTerminalSlackMessage`, and `terminalSlackMessageText` if no other caller needs terminal-message callbacks. |
| `cmd/kelos-slack-server/main.go` | Stop wiring `TerminalMessageHandler: handler.HandleReportedTerminalMessage`. |
| `internal/manifests/charts/kelos/values.yaml` | Remove `slackServer.selfHandoff`. |
| `internal/manifests/charts/kelos/templates/slack-server.yaml` | Remove `SLACK_SELF_HANDOFF_ENABLED` and `SLACK_SELF_HANDOFF_MAX_PER_THREAD`. |
| `internal/reporting/watcher_test.go` | Remove tests that only validate terminal-message self-handoff triggering; keep Slack reporting tests. |
| `internal/slack/handler_test.go` | Remove or rewrite tests that expect bot-authored handoff lines to route. |

Migration guard:

- Before deleting the bridge, set `SLACK_SELF_HANDOFF_ENABLED=false` in the
  target environment and run Cody workflows for at least one release window.
- If no workflows depend on bot-authored Slack handoff lines, delete the code.

## Incident Workflow Contract

The incident debugger should not create PRs. It should investigate and emit:

```text
incident.fix_required: true
incident.fix_type: code
incident.confidence: high
incident.summary: Package install fails because npm uses a GitHub App token without package read permission.
handoff.target: ticket
handoff.reason: Fix requires tracked code/config work.
```

The ticket creator creates Jira and emits:

```text
jira.key: ALPM-123
jira.url: https://wgen4.atlassian.net/browse/ALPM-123
handoff.target: dev
handoff.reason: Jira ticket is ready for implementation.
```

The feature builder opens a PR and emits:

```text
pr.url: https://github.com/quantum-wealth/k8s-platform-gitops/pull/123
branch: cody/ALPM-123-package-auth
handoff.target: review
```

The PR reviewer emits:

```text
review.result: clean
handoff.target: verify
```

or:

```text
review.result: changes-requested
handoff.target: dev-fix
```

The live verifier emits:

```text
verification.result: passed
verification.environment: non-prod
handoff.target: rca
```

The RCA writer emits:

```text
rca.summary: <short summary>
rca.url: https://...
workflow.result: complete
```

## Slack Status Output Shape

Compact `!status` example:

```text
Workflow: Investigate npm package install failure
State: Running - feature-builder
Started by: <@U123>
Thread: <slack permalink>
Jira: ALPM-123
PR: not opened yet

Steps:
1. debug - succeeded - confirmed package token issue
2. ticket - succeeded - created ALPM-123
3. dev - running - implementing fix

Next: wait for feature-builder to open a PR.
```

Expanded `!timeline` includes timestamps, Task names, persona labels, and
selected result keys.

`!history recent` should avoid long walls of text. Return the last 10 runs with
title, phase, persona, Jira, PR, and thread link.

## Security And Access Rules

- Status/history commands only read Kubernetes Kelos resources.
- Broad history defaults to current Slack channel scope.
- `!history me` filters by `spec.source.userID`.
- Query-by-Jira or query-by-PR should only return a workflow if it is visible
  in the current channel or the requester is in a configured Slack
  user/group allowlist.
- The allowlist should be configured through Helm values or environment-backed
  config on `kelos-slack-server`, not hard-coded in Go.
- Terminal WorkflowRuns are retained for one week after completion.
- Redact result keys matching secret-like patterns:
  - `token`
  - `password`
  - `secret`
  - `private_key`
  - `authorization`
- Do not include raw Pod logs in status/history responses.
- Do not expose full prompts by default; show title, summaries, refs, phases,
  and selected result keys.

## Rollout Order

1. Add `WorkflowRun` CRD and controller.
2. Enable `WorkflowRun` creation for Slack-origin Cody Tasks only.
3. Add Slack `!status`, `!history`, `!timeline`, and `!active`.
4. Add result-driven handoff API and controller.
5. Add output helper support for structured agent results.
6. Update Cody GitOps to emit structured results.
7. Disable Slack self-handoff in non-prod.
8. Validate debug -> ticket -> dev -> review -> verify -> RCA in one incident
   workflow.
9. Delete Slack self-handoff Go code and Helm values.
10. Rebaseline docs around `WorkflowRun` and handoff configuration.

## Acceptance Criteria

- A human can run `@cody !debug ...` and Kelos creates a `WorkflowRun`.
- `@cody !status` in the same thread returns the current workflow state without
  creating an agent Task.
- `@cody !history me` returns recent workflows started by the requesting Slack
  user.
- Debugger can hand off to ticket creator through `Task.status.results`.
- Ticket creator can hand off to feature builder only after emitting `jira.key`.
- Feature builder can hand off to reviewer only after emitting `pr.url`.
- Reviewer-to-dev fix loops only run inside explicit babysitter workflows.
- Every child Task carries workflow and lineage labels.
- Handoff reconciliation is idempotent.
- Slack self-handoff can be disabled without breaking structured handoffs.
- The old self-handoff code is removed after migration.

## Open Questions

- Which concrete Slack user IDs and group IDs should be placed in the
  cross-channel PR/Jira lookup allowlist?
