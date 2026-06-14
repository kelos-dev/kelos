# Kanon-Development Orchestration Patterns

This directory contains the orchestration patterns that drive autonomous
development of [`kelos-dev/kanon`](https://github.com/kelos-dev/kanon) — a Go
CLI that manages coding-agent settings (instructions, skills, MCP servers,
hooks, permissions) across multiple machines.

It mirrors [`self-development/`](../self-development), which does the same for
this repository (`kelos-dev/kelos`). The configs live here, in the kelos
repository, but the webhook filters and Workspaces target `kelos-dev/kanon`, so
the agents they spawn operate on the Kanon repository.

## How It Works

Each TaskSpawner references an `AgentConfig` that defines git identity, comment
signatures, and standard constraints. Some agents (pr-responder, triage,
squash-commits) share the base `agentconfig.yaml` (`kanon-dev-agent`), while
others (workers, planner, reviewer, fake-user, fake-strategist) define their own
`AgentConfig` inline.

Eight spawners operate directly on the Kanon repository through the
`kanon-agent` Workspace. The two meta-maintenance spawners
(`kanon-config-update`, `kanon-self-update`) are different: the files they
maintain (`kanon-development/*`) live in *this* repository, so they use the
`kelos-agent` Workspace and the `kelos-dev-agent` AgentConfig from
`self-development/`, and they read Kanon's activity cross-repo with
`gh ... --repo kelos-dev/kanon`.

## TaskSpawners

| TaskSpawner | Trigger | Agent | Description |
|---|---|---|---|
| **kanon-workers** | Webhook: issue comment `/kelos pick-up` | Codex | Picks up issues, creates or updates PRs, self-reviews, and ensures CI passes |
| **kanon-planner** | Webhook: issue comment `/kelos plan` | Codex | Investigates an issue and posts a structured implementation plan — advisory only, no code changes |
| **kanon-reviewer** | Webhook: PR comment `/kelos review` | Codex | Reviews PRs on demand — analyzes code, checks conventions, and submits structured reviews |
| **kanon-pr-responder** | Webhook: PR review/comment with `/kelos pick-up` | Codex | Re-engages on PR review feedback and updates the existing branch incrementally |
| **kanon-triage** | Webhook: issue opened/reopened (untriaged) | Codex | Classifies issues by kind/priority, detects duplicates, and recommends an actor |
| **kanon-fake-user** | Cron (daily 09:00 UTC) | Codex | Tests DX as a new user — follows docs, tries CLI workflows, files issues for problems found |
| **kanon-fake-strategist** | Cron (every 12 hours) | Codex | Explores new use cases, integrations, and managed-settings types |
| **kanon-config-update** | Cron (daily 18:00 UTC) | Codex | Reviews recent Kanon PR feedback and updates the `kanon-development/` config accordingly |
| **kanon-self-update** | Cron (daily 06:00 UTC) | Codex | Reviews and tunes the `kanon-development/` prompts, configs, and README — the pipeline improves itself |
| **kanon-squash-commits** | Webhook: PR comment `/kelos squash-commits` | Codex | Rebases and squashes PR branch commits into a single clean commit |

> **Not ported from `self-development/`:** `kelos-api-reviewer` (Kanon has no
> Kubernetes CRDs/API surface to review) and `kelos-image-update` (Kanon has no
> coding-agent Dockerfiles to bump).

Apply the whole directory at once — this includes `agentconfig.yaml`, which
defines the shared `kanon-dev-agent` referenced by the pr-responder, triage, and
squash-commits spawners:

```bash
kubectl apply -f kanon-development/
```

The per-spawner `kubectl apply` commands below are for deploying or updating an
individual spawner.

### kanon-workers.yaml

Picks up open GitHub issues when a maintainer posts `/kelos pick-up` and creates autonomous agent tasks to fix them.

| | |
|---|---|
| **Trigger** | GitHub `issue_comment` webhook with `/kelos pick-up` |
| **Agent** | Codex |
| **Concurrency** | 8 |

**Key features:**
- Automatically checks for existing PRs and updates them incrementally
- Self-reviews PRs before requesting human review
- Ensures CI passes before completion
- Requires a `/kelos pick-up` comment to pick up an issue (maintainer approval gate)
- Hands off PR review feedback to `kanon-pr-responder`

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-workers.yaml
```

### kanon-planner.yaml

Reacts to `/kelos plan` comments on open issues. Investigates the issue, inspects the codebase, and posts a structured implementation plan — advisory only, no code changes. For issues that touch a CLI command/flag or the `kanon.yaml` schema, the plan must resolve naming, shape, and backward compatibility up front.

| | |
|---|---|
| **Trigger** | GitHub `issue_comment` webhook with `/kelos plan` |
| **Agent** | Codex |
| **Concurrency** | 2 |

**Handoff flow:**
1. `/kelos plan` — requests or refreshes an implementation plan
2. `/kelos pick-up` — maintainer hands off to workers when ready

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-planner.yaml
```

### kanon-reviewer.yaml

Reviews open pull requests on demand when a maintainer posts `/kelos review`.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos review` |
| **Agent** | Codex |
| **Concurrency** | 3 |

**Key features:**
- Reads the full diff and surrounding context to understand changes
- Checks correctness, tests, project conventions, security, and code quality
- Pays special attention to CLI/config-surface changes (naming, shape, backward compatibility)
- Submits a structured review via `gh pr review` (approve, request changes, or comment)
- Uses inline review comments for specific file/line findings
- Read-only agent — does not push code, modify files, or run local validation

**Handoff flow:**
1. `/kelos review` — requests a code review on the PR
2. `/kelos review` — maintainer can retrigger review after changes are pushed

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-reviewer.yaml
```

### kanon-pr-responder.yaml

Picks up open GitHub pull requests when a reviewer requests changes with `/kelos pick-up`.

| | |
|---|---|
| **Trigger** | GitHub PR comment with `/kelos pick-up`, or a PR review whose body contains `/kelos pick-up` |
| **Agent** | Codex |
| **Concurrency** | 8 |

**Key features:**
- Reuses the existing PR branch instead of starting over
- Reads review comments and PR conversation before making incremental changes
- Lets the maintainer stay on the PR page for the common review-feedback loop

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-pr-responder.yaml
```

### kanon-triage.yaml

Triages newly opened (and certain reopened) GitHub issues.

| | |
|---|---|
| **Trigger** | GitHub issue opened (no `triage-accepted`), or reopened with `needs-actor` |
| **Agent** | Codex |
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
kubectl apply -f kanon-development/kanon-triage.yaml
```

### kanon-fake-user.yaml

Runs daily to test the developer experience as if you were a new user.

| | |
|---|---|
| **Trigger** | Cron `0 9 * * *` (daily at 09:00 UTC) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Documentation & Onboarding** — follow the quick-start, test CLI help text
- **Developer Experience** — build, exercise init/validate/diff/apply/import, review error messages
- **Examples & Use Cases** — verify example config, identify missing examples

Creates GitHub issues for any problems found.

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-fake-user.yaml
```

### kanon-fake-strategist.yaml

Runs every 12 hours to strategically explore new ways to use and improve Kanon.

| | |
|---|---|
| **Trigger** | Cron `0 */12 * * *` (every 12 hours) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Each run picks one focus area:
- **New Use Cases** — explore teams/fleets/workflows that could benefit from managed agent settings
- **Integration Opportunities** — identify agents, tools, and toolchains Kanon could integrate with
- **New Managed-Settings Types & CLI Extensions** — propose new render targets, settings types, or `kanon.yaml` schema extensions

Creates GitHub issues for actionable insights.

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-fake-strategist.yaml
```

### kanon-config-update.yaml

Runs daily to update the Kanon agent configuration based on patterns found in Kanon's PR reviews.

| | |
|---|---|
| **Trigger** | Cron `0 18 * * *` (daily at 18:00 UTC) |
| **Agent** | Codex |
| **Workspace** | `kelos-agent` (edits `kanon-development/` in this repo) |
| **Concurrency** | 1 |

Reviews recent `kelos-dev/kanon` PRs and their review comments to identify recurring feedback patterns, then updates the configuration under `kanon-development/` (the shared `agentconfig.yaml` or a specific TaskSpawner prompt). Opens a PR against this repository using `/kind cleanup` and `release-note: NONE`, since it only touches `kanon-development/`.

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-config-update.yaml
```

### kanon-self-update.yaml

Runs daily to review and improve the `kanon-development/` workflow files themselves.

| | |
|---|---|
| **Trigger** | Cron `0 6 * * *` (daily at 06:00 UTC) |
| **Agent** | Codex |
| **Workspace** | `kelos-agent` (reasons about `kanon-development/` in this repo) |
| **Concurrency** | 1 |

Each run picks one focus area: **Prompt Tuning**, **Configuration Alignment**, or **Workflow Completeness**. Creates GitHub issues for actionable improvements found.

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-self-update.yaml
```

### kanon-squash-commits.yaml

Rebases and squashes PR branch commits into a single clean commit when a maintainer posts `/kelos squash-commits`.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos squash-commits` |
| **Agent** | Codex |
| **Concurrency** | 1 |

**Key features:**
- Rebases the PR branch on `origin/main` and squashes all commits after the merge base into one
- Amends the squashed commit message based on the linked issue and PR description when needed
- Force-pushes with `--force-with-lease`
- Adds `kelos/needs-input` to the linked issue to signal the PR is ready for re-review
- Does not start new development work or modify source code

**Deploy:**
```bash
kubectl apply -f kanon-development/kanon-squash-commits.yaml
```

## Prerequisites

These spawners are applied to the same cluster that runs `self-development/`.
Before deploying them, set up the following.

### 1. Workspaces

Two Workspaces are referenced:

- **`kanon-agent`** — points at the Kanon repository (used by all spawners except the two meta-maintenance ones):

  ```yaml
  apiVersion: kelos.dev/v1alpha2
  kind: Workspace
  metadata:
    name: kanon-agent
  spec:
    repo: https://github.com/kelos-dev/kanon.git
    ref: main
    secretRef:
      name: github-token  # For pushing branches and creating PRs
  ```

- **`kelos-agent`** — points at this repository (`kelos-dev/kelos`). Used by
  `kanon-config-update` and `kanon-self-update`, which edit the
  `kanon-development/` files that live here. This is the same Workspace
  `self-development/` already uses, so if you deployed those examples it
  already exists.

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

### 3. GitHub Token Secret

Create a secret with a GitHub token that has write access to both
`kelos-dev/kanon` and `kelos-dev/kelos` (needed for the `gh` CLI and git
authentication):

```bash
kubectl create secret generic github-token \
  --from-literal=GITHUB_TOKEN=<your-github-token>
```

The token needs `repo` (full control) and `workflow` (if your repo uses GitHub Actions).

### 4. GitHub Webhook Secret and Delivery

The issue and pull request TaskSpawners are webhook-driven. Reuse the
`github-webhook-secret` from your existing deployment, then configure a
repository webhook on `kelos-dev/kanon`:

- Point it at the same `https://<your-domain>/webhook/github` endpoint
- Use the same shared secret
- Subscribe to `issues`, `issue_comment`, and `pull_request_review`

Webhook TaskSpawners only react to **new** events after deployment. Retrigger an
existing issue or PR with a fresh comment or relabel if it was already in a
matching state.

### 5. Agent Credentials Secret

The spawners reuse the `kelos-credentials` secret (the AI agent credentials are
the same regardless of repository). The checked-in spawners use Codex OAuth:

```bash
kubectl create secret generic kelos-credentials \
  --from-file=CODEX_AUTH_JSON=$HOME/.codex/auth.json
```

For API-key auth, change the task template credential type to `api-key` and use
`--from-literal=CODEX_API_KEY=<your-openai-api-key>`.

## Customizing

The `TaskSpawner.spec.when.githubWebhook` filters and template variables work
exactly as in `self-development/`. See
[`self-development/README.md`](../self-development/README.md#customizing-for-your-repository)
for the webhook filter field reference and the full
[template variable table](../self-development/README.md), and
[docs/reference.md](../docs/reference.md#taskspawner) for the authoritative
`TaskSpawner` field reference.

## Troubleshooting

**TaskSpawner not creating tasks:**
- Check the TaskSpawner status: `kubectl get taskspawner <name> -o yaml`
- Verify the Workspaces exist: `kubectl get workspace kanon-agent kelos-agent`
- Ensure credentials are configured: `kubectl get secret kelos-credentials`
- Ensure the GitHub webhook server is enabled and the `github-webhook-secret` exists
- Review the `kelos-dev/kanon` repository webhook's recent deliveries in GitHub

**Tasks failing immediately:**
- Verify the agent credentials are valid
- Check the Workspace repository is accessible and the token has push access to it
- Review task logs: `kubectl logs -l job-name=<job-name>`

**Triage or PR/issue creation failing on labels:**
- Confirm the labels from [Repository labels](#2-repository-labels) exist on `kelos-dev/kanon` — `gh` errors when adding or creating with a label that does not exist

## Next Steps

- Read the [main README](../README.md) for more details on Tasks and Workspaces
- See [`self-development/`](../self-development) for the equivalent setup that develops this repository
- Monitor task execution: `kelos get tasks` or `kubectl get tasks`
