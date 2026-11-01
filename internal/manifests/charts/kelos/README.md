# Kelos Helm Chart

The Kelos Helm chart is published as an OCI artifact in GHCR:

```bash
oci://ghcr.io/kelos-dev/charts/kelos
```

## Requirements

[cert-manager](https://cert-manager.io/) must be installed in the cluster before
installing Kelos. Its admission webhook certificate is issued by cert-manager
and is also used by CRD conversion webhooks when an API version requires them.
Follow the [cert-manager installation documentation](https://cert-manager.io/docs/installation/)
for the current recommended installation method.

`kelos install` checks for cert-manager and fails fast with this guidance if it
is missing.

## Release Namespace

The chart installs every namespaced resource, including the controller
manager, into the Helm release namespace. When the release namespace is
`kelos-system`, the namespace `kelos install` uses, the chart also renders that
`Namespace`; any other release namespace must already exist or be created with
`--create-namespace`.

Kelos CRDs send conversion requests to the `kelos-webhook` Service in the
namespace they name. CRDs rendered with `crds.install=true` name the release
namespace, and CRDs installed by `kelos install` name `kelos-system`. With
`crds.install=false`, Helm installs and upgrades fail if the existing
`tasks.kelos.dev` CRD names a namespace other than the release namespace. This
check reads the CRD, so the Helm identity needs `get` on
`customresourcedefinitions`. Tools that render with `helm template`, such as
Argo CD, skip it.

### Releases That Own `kelos-system` From Another Namespace

A release in another namespace that owns `Namespace/kelos-system` runs the
controller manager there. Check with:

```bash
helm get manifest kelos -n <namespace> | grep -A2 '^kind: Namespace'
```

Upgrading such a release moves the controller manager, webhook Service,
certificate, leader-election RBAC, and telemetry CronJob into the release
namespace. Helm then deletes `Namespace/kelos-system` with everything in it, and
the telemetry installation ID is regenerated. To keep the namespace, annotate
it before upgrading:

```bash
kubectl annotate namespace kelos-system helm.sh/resource-policy=keep
```

The CRDs must point at the new webhook Service in the same upgrade, so do not
use the two-phase flow from [Upgrades](#upgrades) for this upgrade:

- If Helm owns the CRDs, upgrade once with `--set crds.install=true`.
- If the CRDs are managed outside Helm, label and annotate them as in
  [Option 2](#option-2-adopt-existing-crds-into-helm) with `NAMESPACE` set to
  the release namespace, then upgrade once with `--set crds.install=true`.

Conversion requests fail until the controller manager and its serving
certificate are ready in the release namespace.

## First-Time Install

For most first-time installs, use `kelos install`; it stages the controller,
certificate, webhooks, and CRDs automatically.

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
The CRDs are packaged in a conditional subchart, so controller-only releases
do not store the CRD payload in Helm's release Secret.

CRDs installed by `kelos install` send conversion requests to `kelos-system`,
so this option requires the release namespace `kelos-system`. See
[Release Namespace](#release-namespace).

### Option 2: Adopt Existing CRDs Into Helm

Use this if you want Helm to manage future Kelos CRD upgrades by default.

```bash
RELEASE=kelos
NAMESPACE=kelos-system

for crd in \
  agentconfigs.kelos.dev \
  sessions.kelos.dev \
  sessionspawners.kelos.dev \
  tasks.kelos.dev \
  taskbudgets.kelos.dev \
  taskpipelines.kelos.dev \
  taskrecords.kelos.dev \
  taskspawners.kelos.dev \
  webhookgateways.kelos.dev \
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

Before a Helm-managed CRD upgrade that removes an API version, migrate stored
resources while the currently installed controller is still running:

```bash
kelos migrate-storage --kubeconfig <path>
```

`kelos install` performs this migration automatically. The command is
idempotent and also prepares clusters for future storage-version changes.

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
runtime stops accepting new turns and finishes all previously accepted turns.
The desired runtime is recorded immediately, while the existing Pod remains in
place until its runtime reports that it is drained. Pending user input delays
the Pod replacement until it is answered or interrupted. A failed Pod is
replaced immediately so it can start with the desired runtime. Persistent
Session workspaces survive the replacement, while `emptyDir` workspaces do
not.

If Helm owns the CRDs and a release adds a conversion-backed API version,
upgrade in two phases so the new webhook is ready before Helm applies the CRDs.
A release that owns `kelos-system` from another namespace upgrades in one
phase instead; see
[Releases That Own `kelos-system` From Another Namespace](#releases-that-own-kelos-system-from-another-namespace).

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

## Kelos Console

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

The Console provides an overview and manifest browser for Kelos resources in
the active namespace. Select a TaskSpawner and choose **Suspend** or **Resume**
to pause or enable automatic task creation. Existing Tasks keep running.
Choose **Run task** to create a standalone Task, with optional YAML or JSON
template values. Manual runs bypass the schedule, source filters, suspension,
and spawner task limits.
Its Sessions view creates and organizes Sessions, bridges
each chat to its Session Pod through Kubernetes exec, and can request a
destructive Session workspace reset after user confirmation. Long conversations
open at a bounded recent page and load earlier messages on demand. The Console
is disabled by default. Static-token mode is the default and is intended for
local or single-user access. It requires a non-empty token in an existing Secret:

```bash
kubectl create secret generic kelos-console-auth \
  --from-literal=token='replace-with-a-long-random-token' \
  -n kelos-system

helm upgrade kelos oci://ghcr.io/kelos-dev/charts/kelos \
  -n kelos-system \
  --set consoleServer.enabled=true \
  --set consoleServer.secretName=kelos-console-auth
```

The default Service is cluster-internal. For local access:

```bash
kubectl port-forward -n kelos-system service/kelos-console-server 8080:80
```

Then open `http://localhost:8080` and enter the token. The token represents one
shared user that can inspect Kelos resources, manage Workspaces, AgentConfigs,
and WorkerPools through the Admin page, suspend or resume TaskSpawners, run Tasks
from TaskSpawners, and create, reset, delete, and connect to Sessions in any
namespace. It also grants interactive shell access
to Ready Sessions' agent containers, including their workspaces and mounted
credentials. Treat it as a credential. For access
beyond a local port-forward,
terminate TLS at a trusted proxy, set `consoleServer.secureCookie=true`, and
restrict access to the endpoint at the network or proxy layer. The Console does
not provide separate user identities or per-user authorization in static-token
mode. Use OIDC or GitHub mode for shared team access.

### Console OIDC authentication

OIDC mode adds an OAuth2 Proxy sidecar and authorizes Console operations with
Kubernetes SubjectAccessReview. The cluster API server does not need to trust
the OIDC issuer. Namespace RoleBindings determine access; enabling OIDC does
not grant any user permission automatically.

Register a confidential OIDC client with the callback URL
`https://console.example.com/oauth2/callback`. The issuer must support the
Authorization Code flow with PKCE S256 and supply a stable `sub`, a verified email,
and the configured groups claim. Set up HTTPS at your ingress or reverse proxy;
the chart does not create an Ingress or certificate.

Create an existing Secret in the release namespace containing `client-secret`
and a `cookie-secret` with 32 random bytes encoded as URL-safe base64. For example,
`openssl rand -base64 32 | tr -- '+/' '-_'` generates a cookie secret. Keep both values out of
Helm values and command-line arguments. Then configure:

```yaml
consoleServer:
  enabled: true
  defaultNamespace: team-frontend
  auth:
    mode: oidc
    oidc:
      issuerURL: https://identity.example.com
      clientID: kelos-console
      redirectURL: https://console.example.com/oauth2/callback
      secretName: kelos-console-oidc
      usernamePrefix: "oidc:example:"
      groupsPrefix: "oidc:example:"
      groupsClaim: groups
      scope: openid profile email groups
      reverseProxy: true
      trustedProxyIPs:
        - 10.20.1.0/24
```

Set `trustedProxyIPs` to the actual ingress proxy addresses, including every
trusted hop that appears in `X-Forwarded-For`. Use narrowly scoped ranges;
catch-all CIDRs are rejected. OAuth2 Proxy validates IP/CIDR syntax at startup
and fails to start for invalid entries. These addresses identify trusted proxies and
never exempt clients from login. The proxy exposes the Service port, and
Kelos listens only on loopback. HTTP and WebSockets follow the same proxy
path. `/healthz` and `/readyz` accept unauthenticated GET probes through the
proxy to Kelos. Incoming identity and credential headers are removed; only
the verified subject and group identities are passed to Kelos.

Choose prefixes unique to this issuer; changing issuer while reusing its
prefixes can associate existing RoleBindings with different identities.
Prefixes are required and must not start with `system:` or be a prefix of it
(such as `system` or `sys`). Usernames use the OIDC
`sub`, not the display name or email. Bind groups rather than mutable display
names. Group claims must contain **comma-free group identifiers**: the proxy
uses commas to delimit groups, so group names containing commas are not
supported and must be mapped to identifiers at the IdP. Kelos accepts at most
1024 groups, 256 bytes per group, 65536 bytes (64 KiB) for the group header,
and 1024 bytes for the subject. Empty, repeated, or malformed identity headers
are rejected; a user may have no groups and receive permissions through a User
RoleBinding.
Large sessions also require ingress request and response header limits that
accommodate the proxy's session cookies.

The chart creates unbound `kelos-console-user` and `kelos-console-admin`
ClusterRoles in OIDC mode; GitHub mode creates only `kelos-console-user`.
Both roles allow manual Task creation from TaskSpawners and suspending or resuming
automatic task creation. The suspend/resume permissions do not grant general
TaskSpawner editing. To grant Session access, manual Task creation, TaskSpawner
suspension, and resource inspection in one namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kelos-console-developers
  namespace: team-frontend
subjects:
  - kind: Group
    name: oidc:example:frontend-developers
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: kelos-console-user
  apiGroup: rbac.authorization.k8s.io
```

Use narrower Roles for read-only or limited access. The permission table and
limitations are in [the Console authentication reference](../../../../docs/reference.md#console-authentication-and-authorization).
The generated user role grants no Pod permissions. The Console ServiceAccount
performs the underlying Pod operations after checking the caller's permissions.

The Admin page uses the same namespace RoleBindings. To bootstrap an
administrator who can manage configuration and assign User or Admin roles,
bind `kelos-console-admin` in the namespace:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kelos-console-administrators
  namespace: team-frontend
subjects:
  - kind: Group
    name: oidc:example:frontend-administrators
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: kelos-console-admin
  apiGroup: rbac.authorization.k8s.io
```

The Admin role grants `list`, `create`, and `delete` on every RoleBinding in
its bound namespace, including bindings for other applications. The Console
ServiceAccount has those permissions across all namespaces. Kubernetes RBAC
cannot restrict them to bindings with the Console's membership label; the
Console's HTTP handlers enforce that restriction for membership changes. A
compromised Console server could read, delete, or create RoleBindings in any
namespace, including bindings that grant other identities permissions the Console
ServiceAccount already holds, such as `pods/exec`. Kubernetes allows creating a
binding to a role whose permissions the caller already holds without explicit
`bind` permission. A user with the Admin role can also manage RoleBindings
directly through the Kubernetes API, including granting permissions they hold.
Grant Admin access only to people trusted to manage the namespace's RoleBindings.
Explicit `bind` permission is restricted to the two Console ClusterRoles; neither
role grants permission to edit or escalate ClusterRoles.

Choose an accessible namespace from the sidebar dropdown, then open **Admin →
Members**. **Add member** takes an OIDC subject ID and defaults to **User**.
The Console adds the configured username prefix and grants membership in the
selected namespace. User members can use Sessions and inspect resources;
Admin members can also manage configuration and members. The Console remembers
the selected namespace. Reopen the dropdown after access changes to refresh the
namespace list.

Use **Change role** to promote or demote a member, or **Remove member** to remove
their direct membership. When external user assignments remain, the action is
labeled **Remove direct access**. Role changes require permission to create RoleBindings
and bind the selected Console ClusterRole, plus delete permission for the
member's Console-managed bindings. The page shows each direct member once and
rejects changes based on stale membership versions. If a role change fails,
refresh Members to inspect the resulting role before retrying.

External User assignments and namespace group grants to the Console roles are
shown with their sources. External assignments cannot be changed here, and
removing direct membership preserves external and group access. The Console does
not query the identity provider for user names, email addresses, or individual
group members. Other roles and ClusterRoleBindings remain managed separately.
Membership management requires OIDC; it is unavailable with GitHub authentication
or a shared static token.
The Console ServiceAccount lists namespaces and filters them by the signed-in
user's permissions; human users do not need permission to list namespaces.
Discovery runs at most eight authorization checks concurrently per request. The
number of checks still grows with namespace count and the resource permissions
that must be evaluated.

`secretName`, `tokenKey`, and `secureCookie` at the `consoleServer` level belong
to static-token mode; do not combine static settings with OIDC. OIDC cookies
are always Secure and HttpOnly. Optional OIDC `clientSecretKey` and
`cookieSecretKey` default to `client-secret` and `cookie-secret`.
`groupsClaim` defaults to `groups`; `scope` defaults to `openid profile email groups`.
Some issuers require adding `offline_access` to issue refresh tokens. The
proxy requests a refresh after five minutes and uses an eight-hour cookie
expiry. Successful refreshes can renew the cookie; this is not an absolute
maximum login duration. IdP account and group changes depend on the issuer's
refresh and expiry behavior. Signing out clears the Console proxy cookie;
it does not necessarily end the IdP's SSO session.

The proxy image defaults to `quay.io/oauth2-proxy/oauth2-proxy:v7.15.5` and can
be set with `auth.oidc.image`. An explicit release tag of v7.15.5 or later is
required; a digest may follow the tag. The chart uses the proxy's structured
configuration to strip credential headers and preserve only the required
identity claims. Validate this configuration with `make test-integration` when
updating the proxy version. This target downloads the pinned proxy release
with the GitHub CLI and requires `gh` authentication (or `GH_TOKEN`).

### Console GitHub and GHES authentication

GitHub mode uses the managed OAuth2 Proxy sidecar's GitHub provider directly.
It supports GitHub.com and GitHub Enterprise Server (GHES), without requiring
an OIDC issuer or Dex. HTTPS ingress and namespace RoleBindings are required,
as in OIDC mode; the chart does not create an Ingress or certificate.

Register an OAuth application on the GitHub instance with callback URL
`https://console.example.com/oauth2/callback`. Create a Secret in the release
namespace with `client-secret` containing the OAuth application's secret and
`cookie-secret` containing 32 random bytes encoded as URL-safe base64. Keep
secret values out of Helm values and command-line arguments; supply protected
files to `kubectl create secret`:

```bash
kubectl -n kelos-system create secret generic kelos-console-github \
  --from-file=client-secret=/secure/path/github-client-secret \
  --from-file=cookie-secret=/secure/path/cookie-secret
```

The cookie secret can be generated with
`openssl rand -base64 32 | tr -- '+/' '-_'`. Save the following as
`console-github.yaml` and replace the example values:

```yaml
consoleServer:
  enabled: true
  defaultNamespace: team-frontend
  auth:
    mode: github
    github:
      enterpriseURL: https://github.company.example
      clientID: kelos-console-oauth-client
      redirectURL: https://console.example.com/oauth2/callback
      secretName: kelos-console-github
      usernamePrefix: "github:company:"
      groupsPrefix: "github:company:"
      org: my-org
      reverseProxy: true
      trustedProxyIPs:
        - 10.20.1.0/24
```

Set `trustedProxyIPs` to your actual ingress proxy addresses and trusted
forwarding hops. Configure the ingress to forward HTTP and WebSocket requests
to `kelos-console-server:80`. Then install with the values file:

```bash
kelos install --values console-github.yaml
```

Omit `enterpriseURL` to use GitHub.com. For GHES it must be an HTTPS origin,
optionally including a port, without credentials, a query, fragment, or path
other than `/`. The chart derives `/login/oauth/authorize`,
`/login/oauth/access_token`, and `/api/v3` from this origin.

For a private GHES certificate authority, create a Secret containing `ca.crt`
and set `consoleServer.auth.github.caSecretName` to its name. Only the proxy
mounts this certificate; it adds the CA to the system trust store and keeps
TLS verification enabled:

```bash
kubectl -n kelos-system create secret generic ghes-ca \
  --from-file=ca.crt=/secure/path/company-ca.pem
kelos install --values console-github.yaml \
  --set consoleServer.auth.github.caSecretName=ghes-ca
```

The provider requests `user:email read:org` and requires a verified primary
email. `org` optionally restricts sign-in to members of one organization;
authorize the OAuth application to read that organization's membership.
To restrict sign-in to one team within that organization, also set
`consoleServer.auth.github.team` to its slug, for example:

```yaml
consoleServer:
  auth:
    mode: github
    github:
      # Include the client, callback, Secret, and prefix settings shown above.
      org: my-org
      team: frontend-developers
```

`team` requires `org` and accepts a single team slug, not a display name,
comma-separated list, or `org:team-slug`. With only `org`, any organization
member may sign in. With both, the user must belong to that organization's team.
Login restrictions grant no Kubernetes permissions. To grant the
`my-org:frontend-developers` group Console access in an existing namespace:

```bash
kubectl -n team-frontend create rolebinding kelos-console-developers \
  --clusterrole=kelos-console-user \
  --group=github:company:my-org:frontend-developers
```

User identities use the prefixed GitHub login name, such as
`github:company:alice`, rather than an immutable numeric account ID. Login
names can change and be reused. Update User RoleBindings when accounts are
renamed or removed, and choose identity prefixes unique to each GitHub/GHES
instance. For immutable personal identities, use OIDC with stable subjects.
GitHub groups include all organization names and `org:team-slug` values
returned by the GitHub API for the user's OAuth token; `org` and `team` do not
filter these groups. Kelos accepts at most 1024 groups, 256 bytes per group, and
65536 bytes (64 KiB) for the comma-separated group header, before adding the
configured prefix. All groups are retained for RBAC; exceeding any of these
limits returns 401 rather than truncating the identity. Large sessions also
require ingress request and response header limits that accommodate the proxy's
session cookies. Groups are loaded at sign-in; the proxy's five-minute token
validation does not reload organization or team membership. Sign out and sign
in again to update these claims.

Removing a user from a GitHub organization or team does not end their existing
Console login or update its groups. Activity can keep renewing the eight-hour
cookie, so waiting eight hours does not guarantee access ends. To invalidate
an existing login, [revoke the user's OAuth grant or access token for the app](https://docs.github.com/en/rest/apps/oauth-applications#delete-an-app-authorization);
the proxy rejects that session on its next request after the five-minute
validation interval elapses. To invalidate all Console logins, rotate the
configured cookie secret and restart all Console server Pods; updating the
Secret alone does not reload the proxy's cookie secret. Do this when tightening
`org` or `team` restrictions to require existing users to sign in again.
Kubernetes RoleBinding changes affect subsequent requests. Token revocation and
RBAC changes do not terminate established WebSockets; restart the Console server
Pods to disconnect them.

GitHub mode accepts the same `clientSecretKey`, `cookieSecretKey`, `image`,
`reverseProxy`, and `trustedProxyIPs` settings as OIDC mode. Its cookies are
Secure, HttpOnly, and use a separate name from OIDC cookies. Do not combine
`auth.github` with `auth.oidc` or static-token settings. When changing the
provider instance, use a separate cookie secret and identity prefixes.

### Console context-source Secrets

The chart does not grant the Console ServiceAccount access to Secrets. For
manual TaskSpawner runs that use authenticated context sources, grant `get` on
each referenced Secret in the TaskSpawner's namespace. For example:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: kelos-console-context-secrets
  namespace: team-frontend
rules:
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: [context-auth]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: kelos-console-context-secrets
  namespace: team-frontend
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: kelos-console-context-secrets
subjects:
  - kind: ServiceAccount
    name: kelos-console-server
    namespace: kelos-system
```

Replace `team-frontend`, `context-auth`, and the ServiceAccount namespace with
the TaskSpawner namespace, Secret name, and chart release namespace.
Context-source requests use the Console ServiceAccount's access, rather than
the signed-in user's Secret permissions. Template values can affect the request
URL, headers, and body; users can read fetched context rendered into their Tasks.

### Console namespaces

The Console operates on one active namespace at a time and can switch it live
from the sidebar. `consoleServer.defaultNamespace` (`default` unless
overridden) sets the initial active namespace. The selected namespace must
already exist. All Console inventory and Session form options are loaded only
from the active namespace. The creation form and selected Session header can
assign Sessions to sidebar sections.
Assignments are stored in the `kelos.dev/session-section` annotation, and
the controls list existing section names in the active namespace or create a
section once for later reuse. In the sidebar, Sessions can be dragged between
sections, and section headings, including **Unsectioned**, can be dragged or
moved with arrow controls. Each browser stores section order separately for each
namespace. The selected Session's **Rename** control sets a web-only display
name without changing the Kubernetes resource name. Display names are stored in
the `kelos.dev/session-display-name` annotation; clearing the value restores the
resource name. The creation dialog can generate a new Session from an existing
Session in that namespace,
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
uninstall. This keeps the controller available to clear custom-resource
finalizers before the CRDs are deleted.

For a full cleanup, use `kelos uninstall` instead of `helm uninstall`, or delete
all Kelos custom resources while the controller is still running, upgrade the
release with `crds.keep=false`, and then uninstall the chart.

`kelos uninstall` also removes Console membership RoleBindings labeled
`kelos.dev/console-membership=true` across all namespaces. Unlabeled bootstrap
and externally managed bindings remain; remove them separately if their grants
should not become active again when the Console roles are reinstalled. Helm
uninstall does not remove membership bindings created through Console.

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
