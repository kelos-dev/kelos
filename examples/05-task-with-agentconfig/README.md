# 05 — Task with AgentConfig

A Task that uses an AgentConfig to inject reusable instructions and plugins
into the agent container. AgentConfig lets you define shared guidelines and
skills once, then reference them from any number of Tasks.

## Use Case

Standardize agent behavior across multiple Tasks — for example, enforcing
code review guidelines, security policies, or team coding standards — without
duplicating instructions in every Task prompt.

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `github-token-secret.yaml` | Secret | GitHub token for cloning and PR creation |
| `credentials-secret.yaml` | Secret | Claude OAuth token for the agent |
| `workspace.yaml` | Workspace | Git repository to clone |
| `agentconfig.yaml` | AgentConfig | Shared instructions (`agentsMD`) and plugins |
| `task.yaml` | Task | The prompt to execute, referencing the AgentConfig |

## How AgentConfig Works

- **`agentsMD`** is written to the agent's instruction file (e.g.,
  `~/.claude/CLAUDE.md` for Claude Code) before the agent starts. This is
  additive and does not overwrite the repo's own instruction files.
- **`plugins`** are mounted as plugin directories and passed via `--plugin-dir`.
  Each plugin can define skills (slash commands) and agents (sub-agents). Only
  applicable to `claude-code` type agents.

## Steps

1. **Edit the secrets** — replace placeholders in both `github-token-secret.yaml`
   and `credentials-secret.yaml` with your real tokens.

2. **Edit `workspace.yaml`** — set your repository URL and branch.

3. **Review `agentconfig.yaml`** — customize the review guidelines and plugin
   content for your project.

4. **Apply the resources:**

```bash
kubectl apply -f examples/05-task-with-agentconfig/
```

5. **Watch the Task:**

```bash
axon get tasks -w
```

6. **Stream the agent logs:**

```bash
axon logs review-pr -f
```

7. **Cleanup:**

```bash
kubectl delete -f examples/05-task-with-agentconfig/
```

## Notes

- The AgentConfig must exist in the same namespace as the Task. If the
  AgentConfig is not found when the Task starts, the controller will retry
  until it becomes available.
- You can reuse the same AgentConfig across multiple Tasks by setting
  `agentConfigRef.name` in each Task spec.
- Plugins are only supported for `claude-code` type agents. Other agent types
  will use `agentsMD` but ignore the `plugins` field.
