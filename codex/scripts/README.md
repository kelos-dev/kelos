# Codex agent — helper binaries

These scripts are baked into the codex agent image and live on `$PATH`
inside the running container. They are invoked from
`codex/kelos_entrypoint.sh` (via `kelos-agent-setup`) or from the agent
itself once it is running.

## Env-var contract

| Variable | Provided by | Used by | Purpose |
| --- | --- | --- | --- |
| `CODY_TOOLS_GITHUB_BASE_URL` | Task `podOverrides.env` (literal) | `cody-github-token`, `cody-github-credential-helper`, `gh`, `npm`, `pnpm` | Base URL for the cody-tools GitHub broker, for example `http://cody-tools.kelos-system.svc.cluster.local:8080/github`. GitHub App private key material stays in `cody-tools`, not in the task pod. |
| `KUBERNETES_CLUSTER_NAME` | Task `podOverrides.env` (literal) | `kelos-agent-setup` | Optional human-readable cluster name baked into `~/.kube/config`. Defaults to `in-cluster`. |
| `GIT_AUTHOR_NAME` | Task `podOverrides.env` (literal) | `kelos-agent-setup` | Sets `git config user.name`. Without it `git commit` aborts. Defaults to `Cody (Alpheya)`. |
| `GIT_AUTHOR_EMAIL` | Task `podOverrides.env` (literal) | `kelos-agent-setup` | Sets `git config user.email`. Defaults to `cody@alpheya.com`. |
| `NODE_AUTH_TOKEN` | Derived at call time by `npm` / `pnpm` wrappers | `npm`, `pnpm` (wrappers) | Populated from the cody-tools GitHub broker so `.npmrc` lines like `_authToken=${NODE_AUTH_TOKEN}` against `npm.pkg.github.com` resolve to a fresh installation token. Pre-set values are honored (operator override). Requires `packages: read` on the GitHub App installation. |
| `ALPHEYA_TOKEN_SIGNING_KEY` | Task `podOverrides.env` (Secret) | `kelos-jwt`, `curl` (wrapper) | PEM (RS256) or HMAC bytes (HS256). Literal `\n` from sealed-secret env vars is unescaped before signing. |
| `ALPHEYA_TOKEN_SIGNING_KEY_FILE` | Task `podOverrides.env` (literal path) | `kelos-jwt`, `curl` (wrapper) | Optional fallback when `KEY` is unset. Useful when the secret is mounted as a file. |
| `ALPHEYA_TOKEN_SIGNING_ALGORITHM` | Task `podOverrides.env` (literal) | `kelos-jwt`, `curl` (wrapper) | `RS256` (default) or `HS256`. |
| `ALPHEYA_TOKEN_SIGNING_ISSUER` | Task `podOverrides.env` (literal) | `kelos-jwt`, `curl` (wrapper) | JWT `iss` claim. Required. |
| `ALPHEYA_TOKEN_SIGNING_AUDIENCE` | Task `podOverrides.env` (literal) | `kelos-jwt`, `curl` (wrapper) | JWT `aud` claim. Optional but required in practice for any Alpheya service that validates audience (`alpheya` in non-prod). |
| `ALPHEYA_TOKEN_SIGNING_KEY_ID` | Task `podOverrides.env` (literal) | `kelos-jwt`, `curl` (wrapper) | Optional JWT header `kid` for key rotation. |
| `ALPHEYA_TOKEN_SIGNING_EXPIRES_IN` | Task `podOverrides.env` (literal) | `kelos-jwt`, `curl` (wrapper) | TTL in seconds. Default `3600`. Range `[60, 86400]`. |
| `ALPHEYA_TOKEN_SIGNING_DEFAULT_CLAIMS` | Task `podOverrides.env` (literal JSON) | `kelos-jwt`, `curl` (wrapper) | `{"sub":...,"roles":[...],"email"?:...,"name"?:...,"ext"?:{...}}`. Required. The optional `ext` object is emitted as a nested claim verbatim (matches oauth2-proxy's token shape). |
| `ALPHEYA_TOKEN_SIGNING_PROFILES` | Task `podOverrides.env` (literal JSON) | `kelos-jwt`, `curl` (wrapper) | Optional `{profileName: claims}` map for per-request identity. |
| `ALPHEYA_TOKEN_PROFILE` | Per-call env (literal) | `curl` (wrapper) | Optional. Appended as `:profile` to the resolved service before signing, for one-off privilege bumps. |

The GitHub App credentials are configured on `cody-tools` through
`CODY_TOOLS_GITHUB_APP_CLIENT_ID`,
`CODY_TOOLS_GITHUB_APP_INSTALLATION_ID`, and
`CODY_TOOLS_GITHUB_APP_PRIVATE_KEY`. They must not be injected into task pods.

## Binaries

- **`kelos-agent-setup`** — Pre-agent setup invoked from `kelos_entrypoint.sh`. Wires `git config credential.helper` to the cody-tools-backed helper when `CODY_TOOLS_GITHUB_BASE_URL` is set, and synthesises `~/.kube/config` from the projected ServiceAccount token. Each step is a no-op when its inputs are missing, so this script is safe to run unconditionally.
- **`cody-github-credential-helper`** — Git credential helper. Reads the credential request on stdin and, for `github.com` / `api.github.com` over HTTPS, returns a fresh brokered installation token as the password. Returns nothing for other hosts so git falls through to its other helpers.
- **`github-app-credential-helper`** — Existing command name that execs `cody-github-credential-helper`. It does not sign JWTs locally.
- **`cody-github-token`** — Requests a short-lived GitHub App installation token from `cody-tools` and prints it to stdout. It requires `CODY_TOOLS_GITHUB_BASE_URL`.
- **`github-app-token`** — Existing command name that execs `cody-github-token`. It does not sign JWTs locally.
- **`gh`** — Wrapper at `/usr/local/bin/gh` (ahead of `/usr/bin/gh` in `PATH`) that requests an installation token from `cody-tools` and exports it as `GH_TOKEN` before exec'ing the real `gh`. Lets every `gh` invocation use App auth without per-call plumbing. Defers to a pre-set `GH_TOKEN` / `GITHUB_TOKEN` when one is already in the env.
- **`npm`** — Wrapper at `/usr/local/bin/npm` (ahead of `/usr/bin/npm` in `PATH`) that requests an installation token from `cody-tools` and exports it as `NODE_AUTH_TOKEN` before exec'ing the real `npm`. Repos like `notification-service` ship `.npmrc` with `//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}` for the `@quantum-wealth` scope, so `npm ci` resolves private packages without per-call plumbing. Defers to a pre-set `NODE_AUTH_TOKEN` when one is already in the env. Requires `packages: read` on the App installation; without it the first private-package fetch returns `npm ERR! 401 Unauthorized`.
- **`pnpm`** — Wrapper at `/usr/local/bin/pnpm`, identical pattern to the `npm` wrapper. Needed because `pnpm` does not shell out to `npm` — it reads `.npmrc` and the env directly, so the npm wrapper does not cover `pnpm install` invocations on its own. Same env contract, same passthrough rules, same `packages: read` requirement.

### Outbound JWT auth: `kelos-jwt` and the `curl` wrapper

Port of [`TokenSigningProvider`](../../../ai-agent/assay/src/adapters/auth/token-signing.ts) from `ai-agent/assay`. Built from Go sources in `internal/jwt/` and `cmd/kelos-{jwt,curl}/`; see those packages for the authoritative interface and tests.

- **`/usr/local/bin/curl`** — Transparent wrapper that replaces `/usr/bin/curl` on `PATH`. The real curl binary is relocated to `/usr/local/libexec/curl-real` at image build time and reached only via `KELOS_CURL_REAL` (set in the image as a default), so there is no `/usr/bin/curl` for an agent or script to bypass to. When `ALPHEYA_TOKEN_SIGNING_ISSUER` is set, the wrapper mints a JWT and prepends `-H "Authorization: Bearer <jwt>"` for every http(s) URL before `syscall.Exec`'ing the real curl. Passthrough cases (byte-for-byte real curl, including exit code and TTY): no signing config, no URL in argv, or argv already carries an explicit `-H Authorization:` / `-u user:pass`. Agent-explicit auth wins over the wrapper.
- **`/usr/local/bin/kelos-jwt`** — Explicit CLI for the cases where transparent injection is the wrong shape: embedding a JWT in a non-curl request, debug commands that want to inspect the minted token, or grpcurl (which the wrapper doesn't cover). Usage: `kelos-jwt sign <service[:profile]>`. Reads the same env contract.

**`service:profile` syntax** (matches assay D-12): `kelos-jwt sign order-service` → `DEFAULT_CLAIMS`; `kelos-jwt sign order-service:admin` → `PROFILES.admin`, falls back to defaults if the profile is absent. For the curl wrapper, set `ALPHEYA_TOKEN_PROFILE=admin` on the invocation to apply the same suffix.

**Why transparent over explicit:** the initial port was a bash `sign-jwt` helper the agent had to remember to invoke. That repeats the failure mode where the agent silently skips auth steps it wasn't reminded about. The `curl` wrapper makes signing a property of the tool, not a property of the agent's prompt — the same design as assay's `AuthProvider` registered against an HTTP client.

### Wiring from a TaskSpawner

The signing layer is configured entirely through `TaskSpawner.spec.taskTemplate.podOverrides.env` — no CRD schema change. The `task_types.go:17` comment designates that field as the credential delivery path, so a typed `tokenSigning` block would only add indirection.

```yaml
spec:
  taskTemplate:
    podOverrides:
      env:
        - name: ALPHEYA_TOKEN_SIGNING_KEY
          valueFrom:
            secretKeyRef:
              name: cody-jwt-signing
              key: key.pem
        - name: ALPHEYA_TOKEN_SIGNING_ISSUER
          value: assay
        - name: ALPHEYA_TOKEN_SIGNING_DEFAULT_CLAIMS
          value: '{"sub":"cody","roles":["debug"],"email":"cody@alpheya.com"}'
        - name: ALPHEYA_TOKEN_SIGNING_PROFILES
          value: '{"admin":{"sub":"cody-admin","roles":["admin","debug"]}}'
        - name: ALPHEYA_TOKEN_SIGNING_HOSTS
          value: '{"hermes-api.alpheya.com":"hermes","facade.alpheya.com":"facade"}'
```

## Alpheya engineering skills

The image bakes `quantum-wealth/skills/plugins/alpheya-standards/skills/*/` into `/etc/codex/skills/<name>/` at build time. Per the [OpenAI Codex skills docs](https://developers.openai.com/codex/skills), Codex auto-discovers skills from `/etc/codex/skills/` and `$HOME/.agents/skills/` at startup — no flag, no env var, no entrypoint logic. The `description` field in each `SKILL.md` frontmatter tells codex when to trigger that skill.

To update the baked skills: edit/merge in `quantum-wealth/skills`, then rebuild the image with `GITHUB_TOKEN=$(gh auth token) make image WHAT=codex ...`. The token is consumed only at build time via a BuildKit secret (`--secret id=github_token,env=GITHUB_TOKEN`); it never lands in an image layer.

## Why credential.helper instead of a static `GITHUB_TOKEN`

Installation tokens expire after one hour. Caching a token at pod start
would either limit the agent to short tasks or require a rotation
sidecar. The credential helper requests a new token from `cody-tools` on
each git call, so the pod can run for hours without thinking about expiry.
The long-lived GitHub App private key stays in `cody-tools` and is never
written to the task pod.
