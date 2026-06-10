# Session Root Terminal Updates For `NO_SLACK` Turns

Status: draft implementation spec
Date: 2026-06-10
Owner: Cody / Kelos

## Summary

Change `session-summary-root` Slack behavior so a terminal `AgentTurn` whose
result starts with `NO_SLACK:` still updates an existing session-owned Slack
root message.

The prefix should continue to suppress noisy terminal detail posts. It should
not leave an already-created session root stuck on stale live activity such as
`Running command: ...`.

This is scoped to non-Slack-originating `AgentSession`s that use:

```yaml
kelos.dev/slack-reporting: deferred
kelos.dev/slack-layout: session-summary-root
```

Slack-originated sessions and one-shot `Task` reporting remain unchanged.

## Problem

The current session-owned Slack surface spec says:

> `NO_SLACK:` turns remain silent and do not create/update the surface.

That was useful when a no-op turn had not posted anything yet. It is wrong once
a session root already exists.

Observed behavior from the Aikido security run on 2026-06-10:

- Aikido session turns posted/updated root messages while running.
- The turns completed successfully in the app-server logs.
- Their `AgentTurn.status.phase` became `Succeeded`.
- Their final `status.resultText` started with `NO_SLACK:`.
- Their `status.slackProgressMessageTS` was set to the root message timestamp.
- Their `status.slackAgentMessageTS` remained empty.
- Slack roots stayed visually stuck on the last activity command.

Example affected turns:

- `cody-aikido-security-main-aikido-sess-61407079c9ff-t-0001`
- `cody-aikido-security-main-aikido-sess-2ee67651e156-t-0001`
- `cody-aikido-security-main-aikido-sess-c2fe4f3f480b-t-0001`

The agent runtime was healthy. The bug is in terminal Slack reporting semantics.

## Goals

- Preserve `NO_SLACK:` as a way to avoid noisy Slack details.
- Do not create a brand-new Slack root solely for a terminal `NO_SLACK:` result.
- If a session Slack root already exists, update it on terminal `NO_SLACK:`.
- Strip the `NO_SLACK:` prefix before adding concise root text.
- Mark the turn as Slack-terminal-reported after the root update.
- Keep thread detail replies suppressed for `NO_SLACK:`.
- Keep Slack-originated session behavior unchanged.
- Keep non-session deferred one-shot behavior unchanged.

## Non-Goals

- Do not change Aikido prompt behavior in this spec.
- Do not change the session lifecycle, max age, idle timeout, or heartbeat model.
- Do not add a persistent artifact store.
- Do not change Slack route resolution.
- Do not modify one-shot `Task` reporting.
- Do not post full `NO_SLACK:` content into the thread.

## Current Runtime Logic

For an `AgentTurn`, `SlackTurnReporter.ReportTurnStatus` computes a Slack phase:

- `Queued` / `Running` -> `accepted`
- `Succeeded` -> `succeeded`
- `Failed` / `Canceled` -> `failed`

For `session-summary-root` turns, terminal handling flows through:

```go
reportSessionSummaryTerminalTurn(ctx, turn, desiredPhase)
```

Current early return:

```go
if suppressDeferredSlackTurn(turn) {
    return nil
}
```

`suppressDeferredSlackTurn` returns true when:

```go
strings.HasPrefix(strings.TrimSpace(turn.Status.ResultText), "NO_SLACK:")
```

Because this check happens before loading the session or root state:

- existing roots are not updated;
- `status.slackAgentMessageTS` is not set;
- the reporting loop keeps seeing a terminal turn that has not been terminally
  reported, but keeps returning early;
- the user sees stale `Latest` content forever.

## Proposed Contract

### Terminal `NO_SLACK:` With No Existing Session Root

If the terminal turn starts with `NO_SLACK:` and no session Slack root exists:

- do not create a Slack root;
- do not post thread details;
- set no Slack message timestamp;
- preserve current silent no-op behavior.

This keeps no-issue/no-material-output sessions quiet.

### Terminal `NO_SLACK:` With Existing Session Root

If the terminal turn starts with `NO_SLACK:` and
`AgentSession.status.slack.rootTS` exists:

- update that root message in place;
- append a concise summary line derived from the stripped result text;
- set `Latest` to a concise terminal status derived from the stripped result
  text;
- do not post any thread replies;
- set both:
  - `turn.status.slackProgressMessageTS = rootTS`
  - `turn.status.slackAgentMessageTS = rootTS`
- clear the reporter activity cache for the turn.

This makes Slack accurately show that the turn finished without adding detailed
noise to the thread.

### Terminal `NO_SLACK:` With Existing Root From A Prior Turn

If a later turn is `NO_SLACK:` and the session root exists from a previous
material turn, use the same behavior as above:

- update the root;
- suppress thread details.

Reason: the root is the session dashboard. Once it exists, terminal turns should
not leave it stale.

## Root Text Rules

Add a helper that strips the control prefix:

```go
func noSlackResultText(turn *kelosv1alpha1.AgentTurn) (string, bool)
```

Behavior:

- trim whitespace;
- detect exact prefix `NO_SLACK:`;
- return the remainder trimmed;
- if the remainder is empty, fall back to a short phase message such as
  `Turn finished without a reportable Slack update.`;
- do not include `NO_SLACK:` in Slack-visible text.

For session root content:

- `Summary`: append one compact line from stripped text using existing
  `appendSessionSummary`.
- `Latest`: use stripped text via existing `compactSessionLine`.
- continue existing summary de-dupe and max-lines behavior.

Example final root:

```text
aikido:Aikido issue group 23991044

Summary
Session started.
- Aikido group `23991044` appears already remediated on latest `main`; no new
  PR was created.

Latest
Aikido group `23991044` appears already remediated on latest `main`; no new PR
was created.

Task: cody-aikido-security-main-aikido-sess-2ee67651e156-t-0001
```

No thread replies are posted for this terminal result.

## Implementation Plan

Files:

- `internal/reporting/slack_turn.go`
- `internal/reporting/slack_turn_test.go`

No CRD, chart, or skills changes are required.

### Reporter Changes

Change `reportSessionSummaryTerminalTurn` only.

Pseudo-flow:

```go
func (tr *SlackTurnReporter) reportSessionSummaryTerminalTurn(...) error {
    noSlackText, noSlack := noSlackResultText(turn)

    if tr.hasDeferredTerminalPost(turn.UID) {
        return nil
    }

    session, err := tr.getSession(...)
    if err != nil {
        return err
    }

    if noSlack && (session.Status.Slack == nil || session.Status.Slack.RootTS == "") {
        return nil
    }

    var summaryAddition string
    var latest string
    var details bool
    var results map[string]string

    if noSlack {
        summaryAddition = noSlackText
        latest = compactSessionLine(noSlackText, 700)
        details = false
        results = nil
    } else {
        results = turnSlackResults(turn)
        details = hasSlackTerminalDetails(desiredPhase, turn.Status.Message, results)
        summaryAddition = terminalSessionSummary(turn, desiredPhase)
        latest = terminalSessionLatest(turn, desiredPhase, details)
    }

    summary := appendSessionSummary(sessionSlackSummary(session), summaryAddition)
    if summary == "" {
        summary = initialSessionSummary(session)
    }

    channel, rootTS, err := tr.upsertSessionSummaryRoot(...)
    if err != nil || channel == "" || rootTS == "" {
        return err
    }

    if details {
        post full details to root thread
    }

    mark deferred terminal post
    patch turn SlackProgressMessageTS and SlackAgentMessageTS to rootTS
    clear activity
}
```

Important detail: for `noSlack && no existing root`, return before calling
`upsertSessionSummaryRoot`, because `upsertSessionSummaryRoot` creates roots.

### Helper Naming

Avoid reusing `suppressDeferredSlackTurn` as the controlling abstraction for
session-summary terminal behavior.

Suggested options:

- keep `suppressDeferredSlackTurn` for non-session deferred behavior;
- add `noSlackResultText`;
- in the session-summary path, interpret `NO_SLACK:` as
  `suppress thread details, but update existing root if present`.

## Tests

Add/update tests in `internal/reporting/slack_turn_test.go`.

### Existing Non-Session Deferred Behavior

Keep the current test:

- `TestSlackTurnReporter_DeferredNoSlackFinalStaysSilent`

This validates that one-shot/deferred non-session `NO_SLACK:` still posts
nothing.

### New Session-Root Existing Surface Test

Add a test:

```text
TestSlackTurnReporter_SessionSummaryNoSlackFinalUpdatesExistingRoot
```

Setup:

- `AgentSession.status.slack.rootTS = "root-ts"`
- `AgentSession.status.slack.channelID = "C123"`
- `AgentSession.status.slack.summary = "Session started."`
- terminal `AgentTurn`:
  - `spec.sessionRef.name` set
  - `phase = Succeeded`
  - `resultText = "NO_SLACK: issue already remediated"`
  - annotation `kelos.dev/slack-reporting: deferred`
  - annotation `kelos.dev/slack-layout: session-summary-root`
  - label `kelos.dev/slack-reporting: enabled`

Expected:

- `UpdateMessage` called once with `channel=C123`, `ts=root-ts`;
- `PostMessage` not called;
- `PostThreadReply` not called;
- updated root text contains `issue already remediated`;
- updated root text does not contain `NO_SLACK:`;
- turn status has:
  - `slackProgressMessageTS = root-ts`
  - `slackAgentMessageTS = root-ts`.

### New Session-Root No Existing Surface Test

Add a test:

```text
TestSlackTurnReporter_SessionSummaryNoSlackFinalWithoutRootStaysSilent
```

Setup:

- session has no `status.slack.rootTS`;
- terminal turn starts with `NO_SLACK:`.

Expected:

- no `PostMessage`;
- no `UpdateMessage`;
- no `PostThreadReply`;
- `slackAgentMessageTS` remains empty.

### Material Terminal Behavior Regression Test

Keep or add coverage that non-`NO_SLACK:` terminal session-summary turns still:

- update or create the root;
- post full details in the thread when details exist;
- mark `slackAgentMessageTS`.

## Rollout

Kelos-only change.

Expected manual rollout after merge:

```bash
REGISTRY=docker.io/alpheya VERSION=main IMAGE_PLATFORMS=linux/amd64 PUSH=true \
  make image WHAT=cmd/kelos-slack-server

helm package internal/manifests/charts/kelos --destination /tmp
helm push /tmp/kelos-cody-<version>.tgz oci://registry-1.docker.io/alpheya

kubectl -n kelos-system rollout restart deployment/kelos-slack-server
kubectl -n kelos-system rollout status deployment/kelos-slack-server
```

No CRD apply is expected for this change.

## Verification

After rollout, run a session-backed workflow that produces:

1. progress/root activity;
2. a final `NO_SLACK:` result.

Verify:

- session root no longer shows the last command as active work;
- root summary/latest show a concise terminal no-op/remediated outcome;
- no full terminal thread detail is posted;
- `AgentTurn.status.slackAgentMessageTS` is set to the session root timestamp.

For Aikido security, the expected UX is:

- if the agent finds an issue already remediated, the existing root says so;
- the thread does not get a long duplicate explanation;
- the turn is terminally reported and does not keep retrying silently.

## Risks

- If the stripped `NO_SLACK:` text is long, it can still make the root noisy.
  Mitigation: use existing compacting helpers and summary line caps.
- If the existing root was created from transient activity for a truly no-op
  turn, this change will still update that root with a terminal no-op. That is
  intentional because the user has already seen the root.
- If a workflow uses `NO_SLACK:` to mean "never reveal anything in Slack",
  it should avoid creating progress/root activity in the first place or use a
  different control prefix in a future spec.

## Open Questions

- Should the final `Latest` be the stripped `NO_SLACK:` text or a generic
  phrase like `Turn completed without reportable changes`?
- Should `NO_SLACK:` terminal root updates be optional per workflow, or should
  the session-owned root contract always prefer accurate terminal state once a
  root exists?

