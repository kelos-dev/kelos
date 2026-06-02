# Cody Infra Health Simple Webhook Implementation Spec

Status: Draft
Date: 2026-06-01
Owner: Cody / Kelos / Platform

## Summary

Implement the first Cody infra-health workflow using the existing Kelos
`genericWebhook` TaskSpawner source.

This version intentionally stays small:

- Alertmanager routes non-prod infrastructure alerts labeled `cody="true"` to
  the Kelos generic webhook server.
- Alertmanager sends its native webhook payload. Kelos receives all alert data
  Alertmanager has for the group, including labels, annotations, timestamps,
  generator URLs, fingerprints, common labels, and common annotations.
- One `TaskSpawner` gates the webhook payload by `cody="true"` and firing
  status.
- One Cody task investigates the alert using the existing Cody debugger
  runbook.
- Kelos posts lifecycle updates to one pre-approved Slack channel through the
  Slack server's deferred task reporter.
- Cody creates Jira and opens PRs only when there is enough evidence for a
  safe code or GitOps fix.

This is a bridge implementation. It validates whether automated infra
investigation is useful before adding a durable incident-intake service.

## Goals

- Route infra-health alerts into Cody automatically with one opt-in alert
  label: `cody="true"`.
- Cover the first alert set:
  - `PodCrashLooping`
  - `PodOOMKilled`
  - `HelmReleaseFailed`
  - `ExternalSecretSyncError`
- Keep the alert selection visible in GitOps alert rules without adding
  Cody-only routing metadata beyond the single `cody` label.
- Reuse the existing `cody-debugger` RBAC, GitHub broker, Atlassian MCP, and
  debugger workflow.
- Post three lifecycle updates to one pre-defined Slack channel:
  - issue detected and investigating;
  - RCA;
  - PR fix or blocked/manual follow-up.
- Keep all remediation reviewable through PRs.
- Scope the first rollout to non-prod.

## Non-Goals

- Do not build a durable incident state store in this pass.
- Do not implement multi-alert correlation beyond Alertmanager grouping and a
  stable webhook id.
- Do not support production remediation.
- Do not give Cody direct Slack tokens.
- Do not allow arbitrary Slack channel posting from agent pods.
- Do not directly mutate Kubernetes resources from Cody.
- Do not add broad Datadog/Grafana adapters yet. Alertmanager is the first
  source.

## Current Facts

- Kelos already supports `spec.when.webhook` / `genericWebhook`.
- Generic webhook `fieldMapping` exposes selected JSONPath values as template
  variables.
- Generic webhook `filters` can match exact values or regex patterns.
- Generic webhook task creation currently deduplicates per delivery id derived
  from the mapped `id` field or request body hash.
- Generic webhook TaskSpawners are marked webhook-based and do not create their
  own polling Deployment or CronJob.
- `k8s-platform-gitops/non-prod/kelos/helmrelease-patch.yaml` currently enables
  the GitHub webhook server and Slack server, but not the generic webhook
  server.
- Existing Slack-originated tasks annotate the source channel and thread
  timestamp. A generic webhook task should instead opt into deferred Slack
  reporting with a configured Slack destination route.
- The current Cody debugger AgentConfig already explains how to inspect pods,
  logs, events, Flux, HelmReleases, source repos, and GitOps repos.

## Architecture

```text
PrometheusRule / Flux-health metric / ExternalSecret metric
  -> Alertmanager route for cody="true"
  -> kelos-webhook-generic /webhook/alertmanager
  -> TaskSpawner cody-infra-health-alerts
  -> Kelos Task
  -> Codex pod with cody-debugger + cody-infra-health AgentConfigs
  -> Kubernetes read-only diagnostics
  -> Jira + GitHub PR when fixable
  -> kelos-slack-server deferred Slack reporter
```

## Alert Selection

Selection is intentionally simple:

1. Alert rules opt in with a single label: `cody="true"`.
2. Alertmanager routes every Cody-labeled alert group to Kelos, including
   resolved notifications.
3. The Kelos `TaskSpawner` gates task creation on `status=firing` and
   `cody="true"`.
4. Cody branches by `alertname` and the labels/annotations already present in
   the native Alertmanager payload.

This avoids extra Cody-only labels such as workflow, owner, repo, or Slack
channel. Existing alert metadata remains the source of truth.

### Alert Labels

Every alert that may create a Cody task should include:

```yaml
labels:
  severity: warning # or critical
  cody: "true"
```

The alert should also expose normal operational labels where the source metric
already has them:

```yaml
labels:
  namespace: "{{ $labels.namespace }}"
  pod: "{{ $labels.pod }}"
  container: "{{ $labels.container }}"
  helmrelease: "{{ $labels.name }}"
  externalsecret: "{{ $labels.name }}"
```

Not every alert has every label. The Cody prompt must branch based on the alert
kind and available fields.

Do not add labels only to help Cody route the alert. If metadata is needed for
humans or alert quality, add it as normal alert metadata; otherwise rely on the
full Alertmanager webhook payload.

### Initial Alert Rules

`PodCrashLooping` and `PodOOMKilled` can reuse the existing non-prod pod health
rules, with the single Cody opt-in label added.

`HelmReleaseFailed` should be implemented with the Flux metrics already scraped
in the cluster. The exact metric name must be verified in Prometheus before the
PR lands. The expected shape is a `Ready=False` condition for
`kind="HelmRelease"`.

`ExternalSecretSyncError` should be implemented from External Secrets Operator
status metrics. The exact metric name must be verified in Prometheus before the
PR lands. The expected shape is an `ExternalSecret` Ready condition that is
false for a sustained period.

## Alertmanager Routing

Add a receiver that posts Alertmanager's native webhook payload to the
in-cluster generic webhook service:

```yaml
receivers:
  - name: cody-infra-health
    webhook_configs:
      - url: http://kelos-webhook-generic.kelos-system.svc.cluster.local:8443/webhook/alertmanager
        send_resolved: true
```

Add a route before the default Slack fallback:

```yaml
routes:
  - receiver: cody-infra-health
    continue: true
    matchers:
      - cody="true"
```

The requirement is fan-out: Kelos gets a copy of every `cody="true"` alert group,
including resolved notifications, and existing Slack routing continues to behave
as it does today. If the deployed Alertmanager route tree cannot preserve the
Slack fallback with `continue: true`, attach the webhook to the existing Slack
receiver path or duplicate the route explicitly. Do not replace existing human
alert delivery.

For the simple version, keep this endpoint internal to the cluster. Do not
expose `/webhook/alertmanager` publicly until generic webhook authentication is
implemented and verified.

## Kelos TaskSpawner

Add a new TaskSpawner in `k8s-platform-gitops/non-prod/kelos`.

Representative YAML:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-infra-health-alerts
  namespace: kelos-system
  labels:
    cody.alpheya.com/workflow: infra-health
spec:
  maxConcurrency: 2
  when:
    webhook:
      source: alertmanager
      fieldMapping:
        id: "$.groupKey"
        title: "$.commonAnnotations.summary"
        status: "$.status"
        alertname: "$.commonLabels.alertname"
        severity: "$.commonLabels.severity"
        namespace: "$.commonLabels.namespace"
        pod: "$.commonLabels.pod"
        container: "$.commonLabels.container"
        helmrelease: "$.commonLabels.helmrelease"
        externalsecret: "$.commonLabels.externalsecret"
      filters:
        - field: "$.status"
          value: firing
        - field: "$.commonLabels.cody"
          value: "true"
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
    promptTemplate: |
      Infra health alert detected.

      Alertmanager status: {{.status}}
      Alert: {{.alertname}}
      Severity: {{.severity}}
      Namespace: {{.namespace}}
      Pod: {{.pod}}
      Container: {{.container}}
      HelmRelease: {{.helmrelease}}
      ExternalSecret: {{.externalsecret}}

      Full Alertmanager payload:
      {{.Payload}}

      Follow AGENTS.md.
      First, emit a concise progress update saying the issue was detected and
      Cody is investigating. Kelos will create the Slack root message from
      that progress output.
      Then investigate using the Cody infra health workflow.
      Produce an RCA.
      If a safe fix exists, create or reuse an ALPM Jira ticket and open a
      small PR.
      Include the RCA and PR/blocked details in progress/final output so Kelos
      can update the same Slack message.
    metadata:
      labels:
        cody.alpheya.com/workflow: infra-health
        cody.alpheya.com/source: alertmanager
        kelos.dev/slack-reporting: "enabled"
      annotations:
        kelos.dev/slack-reporting: "deferred"
        kelos.dev/slack-destination: asd
    ttlSecondsAfterFinished: 86400
    podOverrides:
      labels:
        cody.alpheya.com/tools-client: "true"
      serviceAccountName: cody-debugger
      env:
        - name: CODY_TOOLS_GITHUB_BASE_URL
          value: http://cody-tools.kelos-system.svc.cluster.local:8080/github
```

Use `$.groupKey` as the first id because Alertmanager grouping should be the
simple dedupe boundary. If we discover that group keys are too broad or too
narrow, switch to a composed id such as:

```text
{{ alertname }}:{{ namespace }}:{{ pod }}:{{ helmrelease }}:{{ externalsecret }}
```

That composition requires a small Kelos generic-webhook enhancement or an
Alertmanager template that emits a stable id in the payload.

## Cody Infra Health AgentConfig

Add `agentconfig-cody-infra-health.yaml`.

It should layer on top of `cody-debugger`, not duplicate the whole debugger
runbook. It should define:

- alert-specific workflow;
- Slack update requirements;
- alert-name-specific diagnostic paths;
- safe PR rules;
- output shape.

Required behavior:

```text
1. Emit detected/investigating progress so Kelos posts to the configured
   incident channel.
2. Resolve the affected Kubernetes object.
3. Collect cheap cluster evidence first:
   - pods
   - describe
   - logs and previous logs
   - events
   - Flux status
   - ExternalSecret status when relevant
4. Decide whether root cause is:
   - app code
   - app deploy config in k8s-apps-gitops
   - platform config in k8s-platform-gitops
   - operational/transient
   - unknown/manual
5. Emit RCA with evidence and confidence.
6. If safe, create/reuse ALPM and open a small PR.
7. Emit PR links or blocked/manual follow-up so Kelos updates the incident
   channel.
```

The AgentConfig should explicitly tell Cody not to claim a fix unless a PR was
opened or a human operational action was identified.

## Slack Route ConfigMap and Deferred Reporting

Generic webhook tasks do not have originating Slack channel/thread annotations.
For this MVP, add a proactive Slack reporting path to `kelos-slack-server`:

- TaskSpawners opt in with label `kelos.dev/slack-reporting: "enabled"`.
- TaskSpawners request top-level Slack creation with annotation
  `kelos.dev/slack-reporting: "deferred"`.
- TaskSpawners select the configured destination with annotation
  `kelos.dev/slack-destination: asd`.
- `kelos-slack-server` resolves the destination through a mounted route
  ConfigMap and posts to exactly one Slack channel for this version.
- The first meaningful progress snapshot creates the root Slack message.
- Later progress/final snapshots update that same Slack message by using the
  persisted Slack message timestamp annotations.

Route ConfigMap:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: cody-slack-routes
  namespace: kelos-system
data:
  routes.yaml: |
    routes:
      asd:
        channel: "#asd"
```

The configured channel value can be a Slack channel ID for private channels or
for resilience to channel renames. Cody task pods never receive Slack bot
tokens or arbitrary channel IDs.

## GitOps Changes

In `k8s-platform-gitops/non-prod/kelos`:

- enable generic webhook server in `helmrelease-patch.yaml`;
- set `webhookServer.image` to the Alpheya Kelos webhook image if the fork is
  required for the generic source behavior used here;
- add the `cody-infra-health-alerts` TaskSpawner;
- add the `cody-infra-health` AgentConfig;
- add the `cody-slack-routes` ConfigMap and wire it into the Kelos HelmRelease;
- keep Slack bot/app tokens in the existing Slack server Secret;
- add NetworkPolicy if the cluster enforces ingress restrictions between
  Alertmanager and `kelos-webhook-generic`;
- do not add a public HTTPRoute for `/webhook/alertmanager` in the first
  version.

In `k8s-platform-gitops/non-prod/alpheya-system` or the relevant monitoring
overlay:

- add `cody: "true"` to alerts that should spawn Cody investigations;
- add Alertmanager receiver and fan-out route;
- keep existing Slack alert routing in place unless the platform team decides
  the Cody detected message replaces a specific alert message.

## Kelos Changes

Required:

- Add deferred Slack reporting for non-Slack-originated Tasks.
- Add Slack destination route config loading in `kelos-slack-server`.
- Add unit tests for configured routes and rejected invalid route configs.
- Add tests proving agent pods do not need Slack tokens.

Optional but recommended before public exposure:

- Add generic webhook authentication.
- Support a stable generic webhook id composed from multiple mapped fields.
- Support richer lifecycle phase formatting in Kelos if plain progress/final
  snapshots are not enough.

## Rollout Plan

### Phase 1: POC

- Deploy only in non-prod.
- Add `cody: "true"` only to `PodCrashLooping` and `PodOOMKilled`.
- Send Slack notifications to `#asd`.
- Set `maxConcurrency: 1`.
- Require Cody to produce RCA but allow PR creation only for
  `k8s-apps-gitops` resource/probe fixes.

### Phase 2: Broaden Alert Types

- Add `cody: "true"` to `HelmReleaseFailed`.
- Add `cody: "true"` to `ExternalSecretSyncError`.
- Increase `maxConcurrency` to `2` if POC noise is manageable.

### Phase 3: Broaden PR Scope

- Allow app source repo PRs when logs and stack traces clearly point to source
  code.
- Keep Slack reporting on the same incident channel. Revisit multi-destination
  routing only after the POC proves useful.

## Validation

Static validation:

- `go test ./cmd/kelos-slack-server ./internal/webhook ./internal/reporting`
- Helm render for the Kelos chart with generic webhook enabled.
- `kubectl kustomize non-prod/kelos`.
- `kubectl kustomize` for the monitoring overlay that owns Alertmanager.

Runtime validation:

1. Send a synthetic Alertmanager payload to:

   ```text
   http://kelos-webhook-generic.kelos-system.svc.cluster.local:8443/webhook/alertmanager
   ```

2. Verify exactly one Task is created:

   ```bash
   kubectl get tasks -n kelos-system -l cody.alpheya.com/workflow=infra-health
   ```

3. Verify Kelos posts detected/investigating to the configured Slack channel
   after Cody emits the first progress update.
4. Verify Cody collects cluster evidence before cloning repos.
5. Verify Cody posts RCA.
6. Verify Cody opens no PR when evidence is insufficient.
7. Verify Cody opens a small PR for a controlled GitOps fixture alert.
8. Re-send the same payload and confirm duplicate behavior is acceptable for
   the POC.

## Failure Modes

| Failure | Expected behavior |
| --- | --- |
| Alertmanager sends grouped alerts | Cody treats the group as one incident and reports every alert in the group. |
| Payload lacks namespace/service labels | Cody posts blocked/manual follow-up and identifies the missing alert labels. |
| Generic webhook server unavailable | Alertmanager retries according to its normal webhook behavior; no Cody task is created. |
| Cody task already running and `maxConcurrency` reached | Kelos may drop the event. This is accepted for the simple version and fixed by the end-state intake service. |
| Slack reporter unavailable | Cody continues investigation; Kelos logs/reporting retries surface the Slack failure outside the task pod. |
| Jira unavailable | Cody posts RCA and blocked status, but does not open a PR without a ticket unless the prompt is explicitly changed later. |
| GitHub unavailable | Cody posts RCA and blocked status with the intended repo/diff summary. |

## Exit Criteria

- Non-prod infra alerts labeled `cody="true"` automatically create Cody tasks.
- Cody posts detected, RCA, and PR/blocked updates to a predefined Slack
  channel.
- Cody creates or reuses ALPM Jira before opening PRs.
- Cody opens PRs only for evidence-backed, reviewable fixes.
- No Slack bot token is mounted into Cody task pods.
- No public unauthenticated generic webhook route is exposed.
- Platform has enough signal to decide whether to invest in the end-state
  incident-intake service.
