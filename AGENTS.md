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
- **Keep API surfaces minimal.** When adding new API fields, types, or CRD changes, include only what is immediately needed. Do not add speculative fields — API is hard to change once shipped. Start with the minimum viable API and extend in follow-up PRs.
- **API changes must preserve backward compatibility for existing manifests.** Existing in-cluster resources must continue to apply after a CRD update. Do not change a field's kind (scalar ↔ array, string ↔ object) on an existing field; do not add `MinLength`, `Required`, or other tightening validation to a field that previously accepted absence/empty values; when replacing a field, mark the old one `+deprecated` and keep it functional rather than removing it. When the schema must change, sweep `examples/`, `self-development/`, and any in-tree YAMLs that use the old form and update them in the same PR.
- **Docs must match implementation, not aspiration.** When writing or updating docs, READMEs, or comments, describe only what the code actually does. Do not document unimplemented behavior, overstate guarantees, or describe security checks (e.g., HMAC validation) that aren't enforced. Before describing a contract ("X is filtered", "Y is validated"), verify the code enforces it — partial enforcement should be documented as partial.
- **Do not use Gomega's global `Expect()` inside `Eventually` polling blocks.** Gomega does not retry on `Expect` failures — a transient API error short-circuits the poller and fails the test on the first blip instead of retrying. In e2e `WaitFor*` helpers, either inline the call and return a zero-value on error, or use the `Eventually(func(g Gomega) { ... })` form so failed assertions are caught and retried.
- **CLI error messages must name the resource.** When a CLI command fails for a named resource (task, spawner, workspace, etc.), include the resource name in the returned error so operators piping or batching invocations get an actionable signal. `fmt.Errorf("task %s failed", name)`, not `errors.New("task failed")`.
- **Test the happy path, not only the early-return guards.** When a handler has both early-return guards and a primary action (post a message, create a resource, emit a metric), unit tests must include at least one positive case verifying the primary action runs with the right arguments — not just the no-op branches. If the production code lacks a seam, add one (interface, function field, fake client) so the happy path is covered.
- **Avoid vacuous substring assertions in printer/formatter tests.** When asserting a `label: value` line is emitted, match against the full `"label: value"` string (or a regex), not the bare value — bare values often collide with the resource's `Name` or other surrounding context in the fixture and pass even when the line is missing.
- **Keep CRD enum docstrings consistent with the `+kubebuilder:validation:Enum` marker.** If the godoc says "empty matches both", either include `""` in the enum list so `field: ""` is accepted, or rephrase to "Omit to match both" so no one writes the explicit empty form. A docstring that invites a value the API server then rejects is a worse contract than either alternative.
- **Qualify cross-CRD field references with the owning kind in docs.** In a CRD reference section, write `Task.spec.podOverrides.env` rather than bare `podOverrides.env` when describing a field that lives on a sibling CRD. A reader of one CRD's reference page should be able to locate the cited field without already knowing the layout of the others.

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

