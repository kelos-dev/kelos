# Integration

Kelos integrates with external systems in two ways:

1. **TaskSpawner** — Kelos natively watches external sources and automatically creates Tasks. No glue code needed.
2. **Direct Task creation** — Create Task resources from your own workflows (GitHub Actions, CI/CD pipelines, scripts, etc.) for full control over when and how agents run.

## TaskSpawner: Native Integration

TaskSpawner watches external sources and creates Tasks automatically for each discovered work item:

```
External Source → TaskSpawner (polls/watches) → Task → Agent runs in Pod
```

One TaskSpawner handles the full lifecycle — discovery, filtering, Task creation, concurrency control, and optional status reporting back to the source.

### GitHub Issues

React to issues in a GitHub repository. The spawner polls the GitHub API and creates a Task for each issue matching your filters.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: fix-bugs
spec:
  when:
    githubIssues:
      labels: [bug]
      excludeLabels: [needs-triage]
      state: open
      pollInterval: 5m
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: |
      Fix the following GitHub issue and open a PR with the fix.

      Issue #{{.Number}}: {{.Title}}

      {{.Body}}
    branch: "fix-{{.Number}}"
    ttlSecondsAfterFinished: 3600
  maxConcurrency: 3
```

**Filtering options:** `labels`, `excludeLabels`, `state`, `assignee`, `author`, `types` (issues, pulls, or both).

**Comment-based control:** Use `commentPolicy` to let users trigger or exclude agents via issue comments. Combine with authorization rules (`allowedUsers`, `allowedTeams`, or `minimumPermission`) to control who can invoke agents:

```yaml
commentPolicy:
  triggerComment: "/kelos run"
  excludeComments: ["/kelos stop"]
  minimumPermission: write   # only repo collaborators can trigger
```

> **Note:** The top-level `triggerComment` and `excludeComments` fields are deprecated. Use `commentPolicy.triggerComment` and `commentPolicy.excludeComments` instead.

**Status reporting:** Set `reporting.enabled: true` to post status updates (started, succeeded, failed) back to the issue as comments.

### GitHub Pull Requests

React to pull requests — review code, respond to feedback, or enforce standards.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: pr-reviewer
spec:
  when:
    githubPullRequests:
      labels: [needs-review]
      state: open
      reviewState: any
      pollInterval: 5m
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: |
      Review pull request #{{.Number}}: {{.Title}}

      {{.Body}}

      Branch: {{.Branch}}
      Review state: {{.ReviewState}}

      {{.ReviewComments}}
    branch: "{{.Branch}}"
  maxConcurrency: 2
```

**PR-specific variables:** `{{.Branch}}` (head branch), `{{.ReviewState}}` (`approved`, `changes_requested`), `{{.ReviewComments}}` (inline review comments).

**Additional filters:** `reviewState`, `author`, `draft`.

### GitHub Webhooks

React to GitHub webhook events in real time — issues, pull requests, pushes, reviews, and more. Unlike the polling-based GitHub Issues and Pull Requests sources, webhooks provide instant response to repository events.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: webhook-responder
spec:
  when:
    githubWebhook:
      events:
        - "issues"
        - "pull_request"
        - "issue_comment"
      excludeAuthors:
        - "dependabot[bot]"
      filters:
        - event: "issues"
          action: "opened"
          labels: ["bug"]
        - event: "issue_comment"
          action: "created"
          bodyContains: "/kelos"
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: |
      A {{.Event}} event ({{.Action}}) was triggered by @{{.Sender}}.

      {{if .Title}}Title: {{.Title}}{{end}}
      {{if .URL}}URL: {{.URL}}{{end}}

      Please investigate and take appropriate action.
    branch: "webhook-{{.Event}}-{{.ID}}"
  maxConcurrency: 3
```

**Setup:** Configure your GitHub repository to send webhooks to your Kelos instance and create a secret with the webhook signing secret. See [example 10](../examples/10-taskspawner-github-webhook/) for full setup instructions.

**Filtering options:** `events` (required), `repository`, `excludeAuthors`, and per-filter fields: `action`, `labels`, `excludeLabels`, `state`, `branch`, `draft`, `author`, `bodyContains`.

**Webhook-specific variables:** `{{.Event}}`, `{{.Action}}`, `{{.Sender}}`, `{{.Ref}}`, `{{.Repository}}`, `{{.Payload}}` (full payload access).

### Jira

React to Jira issues. The spawner polls the Jira API (Cloud or Data Center/Server) using JQL.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: jira-worker
spec:
  when:
    jira:
      baseUrl: https://your-org.atlassian.net
      project: ENG
      jql: "status = Open AND priority in (High, Highest)"
      secretRef:
        name: jira-credentials
      pollInterval: 10m
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: |
      Fix the following Jira issue:

      {{.Title}}

      {{.Body}}
    branch: "jira-{{.ID}}"
  maxConcurrency: 2
```

The Jira secret requires a `JIRA_TOKEN` key. For Jira Cloud, also include `JIRA_USER` (your email):

```bash
# Jira Cloud
kubectl create secret generic jira-credentials \
  --from-literal=JIRA_USER=you@example.com \
  --from-literal=JIRA_TOKEN=<your-api-token>

# Jira Data Center / Server (Bearer token)
kubectl create secret generic jira-credentials \
  --from-literal=JIRA_TOKEN=<your-pat>
```

### Linear Webhooks

React to Linear webhook events in real time — issues, comments, and more. The webhook server receives events from Linear and creates Tasks for matching items.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: linear-responder
spec:
  when:
    linearWebhook:
      types:
        - "Issue"
      filters:
        - action: "create"
          states:
            - "Todo"
            - "In Progress"
          labels:
            - "agent-task"
          excludeLabels:
            - "no-automation"
        - action: "update"
          states:
            - "Todo"
            - "In Progress"
          labels:
            - "agent-task"
          excludeLabels:
            - "no-automation"
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: |
      Linear {{.Type}} {{.Action}}: {{.Title}}

      Linear Issue ID: {{.ID}}
      State: {{.State}}
      Labels: {{.Labels}}

      Please analyze this Linear issue and take appropriate action.
    branch: "linear-task-{{.ID}}"
  maxConcurrency: 3
```

**Setup:** Configure the `kelos-webhook-server` for Linear in your Helm values and create a webhook secret:

```bash
kubectl create secret generic linear-webhook-secret \
  --from-literal=WEBHOOK_SECRET=your-linear-webhook-secret
```

Then configure a webhook in Linear (Settings → API → Webhooks) pointing to `https://your-webhook-domain/webhook/linear` with the same secret. See [example 11](../examples/11-taskspawner-linear-webhook/) for full setup instructions including optional Linear API key configuration for Comment label enrichment.

**Filtering options:** `types` (required — e.g., `"Issue"`, `"Comment"`), and per-filter fields: `action` (`create`, `update`, `remove`), `states`, `labels`, `excludeLabels`.

**Linear-specific variables:** `{{.Type}}` (resource type), `{{.State}}` (workflow state), `{{.Action}}` (webhook action), `{{.IssueID}}` (parent issue ID for Comment events), `{{.Labels}}`, `{{.Payload}}` (full payload access).

### Cron

Run agents on a schedule — dependency updates, code health checks, or periodic maintenance.

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: weekly-deps
spec:
  when:
    cron:
      schedule: "0 9 * * 1"  # Every Monday at 9:00 AM UTC
  taskTemplate:
    type: claude-code
    workspaceRef:
      name: my-workspace
    credentials:
      type: oauth
      secretRef:
        name: claude-oauth-token
    promptTemplate: |
      Check for outdated dependencies and open a PR to update them.
      Triggered at: {{.Time}}
    ttlSecondsAfterFinished: 3600
```

### Template Variables

All `promptTemplate` and `branch` fields support Go `text/template` syntax. Available variables depend on the source:

| Variable | GitHub Issues | GitHub PRs | GitHub Webhook | Jira | Linear Webhook | Cron |
|----------|--------------|------------|----------------|------|----------------|------|
| `{{.ID}}` | Issue number (string) | PR number (string) | Issue/PR number or commit ID | Issue key (e.g., `ENG-42`) | Linear resource ID | Date-time string |
| `{{.Number}}` | Issue number (int) | PR number (int) | Issue/PR number | `0` | Empty | `0` |
| `{{.Title}}` | Issue title | PR title | Issue/PR title | Issue summary | Resource title | Trigger time (RFC3339) |
| `{{.Body}}` | Issue body | PR body | Issue/PR/comment body | Issue description | Empty | Empty |
| `{{.URL}}` | Issue URL | PR URL | Issue/PR URL | Issue URL | Empty | Empty |
| `{{.Labels}}` | Comma-separated | Comma-separated | Empty | Comma-separated | Comma-separated | Empty |
| `{{.Comments}}` | Issue comments | PR comments | Empty | Issue comments | Empty | Empty |
| `{{.Kind}}` | `"Issue"` | `"PR"` | `"webhook"` | Jira issue type | `"LinearWebhook"` | `"Issue"` |
| `{{.Event}}` | Empty | Empty | Event type (e.g., `"issues"`) | Empty | Empty | Empty |
| `{{.Action}}` | Empty | Empty | Action (e.g., `"opened"`) | Empty | Action (e.g., `"create"`, `"update"`) | Empty |
| `{{.Sender}}` | Empty | Empty | Event sender username | Empty | Empty | Empty |
| `{{.Branch}}` | Empty | PR head branch | PR/push branch | Empty | Empty | Empty |
| `{{.Ref}}` | Empty | Empty | Git ref (e.g., `"refs/heads/main"`) | Empty | Empty | Empty |
| `{{.Repository}}` | Empty | Empty | `owner/repo` format | Empty | Empty | Empty |
| `{{.RepositoryOwner}}` | Empty | Empty | Repository owner login | Empty | Empty | Empty |
| `{{.RepositoryName}}` | Empty | Empty | Repository name only | Empty | Empty | Empty |
| `{{.Payload}}` | Empty | Empty | Full webhook payload | Empty | Full Linear webhook payload | Empty |
| `{{.ReviewState}}` | Empty | `approved` / `changes_requested` | Empty | Empty | Empty | Empty |
| `{{.ReviewComments}}` | Empty | Inline review comments | Empty | Empty | Empty | Empty |
| `{{.Type}}` | Empty | Empty | Empty | Empty | Resource type (e.g., `"Issue"`, `"Comment"`) | Empty |
| `{{.State}}` | Empty | Empty | Empty | Empty | Workflow state (e.g., `"Todo"`, `"In Progress"`) | Empty |
| `{{.IssueID}}` | Empty | Empty | Empty | Empty | Parent issue ID (Comment events only) | Empty |
| `{{.Time}}` | Empty | Empty | Empty | Empty | Empty | Trigger time (RFC3339) |

## Direct Task Creation: Workflow Integration

For workflows that TaskSpawner doesn't cover natively, create Task resources directly. Any system that can run `kubectl apply` or call the Kubernetes API can trigger agent runs.

This approach gives you full control over when Tasks are created and lets you integrate kelos into existing CI/CD pipelines, custom automation, or one-off scripts.

### GitHub Actions

Trigger an agent run from a GitHub Actions workflow. This is useful for tasks that should run in response to CI events (push, release, workflow_dispatch) rather than issue or PR activity.

```yaml
# .github/workflows/kelos-task.yaml
name: Run Kelos Task
on:
  workflow_dispatch:
    inputs:
      prompt:
        description: "Task prompt"
        required: true

jobs:
  run-task:
    runs-on: ubuntu-latest
    steps:
      - name: Configure kubeconfig
        run: |
          # Configure access to your Kubernetes cluster
          # (e.g., via cloud provider CLI, kubeconfig secret, etc.)

      - name: Create Task
        run: |
          cat <<EOF | kubectl apply -f -
          apiVersion: kelos.dev/v1alpha1
          kind: Task
          metadata:
            name: gha-task-${{ github.run_id }}
          spec:
            type: claude-code
            prompt: "${{ github.event.inputs.prompt }}"
            credentials:
              type: oauth
              secretRef:
                name: claude-oauth-token
            workspaceRef:
              name: my-workspace
            ttlSecondsAfterFinished: 3600
          EOF

      - name: Wait for Task completion
        run: |
          kubectl wait --for=jsonpath='{.status.phase}'=Succeeded \
            task/gha-task-${{ github.run_id }} --timeout=30m
```

You can also use this pattern to create Tasks on push events, after releases, or as part of a larger CI/CD pipeline.

### Shell Scripts and Automation

Use the `kelos` CLI or `kubectl` from any script:

```bash
# Using the kelos CLI
kelos run \
  -p "Investigate the flaky test in ci_test.go and fix it" \
  --workspace my-workspace \
  --branch fix-flaky-test \
  --timeout 30m \
  -w

# Using kubectl
cat <<EOF | kubectl apply -f -
apiVersion: kelos.dev/v1alpha1
kind: Task
metadata:
  name: fix-flaky-test
spec:
  type: claude-code
  prompt: "Investigate the flaky test in ci_test.go and fix it"
  credentials:
    type: api-key
    secretRef:
      name: anthropic-api-key
  workspaceRef:
    name: my-workspace
  branch: fix-flaky-test
  ttlSecondsAfterFinished: 3600
  podOverrides:
    activeDeadlineSeconds: 1800
EOF
```

### Kubernetes API (Programmatic)

Any Kubernetes client library can create Tasks. Example with Python:

```python
from kubernetes import client, config

config.load_kube_config()
api = client.CustomObjectsApi()

task = {
    "apiVersion": "kelos.dev/v1alpha1",
    "kind": "Task",
    "metadata": {"name": "programmatic-task"},
    "spec": {
        "type": "claude-code",
        "prompt": "Add input validation to the /api/users endpoint",
        "credentials": {
            "type": "api-key",
            "secretRef": {"name": "anthropic-api-key"},
        },
        "workspaceRef": {"name": "my-workspace"},
        "branch": "add-validation",
    },
}

api.create_namespaced_custom_object(
    group="kelos.dev",
    version="v1alpha1",
    namespace="default",
    plural="tasks",
    body=task,
)
```

### Reading Task Results

After a Task completes, its status contains structured outputs:

```bash
# Check task status
kubectl get task fix-flaky-test -o jsonpath='{.status.phase}'

# Get the PR URL
kubectl get task fix-flaky-test -o jsonpath='{.status.results.pr}'

# Get all results
kubectl get task fix-flaky-test -o jsonpath='{.status.results}'
```

Available result keys: `branch`, `commit`, `base-branch`, `pr`, `cost-usd`, `input-tokens`, `output-tokens`.

This makes it straightforward to chain a kelos Task into a larger pipeline — create the Task, wait for completion, then read the results and act on them.

## Choosing an Approach

| | TaskSpawner | Direct Task |
|---|---|---|
| **Best for** | Continuous, event-driven workflows | One-off runs, CI/CD integration, custom triggers |
| **Setup** | Declare once, runs continuously | Create Task per invocation |
| **Concurrency control** | Built-in (`maxConcurrency`, `maxTotalTasks`) | You manage it |
| **Source filtering** | Labels, state, comments, assignees, review state | Your workflow logic decides when to create Tasks |
| **Status reporting** | Can post back to GitHub issues | You read `status.results` and act on them |
| **Examples** | Watch all `bug` issues, respond to PR reviews | Run agent after deploy, trigger from Slack bot |

Both approaches use the same Task resource under the hood — TaskSpawner is an automation layer that creates Tasks for you. Everything a TaskSpawner-created Task can do, a directly created Task can do too.
