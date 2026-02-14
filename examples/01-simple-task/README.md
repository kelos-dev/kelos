# 01 — Simple Task

A minimal Task that runs an AI agent with an API key. No git workspace is
involved — the agent works in an empty directory inside an ephemeral Pod.

## Use Case

Run a one-off prompt (code generation, text analysis, etc.) without needing
access to a repository.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `secret.yaml` | Secret | Anthropic API key for the agent |
| `task.yaml` | Task | The prompt to execute |

## Steps

1. **Edit `secret.yaml`** — replace the placeholder with your real Anthropic API key.

2. **Apply the resources:**

```bash
kubectl apply -f examples/01-simple-task/
```

3. **Watch the Task:**

```bash
kubectl get tasks -w
```

4. **Stream the agent logs:**

```bash
axon logs simple-task -f
```

> **Tip:** You can also use `kubectl logs -l job-name=simple-task -f` for direct
> Kubernetes access.

5. **Cleanup:**

```bash
kubectl delete -f examples/01-simple-task/
```
