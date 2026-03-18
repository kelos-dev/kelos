# TaskSpawner Pipeline

This example demonstrates using `taskTemplates` (plural) to spawn a multi-step
pipeline of Tasks for each discovered work item.

## How it works

When a GitHub issue with the `agent-pipeline` label is discovered, the spawner
creates three Tasks per issue:

1. **plan** - Analyzes the issue and produces an implementation plan
2. **implement** - Implements the plan on a dedicated branch (depends on `plan`)
3. **open-pr** - Opens a pull request for the implementation (depends on `implement`)

Each step's `dependsOn` field references sibling step names. The spawner
translates these to fully-qualified task names at creation time:

```
dependsOn: [plan]  ->  dependsOn: [issue-pipeline-42-plan]
```

Steps execute in dependency order. Results from earlier steps are available
via the `{{.Deps}}` template variable.

## Key concepts

- **`maxConcurrency`** counts pipeline instances (not individual tasks)
- **`maxTotalTasks`** counts individual tasks created across all pipelines
- Each step independently configures `type`, `model`, `credentials`, `branch`, etc.
- DAG dependencies are supported (a step can depend on multiple predecessors)

## Prerequisites

- A `Workspace` named `my-workspace` with a GitHub repository
- A `Secret` named `claude-credentials` with OAuth credentials
