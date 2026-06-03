# Cody Session-Owned Slack Surface Implementation Spec

Status: draft implementation spec
Date: 2026-06-03
Owner: Cody / Kelos

## Summary

Give non-Slack-originating `AgentSession`s one simple Slack surface owned by the
session:

```text
Session root message
  Summary
    concise additive summary of what this session has found/done
  Latest
    live/streaming active work currently happening

Session thread
  full turn posts and details
```

The AI supplies content. Kelos owns the Slack shape.

This is for sessions that do not already have a Slack thread, such as cron,
webhook, or proactive sessions. Slack-originated sessions already have a human
Slack thread and should keep their existing UX.

## Goals

- One Slack root/thread per non-Slack-originating session.
- Root message has only two content sections:
  - `Summary`
  - `Latest`
- `Summary` is initialized when the session Slack surface is created.
- `Summary` is updated at the end of every material turn, very concisely and
  additively.
- `Latest` streams active work while a turn is running.
- Full turn output is always posted in the session thread.
- `NO_SLACK:` turns remain silent and do not create/update the surface.
- Slack-originated sessions remain unchanged.
- One-shot `Task` reporting remains unchanged.

## Non-Goals

- Do not keep `Detected issue` / `Outcome` as session-root concepts.
- Do not make this infra-health-specific.
- Do not solve cron stale-tick catch-up in this spec.
- Do not add an incident store, artifact store, or cross-session handoff.
- Do not let Cody pods call Slack APIs directly.

## Current State

### Slack-Originated Sessions

Slack sessions already have a Slack thread at creation time:

- `AgentSession.spec.source.type: SlackThread`
- `AgentSession.spec.source.channelID`
- `AgentSession.spec.source.rootTS`
- every `AgentTurn` gets `kelos.dev/slack-channel` and
  `kelos.dev/slack-thread-ts`

`SlackTurnReporter` replies in the existing Slack thread. This is already one
thread per session and should not be changed.

### Non-Slack-Originating Sessions

Cron sessions currently have no Slack thread identity:

- `AgentSession.spec.source.type: Cron`
- `AgentSession.spec.source.key`
- `AgentSession.spec.source.schedule`

Slack routing comes from the rendered turn annotations:

```yaml
kelos.dev/slack-reporting: deferred
kelos.dev/slack-destination: cody-devops
kelos.dev/slack-layout: stable-summary-root
```

When a deferred `AgentTurn` finishes, `SlackTurnReporter` can create a top-level
Slack root message and store its timestamp on that turn. The session itself does
not own that Slack surface yet.

## Proposed Model

### Session Slack Surface

Add optional Slack surface status to `AgentSession.status`.

File:

- `api/v1alpha1/agentsession_types.go`

Sketch:

```go
type AgentSessionStatus struct {
    // existing fields...

    // Slack records the Slack surface owned by this session when Kelos creates
    // one for a non-Slack-originating session.
    // +optional
    Slack *AgentSessionSlackStatus `json:"slack,omitempty"`
}

type AgentSessionSlackStatus struct {
    // ChannelID is the Slack channel containing the session root message.
    // +optional
    ChannelID string `json:"channelID,omitempty"`

    // RootTS is the root message timestamp for the session Slack thread.
    // +optional
    RootTS string `json:"rootTS,omitempty"`

    // Destination is the logical Slack route used to create the root.
    // +optional
    Destination string `json:"destination,omitempty"`

    // Layout is the root layout. Expected first value: session-summary-root.
    // +optional
    Layout string `json:"layout,omitempty"`

    // Summary is the concise additive session summary shown in the root.
    // +optional
    Summary string `json:"summary,omitempty"`

    // Latest is the latest active work/status shown in the root.
    // +optional
    Latest string `json:"latest,omitempty"`

    // LastPostedTurn is the most recent turn that posted full details.
    // +optional
    LastPostedTurn string `json:"lastPostedTurn,omitempty"`

    // LastPostedSequence is the sequence of the most recent posted turn.
    // +optional
    LastPostedSequence int32 `json:"lastPostedSequence,omitempty"`
}
```

Why status:

- Slack-originated thread identity is part of `spec.source`.
- Runtime-created Slack roots are observed state.
- `status.lastAgentMessageTS` stays focused on context-window behavior.

### Layout

Use a generic layout annotation:

```yaml
kelos.dev/slack-layout: session-summary-root
```

The root message shape is always:

```text
<session title>

Summary
<concise additive session summary>

Latest
<streaming active work, or final latest status>

Task: <latest turn name>
```

No `Detected issue`. No `Outcome`.

For deferred workflows that should remain silent on no-op turns, the Slack
surface is created on the first material reportable turn, not necessarily when
the Kubernetes `AgentSession` object is created.

## Behavior

### Surface Resolution

For each `AgentTurn`, resolve Slack target in this order:

1. If the turn has explicit Slack channel/thread annotations, use those.
   - Preserves Slack-originated sessions.

2. Else, if the session source is `SlackThread`, use
   `session.spec.source.channelID/rootTS`.
   - Defensive fallback for Slack-originated sessions.

3. Else, if `session.status.slack.rootTS` exists, use that session-owned root.

4. Else, if the turn is deferred and has a Slack destination, create the
   session root and write `session.status.slack`.

5. Else, do not post.

### Running Turn

When a non-Slack-originating deferred session turn is running:

- if the turn produces activity/progress text, update
  `session.status.slack.latest`;
- update the root message's `Latest` section in place;
- do not post thread replies for progress;
- do not modify `Summary` during streaming work.

If no session Slack surface exists yet:

- create it only if this is a material/reportable turn;
- initialize `Summary` from the first concise progress text when available;
- initialize `Latest` from the same progress text or current activity.

### Terminal Turn

When a material turn finishes:

- append/update `Summary` very concisely;
- clear or replace `Latest` with a concise final status for that turn;
- update the root message in place;
- post the full terminal turn details in the root thread;
- set `lastPostedTurn` and `lastPostedSequence`.

The additive summary should stay short. It should not paste the full RCA.

Example:

```text
Summary
QA rollout issue covered by k8s-apps-gitops#8598. Preview worker issue covered
by #8673. Integration ai-api-worker fix opened in #8685.

Latest
Integration turn finished: opened #8685 for missing ai-api-worker service
account and overlay.
```

### Silent Turns

If `status.resultText` starts with `NO_SLACK:`:

- do not create a session Slack surface;
- do not update `Summary`;
- do not update `Latest`;
- do not post details;
- keep existing suppression semantics.

### Full Turn Details

Every material terminal turn posts its full response in the session thread.

Use existing long-message splitting:

- markdown to Slack blocks;
- split across multiple thread replies when needed;
- preserve current `FormatSlackTransitionMessage` behavior for details.

The root message is only the compact session dashboard.

## Reporter Changes

Files:

- `internal/reporting/slack_turn.go`
- `internal/reporting/slack.go`
- `internal/reporting/slack_turn_test.go`
- `internal/reporting/slack_test.go`

### SlackTurnReporter

Change only session `AgentTurn` reporting.

Rules:

- explicit Slack thread annotations keep current behavior;
- sessionRef empty keeps old deferred behavior for compatibility;
- sessionRef set and deferred reporting uses session-owned surface;
- `NO_SLACK:` suppresses before creating/updating any root.

Add helpers:

```go
type SlackSurface struct {
    ChannelID string
    RootTS string
    Destination string
    Layout string
    Summary string
    Latest string
}
```

Suggested helper functions:

- `loadTurnSession(ctx, turn)`
- `resolveSessionSlackSurface(turn, session)`
- `ensureSessionSlackSurface(ctx, turn, session, initialSummary, latest)`
- `updateSessionSlackSurface(ctx, session, mutate)`
- `postTurnDetails(ctx, surface, turn)`

### Formatter

Add one generic formatter:

```go
FormatSessionSummaryRootMessage(title, summary, latest, taskName string) SlackMessage
```

The formatter is deterministic. It should:

- render title header;
- render `Summary` only if non-empty;
- render `Latest` only if non-empty;
- render task/session context;
- never duplicate the same text in multiple sections;
- keep root content compact.

Remove infra-health-specific assumptions from the session root path.

## API And Generation Work

Kelos files:

- `api/v1alpha1/agentsession_types.go`
- `api/v1alpha1/zz_generated.deepcopy.go`
- `internal/manifests/install-crd.yaml`
- `internal/manifests/charts/kelos/templates/crds/agentsession-crd.yaml`
- `internal/manifests/charts/kelos/Chart.yaml` if publishing a chart

Run:

```bash
make update
```

## Tests

Add tests for:

- Slack-originated session still replies to existing thread.
- Deferred cron/session turn creates one session-owned root.
- Running progress updates `Latest` only.
- Terminal turn updates concise additive `Summary`.
- Terminal turn posts full details in the thread.
- Later material turn updates the same root, not a new root.
- `NO_SLACK:` turn creates no root and posts nothing.
- Formatter renders only `Summary` and `Latest`.
- Formatter does not duplicate identical text.

## Deployment

Merge Kelos first.

Manual rollout:

```bash
REGISTRY=docker.io/alpheya VERSION=main IMAGE_PLATFORMS=linux/amd64 PUSH=true \
  make image WHAT=cmd/kelos-slack-server

helm package internal/manifests/charts/kelos --destination /tmp
helm push /tmp/kelos-cody-<version>.tgz oci://registry-1.docker.io/alpheya

kubectl apply -f internal/manifests/install-crd.yaml
kubectl -n kelos-system rollout restart deployment/kelos-slack-server
kubectl -n kelos-system rollout status deployment/kelos-slack-server
```

Skills follow-up:

- replace `kelos.dev/slack-layout: stable-summary-root` with
  `kelos.dev/slack-layout: session-summary-root` for infra-health session
  spawners.

## Backward Compatibility

Slack-originated sessions:

- unchanged;
- still use `spec.source.channelID/rootTS`;
- still reply in the human Slack thread.

One-shot tasks:

- unchanged;
- still use `SlackTaskReporter`.

Deferred non-session turns:

- unchanged unless they have `sessionRef.name`.

Existing cron sessions:

- new status field is optional;
- if a previous turn already posted a root, the reporter may recover the root
  from that turn before creating a new one.

## Separate Follow-Up

Cron stale-tick catch-up still needs its own small fix:

- make cron session mode respect `spec.when.cron.startingDeadlineSeconds`;
- drop stale discovered ticks;
- set infra-health `maxQueuedTurns: 1` defensively until that fix is deployed.
