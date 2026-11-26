# aloop self-development

Kelos runs issue and pull request workflows for `kelos-dev/aloop` using the following spawners. Post slash commands on their own line.

| Trigger | Result |
| --- | --- |
| An issue opens, or reopens with `needs-actor` | `aloop-triage` classifies the issue and recommends an actor. Issues labeled `triage-accepted` are skipped. |
| `/kelos plan` on an open issue | `aloop-planner` posts an implementation plan. |
| `/kelos pick-up` on an open issue | `aloop-workers` starts a Codex Session on `aloop-task-<issue number>`. |
| `/kelos pick-up` on an open PR | `aloop-pr-responder` starts a Codex Session on the PR branch. |
| `/kelos review` on an open PR | `aloop-reviewer` publishes a sticky review comment. |
| `/kelos squash-commits` on an open PR | `aloop-squash-commits` rebases and squashes the PR branch. |

Slash commands accept comments from `gjkim42`. The reviewer also accepts PR comments from `kelos-bot[bot]`.

Four scheduled agents maintain the workflow. `aloop-fake-user` looks for onboarding problems and `aloop-fake-strategist` proposes use cases supported by maintainer goals. Each maintains at most one open issue in aloop. `aloop-self-update` proposes workflow improvements as issues in `kelos-dev/kelos`, and `aloop-config-update` opens PRs there when aloop review feedback warrants a configuration change.

## Setup

1. Give the shared Kelos GitHub App access to `kelos-dev/aloop` for repository operations and check reporting. Tasks use the bot credentials in `aloop-agent`. Sessions use the personal GitHub token in `aloop-session-agent`, which needs write access to aloop. The two workflow maintenance tasks use `kelos-agent` to work in this repository. See the [shared credentials and Workspace setup](../README.md#prerequisites).
2. Create the labels the spawners use in aloop.

   ```bash
   repo=kelos-dev/aloop
   gh label create generated-by-kelos --repo "$repo" --color 1d76db --force
   for label in kind/bug kind/feature kind/api kind/docs; do
     gh label create "$label" --repo "$repo" --color 0e8a16 --force
   done
   for label in priority/important-soon priority/important-longterm priority/backlog; do
     gh label create "$label" --repo "$repo" --color fbca04 --force
   done
   for label in actor/kelos actor/human; do
     gh label create "$label" --repo "$repo" --color 5319e7 --force
   done
   for label in needs-actor needs-kind needs-priority needs-triage triage-accepted kelos/needs-input; do
     gh label create "$label" --repo "$repo" --color c5def5 --force
   done
   ```

3. The dev deployment applies the shared resources followed by `self-development/aloop/`. To apply them manually in the deployment namespace:

   ```bash
   kubectl apply -n "$KELOS_NAMESPACE" -f self-development/
   kubectl apply -n "$KELOS_NAMESPACE" -f self-development/aloop/
   kubectl get webhookgateway aloop -n "$KELOS_NAMESPACE" -o jsonpath='{.status.path}'
   ```

4. Configure a GitHub repository webhook on `kelos-dev/aloop`. Append the gateway path to the public webhook server URL, select `application/json`, and use the `webhook-secret` value from the shared `github-webhook-secret` Secret. Subscribe to `issues`, `issue_comment`, and `pull_request_review`.
