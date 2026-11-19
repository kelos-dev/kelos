# Kanon-Development Orchestration Patterns

This directory contains the orchestration patterns that drive autonomous
development of [`kelos-dev/kanon`](https://github.com/kelos-dev/kanon) — a Go
CLI that manages coding-agent settings (instructions, skills, MCP servers,
hooks, permissions) across multiple machines.

It mirrors [`self-development/`](../), which does the same for
this repository (`kelos-dev/kelos`). The configs live here, in the kelos
repository, but the webhook filters and Workspaces target `kelos-dev/kanon`, so
the agents they spawn operate on the Kanon repository.

## How It Works

Every spawner references the root [`base-agent`](../base-agent.yaml) for shared
instructions and skills. The issue and PR pick-up SessionSpawners reference
only `base-agent`. The remaining TaskSpawners add repository- or role-specific
instructions where needed: triage and squash-commits share `agentconfig.yaml`
(`kanon-dev-agent`), while planner, the reviewer, fake-user, and fake-strategist
define their own AgentConfig inline.

Autonomous discovery agents that publish GitHub issues maintain at most one
open `generated-by-kelos` issue slot per TaskSpawner. Its title starts with the
TaskSpawner name in brackets, and its body includes both a
`kelos-taskspawner=<name>` marker and one replaceable `Latest verdict` section.
Each run checks whether an unassigned slot is still valid against the current
repository before retaining, replacing, or closing it. Assigned issues and PRs
are treated as ongoing human or agent work and are not updated by autonomous
discovery jobs.

The two SessionSpawners operate on the Kanon repository through the
`kanon-session-agent` Workspace, which uses the personal Session token. Six
TaskSpawners use the `kanon-agent` Workspace. The two meta-maintenance spawners
(`kanon-config-update`, `kanon-self-update`) are different: the files they
maintain (`self-development/kanon/*`) live in *this* repository, so they use the
`kelos-agent` Workspace and the `kelos-dev-agent` role AgentConfig from
`self-development/`, and they read Kanon's activity cross-repo with
`gh ... --repo kelos-dev/kanon`.

## Spawners

| Spawner | Trigger | Agent | Description |
|---|---|---|---|
| **kanon-workers** | Webhook: issue comment `/kelos pick-up` | GPT-6.2 Astra | Creates a durable Session with the open issue URL and a dedicated issue branch |
| **kanon-planner** | Webhook: issue comment `/kelos plan` | Claude Opus | Investigates an issue and posts a structured implementation plan — advisory only, no code changes |
| **kanon-reviewer** | Webhook: PR comment `/kelos review` | Claude Opus | Reviews PRs on demand — analyzes code, checks conventions, and updates a sticky review comment |
| **kanon-pr-responder** | Webhook: PR review/comment with `/kelos pick-up` | GPT-6.2 Astra | Creates a durable Session with the open PR URL on its existing branch |
| **kanon-triage** | Webhook: issue opened/reopened (untriaged) | Claude Opus | Classifies issues by kind/priority, detects duplicates, and recommends an actor |
| **kanon-fake-user** | Cron (daily 09:00 UTC) | Claude Sonnet | Tests DX as a new user and maintains one unassigned issue slot for the highest-impact problem found |
| **kanon-fake-strategist** | Cron (every 12 hours) | Claude Opus | Explores new use cases, integrations, and managed-settings types while maintaining one unassigned strategic issue slot |
| **kanon-config-update** | Cron (daily 18:00 UTC) | Claude Opus | Reviews recent Kanon PR feedback and creates or updates unassigned configuration PRs accordingly |
| **kanon-self-update** | Cron (daily 06:00 UTC) | Claude Opus | Reviews and tunes the `self-development/kanon/` prompts, configs, and README while maintaining one unassigned improvement issue slot |
| **kanon-squash-commits** | Webhook: PR comment `/kelos squash-commits` | Claude Sonnet | Rebases and squashes PR branch commits into a single clean commit |

> **Not ported from `self-development/`:** the Kelos API reviewers (Kanon has no
> Kubernetes CRDs/API surface to review) and `kelos-image-update` (Kanon has no
> coding-agent Dockerfiles to bump).

Apply the shared Workspaces and root `base-agent` first, then the whole
directory. The directory includes `agentconfig.yaml`, which defines the
`kanon-dev-agent` role instructions referenced by the triage and
squash-commits spawners:

```bash
kubectl apply -f self-development/workspaces.yaml
kubectl apply -f self-development/session-workspaces.yaml
kubectl apply -f self-development/base-agent.yaml
kubectl apply -f self-development/kanon/
```

The per-spawner `kubectl apply` commands below are for deploying or updating an
individual spawner after `base-agent` is installed.

### kanon-workers.yaml

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
- Uses `kanon-task-<number>` for the issue branch
- Requires a `/kelos pick-up` comment to pick up an issue (maintainer approval gate)
- Keeps the workspace across Session follow-ups and pod restarts
- Supports routine follow-ups through the Session's web or terminal clients

**Deploy:**
```bash
kubectl apply -f self-development/kanon/kanon-workers.yaml
```

### kanon-planner.yaml

Reacts to `/kelos plan` comments on open issues. Investigates the issue, inspects the codebase, and posts a structured implementation plan — advisory only, no code changes. For issues that touch a CLI command/flag or the `kanon.yaml` schema, the plan must resolve naming, shape, and backward compatibility up front.

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
kubectl apply -f self-development/kanon/kanon-planner.yaml
```

### kanon-reviewer.yaml

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
- Pays special attention to CLI/config-surface changes (naming, shape, backward compatibility)
- Creates or updates a single sticky PR comment with the structured review result
- Summarizes specific file/line findings in the sticky comment without inline review comments
- Read-only agent — does not push code, modify files, or run local validation

**Handoff flow:**
1. `/kelos review` — a maintainer or `kelos-bot[bot]` requests a code review
2. `/kelos review` — a maintainer can retrigger review after changes are pushed

**Deploy:**
```bash
kubectl apply -f self-development/kanon/kanon-reviewer.yaml
```

### kanon-pr-responder.yaml

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
kubectl apply -f self-development/kanon/kanon-pr-responder.yaml
```

### kanon-triage.yaml

Triages newly opened (and certain reopened) GitHub issues.

| | |
|---|---|
| **Trigger** | GitHub issue opened (no `triage-accepted`), or reopened with `needs-actor` |
| **Agent** | Claude Opus via Claude Code |
| **Concurrency** | 8 |

**For each issue, the agent:**
1. Classifies with exactly one `kind/*` label (`kind/bug`, `kind/feature`, `kind/api`, `kind/docs`). `kind/api` covers any change to a user-facing surface — a CLI command or flag, or the `kanon.yaml` configuration schema.
2. Checks if the issue has already been fixed by a merged PR or recent commit
3. Checks if the issue references outdated commands, flags, or config fields
4. Detects duplicate issues
5. Assesses priority (`priority/important-soon`, `priority/important-longterm`, `priority/backlog`)
6. Recommends an actor — assigns `actor/kelos` if the issue has clear scope and verifiable criteria, otherwise `actor/human`. `kind/api` issues always get `actor/human` and are **not** marked `triage-accepted`, because new user-facing surface must be reviewed with a maintainer first.

Posts a single triage comment and adds `triage-accepted` to prevent re-triage.

**Deploy:**
```bash
kubectl apply -f self-development/kanon/kanon-triage.yaml
```

### kanon-fake-user.yaml

Runs daily to test the developer experience as if you were a new user.

| | |
|---|---|
| **Trigger** | Cron `0 9 * * *` (daily at 09:00 UTC) |
| **Agent** | Claude Sonnet via Claude Code |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Documentation & Onboarding** — follow the quick-start, test CLI help text
- **Developer Experience** — build, exercise init/validate/diff/apply/import, review error messages
- **Examples & Use Cases** — verify example config, identify missing examples

Creates or updates the single unassigned `kanon-fake-user` issue slot for the
highest-impact problem found. If that issue is assigned, the run treats it as
ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/kanon/kanon-fake-user.yaml
```

### kanon-fake-strategist.yaml

Runs every 12 hours to strategically explore new ways to use and improve Kanon.

| | |
|---|---|
| **Trigger** | Cron `0 */12 * * *` (every 12 hours) |
| **Agent** | Claude Opus via Claude Code |
| **Concurrency** | 1 |

Each run picks one focus area:
- **New Use Cases** — explore teams/fleets/workflows that could benefit from managed agent settings
- **Integration Opportunities** — identify agents, tools, and toolchains Kanon could integrate with
- **New Managed-Settings Types & CLI Extensions** — propose new render targets, settings types, or `kanon.yaml` schema extensions

Creates or updates the single unassigned `kanon-fake-strategist` issue slot for
the highest-impact actionable insight. If that issue is assigned, the run treats
it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/kanon/kanon-fake-strategist.yaml
```

### kanon-config-update.yaml

Runs daily to update the Kanon agent configuration based on patterns found in Kanon's PR reviews.

| | |
|---|---|
| **Trigger** | Cron `0 18 * * *` (daily at 18:00 UTC) |
| **Agent** | Claude Opus via Claude Code |
| **Workspace** | `kelos-agent` (edits `self-development/kanon/` in this repo) |
| **Concurrency** | 1 |

Reviews recent `kelos-dev/kanon` PRs and their review comments to identify recurring feedback patterns, then updates the configuration under `self-development/kanon/` (the shared Kanon role instructions in `agentconfig.yaml` or a specific TaskSpawner prompt). Opens a PR against this repository using `/kind cleanup` and `release-note: NONE`, since it only touches `self-development/kanon/`.
Skips uncertain or contradictory feedback, and skips an existing configuration
PR when it has assignees.

**Deploy:**
```bash
kubectl apply -f self-development/kanon/kanon-config-update.yaml
```

### kanon-self-update.yaml

Runs daily to review and improve the `self-development/kanon/` workflow files themselves.

| | |
|---|---|
| **Trigger** | Cron `0 6 * * *` (daily at 06:00 UTC) |
| **Agent** | Claude Opus via Claude Code |
| **Workspace** | `kelos-agent` (reasons about `self-development/kanon/` in this repo) |
| **Concurrency** | 1 |

Each run picks one focus area: **Prompt Tuning**, **Configuration Alignment**, or **Workflow Completeness**.

Creates or updates the single unassigned `kanon-self-update` issue slot for the
highest-impact actionable improvement. If that issue is assigned, the run treats
it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/kanon/kanon-self-update.yaml
```

### kanon-squash-commits.yaml

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
kubectl apply -f self-development/kanon/kanon-squash-commits.yaml
```

## Prerequisites

These spawners are applied to the same cluster that runs `self-development/`.
Before deploying them, set up the following.

### 1. Workspaces

Three Workspaces are referenced:

- **`kanon-session-agent`** — points at the Kanon repository and is used only
  by `kanon-workers` and `kanon-pr-responder`. It is defined in
  [`session-workspaces.yaml`](../session-workspaces.yaml) and references the
  `personal-github-token` Secret.

- **`kanon-agent`** — points at the Kanon repository and is used by the six
  TaskSpawners that operate directly on Kanon. It is defined in
  [`workspaces.yaml`](../workspaces.yaml) and references the
  `kelos-agent-credentials` Secret.

- **`kelos-agent`** — points at this repository (`kelos-dev/kelos`). Used by
  `kanon-config-update` and `kanon-self-update`, which edit the
  `self-development/kanon/` files that live here. It is also defined in
  [`workspaces.yaml`](../workspaces.yaml) and references the
  `kelos-agent-credentials` Secret.

### 2. Repository labels

The Kanon repository starts with only the default GitHub labels. Create the
labels these spawners rely on (run once, against `kelos-dev/kanon`):

```bash
REPO=kelos-dev/kanon
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
- The `kind/*`, `priority/*`, `actor/*`, and lifecycle labels are applied by `kanon-triage`.
- `kelos/needs-input` is applied by `kanon-squash-commits`.

Unlike this repository, Kanon's CI runs on every PR, so there is **no
`ok-to-test` gate** and the spawners do not apply that label.

### 3. Session GitHub Token Secret

Create the personal token Secret used by the Session-only Workspace. Task
credentials remain configured through `kanon-agent` and `kelos-agent`:

```bash
kubectl create secret generic personal-github-token \
  --from-literal=GITHUB_TOKEN="$(gh auth token)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

The token needs write access to `kelos-dev/kanon` and `repo` plus `workflow`
when using a classic personal access token.

### 4. WebhookGateway, Secret, and Delivery

The webhook TaskSpawners and SessionSpawners route through the `kanon`
`WebhookGateway` in `webhookgateway.yaml`. Apply it in the same namespace as
the spawners. It reuses `github-webhook-secret`, which must contain the inbound
HMAC secret under `webhook-secret` and outbound API credentials as either a
`GITHUB_TOKEN` or GitHub App keys.

```bash
kubectl apply -f self-development/kanon/webhookgateway.yaml
```

Then configure a repository webhook on `kelos-dev/kanon`:

- Point it at `https://<your-domain>/webhook/<namespace>/kanon`; obtain the
  relative path with `kubectl get webhookgateway kanon -o jsonpath='{.status.path}'`
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
- Verify the Workspaces exist: `kubectl get workspace kanon-session-agent kanon-agent kelos-agent`
- Ensure credentials are configured: `kubectl get secret kelos-credentials`
- Ensure the GitHub webhook server is enabled and the `github-webhook-secret` exists
- Review the `kelos-dev/kanon` repository webhook's recent deliveries in GitHub

**Sessions or tasks failing immediately:**
- Verify the agent credentials are valid
- Check the Workspace repository is accessible and the token has push access to it
- Review the corresponding Session or Task status and pod logs

**Triage or PR/issue creation failing on labels:**
- Confirm the labels from [Repository labels](#2-repository-labels) exist on `kelos-dev/kanon` — `gh` errors when adding or creating with a label that does not exist

## Next Steps

- Read the [main README](../../README.md) for more details on Tasks and Workspaces
- See [`self-development/`](../) for the equivalent setup that develops this repository
- Monitor task execution: `kelos get tasks` or `kubectl get tasks`
