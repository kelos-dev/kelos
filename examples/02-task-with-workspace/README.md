# 02 — Task with Workspace

A Task that clones a git repository and can push branches or create pull
requests. This is the most common pattern for code-modification tasks.

## Use Case

Run an AI agent against an existing repository — fixing bugs, adding features,
writing tests, or creating PRs.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `workspace.yaml` | Workspace | Git repository to clone |
| `task.yaml` | Task | The prompt to execute |

## Steps

1. **Edit the secrets** — replace placeholders in both `github-token-secret.yaml`
   and `credentials-secret.yaml` with your real tokens.

2. **Edit `workspace.yaml`** — set your repository URL and branch.

3. **Apply the resources:**

```bash
kubectl apply -f examples/02-task-with-workspace/
```

4. **Watch the Task:**

```bash
axon get tasks -w
```

5. **Stream the agent logs:**

```bash
axon logs add-tests -f
```

> **Tip:** You can also use `kubectl logs -l job-name=add-tests -f` for direct
> Kubernetes access.

6. **Cleanup:**

```bash
kubectl delete -f examples/02-task-with-workspace/
```

## Notes

- The `GITHUB_TOKEN` is injected into the agent container, so the `gh` CLI
  and `git push` work out of the box.
- The agent runs with full autonomy inside the Pod — it can create branches,
  push commits, and open PRs.
