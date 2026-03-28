# Project Conventions for AI Assistants

## Rules for AI Assistants
- **Use Makefile targets** instead of discovering build/test commands yourself.
- **Keep changes minimal.** Do not refactor, reorganize, or 'improve' code beyond what was explicitly requested.
- **For CI/release workflows**, always use existing Makefile targets rather than reimplementing build logic in YAML.
- **Better tests.** Always try to add or improve tests(including integration, e2e) when modifying code.
- **Logging conventions.** Start log messages with capital letters and do not end with punctuation.
- **Commit messages.** Do not include PR links in commit messages.
- **Kubernetes resource comparison.** Use semantic `.Equal()` or `.Cmp()` methods for `resource.Quantity` comparisons, not `reflect.DeepEqual` — structurally different Quantity values can be semantically identical (e.g., `1000m` vs `1` CPU).
- **Consistent guidance across surfaces.** When changing user-facing guidance or instructions, search for all surfaces that reference the same topic (docs, CLI output, templates, code comments) and update them together.
- **Provider-agnostic API design.** When adding CRD fields or API types, prefer generic, provider-agnostic abstractions over provider-specific ones (e.g., a generic `none` credential type instead of `bedrock`). Provider-specific details should be handled through existing generic mechanisms like `PodOverrides.Env` rather than dedicated fields.
- **Idiomatic Helm values.** Use nested dictionary structures in `values.yaml` (e.g., `controller.resources.requests.cpu`) instead of flat string-based parameters. Values that represent structured data should be proper YAML dictionaries, not strings.
- **Deploy-dev workflow sync.** When adding new controller flags, resource settings, or monitoring config, update `.github/workflows/deploy-dev.yaml` to pass the corresponding values.
- **Controller-driven migration.** When introducing breaking changes to deployed resources (e.g., label selector changes), prefer automated migration in the controller over requiring manual user intervention. Add a FIXME comment noting when the migration code can be removed.

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
- If the PR introduces a new API field, CRD change, or user-facing feature, write a meaningful release note describing the change — do not write "NONE". If the change affects existing deployments (e.g., selector changes, breaking migrations), the release note must describe the required user action.
- PRs that only modify files under `self-development/` are internal agent improvements — use `/kind cleanup` and write "NONE" in the `release-note` block.

## Directory Structure
- `cmd/` — CLI entrypoints
- `test/e2e/` — end-to-end tests
- `.github/workflows/` — CI workflows

