# Single-Branch Shallow Workspace Clones

## Problem

Kelos currently creates branch-backed Workspaces with `git clone --no-single-branch --depth 1`. Although history is shallow, Git fetches the tip of every branch. Repositories with large archival branches therefore make every new Task, WorkerPool, and Session download objects unrelated to the configured Workspace ref.

Kelos originally fetched every branch so a Task branch could be checked out. The current `branch-setup` init container already checks for and explicitly fetches the requested remote branch, so prefetching all branch tips is no longer necessary.

## Design

Branch-backed and default-branch Workspace clones will use explicit `--single-branch --depth 1`. When `Workspace.spec.ref` names a branch, `--branch <ref>` will continue selecting it. When the ref is omitted, Git will shallow-clone only the repository's default branch.

Full commit-SHA Workspaces will retain their existing `git init` plus targeted `fetch --depth 1` path. Task and Session initial branches will retain the existing `branch-setup` behavior, which fetches a matching remote branch on demand or creates a new local branch when no remote branch exists.

The change applies to both Job-backed Tasks and persistent WorkerPool/Session workspaces. It does not add a Workspace API field or change manifests.

## Compatibility

The configured Workspace branch and requested Task or Session branch remain available. Other unrelated remote-tracking branches will no longer be created during initial clone; an agent that needs another branch can fetch it explicitly.

## Tests

Controller unit tests will assert that both Job and WorkerPool clone init containers include `--single-branch --depth 1` and exclude `--no-single-branch`. Existing branch-setup tests will continue proving that requested task branches are fetched explicitly. Full-SHA checkout tests must remain unchanged and passing.

Focused controller tests, `make verify`, and the repository unit suite will be run. The pre-existing `TestClaudeEntrypointUsesPersistentSessionConfig` fixture failure will be reported separately if it remains the only unit-suite failure.
