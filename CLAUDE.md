# CLAUDE.md - Project Conventions for AI Assistants

## Rules for AI Assistants
- **Use Makefile targets** instead of discovering build/test commands yourself.
- **Keep changes minimal.** Do not refactor, reorganize, or 'improve' code beyond what was explicitly requested.
- **For CI/release workflows**, always use existing Makefile targets rather than reimplementing build logic in YAML.
- **Better tests.** Always try to add or improve tests(including integration, e2e) when modifying code.
- **Logging conventions.** Start log messages with capital letters and do not end with punctuation.
- **Commit messages.** Do not include PR links in commit messages.

## Key Makefile Targets
- `make verify` — run all verification checks (lint, fmt, vet, etc.), it will
fail if there is unstaged changes.
- `make update` — update all generated files
- tests:
  - `make test` — run all unit tests
  - `make test-integration` — run integration tests
  - `make test-e2e` — run end-to-end tests (use the cheapest model for cost savings)
- `make build` — build binary

## Directory Structure
- `cmd/` — CLI entrypoints
- `test/e2e/` — end-to-end tests
- `.github/workflows/` — CI workflows

