# 05 — TaskSpawner for Bitbucket Data Center PRs

A TaskSpawner that polls Bitbucket Data Center for pull requests and
automatically creates a Task for each one. This enables automated PR
review or processing for teams using Bitbucket Data Center (Server).

## Use Case

Automatically assign an AI agent to review every open pull request.
The agent clones the repo, reviews the PR, and provides feedback.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `bitbucket-token-secret.yaml` | Secret | Bitbucket HTTP access token for API polling and cloning |
| `workspace.yaml` | Workspace | Bitbucket DC repository to clone into each Task |
| `taskspawner.yaml` | TaskSpawner | Watches Bitbucket DC PRs and spawns Tasks |

## How It Works

```
TaskSpawner polls Bitbucket Data Center PRs (state: OPEN)
    |
    +-- new PR found -> creates Task -> agent reviews PR -> provides feedback
    +-- new PR found -> creates Task -> agent reviews PR -> provides feedback
    +-- ...
```

## Steps

1. **Edit the secrets** — replace placeholders in both secret files.

2. **Edit `workspace.yaml`** — set your Bitbucket Data Center repository URL and branch.

3. **Apply the resources:**

```bash
kubectl apply -f examples/05-taskspawner-bitbucket-dc-prs/
```

4. **Verify the spawner is running:**

```bash
kubectl get taskspawners -w
```

5. **Create a test pull request** in your repository. The TaskSpawner picks
   it up on the next poll and creates a Task.

6. **Watch spawned Tasks:**

```bash
kubectl get tasks -w
```

7. **Cleanup:**

```bash
kubectl delete -f examples/05-taskspawner-bitbucket-dc-prs/
```

## Customization

- Change `state` in `taskspawner.yaml` to `MERGED`, `DECLINED`, or `ALL`.
- Adjust `pollInterval` to control how often Bitbucket DC is polled.
- Set `maxConcurrency` to limit how many Tasks run in parallel.
- Edit `promptTemplate` to give the agent more specific instructions.
