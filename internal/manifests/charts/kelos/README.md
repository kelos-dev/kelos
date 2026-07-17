# Kelos Helm Chart

The Kelos Helm chart is published as an OCI artifact in GHCR:

```bash
oci://ghcr.io/kelos-dev/charts/kelos
```

## Requirements

[cert-manager](https://cert-manager.io/) must be installed in the cluster before
installing Kelos. Kelos serves a CRD conversion webhook, and its serving
certificate is issued by cert-manager. Installing the Kelos CRDs without
cert-manager present would leave them pointing at a webhook the API server can
never reach.
Follow the [cert-manager installation documentation](https://cert-manager.io/docs/installation/)
for the current recommended installation method.

`kelos install` checks for cert-manager and fails fast with this guidance if it
is missing.

## First-Time Install

For most first-time installs, use `kelos install`; it stages the controller,
certificate, conversion webhook, and CRDs automatically.

If you want Helm to own CRDs on a fresh cluster, render the CRDs during the
initial Helm install so the controller can start its Kelos resource watches:

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version> \
  --set crds.install=true
```

Do not use a controller-only first pass on a fresh cluster; with no Kelos CRDs
installed, the controller cannot become ready. The chart default is still
controller-only for clusters where CRDs are managed by `kelos install` or
another manifest workflow:

```yaml
crds:
  install: false
  keep: true
```

For existing installations, CRDs use a conversion webhook, so applying upgraded
conversion-enabled CRDs before the controller Service, certificate, and ready
webhook exist can make conversion requests fail. `kelos install` handles that
staging automatically. Helm upgrade and adoption workflows use the two-phase
controller-first flow below because the CRDs already exist.

When CRDs are rendered, `crds.keep=true` preserves them during chart uninstall.
This is the safe default because Tasks and TaskSpawners can have finalizers that
require the controller to run before custom resources can be deleted. If you
want Helm uninstall to remove CRDs too, delete all Kelos custom resources while
the controller is still running, upgrade the release with `crds.install=true`
and `crds.keep=false`, and then uninstall.

## Migrating Existing CRDs Into Helm Ownership

If your cluster already has Kelos CRDs from `kelos install` or `kubectl apply`, your first Helm install or upgrade must choose one CRD owner.

### Option 1: Keep CRDs Managed Outside Helm

Use this if you want to continue managing CRDs with `kelos install` or another manifest-based workflow:

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version>
```

With `crds.install=false`, Helm manages only the controller resources.

### Option 2: Adopt Existing CRDs Into Helm

Use this if you want Helm to manage future Kelos CRD upgrades by default.

```bash
RELEASE=kelos
NAMESPACE=kelos-system

for crd in \
  agentconfigs.kelos.dev \
  sessions.kelos.dev \
  tasks.kelos.dev \
  taskbudgets.kelos.dev \
  taskrecords.kelos.dev \
  taskspawners.kelos.dev \
  workerpools.kelos.dev \
  workspaces.kelos.dev
do
  kubectl label crd "$crd" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate crd "$crd" meta.helm.sh/release-name="$RELEASE" --overwrite
  kubectl annotate crd "$crd" meta.helm.sh/release-namespace="$NAMESPACE" --overwrite
done

helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version> \
  --set crds.install=false

kubectl rollout status deployment/kelos-controller-manager \
  -n kelos-system --timeout=120s

helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version> \
  --set crds.install=true
```

After adoption, stop managing those CRDs with `kelos install` or `kubectl apply`.

## Upgrades

For controller-only Helm upgrades, use the chart defaults:

```bash
helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version> \
  --set crds.install=false
```

The chart passes `image.tag` to the controller through `--version`. The
controller applies that version to untagged managed image values, including
`sessionRuntime.image`; tagged and digested values remain explicit overrides.
When an upgrade changes the resolved runtime image, each existing Session
StatefulSet replaces its Pod through a rolling update. Active work is interrupted
and is not submitted again automatically. Persistent Session workspaces survive
the replacement, while `emptyDir` workspaces do not.

If Helm owns the CRDs and the release includes CRD changes, upgrade in two
phases so the new webhook is ready before Helm applies conversion-backed CRDs:

```bash
helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version> \
  --set crds.install=false

kubectl rollout status deployment/kelos-controller-manager \
  -n kelos-system --timeout=120s

helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version> \
  --set crds.install=true
```

## Codex OAuth Token Refresh

Codex OAuth credentials are stored in the `CODEX_AUTH_JSON` key of a credentials Secret. Kelos writes this bundle to `~/.codex/auth.json` and configures Codex to use file-backed credentials (`cli_auth_credentials_store = "file"`), matching OpenAI's CI/CD auth guidance. Agent pods are ephemeral, so refreshed tokens written by the Codex CLI inside a pod are not persisted back to that source Secret.

The chart includes a controller that creates one CronJob per labeled Codex OAuth credentials Secret, refreshing each bundle independently of agent activity by posting to OpenAI's OAuth token endpoint:

```yaml
codexAuthRefresher:
  schedule: "0 */6 * * *"
```

Label each Secret that should be refreshed:

```bash
kubectl label secret codex-credentials \
  kelos.dev/codex-oauth-refresh=true \
  -n <credentials-namespace>
```

The controller watches Secrets labeled `kelos.dev/codex-oauth-refresh=true` and manages a dedicated CronJob for each Secret that has a non-empty `CODEX_AUTH_JSON` key. Each CronJob runs in the target Secret namespace with access limited to that Secret, refreshes that bundle through the endpoint, and updates only the `CODEX_AUTH_JSON` key. Removing the label or deleting the Secret removes the managed refresh resources. Secrets without `CODEX_AUTH_JSON`, API-key credentials, and OAuth bundles without a `refresh_token` are skipped. Externally managed Secrets, such as ExternalSecrets, Vault-synced Secrets, or sealed-secrets, will overwrite the refreshed value on their next sync and are not supported.

If `CODEX_AUTH_JSON` does not include `client_id` (top-level or `tokens.client_id`), Kelos uses OpenAI's public Codex OAuth client id `app_EMoamEEZ73f0CkXaXp7hrann` as the fallback for token refresh.

## Session Web Chat

Sessions use `emptyDir` workspaces unless `spec.volumeClaimTemplate` is set.
Configure persistent storage on each Session that must preserve provider state,
repository changes, and chat event history across Pod replacement:

```yaml
spec:
  volumeClaimTemplate:
    accessModes:
      - ReadWriteOnce
    storageClassName: standard-rwo
    resources:
      requests:
        storage: 20Gi
```

The selected StorageClass must dynamically provision or otherwise bind a
matching volume. The claim survives Pod, StatefulSet, and scaling changes and
is deleted with its owning Session; the StorageClass reclaim policy controls
the underlying PersistentVolume.

The shared Session server serves the web application and bridges each chat to
its Session Pod through Kubernetes exec. It is disabled by default and requires
a non-empty static token in an existing Secret:

```bash
kubectl create secret generic kelos-session-auth \
  --from-literal=token='replace-with-a-long-random-token' \
  -n kelos-system

helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --set sessionServer.enabled=true \
  --set sessionServer.secretName=kelos-session-auth
```

The default Service is cluster-internal. For local access:

```bash
kubectl port-forward -n kelos-system service/kelos-session-server 8080:80
```

Then open `http://localhost:8080` and enter the token. The token represents one
shared user that can create, list, delete, and connect to Sessions in any
namespace. The web application operates on one active namespace at a time and
can switch it live from the sidebar. `sessionServer.defaultNamespace` (`default`
unless overridden) sets the initial active namespace. The selected namespace
must already exist. Session, Workspace, AgentConfig, and previously used
credential options are loaded only from the active namespace. The creation
dialog can generate a new Session from an existing Session in that namespace,
copying its complete `Session.spec` into the form and editable YAML manifest but
not copying its metadata, conversation, or volume data. The creation form
accepts provider, credentials, model, Workspace, AgentConfig references, and an
optional persistent volume claim. In YAML mode, the server applies one
`kelos.dev/v1alpha2` Session manifest in the active namespace. The manifest may
also include labels, annotations, the complete `WorkerSpec`, and an optional
persistent volume claim.

## Uninstall

To uninstall the Helm release:

```bash
helm uninstall kelos -n kelos-system
```

By default, Helm preserves the Kelos CRDs and their custom resources during
uninstall. This avoids deleting conversion-webhook-backed CRDs after Helm has
already removed the controller that clears custom-resource finalizers.

For a full cleanup, use `kelos uninstall` instead of `helm uninstall`, or delete
all Kelos custom resources while the controller is still running, upgrade the
release with `crds.keep=false`, and then uninstall the chart.

## Webhook Server Configuration

The chart includes an optional webhook server for GitHub integration. It is disabled by default and must be explicitly enabled.

### Prerequisites

1. Create secrets containing webhook signing secrets:

```bash
# GitHub webhook secret
kubectl create secret generic github-webhook-secret \
  --from-literal=WEBHOOK_SECRET=your-github-webhook-secret \
  -n kelos-system
```

2. Configure webhooks in your GitHub repositories to send events to your webhook endpoints.

### Enable Webhook Servers

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --set webhookServer.sources.github.enabled=true \
  --set webhookServer.sources.github.secretName=github-webhook-secret \
  --set webhookServer.ingress.enabled=true \
  --set webhookServer.ingress.host=webhooks.your-domain.com \
  --set webhookServer.ingress.className=nginx \
  --set webhookServer.ingress.tls.enabled=true \
  --set-json 'webhookServer.ingress.annotations={"cert-manager.io/cluster-issuer":"letsencrypt-prod"}'
```

### Webhook Configuration Options

```yaml
webhookServer:
  image: ghcr.io/kelos-dev/kelos-webhook-server
  sources:
    github:
      enabled: false          # Enable GitHub webhook server
      replicas: 1            # Number of replicas
      secretName: ""         # Secret containing WEBHOOK_SECRET
  ingress:
    enabled: false           # Enable ingress for external access
    className: ""           # Ingress class name (e.g., nginx)
    host: ""               # Hostname for webhook endpoints
    annotations: {}        # Additional ingress annotations
    tls:
      enabled: false         # Enable TLS for the ingress
      secretName: ""         # Secret name containing TLS certificate
```

### TLS Configuration

The webhook ingress supports TLS termination for secure HTTPS connections. TLS is strongly recommended for production deployments.

#### Option 1: Use cert-manager for automatic certificate management

Install cert-manager first if it is not already present. Follow the
[cert-manager installation documentation](https://cert-manager.io/docs/installation/)
for the current recommended installation method.

```bash
# Configure with cert-manager annotations
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --set webhookServer.sources.github.enabled=true \
  --set webhookServer.sources.github.secretName=github-webhook-secret \
  --set webhookServer.ingress.enabled=true \
  --set webhookServer.ingress.host=webhooks.your-domain.com \
  --set webhookServer.ingress.className=nginx \
  --set webhookServer.ingress.tls.enabled=true \
  --set-json 'webhookServer.ingress.annotations={"cert-manager.io/cluster-issuer":"letsencrypt-prod"}'
```

#### Option 2: Use existing TLS certificate

```bash
# Create TLS secret manually
kubectl create secret tls webhook-tls-secret \
  --cert=path/to/tls.crt \
  --key=path/to/tls.key \
  -n kelos-system

# Configure ingress to use the secret
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --set webhookServer.ingress.enabled=true \
  --set webhookServer.ingress.host=webhooks.your-domain.com \
  --set webhookServer.ingress.tls.enabled=true \
  --set webhookServer.ingress.tls.secretName=webhook-tls-secret
```

### Webhook Endpoints

When enabled, the webhook servers expose the following endpoints:

- **GitHub**: `https://your-host/webhook/github`

### Example Values File

See `examples/helm-values-webhook.yaml` for a complete example configuration.

### Gateway API (Alternative to Ingress)

Kelos webhook servers can be exposed via the Kubernetes Gateway API instead of an Ingress. See `examples/gateway-api-webhook.md` for prerequisites, configuration, and a provider comparison (Istio, Envoy Gateway, Kong, Nginx Gateway Fabric). The companion values file is `examples/webhook-gateway-values.yaml`.

### Concurrency Behavior

For webhook-driven TaskSpawners, see `examples/webhook-concurrency.md` for how `maxConcurrency` enforcement works (events accepted with HTTP 200 and skipped when at the limit), monitoring tips, and troubleshooting guidance.
