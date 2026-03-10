# Project Conventions for AI Assistants

## Rules for AI Assistants
- **Use Makefile targets** instead of discovering build/test commands yourself.
- **Keep changes minimal.** Do not refactor, reorganize, or 'improve' code beyond what was explicitly requested.
- **Stay within scope.** Only modify what the task explicitly asks for. Do not make changes to files or components outside the stated scope, even if they seem related.
- **For CI/release workflows**, always use existing Makefile targets rather than reimplementing build logic in YAML.
- **Better tests.** Always try to add or improve tests(including integration, e2e) when modifying code.
- **Use typed constants.** In Go tests, always use typed constants (e.g., `batchv1.PodFailurePolicyActionCount`) instead of string literals (e.g., `"Count"`). This ensures compile-time checking and consistency with production code.
- **Keep docs in sync.** When modifying configuration or removing/adding features, update related documentation (e.g., README files) in the same PR.
- **Logging conventions.** Start log messages with capital letters and do not end with punctuation.
- **Commit messages.** Do not include PR links in commit messages.

## Key Makefile Targets
- `make verify` — run all verification checks (lint, fmt, vet, etc.).
- `make update` — update all generated files
- tests:
  - `make test` — run all unit tests
  - `make test-integration` — run integration tests
  - e2e tests are hard to run locally. Push changes and use the PR's CI jobs to run them instead.
- `make build` — build binary

## Pull Requests
- **Always follow `.github/PULL_REQUEST_TEMPLATE.md`** when creating PRs.
- Fill in every section of the template. Do not remove or skip sections — use "N/A" or "NONE" where appropriate.
- Choose exactly one `/kind` label from: `bug`, `cleanup`, `docs`, `feature`.
- If there is no associated issue, write "N/A" under the issue section.
- If the PR does not introduce a user-facing change, write "NONE" in the `release-note` block.

## Directory Structure
- `cmd/` — CLI entrypoints
- `test/e2e/` — end-to-end tests
- `.github/workflows/` — CI workflows

