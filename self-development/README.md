# Self-Development Orchestration Patterns

This directory contains real-world orchestration patterns used by the Kelos project itself for autonomous development.

## How It Works

<img width="2694" height="1966" alt="kelos-self-development" src="https://github.com/user-attachments/assets/604bf531-bc69-41be-a716-b9d17b477460" />

All agents share an `AgentConfig` (`agentconfig.yaml`) that defines git identity, comment signatures, and standard constraints.

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
- Labels issues with `kelos/needs-input` when human input is needed
- Creates a feedback loop: remove the label to re-queue the issue
- Supports manual reset via `/reset-worker` comment from a repository admin:
  - Deletes `Task/kelos-workers-<ISSUE-NUMBER>` so it can be recreated with the same name
  - Removes `kelos/needs-input` from the relevant issue and PR

**Deploy:**
```bash
kubectl apply -f self-development/kelos-workers.yaml
```

### kelos-triage.yaml

Picks up open GitHub issues labeled `needs-actor` and performs automated triage.

| | |
|---|---|
| **Trigger** | GitHub Issues with `needs-actor` label |
| **Model** | Opus |
| **Concurrency** | 8 |

**For each issue, the agent:**
1. Classifies with exactly one `kind/*` label (`kind/bug`, `kind/feature`, `kind/api`, `kind/docs`)
2. Checks if the issue has already been fixed by a merged PR or recent commit
3. Checks if the issue references outdated APIs, flags, or features
4. Detects duplicate issues
5. Assesses priority (`priority/important-soon`, `priority/important-longterm`, `priority/backlog`)
6. Recommends an actor — assigns `actor/kelos` if the issue has clear scope and verifiable criteria, otherwise leaves `needs-actor` for human decision

Posts a single triage comment with its findings and adds `kelos/needs-input` for maintainer review before any worker agent picks up the issue.

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
- **Workflow Improvements** — analyze existing configs and suggest better patterns
- **Integration Opportunities** — identify tools/platforms Kelos could integrate with
- **New CRDs & API Extensions** — propose new CRDs or extensions to existing ones

Creates GitHub issues for actionable insights.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-fake-strategist.yaml
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

Creates a PR for code changes or an issue if discussion is needed.

**Deploy:**
```bash
kubectl apply -f self-development/kelos-self-update.yaml
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

4. **Set the polling interval:**
   ```yaml
   spec:
     pollInterval: 5m  # How often to check for new issues
   ```

5. **Choose the right model:**
   ```yaml
   spec:
     taskTemplate:
       model: sonnet  # or opus for more complex tasks
   ```

## Feedback Loop Pattern

The key pattern in these examples is the `excludeLabels: [kelos/needs-input]` configuration. This creates an autonomous feedback loop:

1. Agent picks up an open issue without the `kelos/needs-input` label
2. Agent investigates, creates/updates a PR, and self-reviews
3. If the agent needs human input, it adds the `kelos/needs-input` label
4. The issue is excluded from future polls until a human removes the label
5. Removing the label re-queues the issue on the next poll

This allows agents to work fully autonomously while gracefully handing off to humans when needed.

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
