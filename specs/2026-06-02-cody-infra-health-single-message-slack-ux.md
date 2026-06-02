# Cody Infra Health Single-Message Slack UX Spec

Status: Draft
Date: 2026-06-02
Owner: Cody / Kelos

## Summary

Improve the proactive infra-health Slack UX so each detected infra issue is
reported as one top-level Slack message that is updated in place for the full
investigation lifecycle.

The first material progress snapshot becomes a stable detected-issues section.
Later progress updates are rendered below it in the same message. When the task
finishes, Kelos updates the same message with the final RCA, fix status, PR
links, and any manual follow-up.

This behavior is opt-in and should only be enabled for the infra-health cron
TaskSpawner created for non-prod/qa.

## Goals

- Keep no-op scheduled runs silent.
- For material infra-health findings, create exactly one top-level Slack
  message in the configured destination channel.
- Keep the initial detected-issues section stable for the life of the message.
- Stream/update investigation progress below the stable section.
- Replace the live progress section with a final RCA/fix summary when the Task
  succeeds or fails.
- Avoid creating duplicate fix PRs when an earlier infra-health run already has
  a matching open PR for the same environment/symptoms.
- Avoid changing existing Slack-originated Cody behavior, including replies in
  user-created Slack threads.

## Non-Goals

- Do not add thread replies for infra-health cron tasks.
- Do not change Slack reporting for normal `kelos.dev/slack-reporting=enabled`
  tasks with an originating Slack thread.
- Do not add a new CRD field.
- Do not solve alert-level dedupe or incident correlation in this spec.
- Do not require Cody task pods to call Slack directly.

## Current Behavior

For proactive/deferred tasks, `kelos-slack-server` waits until Cody emits the
first meaningful progress snapshot. It then:

1. resolves `kelos.dev/slack-destination`;
2. posts a top-level Slack message;
3. stores the message timestamp on the Task;
4. updates that same message for later progress/final states.

This is already close to the target shape, but every update rebuilds the whole
message from the latest progress or final output. The originally detected issue
is not explicitly pinned as stable content.

Relevant code:

- `internal/reporting/watcher.go`
- `internal/reporting/slack.go`
- `internal/reporting/activity.go`

## Proposed Behavior

Add an opt-in Slack layout annotation on the infra-health Task template:

```yaml
metadata:
  annotations:
    kelos.dev/slack-reporting: deferred
    kelos.dev/slack-destination: asd
    kelos.dev/slack-layout: infra-health-single-message
```

Only tasks with both of these conditions use the new layout:

- `kelos.dev/slack-reporting=deferred`
- `kelos.dev/slack-layout=infra-health-single-message`

All other Slack reporting paths keep the current behavior.

### Message Lifecycle

1. **No issue found**
   - Cody emits no progress.
   - Kelos posts nothing.

2. **Issue detected**
   - Cody emits its first material progress snapshot, formatted as the detected
     issue summary.
   - Kelos posts one top-level Slack message.
   - Kelos stores the stable summary on the Task annotation, truncated to a
     bounded length.

3. **Investigation progress**
   - Later progress snapshots update the same Slack message.
   - The detected issue summary remains unchanged at the top.
   - Current work/activity appears below it.

4. **Final result**
   - On `Succeeded` or `Failed`, Kelos updates the same Slack message.
   - The final message keeps the stable detected issue section, then shows:
     - RCA;
     - fix applied or blocked/manual follow-up;
     - PR links when present;
     - failure reason when the task failed.

## Reporter Changes

Add internal annotation constants:

- `kelos.dev/slack-layout`
- `kelos.dev/slack-stable-summary`

Add a deferred-only branch in `SlackTaskReporter`:

- `updateDeferredProgress`:
  - if layout is `infra-health-single-message` and no stable summary exists,
    treat the first progress text as the stable summary;
  - persist the stable summary annotation;
  - post a root message using the infra-health formatter;
  - store the message timestamp exactly as today.
- `updateProgress`:
  - if layout is `infra-health-single-message`, update the root message with:
    stable summary + latest progress + context/activity.
- terminal reporting:
  - if layout is `infra-health-single-message`, update the root message with:
    stable summary + final response + PR/failure metadata;
  - do not post continuation thread replies for this layout.

Keep the existing progress timestamp behavior. For this layout, the progress
timestamp is the root message timestamp.

## Formatter Changes

Add small formatter helpers in `internal/reporting/slack.go`:

- `FormatInfraHealthProgressMessage(stableSummary, currentProgress, taskName)`
- `FormatInfraHealthFinalMessage(stableSummary, phase, taskName, message, results)`

Suggested Block Kit shape:

```text
Infra health issue detected
[stable detected issue summary]

Investigation
[latest progress or activity]

Task: ...
```

Final shape:

```text
Infra health investigation complete
[stable detected issue summary]

RCA / Fix / PRs
[final Cody response and PR links]

Task: ...
```

The formatter should keep a single Slack message. If the final response is too
large, truncate the rendered response with an explicit note rather than posting
thread spillover for this layout.

## Skills Change

Update only the infra-health cron TaskSpawner in `quantum-wealth/skills`:

- add `kelos.dev/slack-layout: infra-health-single-message`;
- tighten the prompt so the first emitted assistant progress message is a short
  detected-issues summary, not a generic "working" update;
- add an immediate duplicate-PR guard before Cody creates or materially updates
  any fix branch.

### Immediate Duplicate-PR Guard

This prompt-only guard does not prevent a scheduled Task from starting, but it
prevents repeated fix PRs while a previous run's remediation is still open.

Before creating a branch or PR, Cody should:

1. Identify the affected environment, namespace, services, and primary symptoms.
2. Search likely GitHub repos for open PRs created by Cody or tagged as
   infra-health work.
3. Treat a PR as matching when its title, body, branch name, or labels mention:
   - `infra-health`;
   - `non-prod/qa` or `qa`;
   - one or more affected services;
   - the same material symptom class, such as `CrashLoopBackOff`,
     `ImagePullBackOff`, rollout failure, missing env/config, or ExternalSecret
     sync failure.
4. If a matching open PR exists:
   - do not create a new branch;
   - do not create a duplicate PR;
   - update Slack with the existing PR link and a short current-state summary;
   - inspect the existing PR only enough to decide whether it still plausibly
     addresses the active symptoms;
   - stop unless there is a clearly separate issue outside the existing PR's
     scope.
5. If no matching PR exists, create the fix PR with stable searchable metadata.

PRs created by this workflow should use searchable title/body metadata, for
example:

```text
Title: fix(ALPM-23769): restore qa service config

Body metadata:
Cody-Infra-Health: non-prod/qa
Cody-Infra-Health-Services: portfolio-management, compliance-service, order-service
Cody-Infra-Health-Symptoms: CrashLoopBackOff, ImagePullBackOff, rollout-failed
```

Example first progress shape Cody should emit only after finding a material
issue:

```text
Issue detected in non-prod/qa:
- <service/symptom/evidence>
- <service/symptom/evidence>

Investigating Datadog and Kubernetes evidence now.
```

## Tests

Add Kelos unit tests covering:

- deferred infra-health first progress posts one root message;
- stable summary is persisted on the Task;
- later progress updates the same message and preserves the stable summary;
- terminal success updates the same message with final RCA/PR content;
- terminal failure updates the same message with failure content;
- normal Slack-thread reporting is unchanged;
- normal deferred reporting without the new layout is unchanged;
- oversized final responses are truncated instead of split into thread replies.

## Rollout

1. Implement Kelos reporter/formatter changes.
2. Publish Kelos images/chart.
3. Update runtime GitOps to consume the new Kelos version.
4. Update only the infra-health TaskSpawner in `skills` with the new layout
   annotation.
5. Verify a no-op run remains silent.
6. Verify a material finding produces one top-level Slack message in `#asd` and
   updates the same message through final RCA.
