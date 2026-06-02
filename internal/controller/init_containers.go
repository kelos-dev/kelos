/*
Copyright 2025 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// buildGitCloneInitContainer creates the init container that clones the
// workspace repository. If the workspace has a SecretRef, git credential
// helpers are configured for authenticated cloning.
func buildGitCloneInitContainer(workspace *kelosv1alpha1.WorkspaceSpec, workspaceEnvVars []corev1.EnvVar) corev1.Container {
	agentUID := AgentUID

	volumeMount := corev1.VolumeMount{
		Name:      WorkspaceVolumeName,
		MountPath: WorkspaceMountPath,
	}

	cloneArgs := []string{"clone"}
	if workspace.Ref != "" {
		cloneArgs = append(cloneArgs, "--branch", workspace.Ref)
	}
	cloneArgs = append(cloneArgs, "--no-single-branch", "--depth", "1", "--", workspace.Repo, WorkspaceMountPath+"/repo")

	initContainer := corev1.Container{
		Name:         "git-clone",
		Image:        GitCloneImage,
		Args:         cloneArgs,
		Env:          workspaceEnvVars,
		VolumeMounts: []corev1.VolumeMount{volumeMount},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: &agentUID,
		},
	}

	if workspace.SecretRef != nil {
		credentialHelper := `!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f`
		initContainer.Command = []string{"sh", "-c",
			fmt.Sprintf(
				`git -c credential.helper= -c credential.helper='%s' "$@" && { `+
					`git -C %s/repo config --unset-all credential.helper 2>/dev/null || true; `+
					`git -C %s/repo config --add credential.helper '%s'; }`,
				credentialHelper, WorkspaceMountPath, WorkspaceMountPath, credentialHelper,
			),
		}
		initContainer.Args = append([]string{"--"}, cloneArgs...)
	}

	return initContainer
}

// buildRemoteSetupInitContainer creates the init container that configures
// additional git remotes. Returns nil if there are no remotes to configure.
func buildRemoteSetupInitContainer(remotes []kelosv1alpha1.GitRemote) *corev1.Container {
	if len(remotes) == 0 {
		return nil
	}

	agentUID := AgentUID
	var parts []string
	parts = append(parts, fmt.Sprintf("cd %s/repo", WorkspaceMountPath))
	for _, r := range remotes {
		parts = append(parts,
			fmt.Sprintf(
				"if git remote get-url %s >/dev/null 2>&1; then git remote set-url %s %s; else git remote add %s %s; fi",
				shellQuote(r.Name),
				shellQuote(r.Name),
				shellQuote(r.URL),
				shellQuote(r.Name),
				shellQuote(r.URL),
			),
		)
	}

	return &corev1.Container{
		Name:    "remote-setup",
		Image:   GitCloneImage,
		Command: []string{"sh", "-c", strings.Join(parts, " && ")},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      WorkspaceVolumeName,
			MountPath: WorkspaceMountPath,
		}},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: &agentUID,
		},
	}
}

// buildBranchSetupInitContainer creates the init container that checks out
// the task branch. Returns nil if no branch is specified.
func buildBranchSetupInitContainer(branch string, workspace *kelosv1alpha1.WorkspaceSpec, workspaceEnvVars []corev1.EnvVar) *corev1.Container {
	if branch == "" {
		return nil
	}

	agentUID := AgentUID

	fetchCmd := `git fetch origin "$KELOS_BRANCH":"$KELOS_BRANCH" 2>/dev/null`
	if workspace.SecretRef != nil {
		credHelper := `!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f`
		fetchCmd = fmt.Sprintf(`git -c credential.helper= -c credential.helper='%s' fetch origin "$KELOS_BRANCH":"$KELOS_BRANCH" 2>/dev/null`, credHelper)
	}
	branchSetupScript := fmt.Sprintf(
		`cd %s/repo && %s; `+
			`if git rev-parse --verify refs/heads/"$KELOS_BRANCH" >/dev/null 2>&1; then `+
			`git checkout "$KELOS_BRANCH"; `+
			`else git checkout -b "$KELOS_BRANCH"; fi`,
		WorkspaceMountPath, fetchCmd,
	)
	branchEnv := make([]corev1.EnvVar, len(workspaceEnvVars), len(workspaceEnvVars)+1)
	copy(branchEnv, workspaceEnvVars)
	branchEnv = append(branchEnv, corev1.EnvVar{
		Name:  "KELOS_BRANCH",
		Value: branch,
	})

	return &corev1.Container{
		Name:    "branch-setup",
		Image:   GitCloneImage,
		Command: []string{"sh", "-c", branchSetupScript},
		Env:     branchEnv,
		VolumeMounts: []corev1.VolumeMount{{
			Name:      WorkspaceVolumeName,
			MountPath: WorkspaceMountPath,
		}},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: &agentUID,
		},
	}
}

// buildWorkspaceFilesInitContainer creates the init container that injects
// workspace files. Returns nil if there are no files to inject.
func buildWorkspaceFilesInitContainer(files []kelosv1alpha1.WorkspaceFile) (*corev1.Container, error) {
	if len(files) == 0 {
		return nil, nil
	}

	agentUID := AgentUID

	injectionScript, err := buildWorkspaceFileInjectionScript(files)
	if err != nil {
		return nil, err
	}

	return &corev1.Container{
		Name:    "workspace-files",
		Image:   GitCloneImage,
		Command: []string{"sh", "-c", injectionScript},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      WorkspaceVolumeName,
			MountPath: WorkspaceMountPath,
		}},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser: &agentUID,
		},
	}, nil
}

// buildPluginSetupInitContainer creates the init container that sets up
// plugin directories. Returns nil if there are no plugins.
func buildPluginSetupInitContainer(plugins []kelosv1alpha1.PluginSpec) (*corev1.Container, error) {
	if len(plugins) == 0 {
		return nil, nil
	}

	agentUID := AgentUID

	script, err := buildPluginSetupScript(plugins)
	if err != nil {
		return nil, fmt.Errorf("invalid plugin configuration: %w", err)
	}

	return &corev1.Container{
		Name:    "plugin-setup",
		Image:   GitCloneImage,
		Command: []string{"sh", "-c", script},
		VolumeMounts: []corev1.VolumeMount{
			{Name: PluginVolumeName, MountPath: PluginMountPath},
		},
		SecurityContext: &corev1.SecurityContext{RunAsUser: &agentUID},
	}, nil
}

// buildSkillsInstallInitContainer creates the init container that installs
// skills.sh packages. Returns nil if there are no skills.
func buildSkillsInstallInitContainer(skills []kelosv1alpha1.SkillsShSpec, agentType string) (*corev1.Container, error) {
	if len(skills) == 0 {
		return nil, nil
	}

	script, err := buildSkillsInstallScript(skills, agentType)
	if err != nil {
		return nil, fmt.Errorf("invalid skills configuration: %w", err)
	}

	return &corev1.Container{
		Name:    "skills-install",
		Image:   NodeImage,
		Command: []string{"sh", "-c", script},
		Env: []corev1.EnvVar{
			{Name: "HOME", Value: PluginMountPath},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: PluginVolumeName, MountPath: PluginMountPath},
		},
	}, nil
}
