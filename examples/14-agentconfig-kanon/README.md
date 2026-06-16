# 14 — AgentConfig from Kanon

Run a Task whose reusable agent settings come from a Kanon repository. Kelos
clones the repository, then the Codex or Claude Code entrypoint runs Kanon
before starting the agent.

## What This Demonstrates

- Referencing `AgentConfig.spec.kanon`
- Loading user-level agent settings from a repository with `kanon.yaml` at the root
- Keeping Kanon-backed config separate from inline AgentConfig fields

## Resources

| File | Kind | Purpose |
|------|------|---------|
| `credentials-secret.yaml` | Secret | Codex API key for the agent |
| `agentconfig.yaml` | AgentConfig | Points Kelos at the Kanon source repository |
| `task.yaml` | Task | Runs Codex with the Kanon-backed AgentConfig |

## Usage

1. Edit `credentials-secret.yaml` and set `CODEX_API_KEY`.
2. Edit `agentconfig.yaml` and set `spec.kanon.repo` to your Kanon repository.
   Set `spec.kanon.ref` to a branch, tag, or commit.
3. Apply the resources:

```bash
kubectl apply -f examples/14-agentconfig-kanon/
```

4. Watch the Task:

```bash
kubectl get tasks -w
```

5. Stream the logs:

```bash
kelos logs kanon-configured-task -f
```

## Private Repositories

For a private Kanon repository, create a Secret with a `GITHUB_TOKEN` key and
reference it from `spec.kanon.secretRef.name`.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kanon-github-token
type: Opaque
stringData:
  GITHUB_TOKEN: "ghp_REPLACE-ME"
```

## Notes

- `spec.kanon` is supported for Codex and Claude Code Tasks.
- `spec.kanon` cannot be combined with inline `agentsMD`, `plugins`, `skills`,
  or `mcpServers` fields.
- A Task can resolve at most one Kanon-backed AgentConfig.
