# Project Conventions for AI Assistants

## Rules for AI Assistants
- **Use Makefile targets** instead of discovering build/test commands yourself.
- **Keep changes minimal.** Do not refactor, reorganize, or 'improve' code beyond what was explicitly requested.
- **For CI/release workflows**, always use existing Makefile targets rather than reimplementing build logic in YAML.
- **Better tests.** Always try to add or improve tests(including integration, e2e) when modifying code.
- **Logging conventions.** Start log messages with capital letters and do not end with punctuation.
- **Commit messages.** Do not include PR links in commit messages.
- **Kubernetes resource comparison.** Use semantic `.Equal()` or `.Cmp()` methods for `resource.Quantity` comparisons, not `reflect.DeepEqual` — structurally different Quantity values can be semantically identical (e.g., `1000m` vs `1` CPU).
- **Never use `os.Getenv()` for secrets as Go `flag` defaults.** Go's `flag` package prints default values in usage/help output, which leaks secret values. Instead, use an empty default and read the env var after `flag.Parse()`.
- **Fail fast on invalid configuration.** Do not silently fall back to degraded behavior (e.g., unauthenticated requests) when configuration or credentials are invalid or missing. Return an error or exit immediately instead of returning nil or empty values that mask the failure.

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
- If the PR introduces a new API field, CRD change, or user-facing feature, write a meaningful release note describing the change — do not write "NONE".
- PRs that only modify files under `self-development/` are internal agent improvements — use `/kind cleanup` and write "NONE" in the `release-note` block.

## Directory Structure
- `cmd/` — CLI entrypoints
- `test/e2e/` — end-to-end tests
- `.github/workflows/` — CI workflows

