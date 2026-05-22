# Cody Personas Phase 1 Implementation Spec

## Status

Implementation-ready Phase 1 spec.

Scope: `k8s-platform-gitops/non-prod/kelos`.

Kelos code changes: none.

This spec assumes the broader design in
`specs/2026-05-22-12-00-cody-personas-and-handoffs.md`, but narrows the work
to the exact GitOps-only changes needed for Phase 1.

## Summary

Phase 1 adds explicit Cody personas while preserving current Cody behavior
outside newly reserved persona prefixes.

New Slack entrypoints:

- `@cody !ticket ...`
- `@cody !dev ...`
- `@cody !review ...`

No router persona, no automatic handoff, no new Kelos CRD/API fields, no new
service accounts, no Slack channel whitelist, and no GitHub triggers in Phase
1.

All Phase 1 persona TaskSpawners reuse the current Cody runtime shape:

- `type: codex`
- `image: docker.io/alpheya/codex:main`
- `credentials.secretRef.name: cody-codex-credentials`
- `podOverrides.serviceAccountName: cody-debugger`
- current GitHub App env from `cody-github-app`, used by the agent for GitHub
  API/clone/PR operations when a Slack request requires them
- current JWT signing env from `cody-jwt-signing`
- `podOverrides.labels.cody.alpheya.com/tools-client: "true"`

Important naming detail: `cody-github-app` is a Kubernetes Secret name, not the
GitHub App name. The actual GitHub App currently wired through that Secret is
the existing `cursor` GitHub App, ID `3429269`, using Key Vault keys named
`cursor-github-app-*`.

Persona-specific RBAC and narrower pod env are intentionally deferred.

## Behavioral Contract

### Slack routing

Kelos strips the leading bot mention before evaluating Slack trigger regexes.
The persona routes therefore match on `^!ticket`, `^!dev`, and `^!review`,
while users type `@cody !ticket ...`, `@cody !dev ...`, or
`@cody !review ...`.

| User message | Expected TaskSpawner | Notes |
| --- | --- | --- |
| `@cody why is X broken?` | existing `cody-debug-slack` | Unchanged. |
| `@cody debug X in dev` | existing `cody-debug-slack` | Unchanged. |
| `@cody !alpha debug X` | existing `cody-debug-alpha-slack` | Unchanged. |
| `@cody !exp debug X` | existing `cody-debug-alpha-slack` | Unchanged. |
| `@cody !ticket create a ticket for X` | new `cody-ticket-slack` | New reserved prefix. |
| `@cody !dev fix ALPM-123` | new `cody-dev-slack` | New reserved prefix. |
| `@cody !review https://github.com/.../pull/123` | new `cody-pr-reviewer-slack` | New reserved prefix. Slack-only trigger; include a PR URL. |
| `!dev fix X` without `@cody` | no Cody task | `mentionOptional` must not be set. |

## Non-Goals

- No router persona.
- No Phase 1 automatic handoff.
- No new Kelos code, CRD, controller, webhook, or image change.
- No GitHub webhook, GitHub PR polling, GitHub comment, or GitHub label
  triggers.
- No Slack `channels[]` filtering.
- No Slack `mentionOptional: true`.
- No new service accounts or RBAC.
- No changes to `cody-debug-alpha-slack`.
- No behavior change for normal `@cody ...` messages outside the reserved
  `!ticket`, `!dev`, and `!review` prefixes.

## Files To Change

### Modify existing files

`non-prod/kelos/taskspawner-cody-debug.yaml`

Add the new reserved prefixes to the stable catch-all exclusion list:

```yaml
excludePatterns:
  - '^!(alpha|exp)\b'
  - '^!(ticket|dev|review)\b'
```

Do not otherwise change the stable debugger TaskSpawner.

`non-prod/kelos/kustomization.yaml`

Add the new AgentConfigs and Slack TaskSpawners. Keep ordering readable:

1. secrets/tools
2. RBAC
3. AgentConfigs
4. TaskSpawners

### Add new files

Required:

- `non-prod/kelos/agentconfig-cody-base.yaml`
- `non-prod/kelos/agentconfig-cody-ticket-creator.yaml`
- `non-prod/kelos/agentconfig-cody-dev.yaml`
- `non-prod/kelos/agentconfig-cody-pr-reviewer.yaml`
- `non-prod/kelos/taskspawner-cody-ticket.yaml`
- `non-prod/kelos/taskspawner-cody-dev.yaml`
- `non-prod/kelos/taskspawner-cody-pr-reviewer-slack.yaml`

Use persona-specific filenames so future routes can be added mechanically and
reviewed separately.

## Shared TaskSpawner Runtime

For Phase 1, every new persona TaskSpawner should copy the current
`taskspawner-cody-debug.yaml` runtime block instead of introducing new RBAC or
secret wiring.

Required fields:

```yaml
taskTemplate:
  type: codex
  credentials:
    type: oauth
    secretRef:
      name: cody-codex-credentials
  image: docker.io/alpheya/codex:main
  ttlSecondsAfterFinished: 3600
  podOverrides:
    labels:
      cody.alpheya.com/tools-client: "true"
    serviceAccountName: cody-debugger
    env:
      - name: GITHUB_APP_CLIENT_ID
        valueFrom:
          secretKeyRef:
            name: cody-github-app
            key: GITHUB_APP_CLIENT_ID
      - name: GITHUB_APP_INSTALLATION_ID
        valueFrom:
          secretKeyRef:
            name: cody-github-app
            key: GITHUB_APP_INSTALLATION_ID
      - name: GITHUB_APP_PRIVATE_KEY
        valueFrom:
          secretKeyRef:
            name: cody-github-app
            key: GITHUB_APP_PRIVATE_KEY
      - name: KUBERNETES_CLUSTER_NAME
        value: non-prod
      - name: ALPHEYA_TOKEN_SIGNING_KEY
        valueFrom:
          secretKeyRef:
            name: cody-jwt-signing
            key: key.pem
      - name: ALPHEYA_TOKEN_SIGNING_KEY_ID
        value: ca1858fd-4624-4524-be5b-8c4f265ada2a
      - name: ALPHEYA_TOKEN_SIGNING_ISSUER
        value: https://auth.qwlth.dev
      - name: ALPHEYA_TOKEN_SIGNING_AUDIENCE
        value: alpheya
      - name: ALPHEYA_TOKEN_SIGNING_DEFAULT_CLAIMS
        value: |-
          {
            "sub": "6ab6d10e-5f43-4e74-9dca-e99e7c7c73dd",
            "roles": [
              "all_access:int",
              "all_access:int2",
              "all_access:dq-dev",
              "all_access:performance",
              "head_of_wealth:integration-testing",
              "tenant_group_admin",
              "iam_admin"
            ],
            "email": "cody@alpheya.com",
            "name": "Cody Developer",
            "ext": {
              "sub": "3abc9f82-ca4b-49ad-b3d2-3fe9723ed2e5",
              "preferred_username": "cody@alpheya.com"
            }
          }
```

This is intentionally broad and matches current Cody. Create a separate
hardening PR later to split service accounts and env by persona.

## AgentConfigs

### `cody-base`

Purpose: short shared Cody rules used by all new personas. Do not refactor the
existing debugger AgentConfig into this in Phase 1.

Required behavior:

- identify as Cody for Alpheya internal work
- stay inside the requested persona
- use evidence over guesses
- never expose secrets
- use PRs for mutations
- keep user-visible replies concise
- if the request belongs to another persona, say which `@cody !...` command to
  use instead of doing the work

### `cody-ticket-creator`

Purpose: create or update Jira tickets from a Slack request.

Use `agentConfigRefs`:

```yaml
agentConfigRefs:
  - name: cody-base
  - name: cody-ticket-creator
  - name: cody-atlassian-mcp
```

Required instructions:

- Treat the leading `!ticket` as routing metadata.
- Use only Atlassian site `wgen4.atlassian.net`.
- Search Jira first for obvious duplicates when the request names an existing
  issue, PR, service, incident, or ALPM key.
- Create in project `ALPM` unless the user explicitly supplies another project
  and the MCP metadata confirms it is valid.
- If required Jira fields are missing, ask one concise follow-up in Slack.
- The ticket should include:
  - title
  - problem statement
  - source Slack thread URL
  - affected repo/service/env if present
  - evidence copied from the request
  - acceptance criteria
  - suggested owner/team if obvious
- Do not implement code.
- Do not open PRs.
- If the user asked for implementation too, create the ticket first and reply
  with the exact follow-up command, for example
  `@cody !dev ALPM-123 implement <summary>`.

Slack response shape:

```text
Created ALPM-123: <title>
Next: @cody !dev ALPM-123 <short implementation request>
```

If no ticket is created:

```text
I need <missing field> before I can create the ticket.
```

### `cody-dev`

Purpose: implement small, reviewable code or GitOps changes and open a PR.

Use `agentConfigRefs`:

```yaml
agentConfigRefs:
  - name: cody-base
  - name: cody-dev
  - name: cody-atlassian-mcp
```

Required instructions:

- Treat the leading `!dev` as routing metadata.
- Reuse an existing `ALPM-<number>` if present.
- If no ALPM key is present, create one through Atlassian MCP before opening a
  PR. If Jira creation is blocked by missing required fields, ask the user and
  do not open a PR.
- Clone only the repo or repos needed for the requested change.
- Prefer small diffs.
- Do not directly mutate Kubernetes resources.
- For deploy config changes, use PRs against the appropriate GitOps repo.
- For code changes, follow the target repo's local instructions.
- Branch naming: `cody/<alpm-key>-<short-slug>` when an ALPM key exists.
- Commit/PR title should include the ALPM key.
- PR body must include:
  - ticket link
  - summary
  - verification
  - risk/rollback notes when relevant

Slack response shape:

```text
Opened PR: <url>
Ticket: ALPM-123
Verification: <what was run or why not run>
```

If blocked:

```text
Blocked: <specific missing info or failing check>
Next: <one concrete ask>
```

### `cody-pr-reviewer`

Purpose: review pull requests from Slack-provided PR URLs.

Use `agentConfigRefs`:

```yaml
agentConfigRefs:
  - name: cody-base
  - name: cody-pr-reviewer
  - name: cody-atlassian-mcp
```

Required instructions:

- Treat the leading `!review` as routing metadata for Slack.
- Review only; do not push commits unless the user explicitly asks for fixes.
- Focus on correctness, regressions, missing tests, security, data leakage, and
  deploy risk.
- Prefer actionable findings with file/line references.
- If no material issues are found, say that clearly and list remaining test
  gaps or residual risk.
- Reply in the Slack thread.

Review output shape:

```text
Findings
- [P1/P2/P3] <issue> <file:line> - <why it matters>

Open questions
- <only if needed>

Summary
<one short paragraph>
```

## Slack TaskSpawners

### `taskspawner-cody-ticket.yaml`

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-ticket-slack
  namespace: kelos-system
spec:
  maxConcurrency: 2
  when:
    slack:
      triggers:
        - pattern: '^!ticket\b'
  taskTemplate:
    # shared runtime block from this spec
    agentConfigRefs:
      - name: cody-base
      - name: cody-ticket-creator
      - name: cody-atlassian-mcp
    promptTemplate: |
      Cody ticket creator request from Slack.

      Treat `!ticket` as routing metadata and remove it before
      interpreting the request.

      Slack message:
        {{.Body}}

      Slack thread: {{.URL}}

      Reply once in the same thread.
    metadata:
      labels:
        cody.alpheya.com/persona: ticket-creator
        cody.alpheya.com/source: slack
```

### `taskspawner-cody-dev.yaml`

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-dev-slack
  namespace: kelos-system
spec:
  maxConcurrency: 1
  when:
    slack:
      triggers:
        - pattern: '^!dev\b'
  taskTemplate:
    # shared runtime block from this spec
    agentConfigRefs:
      - name: cody-base
      - name: cody-dev
      - name: cody-atlassian-mcp
    promptTemplate: |
      Cody dev request from Slack.

      Treat `!dev` as routing metadata and remove it before
      interpreting the request.

      Slack message:
        {{.Body}}

      Slack thread: {{.URL}}

      Reply once in the same thread.
    metadata:
      labels:
        cody.alpheya.com/persona: dev
        cody.alpheya.com/source: slack
```

### `taskspawner-cody-pr-reviewer-slack.yaml`

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: cody-pr-reviewer-slack
  namespace: kelos-system
spec:
  maxConcurrency: 2
  when:
    slack:
      triggers:
        - pattern: '^!review\b'
  taskTemplate:
    # shared runtime block from this spec
    agentConfigRefs:
      - name: cody-base
      - name: cody-pr-reviewer
      - name: cody-atlassian-mcp
    promptTemplate: |
      Cody PR review request from Slack.

      Treat `!review` as routing metadata and remove it before
      interpreting the request.

      Slack message:
        {{.Body}}

      Slack thread: {{.URL}}

      If the message includes a PR URL, review that PR. If it does not
      include a PR URL or enough repo/PR information, ask for the PR URL.

      Reply once in the same thread.
    metadata:
      labels:
        cody.alpheya.com/persona: pr-reviewer
        cody.alpheya.com/source: slack
```

Slack TaskSpawner requirements:

- no `channels[]`
- no `mentionOptional`
- no `allowedBotIDs` unless a separate trusted bot use case is approved

## Validation Plan

### Static validation

Run from `k8s-platform-gitops`:

```bash
kustomize build non-prod/kelos
```

If the repo normally uses a wrapper, use that wrapper instead. The important
gate is that the rendered Kelos bundle is valid YAML and includes the new
resources once each.

### Behavioral validation

Slack:

1. Send `@cody !ticket test ticket creation request`.
   - Expect one Task from `cody-ticket-slack`.
   - Expect no Task from `cody-debug-slack`.
2. Send `@cody !dev test dev request`.
   - Expect one Task from `cody-dev-slack`.
   - Expect no Task from `cody-debug-slack`.
3. Send `@cody !review <PR URL>`.
   - Expect one Task from `cody-pr-reviewer-slack`.
   - Expect no Task from `cody-debug-slack`.
4. Send `@cody debug some-service`.
   - Expect existing `cody-debug-slack`.
5. Send `@cody !alpha debug some-service`.
   - Expect existing `cody-debug-alpha-slack`.

Kubernetes:

```bash
kubectl get task -n kelos-system -l cody.alpheya.com/persona
kubectl get taskspawner -n kelos-system
```

Confirm new Tasks have:

- `cody.alpheya.com/persona`
- `cody.alpheya.com/source`
- `kelos.dev/taskspawner`

## Rollback

Rollback is GitOps-only:

1. Remove new persona TaskSpawners from `kustomization.yaml`.
2. Remove `^!(ticket|dev|review)\b` from
   `taskspawner-cody-debug.yaml.excludePatterns`.
3. Leave AgentConfigs in place if harmless, or remove them from
   `kustomization.yaml` in the same rollback.

After rollback:

- `@cody !ticket`, `@cody !dev`, and `@cody !review` fall back to the existing
  stable debugger route.
- `@cody !alpha` / `@cody !exp` still route to alpha.

## Follow-Up Hardening

After Phase 1 behavior is proven:

- split service accounts by persona
- remove tenant Secret read access from ticket creator and PR reviewer
- remove JWT signing env from personas that do not need it
- evaluate GitHub webhook/PR polling triggers later, after Slack-only Phase 1
  proves useful and repository/workspace ownership is clear
- implement Phase 2 handoff with structured Task results
