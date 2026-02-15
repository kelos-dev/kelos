# Axon Orchestration Examples

Ready-to-use patterns and YAML manifests for orchestrating AI agents with Axon. These examples demonstrate how to combine Tasks, Workspaces, and TaskSpawners into functional AI workflows.

## Prerequisites

- Kubernetes cluster (1.28+) with Axon installed (`axon install`)
- `kubectl` configured

## Examples

| Example | Description |
|---------|-------------|
| [01-simple-task](01-simple-task/) | Run a single Task with an API key, no git workspace |
| [02-task-with-workspace](02-task-with-workspace/) | Run a Task that clones a git repo and can create PRs |
| [03-taskspawner-github-issues](03-taskspawner-github-issues/) | Automatically create Tasks from labeled GitHub issues |
| [04-taskspawner-cron](04-taskspawner-cron/) | Run agent tasks on a cron schedule |
| [05-task-with-agentconfig](05-task-with-agentconfig/) | Inject reusable instructions and plugins via AgentConfig |

## How to Use

1. Pick an example directory.
2. Read its `README.md` for context.
3. Edit the YAML files and replace every `# TODO:` placeholder with your real values.
4. Apply the resources:

```bash
kubectl apply -f examples/<example-directory>/
```

5. Watch the Task progress:

```bash
axon get tasks -w
```

## Tips

- **Secrets first** — always create Secrets before the resources that reference them.
- **Namespace** — all examples use the `default` namespace. Change `metadata.namespace`
  if you use a different one.
- **Cleanup** — delete resources with `kubectl delete -f examples/<example-directory>/`.
  Owner references ensure that deleting a Task also cleans up its Job and Pod.
