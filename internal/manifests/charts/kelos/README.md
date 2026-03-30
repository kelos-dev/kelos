# Kelos Helm Chart

The Kelos Helm chart is published as an OCI artifact in GHCR:

```bash
oci://ghcr.io/kelos-dev/charts/kelos
```

## First-Time Install

For a fresh Helm-managed install:

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version>
```

By default, the chart installs and upgrades the Kelos CRDs:

```yaml
crds:
  install: true
  keep: true
```

`crds.keep=true` keeps Helm from deleting the CRDs during chart uninstall.

## Migrating Existing CRDs Into Helm Ownership

If your cluster already has Kelos CRDs from `kelos install` or `kubectl apply`, your first Helm install or upgrade must choose one CRD owner.

### Option 1: Keep CRDs Managed Outside Helm

Use this if you want to continue managing CRDs with `kelos install` or another manifest-based workflow:

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version> \
  --set crds.install=false
```

With `crds.install=false`, Helm manages only the controller resources.

### Option 2: Adopt Existing CRDs Into Helm

Use this if you want Helm to manage future Kelos CRD upgrades by default.

```bash
RELEASE=kelos
NAMESPACE=kelos-system

for crd in \
  agentconfigs.kelos.dev \
  tasks.kelos.dev \
  taskspawners.kelos.dev \
  workspaces.kelos.dev
do
  kubectl label crd "$crd" app.kubernetes.io/managed-by=Helm --overwrite
  kubectl annotate crd "$crd" meta.helm.sh/release-name="$RELEASE" --overwrite
  kubectl annotate crd "$crd" meta.helm.sh/release-namespace="$NAMESPACE" --overwrite
done

helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --version <version>
```

After adoption, stop managing those CRDs with `kelos install` or `kubectl apply`.

## Upgrades

For Helm-managed installs where Helm already owns the CRDs, a normal upgrade is:

```bash
helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version>
```

If your cluster still manages CRDs outside Helm, keep using:

```bash
helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --version <version> \
  --set crds.install=false
```

## Uninstall

To uninstall the Helm release:

```bash
helm uninstall kelos -n kelos-system
```

Because `crds.keep=true` by default, uninstalling the chart does not delete the Kelos CRDs.

## Webhook Server Configuration

The chart includes optional webhook servers for GitHub and Linear integration. These are disabled by default and must be explicitly enabled.

### Prerequisites

1. Create secrets containing webhook signing secrets:

```bash
# GitHub webhook secret
kubectl create secret generic github-webhook-secret \
  --from-literal=WEBHOOK_SECRET=your-github-webhook-secret \
  -n kelos-system

# Linear webhook secret
kubectl create secret generic linear-webhook-secret \
  --from-literal=WEBHOOK_SECRET=your-linear-webhook-secret \
  -n kelos-system
```

2. Configure webhooks in your GitHub repositories or Linear workspace to send events to your webhook endpoints.

### Enable Webhook Servers

```bash
helm upgrade --install kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --create-namespace \
  --set webhookServer.sources.github.enabled=true \
  --set webhookServer.sources.github.secretName=github-webhook-secret \
  --set webhookServer.sources.linear.enabled=true \
  --set webhookServer.sources.linear.secretName=linear-webhook-secret \
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
    linear:
      enabled: false          # Enable Linear webhook server
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

```bash
# Install cert-manager if not already present
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.13.0/cert-manager.yaml

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
- **Linear**: `https://your-host/webhook/linear`

### Example Values File

See `examples/helm-values-webhook.yaml` for a complete example configuration.
