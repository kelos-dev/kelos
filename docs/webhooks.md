# Webhook Support

Kelos supports webhooks for work item discovery. Instead of polling external APIs, Kelos can receive push notifications when issues or pull requests are created or updated.

Supported webhook sources:
- **GitHub**: Issues and Pull Requests
- **Linear**: Issues

## Architecture

The webhook system consists of three components:

### 1. WebhookEvent CRD

Webhook payloads are stored as `WebhookEvent` custom resources in Kubernetes, providing:

- **Persistence**: Events survive pod restarts (stored in etcd)
- **Auditability**: All events are visible via `kubectl get webhookevents`
- **Processing tracking**: Events are marked as processed after discovery

### 2. Webhook Receiver (kelos-webhook-receiver)

An HTTP server that:
- Listens on `/webhook/github`
- Validates GitHub webhook signatures (HMAC-SHA256)
- Creates `WebhookEvent` CRD instances
- Returns 202 Accepted

Deploy as a Deployment with a LoadBalancer Service to expose it publicly.

### 3. GitHubWebhookSource

The `GitHubWebhookSource` implementation:
- Lists unprocessed `WebhookEvent` resources
- Parses GitHub webhook payloads into `WorkItem` format
- Applies filters (labels, state, etc.)
- Marks events as processed

## GitHub Webhook Setup

### 1. Deploy the webhook receiver

```bash
kubectl apply -f examples/taskspawner-github-webhook.yaml
```

This creates:
- `kelos-webhook-receiver` Deployment
- LoadBalancer Service
- RBAC for creating WebhookEvent resources

### 2. Get the external URL

```bash
kubectl get service kelos-webhook-receiver -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

### 3. Configure GitHub webhook

In your GitHub repository settings:
1. Go to Settings → Webhooks → Add webhook
2. **Payload URL**: `http://<your-loadbalancer>/webhook/github`
3. **Content type**: `application/json`
4. **Secret**: Set a secret and store it in the `github-webhook-secret` Secret
5. **Events**: Select "Issues" and "Pull requests"
6. Click "Add webhook"

### 4. Create a TaskSpawner with githubWebhook

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: my-webhook-spawner
spec:
  when:
    githubWebhook:
      namespace: default
      labels:
        - "kelos-task"
  taskTemplate:
    type: claude-code
    credentials:
      type: api-key
      secretRef:
        name: anthropic-api-key
    workspaceRef:
      name: my-workspace
    promptTemplate: |
      {{ .Title }}
      {{ .Body }}
```

## Webhook Signature Validation

For GitHub webhooks, the receiver validates the `X-Hub-Signature-256` header using HMAC-SHA256.

Set the `GITHUB_WEBHOOK_SECRET` environment variable on the webhook receiver to enable validation:

```yaml
env:
- name: GITHUB_WEBHOOK_SECRET
  valueFrom:
    secretKeyRef:
      name: github-webhook-secret
      key: secret
```

If the secret is not set, signature validation is skipped (development mode only).

---

# Linear Webhook Support

Kelos supports Linear webhooks for work item discovery. Instead of polling the Linear API, Kelos can receive push notifications when issues are created or updated.

## Architecture

The Linear webhook system uses the same architecture as GitHub webhooks:

### 1. WebhookEvent CRD

Webhook payloads are stored as `WebhookEvent` custom resources with `source: linear`.

### 2. Webhook Receiver (kelos-webhook-receiver)

The HTTP server:
- Listens on `/webhook/linear`
- Validates Linear webhook signatures (HMAC-SHA256)
- Creates `WebhookEvent` CRD instances with `source: linear`
- Returns 202 Accepted

### 3. LinearWebhookSource

The `LinearWebhookSource` implementation:
- Lists unprocessed `WebhookEvent` resources with `source: linear`
- Parses Linear webhook payloads (Issue create/update events)
- Applies filters (states, labels, excludeLabels)
- Excludes terminal states (completed, canceled) by default
- Marks events as processed

## Linear Webhook Setup

### 1. Deploy the webhook receiver

The same webhook receiver handles both GitHub and Linear webhooks:

```bash
kubectl apply -f examples/taskspawner-linear-webhook.yaml
```

This creates:
- `kelos-webhook-receiver` Deployment
- LoadBalancer Service
- RBAC for creating WebhookEvent resources

### 2. Get the external URL

```bash
kubectl get service kelos-webhook-receiver -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

### 3. Configure Linear webhook

In your Linear workspace settings:
1. Go to Settings → API → Webhooks
2. Click "Create webhook"
3. **URL**: `http://<your-loadbalancer>/webhook/linear`
4. **Secret**: Set a secret and store it in the `linear-webhook-secret` Secret
5. **Events**: Select "Issue" events (create, update)
6. Click "Create"

### 4. Create a TaskSpawner with linearWebhook

```yaml
apiVersion: kelos.dev/v1alpha1
kind: TaskSpawner
metadata:
  name: my-linear-spawner
spec:
  when:
    linearWebhook:
      namespace: default
      states:
        - "Todo"
        - "In Progress"
      labels:
        - "kelos-task"
  taskTemplate:
    type: claude-code
    credentials:
      type: api-key
      secretRef:
        name: anthropic-api-key
    workspaceRef:
      name: my-workspace
    promptTemplate: |
      {{ .Title }}
      {{ .Body }}
```

## Linear Webhook Configuration

### State Filtering

Control which Linear issue states are processed:

```yaml
when:
  linearWebhook:
    namespace: default
    states:
      - "Todo"
      - "In Progress"
```

**Default behavior** (when `states` is not specified):
- Accepts all non-terminal states
- Excludes `completed` and `canceled` states

### Label Filtering

Require specific labels:

```yaml
when:
  linearWebhook:
    namespace: default
    labels:
      - "bug"
      - "high-priority"
```

Exclude specific labels:

```yaml
when:
  linearWebhook:
    namespace: default
    excludeLabels:
      - "wont-fix"
```

## Webhook Signature Validation

The receiver validates the `X-Linear-Signature` header using HMAC-SHA256.

Set the `LINEAR_WEBHOOK_SECRET` environment variable to enable validation:

```yaml
env:
- name: LINEAR_WEBHOOK_SECRET
  valueFrom:
    secretKeyRef:
      name: linear-webhook-secret
      key: secret
```

If the secret is not set, signature validation is skipped (development mode only).

## Event Types

Linear webhook events include:
- **Issue create**: New issues matching your filters will be discovered
- **Issue update**: State changes and label updates will re-trigger discovery

Only Issue events are processed. Comment and other event types are ignored.
