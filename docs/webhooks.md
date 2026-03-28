# Webhook Support

Kelos supports webhooks for work item discovery. Instead of polling external APIs, Kelos can receive push notifications when issues or pull requests are created or updated.

Supported webhook sources:
- **GitHub**: Issues and Pull Requests

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

The webhook receiver is included in the Kelos Helm chart but disabled by
default. Enable it during install or upgrade:

```bash
kelos install \
  --set webhook.enabled=true \
  --set webhook.githubWebhookSecretName=github-webhook-secret \
  --set webhook.service.type=LoadBalancer
```

Key values:

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
| `webhook.githubWebhookSecretName` | `""` | Existing Secret with a `secret` key for HMAC validation |
| `webhook.extraEnv` | `[]` | Additional environment variables |

Create the webhook secret before installing:

```bash
kubectl create secret generic github-webhook-secret \
  --namespace kelos-system \
  --from-literal=secret=YOUR_WEBHOOK_SECRET
```

### 2. Get the external URL

If using a LoadBalancer service:

```bash
kubectl get service kelos-webhook-receiver -n kelos-system \
  -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'
```

If using an Ingress, use the configured `webhook.ingress.host` value.

### 3. Configure GitHub webhook

In your GitHub repository settings:
1. Go to Settings → Webhooks → Add webhook
2. **Payload URL**: `https://<your-loadbalancer>/webhook/github`
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
