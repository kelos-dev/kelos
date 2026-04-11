# Self-Development Orchestration Patterns

This directory contains real-world orchestration patterns used by the Kelos project itself for autonomous development.

## How It Works

<img width="2694" height="1966" alt="kelos-self-development" src="https://github.com/user-attachments/assets/10719599-426e-4c3d-87a0-cde43e1b3113" />

Each TaskSpawner references an `AgentConfig` that defines git identity, comment signatures, and standard constraints. Some agents (triage, pr-responder, squash-commits, config-update) share the base `agentconfig.yaml` (`kelos-dev-agent`), while others (workers, planner, fake-user, fake-strategist, self-update, image-update) define their own `AgentConfig` inline.

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

### 3. Agent Credentials Secret

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

## TaskSpawners

### kelos-workers.yaml

Picks up open GitHub issues labeled `actor/kelos` and creates autonomous agent tasks to fix them.

| | |
|---|---|
| **Trigger** | GitHub Issues with `actor/kelos` label |
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
| **Trigger** | GitHub Issues with `/kelos plan` comment |
| **Model** | Opus |
| **Concurrency** | 2 |

**Key features:**
- Reads the issue body, all comments, linked issues/PRs, and relevant source code
- Posts a single planning comment with: plan assessment, implementation steps, acceptance criteria, and open questions/risks
- If the issue already contains a solid plan, normalizes it into a canonical step list instead of inventing a new one
- Ends every response with `/kelos needs-input` to pause further automation
- A later `/kelos plan` comment retriggers planning after more discussion or scope changes

**Handoff flow:**
1. `/kelos plan` — requests or refreshes an implementation plan
2. `/kelos needs-input` — pauses further automation after planning
3. `/kelos pick-up` — maintainer hands off to workers when ready

**Deploy:**
```bash
kubectl apply -f self-development/kelos-planner.yaml
```

### kelos-reviewer.yaml

Reviews open pull requests on demand when a maintainer posts `/kelos review`.

| | |
|---|---|
| **Trigger** | GitHub Pull Requests with `/kelos review` comment |
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
2. `/kelos needs-input` — pauses further automation after review is posted
3. `/kelos review` — maintainer can retrigger review after changes are pushed

**Deploy:**
```bash
kubectl apply -f self-development/kelos-reviewer.yaml
```

### kelos-pr-responder.yaml

Picks up open GitHub pull requests labeled `generated-by-kelos` when a reviewer requests changes.

| | |
|---|---|
| **Trigger** | GitHub Pull Requests with `generated-by-kelos` label and `changes requested` review state |
| **Model** | Opus |
| **Concurrency** | 2 |

**Key features:**
- Reuses the existing PR branch instead of starting over
- Reads review comments and PR conversation before making incremental changes
- Lets the maintainer stay on the PR page for the common review-feedback loop
- Requires `/kelos pick-up` PR comment to be picked up
- Uses `/kelos needs-input` PR comments to pause when human input is required

**Deploy:**
```bash
kubectl apply -f self-development/kelos-pr-responder.yaml
```

### kelos-triage.yaml

Picks up open GitHub issues labeled `needs-actor` and performs automated triage.

| | |
|---|---|
| **Trigger** | GitHub Issues with `needs-actor` label |
| **Model** | Opus |
| **Concurrency** | 8 |

**For each issue, the agent:**
1. Classifies with exactly one `kind/*` label (`kind/bug`, `kind/feature`, `kind/api`, `kind/docs`). `kind/api` covers any change that introduces or modifies a user-facing API surface — CRD fields, CLI commands or flags, webhooks, etc.
2. Checks if the issue has already been fixed by a merged PR or recent commit
3. Checks if the issue references outdated APIs, flags, or features
4. Detects duplicate issues
5. Assesses priority (`priority/important-soon`, `priority/important-longterm`, `priority/backlog`)
6. Recommends an actor — assigns `actor/kelos` if the issue has clear scope and verifiable criteria, otherwise assigns `actor/human`. `kind/api` issues always get `actor/human` and are **not** marked `triage-accepted`, because new user-facing APIs must be reviewed and discussed with a maintainer before any PR is opened.

Posts a single triage comment with its findings, adds the `kelos/needs-input` label (to prevent re-triage), and posts a `/kelos needs-input` comment (to prevent workers from picking up the issue before maintainer review).

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

## Customizing for Your Repository

To adapt these examples for your own repository:

1. **Update the Workspace reference:**
   - Change `spec.taskTemplate.workspaceRef.name` to match your Workspace resource
   - Or update the Workspace to point to your repository

2. **Adjust the issue filters:**
   ```yaml
   spec:
     when:
       githubIssues:
         labels: [your-label]        # Issues to pick up
         excludeLabels: [wontfix]    # Issues to skip
         state: open                 # open, closed, or all
   ```

3. **Customize the prompt:**
   - Edit `spec.taskTemplate.promptTemplate` to match your workflow
   - Available template variables (Go `text/template` syntax):

   | Variable | Description | GitHub Issues | Cron |
   |----------|-------------|---------------|------|
   | `{{.ID}}` | Unique identifier for the work item | Issue/PR number as string (e.g., `"42"`) | Date-time string (e.g., `"20260207-0900"`) |
   | `{{.Number}}` | Issue or PR number | Issue/PR number (e.g., `42`) | `0` |
   | `{{.Title}}` | Title of the work item | Issue/PR title | Trigger time (RFC3339) |
   | `{{.Body}}` | Body text of the work item | Issue/PR body | Empty |
   | `{{.URL}}` | URL to the source item | GitHub HTML URL | Empty |
   | `{{.Labels}}` | Comma-separated labels | Issue/PR labels | Empty |
   | `{{.Comments}}` | Concatenated comments | Issue/PR comments | Empty |
   | `{{.Kind}}` | Type of work item | `"Issue"` or `"PR"` | `"Issue"` |
   | `{{.Time}}` | Trigger time (RFC3339) | Empty | Cron tick time (e.g., `"2026-02-07T09:00:00Z"`) |
   | `{{.Schedule}}` | Cron schedule expression | Empty | Schedule string (e.g., `"0 * * * *"`) |

4. **Set the polling interval** (per-source):
   ```yaml
   spec:
     when:
       githubIssues:
         pollInterval: 5m  # How often to check for new issues
   ```

5. **Choose the right model:**
   ```yaml
   spec:
     taskTemplate:
       model: sonnet  # or opus for more complex tasks
   ```

## Feedback Loop Pattern

The key pattern in these examples uses `triggerComment` and `excludeComments` to create an autonomous feedback loop:

1. A maintainer posts a `/kelos pick-up` comment to approve an issue for agent work
2. Agent picks up the issue, investigates, creates/updates a PR, and self-reviews
3. If the agent needs human input, it posts a `/kelos needs-input` comment
4. The maintainer can re-trigger the agent by posting the trigger comment again

The planner uses the same pattern with a different trigger:

1. A maintainer posts `/kelos plan` on an issue to request an implementation plan
2. The planner investigates the issue and codebase, then posts a structured plan
3. The plan comment ends with `/kelos needs-input`, pausing further automation
4. A later `/kelos plan` comment retriggers planning after more discussion or scope changes

This allows agents to work fully autonomously while keeping a maintainer approval gate, without requiring any external GitHub Actions or label management.

## Troubleshooting

**TaskSpawner not creating tasks:**
- Check the TaskSpawner status: `kubectl get taskspawner <name> -o yaml`
- Verify the Workspace exists: `kubectl get workspace`
- Ensure credentials are correctly configured: `kubectl get secret kelos-credentials`
- Check TaskSpawner logs: `kubectl logs deployment/kelos-controller-manager -n kelos-system`

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
