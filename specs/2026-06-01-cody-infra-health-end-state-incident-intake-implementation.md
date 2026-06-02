# Cody Infra Health End-State Incident Intake Implementation Spec

Status: Draft
Date: 2026-06-01
Owner: Cody / Kelos / Platform

## Summary

Implement the end-state Cody infra-health system after the simple generic
webhook version proves useful.

The end-state version replaces direct `Alertmanager -> genericWebhook ->
TaskSpawner` execution with a dedicated incident-intake control loop:

- observability events are normalized into a common signal envelope;
- related events are correlated into durable incidents;
- Slack lifecycle updates are owned by the platform service, not by prompt
  convention;
- Kelos/Cody remains the investigation and PR-generation runtime;
- retry, replay, dedupe, cooldown, and routing are explicit platform behavior.

This spec assumes the simple version already exists and has validated:

- infra alerts opted in with `cody="true"` are useful Cody triggers;
- the Cody debugger workflow can produce good RCA;
- reviewable PR-only remediation is safe enough for non-prod;
- Slack lifecycle updates are valuable to operators.

## Goals

- Add durable incident state for Cody infra-health workflows.
- Support multiple event sources:
  - Alertmanager;
  - Grafana webhooks;
  - Flux notification events;
  - KRR/right-sizing reports;
  - later Datadog events.
- Normalize source-specific payloads into one incident signal model.
- Correlate alert storms into one Cody investigation.
- Keep Slack updates reliable, throttled, allowlisted, and threaded where
  possible.
- Keep Cody task execution bounded by severity, environment, cooldown, and
  concurrency policy.
- Route PR notifications to incident channels and repo/team-specific channels.
- Preserve the existing safety model: read-only Kubernetes diagnostics and
  PR-only remediation.
- Support replay of failed or suppressed incidents by platform operators.

## Non-Goals

- Do not auto-merge PRs.
- Do not directly mutate cluster resources.
- Do not add production auto-remediation without an approval policy.
- Do not make Cody responsible for incident dedupe or Slack routing through
  prompt instructions.
- Do not replace Alertmanager, Grafana, Flux, or Datadog as signal sources.
  Cody consumes their events.
- Do not build a full incident-management product in this pass. The incident
  model is only what Cody infra-health needs.

## Design Principle

Separate incident control from agent execution.

Kelos should own:

- signal ingestion;
- normalization;
- dedupe;
- incident state;
- Slack lifecycle delivery;
- task creation;
- final result routing.

Cody should own:

- investigation;
- evidence collection;
- RCA;
- deciding whether a PR is safe;
- Jira and PR creation when permitted.

## Target Architecture

```text
Alertmanager / Grafana / Flux / KRR / Datadog
  -> kelos-incident-intake
  -> normalize signal
  -> find or create InfraHealthIncident
  -> post or update Slack detected message
  -> create Cody Task when policy allows
  -> Cody investigates and writes structured task results
  -> incident controller reads task result
  -> post RCA update
  -> post PR updates to incident + repo channels
  -> mark incident resolved, escalated, or blocked
```

## API Additions

Add two Kelos CRDs.

### `InfraHealthMonitor`

Defines a monitored workflow.

Representative shape:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: InfraHealthMonitor
metadata:
  name: non-prod-infra-health
  namespace: kelos-system
spec:
  enabled: true
  environment: non-prod
  sources:
    - type: alertmanager
      path: /webhook/alertmanager/non-prod
    - type: flux
      path: /webhook/flux/non-prod
  correlation:
    groupBy:
      - environment
      - cluster
      - namespace
      - service
      - alertname
    dedupeWindow: 30m
    cooldown: 15m
  triagePolicy:
    requiredLabels:
      cody: "true"
    minSeverity: warning
    maxActiveIncidents: 10
  slack:
    defaultChannel: infra-poc
    channelAliases:
      infra-poc: C0123456789
      platform-infra: C0234567890
    repoRoutes:
      quantum-wealth/k8s-apps-gitops:
        - platform-infra
      quantum-wealth/k8s-platform-gitops:
        - platform-infra
  taskTemplate:
    type: codex
    credentials:
      type: oauth
      secretRef:
        name: cody-codex-credentials
    image: docker.io/alpheya/codex:main
    agentConfigRefs:
      - name: cody-debugger
      - name: cody-infra-health
      - name: cody-atlassian-mcp
    ttlSecondsAfterFinished: 86400
    podOverrides:
      labels:
        cody.alpheya.com/tools-client: "true"
      serviceAccountName: cody-debugger
  remediationPolicy:
    mode: pr-only
    productionRequiresApproval: true
    allowedRepos:
      - quantum-wealth/k8s-apps-gitops
      - quantum-wealth/k8s-platform-gitops
```

### `InfraHealthIncident`

Stores durable incident state.

Representative shape:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: InfraHealthIncident
metadata:
  name: non-prod-podcrashlooping-int-advisor-portal
  namespace: kelos-system
  labels:
    kelos.dev/monitor: non-prod-infra-health
    kelos.dev/environment: non-prod
    kelos.dev/severity: critical
spec:
  monitorRef:
    name: non-prod-infra-health
  fingerprint: sha256:...
  correlationKey: non-prod/alpheya-non-prod/int/advisor-portal/PodCrashLooping
  firstSeen: "2026-06-01T08:12:00Z"
  lastSeen: "2026-06-01T08:14:00Z"
  severity: critical
  state: investigating
  signal:
    source: alertmanager
    alertname: PodCrashLooping
    cluster: alpheya-non-prod
    namespace: int
    service: advisor-portal
    pod: advisor-portal-abc
    summary: Pod int/advisor-portal is crash looping
    sourceURL: https://grafana...
status:
  phase: Investigating
  activeTaskName: cody-infra-health-alerts-abc123
  slack:
    incidentChannel: infra-poc
    rootMessageTS: "1710000000.000001"
  rca:
    summary: ""
    confidence: ""
  pullRequests: []
  conditions: []
```

Incident `spec` captures the normalized source fact. Incident `status` captures
what Kelos did with it.

## Normalized Signal Envelope

Every source adapter should normalize into:

```json
{
  "source": "alertmanager",
  "sourceEventID": "optional provider id",
  "status": "firing",
  "environment": "non-prod",
  "cluster": "alpheya-non-prod",
  "namespace": "int",
  "service": "advisor-portal",
  "workload": "deployment/advisor-portal",
  "pod": "advisor-portal-abc",
  "container": "app",
  "alertname": "PodCrashLooping",
  "severity": "critical",
  "summary": "Pod int/advisor-portal is crash looping",
  "description": "Container app has restarted repeatedly",
  "fingerprint": "source-specific stable fingerprint",
  "sourceURL": "https://grafana...",
  "labels": {},
  "annotations": {},
  "rawPayloadRef": "optional configmap/object reference"
}
```

The intake service should reject or quarantine events that cannot produce:

- source;
- status;
- environment or monitor;
- alertname;
- severity;
- correlation key inputs.

For Alertmanager, the raw native webhook payload should be retained or
referenced so Cody and operators can inspect every field Alertmanager provided,
not only the normalized fields above.

## Correlation and Dedupe

The incident controller should:

1. Compute `correlationKey` from monitor `spec.correlation.groupBy`.
2. Look for an open incident with that key.
3. If found, update `lastSeen`, append the signal event, and avoid spawning a
   new Cody task when one is active or cooldown is in effect.
4. If not found, create a new incident.
5. If a resolved signal arrives, mark the incident `Monitoring` or `Resolved`
   depending on policy.

This fixes the simple version's main weakness: repeated alerts should update
one incident instead of spawning many tasks or being dropped by
`maxConcurrency`.

## Incident Phases

Use explicit phases:

- `New`
- `Queued`
- `Investigating`
- `RCAReady`
- `PRCreated`
- `Blocked`
- `Escalated`
- `Monitoring`
- `Resolved`
- `Suppressed`

Phase transitions should be driven by source events, task status, and parsed
task results.

## Task Creation

The incident controller creates a normal Kelos `Task` from the monitor's
`taskTemplate`.

The prompt should include:

- normalized incident envelope;
- correlated event timeline;
- current Slack thread URL if available;
- required output contract;
- remediation policy;
- repo/channel routing hints;
- source payload excerpts.

Task labels:

```yaml
kelos.dev/infra-health-monitor: non-prod-infra-health
kelos.dev/infra-health-incident: non-prod-podcrashlooping-int-advisor-portal
cody.alpheya.com/workflow: infra-health
cody.alpheya.com/source: incident-intake
```

Task owner reference should point to the `InfraHealthIncident`.

## Cody Output Contract

The Cody task must write structured outputs that the incident controller can
parse from `Task.status.results`.

Required keys:

```text
infra-health-status=rca_ready|pr_created|blocked|escalated|resolved
infra-health-rca-json=<base64 JSON>
infra-health-prs-json=<base64 JSON array>
infra-health-confidence=low|medium|high
infra-health-human-summary=<base64 markdown>
```

`infra-health-rca-json` shape:

```json
{
  "summary": "one sentence",
  "rootCause": "evidence-backed RCA",
  "evidence": [
    {
      "kind": "log|event|flux|config|metric|commit",
      "source": "where it came from",
      "excerpt": "short sanitized excerpt"
    }
  ],
  "fixType": "code|gitops|platform|operational|unknown",
  "confidence": "low|medium|high",
  "manualFollowUp": "optional"
}
```

`infra-health-prs-json` shape:

```json
[
  {
    "repo": "quantum-wealth/k8s-apps-gitops",
    "url": "https://github.com/quantum-wealth/k8s-apps-gitops/pull/123",
    "title": "fix(ALPM-123): increase advisor-portal memory limit",
    "jira": "ALPM-123",
    "verification": "kubectl kustomize non-prod/int"
  }
]
```

The incident controller owns Slack posting based on these structured results.
Cody should not call Slack directly in the end-state design.

## Slack Delivery

Slack delivery is owned by `kelos-incident-intake` or a shared Kelos Slack
delivery component.

Rules:

- Tokens are mounted only into the platform service.
- Channel ids are configured through `InfraHealthMonitor.spec.slack`.
- Cody never receives raw Slack channel ids unless they are harmless aliases.
- Every incident gets a root Slack message when it enters `Investigating`.
- RCA and status updates are posted in the root thread when a thread timestamp
  exists.
- PR updates are posted to:
  - the incident thread;
  - repo/team channels from `repoRoutes`.
- Updates are throttled to avoid Slack spam.
- Long RCA messages are summarized in Slack and full detail remains in the
  Task result / Jira / PR body.

Lifecycle messages:

1. `Investigating`: source, severity, affected env/ns/service, and link to
   source alert.
2. `RCAReady`: root cause, confidence, evidence bullets, and next action.
3. `PRCreated`: PR links, Jira links, verification, rollback notes.
4. `Blocked` or `Escalated`: exact blocker and owner action.
5. `Resolved`: only when a resolved signal or verification proves recovery.

## Source Adapters

### Alertmanager

- Accept native Alertmanager webhook payloads.
- Preserve all Alertmanager-provided labels, annotations, group labels, common
  labels, timestamps, generator URLs, fingerprints, receiver, and group key.
- Split or preserve grouped alerts based on monitor policy.
- Default: one incident per alert after grouping by monitor correlation keys.
- Support firing and resolved statuses.

### Flux

Two acceptable implementations:

1. Use Alertmanager rules over Flux metrics for failed reconciliations.
2. Add a Flux notification provider that posts directly to incident intake.

The end state can support both. The normalized signal should use the same
`alertname=HelmReleaseFailed` shape regardless of source.

### KRR

Replace direct Slack-only weekly KRR reporting with a dual-output mode:

- summary still goes to the human resource channel;
- critical right-sizing findings can create incident signals with
  `alertname=KRRResourceRecommendation`.

KRR incidents should default to `Blocked` or `PRCandidate`, not `Critical`,
unless the workload is currently unhealthy.

### Datadog / Grafana

Add after Alertmanager and Flux are stable. They should only need source
normalizers, not new Cody logic.

## GitOps Changes

In `k8s-platform-gitops/non-prod/kelos`:

- enable `kelos-incident-intake` through Helm values;
- add Slack channel alias configuration;
- add `InfraHealthMonitor` resources;
- remove or suspend the simple `cody-infra-health-alerts` generic webhook
  TaskSpawner after cutover;
- route Alertmanager/Grafana/Flux/KRR to incident intake instead of directly
  to `kelos-webhook-generic`.

The cutover should preserve the same single Alertmanager opt-in label from the
simple version, so alert rules do not need to be rewritten.

## Kelos Changes

### API

- Add `InfraHealthMonitor` type.
- Add `InfraHealthIncident` type.
- Generate CRDs and client code.
- Add status conditions and print columns.

### Controller / Server

- Add `cmd/kelos-incident-intake`.
- Add source adapters:
  - Alertmanager first;
  - Flux second;
  - KRR third;
  - Datadog/Grafana later.
- Add incident correlation logic.
- Add incident reconciler that creates Cody Tasks.
- Add Slack delivery logic or reuse the cody-tools Slack notifier as a library.
- Add result parser for infra-health Task outputs.
- Add replay endpoint or CLI action for failed/suppressed incidents.

### Helm Chart

- Add `incidentIntake.enabled`.
- Add image, resources, service, env, and secret configuration.
- Add optional internal Service for source webhooks.
- Do not expose public ingress/HTTPRoute by default.

### Security

- Keep source webhooks internal by default.
- If external webhooks are required, support explicit authentication per source.
- Restrict Slack channel posting to configured aliases.
- Keep Slack tokens out of Cody task pods.
- Keep Kubernetes remediation PR-only.

## Migration From Simple Version

1. Keep the same alert label:

   ```yaml
   cody: "true"
   ```

2. Deploy incident intake in shadow mode:

   - receives copied alerts;
   - creates incidents;
   - does not create Cody tasks;
   - does not post Slack except to a test channel.

3. Enable task creation for `PodCrashLooping` and `PodOOMKilled`.
4. Compare results against the simple generic-webhook path.
5. Disable the simple TaskSpawner.
6. Move Alertmanager receiver URL from:

   ```text
   /webhook/alertmanager
   ```

   to:

   ```text
   /incident/alertmanager/non-prod
   ```

7. Enable Flux and ExternalSecret incidents.
8. Add KRR and Datadog/Grafana sources later.

## Rollout Plan

### Phase 1: Shadow Intake

- Incident intake receives Alertmanager payloads.
- Incidents are created and updated.
- No Cody tasks are spawned.
- Slack posts only to a test channel.

### Phase 2: Non-Prod Active Intake

- Enable Cody task creation for pod health alerts.
- Enable RCA Slack updates.
- Keep PR creation limited to `k8s-apps-gitops`.

### Phase 3: Full Non-Prod Infra Health

- Add Flux and ExternalSecret incidents.
- Add repo/channel PR routing.
- Enable app source repo PRs when evidence is strong.

### Phase 4: Production Guarded Mode

- Production events may create incidents and RCA.
- PR creation requires policy approval or explicit label.
- No production direct remediation.

## Validation

Unit tests:

- Alertmanager payload normalization.
- Flux payload normalization.
- Correlation key generation.
- Dedupe window behavior.
- Cooldown behavior.
- Incident phase transitions.
- Slack channel alias enforcement.
- Task result parsing.

Integration tests:

- Synthetic alert creates one incident.
- Repeated alert updates the same incident.
- Active incident does not spawn duplicate Cody tasks.
- Resolved alert marks incident monitoring/resolved.
- Cody task success with RCA posts RCA Slack update.
- Cody task result with PRs posts repo-routed Slack updates.
- Unknown Slack channel alias is rejected.

End-to-end tests in non-prod:

1. Trigger synthetic `PodCrashLooping`.
2. Verify incident created.
3. Verify Slack detected message.
4. Verify one Cody Task created.
5. Verify RCA parsed and posted.
6. Verify PR links parsed and posted when a fixture fix is created.
7. Re-send alert and verify no duplicate task.
8. Send resolved alert and verify incident state changes.

## Failure Modes

| Failure | Expected behavior |
| --- | --- |
| Alert storm | One incident is updated; Cody tasks are throttled by cooldown and active-task state. |
| Intake cannot normalize payload | Store rejected event with reason; no Cody task. |
| Slack API unavailable | Incident continues; Slack delivery retries and records condition. |
| Cody task fails | Incident moves to `Escalated` or `Blocked` with task failure details. |
| Cody opens PR but output parsing fails | Incident links raw task result and requires manual follow-up. |
| GitHub unavailable | Cody reports blocked result; incident posts blocked update. |
| Jira unavailable | Cody reports blocked result; no PR required unless policy changes. |
| Resolved signal arrives while Cody is running | Incident records resolved signal and lets Cody finish unless policy cancels running tasks. |

## Exit Criteria

- Incidents are durable Kubernetes resources.
- Repeated alerts update existing incidents instead of spawning duplicate tasks.
- Slack detected/RCA/PR updates are sent by platform-owned code, not prompt
  convention.
- Cody task results are structured and machine-parseable.
- PR notifications route to both incident and repo/team channels.
- Production can run in RCA-only mode with explicit approval for PR creation.
- The simple generic-webhook TaskSpawner can be removed or left suspended as a
  fallback.
