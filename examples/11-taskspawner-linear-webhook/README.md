# Linear Webhook TaskSpawner Example

This example demonstrates how to configure a TaskSpawner to respond to Linear webhook events.

## Overview

The Linear webhook TaskSpawner triggers task creation based on Linear workspace events like:
- Issues being created, updated, or deleted
- Comments being added or modified
- Projects being updated
- Issue state changes and label assignments

## Prerequisites

1. **Webhook Server**: Deploy the kelos-webhook-server with Linear source configuration
2. **Linear Webhook**: Configure your Linear workspace to send webhooks to your Kelos instance
3. **Secret**: Create a Kubernetes secret containing the webhook signing secret

## Setup

### 1. Create Webhook Secret

```bash
kubectl create secret generic linear-webhook-secret \
  --from-literal=WEBHOOK_SECRET=your-linear-webhook-secret
```

### 2. Configure Linear Workspace Webhook

In your Linear workspace settings:
1. Go to Settings → API → Webhooks → Create webhook
2. Set URL to: `https://your-kelos-instance.com/webhook/linear`
3. Set Secret to the same value as in your Kubernetes secret
4. Select the resource types you want to receive (Issues, Comments, etc.)
5. Enable the webhook

### 3. Deploy TaskSpawner

Apply the TaskSpawner configuration:

```bash
kubectl apply -f taskspawner.yaml
```

## Configuration Details

The example TaskSpawner demonstrates several filtering patterns:

- **Resource Types**: Only responds to `Issue`, `Comment`, and `IssueLabel` events
- **Action Filtering**: Responds to specific actions like "create", "update", etc.
- **State Filtering**: Can filter by Linear workflow states
- **Label Requirements**: Can require or exclude specific labels
- **OR Semantics**: Multiple filters for same type use OR logic

## Template Variables

Linear webhook events provide template variables for task creation:

### Standard Variables
- `{{.Type}}` - Linear resource type (e.g., "Issue", "Comment")
- `{{.Action}}` - Webhook action (e.g., "create", "update", "remove")
- `{{.ID}}` - Resource ID
- `{{.Title}}` - Issue title (when available)
- `{{.Payload}}` - Full webhook payload for accessing any field

### Raw Payload Access
- `{{.Payload.data.description}}` - Issue description
- `{{.Payload.data.state.name}}` - Issue state name
- `{{.Payload.data.assignee.name}}` - Assignee name
- `{{.Payload.data.labels}}` - Array of labels
- `{{.Payload.data.team.name}}` - Team name

Example template usage:
```yaml
promptTemplate: |
  A Linear {{.Type}} event occurred in the workspace.

  Type: {{.Type}}
  Action: {{.Action}}
  {{if .Title}}Title: {{.Title}}{{end}}

  {{if eq .Type "Issue"}}
  ## Issue Details
  - **State**: {{.Payload.data.state.name}}
  - **Team**: {{.Payload.data.team.name}}
  {{if .Payload.data.assignee}}- **Assignee**: {{.Payload.data.assignee.name}}{{end}}
  {{if .Payload.data.labels}}- **Labels**: {{range .Payload.data.labels}}{{.name}} {{end}}{{end}}

  ## Description
  {{.Payload.data.description}}
  {{end}}

  Please analyze this issue and provide recommendations.

branch: "linear-{{.Type}}-{{.ID}}"
```

## Webhook Security

The webhook server validates Linear signatures using HMAC-SHA256:
- Linear sends signatures in `Linear-Signature` header as raw hex digest
- The server validates against the secret stored in `WEBHOOK_SECRET` env var
- Invalid signatures result in HTTP 401 responses

## Linear Webhook Format

Linear webhooks follow this general structure:

```json
{
  "type": "Issue",
  "action": "create",
  "data": {
    "id": "issue-id",
    "title": "Issue title",
    "description": "Issue description",
    "state": {
      "id": "state-id",
      "name": "Todo"
    },
    "team": {
      "id": "team-id",
      "name": "Engineering"
    },
    "assignee": {
      "id": "user-id",
      "name": "John Doe"
    },
    "labels": [
      {
        "id": "label-id",
        "name": "bug"
      }
    ]
  }
}
```

## Scaling and Reliability

### Concurrency Control
- Set `maxConcurrency` to limit parallel tasks from webhook events
- When exceeded, returns HTTP 503 with `Retry-After` header
- Linear will automatically retry failed webhook deliveries

### Idempotency
- Webhook deliveries are tracked by `Linear-Delivery` header
- Duplicate deliveries (e.g., retries) are ignored
- Delivery cache entries expire after 24 hours

### Fault Isolation
- Per-source webhook servers provide fault isolation
- Linear webhook failures don't affect GitHub or other sources
- Each source can be scaled independently

## Common Linear Event Types

### Issues
- **create**: New issue created
- **update**: Issue updated (title, description, state, assignee, etc.)
- **remove**: Issue deleted

### Comments
- **create**: New comment added
- **update**: Comment edited
- **remove**: Comment deleted

### IssueLabel
- **create**: Label added to issue
- **remove**: Label removed from issue

## Troubleshooting

### Common Issues

1. **Tasks not being created**
   - Check webhook server logs for signature validation errors
   - Verify Linear webhook is configured with correct URL and secret
   - Check TaskSpawner resource type and filter configuration

2. **Signature validation failures**
   - Ensure WEBHOOK_SECRET matches Linear webhook secret exactly
   - Check for trailing newlines or encoding issues in secret

3. **Filtering not working**
   - Linear webhook payloads have nested structure (`data` object)
   - Verify filter values match exact Linear state/label names
   - Use Linear webhook logs to see actual payload structure

### Debugging

Enable verbose logging:
```yaml
env:
  - name: LOG_LEVEL
    value: "debug"
```

Check webhook deliveries in Linear:
- Settings → API → Webhooks → View webhook → Recent deliveries
- Shows request/response details and retry attempts

### Testing Webhooks

You can test webhook functionality using curl:

```bash
# Test Linear webhook with valid signature
curl -X POST https://your-kelos-instance.com/webhook/linear \
  -H "Content-Type: application/json" \
  -H "Linear-Signature: your-hmac-signature" \
  -H "Linear-Delivery: test-delivery-123" \
  -d '{"type":"Issue","action":"create","data":{"id":"test-123","title":"Test Issue"}}'
```