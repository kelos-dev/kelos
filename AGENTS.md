# Project Conventions for AI Assistants

## Rules for AI Assistants
- **Use Makefile targets** instead of discovering build/test commands yourself.
- **Keep changes minimal.** Do not refactor, reorganize, or 'improve' code beyond what was explicitly requested.
- **For CI/release workflows**, always use existing Makefile targets rather than reimplementing build logic in YAML.
- **Better tests.** Always try to add or improve tests(including integration, e2e) when modifying code.
- **Logging conventions.** Start log messages with capital letters and do not end with punctuation.
- **Commit messages.** Do not include PR links in commit messages.
- **API deprecation notices.** Do not hard-code future API version names (e.g., "will be removed in v1alpha2") — the next version is not yet determined.
- **New API fields/features.** When adding new API fields or features, always update `self-development/` configs and `examples/` to use the newly added fields.

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
- Choose exactly one `/kind` label from: `bug`, `cleanup`, `docs`, `feature`. The `/kind <label>` line in the PR body automatically applies the label — do not pass redundant `--label kind/...` flags to `gh pr create`.
- If there is no associated issue, write "N/A" under the issue section.
- If the PR does not introduce a user-facing change, write "NONE" in the `release-note` block.
- If the PR introduces a new API field, CRD change, or user-facing feature, write a meaningful release note describing the change — do not write "NONE".
- PRs that only modify files under `self-development/` are internal agent improvements — use `/kind cleanup` and write "NONE" in the `release-note` block.

## Directory Structure
- `cmd/` — CLI entrypoints
- `test/e2e/` — end-to-end tests
- `.github/workflows/` — CI workflows

