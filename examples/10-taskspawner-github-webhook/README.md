# GitHub Webhook TaskSpawner Example

This example demonstrates how to configure a TaskSpawner to respond to GitHub webhook events.

## Overview

The GitHub webhook TaskSpawner triggers task creation based on GitHub repository events like:
- Issues being opened, closed, or commented on
- Pull requests being opened, reviewed, or merged
- Code being pushed to specific branches
- And many other GitHub events

## Prerequisites

1. **Webhook Server**: Deploy the kelos-webhook-server with GitHub source configuration
2. **GitHub Webhook**: Configure your GitHub repository to send webhooks to your Kelos instance
3. **Secret**: Create a Kubernetes secret containing the webhook signing secret

## Setup

### 1. Create Webhook Secret

```bash
kubectl create secret generic github-webhook-secret \
  --from-literal=WEBHOOK_SECRET=your-github-webhook-secret
```

### 2. Configure GitHub Repository Webhook

In your GitHub repository settings:
1. Go to Settings → Webhooks → Add webhook
2. Set Payload URL to: `https://your-kelos-instance.com/webhook/github`
3. Set Content type to: `application/json`
4. Set Secret to the same value as in your Kubernetes secret
5. Select events you want to receive (or "Send me everything")

### 3. Deploy TaskSpawner

Apply the TaskSpawner configuration:

```bash
kubectl apply -f taskspawner.yaml
```

## Configuration Details

The example TaskSpawner demonstrates several filtering patterns:

- **Event Types**: Only responds to `issues`, `pull_request`, and `issue_comment` events
- **Action Filtering**: Responds to specific actions like "opened", "created", etc.
- **Label Requirements**: Can require specific labels to be present
- **Author Filtering**: Can filter by the user who triggered the event
- **Draft Filtering**: Can exclude or include draft pull requests

## Template Variables

GitHub webhook events provide rich template variables for task creation:

### Standard Variables
- `{{.Event}}` - GitHub event type (e.g., "issues", "pull_request")
- `{{.Action}}` - Webhook action (e.g., "opened", "created", "submitted")
- `{{.Sender}}` - Username of person who triggered the event
- `{{.ID}}` - Issue/PR number as string
- `{{.Title}}` - Issue/PR title
- `{{.Number}}` - Issue/PR number as integer
- `{{.Body}}` - Issue/PR body text
- `{{.URL}}` - Issue/PR HTML URL
- `{{.Branch}}` - PR source branch or push branch
- `{{.Ref}}` - Git ref for push events

### Raw Payload Access
- `{{.Payload.*}}` - Access any field from the GitHub webhook payload

Example template usage:
```yaml
promptTemplate: |
  A new {{.Event}} event occurred in the repository.

  Event: {{.Event}}
  Action: {{.Action}}
  Triggered by: {{.Sender}}

  {{if .Title}}Title: {{.Title}}{{end}}
  {{if .URL}}URL: {{.URL}}{{end}}

  Please investigate and take appropriate action.

branch: "webhook-{{.Event}}-{{.ID}}"
```

## Webhook Security

The webhook server validates GitHub signatures using HMAC-SHA256:
- GitHub sends signatures in `X-Hub-Signature-256` header with `sha256=` prefix
- The server validates against the secret stored in `WEBHOOK_SECRET` env var
- Invalid signatures result in HTTP 401 responses

## Scaling and Reliability

### Concurrency Control
- Set `maxConcurrency` to limit parallel tasks from webhook events
- When exceeded, returns HTTP 503 with `Retry-After` header
- GitHub will automatically retry failed webhook deliveries

### Idempotency
- Webhook deliveries are tracked by `X-GitHub-Delivery` header
- Duplicate deliveries (e.g., retries) are ignored
- Delivery cache entries expire after 24 hours

### Fault Isolation
- Per-source webhook servers provide fault isolation
- GitHub webhook failures don't affect Linear or other sources
- Each source can be scaled independently

## Troubleshooting

### Common Issues

1. **Tasks not being created**
   - Check webhook server logs for signature validation errors
   - Verify GitHub webhook is configured with correct URL and secret
   - Check TaskSpawner event type and filter configuration

2. **Signature validation failures**
   - Ensure WEBHOOK_SECRET matches GitHub webhook secret exactly
   - Check for trailing newlines or encoding issues in secret

3. **Max concurrency errors**
   - Increase maxConcurrency limit or reduce webhook frequency
   - Check for stuck tasks that aren't completing

### Debugging

Enable verbose logging:
```yaml
env:
  - name: LOG_LEVEL
    value: "debug"
```

Check webhook deliveries in GitHub:
- Repository Settings → Webhooks → Recent Deliveries
- Shows request/response details and retry attempts