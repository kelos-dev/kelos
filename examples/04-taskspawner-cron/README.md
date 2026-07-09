# 04 — TaskSpawner with Cron Schedule

A TaskSpawner that creates Tasks on a cron schedule. Useful for recurring
maintenance, dependency updates, or periodic code health checks.

## Use Case

Run an AI agent every Monday at 9 AM UTC to check for outdated dependencies
and open a PR with updates.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Anthropic API key for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `workspace.yaml` | Workspace | Git repository to clone |
| `taskspawner.yaml` | TaskSpawner | Cron schedule that spawns Tasks |

## Steps

1. **Edit the secrets** — replace placeholders in both secret files.

2. **Edit `workspace.yaml`** — set your repository URL and branch.

3. **Edit `taskspawner.yaml`** — adjust the cron schedule if needed.

4. **Apply the resources:**

```bash
kubectl apply -f examples/04-taskspawner-cron/
```

5. **Verify the spawner is running:**

```bash
kubectl get taskspawners -w
```

6. **Watch spawned Tasks after the schedule fires:**

```bash
kubectl get tasks -w
```

7. **Create a standalone Task without waiting for the schedule:**

```bash
kelos run --from taskspawner/weekly-dependency-update
```

The command always uses the current UTC time for `{{.Time}}`, so no values file
is needed. It instantiates the Task template directly; it does not update the
TaskSpawner's status or apply its concurrency and lifetime limits.

8. **Cleanup:**

```bash
kubectl delete -f examples/04-taskspawner-cron/
```

## Cron Schedule Reference

| Expression | Meaning |
|-----------|---------|
| `0 9 * * 1` | Every Monday at 9:00 AM UTC |
| `0 * * * *` | Every hour |
| `0 0 * * *` | Every day at midnight UTC |
| `*/30 * * * *` | Every 30 minutes |
| `0 9 * * 1-5` | Weekdays at 9:00 AM UTC |
