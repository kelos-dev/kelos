# 10 — TaskSpawner for GitHub Webhooks

A TaskSpawner that reacts to GitHub webhook events (issues and pull requests)
instead of polling the GitHub API. Webhook events are received by the
`kelos-webhook-receiver` and stored as `WebhookEvent` custom resources that
the spawner watches.

## Use Case

React instantly to new or labeled GitHub issues and pull requests without
polling delays. A GitHub webhook fires, the receiver persists the event, and
the spawner creates a Task to investigate.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `webhook-secret.yaml` | Secret | GitHub webhook secret for payload signature validation |
| `workspace.yaml` | Workspace | Git repository to clone into each Task |
| `taskspawner.yaml` | TaskSpawner | Watches WebhookEvent resources and spawns Tasks |

## How It Works

```
GitHub webhook POST → kelos-webhook-receiver → WebhookEvent CR
    │
    └── TaskSpawner watches WebhookEvents
            ├── new issue event → creates Task → agent fixes issue → opens PR
            ├── new PR event   → creates Task → agent reviews PR
            └── ...
```

## Prerequisites

The `kelos-webhook-receiver` must be deployed and accessible from GitHub.
When installed via `kelos install`, the webhook receiver deployment and RBAC
are created automatically in the `kelos-system` namespace. You need to:

1. Create a GitHub webhook in your repository settings pointing to the
   receiver's external URL at `/webhook/github`.
2. Set the content type to `application/json`.
3. Choose which events to send (e.g., Issues, Pull requests, Issue comments).
4. Set a webhook secret and store it in the `webhook-secret.yaml`.

## Steps

1. **Edit the secrets** — replace placeholders in the secret files.

2. **Edit `workspace.yaml`** — set your repository URL and branch.

3. **Apply the resources:**

```bash
kubectl apply -f examples/10-taskspawner-github-webhook/
```

4. **Verify the spawner is running:**

```bash
kubectl get taskspawners -w
```

5. **Trigger a webhook** by creating an issue with the `kelos-task` label in
   your repository. The receiver creates a WebhookEvent and the spawner
   picks it up immediately.

6. **Watch spawned Tasks:**

```bash
kubectl get tasks -w
```

7. **Cleanup:**

```bash
kubectl delete -f examples/10-taskspawner-github-webhook/
```

## Customization

- Change `labels` in `taskspawner.yaml` to match your labeling scheme.
- Add `excludeLabels` to skip items that need human input.
- Set `actions` to limit which webhook actions trigger tasks (e.g., only
  `opened` and `reopened`).
- Use `commentPolicy` with `triggerComment` to require a slash command
  (e.g., `/kelos-run`) before creating a task.
- Set `maxConcurrency` to limit how many Tasks run in parallel.
- Edit `promptTemplate` to give the agent more specific instructions.
