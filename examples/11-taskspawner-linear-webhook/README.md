# 11 — TaskSpawner for Linear Webhooks

A TaskSpawner that reacts to Linear webhook events. Linear webhooks are
received by the `kelos-webhook-receiver` at `/webhook/linear` and stored
as `WebhookEvent` custom resources that the spawner watches.

## Use Case

Automatically assign an AI agent to new Linear issues. When an issue is
created or moves to "Todo", a webhook fires, the receiver persists the
event, and the spawner creates a Task to work on it.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `workspace.yaml` | Workspace | Git repository to clone into each Task |
| `taskspawner.yaml` | TaskSpawner | Watches Linear WebhookEvent resources and spawns Tasks |

## How It Works

```
Linear webhook POST → kelos-webhook-receiver → WebhookEvent CR
    │
    └── TaskSpawner watches WebhookEvents (source: "linear")
            ├── new issue event → creates Task → agent works on issue
            ├── updated issue  → creates Task → agent works on issue
            └── ...
```

## Prerequisites

The `kelos-webhook-receiver` must be deployed and accessible from Linear.
When installed via `kelos install`, the webhook receiver deployment and RBAC
are created automatically in the `kelos-system` namespace. You need to:

1. In Linear, go to **Settings > API > Webhooks** and create a new webhook.
2. Set the URL to your receiver's external endpoint at `/webhook/linear`.
3. Select which events to receive (e.g., Issues).

## Steps

1. **Edit the secrets** — replace placeholders in the secret files.

2. **Edit `workspace.yaml`** — set your repository URL and branch.

3. **Apply the resources:**

```bash
kubectl apply -f examples/11-taskspawner-linear-webhook/
```

4. **Verify the spawner is running:**

```bash
kubectl get taskspawners -w
```

5. **Trigger a webhook** by creating or updating a Linear issue. The
   receiver creates a WebhookEvent and the spawner picks it up.

6. **Watch spawned Tasks:**

```bash
kubectl get tasks -w
```

7. **Cleanup:**

```bash
kubectl delete -f examples/11-taskspawner-linear-webhook/
```

## Customization

- Set `types` to react to different Linear object types (e.g., `["Issue", "Comment"]`).
- Set `actions` to limit which actions trigger tasks (e.g., only `["create"]`).
- Use `states` to filter by workflow state (e.g., `["Todo", "In Progress"]`).
  By default, terminal states ("Done", "Canceled") are excluded.
- Add `labels` or `excludeLabels` for label-based filtering.
- Set `maxConcurrency` to limit how many Tasks run in parallel.
- Edit `promptTemplate` to give the agent more specific instructions.
