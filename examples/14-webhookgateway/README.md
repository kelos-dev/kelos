# WebhookGateway: per-channel auth and multiple GitHub instances

A `WebhookGateway` is a per-channel authentication and routing boundary for
webhook-driven TaskSpawners. Instead of one "universal" webhook server with a
single shared `WEBHOOK_SECRET` and a single GitHub backend, each gateway:

- is configured by exactly one provider sub-struct — `spec.github`,
  `spec.linear`, or `spec.generic` — whose presence selects the source;
- owns one inbound path, `/webhook/<namespace>/<name>` (surfaced in
  `status.path`);
- carries its own HMAC secret (`github.secretRef` / `linear.secretRef`) used to
  verify inbound deliveries;
- for `github`, carries its own outbound API base URL (`github.apiBaseURL`) and
  credentials (`github.credentialsRef`) used for pull-request file enrichment
  and status reporting;
- fans out only to TaskSpawners **in its own namespace** that reference it via
  `when.<source>Webhook.gatewayRef`.

This makes per-tenant secrets and **multiple GitHub instances** (github.com plus
one or more GitHub Enterprise servers) possible from a single gateway server,
declared entirely as Kubernetes resources — no per-instance Deployment and no
out-of-band env vars.

## What this example deploys

- `webhookgateway.yaml`: two `github` gateways — `github-com` (uses
  `https://api.github.com`) and `ghe` (a GitHub Enterprise server at
  `https://ghe.example.com/api/v3`), each with its own webhook secret and API
  credentials.
- `taskspawner.yaml`: a TaskSpawner bound to the `ghe` gateway via `gatewayRef`,
  triggered by `/kelos` issue comments on the Enterprise instance.

## Secrets

Each `github` gateway references two Secrets in its namespace:

```sh
# HMAC secret — must match the secret configured on the GitHub webhook.
# The key must be "webhook-secret".
kubectl create secret generic ghe-webhook \
  --from-literal=webhook-secret="<the webhook signing secret>"

# Outbound API credentials — a PAT (GITHUB_TOKEN) or GitHub App keys
# (appID, installationID, privateKey).
kubectl create secret generic ghe-credentials \
  --from-literal=GITHUB_TOKEN="<token valid for the GHE instance>"
```

Repeat for the `github-com-webhook` and `github-com-credentials` Secrets.

> The `github.credentialsRef` Secret is for the webhook server's **outbound** API calls
> (PR-file enrichment and status reporting). Task execution (cloning and pushing
> the repo) uses the **Workspace's** `secretRef`, which is configured separately
> on the referenced `Workspace`.

## Enable the gateway server

The gateway server is off by default. Enable it (and a Gateway-API route) in your
Helm values:

```yaml
webhookServer:
  gatewayServer:
    enabled: true
  gateway:
    enabled: true
    gatewayClassName: "<your gateway class>"
    host: "webhooks.example.com"
```

## Apply and verify

```sh
kubectl apply -f webhookgateway.yaml
kubectl get webhookgateways
# NAME         PATH                          PHASE
# github-com   /webhook/default/github-com   Authenticated
# ghe          /webhook/default/ghe          Authenticated

kubectl apply -f taskspawner.yaml
```

Configure each GitHub instance's repository webhook to POST to the gateway's
`status.path`, relative to your configured webhook host — for example
`https://webhooks.example.com/webhook/default/ghe` — using the matching webhook
secret. The gateway verifies the HMAC signature with that gateway's
`github.secretRef` and creates a Task for the `ghe-comment-responder` spawner.

## Notes

- `apiBaseURL` and `credentialsRef` live under `spec.github` (GitHub-only).
- `linear` gateways carry only `linear.secretRef`.
- `generic` gateways are accepted but **not signature-verified** in this version
  (the gateway's `status.phase` is `Unauthenticated`); restrict access at the
  network layer until a per-provider verification scheme is added.
- Spawners with a `gatewayRef` are served exclusively by the gateway; the legacy
  per-source webhook server ignores them, so a spawner is never triggered twice.
