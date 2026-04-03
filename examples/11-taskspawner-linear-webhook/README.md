# Linear Webhook TaskSpawner Example

This example demonstrates using a TaskSpawner triggered by Linear webhook events.

## Configuration

The TaskSpawner listens for Linear Issue events with specific filtering:

- **Types**: `["Issue"]` — only issue events
- **Actions**: `"create"` and `"update"` — explicit filters per action (omitting `action` would also match `remove`)
- **States**: `["Todo", "In Progress"]` — only issues in these workflow states
- **Labels**: must have `"agent-task"` label
- **Exclude Labels**: excludes issues with `"no-automation"` label

## Webhook Setup

1. **Deploy webhook server** (if not already done):
   ```bash
   # Enable Linear webhook server in your Helm values
   webhookServer:
     sources:
       linear:
         enabled: true
         replicas: 1
         secretName: linear-webhook-secret
         # Optional: provide a Linear API key to enable label enrichment
         # for Comment events (Linear does not include issue labels in
         # Comment webhook payloads by default)
         apiKeySecretName: linear-api-key
   ```

2. **Create webhook secret**:
   ```bash
   kubectl create secret generic linear-webhook-secret \
     --from-literal=WEBHOOK_SECRET=your-linear-webhook-secret
   ```

3. **(Optional) Create Linear API key secret** for Comment label enrichment:
   ```bash
   kubectl create secret generic linear-api-key \
     --from-literal=LINEAR_API_KEY=lin_api_your-linear-api-key
   ```

4. **Configure Linear webhook**:
   - In Linear Settings → API → Webhooks
   - URL: `https://your-webhook-domain/webhook/linear`
   - Secret: Use the same secret as above
   - Events: Select "Issues" and "Comments" as needed

## Template Variables

Linear webhook TaskSpawners have access to these template variables:

- `{{.ID}}` - Linear issue/resource ID
- `{{.Title}}` - Issue title
- `{{.Kind}}` - Always "LinearWebhook"
- `{{.Type}}` - Resource type (e.g., "Issue", "Comment")
- `{{.Action}}` - Webhook action (e.g., "create", "update", "remove")
- `{{.State}}` - Current workflow state name
- `{{.Labels}}` - Comma-separated list of labels
- `{{.IssueID}}` - Parent issue ID (Comment events only)
- `{{.Payload}}` - Full webhook payload for advanced templating

## Usage

```bash
kubectl apply -f taskspawner.yaml
```

When matching Linear issues are created or updated, this TaskSpawner will automatically create Claude Code tasks to process them.
