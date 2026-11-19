# Agora-Development Orchestration Patterns

This directory contains the orchestration patterns that drive autonomous
development of [`kelos-dev/agora`](https://github.com/kelos-dev/agora) — a
local coordination server for humans and coding agents.

It extends the self-development setup in this repository. The configs live
under `self-development/agora/` in the kelos repository, but the webhook filters
and Workspaces target `kelos-dev/agora`, so the agents they spawn operate on
the Agora repository.

## How It Works

Every spawner references the root [`base-agent`](../base-agent.yaml) for shared
instructions and skills. The issue and PR pick-up SessionSpawners reference
only `base-agent`. The remaining TaskSpawners add repository- or role-specific
instructions where needed: triage and squash-commits share `agentconfig.yaml`
(`agora-dev-agent`), while planner, the reviewer, fake-user, and fake-strategist
define their own AgentConfig inline.

Autonomous discovery agents that publish GitHub issues maintain at most one
open `generated-by-kelos` issue slot per TaskSpawner. Its title starts with the
TaskSpawner name in brackets, and its body includes both a
`kelos-taskspawner=<name>` marker and one replaceable `Latest verdict` section.
Each run checks whether an unassigned slot is still valid against the current
repository before retaining, replacing, or closing it. Assigned issues and PRs
are treated as ongoing human or agent work and are not updated by autonomous
discovery jobs.

The two SessionSpawners operate on the Agora repository through the
`agora-session-agent` Workspace, which uses the personal Session token. Six
TaskSpawners use the `agora-agent` Workspace. The two meta-maintenance spawners
(`agora-config-update`, `agora-self-update`) are different: the files they
maintain (`self-development/agora/*`) live in *this* repository, so they use the
`kelos-agent` Workspace and the `kelos-dev-agent` role AgentConfig from
`self-development/`, and they read Agora's activity cross-repo with
`gh ... --repo kelos-dev/agora`.

## Spawners

| Spawner | Trigger | Agent | Description |
|---|---|---|---|
| **agora-workers** | Webhook: issue comment `/kelos pick-up` | GPT-6.2 Astra | Creates a durable Session with the open issue URL and a dedicated issue branch |
| **agora-planner** | Webhook: issue comment `/kelos plan` | Claude Opus | Investigates an issue and posts a structured implementation plan — advisory only, no code changes |
| **agora-reviewer** | Webhook: PR comment `/kelos review` | Claude Opus | Reviews PRs on demand — analyzes code, checks conventions, and updates a sticky review comment |
| **agora-pr-responder** | Webhook: PR review/comment with `/kelos pick-up` | GPT-6.2 Astra | Creates a durable Session with the open PR URL on its existing branch |
| **agora-triage** | Webhook: issue opened/reopened (untriaged) | Claude Opus | Classifies issues by kind/priority, detects duplicates, and recommends an actor |
| **agora-fake-user** | Cron (daily 09:00 UTC) | Claude Sonnet | Tests DX as a new user and maintains one unassigned issue slot for the highest-impact problem found |
| **agora-fake-strategist** | Cron (every 12 hours) | Claude Opus | Explores new use cases, integrations, and API/UI/deployment capabilities while maintaining one unassigned strategic issue slot |
| **agora-config-update** | Cron (daily 18:00 UTC) | Claude Opus | Reviews recent Agora PR feedback and creates or updates unassigned configuration PRs accordingly |
| **agora-self-update** | Cron (daily 06:00 UTC) | Claude Opus | Reviews and tunes the `self-development/agora/` prompts, configs, and README while maintaining one unassigned improvement issue slot |
| **agora-squash-commits** | Webhook: PR comment `/kelos squash-commits` | Claude Sonnet | Rebases and squashes PR branch commits into a single clean commit |

> **Not ported from `self-development/`:** the Kelos API reviewers (Agora has no
> Kubernetes CRDs to review) and `kelos-image-update` (it updates
> coding-agent image versions, not service base images).

Apply the shared Workspaces and root `base-agent` first, then the whole
directory. The directory includes `agentconfig.yaml`, which defines the
`agora-dev-agent` role instructions referenced by the triage and
squash-commits spawners:

```bash
kubectl apply -f self-development/workspaces.yaml
kubectl apply -f self-development/session-workspaces.yaml
kubectl apply -f self-development/base-agent.yaml
kubectl apply -f self-development/agora/
```

The per-spawner `kubectl apply` commands below are for deploying or updating an
individual spawner after `base-agent` is installed.

### agora-workers.yaml

Picks up open GitHub issues when a maintainer posts `/kelos pick-up` and
creates a durable Session. The initial prompt supplies the issue URL and asks
the agent to find the best way to address it.

| | |
|---|---|
| **Trigger** | GitHub `issue_comment` webhook with `/kelos pick-up` |
| **Agent** | GPT-6.2 Astra via Codex |
| **Storage** | 10 Gi PVC |

**Key features:**
- Lets the agent choose how to address the linked issue
- Uses `agora-task-<number>` for the issue branch
- Requires a `/kelos pick-up` comment to pick up an issue (maintainer approval gate)
- Keeps the workspace across Session follow-ups and pod restarts
- Supports routine follow-ups through the Session's web or terminal clients

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-workers.yaml
```

### agora-planner.yaml

Reacts to `/kelos plan` comments on open issues. Investigates the issue, inspects the codebase, and posts a structured implementation plan — advisory only, no code changes. For issues that touch the HTTP API, event/status types, environment variables, browser UI, Kubernetes sample, Docker image contract, JSONL storage, or reporting skill interface, the plan must resolve naming, shape, and backward compatibility up front.

| | |
|---|---|
| **Trigger** | GitHub `issue_comment` webhook with `/kelos plan` |
| **Agent** | Claude Opus via Claude Code |
| **Concurrency** | 2 |

**Handoff flow:**
1. `/kelos plan` — requests or refreshes an implementation plan
2. `/kelos pick-up` — maintainer hands off to workers when ready

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-planner.yaml
```

### agora-reviewer.yaml

Reviews open pull requests on demand when a maintainer or `kelos-bot[bot]`
posts `/kelos review`.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos review` from a maintainer or `kelos-bot[bot]` |
| **Agent** | Claude Opus via Claude Code |
| **Concurrency** | 3 |

**Key features:**
- Reads the full diff and surrounding context to understand changes
- Checks correctness, tests, project conventions, security, and code quality
- Pays special attention to public-contract changes (naming, shape, backward compatibility)
- Creates or updates a single sticky PR comment with the structured review result
- Summarizes specific file/line findings in the sticky comment without inline review comments
- Read-only agent — does not push code, modify files, or run local validation

**Handoff flow:**
1. `/kelos review` — a maintainer or `kelos-bot[bot]` requests a code review
2. `/kelos review` — a maintainer can retrigger review after changes are pushed

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-reviewer.yaml
```

### agora-pr-responder.yaml

Picks up open GitHub pull requests when a reviewer requests changes with
`/kelos pick-up`. The initial prompt supplies the PR URL and asks the agent to
find the best way to address it.

| | |
|---|---|
| **Trigger** | GitHub PR comment with `/kelos pick-up`, or a PR review whose body contains `/kelos pick-up` |
| **Agent** | GPT-6.2 Astra via Codex |
| **Storage** | 10 Gi PVC |

**Key features:**
- Starts the Session on the existing PR branch
- Lets the agent choose how to address the linked PR
- Lets the maintainer stay on the PR page for the common review-feedback loop
- Requires a `/kelos pick-up` PR comment or review body to be picked up
- Keeps the workspace across Session follow-ups and pod restarts
- Supports routine follow-ups through the Session's web or terminal clients

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-pr-responder.yaml
```

### agora-triage.yaml

Triages newly opened (and certain reopened) GitHub issues.

| | |
|---|---|
| **Trigger** | GitHub issue opened (no `triage-accepted`), or reopened with `needs-actor` |
| **Agent** | Claude Opus via Claude Code |
| **Concurrency** | 8 |

**For each issue, the agent:**
1. Classifies with exactly one `kind/*` label (`kind/bug`, `kind/feature`, `kind/api`, `kind/docs`). `kind/api` covers changes to public contracts such as HTTP API endpoints and payloads, event/status types, environment variables, JSONL storage, Kubernetes sample behavior, Docker image contract, browser UI behavior, or the reporting skill interface.
2. Checks if the issue has already been fixed by a merged PR or recent commit
3. Checks if the issue references outdated API endpoints, fields, event/status types, environment variables, manifests, or skill behavior
4. Detects duplicate issues
5. Assesses priority (`priority/important-soon`, `priority/important-longterm`, `priority/backlog`)
6. Recommends an actor — assigns `actor/kelos` if the issue has clear scope and verifiable criteria, otherwise `actor/human`. `kind/api` issues always get `actor/human` and are **not** marked `triage-accepted`, because new user-facing surface must be reviewed with a maintainer first.

Posts a single triage comment and adds `triage-accepted` to prevent re-triage.

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-triage.yaml
```

### agora-fake-user.yaml

Runs daily to test the developer experience as if you were a new user.

| | |
|---|---|
| **Trigger** | Cron `0 9 * * *` (daily at 09:00 UTC) |
| **Agent** | Claude Sonnet via Claude Code |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Documentation & Onboarding** — follow the README run instructions and API examples
- **Developer Experience** — build, run locally, exercise inbox/status/timeline flows, and review error messages
- **Examples & Use Cases** — verify the Kubernetes sample and identify missing examples

Creates or updates the single unassigned `agora-fake-user` issue slot for the
highest-impact problem found. If that issue is assigned, the run treats it as
ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-fake-user.yaml
```

### agora-fake-strategist.yaml

Runs every 12 hours to strategically explore new ways to use and improve Agora.

| | |
|---|---|
| **Trigger** | Cron `0 */12 * * *` (every 12 hours) |
| **Agent** | Claude Opus via Claude Code |
| **Concurrency** | 1 |

Each run picks one focus area:
- **New Use Cases** — explore teams and workflows that could benefit from a shared coordination timeline and targeted inboxes
- **Integration Opportunities** — identify agents, CI jobs, chat tools, and orchestration systems Agora could integrate with
- **New API, UI, or Deployment Capabilities** — propose small extensions to event/status workflows, inboxes, the browser UI, Kubernetes deployment, or reporting skill

Creates or updates the single unassigned `agora-fake-strategist` issue slot for
the highest-impact actionable insight. If that issue is assigned, the run treats
it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-fake-strategist.yaml
```

### agora-config-update.yaml

Runs daily to update the Agora agent configuration based on patterns found in Agora's PR reviews.

| | |
|---|---|
| **Trigger** | Cron `0 18 * * *` (daily at 18:00 UTC) |
| **Agent** | Claude Opus via Claude Code |
| **Workspace** | `kelos-agent` (edits `self-development/agora/` in this repo) |
| **Concurrency** | 1 |

Reviews recent `kelos-dev/agora` PRs and their review comments to identify recurring feedback patterns, then updates the configuration under `self-development/agora/` (the shared Agora role instructions in `agentconfig.yaml` or a specific TaskSpawner prompt). Opens a PR against this repository using `/kind cleanup` and `release-note: NONE`, since it only touches `self-development/agora/`.
Skips uncertain or contradictory feedback, and skips an existing configuration
PR when it has assignees.

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-config-update.yaml
```

### agora-self-update.yaml

Runs daily to review and improve the `self-development/agora/` workflow files themselves.

| | |
|---|---|
| **Trigger** | Cron `0 6 * * *` (daily at 06:00 UTC) |
| **Agent** | Claude Opus via Claude Code |
| **Workspace** | `kelos-agent` (reasons about `self-development/agora/` in this repo) |
| **Concurrency** | 1 |

Each run picks one focus area: **Prompt Tuning**, **Configuration Alignment**, or **Workflow Completeness**.

Creates or updates the single unassigned `agora-self-update` issue slot for the
highest-impact actionable improvement. If that issue is assigned, the run treats
it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-self-update.yaml
```

### agora-squash-commits.yaml

Rebases and squashes PR branch commits into a single clean commit when a maintainer posts `/kelos squash-commits`.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos squash-commits` |
| **Agent** | Claude Sonnet via Claude Code |
| **Concurrency** | 1 |

**Key features:**
- Rebases the PR branch on `origin/main` and squashes all commits after the merge base into one
- Amends the squashed commit message based on the linked issue and PR description when needed
- Force-pushes with `--force-with-lease`
- Adds `kelos/needs-input` to the linked issue to signal the PR is ready for re-review
- Does not start new development work or modify source code

**Deploy:**
```bash
kubectl apply -f self-development/agora/agora-squash-commits.yaml
```

## Prerequisites

These spawners are applied to the same cluster that runs `self-development/`.
Before deploying them, set up the following.

### 1. Workspaces

Three Workspaces are referenced:

- **`agora-session-agent`** — points at the Agora repository and is used only
  by `agora-workers` and `agora-pr-responder`. It is defined in
  [`session-workspaces.yaml`](../session-workspaces.yaml) and references the
  `personal-github-token` Secret.

- **`agora-agent`** — points at the Agora repository and is used by the six
  TaskSpawners that operate directly on Agora. It is defined in
  [`workspaces.yaml`](../workspaces.yaml) and references the
  `kelos-agent-credentials` Secret.

- **`kelos-agent`** — points at this repository (`kelos-dev/kelos`). Used by
  `agora-config-update` and `agora-self-update`, which edit the
  `self-development/agora/` files that live here. It is also defined in
  [`workspaces.yaml`](../workspaces.yaml) and references the
  `kelos-agent-credentials` Secret.

### 2. Repository labels

The Agora repository starts with only the default GitHub labels. Create the
labels these spawners rely on (run once, against `kelos-dev/agora`):

```bash
REPO=kelos-dev/agora
gh label create generated-by-kelos --repo "$REPO" --color 1d76db --force
for l in kind/bug kind/feature kind/api kind/docs; do
  gh label create "$l" --repo "$REPO" --color 0e8a16 --force
done
for l in priority/important-soon priority/important-longterm priority/backlog; do
  gh label create "$l" --repo "$REPO" --color fbca04 --force
done
for l in actor/kelos actor/human; do
  gh label create "$l" --repo "$REPO" --color 5319e7 --force
done
for l in triage-accepted needs-actor needs-kind needs-priority needs-triage kelos/needs-input; do
  gh label create "$l" --repo "$REPO" --color c5def5 --force
done
```

- `generated-by-kelos` marks bot-created PRs and issues; `gh pr/issue create --label` fails without it.
- The `kind/*`, `priority/*`, `actor/*`, and lifecycle labels are applied by `agora-triage`.
- `kelos/needs-input` is applied by `agora-squash-commits`.

Unlike this repository, Agora's CI runs on every PR, so there is **no
`ok-to-test` gate** and the spawners do not apply that label.

### 3. Session GitHub Token Secret

Create the personal token Secret used by the Session-only Workspace. Task
credentials remain configured through `agora-agent` and `kelos-agent`:

```bash
kubectl create secret generic personal-github-token \
  --from-literal=GITHUB_TOKEN="$(gh auth token)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

The token needs write access to `kelos-dev/agora` and `repo` plus `workflow`
when using a classic personal access token.

### 4. WebhookGateway, Secret, and Delivery

The webhook TaskSpawners and SessionSpawners route through the `agora`
`WebhookGateway`. Apply it in the same namespace as the spawners. Its
`github-webhook-secret` contains the inbound HMAC secret and outbound API
credentials.

```bash
kubectl apply -f self-development/agora/webhookgateway.yaml
```

Then configure a repository webhook on `kelos-dev/agora`:

- Point it at `https://<your-domain>/webhook/<namespace>/agora`; obtain the
  relative path with `kubectl get webhookgateway agora -o jsonpath='{.status.path}'`
- Use the same shared secret
- Subscribe to `issues`, `issue_comment`, and `pull_request_review`

Webhook spawners only react to **new** events after deployment. Retrigger an
existing issue or PR with a fresh matching event if needed.

### 5. Agent Credentials Secret

All spawners share the `kelos-credentials` Secret and use OAuth. Workers and
PR responders use Codex; other agents use Claude Code. Run `codex login`
locally, then create or update the Secret with both credentials:

```bash
kubectl create secret generic kelos-credentials \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<your-claude-code-oauth-token> \
  --from-file=CODEX_AUTH_JSON="$HOME/.codex/auth.json" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl label secret kelos-credentials kelos.dev/codex-oauth-refresh=true --overwrite
```

The label enables scheduled refresh of `CODEX_AUTH_JSON`; the shared Claude
credential is preserved.

For API-key auth, change each agent's worker credential type to `api-key` and
use `CODEX_API_KEY` for Codex or `ANTHROPIC_API_KEY` for Claude Code.

## Customizing

The `spec.when.githubWebhook` filters and template variables work the same for
TaskSpawner and SessionSpawner resources. See
[`self-development/README.md`](../README.md#customizing-for-your-repository)
for the webhook filter field reference and the full
[template variable table](../README.md), and
[docs/reference.md](../../docs/reference.md) for the authoritative field
references.

## Troubleshooting

**Webhook spawner not creating work:**
- For pick-up, check the SessionSpawner status: `kubectl get sessionspawner <name> -o yaml`
- For other automation, check the TaskSpawner status: `kubectl get taskspawner <name> -o yaml`
- Verify the Workspaces exist: `kubectl get workspace agora-session-agent agora-agent kelos-agent`
- Ensure credentials are configured: `kubectl get secret kelos-credentials`
- Ensure the GitHub webhook server is enabled and the `github-webhook-secret` exists
- Review the `kelos-dev/agora` repository webhook's recent deliveries in GitHub

**Sessions or tasks failing immediately:**
- Verify the agent credentials are valid
- Check the Workspace repository is accessible and the token has push access to it
- Review the corresponding Session or Task status and pod logs

**Triage or PR/issue creation failing on labels:**
- Confirm the labels from [Repository labels](#2-repository-labels) exist on `kelos-dev/agora` — `gh` errors when adding or creating with a label that does not exist

## Next Steps

- Read the [main README](../../README.md) for more details on Tasks and Workspaces
- See [`self-development/`](../) for the equivalent setup that develops this repository
- Monitor task execution: `kelos get tasks` or `kubectl get tasks`
