# Self-Development Orchestration Patterns

This directory contains real-world orchestration patterns used by the Axon project itself for autonomous development.

## Overview

These TaskSpawners demonstrate how to orchestrate fully autonomous AI workers that:
- Monitor GitHub issues
- Investigate and fix problems
- Create or update pull requests
- Automatically review pull requests
- Self-review and iterate on feedback
- Request human input when blocked

## Prerequisites

Before deploying these examples, you need to create the following resources:

### 1. Workspace Resource

Create a Workspace that points to your repository:

```yaml
apiVersion: axon.io/v1alpha1
kind: Workspace
metadata:
  name: axon-agent
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
  secretRef:
    name: github-token  # For pushing branches and creating PRs
  # or authenticate with githubApp
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
kubectl create secret generic axon-credentials \
  --from-literal=CLAUDE_CODE_OAUTH_TOKEN=<your-claude-oauth-token>
```

**For API Key:**
```bash
kubectl create secret generic axon-credentials \
  --from-literal=ANTHROPIC_API_KEY=<your-api-key>
```

## Deploying the Examples

## Run one-off fake strategist task

Run it either manually or from GitHub Actions.

Manual:

```bash
kubectl apply -f self-development/tasks/fake-strategist-task.yaml
```

GitHub Actions:

1. Open `Actions` -> `Run Fake Strategist`
2. Click `Run workflow` (optionally set `namespace`)

The target cluster must already have:

- Axon installed
- `Workspace` named `axon-agent`
- Secret `axon-credentials`

### axon-workers.yaml

This TaskSpawner picks up open GitHub issues labeled with `actor/axon` and creates autonomous agent tasks to fix them.

**Key features:**
- Automatically checks for existing PRs and updates them incrementally
- Self-reviews PRs before requesting human review
- Ensures CI passes before completion
- Labels issues with `axon/needs-input` when human input is needed
- Creates a feedback loop: remove the label to re-queue the issue
- Supports manual reset via `/reset-worker` comment from a repository admin:
  - Deletes `Task/axon-workers-<ISSUE-NUMBER>` so it can be recreated with the same name
  - Removes `axon/needs-input` from the relevant issue and PR

**Deploy:**
```bash
# First, ensure all prerequisites are created
kubectl apply -f - <<EOF
apiVersion: axon.io/v1alpha1
kind: Workspace
metadata:
  name: axon-agent
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
  secretRef:
    name: github-token
  # or authenticate with githubApp
EOF

# Then deploy the TaskSpawner
kubectl apply -f self-development/axon-workers.yaml
```

**Monitor:**
```bash
# Watch for new tasks being created
kubectl get tasks -w

# Check TaskSpawner status
kubectl get taskspawner axon-workers -o yaml

# View logs from a specific task
kubectl logs -l job-name=<job-name> -f
```

### axon-pr-reviewer.yaml

This TaskSpawner watches for open pull requests labeled `ok-to-test` and automatically reviews them. It is the first self-development spawner to use `types: ["pulls"]`, demonstrating Axon's PR-driven automation capabilities.

**Key features:**
- Polls for open PRs with the `ok-to-test` label
- Checks out the PR branch and reads the full diff
- Evaluates correctness, testing, code quality, security, and API compatibility
- Runs `make verify` and `make test` as part of the review
- Posts a structured review via the GitHub API (APPROVE, REQUEST_CHANGES, or COMMENT)
- Skips re-reviewing PRs that have no new commits since the last review
- Uses Sonnet for cost efficiency (reviews are read-heavy, not code-generation)
- TTL of 30 minutes allows re-review when PRs are updated

**Deploy:**
```bash
kubectl apply -f self-development/axon-pr-reviewer.yaml
```

**How the review cycle works:**
1. A PR is opened and labeled `ok-to-test`
2. The spawner discovers the PR and creates a review Task
3. The agent checks out the PR, reads the diff, runs checks, and posts a review
4. The Task completes and is cleaned up after 30 minutes (TTL)
5. If the PR is updated (new commits pushed), the spawner creates a new review Task on the next poll

### axon-fake-user.yaml

This TaskSpawner runs daily to test the developer experience as if you were a new user.

**Deploy:**
```bash
kubectl apply -f self-development/axon-fake-user.yaml
```

This spawner uses a daily cron schedule (`0 9 * * *`) and will create a task each day to:
- Test documentation and onboarding flows
- Check CLI help text and error messages
- Review examples and identify gaps
- Create GitHub issues for any problems found

### axon-fake-strategist.yaml

This TaskSpawner runs every 12 hours to strategically explore new ways to use and improve Axon.

**Deploy:**
```bash
kubectl apply -f self-development/axon-fake-strategist.yaml
```

This spawner uses a cron schedule (`0 */12 * * *`) and will create a task every 12 hours to focus on one of:
- **New Use Cases** — explore what types of projects/teams could benefit from Axon, propose example TaskSpawner configs
- **Workflow Improvements** — analyze existing self-development configs and suggest better patterns, prompts, or automation flows
- **Integration Opportunities** — identify tools/platforms Axon could integrate with (CI systems, monitoring, chat ops, etc.)
- **New CRDs & API Extensions** — propose new Custom Resource Definitions or extensions to existing CRDs that would expand Axon's capabilities

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

The key pattern in these examples is the `excludeLabels: [axon/needs-input]` configuration. This creates an autonomous feedback loop:

1. Agent picks up an open issue without the `axon/needs-input` label
2. Agent investigates, creates/updates a PR, and self-reviews
3. If the agent needs human input, it adds the `axon/needs-input` label
4. The issue is excluded from future polls until a human removes the label
5. Removing the label re-queues the issue on the next poll

This allows agents to work fully autonomously while gracefully handing off to humans when needed.

## Troubleshooting

**TaskSpawner not creating tasks:**
- Check the TaskSpawner status: `kubectl get taskspawner <name> -o yaml`
- Verify the Workspace exists: `kubectl get workspace`
- Ensure credentials are correctly configured: `kubectl get secret axon-credentials`
- Check TaskSpawner logs: `kubectl logs deployment/axon-controller-manager -n axon-system`

**Tasks failing immediately:**
- Verify the agent credentials are valid
- Check if the Workspace repository is accessible
- Review task logs: `kubectl logs -l job-name=<job-name>`

**Agent not creating PRs:**
- Ensure the `github-token` secret exists and is referenced in the Workspace
- Verify the token has `repo` permissions
- Check if git user is configured in the agent prompt (see `axon-workers.yaml` for example)

## Next Steps

- Read the [main README](../README.md) for more details on Tasks and Workspaces
- Review the [agent image interface](../docs/agent-image-interface.md) to create custom agents
- Check existing TaskSpawners: `kubectl get taskspawners`
- Monitor task execution: `axon get tasks` or `kubectl get tasks`
