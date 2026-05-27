---
name: kelos
description: Author, debug, and operate Kelos resources (Task, Workspace, AgentConfig, TaskSpawner) on Kubernetes. Use when working with Kelos CRDs or the kelos CLI.
compatibility: Requires kubectl and access to a Kubernetes cluster
---

# Kelos Skill

Use this skill when you need to author, debug, or operate Kelos resources
(Task, Workspace, AgentConfig, TaskSpawner) on a Kubernetes cluster.

## Installing Kelos

Install the controller and CRDs into a Kubernetes cluster:

```bash
kelos install
```

Uninstall:

```bash
kelos uninstall
```

Initialize a local config file at `~/.kelos/config.yaml`:

```bash
kelos init
```

## Core Resources

Kelos defines four custom resources:

| Resource | Purpose |
|----------|---------|
| **Task** | A single agent run — prompt, credentials, optional workspace and config |
| **Workspace** | A git repository to clone for the agent |
| **AgentConfig** | Reusable instructions, skills, agents, MCP servers |
| **TaskSpawner** | Automatically creates Tasks from GitHub issues, Jira tickets, or cron |

### Task

A Task runs an AI agent with a prompt. Key fields:

- `spec.type` (required): `claude-code`, `codex`, `gemini`, `opencode`, `cursor`, or `antigravity`
- `spec.prompt` (required): The task prompt
- `spec.credentials` (required): `type` (`api-key` or `oauth`) and `secretRef.name`
- `spec.workspaceRef.name`: Reference to a Workspace
- `spec.agentConfigRef.name`: Reference to an AgentConfig
- `spec.branch`: Git branch mutex — only one Task with the same branch runs at a time
- `spec.dependsOn`: Task names that must succeed first
- `spec.ttlSecondsAfterFinished`: Auto-delete after completion (seconds)
- `spec.model`: Model override
- `spec.podOverrides`: Resource limits, timeout, env vars, node selector

Task status phases: `Pending` -> `Running` -> `Succeeded` or `Failed`.
Tasks with unmet dependencies enter `Waiting`.

### Workspace

A Workspace defines a git repository for the agent:

- `spec.repo` (required): Git URL (HTTPS, git://, or SSH)
- `spec.ref`: Branch, tag, or commit to checkout
- `spec.secretRef.name`: Secret with `GITHUB_TOKEN` (PAT) or GitHub App credentials (`appID`, `installationID`, `privateKey`)
- `spec.remotes`: Additional git remotes (name must not be `origin`)
- `spec.files`: Files to inject into the repo before the agent starts (e.g., `CLAUDE.md`, skills)

### AgentConfig

An AgentConfig injects reusable instructions and tools into Tasks:

- `spec.agentsMD`: Instructions written to the agent's config (e.g., `~/.claude/CLAUDE.md`). Additive — does not overwrite repo files
- `spec.plugins`: Plugin bundles with skills and sub-agents
  - `plugins[].name`: Plugin name (directory namespace)
  - `plugins[].skills[].name` / `.content`: Skill definitions (become `SKILL.md`)
  - `plugins[].agents[].name` / `.content`: Agent definitions (become `<name>.md`)
- `spec.skills`: skills.sh ecosystem packages
  - `skills[].source`: Package in `owner/repo` format
  - `skills[].skill`: Optional specific skill name
- `spec.mcpServers`: MCP server configurations
  - Supports `stdio`, `http`, and `sse` transport types
  - Use `headersFrom` / `envFrom` with a `secretRef` for sensitive values

### TaskSpawner

A TaskSpawner auto-creates Tasks from external sources:

- `spec.when.githubIssues`: Discover from GitHub issues (labels, state, assignee, author, commentPolicy, priority labels)
- `spec.when.githubPullRequests`: Discover from GitHub PRs (labels, state, reviewState, author, draft, commentPolicy, priority labels)
- `spec.when.cron`: Trigger on a cron schedule
- `spec.when.jira`: Discover from Jira (project, JQL filter, secret with `JIRA_TOKEN`)
- `spec.when.githubIssues.commentPolicy` / `spec.when.githubPullRequests.commentPolicy`: Comment-based workflow control with authorization
  - `triggerComment`: Command that must appear for the item to be included (e.g., "/kelos pick-up")
  - `excludeComments`: Commands that exclude items; when combined with triggerComment, the most recent authorized command wins
  - `allowedUsers`: Restrict comment control to specific GitHub usernames
  - `allowedTeams`: Restrict to GitHub teams in `org/team-slug` format
  - `minimumPermission`: Require at least this repo permission (`read`, `triage`, `write`, `maintain`, `admin`)
- `spec.taskTemplate`: Template for spawned Tasks (same fields as Task spec)
  - `promptTemplate` and `branch` support Go `text/template` variables: `{{.ID}}`, `{{.Number}}`, `{{.Title}}`, `{{.Body}}`, `{{.URL}}`, `{{.Labels}}`, `{{.Comments}}`, `{{.Kind}}`, `{{.Time}}`, `{{.Schedule}}`
- `spec.pollInterval`: Polling frequency (default `5m`)
- `spec.maxConcurrency`: Limit concurrent running Tasks
- `spec.maxTotalTasks`: Lifetime task creation limit
- `spec.suspend`: Pause/resume without deleting

## CLI Quick Reference

### Running Tasks

```bash
# Simple task
kelos run -p "Fix the login bug" --type claude-code

# With workspace and agent config
kelos run -p "Add tests" --workspace my-ws --agent-config my-ac

# With model override and branch
kelos run -p "Refactor auth" --model opus --branch feature/auth

# Watch task progress
kelos run -p "Fix bug" -w
```

### Creating Resources

```bash
# Create a workspace
kelos create workspace my-ws \
  --repo https://github.com/org/repo.git \
  --ref main \
  --secret github-token

# Create an agent config with inline skill
kelos create agentconfig my-ac \
  --skill review="Review the PR for correctness and security" \
  --agents-md @instructions.md

# Create an agent config with skills.sh package
kelos create agentconfig my-ac \
  --skills-sh anthropics/skills:skill-creator

# Create an agent config with MCP server
kelos create agentconfig my-ac \
  --mcp github='{"type":"http","url":"https://api.githubcopilot.com/mcp/"}'

# Dry-run to preview YAML
kelos create agentconfig my-ac --skill review=@review.md --dry-run
```

### Managing Resources

```bash
# List resources
kelos get tasks
kelos get taskspawners
kelos get workspaces

# View details
kelos get task my-task -d
kelos get task my-task -o yaml

# Stream logs
kelos logs my-task -f

# Suspend / resume a spawner
kelos suspend taskspawner my-spawner
kelos resume taskspawner my-spawner

# Delete
kelos delete task my-task
```

### Configuration

Config file at `~/.kelos/config.yaml`:

```yaml
oauthToken: <token>       # or apiKey: <key>
model: sonnet                   # or a versioned ID like 'claude-sonnet-4-6' — value is passed to the agent as KELOS_MODEL
namespace: default
workspace:
  repo: https://github.com/org/repo.git
  ref: main
  token: <github-token>
```

CLI flags always override config file values.

## Dependency Chains

Tasks can depend on other Tasks using `dependsOn`. Dependent tasks access
upstream results via Go template syntax in the prompt:

```yaml
dependsOn: [scaffold]
prompt: |
  Code is on branch {{index .Deps "scaffold" "Results" "branch"}}.
  PR: {{index .Deps "scaffold" "Results" "pr"}}
```

Available result keys: `branch`, `commit`, `base-branch`, `pr`, `input-tokens`, `output-tokens`, `cost-usd`.

## Troubleshooting

### Task stuck in Pending
- Check if the credentials secret exists: `kubectl get secret <name>`
- Check controller logs: `kubectl logs deployment/kelos-controller-manager -n kelos-system`

### Task stuck in Waiting
- Check if a dependency in `dependsOn` has not yet succeeded
- Check if another Task holds the branch lock (same `spec.branch`)

### Task fails immediately
- Verify agent credentials are valid
- Check the workspace repository is accessible
- Review pod logs: `kelos logs <task-name>` or `kubectl logs -l job-name=<job-name>`

### TaskSpawner not creating Tasks
- Check spawner status: `kubectl get taskspawner <name> -o yaml`
- Verify the Workspace exists: `kubectl get workspace`
- Check if `maxConcurrency` is reached (active tasks at limit)
- Check if `maxTotalTasks` limit is reached
- Check if `suspend: true` is set

### AgentConfig not taking effect
- Verify the Task references it: `spec.agentConfigRef.name` must match
- Check plugin structure: skills become `<plugin>/skills/<skill>/SKILL.md`
- For skills.sh: ensure the package source is valid `owner/repo` format

### Agent cannot push or create PRs
- Ensure the workspace secret has a valid `GITHUB_TOKEN`
- Verify the token has `repo` (and `workflow` if needed) permissions
- For GitHub Apps, check that `appID`, `installationID`, and `privateKey` are correct

## Supported Agent Types

| Type | CLI | Credential Env Var |
|------|-----|--------------------|
| `claude-code` | `claude` | `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` |
| `codex` | `codex` | `CODEX_API_KEY` or `CODEX_AUTH_JSON` |
| `gemini` | `gemini` | `GEMINI_API_KEY` |
| `opencode` | `opencode` | `OPENCODE_API_KEY` |
| `cursor` | `agent` (Cursor) | `CURSOR_API_KEY` |
| `antigravity` | `agy` (Google Antigravity) | n/a (only `credentials.type: none` supported; supply credentials via `podOverrides.env`) |

## References

See the `references/` directory next to this file for complete YAML examples:

- `task.yaml` — Task patterns
- `workspace.yaml` — Workspace patterns
- `agentconfig.yaml` — AgentConfig patterns
- `taskspawner.yaml` — TaskSpawner patterns
