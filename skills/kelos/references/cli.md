# Kelos CLI Reference

Read this only when the task needs CLI flags, configuration, installation, or
supported agent types.

## Install And Configure

```bash
kelos install
kelos uninstall
kelos init
```

The default config file is `~/.kelos/config.yaml`:

```yaml
oauthToken: <token>
model: sonnet
effort: high
namespace: default
workspace:
  repo: https://github.com/org/repo.git
  ref: main
  token: <github-token>
```

Use `apiKey` instead of `oauthToken` when the agent uses API-key
authentication. CLI flags override config file values. The model value is
passed to the agent as `KELOS_MODEL`.

## Running Tasks

```bash
kelos run -p "Fix the login bug" --type claude-code
kelos run -p "Add tests" --workspace my-ws --agent-config my-ac
kelos run -p "Refactor auth" --model opus --effort high --branch feature/auth
kelos run -p "Fix bug" -w
```

## Creating Resources

```bash
kelos create workspace my-ws \
  --repo https://github.com/org/repo.git \
  --ref main \
  --secret github-token

kelos create agentconfig my-ac \
  --skill review="Review the PR for correctness and security" \
  --agents-md @instructions.md

kelos create agentconfig my-ac \
  --skills-sh anthropics/skills:skill-creator

kelos create agentconfig my-ac \
  --mcp github='{"type":"http","url":"https://api.githubcopilot.com/mcp/"}'

kelos create agentconfig my-ac --skill review=@review.md --dry-run
```

## Managing Resources

```bash
kelos get tasks
kelos get taskspawners
kelos get workspaces

kelos get task my-task -d
kelos get task my-task -o yaml
kelos logs my-task -f

kelos suspend taskspawner my-spawner
kelos resume taskspawner my-spawner
kelos delete task my-task
```

## Supported Agent Types

| Type | CLI | Credential Env Var |
| --- | --- | --- |
| `claude-code` | `claude` | `ANTHROPIC_API_KEY` or `CLAUDE_CODE_OAUTH_TOKEN` |
| `codex` | `codex` | `CODEX_API_KEY` or `CODEX_AUTH_JSON` |
| `gemini` | `gemini` | `GEMINI_API_KEY` |
| `opencode` | `opencode` | `OPENCODE_API_KEY` |
| `cursor` | `agent` (Cursor) | `CURSOR_API_KEY` |

## Task Dependency Templates

Tasks can depend on other Tasks with `dependsOn`. Dependent task prompts can
access upstream results with Go template syntax:

```yaml
dependsOn: [scaffold]
prompt: |
  Code is on branch {{index .Deps "scaffold" "Results" "branch"}}.
  PR: {{index .Deps "scaffold" "Results" "pr"}}
```

Available result keys include `branch`, `commit`, `base-branch`, `pr`,
`input-tokens`, `output-tokens`, and `cost-usd`.
