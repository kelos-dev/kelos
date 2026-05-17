# Codex agent — helper binaries

These scripts are baked into the codex agent image and live on `$PATH`
inside the running container. They are invoked from
`codex/kelos_entrypoint.sh` (via `kelos-agent-setup`) or from the agent
itself once it is running.

## Env-var contract

| Variable | Provided by | Used by | Purpose |
| --- | --- | --- | --- |
| `GITHUB_APP_CLIENT_ID` | Task `podOverrides.envFrom` (Secret) | `github-app-token` | JWT `iss` claim — GitHub now accepts Client ID alongside numeric App ID for App authentication. |
| `GITHUB_APP_INSTALLATION_ID` | Task `podOverrides.envFrom` (Secret) | `github-app-token` | Installation to mint the token for. |
| `GITHUB_APP_PRIVATE_KEY` | Task `podOverrides.envFrom` (Secret) | `kelos-agent-setup` | Written to `/etc/kelos-agent/github-app.pem` once at startup; the helper reads from the file, not the env var, on each call. |
| `KUBERNETES_CLUSTER_NAME` | Task `podOverrides.env` (literal) | `kelos-agent-setup` | Optional human-readable cluster name baked into `~/.kube/config`. Defaults to `in-cluster`. |

All three GitHub App variables are mutually required — `kelos-agent-setup`
aborts the task if `CLIENT_ID` is set but `PRIVATE_KEY` is missing rather
than silently producing a broken git config. When none of them are set,
setup is skipped entirely and the container runs as before.

## Binaries

- **`kelos-agent-setup`** — Pre-agent setup invoked from `kelos_entrypoint.sh`. Materialises the GitHub App private key to disk, wires `git config credential.helper`, and synthesises `~/.kube/config` from the projected ServiceAccount token. Each step is a no-op when its inputs are missing, so this script is safe to run unconditionally.
- **`github-app-credential-helper`** — Git credential helper. Reads the credential request on stdin and, for `github.com` / `api.github.com` over HTTPS, returns a fresh installation token as the password. Returns nothing for other hosts so git falls through to its other helpers.
- **`github-app-token`** — Signs a short-lived JWT with the App private key and exchanges it at `/app/installations/{id}/access_tokens` for a ~1 h installation token. Three attempts with exponential backoff before failing, so a transient network hiccup doesn't hang `git push`.

## Why credential.helper instead of a static `GITHUB_TOKEN`

Installation tokens expire after one hour. Caching a token at pod start
would either limit the agent to short tasks or require a rotation
sidecar. The credential helper mints a new token on each git call, so
the pod can run for hours without thinking about expiry, and the only
long-lived secret on disk is the App's RSA private key (read-only,
0600, under `/etc/kelos-agent/`).
