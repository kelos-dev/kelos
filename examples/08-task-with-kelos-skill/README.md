# 08 — Task with Kelos Skill

Run a Task whose agent knows how to author and debug Kelos resources, powered
by the first-party Kelos skill.

## What This Demonstrates

- Injecting the Kelos skill via an AgentConfig plugin
- Giving the agent knowledge of Kelos CRDs, CLI, and troubleshooting

## Resources

| File | Resource | Purpose |
|------|----------|---------|
| `agentconfig.yaml` | AgentConfig | Defines the `kelos` plugin with the Kelos skill |
| `task.yaml` | Task | Runs an agent that uses the skill to set up a Kelos workflow |

## Prerequisites

1. A running Kubernetes cluster with Kelos installed (`kelos install`)
2. A Workspace resource named `my-workspace` pointing to your repo
3. A Secret named `claude-oauth-token` with OAuth credentials

## Usage

```bash
# Create the AgentConfig
kubectl apply -f examples/08-task-with-kelos-skill/agentconfig.yaml

# Run the Task
kubectl apply -f examples/08-task-with-kelos-skill/task.yaml

# Watch progress
kubectl get tasks -w
```

## Customizing

The skill content in `agentconfig.yaml` is a condensed version. For the full
packaged skill with complete reference YAML patterns, see `skills/kelos/` in
the repository root.

To use the full package from an AgentConfig, use the skills.sh package instead
of copying only `SKILL.md`; the package includes the reference files that the
skill loads on demand:

```bash
kelos create agentconfig kelos-skill-agent \
  --skills-sh kelos-dev/kelos
```

If you need a single-file inline skill, keep the condensed `content` in this
example or write a self-contained skill body that does not refer to
`skills/kelos/references/`.
