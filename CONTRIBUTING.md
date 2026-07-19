# Contributing to Kelos

Thank you for helping improve Kelos. Contributions can include bug fixes,
features, tests, documentation, examples, and issue reports.

## Before You Start

- Search [existing issues](https://github.com/kelos-dev/kelos/issues) before
  opening a new one.
- For a significant change, open an issue first so the approach and API can be
  discussed before implementation.
- Keep each change focused. Avoid unrelated refactoring in the same pull
  request.

## Development Setup

You need:

- Git
- The Go version declared in [`go.mod`](go.mod)
- Make
- Docker only when building container images
- A Kubernetes cluster and agent credentials only when running end-to-end tests

Fork the repository, clone your fork, and create a branch from the latest
`main`:

```bash
git clone https://github.com/<your-user>/kelos.git
cd kelos
git remote add upstream https://github.com/kelos-dev/kelos.git
git fetch upstream
git switch -c <branch-name> upstream/main
```

The Makefile installs its development tools into `bin/` as needed.

## Build and Test

Use the repository's Make targets for development and CI checks:

| Command | Purpose |
| --- | --- |
| `make update` | Regenerate code, CRDs, and manifests; format Go, YAML, and shell files; and tidy Go modules |
| `make verify` | Check generated files, formatting, Go modules, and `go vet` without retaining generated changes |
| `make test` | Run unit tests |
| `make test-integration` | Run integration tests with envtest |
| `make build` | Build the binaries into `bin/` |
| `make test-e2e` | Run end-to-end tests against a configured cluster |

Run `make update` after changing Go APIs, dependencies, YAML, or shell scripts,
and commit any resulting files. Do not edit generated files directly. For code
changes, run the standard local checks before submitting a pull request:

```bash
make update
make verify
make test
make test-integration
make build
```

For documentation-only changes, `make verify` is sufficient. If an integration
test is not relevant to your change or cannot run in your environment, explain
that in the pull request.

End-to-end tests require a Kubernetes cluster and agent credentials. Run them
when your environment is configured for them; otherwise, describe the tests
you ran in the pull request.

## Making Changes

- Follow the style and patterns in the surrounding code.
- Add or improve tests for behavior you change. Cover the successful path as
  well as relevant error paths.
- Keep log messages capitalized and without trailing punctuation.
- Include the resource name in CLI errors about a named resource.
- Update documentation and examples when behavior, configuration, or project
  structure changes. Document only behavior that is implemented.
- Do not put secret environment variable values into Go `flag` defaults,
  because defaults are printed in help output.
- Compare Kubernetes `resource.Quantity` values with their semantic `Equal` or
  `Cmp` methods rather than structural equality.

### Kubernetes API Changes

Kubernetes APIs are compatibility contracts and require extra care:

- Add new capabilities only to the latest served/storage API version (currently
  `api/v1alpha2`). Keep older versions as compatibility surfaces.
- Preserve compatibility with existing manifests. Do not change an existing
  field's kind or add validation that rejects values previously accepted.
- Deprecate and retain a replaced field instead of removing it.
- Keep new API surfaces to the minimum needed for the current change.
- Update conversion logic and tests when necessary.
- Run `make update` and commit all generated CRDs, clients, and manifests.
- Update affected files under `examples/`, `self-development/`, and `docs/`.

## Commits and Pull Requests

Write clear commit messages and do not include pull request links in commit
messages. Rebase onto the latest `upstream/main` before submitting when needed;
do not add merge commits to your branch.

Every pull request must follow
[the pull request template](.github/PULL_REQUEST_TEMPLATE.md):

- Fill in every section; use `N/A` where appropriate.
- Choose exactly one kind: `/kind bug`, `/kind cleanup`, `/kind docs`, or
  `/kind feature`.
- Link the associated issue, or write `N/A` if there is none.
- Add a meaningful release note for a user-facing change. Write `NONE` in the
  `release-note` block when there is no user-facing change.
- Explain what changed, why it is needed, and how it was tested.
- Keep generated files, tests, examples, and documentation in the same pull
  request as the change that requires them.

### Squash Commits

After review and before merge, clean up the branch so every remaining commit is
a meaningful milestone or logical unit of work. Squash commits that only
contain review fixes, typo corrections, work in progress, merges, or rebases.
A pull request containing one logical change should usually end with one
commit.

Use an interactive rebase to combine commits, then safely update your remote
branch:

```bash
git fetch upstream
git rebase -i upstream/main
git push --force-with-lease
```

In the interactive rebase, leave `pick` on commits that should remain and use
`squash` or `fixup` on follow-up commits that belong with the preceding commit.
Use `--force-with-lease`, not `--force`, so the push fails rather than
overwriting remote work you do not have locally. For more detail, see the
[Kubernetes contributor guide to squashing commits](https://www.kubernetes.dev/docs/guide/github-workflow/#squash-commits).

CI runs the build, verification, unit, integration, and applicable end-to-end
checks. Please address failures and reviewer feedback with focused follow-up
commits.

## License

By contributing, you agree that your contributions will be licensed under the
[Apache License 2.0](LICENSE).
