# Self-Development Orchestration Patterns

This directory contains real-world orchestration patterns used by the Kelos project itself for autonomous development.

## How It Works

<img width="2694" height="1966" alt="kelos-self-development" src="https://github.com/user-attachments/assets/10719599-426e-4c3d-87a0-cde43e1b3113" />

Each TaskSpawner references an `AgentConfig` that defines git identity, comment signatures, and standard constraints. Some agents (triage, pr-responder, squash-commits, config-update) share the base `agentconfig.yaml` (`kelos-dev-agent`), while others (workers, planner, fake-user, fake-strategist, self-update, image-update) define their own `AgentConfig` inline.

## TaskSpawners

| TaskSpawner | Trigger | Model | Description |
|---|---|---|---|
| **kelos-workers** | Webhook: issue comment/label (`actor/kelos`) | Opus | Picks up issues, creates or updates PRs, self-reviews, and ensures CI passes |
| **kelos-planner** | Webhook: issue comment `/kelos plan` | Opus | Investigates an issue and posts a structured implementation plan — advisory only, no code changes |
| **kelos-reviewer** | Webhook: PR comment `/kelos review` | Opus | Reviews PRs on demand — analyzes code, checks conventions, and submits structured reviews |
| **kelos-api-reviewer** | Webhook: issue/PR comment `/kelos api-review` | Opus | Reviews Kubernetes API design on issues or PRs — naming, compatibility, CRD validation |
| **kelos-pr-responder** | Webhook: PR review/comment on `generated-by-kelos` PRs | Opus | Re-engages on PR review feedback and updates the existing branch incrementally |
| **kelos-triage** | Webhook: issue opened/labeled/reopened (`needs-actor`) | Opus | Classifies issues by kind/priority, detects duplicates, and recommends an actor |
| **kelos-fake-user** | Cron (daily 09:00 UTC) | Sonnet | Tests DX as a new user — follows docs, tries CLI workflows, files issues for problems found |
| **kelos-fake-strategist** | Cron (every 12 hours) | Opus | Explores new use cases, workflow improvements, and integration opportunities |
| **kelos-config-update** | Cron (daily 18:00 UTC) | Opus | Reviews recent PR feedback and updates agent configuration (conventions, prompts, configs) accordingly |
| **kelos-self-update** | Cron (daily 06:00 UTC) | Opus | Reviews and tunes prompts, configs, and workflow files — the pipeline improves itself |
| **kelos-image-update** | Cron (daily 03:00 UTC) | Sonnet | Checks for newer agent image versions (Claude Code, Codex, Gemini, etc.) and creates PRs to update them |
| **kelos-squash-commits** | Webhook: PR comment `/kelos squash-commits` | Sonnet | Rebases and squashes PR branch commits into a single clean commit |

### kelos-workers.yaml

Picks up open GitHub issues labeled `actor/kelos` and creates autonomous agent tasks to fix them.

| | |
|---|---|
| **Trigger** | GitHub issue comment/label webhooks for issues labeled `actor/kelos` |
| **Model** | Opus |
| **Concurrency** | 3 |

**Key features:**
- Automatically checks for existing PRs and updates them incrementally
- Self-reviews PRs before requesting human review
- Ensures CI passes before completion
- Requires a `/kelos pick-up` comment to pick up an issue (maintainer approval gate)
- Hands off PR review feedback to `kelos-pr-responder`

**Deploy:**
```bash
kubectl apply -f self-development/kelos-workers.yaml
```

### kelos-planner.yaml

Reacts to `/kelos plan` comments on open issues. Investigates the issue, inspects the codebase, and posts a structured implementation plan — advisory only, no code changes.

| | |
|---|---|
| **Trigger** | GitHub `issue_comment` webhook with `/kelos plan` |
| **Model** | Opus |
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

Reviews open pull requests on demand when a maintainer posts `/kelos review`.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos review` |
| **Model** | Opus |
| **Concurrency** | 3 |

**Key features:**
- Reads the full diff and surrounding context to understand changes
- Checks correctness, tests, project conventions, security, and code quality
- Runs `make test` to verify tests pass
- Submits a structured review via `gh pr review` (approve, request changes, or comment)
- Uses inline review comments for specific file/line findings
- Read-only agent — does not push code or modify files

**Handoff flow:**
1. `/kelos review` — requests a code review on the PR
2. `/kelos review` — maintainer can retrigger review after changes are pushed

**Deploy:**
```bash
kubectl apply -f self-development/kelos-reviewer.yaml
```

### kelos-api-reviewer.yaml

Reviews issues and pull requests for Kubernetes API design conventions, compatibility, and best practices when a maintainer posts `/kelos api-review`.

| | |
|---|---|
| **Trigger** | GitHub issue/PR comment webhook with `/kelos api-review` |
| **Model** | Opus |
| **Concurrency** | 3 |

**Key features:**
- Works on both issues (API design proposals) and pull requests (API implementation review)
- Focused on Kubernetes API design concerns (field naming, primitive types, compatibility, CRD validation, naming/docs, defaulting/conversion)
- References upstream Kubernetes API conventions and API review process documentation
- Checks for correct use of `resource.Quantity`, `metav1.Time`, `metav1.Duration`
- Verifies additive-only changes and forwards compatibility
- For PRs: submits a structured review via `gh pr review` (approve, request changes, or comment)
- For issues: posts a structured comment with API design guidance
- Read-only agent — does not push code or modify files

**Handoff flow:**
1. `/kelos api-review` — requests an API design review on a PR or issue
2. `/kelos api-review` — maintainer can retrigger review after changes or further discussion

**Deploy:**
```bash
kubectl apply -f self-development/kelos-api-reviewer.yaml
```

### kelos-pr-responder.yaml

Picks up open GitHub pull requests labeled `generated-by-kelos` when a reviewer requests changes.

| | |
|---|---|
| **Trigger** | GitHub PR review/comment webhooks on `generated-by-kelos` pull requests |
| **Model** | Opus |
| **Concurrency** | 2 |

**Key features:**
- Reuses the existing PR branch instead of starting over
- Reads review comments and PR conversation before making incremental changes
- Lets the maintainer stay on the PR page for the common review-feedback loop
- Requires `/kelos pick-up` PR comment or review body to be picked up

**Deploy:**
```bash
kubectl apply -f self-development/kelos-pr-responder.yaml
```

### kelos-triage.yaml

Picks up open GitHub issues labeled `needs-actor` and performs automated triage.

| | |
|---|---|
| **Trigger** | GitHub issue opened/labeled/reopened webhooks with `needs-actor` |
| **Model** | Opus |
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
| **Model** | Sonnet |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Documentation & Onboarding** — follow getting-started instructions, test CLI help text
- **Developer Experience** — review error messages, test common workflows
- **Examples & Use Cases** — verify manifests, identify missing examples

Creates GitHub issues for any problems found.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-fake-user.yaml
```

### kelos-fake-strategist.yaml

Runs every 12 hours to strategically explore new ways to use and improve Kelos.

| | |
|---|---|
| **Trigger** | Cron `0 */12 * * *` (every 12 hours) |
| **Model** | Opus |
| **Concurrency** | 1 |

Each run picks one focus area:
- **New Use Cases** — explore what types of projects/teams could benefit from Kelos
- **Integration Opportunities** — identify tools/platforms Kelos could integrate with
- **New CRDs & API Extensions** — propose new CRDs or extensions to existing ones

Creates GitHub issues for actionable insights.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-fake-strategist.yaml
```

### kelos-config-update.yaml

Runs daily to update agent configuration based on patterns found in PR reviews.

| | |
|---|---|
| **Trigger** | Cron `0 18 * * *` (daily at 18:00 UTC) |
| **Model** | Opus |
| **Concurrency** | 1 |

Reviews recent PRs and their review comments to identify recurring feedback patterns, then updates agent configuration accordingly:
- **Project-level changes** — updates `AGENTS.md`, `CLAUDE.md`, or `self-development/agentconfig.yaml` for conventions that apply to all agents
- **Task-specific changes** — updates TaskSpawner prompts in `self-development/*.yaml` or creates/updates AgentConfig for specific agents

Creates PRs with changes for maintainer review. Skips uncertain or contradictory feedback.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-config-update.yaml
```

### kelos-self-update.yaml

Runs daily to review and update the self-development workflow files themselves.

| | |
|---|---|
| **Trigger** | Cron `0 6 * * *` (daily at 06:00 UTC) |
| **Model** | Opus |
| **Concurrency** | 1 |

Each run picks one focus area:
- **Prompt Tuning** — review and improve prompts based on actual agent output quality
- **Configuration Alignment** — ensure resource settings, labels, and AgentConfig stay consistent
- **Workflow Completeness** — check that agent prompts reflect current project conventions and Makefile targets
- **Task Template Maintenance** — keep one-off task definitions in sync with their TaskSpawner counterparts

Creates GitHub issues for actionable improvements found.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-self-update.yaml
```

### kelos-image-update.yaml

Runs daily to check for newer versions of coding agent images and creates PRs to update them.

| | |
|---|---|
| **Trigger** | Cron `0 3 * * *` (daily at 03:00 UTC) |
| **Model** | Sonnet |
| **Concurrency** | 1 |

Checks the following coding agents for updates:
- **claude-code** — `@anthropic-ai/claude-code` npm package
- **codex** — `@openai/codex` npm package
- **gemini** — `@google/gemini-cli` npm package
- **opencode** — `opencode-ai` npm package
- **cursor** — binary download, version discovered from `https://cursor.com/install`

Creates at most one PR per agent. Skips agents that are already up to date or already have an open update PR.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-image-update.yaml
```

### kelos-squash-commits.yaml

Rebases and squashes PR branch commits into a single clean commit when a maintainer posts `/kelos squash-commits`.

| | |
|---|---|
| **Trigger** | GitHub PR comment webhook with `/kelos squash-commits` |
| **Model** | Sonnet |
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
apiVersion: kelos.dev/v1alpha1
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

Create a secret with your AI agent credentials:

**For OAuth (Claude Code):**
```bash
kubectl create secret generic kelos-credentials \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<your-claude-oauth-token>
```

**For API Key:**
```bash
kubectl create secret generic kelos-credentials \
  --from-literal=ANTHROPIC_API_KEY=<your-api-key>
```

## Customizing for Your Repository

To adapt these examples for your own repository:

1. **Update the Workspace reference:**
   - Change `spec.taskTemplate.workspaceRef.name` to match your Workspace resource
   - Or update the Workspace to point to your repository

2. **Update the webhook repository and filters:**
   ```yaml
   spec:
     when:
       githubWebhook:
         repository: your-org/your-repo
         events: [issue_comment]
         filters:
           - event: issue_comment
             action: created
             bodyContains: /kelos pick-up
             labels: [your-label]
             state: open
   ```

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
   | `{{.Branch}}` | Branch name when present in the webhook payload | Usually PR head branch or push branch | Empty |
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

5. **Choose the right model:**
   ```yaml
   spec:
     taskTemplate:
       model: sonnet  # or opus for more complex tasks
   ```

## Feedback Loop Pattern

The key pattern in these examples is webhook-triggered handoff plus runtime re-validation:

1. GitHub delivers an `issue_comment`, `issues`, or `pull_request_review` webhook
2. The matching TaskSpawner creates a Task immediately from that event
3. The agent re-reads the latest issue or PR state with `gh` before acting, so asynchronous label updates are respected
4. If the agent needs human input, it posts a plain-English status comment describing what happened
5. A fresh `/kelos pick-up`, `/kelos plan`, `/kelos review`, `/kelos api-review`, `/kelos squash-commits`, or relabel event retriggers automation later

Each run is a discrete webhook event, so no "pause" comment is needed to prevent re-pickup of stale state — the bot's own replies don't match the trigger substrings and cannot retrigger the spawner.

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
