---
name: kelos
description: >-
  Install and configure Kelos; author, document, inspect, troubleshoot, and
  operate its Kubernetes custom resources and CRD schemas in the kelos.dev API
  group using manifests, kubectl, or the kelos CLI. Use when a request names
  Kelos or a distinctive Kelos kind such as AgentConfig or TaskSpawner and
  concerns an installation, CLI command, API or CRD schema, manifest or example,
  resource-field documentation, or live custom resource. Do not use for ordinary
  code, test, build, review, CI, or Git work, even in the Kelos repository or on
  a service hosted under kelos.dev.
---

# Kelos Skill

Use this skill as a compact guide for Kelos resource work. Keep context small:
use the quick summary first, and read only the reference file that matches the
user's request.

## When To Use

Use this skill for:

- Authoring or editing Kelos manifests for `Task`, `Session`, `Workspace`, `AgentConfig`, or `TaskSpawner`.
- Debugging live Kelos resources on Kubernetes with `kelos` or `kubectl`.
- Operating Kelos installs, task runs, logs, suspension, deletion, or cluster resources.
- Editing Kelos CRD/API examples, generated CRDs, self-development manifests, or docs that describe resource fields.

Do not use this skill for ordinary Kelos repository code edits, tests, reviews,
CI fixes, or git work unless the request specifically involves Kelos resources,
CRDs, resource YAML, examples, self-development manifests, or cluster operations.

## Loading Rule

- For a simple CLI operation, use the quick commands below and do not open references.
- For one resource kind, open only that resource's reference file.
- For multi-resource manifests, open only the involved resource references.
- For live debugging, start with the operational workflow below, then open
  `references/troubleshooting.md` only if the symptom matches.
- For API or CRD source changes, verify current Go types and generated CRDs in
  the repo before trusting examples. Examples are patterns, not the source of truth.

## Resource Summary

Kelos resources use `apiVersion: kelos.dev/v1alpha2`.

| Resource | Purpose | Key fields |
| --- | --- | --- |
| `Task` | Single agent run | `spec.type`, `spec.prompt`, `spec.credentials`, `spec.workspaceRef`, `spec.agentConfigRefs[]`, `spec.branch`, `spec.dependsOn`, `spec.model`, `spec.effort`, `spec.podOverrides`, `spec.ttlSecondsAfterFinished` |
| `Session` | Persistent interactive agent conversation | `spec.worker`, `spec.volumeClaimTemplate` |
| `Workspace` | Git repository for the agent | `spec.repo`, `spec.ref`, `spec.secretRef`, `spec.remotes`, `spec.files` |
| `AgentConfig` | Reusable instructions and tools | `spec.agentsMD`, `spec.plugins`, `spec.skills`, `spec.mcpServers` |
| `TaskSpawner` | Creates Tasks from external sources | `spec.when.githubIssues`, `spec.when.githubPullRequests`, `spec.when.cron`, `spec.when.jira`, per-source `pollInterval`, `spec.taskTemplate`, `spec.maxConcurrency`, `spec.maxTotalTasks`, `spec.suspend` |

Task phases are `Pending`, `Waiting`, `Running`, `Succeeded`, and `Failed`.
Session phases are `Pending`, `Ready`, and `Failed`.

## References

| Need | Read |
| --- | --- |
| Task examples, credentials, dependencies, branch locks, pod overrides, TTL | `references/task.yaml` |
| Session examples, worker configuration, ephemeral or persistent storage | `references/session.yaml` |
| Workspace examples, repository auth, remotes, injected files | `references/workspace.yaml` |
| AgentConfig examples, plugins, skills, agents, MCP servers | `references/agentconfig.yaml` |
| TaskSpawner examples, GitHub/Jira/cron sources, comment policy, concurrency | `references/taskspawner.yaml` |
| CLI flags, config file, supported agent types, install/uninstall | `references/cli.md` |
| Pending/Waiting/Failed tasks, spawner issues, AgentConfig issues, push failures | `references/troubleshooting.md` |

## CLI Quick Commands

```bash
kelos install
kelos uninstall
kelos init

kelos run -p "Fix the login bug" --type claude-code
kelos run -p "Add tests" --workspace my-ws --agent-config my-ac
kelos run -p "Refactor auth" --model opus --effort high --branch feature/auth
kelos run -p "Fix bug" -w

kelos run --from taskspawner/daily-audit
kelos run --from taskspawner/issue-worker -f values.yaml

kelos create workspace my-ws --repo https://github.com/org/repo.git --ref main --secret github-token
kelos create agentconfig my-ac --skill review=@review.md --dry-run

kelos get tasks
kelos get sessions
kelos get task my-task -d
kelos get task my-task -o yaml
kelos get session my-session -d
kelos session connect my-session
kelos logs my-task -f

kelos suspend taskspawner my-spawner
kelos resume taskspawner my-spawner
kelos delete task my-task
kelos delete session my-session
```

## Operational Workflow

1. Identify the resource kind, name, and namespace before running broad queries.
2. Prefer scoped reads such as `kelos get task NAME -d`,
   `kelos logs NAME -f`, or `kubectl get task NAME -o yaml`.
3. Check whether referenced resources and Secrets exist before assuming a
   controller bug. Do not print Secret values.
4. Use full YAML or JSON when comparing resource state. Avoid relying only on
   table output for debugging.
5. If editing manifests in the Kelos repository, keep changes narrow and run
   the project's Makefile targets requested by the repo instructions.
