# Self-Development Examples

This directory contains real-world TaskSpawner configurations used by the Axon project itself for autonomous development.

## Overview

These TaskSpawners demonstrate how to set up fully autonomous AI agents that:
- Monitor GitHub issues
- Investigate and fix problems
- Create or update pull requests
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
  name: axon-workspace
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
  secretRef:
    name: github-token  # For pushing branches and creating PRs
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

### axon-workers.yaml

This TaskSpawner picks up open GitHub issues labeled with `actor/axon` and creates autonomous agent tasks to fix them.

**Key features:**
- Automatically checks for existing PRs and updates them incrementally
- Self-reviews PRs before requesting human review
- Ensures CI passes before completion
- Labels issues with `axon/needs-input` when human input is needed
- Creates a feedback loop: remove the label to re-queue the issue

**Deploy:**
```bash
# First, ensure all prerequisites are created
kubectl apply -f - <<EOF
apiVersion: axon.io/v1alpha1
kind: Workspace
metadata:
  name: axon-workspace
spec:
  repo: https://github.com/your-org/your-repo.git
  ref: main
  secretRef:
    name: github-token
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

### axon-fake-user.yaml

This TaskSpawner runs hourly to test the developer experience as if you were a new user.

**Deploy:**
```bash
kubectl apply -f self-development/axon-fake-user.yaml
```

This spawner uses a cron schedule and will create a task every hour to:
- Test documentation and onboarding flows
- Check CLI help text and error messages
- Review examples and identify gaps
- Create GitHub issues for any problems found

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
   - Available template variables: `{{.Number}}`, `{{.Title}}`, `{{.Body}}`, `{{.URL}}`

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
