# 03 — TaskSpawner for GitHub Issues

A TaskSpawner that polls GitHub for issues matching a label filter and
automatically creates a Task for each one. This is the core pattern for
autonomous issue resolution.

## Use Case

Automatically assign an AI agent to every new `bug`-labeled issue. The agent
clones the repo, investigates the issue, and opens a PR with a fix.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning, PR creation, and issue polling |
| `workspace.yaml` | Workspace | Git repository to clone into each Task |
| `taskspawner.yaml` | TaskSpawner | Watches GitHub issues and spawns Tasks |

## How It Works

```
TaskSpawner polls GitHub Issues (label: bug, state: open)
    │
    ├── new issue found → creates Task → agent fixes bug → opens PR
    ├── new issue found → creates Task → agent fixes bug → opens PR
    └── ...
```

## Steps

1. **Edit the secrets** — replace placeholders in both secret files.

2. **Edit `workspace.yaml`** — set your repository URL and branch.

3. **Apply the resources:**

```bash
kubectl apply -f examples/03-taskspawner-github-issues/
```

4. **Verify the spawner is running:**

```bash
axon get taskspawners -w
```

5. **Create a test issue** with the `bug` label in your repository. The
   TaskSpawner picks it up on the next poll and creates a Task.

6. **Watch spawned Tasks:**

```bash
axon get tasks -w
```

7. **Cleanup:**

```bash
kubectl delete -f examples/03-taskspawner-github-issues/
```

## Customization

- Change `labels` in `taskspawner.yaml` to match your labeling scheme.
- Add `excludeLabels` to skip issues that need human input (e.g.,
  `["needs-triage", "wontfix"]`).
- Adjust `pollInterval` to control how often GitHub is polled.
- Set `maxConcurrency` to limit how many Tasks run in parallel.
- Edit `promptTemplate` to give the agent more specific instructions.
