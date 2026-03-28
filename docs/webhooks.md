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

A shared HTTP server that handles all webhook sources:
- `/webhook/github` — validates GitHub signatures (`X-Hub-Signature-256`, HMAC-SHA256)
- `/webhook/linear` — validates Linear signatures (`X-Linear-Signature`, HMAC-SHA256)
- Creates `WebhookEvent` CRD instances with the appropriate `source` field
- Returns 202 Accepted

### 3. Webhook Sources

Source implementations watch `WebhookEvent` resources and convert them to `WorkItem` objects:

- **GitHubWebhookSource**: parses GitHub issue, pull request, and issue comment payloads
- **LinearWebhookSource**: parses Linear issue payloads with state/label filtering

## Deploying the Webhook Receiver

The webhook receiver is included in the Kelos Helm chart but disabled by
default. Enable it during install or upgrade:

```bash
kelos install \
  --set webhook.enabled=true \
  --set webhook.service.type=LoadBalancer
```

### Helm Values

| Value | Default | Description |
|-------|---------|-------------|
| `webhook.enabled` | `false` | Enable the webhook receiver deployment |
| `webhook.image` | `ghcr.io/kelos/kelos-webhook-receiver` | Receiver container image |
| `webhook.replicas` | `1` | Number of receiver replicas |
| `webhook.port` | `8080` | Container port |
| `webhook.service.type` | `ClusterIP` | Service type (`ClusterIP`, `LoadBalancer`, `NodePort`) |
| `webhook.service.port` | `80` | Service port |
| `webhook.ingress.enabled` | `false` | Create an Ingress resource |
| `webhook.ingress.host` | `""` | Ingress hostname |
| `webhook.ingress.ingressClassName` | `""` | Ingress class |
| `webhook.eventsNamespace` | `kelos-system` | Namespace where WebhookEvent CRs are created |
| `webhook.githubWebhookSecretName` | `""` | Existing Secret with a `secret` key for GitHub HMAC validation |
| `webhook.extraEnv` | `[]` | Additional environment variables |

### Getting the External URL

If using a LoadBalancer service:

```bash
kubectl get service kelos-webhook-receiver -n kelos-system \
  -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

If using an Ingress, use the configured `webhook.ingress.host` value.

### Signature Validation

Webhook secrets enable HMAC-SHA256 signature validation for incoming payloads.
If a secret is not configured for a given source, signature validation is
skipped (development mode only).

**GitHub** — set via `webhook.githubWebhookSecretName` Helm value, or create
the secret manually:

```bash
kubectl create secret generic github-webhook-secret \
  --namespace kelos-system \
  --from-literal=secret=YOUR_GITHUB_WEBHOOK_SECRET
```

**Linear** — pass via `webhook.extraEnv`:

```bash
kelos install \
  --set webhook.enabled=true \
  --set webhook.extraEnv[0].name=LINEAR_WEBHOOK_SECRET \
  --set webhook.extraEnv[0].valueFrom.secretKeyRef.name=linear-webhook-secret \
  --set webhook.extraEnv[0].valueFrom.secretKeyRef.key=secret
```

Or create the secret and reference it:

```bash
kubectl create secret generic linear-webhook-secret \
  --namespace kelos-system \
  --from-literal=secret=YOUR_LINEAR_WEBHOOK_SECRET
```

---

## GitHub Webhook Setup

### 1. Configure the webhook receiver for GitHub

Create the webhook secret and enable the receiver:

```bash
kubectl create secret generic github-webhook-secret \
  --namespace kelos-system \
  --from-literal=secret=YOUR_GITHUB_WEBHOOK_SECRET

kelos install \
  --set webhook.enabled=true \
  --set webhook.githubWebhookSecretName=github-webhook-secret \
  --set webhook.service.type=LoadBalancer
```

### 2. Configure GitHub webhook

In your GitHub repository settings:
1. Go to Settings → Webhooks → Add webhook
2. **Payload URL**: `https://<your-external-url>/webhook/github`
3. **Content type**: `application/json`
4. **Secret**: The same secret value you stored in `github-webhook-secret`
5. **Events**: Select "Issues", "Pull requests", and optionally "Issue comments"
6. Click "Add webhook"

### 3. Create a TaskSpawner with githubWebhook

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

See [examples/10-taskspawner-github-webhook](../examples/10-taskspawner-github-webhook/) for a complete example.

---

## Linear Webhook Setup

### 1. Configure the webhook receiver for Linear

Create the webhook secret and enable the receiver:

```bash
kubectl create secret generic linear-webhook-secret \
  --namespace kelos-system \
  --from-literal=secret=YOUR_LINEAR_WEBHOOK_SECRET

kelos install \
  --set webhook.enabled=true \
  --set webhook.service.type=LoadBalancer \
  --set webhook.extraEnv[0].name=LINEAR_WEBHOOK_SECRET \
  --set webhook.extraEnv[0].valueFrom.secretKeyRef.name=linear-webhook-secret \
  --set webhook.extraEnv[0].valueFrom.secretKeyRef.key=secret
```

### 2. Configure Linear webhook

In your Linear workspace settings:
1. Go to Settings → API → Webhooks
2. Click "Create webhook"
3. **URL**: `https://<your-external-url>/webhook/linear`
4. **Secret**: The same secret value you stored in `linear-webhook-secret`
5. **Events**: Select "Issue" events (create, update)
6. Click "Create"

### 3. Create a TaskSpawner with linearWebhook

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

See [examples/11-taskspawner-linear-webhook](../examples/11-taskspawner-linear-webhook/) for a complete example.

### Linear Webhook Configuration

#### State Filtering

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

#### Label Filtering

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

#### Event Types

Linear webhook events include:
- **Issue create**: New issues matching your filters will be discovered
- **Issue update**: State changes and label updates will re-trigger discovery

Only Issue events are processed by default. Set `types` to include other event types (e.g., `Comment`).
