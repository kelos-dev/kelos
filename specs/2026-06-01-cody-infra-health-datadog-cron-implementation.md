# Cody Infra Health Scheduled Cron Implementation Spec

Status: Draft
Date: 2026-06-01
Owner: Cody / Kelos / Platform

## Summary

Implement an infra-health workflow where Cody proactively checks an environment
on a schedule, triages material environment issues, performs RCA, and creates
PRs when a safe fix exists. Environments can use Datadog MCP as the primary
signal source, or a Kubernetes/Flux/logs mode when Datadog is not the right
observability source for that environment.

This version uses:

- Kelos cron `TaskSpawner`s for scheduled execution;
- `quantum-wealth/skills` `cody/` as the source of truth for Cody
  orchestration manifests;
- Datadog MCP routed through `cody-tools` for Datadog-backed environments;
- Datadog API/application keys held only by `cody-tools`;
- Kubernetes, Flux, and pod log evidence through Cody debugger RBAC for
  Kubernetes-backed environments;
- Cody debugger RBAC for Kubernetes/GitOps follow-up;
- Slack lifecycle updates through `kelos-slack-server`, using deferred proactive
  Task annotations instead of direct Slack calls from Cody task pods.

This is a polling-based alternative to the Alertmanager webhook MVP. It can be
implemented without Kelos controller or CRD changes, but it does require changes
to `cody-tools`, the Kelos Slack reporter/runtime chart, the `skills/cody`
orchestration tree, and a small amount of runtime GitOps configuration for
secrets and deployed env.

## Goals

- Run scheduled Cody health checks per environment or cluster.
- Support an environment-selected primary signal source:
  - Datadog MCP for environments with reliable Datadog monitor/log/metric
    coverage;
  - Kubernetes/Flux/pod logs for environments where in-cluster evidence is the
    source of truth.
- Keep Datadog credentials out of Cody task pods.
- Allow Cody to inspect:
  - active monitors;
  - events;
  - incidents;
  - service health;
  - logs;
  - metrics;
  - traces/APM where the Datadog tenant supports it;
  - DBM where enabled.
- Deliver lifecycle updates to predefined Slack channels:
  - issue detected and investigating;
  - RCA;
  - PR created or blocked/manual follow-up.
- Reuse existing Cody debugger access for Kubernetes, Flux, GitOps, GitHub, and
  Atlassian/Jira context.
- Keep remediation PR-only.
- Start with non-prod, then broaden.

## Non-Goals

- Do not replace Datadog monitors or Alertmanager alerts.
- Do not stream every log line into Cody.
- Do not create or mutate Datadog monitors in the MVP.
- Do not mute monitors or acknowledge incidents from Cody.
- Do not mount Datadog API/application keys into Cody task pods.
- Do not build durable incident correlation in this version.
- Do not make production remediation autonomous.
- Do not add a new Kelos source type.
- Do not add the cron TaskSpawner manifests directly under
  `k8s-platform-gitops/non-prod/kelos`; that repo should keep the runtime
  foundation and Flux pointer, while `skills/cody` owns Cody workflows.

## Current Facts

- Kelos already supports cron TaskSpawners through `spec.when.cron.schedule`.
- Cron TaskSpawners create Kubernetes CronJobs and expose cron template
  variables such as `{{.Time}}` and `{{.Schedule}}`.
- AgentConfig supports HTTP MCP servers.
- Latest `quantum-wealth/skills` main already contains a `cody/` orchestration
  tree with Kustomize overlays and Kelos CRs. Current deployed shape includes:
  - `cody/kustomization.yaml`;
  - `cody/self-development/kustomization.yaml`;
  - `cody/self-development/investor-adib/agentconfig-cody-investor-adib.yaml`;
  - `cody/self-development/investor-adib/workspace.yaml`;
  - `cody/self-development/investor-adib/taskspawner-pr-responder.yaml`.
- Latest `k8s-platform-gitops` main already has the one-time Flux pointer:
  `non-prod/cody/git-repo.yaml` defines a `GitRepository` for
  `https://github.com/quantum-wealth/skills.git`, branch `main`, and a
  `Kustomization` named `cody-orchestration` with `path: cody`.
- `cody-tools` already brokers:
  - Atlassian MCP at `/mcp/atlassian`;
  - Aikido API at `/aikido`;
  - GitHub app/package credentials at `/github`.
- `k8s-platform-gitops` already deploys `cody-tools` in `non-prod/kelos`.
- `k8s-platform-gitops/non-prod/kelos` still owns the Cody runtime foundation:
  Kelos controller installation, `cody-tools` Deployment/Service/NetworkPolicy,
  external secrets, debugger RBAC, and shared base/persona AgentConfigs.
- Existing Kelos Slack reporting is centralized in `kelos-slack-server`.
  `kelos-slack-server` owns Slack Socket Mode ingress and uses the Slack Web API
  to post/update messages. Tasks created from Slack carry Slack channel/thread
  annotations, so the reporter can reply in the originating thread.
- Cron tasks have no originating Slack thread. For proactive notifications, the
  cron TaskSpawner must provide a Slack destination name in Task metadata. The
  `kelos-slack-server` reporter resolves that destination through a structured
  route ConfigMap, creates a new top-level Slack message only after Cody emits
  the first material progress snapshot, and stores the new Slack timestamp back
  on the Task for subsequent in-place updates.
- Datadog Agent is already deployed in GitOps with:
  - site `datadoghq.eu`;
  - log collection enabled;
  - container log collection enabled;
  - OpenTelemetry forwarding;
  - Datadog DBM-related Postgres config in non-prod.
- Datadog's MCP setup supports HTTP transport, `toolsets` query parameters, and
  server-side authentication via `DD_API_KEY` and `DD_APPLICATION_KEY` headers.

References:

- Kelos cron source: `api/v1alpha1/taskspawner_types.go`
- Kelos TaskSpawner docs: `docs/reference.md`
- Cody tools server: `cmd/cody-tools/main.go`
- Kelos Slack server: `cmd/kelos-slack-server/main.go`
- Kelos Slack task reporter: `internal/reporting/watcher.go`
- Skills Cody orchestration root: `skills/cody/kustomization.yaml`
- Skills Cody README: `skills/cody/README.md`
- GitOps Flux pointer to skills/cody:
  `k8s-platform-gitops/non-prod/cody/git-repo.yaml`
- Cody tools GitOps deployment:
  `k8s-platform-gitops/non-prod/kelos/deployment-cody-tools.yaml`
- Datadog Agent base:
  `k8s-platform-gitops/bases/datadog-agent/datadog-agent.yaml`
- Datadog MCP setup:
  https://docs.datadoghq.com/bits_ai/mcp_server/setup/
- Datadog MCP tools:
  https://docs.datadoghq.com/bits_ai/mcp_server/tools/

## Target Architecture

```text
Flux reconciles quantum-wealth/skills path cody/
  -> skills/cody/infra-health/*
  -> Kelos TaskSpawner cron schedule
  -> TaskSpawner chooses signal source: datadog or kubernetes
  -> Kelos-managed CronJob
  -> Cody Task
  -> if signal source is datadog:
       -> cody-tools /mcp/datadog
       -> Datadog MCP Server
  -> if signal source is kubernetes:
       -> Kubernetes API, pod logs, events, Flux, HelmRelease, ExternalSecret
  -> Cody triage decision
  -> if no material issue: finish quietly
  -> if material issue:
       -> Cody emits detected/investigating progress text
       -> kelos-slack-server resolves Task Slack destination
       -> kelos-slack-server posts new root Slack message
       -> kelos-slack-server stores root ts on Task
       -> Kubernetes/GitOps/GitHub/Jira investigation
       -> RCA
       -> PR when safe
       -> kelos-slack-server updates same Slack message with RCA + PR/blocked
```

## High-Level Behavior

Each scheduled run checks one environment scope.

The task should:

1. Read the configured primary signal source for the environment:
   - Datadog mode queries Datadog for material active issues in the configured
     environment and lookback window.
   - Kubernetes mode checks pod health, recent pod logs, events, Flux status,
     HelmRelease status, and ExternalSecret status directly through in-cluster
     read-only access.
2. Decide whether anything deserves a Cody investigation.
3. Exit quietly if there is no material issue.
4. If an issue exists, emit a concise assistant progress message saying the
   issue was detected and Cody is investigating. Kelos converts that first
   material progress snapshot into a new root Slack message in the configured
   Slack destination.
5. Gather primary-source evidence first.
6. Corroborate with Kubernetes, Flux, GitOps, Jira, and GitHub only after the
   primary signal source identifies a concrete affected service, namespace,
   workload, monitor, incident, or error pattern.
7. Produce an RCA.
8. Open a PR only when the fix is specific, safe, and reviewable.
9. Emit RCA and PR/blocked output; Kelos updates the same Slack message.

## Why Cron Instead of Webhook

Cron is useful for this implementation because:

- it avoids Alertmanager/Grafana/Datadog webhook setup for the first iteration;
- it can use Datadog's existing cross-signal context instead of relying on
  individual alert payloads;
- it does not require alert rule edits or new `cody` labels;
- it can inspect functional/application issues in addition to infra issues.

The tradeoffs are:

- every schedule creates a Cody task, even when no issue exists;
- detection latency is bounded by the cron interval;
- dedupe/cooldown is mostly prompt and policy based in the MVP;
- Datadog MCP tool behavior and tool names may change over time;
- event-driven Alertmanager or Datadog monitor webhooks remain better for
  urgent, precisely scoped incidents.

## Environment Signal Source Selection

Each environment TaskSpawner must declare one primary signal source. This keeps
the workflow deterministic and avoids a single global prompt guessing whether
Datadog or in-cluster evidence is authoritative for a given environment.

Use pod env and the TaskSpawner prompt to make the choice explicit:

```yaml
podOverrides:
  env:
    - name: CODY_INFRA_HEALTH_SIGNAL_SOURCE
      value: datadog # or kubernetes
    - name: CODY_INFRA_HEALTH_ENVIRONMENT
      value: qa
    - name: CODY_INFRA_HEALTH_NAMESPACE
      value: qa
    - name: KUBERNETES_CLUSTER_NAME
      value: non-prod
```

Environment, namespace, and cluster labels are not required for routing or
behavior. Keep these values in pod env and prompt text so Cody has them at
runtime. Add metadata labels only if operators want Kubernetes selectors,
dashboards, or metrics grouping. The only required metadata label for this
workflow is `kelos.dev/slack-reporting=enabled`, because `kelos-slack-server`
lists reportable Tasks by that label.

Use a generic Cody source label for scheduled work:

```yaml
cody.alpheya.com/source: cron
```

Do not encode the observability mode as `datadog-cron` or `kubernetes-cron`.
The mode is runtime configuration via `CODY_INFRA_HEALTH_SIGNAL_SOURCE`.

### Datadog Mode

Use Datadog mode when the environment has reliable Datadog monitor, event, log,
metric, APM, or DBM coverage and the Datadog environment/cluster tags are known.

TaskSpawner requirements:

- include the `cody-datadog-mcp` AgentConfigRef;
- set `CODY_INFRA_HEALTH_SIGNAL_SOURCE=datadog`;
- define Datadog site, environment tag, namespace scope, and lookback window in
  the prompt;
- keep Datadog API/application keys only in `cody-tools`;
- use Kubernetes/Flux/logs only for corroboration after Datadog identifies an
  affected service, namespace, workload, or error pattern.

### Kubernetes/Logs Mode

Use Kubernetes mode when Datadog is not deployed, the environment's Datadog tags
are not trustworthy, logs are only available from the cluster, or the desired
health signals are primarily Kubernetes/Flux state.

TaskSpawner requirements:

- omit the `cody-datadog-mcp` AgentConfigRef;
- set `CODY_INFRA_HEALTH_SIGNAL_SOURCE=kubernetes`;
- rely on the existing `cody-debugger` service account and RBAC;
- scan cluster evidence first:
  - pods by namespace and phase;
  - restart counts, CrashLoopBackOff, OOMKilled, ImagePullBackOff, and pending
    pods;
  - recent Kubernetes events sorted by time;
  - `kubectl logs` and `kubectl logs --previous` for affected pods only;
  - Flux Kustomization and HelmRelease status;
  - ExternalSecret and certificate status;
  - node pressure only when workload symptoms point there;
- clone GitOps/source repos only after the affected workload and likely fix
  surface are known.

Kubernetes mode should not run an unbounded log scrape. It should first identify
candidate workloads from pod status, events, or Flux/HelmRelease state, then
read recent logs from those workloads with tight time/line limits.

### Automatic Kubernetes Fallback

Datadog mode should always fall back to Kubernetes mode when Datadog MCP is
unavailable or unusable. No extra fallback flag is required.

Fallback triggers:

- Datadog MCP route is unavailable;
- Datadog credentials are invalid or missing;
- Datadog MCP returns no usable scoped data for the configured environment;
- Datadog tool names or toolsets changed enough that Cody cannot perform the
  planned Datadog scan.

Fallback behavior:

- switch to the Kubernetes/Logs Mode evidence order;
- keep log collection bounded to affected workloads and recent windows;
- do not create Slack output unless Kubernetes fallback finds a material issue;
- if a material issue is found, include in the RCA that Datadog was the primary
  source, Datadog failed or was unusable, and Kubernetes fallback supplied the
  evidence;
- if Kubernetes fallback also lacks enough access or evidence, report a blocked
  investigation rather than guessing.

## Implementation Scope

The MVP requires three implementation areas:

1. `cody-tools` Datadog MCP proxy.
2. Kelos Slack reporter support for deferred proactive Task reporting.
3. `skills/cody` resources for the optional Datadog MCP AgentConfig,
   signal-source-aware infra-health AgentConfig, and cron TaskSpawners.
4. Runtime GitOps resources for Datadog secrets only where Datadog mode is
   enabled, `cody-tools` Deployment configuration, and `kelos-slack-server`
   Slack route ConfigMap configuration.

Kelos controller, CRD, webhook server, and task execution code should remain
unchanged. The Slack reporting runtime changes are limited to
`kelos-slack-server`, `internal/reporting`, and the Helm chart env wiring.

Repository ownership:

| Area | Source of truth | Contents |
| --- | --- | --- |
| Cody workflow manifests | `quantum-wealth/skills`, path `cody/` | Purpose overlays, repo/env overlays, AgentConfigs, Workspaces, TaskSpawners |
| Cody/Kelos runtime foundation | `k8s-platform-gitops/non-prod/kelos` | Kelos Helm release, `cody-tools`, `kelos-slack-server`, Service, NetworkPolicy, ExternalSecrets, RBAC, shared runtime AgentConfigs |
| Flux pointer to Cody workflows | `k8s-platform-gitops/non-prod/cody` | `GitRepository skills` and `Kustomization cody-orchestration` |
| Runtime code | `quantum-wealth/kelos` | Kelos controller/spawner, `cody-tools`, `kelos-slack-server`, Slack/Kelos reporters |

## `cody-tools` Datadog MCP Proxy

Add a Datadog MCP route to `cmd/cody-tools`.

### Route

```text
/mcp/datadog
```

The route should behave like the existing Atlassian MCP proxy:

- accept MCP JSON-RPC HTTP requests from Cody task pods;
- forward the body to the configured Datadog MCP endpoint;
- inject Datadog credentials server-side;
- log method/tool/status/duration;
- never log secrets or full request/response bodies by default.

### Configuration

Add environment variables:

```text
CODY_TOOLS_DATADOG_ENABLED=true
CODY_TOOLS_DATADOG_UPSTREAM_URL=https://mcp.datadoghq.eu/api/unstable/mcp-server/mcp?toolsets=core,alerting,dbm
CODY_TOOLS_DATADOG_API_KEY=<from secret>
CODY_TOOLS_DATADOG_APPLICATION_KEY=<from secret>
CODY_TOOLS_DATADOG_ALLOWED_TOOLSETS=core,alerting,dbm
CODY_TOOLS_DATADOG_STARTUP_VALIDATE=false
```

Notes:

- `datadoghq.eu` matches the current GitOps Datadog site. Verify the Datadog
  tenant site before landing the implementation.
- Start with generally available toolsets where possible.
- Add preview toolsets such as `apm` only after confirming tenant access.
- Avoid `toolsets=all` in the MVP because it increases tool definitions and
  context pressure.

### Credential Injection

For outbound requests to Datadog MCP, `cody-tools` should set:

```text
DD_API_KEY: <CODY_TOOLS_DATADOG_API_KEY>
DD_APPLICATION_KEY: <CODY_TOOLS_DATADOG_APPLICATION_KEY>
```

It should strip or ignore inbound Datadog auth headers from Cody task pods.
Cody should not be able to override the server-side credentials.

### Startup Validation

When `CODY_TOOLS_DATADOG_ENABLED=true` and
`CODY_TOOLS_DATADOG_STARTUP_VALIDATE=true`, startup should:

1. initialize an MCP session;
2. call `tools/list`;
3. log the count and selected toolsets;
4. log a warning but keep `cody-tools` serving if Datadog is unreachable or
   validation fails, so scheduled infra-health tasks can use Kubernetes
   fallback and other `cody-tools` routes are not taken down.

Keep the validation less strict than Atlassian site validation because Datadog
tool names and toolsets are expected to evolve.

### Read-Only Policy

The Datadog service account should only have read permissions:

- `mcp_read`;
- logs read;
- metrics query read;
- monitors read;
- events read;
- incidents read;
- APM read if enabled;
- DBM read if enabled.

Do not grant `mcp_write` for the MVP.

`cody-tools` can initially rely on Datadog role permissions for enforcement.
If the Datadog MCP exposes write tools despite read-only credentials, the call
should fail upstream.

Optional hardening after MVP:

- inspect `tools/list` and hide tools with names that begin with
  `create_`, `update_`, `delete_`, `mute_`, `resolve_`, or `execute_`;
- block outbound `tools/call` requests for explicitly denied tool names;
- maintain an allowlist of approved read-only Datadog tool names.

### Tests

Add `cmd/cody-tools` tests for:

- Datadog route forwards MCP requests to the configured upstream.
- Datadog route injects `DD_API_KEY` and `DD_APPLICATION_KEY`.
- Inbound Datadog auth headers from the agent are ignored.
- Missing Datadog credentials return service unavailable when enabled.
- Request logs include MCP method/tool/status but not credentials.
- Datadog disabled mode leaves `/mcp/datadog` unavailable.

## Runtime GitOps Changes

Runtime GitOps should only cover secrets and deployed service configuration.
The Datadog AgentConfig and cron TaskSpawners belong under `skills/cody`, not
under `k8s-platform-gitops/non-prod/kelos`.

### Datadog MCP Secret

Add an ExternalSecret in `k8s-platform-gitops/non-prod/kelos`:

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: cody-datadog-mcp
  namespace: kelos-system
spec:
  target:
    name: cody-datadog-mcp
  data:
    - secretKey: DD_API_KEY
      remoteRef:
        key: cody-datadog-mcp-api-key
    - secretKey: DD_APPLICATION_KEY
      remoteRef:
        key: cody-datadog-mcp-application-key
```

Use the actual remote secret keys from the platform secret store.

### `cody-tools` Deployment

Add env vars to `deployment-cody-tools.yaml`:

```yaml
- name: CODY_TOOLS_DATADOG_ENABLED
  value: "true"
- name: CODY_TOOLS_DATADOG_UPSTREAM_URL
  value: https://mcp.datadoghq.eu/api/unstable/mcp-server/mcp?toolsets=core,alerting,dbm
- name: CODY_TOOLS_DATADOG_ALLOWED_TOOLSETS
  value: core,alerting,dbm
- name: CODY_TOOLS_DATADOG_API_KEY
  valueFrom:
    secretKeyRef:
      name: cody-datadog-mcp
      key: DD_API_KEY
- name: CODY_TOOLS_DATADOG_APPLICATION_KEY
  valueFrom:
    secretKeyRef:
      name: cody-datadog-mcp
      key: DD_APPLICATION_KEY
```

If outbound egress is restricted, allow `cody-tools` to reach:

```text
https://mcp.datadoghq.eu
```

### Slack Route ConfigMap

Slack tokens and route resolution stay with `kelos-slack-server`, not
`cody-tools` and not Cody task pods.

Add a ConfigMap that defines the allowed proactive Slack destinations. Keep the
MVP single-channel per destination:

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
        channel: "<Slack channel ID for #asd>"
```

Wire that key into the Kelos HelmRelease:

```yaml
spec:
  values:
    slackServer:
      enabled: true
      secretName: cody-slack-tokens
      routeConfigMapName: cody-slack-routes
      routeConfigMapKey: routes.yaml
```

`channel` may be a Slack channel name for public channels or a channel ID. Using
channel IDs is still preferred for private channels and rename-resistant
routing. `kelos-slack-server` parses the route file on startup and rejects
malformed routes. The Cody Slack bot must already be invited to each routed
channel.

### Existing Flux Pointer

No new non-prod GitOps pointer should be required for the first rollout because
`k8s-platform-gitops/non-prod/cody/git-repo.yaml` already reconciles
`quantum-wealth/skills` branch `main`, path `cody`.

For any additional cluster or environment that does not have this pointer, add
the same pattern there once:

```text
GitRepository skills -> quantum-wealth/skills.git, branch main
Kustomization cody-orchestration -> sourceRef skills, path cody
```

After that, new Cody purposes and environment schedules should land only in the
`skills` repo.

## Skills Repo Orchestration Changes

Add a new Cody purpose under the skills repo:

```text
cody/
  infra-health/
    kustomization.yaml
    agentconfig-cody-datadog-mcp.yaml
    agentconfig-cody-infra-health-scheduled.yaml
    non-prod/
      kustomization.yaml
      taskspawner-cody-datadog-health-non-prod-qa.yaml
      taskspawner-cody-kubernetes-health-non-prod.yaml # only where needed
```

Then add `infra-health` to `cody/kustomization.yaml` so Flux reconciles it.

Keep the first pass narrow:

- one non-prod TaskSpawner;
- one Datadog MCP AgentConfig only for Datadog-backed environments;
- one signal-source-aware infra-health AgentConfig;
- no production schedules;
- PR creation constrained to controlled fixtures until Slack and dedupe behavior
  are proven.

### AgentConfig: Datadog MCP

Add `agentconfig-cody-datadog-mcp.yaml`:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: AgentConfig
metadata:
  name: cody-datadog-mcp
  namespace: kelos-system
spec:
  agentsMD: |
    ## Datadog MCP

    Use the `cody-datadog` MCP server for Datadog observability context.

    Rules:

    - Query Datadog only through `cody-datadog`.
    - Do not call Datadog public APIs directly.
    - Do not inspect environment variables, files, or MCP config for Datadog
      credentials.
    - Treat Datadog as read-only.
    - Do not create, update, mute, or delete monitors, incidents, dashboards, or
      Datadog resources.
    - Keep queries scoped to the environment, cluster, namespace, service, and
      lookback window supplied by the TaskSpawner prompt.
    - Prefer summaries and links over raw event/log dumps.
  mcpServers:
    - name: cody-datadog
      type: http
      url: http://cody-tools.kelos-system.svc.cluster.local:8080/mcp/datadog
```

### AgentConfig: Scheduled Infra Health Runbook

Add `agentconfig-cody-infra-health-scheduled.yaml`.

It should define:

- scheduled scan behavior;
- primary signal source selection from `CODY_INFRA_HEALTH_SIGNAL_SOURCE`;
- materiality threshold;
- no-issue quiet exit behavior;
- investigation order;
- deferred Slack progress contract;
- PR safety rules.

Key runbook content:

```text
For scheduled infra-health checks:

1. Read CODY_INFRA_HEALTH_SIGNAL_SOURCE.
2. If the value is datadog, check active critical/warning monitors, incidents,
   service events, and error trends for the supplied environment and lookback.
3. If the value is kubernetes, check pod status, restart counts, OOMKilled,
   CrashLoopBackOff, ImagePullBackOff, pending pods, warning events,
   HelmRelease/Flux status, ExternalSecret status, and bounded logs for affected
   workloads.
4. Treat an issue as material only when there is an active user-impacting,
   deployment-impacting, data-integrity, infra-health, or sustained error-rate
   signal.
5. If no material issue is found, finish silently and do not emit progress text.
6. If a material issue is found, emit detected/investigating progress first.
7. Gather primary-source evidence before secondary GitOps/source evidence.
8. Correlate with recent deployments, Flux events, pod restarts, OOMs, ingress
   5xx, DB health, and error logs.
9. RCA must include root cause, evidence, blast radius, confidence, and next
   action.
10. Open PRs only for specific, reviewable fixes.
```

### Deferred Slack Reporting

Cron tasks do not originate from a Slack thread. The TaskSpawner decides the
Slack destination by setting a route name on the Task template:

```yaml
metadata:
  labels:
    kelos.dev/slack-reporting: "enabled"
  annotations:
    kelos.dev/slack-reporting: "deferred"
    kelos.dev/slack-destination: asd
```

`kelos-slack-server` decides the actual Slack channel by resolving
`kelos.dev/slack-destination` through its mounted route ConfigMap. The
destination value is safe workflow config; the actual Slack channel lives in
platform-owned route config consumed only by `kelos-slack-server`.

Deferred reporting behavior:

1. The reporting loop lists Tasks with label
   `kelos.dev/slack-reporting=enabled`.
2. If the Task annotation is `kelos.dev/slack-reporting=deferred`, the reporter
   resolves the destination from either `kelos.dev/slack-channel` or
   `kelos.dev/slack-destination`.
3. While there is no `kelos.dev/slack-thread-ts`, the reporter does not post
   accepted/terminal lifecycle messages.
4. Once the Task is running and Cody emits a non-empty progress snapshot, the
   reporter posts a new top-level Slack message to the resolved channel.
5. The Slack API returns the root message timestamp.
6. The reporter persists that timestamp on the Task:

```text
kelos.dev/slack-channel=<resolved channel id>
kelos.dev/slack-thread-ts=<root message ts>
kelos.dev/slack-message-ts=<message being updated ts>
kelos.dev/slack-report-phase=accepted
```

7. Later progress snapshots update `kelos.dev/slack-message-ts` in place.
8. The terminal RCA/PR result updates the same message in place. If the final
   Slack payload must be split because of Slack block limits, continuation
   messages are posted as replies using `kelos.dev/slack-thread-ts`.

No material issue means Cody should not emit progress text. With no progress
snapshot, `kelos-slack-server` never creates a Slack root message.

If the Slack destination is missing or not configured in the route ConfigMap,
the reporter skips Slack delivery. The Task still completes and its Kubernetes
status remains the source of truth.

## Cron TaskSpawner Shape

Use one TaskSpawner per environment or cluster, defined under `skills/cody`.

This keeps:

- schedule;
- primary signal source;
- Datadog query scope;
- Kubernetes context;
- Slack destination;
- max concurrency;
- rollout/suspend;
- debugging ownership

isolated per environment.

### Representative Non-Prod TaskSpawner

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-datadog-health-non-prod-qa
  namespace: kelos-system
  labels:
    cody.alpheya.com/workflow: infra-health
    cody.alpheya.com/source: cron
spec:
  maxConcurrency: 1
  when:
    cron:
      schedule: "*/30 * * * *"
  taskTemplate:
    type: codex
    credentials:
      type: oauth
      secretRef:
        name: cody-codex-credentials
    image: docker.io/alpheya/codex:main
    agentConfigRefs:
      - name: cody-debugger
      - name: cody-datadog-mcp
      - name: cody-infra-health-scheduled
      - name: cody-atlassian-mcp
    promptTemplate: |
      Scheduled Datadog infra-health check.

      Environment: qa
      Namespace: qa
      Cluster: non-prod
      Datadog site: datadoghq.eu
      Datadog scope tags:
        - env:qa
        - namespace:qa
      Lookback window: 30m
      Trigger time: {{.Time}}
      Schedule: {{.Schedule}}

      Use the cody-datadog MCP server through cody-tools.

      First determine whether there is any material active issue in this
      environment. Check active monitors, incidents, events, service health,
      recent error spikes, logs, metrics, and DBM/APM signals when available.

      If there is no material issue, finish silently. Do not emit progress text
      and do not trigger Slack.

      If a material issue exists:

      1. Emit a concise assistant progress message saying the issue was
         detected and Cody is investigating. Kelos will create the Slack root
         message from that progress output.
      2. Gather Datadog evidence.
      3. Gather Kubernetes, Flux, GitOps, Jira, and GitHub evidence only as
         needed.
      4. Produce an RCA.
      5. If a safe fix exists, create or reuse an ALPM Jira ticket and open a
         small PR.
      6. Include RCA and PR/blocked details in progress/final output so Kelos
         can update the same Slack message.
    metadata:
      labels:
        cody.alpheya.com/workflow: infra-health
        cody.alpheya.com/source: cron
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
        - name: CODY_INFRA_HEALTH_SIGNAL_SOURCE
          value: datadog
        - name: CODY_INFRA_HEALTH_ENVIRONMENT
          value: qa
        - name: CODY_INFRA_HEALTH_NAMESPACE
          value: qa
        - name: KUBERNETES_CLUSTER_NAME
          value: non-prod
```

### Representative Kubernetes/Logs TaskSpawner

For an environment that should use in-cluster evidence instead of Datadog, keep
the same Slack reporting contract but remove the Datadog MCP AgentConfigRef and
make the signal source explicit:

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-kubernetes-health-non-prod-qa
  namespace: kelos-system
  labels:
    cody.alpheya.com/workflow: infra-health
    cody.alpheya.com/source: cron
spec:
  maxConcurrency: 1
  when:
    cron:
      schedule: "*/30 * * * *"
  taskTemplate:
    type: codex
    credentials:
      type: oauth
      secretRef:
        name: cody-codex-credentials
    image: docker.io/alpheya/codex:main
    agentConfigRefs:
      - name: cody-debugger
      - name: cody-infra-health-scheduled
      - name: cody-atlassian-mcp
    promptTemplate: |
      Scheduled Kubernetes infra-health check.

      Environment: qa
      Namespace: qa
      Cluster: non-prod
      Primary signal source: kubernetes
      Lookback window: 30m
      Trigger time: {{.Time}}
      Schedule: {{.Schedule}}

      Do not use Datadog for this run. Use Kubernetes, Flux, HelmRelease,
      ExternalSecret, events, and bounded pod logs in namespace `qa` as the
      primary evidence.

      First determine whether there is any material active issue in this
      environment. Check pod status, restarts, OOMKilled, CrashLoopBackOff,
      ImagePullBackOff, pending pods, recent warning events, Flux status,
      HelmRelease failures, ExternalSecret sync errors, and recent logs in
      namespace `qa` only for affected workloads.

      If there is no material issue, finish silently. Do not emit progress text
      and do not trigger Slack.

      If a material issue exists, emit detected/RCA/final progress in the same
      shape as Datadog mode so Kelos updates the configured Slack message.
    metadata:
      labels:
        cody.alpheya.com/workflow: infra-health
        cody.alpheya.com/source: cron
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
        - name: CODY_INFRA_HEALTH_SIGNAL_SOURCE
          value: kubernetes
        - name: CODY_INFRA_HEALTH_ENVIRONMENT
          value: qa
        - name: CODY_INFRA_HEALTH_NAMESPACE
          value: qa
        - name: KUBERNETES_CLUSTER_NAME
          value: non-prod
```

### Environment-Specific TaskSpawners

Create separate TaskSpawners for each environment after the non-prod POC:

```text
cody-datadog-health-non-prod-qa
cody-datadog-health-non-prod-northeurope
cody-datadog-health-uat
```

Each TaskSpawner should define:

- environment name;
- namespace name, when the run should be namespace-scoped;
- cluster name;
- primary signal source: `datadog` or `kubernetes`;
- Datadog tag filter when signal source is `datadog`;
- Kubernetes namespace/service scope when signal source is `kubernetes`;
- lookback window;
- default Slack destination via `kelos.dev/slack-destination`;
- schedule;
- PR permission scope.

Avoid a single global TaskSpawner until the platform has durable dedupe and
repo/channel routing.

## Datadog Triage Query Strategy

Cody should use Datadog MCP in this order:

1. Active incidents and high-severity monitor states for the environment.
2. Recent events: deployments, monitor alerts, Kubernetes events, Datadog Agent
   events, service status changes.
3. Service-level health: error rate, latency, throughput, saturation.
4. Logs: sustained error/fatal/exception spikes scoped by service/namespace.
5. APM/traces: failing endpoints, high-latency traces, exceptions, spans.
6. DBM: blocked queries, connection exhaustion, long-running transactions,
   replication/WAL issues.
7. Infrastructure: host/node pressure, pod restarts, OOMs, ingress 5xx,
   Kubernetes workload health.

The prompt should instruct Cody to use Datadog links or IDs in the RCA where
available.

## Kubernetes/Logs Triage Strategy

When `CODY_INFRA_HEALTH_SIGNAL_SOURCE=kubernetes`, Cody should use in-cluster
signals in this order:

1. Flux and rollout health:
   - `flux get kustomizations -A`;
   - `flux get helmreleases -A`;
   - `kubectl get helmreleases -A`;
   - failed or stalled reconciliations in the target environment.
2. Workload health:
   - `kubectl get pods -A` with focus on non-running pods;
   - CrashLoopBackOff, OOMKilled, ImagePullBackOff, CreateContainerConfigError,
     pending pods, and high restart counts;
   - deployments/statefulsets/daemonsets with unavailable replicas.
3. Recent events:
   - warning events sorted by last timestamp;
   - events scoped to candidate namespaces before cluster-wide event review.
4. Bounded pod logs:
   - `kubectl logs --tail=<small limit> --since=<lookback>` for affected pods;
   - `kubectl logs --previous` only for restarted containers;
   - avoid reading all logs across all namespaces.
5. Config/dependency health:
   - ExternalSecret status and sync errors;
   - certificates and ingress/gateway status when symptoms point there;
   - node pressure only when pod scheduling/eviction symptoms point there.
6. GitOps/source context:
   - clone GitOps or source repos only after the failing workload/config surface
     is identified.

The RCA should include the exact Kubernetes resources, namespaces, events, and
log snippets used as evidence. It should also say when the investigation was
Kubernetes-mode and Datadog was intentionally not used.

## Materiality Policy

Do not investigate every warning.

Treat an issue as material if one or more are true:

- active critical monitor;
- active warning monitor sustained beyond the lookback window;
- Datadog incident open for the environment or Kubernetes/Flux health issue
  blocking the environment;
- service error rate or 5xx rate materially above baseline;
- log error spike across multiple pods or releases;
- CrashLoop/OOM/restart pattern on a production-like workload;
- DB health issue that can affect reads/writes;
- Flux/Helm deployment failure that blocks rollout;
- ExternalSecret/certificate/config issue that can break runtime behavior;
- repeated issue already seen in previous cron runs and still unresolved.

Treat as non-material:

- isolated single log line with no repetition;
- monitor already resolved;
- expected deploy transient shorter than the threshold;
- known noisy monitor with no user or platform impact;
- warning without actionable resource, service, or namespace context.

## Dedupe and Cooldown

The cron MVP does not have durable incident state.

Use these mitigations:

- `maxConcurrency: 1` per environment.
- Cron interval no shorter than the normal investigation time.
- Cody checks for existing open Jira tickets before creating a new one.
- Cody checks recent PRs/branches for the same service/issue before opening a
  new PR.
- Cody includes monitor/incident IDs in Jira/PR titles where available.
- Kelos does not create a Slack root message when no material issue exists.

Known limitation:

- The same persistent issue may trigger multiple scheduled Cody tasks until it
  is fixed, muted, resolved, or captured in durable incident state.

This limitation is acceptable for the first scheduled POC, but it is one reason
the end-state design should add durable incident correlation.

## Slack Lifecycle

For a material issue, Cody emits progress/final output and Kelos owns Slack
delivery:

1. `Detected`: Cody emits a short progress snapshot with environment, signal
   source, severity, affected service/namespace, source evidence link or
   resource ID, and "Cody is investigating." `kelos-slack-server` creates the
   root Slack message in the TaskSpawner's configured destination.
2. `RCA`: Cody emits a concise RCA progress snapshot. `kelos-slack-server`
   updates the same Slack message in place.
3. `PRCreated` or `Blocked`: Cody includes PR/Jira links, verification notes,
   rollback notes, or exact manual action in the final result.
   `kelos-slack-server` updates the same Slack message with the terminal
   summary.

For no material issue:

- no Slack delivery;
- do not emit progress/status text while scanning;
- finish silently so no Slack root message is created.

## PR Policy

Cody may create PRs for:

- GitOps resource requests/limits when OOM or throttling evidence is clear;
- probe/timeouts when evidence shows startup/readiness mismatch;
- Helm values, ExternalSecret references, or config mistakes;
- safe rollback/pin updates when deployment evidence is strong;
- small app fixes only when stack traces and source code point to a precise
  issue.

Cody must not create PRs for:

- vague "errors increased" findings without root cause;
- production changes without explicit policy approval;
- Datadog monitor changes in the MVP;
- large refactors;
- speculative retries/timeouts without evidence.

## Security

- Datadog credentials live only in the `cody-tools` Deployment.
- Datadog-mode Cody task pods call only the in-cluster `cody-tools` Datadog
  route.
- Kubernetes-mode Cody task pods do not need Datadog MCP access or Datadog
  credentials.
- Use scoped Datadog service-account credentials with read-only permissions.
- Keep Slack tokens out of Cody task pods and `cody-tools`; only
  `kelos-slack-server` consumes Slack tokens and route config.
- Keep GitHub credentials brokered through `cody-tools`.
- Keep Kubernetes access read-only except for PR-based remediation.
- Do not expose `/mcp/datadog` outside the cluster.
- Restrict NetworkPolicy so only Cody task pods can call `cody-tools`.

## Rollout Plan

### Phase 0: Source-Specific Manual Tasks

- For Datadog mode:
  - add Datadog MCP route to `cody-tools`;
  - add Datadog Secret/ExternalSecret and `cody-tools` env in
    `k8s-platform-gitops/non-prod/kelos`;
  - add `cody-datadog-mcp` AgentConfig in `skills/cody/infra-health`;
  - run a manually created Cody Task against non-prod;
  - verify Cody can list Datadog tools and query scoped read-only data;
  - verify credentials are not visible in task pod env or files.
- For Kubernetes mode:
  - run a manually created Cody Task with
    `CODY_INFRA_HEALTH_SIGNAL_SOURCE=kubernetes`;
  - verify it can read pods, events, pod logs, Flux/HelmRelease status, and
    ExternalSecret status through existing debugger RBAC;
  - verify it does not require Datadog MCP or Datadog credentials.

### Phase 1: Silent Cron POC

- Add the selected namespace-scoped non-prod/qa source-specific TaskSpawner in
  `skills/cody/infra-health/non-prod`.
- Schedule every 60 minutes.
- No Slack root message unless a material issue is detected.
- PR creation disabled by prompt except for a controlled GitOps fixture.
- Review task volume, cost, and useful findings.

### Phase 2: Lifecycle Slack

- Enable `kelos-slack-server` deferred Slack reporting and route
  resolution.
- Configure `cody-slack-routes` with `asd -> <Slack channel ID for #asd>`.
- Add `kelos.dev/slack-reporting=enabled` label plus deferred Slack
  annotations to the infra-health TaskSpawner template.
- Route detected/RCA/PR updates to the `asd` destination.
- Keep `maxConcurrency: 1`.
- Tighten materiality criteria based on observed noise.

### Phase 3: Broaden Scope

- Add non-prod-northeurope under `skills/cody/infra-health`.
- Reduce interval to 30 minutes if cost/noise is acceptable.
- Add DBM/APM toolsets if available and useful.
- Add Kubernetes-mode TaskSpawners for environments where cluster logs/events
  are the primary signal source.
- Allow PR creation for limited GitOps fixes.

### Phase 4: Compare Against Webhooks

- Compare Datadog cron findings with Alertmanager/Datadog monitor webhook
  findings.
- Keep cron for periodic broad health sweeps.
- Use webhooks for urgent event-driven incidents.

## Validation

Static validation:

- `go test ./cmd/cody-tools ./cmd/kelos-slack-server ./internal/reporting`
- Helm render for Kelos/cody-tools/slack-server changes.
- `kubectl kustomize non-prod/kelos`.
- `kubectl kustomize non-prod/cody` in `k8s-platform-gitops` to verify the Flux
  pointer resources.
- `kubectl kustomize cody` in the `skills` repo to verify Cody orchestration
  resources.
- Validate ExternalSecret and NetworkPolicy resources.

Runtime validation:

1. For Datadog mode, exec from a Cody task pod and confirm only `cody-tools` is
   reachable for Datadog MCP.
2. For Datadog mode, confirm Datadog API/application keys are not mounted into
   the Cody task pod.
3. For Datadog mode, call `/mcp/datadog` through the agent MCP client and list
   tools.
4. For Kubernetes mode, confirm a Cody task can read pods, events, bounded pod
   logs, Flux/HelmRelease status, and ExternalSecret status.
5. Run the non-prod cron TaskSpawner manually or with a one-off schedule.
6. Verify no Slack message is created when no material issue exists.
7. Create or identify a safe synthetic signal for the selected source.
8. Verify Cody emits detected/investigating progress and `kelos-slack-server`
   creates a new root Slack message in the configured Slack destination.
9. Verify Cody collects primary-source evidence before GitOps/source evidence.
10. Verify RCA/progress updates edit the same Slack message.
11. Verify Cody does not open a PR without a concrete fix.
12. Verify Cody opens a small PR for a controlled GitOps fixture.
13. Verify the Task has `kelos.dev/slack-thread-ts` and
    `kelos.dev/slack-message-ts` annotations after the first Slack post.

## Failure Modes

| Failure | Expected behavior |
| --- | --- |
| Datadog MCP unavailable | Datadog-mode Cody switches to Kubernetes fallback. If fallback finds a material issue, the RCA says Datadog was unavailable and Kubernetes supplied the evidence. |
| Datadog credentials invalid | `cody-tools` returns service unavailable; no Datadog credentials leak to Cody logs; Datadog-mode Cody uses Kubernetes fallback. |
| Datadog MCP tool names change | Cody uses available tools when possible; if the Datadog scan is unusable, Cody uses Kubernetes fallback. |
| Kubernetes API/log access insufficient | Kubernetes-mode or fallback Cody reports the exact missing RBAC/resource access and exits blocked without claiming an RCA. |
| Cron fires while prior run is active | `maxConcurrency: 1` prevents overlapping tasks for the same environment. |
| No material issue exists | Cody exits quietly without progress output; Kelos creates no Slack message. |
| Same issue persists across cron runs | Cody should reuse existing Jira/PR context when found; durable dedupe is deferred to the end-state incident intake. |
| `kelos-slack-server` unavailable | Cody completes investigation; Slack delivery resumes only when the reporter can process the labeled Task and read its progress/final output. |
| Slack destination missing or unknown | Cody completes investigation; reporter skips Slack delivery because it cannot resolve a safe destination. |
| GitHub unavailable | Cody posts blocked status with intended repo/diff summary. |
| Datadog returns too much data | Cody narrows queries by env/cluster/service/time window and summarizes instead of dumping raw logs. |
| Kubernetes mode finds too many logs/events | Cody narrows by namespace, workload, event reason, and lookback before reading bounded logs. |

## Comparison With Alertmanager Webhook MVP

Datadog cron path:

- best for broad periodic health sweeps;
- can cover infra and functional/app signals;
- does not require alert label edits;
- is slower and creates scheduled tasks even when no issue exists.

Alertmanager webhook path:

- best for precise event-driven triggers;
- cheaper because tasks start only on alerts;
- receives native alert payloads;
- requires alert rules/labels/routes.

Recommended near-term posture:

- Use Datadog cron as the fastest broad Cody health-check POC.
- Keep Alertmanager or Datadog monitor webhooks for event-driven incident
  triggers once signal quality and routing are clear.

## Cleanup and Migration Plan

The repo split should converge on this shape:

- `skills/cody` owns all Cody workflow-specific CRs:
  - purpose directories such as `self-development`, `infra-health`, `security`,
    `qa`;
  - repo/environment overlays;
  - TaskSpawners, Workspaces, and purpose-specific AgentConfigs.
- `k8s-platform-gitops/non-prod/kelos` owns only shared runtime/platform pieces:
  - Kelos controller Helm release and patches;
  - `cody-tools` Deployment/Service/NetworkPolicy;
  - external secrets;
  - debugger/service-account RBAC;
  - shared base/runtime AgentConfigs while they are still consumed by multiple
    workflows.
- `k8s-platform-gitops/non-prod/cody` remains the single Flux pointer to
  `skills/cody`.

Cleanup steps:

1. Keep the existing `skills/cody/self-development/investor-adib` flow as the
   reference pattern for new purpose directories.
2. Add the infra-health cron manifests in `skills/cody` first; do not duplicate
   them in `k8s-platform-gitops/non-prod/kelos`.
3. After the infra-health rollout proves the pointer/prune behavior, migrate
   existing workflow TaskSpawners that still live under
   `k8s-platform-gitops/non-prod/kelos` into appropriate `skills/cody`
   purpose directories.
4. Avoid duplicate resource names during migration: remove a TaskSpawner from
   platform GitOps in the same release that adds the equivalent resource under
   `skills/cody`, or suspend first if a staged rollout is safer.
5. Leave `cody-tools`, secrets, and RBAC in platform GitOps until there is a
   deliberate runtime packaging story for them. They are not skill content.
6. Consider moving shared runtime AgentConfigs such as `cody-base`,
   `cody-dev`, `cody-debugger`, and `cody-atlassian-mcp` into `skills/cody`
   only after all current consumers are accounted for and resource ownership is
   unambiguous.
7. Update `skills/cody/README.md` whenever a new purpose directory lands so it
   remains the operator map for Cody workflows.

## Exit Criteria

- For Datadog-mode environments, `cody-tools` exposes `/mcp/datadog` and
  injects Datadog credentials server-side.
- Cody task pods do not contain Datadog credentials.
- For Kubernetes-mode environments, Cody can inspect pod health, events, bounded
  logs, Flux/HelmRelease status, and ExternalSecret status without Datadog.
- A non-prod cron TaskSpawner from `skills/cody/infra-health` runs on schedule
  through the existing Flux pointer.
- Cody can determine "no material issue" and exit quietly.
- Cody can detect a controlled material issue and Kelos can create/update the
  lifecycle Slack message through deferred Task reporting.
- Cody can use the configured primary signal source to produce an RCA.
- Cody opens PRs only for evidence-backed, reviewable fixes.
- No Kelos controller or CRD changes are required.

## Open Questions

- Which Datadog service account and role should own the MCP API/application
  keys?
- Which Datadog toolsets are enabled in the tenant today?
- Are preview toolsets such as APM available and acceptable for Cody?
- What are the exact Datadog environment tags for each cluster?
- Which environments should use Datadog mode, and which should use
  Kubernetes/logs mode?
- Which Slack destinations should each environment use? For this POC, the
  desired route is `asd -> <Slack channel ID for #asd>`.
- Should production cron runs be allowed to create PRs, or only RCA/Jira?
- Should Datadog monitor webhooks replace cron for high-severity monitors after
  the POC?
