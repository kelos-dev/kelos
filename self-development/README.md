# Self-Development Orchestration Patterns

This directory contains real-world orchestration patterns used by the Kelos project itself for autonomous development.

The nested [`agora/`](agora/README.md) directory applies the same orchestration
pattern to the sibling [`kelos-dev/agora`](https://github.com/kelos-dev/agora)
repository while keeping the configuration in this repo.

The nested [`kanon/`](kanon/README.md) directory does the same for the sibling
[`kelos-dev/kanon`](https://github.com/kelos-dev/kanon) repository.

## How It Works

<img width="2694" height="1966" alt="kelos-self-development" src="https://github.com/user-attachments/assets/10719599-426e-4c3d-87a0-cde43e1b3113" />

Each TaskSpawner references an `AgentConfig` that defines git identity, comment signatures, and standard constraints. Some agents (triage, pr-responder, squash-commits, config-update) share the base `agentconfig.yaml` (`kelos-dev-agent`), while others (workers, planner, fake-user, fake-strategist, self-update, image-update) define their own `AgentConfig` inline.

Autonomous discovery agents that publish GitHub issues maintain at most one
open `generated-by-kelos` issue slot per TaskSpawner. The issue body includes a
`kelos-taskspawner=<name>` marker so later runs can find it. A run may update
the unassigned slot when it finds a clearly more impactful or important
candidate, but it exits without changes when the slot has assignees. Assigned
issues and PRs are treated as ongoing human or agent work and are not updated by
autonomous discovery jobs. This cap does not apply to follow-up issues created
while a worker or PR responder is handling an explicitly requested issue or PR.

## TaskSpawners

| TaskSpawner | Trigger | Agent | Description |
|---|---|---|---|
| **kelos-workers** | Webhook: issue comment `/kelos pick-up` | Codex | Picks up issues, creates or updates PRs, self-reviews, and ensures CI passes |
| **kelos-planner** | Webhook: issue comment `/kelos plan` | Codex | Investigates an issue and posts a structured implementation plan — advisory only, no code changes |
| **kelos-reviewer** | Webhook: PR comment `/kelos review` | Codex | Reviews PRs on demand — analyzes code, checks conventions, and updates a sticky review comment |
| **kelos-glm-reviewer** | Webhook: PR comment `/kelos glm-review` | GLM-5.2 | Runs a second code review path with Z.AI GLM-5.2 through OpenCode and updates a sticky review comment |
| **kelos-api-reviewer** | Webhook: issue/PR comment `/kelos api-review` | Codex | Reviews Kubernetes API design on issues or PRs — naming, compatibility, CRD validation |
| **kelos-glm-api-reviewer** | Webhook: issue/PR comment `/kelos glm-api-review` | GLM-5.2 | Runs a second Kubernetes API design review path with Z.AI GLM-5.2 through OpenCode and updates sticky PR comments |
| **kelos-pr-responder** | Webhook: PR review/comment on `generated-by-kelos` PRs | Codex | Re-engages on PR review feedback and updates the existing branch incrementally |
| **kelos-triage** | Webhook: issue opened/labeled/reopened (`needs-actor`) | Codex | Classifies issues by kind/priority, detects duplicates, and recommends an actor |
| **kelos-fake-user** | Cron (daily 09:00 UTC) | Codex | Tests DX as a new user and maintains one unassigned issue slot for the highest-impact problem found |
| **kelos-fake-strategist** | Cron (every 12 hours) | Codex | Explores new use cases, integrations, and API ideas while maintaining one unassigned strategic issue slot |
| **kelos-config-update** | Cron (daily 18:00 UTC) | Codex | Reviews recent PR feedback and creates or updates unassigned configuration PRs accordingly |
| **kelos-self-update** | Cron (daily 06:00 UTC) | Codex | Reviews prompts, configs, and workflow files while maintaining one unassigned improvement issue slot |
| **kelos-image-update** | Cron (daily 03:00 UTC) | Codex | Checks for newer agent image versions and creates or updates unassigned PRs for them |
| **kelos-squash-commits** | Webhook: PR comment `/kelos squash-commits` | Codex | Rebases and squashes PR branch commits into a single clean commit |

### kelos-workers.yaml

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
- Hands off PR review feedback to `kelos-pr-responder`
- May create separate follow-up issues for out-of-scope discoveries; those
  follow-ups are exempt from the per-TaskSpawner issue slot cap

**Deploy:**
```bash
kubectl apply -f self-development/kelos-workers.yaml
```

### kelos-planner.yaml

Reacts to `/kelos plan` comments on open issues. Investigates the issue, inspects the codebase, and posts a structured implementation plan — advisory only, no code changes.

| | |
|---|---|
| **Trigger** | GitHub `issue_comment` webhook with `/kelos plan` |
| **Agent** | Codex |
| **Concurrency** | 2 |

**Key features:**
- Reads the issue body, all comments, linked issues/PRs, and relevant source code
- Posts a single planning comment with: plan assessment, implementation steps, acceptance criteria, and open questions/risks
- If the issue already contains a solid plan, normalizes it into a canonical step list instead of inventing a new one
- A later `/kelos plan` comment retriggers planning after more discussion or scope changes

**Handoff flow:**
1. `/kelos plan` — requests or refreshes an implementation plan
2. `/kelos pick-up` — maintainer hands off to workers when ready

**Deploy:**
```bash
kubectl apply -f self-development/kelos-planner.yaml
```

### kelos-reviewer.yaml

Reviews open pull requests on demand when a maintainer posts `/kelos review` or when a Kelos worker posts `/kelos review` after pushing a generated PR and confirming CI passes.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos review` from a maintainer or Kelos worker handoff |
| **Agent** | Codex |
| **Concurrency** | 3 |

**Key features:**
- Reads the full diff and surrounding context to understand changes
- Checks correctness, tests, project conventions, security, and code quality
- Reviews test adequacy without rerunning local validation
- Creates or updates a single sticky PR comment with the structured review result
- Summarizes specific file/line findings in the sticky comment without inline review comments
- Read-only agent — does not push code or modify files

**Handoff flow:**
1. `/kelos review` — maintainer requests a code review on the PR
2. `/kelos review` — worker hands off a generated PR for review after pushing changes and confirming CI passes
3. `/kelos review` — maintainer can retrigger review after changes are pushed

**Deploy:**
```bash
kubectl apply -f self-development/kelos-reviewer.yaml
```

### kelos-glm-reviewer.yaml

Runs a GLM-5.2 review when a maintainer posts `/kelos glm-review`, using
Z.AI GLM-5.2 through the OpenCode runner. It uses a separate trigger from
`kelos-reviewer`, which continues to handle `/kelos review`.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos glm-review` from a maintainer or Kelos worker handoff |
| **Agent** | GLM-5.2 via OpenCode |
| **Concurrency** | 3 |

**Key features:**
- Uses the same code review checklist and structured sticky comment output as `kelos-reviewer`
- Creates or updates a single GLM-specific sticky PR comment with the structured review result
- Provides an independent model-family review without replacing the Codex reviewer
- Read-only agent — does not push code or modify files

**Deploy:**
```bash
kubectl apply -f self-development/kelos-glm-reviewer.yaml
```

### kelos-api-reviewer.yaml

Reviews issues and pull requests for Kubernetes API design conventions, compatibility, and best practices when a maintainer posts `/kelos api-review` or when a Kelos worker posts `/kelos api-review` after pushing generated API changes and confirming CI passes.

| | |
|---|---|
| **Trigger** | GitHub issue/PR comment webhook with `/kelos api-review` from a maintainer or Kelos worker handoff |
| **Agent** | Codex |
| **Concurrency** | 3 |

**Key features:**
- Works on both issues (API design proposals) and pull requests (API implementation review)
- Focused on Kubernetes API design concerns (field naming, primitive types, compatibility, CRD validation, naming/docs, defaulting/conversion)
- References upstream Kubernetes API conventions and API review process documentation
- Checks for correct use of `resource.Quantity`, `metav1.Time`, `metav1.Duration`
- Verifies additive-only changes and forwards compatibility
- For PRs: creates or updates a single sticky PR comment with structured API review feedback
- For issues: posts a structured comment with API design guidance
- Read-only agent — does not push code or modify files

**Handoff flow:**
1. `/kelos api-review` — maintainer requests an API design review on a PR or issue
2. `/kelos api-review` — worker hands off a generated API PR for review after pushing changes and confirming CI passes
3. `/kelos api-review` — maintainer can retrigger review after changes or further discussion

**Deploy:**
```bash
kubectl apply -f self-development/kelos-api-reviewer.yaml
```

### kelos-glm-api-reviewer.yaml

Runs a GLM-5.2 API design review when a maintainer posts
`/kelos glm-api-review`, using Z.AI GLM-5.2 through the OpenCode runner. It
uses a separate trigger from `kelos-api-reviewer`, which continues to handle
`/kelos api-review`.

| | |
|---|---|
| **Trigger** | GitHub issue/PR comment webhook with `/kelos glm-api-review` from a maintainer or Kelos worker handoff |
| **Agent** | GLM-5.2 via OpenCode |
| **Concurrency** | 3 |

**Key features:**
- Uses the same Kubernetes API design checklist and structured output as `kelos-api-reviewer`
- Works on both issues (API design proposals) and pull requests (API implementation review)
- For PRs: creates or updates a single GLM-specific sticky PR comment with structured API review feedback
- For issues: posts a structured comment with API design guidance
- Provides an independent model-family review without replacing the Codex API reviewer
- Read-only agent — does not push code or modify files

**Deploy:**
```bash
kubectl apply -f self-development/kelos-glm-api-reviewer.yaml
```

### kelos-pr-responder.yaml

Picks up open GitHub pull requests labeled `generated-by-kelos` when a reviewer requests changes.

| | |
|---|---|
| **Trigger** | GitHub PR review/comment webhooks on `generated-by-kelos` pull requests |
| **Agent** | Codex |
| **Concurrency** | 8 |

**Key features:**
- Reuses the existing PR branch instead of starting over
- Reads review comments and PR conversation before making incremental changes
- Lets the maintainer stay on the PR page for the common review-feedback loop
- Requires `/kelos pick-up` PR comment or review body to be picked up
- May create separate follow-up issues for out-of-scope discoveries; those
  follow-ups are exempt from the per-TaskSpawner issue slot cap

**Deploy:**
```bash
kubectl apply -f self-development/kelos-pr-responder.yaml
```

### kelos-triage.yaml

Picks up open GitHub issues labeled `needs-actor` and performs automated triage.

| | |
|---|---|
| **Trigger** | GitHub issue opened/labeled/reopened webhooks with `needs-actor` |
| **Agent** | Codex |
| **Concurrency** | 8 |

**For each issue, the agent:**
1. Classifies with exactly one `kind/*` label (`kind/bug`, `kind/feature`, `kind/api`, `kind/docs`). `kind/api` covers any change that introduces or modifies a user-facing API surface — CRD fields, CLI commands or flags, webhooks, etc.
2. Checks if the issue has already been fixed by a merged PR or recent commit
3. Checks if the issue references outdated APIs, flags, or features
4. Detects duplicate issues
5. Assesses priority (`priority/important-soon`, `priority/important-longterm`, `priority/backlog`)
6. Recommends an actor — assigns `actor/kelos` if the issue has clear scope and verifiable criteria, otherwise assigns `actor/human`. `kind/api` issues always get `actor/human` and are **not** marked `triage-accepted`, because new user-facing APIs must be reviewed and discussed with a maintainer before any PR is opened.

Posts a single triage comment with its findings and adds the `kelos/needs-input` label to prevent re-triage.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-triage.yaml
```

### kelos-fake-user.yaml

Runs daily to test the developer experience as if you were a new user.

| | |
|---|---|
| **Trigger** | Cron `0 9 * * *` (daily at 09:00 UTC) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Documentation & Onboarding** — follow getting-started instructions, test CLI help text
- **Developer Experience** — review error messages, test common workflows
- **Examples & Use Cases** — verify manifests, identify missing examples

Creates or updates the single unassigned `kelos-fake-user` issue slot for the
highest-impact problem found. If that issue is assigned, the run treats it as
ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-fake-user.yaml
```

### kelos-fake-strategist.yaml

Runs every 12 hours to strategically explore new ways to use and improve Kelos.

| | |
|---|---|
| **Trigger** | Cron `0 */12 * * *` (every 12 hours) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Each run picks one focus area:
- **New Use Cases** — explore what types of projects/teams could benefit from Kelos
- **Integration Opportunities** — identify tools/platforms Kelos could integrate with
- **New CRDs & API Extensions** — propose new CRDs or extensions to existing ones

Creates or updates the single unassigned `kelos-fake-strategist` issue slot for
the highest-impact actionable insight. If that issue is assigned, the run treats
it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-fake-strategist.yaml
```

### kelos-config-update.yaml

Runs daily to update agent configuration based on patterns found in PR reviews.

| | |
|---|---|
| **Trigger** | Cron `0 18 * * *` (daily at 18:00 UTC) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Reviews recent PRs and their review comments to identify recurring feedback patterns, then updates agent configuration accordingly:
- **Project-level changes** — updates `AGENTS.md` or `self-development/agentconfig.yaml` for conventions that apply to all agents
- **Task-specific changes** — updates TaskSpawner prompts in `self-development/*.yaml` or creates/updates AgentConfig for specific agents

Creates PRs with changes for maintainer review. Skips uncertain or contradictory
feedback, and skips an existing configuration PR when it has assignees.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-config-update.yaml
```

### kelos-self-update.yaml

Runs daily to review and update the self-development workflow files themselves.

| | |
|---|---|
| **Trigger** | Cron `0 6 * * *` (daily at 06:00 UTC) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Prompt Tuning** — review and improve prompts based on actual agent output quality
- **Configuration Alignment** — ensure resource settings, labels, and AgentConfig stay consistent
- **Workflow Completeness** — check that agent prompts reflect current project conventions and Makefile targets
- **Task Template Maintenance** — keep one-off task definitions in sync with their TaskSpawner counterparts

Creates or updates the single unassigned `kelos-self-update` issue slot for the
highest-impact actionable improvement. If that issue is assigned, the run treats
it as ongoing and exits without editing it or creating another issue.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-self-update.yaml
```

### kelos-image-update.yaml

Runs daily to check for newer versions of coding agent images and creates PRs to update them.

| | |
|---|---|
| **Trigger** | Cron `0 3 * * *` (daily at 03:00 UTC) |
| **Agent** | Codex |
| **Concurrency** | 1 |

Checks the following coding agents for updates:
- **claude-code** — `@anthropic-ai/claude-code` npm package
- **codex** — `@openai/codex` npm package
- **gemini** — `@google/gemini-cli` npm package
- **opencode** — `opencode-ai` npm package
- **cursor** — binary download, version discovered from `https://cursor.com/install`

Creates at most one PR per agent. Skips agents that are already up to date or
already have an assigned open update PR.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-image-update.yaml
```

### kelos-squash-commits.yaml

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
- Updates the PR description to match the squashed change, preserving the `Closes #N` reference
- Adds `kelos/needs-input` to the linked issue to signal the PR is ready for re-review
- Does not start new development work or modify source code

**Deploy:**
```bash
kubectl apply -f self-development/kelos-squash-commits.yaml
```

## Prerequisites

Before deploying these examples, you need to create the following resources:

### 1. Workspace Resource

Create a Workspace that points to your repository:

```yaml
apiVersion: kelos.dev/v1alpha2
kind: Workspace
metadata:
  name: kelos-agent
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
  secretRef:
    name: github-token  # For pushing branches and creating PRs
  # Or use GitHub App authentication (recommended for production/org use):
  # secretRef:
  #   name: github-app-creds
  # Create the GitHub App secret with:
  #   kubectl create secret generic github-app-creds \
  #     --from-literal=appID=12345 \
  #     --from-literal=installationID=67890 \
  #     --from-file=privateKey=my-app.private-key.pem
```

### 2. GitHub Token Secret

Create a secret with your GitHub token (needed for `gh` CLI and git authentication):

```bash
kubectl create secret generic github-token \
  --from-literal=GITHUB_TOKEN=<your-github-token>
```

The token needs these permissions:
- `repo` (full control of private repositories)
- `workflow` (if your repo uses GitHub Actions)

### 3. GitHub Webhook Secret and Delivery

The issue and pull request TaskSpawners in this directory are webhook-driven.
Create a secret with the shared webhook secret GitHub will use:

```bash
kubectl create secret generic github-webhook-secret \
  --from-literal=WEBHOOK_SECRET=<your-github-webhook-secret>
```

Then:
- Enable the GitHub webhook server in your Kelos deployment (see `examples/helm-values-webhook.yaml` or `examples/webhook-gateway-values.yaml`)
- Expose `https://<your-domain>/webhook/github` over HTTPS
- Configure a repository webhook that uses the same secret
- Subscribe the repository webhook to `issues`, `issue_comment`, and `pull_request_review`

Webhook TaskSpawners only react to **new** events after deployment. If an issue
or PR was already in a matching state before the webhook server went live,
retrigger it with a fresh comment or relabel after deployment.

### 4. Agent Credentials Secret

Create a secret with your agent credentials. Most checked-in spawners use
Codex OAuth. The GLM reviewer spawners use OpenCode with Z.AI GLM-5.2 and read
the Z.AI key from `OPENCODE_API_KEY` in the same Secret:

```bash
kubectl create secret generic kelos-credentials \
  --from-file=CODEX_AUTH_JSON=$HOME/.codex/auth.json \
  --from-literal=OPENCODE_API_KEY=<your-zai-api-key>
kubectl label secret kelos-credentials kelos.dev/codex-oauth-refresh=true
```

Labeling the OAuth Secret opts it into controller-managed Codex OAuth refresh.
Kelos creates one CronJob per labeled Secret with a non-empty `CODEX_AUTH_JSON`
key, skips unlabeled Secrets and API-key credentials, and preserves other keys
such as `OPENCODE_API_KEY`. The OpenCode entrypoint maps `OPENCODE_API_KEY` to
Z.AI's `ZHIPU_API_KEY` for `zai/*` models.

For API-key auth, change the task template credential type to `api-key` and
create the secret without the OAuth refresh label:

```bash
kubectl create secret generic kelos-credentials \
  --from-literal=CODEX_API_KEY=<your-openai-api-key>
```

## Customizing for Your Repository

To adapt these examples for your own repository:

1. **Update the Workspace reference:**
   - Change `spec.taskTemplate.worker.workspaceRef.name` to match your Workspace resource
   - Or update the Workspace to point to your repository

2. **Update the webhook repository and filters:**
   ```yaml
   spec:
     when:
       githubWebhook:
         repository: your-org/your-repo
         excludeAuthors:
           - your-bot[bot]            # avoid self-trigger loops
         events: [issue_comment]
         filters:
           - event: issue_comment
             action: created
             bodyPattern: '(?m)^/kelos pick-up[ \t]*\r?$'
             commentOn: Issue          # or PullRequest, depending on spawner
             author: your-maintainer   # maintainer-approval gate
             labels: [your-label]
             state: open
   ```

   Webhook filter fields the shipped self-development spawners rely on:

   | Field | Where it lives | Purpose |
   |---|---|---|
   | `excludeAuthors` | `TaskSpawner.spec.when.githubWebhook` (top-level) | Drop events sent by listed usernames before filter evaluation; use this to exclude your own bot account and prevent self-trigger loops. |
   | `bodyPattern` | `TaskSpawner.spec.when.githubWebhook.filters[]` | Go re2 regex match against the comment/review body — the modern replacement for substring-only matching. |
   | `excludeBodyPatterns` | `TaskSpawner.spec.when.githubWebhook.filters[]` | Companion to `bodyPattern`: a list of regexes that, if any match, drop the event. Use to carve out bot-echo replies that would otherwise match `bodyPattern`. |
   | `commentOn` | `TaskSpawner.spec.when.githubWebhook.filters[]` | Scopes `issue_comment` events to `Issue` or `PullRequest`. GitHub fires `issue_comment` for both, so set this to keep issue-only spawners off PRs (and vice versa). |
   | `author` | `TaskSpawner.spec.when.githubWebhook.filters[]` | Restrict matches to a single sender's username — the maintainer-approval gate every shipped spawner uses. |
   | `draft` | `TaskSpawner.spec.when.githubWebhook.filters[]` | Match by PR draft status. Set `false` to skip drafts; omit to match both. |

   See [docs/reference.md](../docs/reference.md#taskspawner) for the full
   `TaskSpawner.spec.when.githubWebhook` field reference.

3. **Customize the prompt:**
   - Edit `spec.taskTemplate.promptTemplate` to match your workflow
   - Available template variables (Go `text/template` syntax):

   | Variable | Description | GitHub Webhook | Cron |
   |----------|-------------|----------------|------|
   | `{{.ID}}` | Unique identifier for the work item | Issue/PR number as string (e.g., `"42"`) | Date-time string (e.g., `"20260207-0900"`) |
   | `{{.Number}}` | Issue or PR number | Issue/PR number (e.g., `42`) | `0` |
   | `{{.Title}}` | Title of the work item | Issue/PR title | Trigger time (RFC3339) |
   | `{{.Body}}` | Body text of the work item | Issue/PR body | Empty |
   | `{{.URL}}` | URL to the source item | GitHub HTML URL | Empty |
   | `{{.Event}}` | GitHub webhook event type | `issue_comment`, `issues`, `pull_request_review`, etc. | Empty |
   | `{{.Action}}` | GitHub webhook action | `created`, `labeled`, `submitted`, etc. | Empty |
   | `{{.Sender}}` | GitHub username that triggered the webhook | GitHub login | Empty |
   | `{{.Branch}}` | Branch name when present in the webhook payload | PR head branch or pushed branch; empty for issue events | Empty |
   | `{{.Kind}}` | Type of work item | `"webhook"` | `"Issue"` |
   | `{{.Time}}` | Trigger time (RFC3339) | Empty | Cron tick time (e.g., `"2026-02-07T09:00:00Z"`) |
   | `{{.Schedule}}` | Cron schedule expression | Empty | Schedule string (e.g., `"0 * * * *"`) |

   The webhook-based self-development agents re-read the latest issue or PR
   state with `gh` before acting, so they do not depend on aggregated
   `{{.Comments}}`, `{{.ReviewComments}}`, or `{{.ReviewState}}` variables.

4. **Remember the trigger is event-driven:**
   - Webhook spawners do not poll or backfill old work items
   - Retrigger an existing issue or PR with a fresh comment or relabel after deployment
   - Duplicate a filter if you need to allow multiple specific GitHub usernames

5. **Adjust the Codex model and effort:**
   ```yaml
   spec:
     taskTemplate:
       worker:
         model: gpt-5.5
         effort: xhigh
   ```

   The checked-in spawners use `gpt-5.5` for the tasks that previously used
   Opus, and `gpt-5.4-mini` for the tasks that previously used Sonnet.
   They set `effort` by role: `xhigh` for complex planning, coding, strategy,
   review, PR update, and configuration update workflows; `high` for triage;
   and `medium` for routine image, fake-user, and squash workflows.

## Feedback Loop Pattern

The key pattern in these examples is webhook-triggered handoff plus runtime re-validation:

1. GitHub delivers an `issue_comment`, `issues`, or `pull_request_review` webhook
2. The matching TaskSpawner creates a Task immediately from that event
3. The agent re-reads the latest issue or PR state with `gh` before acting, so asynchronous label updates are respected
4. If the agent needs human input, it posts a plain-English status comment describing what happened
5. A fresh `/kelos pick-up`, `/kelos plan`, `/kelos review`, `/kelos glm-review`, `/kelos api-review`, `/kelos glm-api-review`, `/kelos squash-commits`, or relabel event retriggers automation later

Each run is a discrete webhook event, so no "pause" comment is needed to prevent re-pickup of stale state. Bot status and review replies should not include trigger commands accidentally, but explicit worker handoff comments can intentionally retrigger reviewer spawners when those spawners include a matching bot-author filter.

## Troubleshooting

**TaskSpawner not creating tasks:**
- Check the TaskSpawner status: `kubectl get taskspawner <name> -o yaml`
- Verify the Workspace exists: `kubectl get workspace`
- Ensure credentials are correctly configured: `kubectl get secret kelos-credentials`
- Ensure the GitHub webhook server is enabled and the `github-webhook-secret` exists
- Check webhook server logs: `kubectl logs -l app.kubernetes.io/component=webhook-github`
- Review the repository webhook's recent deliveries in GitHub
- If the issue or PR matched before you deployed the webhook server, retrigger it with a new comment or relabel

**Tasks failing immediately:**
- Verify the agent credentials are valid
- Check if the Workspace repository is accessible
- Review task logs: `kubectl logs -l job-name=<job-name>`

**Agent not creating PRs:**
- Ensure the `github-token` secret exists and is referenced in the Workspace
- Verify the token has `repo` permissions
- Check if git user is configured in the agent prompt (see `kelos-workers.yaml` for example)

## Next Steps

- Read the [main README](../README.md) for more details on Tasks and Workspaces
- Review the [agent image interface](../docs/agent-image-interface.md) to create custom agents
- Check existing TaskSpawners: `kubectl get taskspawners`
- Monitor task execution: `kelos get tasks` or `kubectl get tasks`
