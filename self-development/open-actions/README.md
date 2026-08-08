# Open Actions Development Orchestration Patterns

This directory contains the orchestration patterns that drive autonomous
development of [`kelos-dev/open-actions`](https://github.com/kelos-dev/open-actions)
— a Kubernetes-native, self-hosted control plane that plans, schedules, and
executes a supported subset of GitHub Actions workflows in the team's own
cluster.

It mirrors [`self-development/`](../), which does the same for this repository
(`kelos-dev/kelos`). The configs live here, in the kelos repository, but the
webhook filters and Workspaces target `kelos-dev/open-actions`, so the agents
they spawn operate on the Open Actions repository.

## How It Works

Every spawner references the root [`base-agent`](../base-agent.yaml) for shared
instructions and skills. The issue and PR pick-up SessionSpawners reference
only `base-agent`. The remaining TaskSpawners add repository- or role-specific
instructions where needed: triage and squash-commits share `agentconfig.yaml`
(`open-actions-dev-agent`), while the planner, the two reviewers, fake-user,
and fake-strategist define their own AgentConfig inline.

Autonomous discovery agents that publish GitHub issues maintain at most one
open `generated-by-kelos` issue slot per TaskSpawner. Its title starts with the
TaskSpawner name in brackets, and its body includes both a
`kelos-taskspawner=<name>` marker and one replaceable `Latest verdict` section.
Each run checks whether an unassigned slot is still valid against the current
repository before retaining, replacing, or closing it. Assigned issues and PRs
are treated as ongoing human or agent work and are not updated by autonomous
discovery jobs. This cap does not apply to follow-up issues created while a
worker or PR responder is handling an explicitly requested issue or PR.

The two SessionSpawners operate on the Open Actions repository through the
`open-actions-session-agent` Workspace, which uses the personal Session token.
Seven TaskSpawners use the `open-actions-agent` Workspace. The two
meta-maintenance spawners (`open-actions-config-update`,
`open-actions-self-update`) are different: the files they maintain
(`self-development/open-actions/*`) live in *this* repository, so they use the
`kelos-agent` Workspace and the `kelos-dev-agent` role AgentConfig from
`self-development/`, and they read Open Actions' activity cross-repo with
`gh ... --repo kelos-dev/open-actions`.

## Spawners

| Spawner | Trigger | Agent | Description |
|---|---|---|---|
| **open-actions-workers** | Webhook: issue comment `/kelos pick-up` | Codex | Creates durable Sessions for issue work, including PR creation or updates |
| **open-actions-planner** | Webhook: issue comment `/kelos plan` | Codex | Investigates an issue and posts a structured implementation plan — advisory only, no code changes |
| **open-actions-reviewer** | Webhook: PR comment or review `/kelos review` | Codex | Reviews PRs on demand — analyzes code, checks conventions, and updates a sticky review comment |
| **open-actions-api-reviewer** | Webhook: issue/PR comment or review `/kelos api-review` | Codex | Reviews Kubernetes API design on issues or PRs — naming, compatibility, CRD validation |
| **open-actions-pr-responder** | Webhook: PR review/comment with `/kelos pick-up` | Codex | Creates durable Sessions for PR review feedback on the existing branch |
| **open-actions-triage** | Webhook: issue opened/reopened (untriaged) | Codex | Classifies issues by kind/priority, detects duplicates, and recommends an actor |
| **open-actions-fake-user** | Cron (daily 09:00 UTC) | Codex | Tests DX as a new user and maintains one unassigned issue slot for the highest-impact problem found |
| **open-actions-fake-strategist** | Cron (every 12 hours) | Codex | Explores new use cases, integrations, and API extensions while maintaining one unassigned strategic issue slot |
| **open-actions-config-update** | Cron (daily 18:00 UTC) | Codex | Reviews recent Open Actions PR feedback and creates or updates unassigned configuration PRs accordingly |
| **open-actions-self-update** | Cron (daily 06:00 UTC) | Codex | Reviews and tunes the `self-development/open-actions/` prompts, configs, and README while maintaining one unassigned improvement issue slot |
| **open-actions-squash-commits** | Webhook: PR comment `/kelos squash-commits` | Codex | Rebases and squashes PR branch commits into a single clean commit |

> **Not ported from `self-development/`:** `kelos-image-update` (Open Actions
> builds its own controller and runner images in-repo; there are no
> coding-agent Dockerfiles to bump).

Apply the shared Workspaces and root `base-agent` first, then the whole
directory. The directory includes `agentconfig.yaml`, which defines the
`open-actions-dev-agent` role instructions referenced by the triage and
squash-commits spawners:

```bash
kubectl apply -f self-development/workspaces.yaml
kubectl apply -f self-development/session-workspaces.yaml
kubectl apply -f self-development/base-agent.yaml
kubectl apply -f self-development/open-actions/
```

The per-spawner `kubectl apply` commands below are for deploying or updating an
individual spawner after `base-agent` is installed.

### open-actions-workers.yaml

Picks up open GitHub issues when a maintainer posts `/kelos pick-up` and
creates a durable Session to fix them.

| | |
|---|---|
| **Trigger** | GitHub `issue_comment` webhook with `/kelos pick-up` |
| **Agent** | Codex |
| **Storage** | 10 Gi PVC |

**Key features:**
- Automatically checks for existing PRs and updates them incrementally
- Self-reviews PRs before requesting human review
- Ensures CI passes before completion
- Requires a `/kelos pick-up` comment to pick up an issue (maintainer approval gate)
- Hands off finished PRs to `/kelos review`, or `/kelos api-review` when the diff touches `api/`
- Keeps the workspace across Session follow-ups and pod restarts
- Supports routine follow-ups through the Session's web or terminal clients
- May create separate follow-up issues for out-of-scope discoveries; those
  follow-ups are exempt from autonomous discovery issue slot caps

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-workers.yaml
```

### open-actions-planner.yaml

Reacts to `/kelos plan` comments on open issues. Investigates the issue,
inspects the codebase, and posts a structured implementation plan — advisory
only, no code changes. For issues that touch CRD types or another user-facing
surface (controller flags, the webhook contract, the supported workflow
subset), the plan must resolve naming, shape, and backward compatibility up
front.

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
kubectl apply -f self-development/open-actions/open-actions-planner.yaml
```

### open-actions-reviewer.yaml

Reviews open pull requests on demand when a maintainer posts `/kelos review` or
when an Open Actions worker posts `/kelos review` after pushing a generated PR
and confirming CI passes.

| | |
|---|---|
| **Trigger** | GitHub PR comment or review webhook with `/kelos review` from a maintainer or worker handoff (`kelos-bot[bot]`) |
| **Agent** | Codex |
| **Concurrency** | 3 |

**Key features:**
- Uses the `review-all` skill to reconcile two independent reviews of the same diff
- Reads the full diff and surrounding context to understand changes
- Checks correctness, tests, project conventions, security, and code quality
- Flags obvious CRD permanence risks and defers the deep API checklist to `/kelos api-review`
- Creates or updates a single sticky PR comment with the structured review result
- Read-only agent — does not push code, modify files, or run local validation

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-reviewer.yaml
```

### open-actions-api-reviewer.yaml

Reviews issues and pull requests for Kubernetes API design conventions,
compatibility, and best practices when a maintainer posts `/kelos api-review`
or when an Open Actions worker hands off a generated API PR.

| | |
|---|---|
| **Trigger** | GitHub issue/PR comment or review webhook with `/kelos api-review` from a maintainer or worker handoff (`kelos-bot[bot]`) |
| **Agent** | Codex |
| **Concurrency** | 3 |

**Key features:**
- Uses the `api-review` skill for API design analysis and verdicts
- Works on both issues (API design proposals) and pull requests (API implementation review)
- Treats CRD types, generated schemas, samples, labels, annotations, flags,
  configuration, and webhook contracts as user-facing API surfaces
- For PRs: creates or updates a single sticky PR comment with structured API review feedback
- For issues: posts a structured comment with API design guidance
- Read-only agent — does not push code or modify files

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-api-reviewer.yaml
```

### open-actions-pr-responder.yaml

Picks up open GitHub pull requests when a reviewer requests changes with
`/kelos pick-up`.

| | |
|---|---|
| **Trigger** | GitHub PR comment with `/kelos pick-up`, or a PR review whose body contains `/kelos pick-up` |
| **Agent** | Codex |
| **Storage** | 10 Gi PVC |

**Key features:**
- Reuses the existing PR branch instead of starting over
- Reads review comments and PR conversation before making incremental changes
- Lets the maintainer stay on the PR page for the common review-feedback loop
- Requires a `/kelos pick-up` PR comment or review body to be picked up
- Keeps the workspace across Session follow-ups and pod restarts
- Supports routine follow-ups through the Session's web or terminal clients
- May create separate follow-up issues for out-of-scope discoveries; those
  follow-ups are exempt from autonomous discovery issue slot caps

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-pr-responder.yaml
```

### open-actions-triage.yaml

Triages newly opened (and certain reopened) GitHub issues.

| | |
|---|---|
| **Trigger** | GitHub issue opened (no `triage-accepted`), or reopened with `needs-actor` |
| **Agent** | Codex |
| **Concurrency** | 8 |

**For each issue, the agent:**
1. Classifies with exactly one `kind/*` label (`kind/bug`, `kind/feature`, `kind/api`, `kind/docs`). `kind/api` covers any change to a user-facing surface — CRD fields, generated CRD schemas, controller flags, the webhook contract, or the supported workflow subset.
2. Checks if the issue has already been fixed by a merged PR or recent commit
3. Checks if the issue references outdated CRD fields, flags, or workflow behaviors
4. Detects duplicate issues
5. Assesses priority (`priority/important-soon`, `priority/important-longterm`, `priority/backlog`)
6. Recommends an actor — assigns `actor/kelos` if the issue has clear scope and verifiable criteria, otherwise `actor/human`. `kind/api` issues always get `actor/human` and are **not** marked `triage-accepted`, because user-facing surface changes must be reviewed with a maintainer first.

Posts a single triage comment and adds `triage-accepted` to prevent re-triage.

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-triage.yaml
```

### open-actions-fake-user.yaml

Runs daily to test the developer experience as if you were a new user.

| | |
|---|---|
| **Trigger** | Cron `0 9 * * *` (daily at 09:00 UTC) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Documentation & Onboarding** — follow the installation instructions, check the documented workflow subset
- **Developer Experience** — build and test, review error messages and status conditions, exercise operator workflows
- **Examples & Use Cases** — verify `config/samples/` manifests, identify missing examples

Creates or updates the single unassigned `open-actions-fake-user` issue slot
for the highest-impact problem found. If that issue is assigned, the run treats
it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-fake-user.yaml
```

### open-actions-fake-strategist.yaml

Runs every 12 hours to strategically explore new ways to use and improve Open
Actions.

| | |
|---|---|
| **Trigger** | Cron `0 */12 * * *` (every 12 hours) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Each run picks one focus area:
- **New Use Cases** — explore teams and CI workloads that could benefit from self-hosted, high-volume workflow execution
- **Integration Opportunities** — identify source integrations and Kubernetes-toolchain integrations Open Actions could support
- **New CRDs & API Extensions** — propose extensions to the supported workflow subset, existing CRDs, or new controller capabilities

Creates or updates the single unassigned `open-actions-fake-strategist` issue
slot for the highest-impact actionable insight. If that issue is assigned, the
run treats it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-fake-strategist.yaml
```

### open-actions-config-update.yaml

Runs daily to update the Open Actions agent configuration based on patterns
found in Open Actions' PR reviews.

| | |
|---|---|
| **Trigger** | Cron `0 18 * * *` (daily at 18:00 UTC) |
| **Agent** | Codex |
| **Workspace** | `kelos-agent` (edits `self-development/open-actions/` in this repo) |
| **Concurrency** | 1 |

Reviews recent `kelos-dev/open-actions` PRs and their review comments to
identify recurring feedback patterns, then updates the configuration under
`self-development/open-actions/` (the shared role instructions in
`agentconfig.yaml` or a specific TaskSpawner prompt). Opens a PR against this
repository using `/kind cleanup` and `release-note: NONE`, since it only
touches `self-development/open-actions/`. Skips uncertain or contradictory
feedback, and skips an existing configuration PR when it has assignees.

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-config-update.yaml
```

### open-actions-self-update.yaml

Runs daily to review and improve the `self-development/open-actions/` workflow
files themselves.

| | |
|---|---|
| **Trigger** | Cron `0 6 * * *` (daily at 06:00 UTC) |
| **Agent** | Codex |
| **Workspace** | `kelos-agent` (reasons about `self-development/open-actions/` in this repo) |
| **Concurrency** | 1 |

Each run picks one focus area: **Prompt Tuning**, **Configuration Alignment**, or **Workflow Completeness**.

Creates or updates the single unassigned `open-actions-self-update` issue slot
for the highest-impact actionable improvement. If that issue is assigned, the
run treats it as ongoing and exits without editing it or creating another
issue.

**Deploy:**
```bash
kubectl apply -f self-development/open-actions/open-actions-self-update.yaml
```

### open-actions-squash-commits.yaml

Rebases and squashes PR branch commits into a single clean commit when a
maintainer posts `/kelos squash-commits`.

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
kubectl apply -f self-development/open-actions/open-actions-squash-commits.yaml
```

## Prerequisites

These spawners are applied to the same cluster that runs `self-development/`.
Before deploying them, set up the following.

### 1. Workspaces

Three Workspaces are referenced:

- **`open-actions-session-agent`** — points at the Open Actions repository and
  is used only by `open-actions-workers` and `open-actions-pr-responder`. It is
  defined in [`session-workspaces.yaml`](../session-workspaces.yaml) and
  references the `personal-github-token` Secret.

- **`open-actions-agent`** — points at the Open Actions repository and is used
  by the seven TaskSpawners that operate directly on Open Actions. It is
  defined in [`workspaces.yaml`](../workspaces.yaml) and references the
  `kelos-agent-credentials` Secret, which must contain the Kelos GitHub App
  credentials so reviews and comments are published by `kelos-bot[bot]`.

- **`kelos-agent`** — points at this repository (`kelos-dev/kelos`). Used by
  `open-actions-config-update` and `open-actions-self-update`, which edit the
  `self-development/open-actions/` files that live here. It is also defined in
  [`workspaces.yaml`](../workspaces.yaml) and references the
  `kelos-agent-credentials` Secret.

### 2. Repository labels

The Open Actions repository starts with only the default GitHub labels. Create
the labels these spawners rely on (run once, against
`kelos-dev/open-actions`):

```bash
REPO=kelos-dev/open-actions
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
- The `kind/*`, `priority/*`, `actor/*`, and lifecycle labels are applied by `open-actions-triage`.
- `kelos/needs-input` is applied by `open-actions-squash-commits`.

Unlike this repository, Open Actions' CI runs on every PR, so there is **no
`ok-to-test` gate** and the spawners do not apply that label.

### 3. Session GitHub Token Secret

Create the personal token Secret used by the Session-only Workspace. Task
credentials remain configured through `open-actions-agent` and `kelos-agent`:

```bash
kubectl create secret generic personal-github-token \
  --from-literal=GITHUB_TOKEN="$(gh auth token)" \
  --dry-run=client -o yaml | kubectl apply -f -
```

The token needs write access to `kelos-dev/open-actions` and `repo` plus
`workflow` when using a classic personal access token.

### 4. GitHub Webhook Secret and Delivery

The issue and PR pick-up SessionSpawners and the remaining webhook
TaskSpawners are event-driven. Reuse the `github-webhook-secret` from your
existing deployment, then configure a repository webhook on
`kelos-dev/open-actions`:

- Point it at the same `https://<your-domain>/webhook/github` endpoint
- Use the same shared secret
- Subscribe to `issues`, `issue_comment`, and `pull_request_review`

Webhook spawners only react to **new** events after deployment. Retrigger an
existing issue or PR with a fresh matching event if needed.

### 5. Agent Credentials Secret

The spawners reuse the `kelos-credentials` secret (the AI agent credentials are
the same regardless of repository). The checked-in spawners use Codex OAuth:

```bash
kubectl create secret generic kelos-credentials \
  --from-file=CODEX_AUTH_JSON=$HOME/.codex/auth.json
```

For API-key auth, change the worker credential type to `api-key` and use
`--from-literal=CODEX_API_KEY=<your-openai-api-key>`.

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
- Verify the Workspaces exist: `kubectl get workspace open-actions-session-agent open-actions-agent kelos-agent`
- Ensure credentials are configured: `kubectl get secret kelos-credentials`
- Ensure the GitHub webhook server is enabled and the `github-webhook-secret` exists
- Review the `kelos-dev/open-actions` repository webhook's recent deliveries in GitHub

**Sessions or tasks failing immediately:**
- Verify the agent credentials are valid
- Check the Workspace repository is accessible and the token has push access to it
- Review the corresponding Session or Task status and pod logs

**Triage or PR/issue creation failing on labels:**
- Confirm the labels from [Repository labels](#2-repository-labels) exist on `kelos-dev/open-actions` — `gh` errors when adding or creating with a label that does not exist

## Next Steps

- Read the [main README](../../README.md) for more details on Tasks and Workspaces
- See [`self-development/`](../) for the equivalent setup that develops this repository
- Monitor task execution: `kelos get tasks` or `kubectl get tasks`
