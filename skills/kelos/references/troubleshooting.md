# Kelos Troubleshooting Reference

Read this only when debugging live Kelos resources or explaining an observed
failure state.

## Task Stuck In Pending

- Check whether the credentials Secret exists: `kubectl get secret <name>`.
- Check controller logs: `kubectl logs deployment/kelos-controller-manager -n kelos-system`.
- Inspect the Task YAML for invalid references or missing required fields.

## Task Stuck In Waiting

- Check whether every Task in `spec.dependsOn` has succeeded.
- Check whether another Task holds the same `spec.branch` lock.
- Inspect status conditions for the controller's current reason.

## Task Fails Immediately

- Verify agent credentials are valid without printing Secret values.
- Check whether the Workspace repository is accessible.
- Review pod logs with `kelos logs <task-name>` or
  `kubectl logs -l job-name=<job-name>`.

## Session Stuck In Pending Or Failed

- Inspect the Session status with `kelos get session <name> -d` or
  `kubectl get session <name> -o yaml`.
- Check whether the credentials Secret, Workspace, and AgentConfig resources
  referenced by `spec.worker` exist.
- Check controller logs: `kubectl logs deployment/kelos-controller-manager -n kelos-system`.
- Inspect the Session Pod only after checking the status reason and referenced
  resources. Do not print Secret values.

## TaskSpawner Not Creating Tasks

- Check spawner status: `kubectl get taskspawner <name> -o yaml`.
- Verify referenced Workspace and AgentConfig resources exist.
- Check whether `maxConcurrency` is reached.
- Check whether `maxTotalTasks` is reached.
- Check whether `spec.suspend: true` is set.
- For source polling, check the source-specific `pollInterval` under
  `spec.when.githubIssues`, `spec.when.githubPullRequests`, `spec.when.jira`, or `spec.when.gitlab`.
- For comment-controlled sources, check whether the latest authorized command
  includes or excludes the item.

## AgentConfig Not Taking Effect

- Verify the Task references it with `spec.agentConfigRefs[].name`.
- Check plugin structure: skills become `<plugin>/skills/<skill>/SKILL.md`.
- For skills.sh, ensure the package source uses `owner/repo` format.
- Inspect the running pod or generated workspace only after confirming the Task
  references the intended AgentConfig.

## Agent Cannot Push Or Create PRs

- Ensure the Workspace Secret contains a valid `GITHUB_TOKEN` or GitHub App
  credentials. For `provider: gitlab` Workspaces the Secret must hold a
  `GITLAB_TOKEN` key with the `api` scope; a Task or TaskSpawner whose Secret
  lacks it is marked `Failed` with the missing key in its status message.
- Verify token permissions include repository write access.
- For workflow edits, verify the credential includes workflow permissions.
- For GitHub Apps, check that `appID`, `installationID`, and `privateKey` are
  present in the Secret.
