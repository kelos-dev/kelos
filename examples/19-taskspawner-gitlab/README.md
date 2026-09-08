# 19 — TaskSpawner for GitLab Issues and Merge Requests

A TaskSpawner that polls a GitLab project (gitlab.com, self-hosted, or
in-cluster) for issues and merge requests carrying a trigger command and
creates a Task for each one. Task status is reported back as notes on the
originating issue or merge request.

## Use Case

Hand work to an agent straight from GitLab: label an issue or comment
`/kelos fix` on an issue or merge request, and the agent clones the repo,
does the work, and pushes a branch. Kelos posts a note when the Task is
accepted and again when it succeeds or fails.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `gitlab-token-secret.yaml` | Secret | GitLab access token for cloning, API polling, and notes |
| `workspace.yaml` | Workspace | GitLab repository to clone into each Task (`provider: gitlab`) |
| `taskspawner.yaml` | TaskSpawner | Watches GitLab issues and merge requests and spawns Tasks |
| `taskspawner-ci-remediation.yaml` | TaskSpawner | Spawns a Task for every merge request whose head pipeline failed |
| `taskspawner-webhook.yaml` | TaskSpawner | Real-time variant driven by GitLab webhooks (`/kelos fix` notes and failed pipelines) |
| `gitlab-webhook-secret.yaml` | Secret | Webhook secret token for the GitLab webhook server (webhook variant only) |

## How It Works

```
TaskSpawner polls GitLab issues + merge requests (label: kelos, state: opened)
    │
    ├── item with /kelos fix note → creates Task → posts "accepted" note
    │                                    └── agent pushes fix → posts "succeeded" note
    └── newer /kelos fix note on a finished item → retriggers a Task
```

The GitLab instance URL and project path are derived from the Workspace
`spec.repo`. Set `spec.when.gitlab.baseUrl` when the API must be reached on a
different address than the clone URL (for example an in-cluster Service), and
`spec.when.gitlab.project` to poll a different project than the one cloned.

## Steps

1. **Create a GitLab access token** (personal, group, or project token) with
   the `api` scope so it can clone, read issues and merge requests, and post
   notes.

2. **Edit the secrets** — replace placeholders in both secret files. The GitLab
   token goes under the `GITLAB_TOKEN` key.

3. **Edit `workspace.yaml`** — set your GitLab repository URL and branch and
   keep `provider: gitlab`, which tells Kelos to read `GITLAB_TOKEN`,
   authenticate git as `oauth2`, and preconfigure `glab` for the agent. For an
   in-cluster GitLab, use the Service URL, e.g.
   `http://gitlab-webservice-default.gitlab.svc:8181/group/repo.git`.

4. **Apply the resources:**

```bash
kubectl apply -f examples/19-taskspawner-gitlab/
```

5. **Verify the spawner is running:**

```bash
kubectl get taskspawners -w
```

6. **Trigger a Task** by adding the `kelos` label to an issue or merge request
   and commenting `/kelos fix` on it. The TaskSpawner picks it up on the next
   poll.

7. **Watch spawned Tasks:**

```bash
kubectl get tasks -w
```

8. **Cleanup:**

```bash
kubectl delete -f examples/19-taskspawner-gitlab/
```

## Customization

- Drop `commentPolicy` to spawn a Task for every labeled item without waiting
  for a command, or set `allowedUsers` to restrict who may issue commands.
- Set `types` to `["issues"]` or `["mergeRequests"]` to watch only one kind.
- Gate merge requests on `pipelineStatus: failed` (see
  `taskspawner-ci-remediation.yaml`) or `reviewState: changes_requested` to
  react to CI failures and review outcomes instead of labels.
- Use `reporting.comments.mode: Sticky` to keep one status note per item
  instead of one per Task.
- Give each merge request one gate. Two TaskSpawners that both match the same
  merge request (for example `pipelineStatus: failed` and
  `reviewState: changes_requested`) each spawn a Task that pushes to the same
  source branch, so they race each other.
- Adjust `pollInterval` inside the source block to control how often GitLab is polled.

## Webhook Variant

`taskspawner-webhook.yaml` reacts within seconds instead of on a poll
interval. It needs the GitLab webhook server: set
`webhookServer.sources.gitlab.enabled: true` and
`webhookServer.sources.gitlab.secretName: gitlab-webhook-secret` in your Helm
values (see [`examples/helm-values-webhook.yaml`](../helm-values-webhook.yaml)),
then add a project webhook in GitLab (Settings → Webhooks) with the URL
`https://<webhook-host>/webhook/gitlab`, the token from
`gitlab-webhook-secret.yaml`, and the **Issues events**, **Comments** and
**Pipeline events** triggers enabled. Filters accept `labels` (all required)
and `excludeLabels` (any rejects) on `issue`, `merge_request` and `note`
events; note filters use the labels of the commented issue or merge request. For an in-cluster GitLab, point the webhook at the
`kelos-webhook-gitlab` Service and allow local network requests in the GitLab
admin settings (Admin → Settings → Network → Outbound requests). To get status
notes from webhook-created Tasks, also set
`webhookServer.sources.gitlab.tokenSecretName` to a Secret holding a
`GITLAB_TOKEN` with the `api` scope.
