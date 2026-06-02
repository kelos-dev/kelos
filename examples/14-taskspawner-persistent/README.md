# 14 — TaskSpawner with Persistent Sessions

A TaskSpawner using `executionMode: persistent` to run long-lived session pods
that process tasks sequentially without cold-start overhead. The workspace
persists across tasks via a PVC, so git history, build caches, and
`node_modules` survive between assignments.

## Use Case

A team uses Slack-triggered tasks that require quick response times. Rather
than spinning up a new pod for each task (30-60s cold start), persistent
sessions keep pods warm and ready to accept work immediately.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Anthropic API key for the agent |
| `github-token-secret.yaml` | Secret | GitHub token for cloning and pushing |
| `workspace.yaml` | Workspace | Git repository with persistent storage |
| `taskspawner.yaml` | TaskSpawner | Persistent-mode spawner with session config |

## Steps

1. **Edit the secrets** — replace placeholders in both secret files.

2. **Edit `workspace.yaml`** — set your repository URL.

3. **Edit `taskspawner.yaml`** — adjust replicas, idle timeout, or storage
   size as needed.

4. **Apply the resources:**

```bash
kubectl apply -f examples/14-taskspawner-persistent/
```

5. **Verify the StatefulSet is running:**

```bash
kubectl get statefulsets -l kelos.dev/component=session
kubectl get pods -l kelos.dev/component=session
```

6. **Create tasks and watch them get picked up immediately:**

```bash
kelos run -p "Fix the typo in README.md" --workspace my-workspace
kubectl get tasks -w
```

7. **Cleanup:**

```bash
kubectl delete -f examples/14-taskspawner-persistent/
```

## Customization

- **`spec.sessionConfig.replicas`** — increase for parallel task processing
- **`spec.sessionConfig.idleTimeout`** — reduce to reclaim resources faster
  during quiet periods
- **`spec.sessionConfig.workspaceReset.preserveDirectories`** — keep expensive
  build artifacts between tasks (e.g., `node_modules`, `.venv`, `target`)
- **`spec.sessionConfig.storageSize`** — increase for large repositories
