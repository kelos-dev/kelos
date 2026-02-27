package controller

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// DefaultSpawnerImage is the default image for the spawner binary.
	DefaultSpawnerImage = "gjkim42/kelos-spawner:latest"

	// DefaultTokenRefresherImage is the default image for the token refresher sidecar.
	DefaultTokenRefresherImage = "gjkim42/kelos-token-refresher:latest"

	// SpawnerServiceAccount is the service account used by spawner Deployments.
	SpawnerServiceAccount = "kelos-spawner"

	// SpawnerClusterRole is the ClusterRole referenced by spawner RoleBindings.
	SpawnerClusterRole = "kelos-spawner-role"
)

// DeploymentBuilder constructs Kubernetes Deployments for TaskSpawners.
type DeploymentBuilder struct {
	SpawnerImage                  string
	SpawnerImagePullPolicy        corev1.PullPolicy
	TokenRefresherImage           string
	TokenRefresherImagePullPolicy corev1.PullPolicy
}

// NewDeploymentBuilder creates a new DeploymentBuilder.
func NewDeploymentBuilder() *DeploymentBuilder {
	return &DeploymentBuilder{
		SpawnerImage:        DefaultSpawnerImage,
		TokenRefresherImage: DefaultTokenRefresherImage,
	}
}

// spawnerPodParts holds the components needed to build a spawner pod template.
type spawnerPodParts struct {
	args           []string
	envVars        []corev1.EnvVar
	volumes        []corev1.Volume
	volumeMounts   []corev1.VolumeMount
	initContainers []corev1.Container
	labels         map[string]string
}

// buildPodParts computes the args, env, volumes, and labels that are shared
// between a Deployment pod and a CronJob pod for the given TaskSpawner.
func (b *DeploymentBuilder) buildPodParts(ts *kelosv1alpha1.TaskSpawner, workspace *kelosv1alpha1.WorkspaceSpec, isGitHubApp bool) spawnerPodParts {
	args := []string{
		"--taskspawner-name=" + ts.Name,
		"--taskspawner-namespace=" + ts.Namespace,
	}

	var envVars []corev1.EnvVar
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	var initContainers []corev1.Container

	if workspace != nil {
		host, owner, repo := parseGitHubRepo(workspace.Repo)

		// Override with explicit GitHubIssues.Repo if set (fork workflow)
		if ts.Spec.When.GitHubIssues != nil && ts.Spec.When.GitHubIssues.Repo != "" {
			host, owner, repo = parseGitHubRepo(ts.Spec.When.GitHubIssues.Repo)
		}

		args = append(args,
			"--github-owner="+owner,
			"--github-repo="+repo,
		)
		if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
			args = append(args, "--github-api-base-url="+apiBaseURL)
		}

		if workspace.SecretRef != nil {
			if isGitHubApp {
				// GitHub App: add token refresher as a native sidecar
				args = append(args, "--github-token-file=/shared/token/GITHUB_TOKEN")

				volumes = append(volumes,
					corev1.Volume{
						Name: "github-token",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{},
						},
					},
					corev1.Volume{
						Name: "github-app-secret",
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{
								SecretName: workspace.SecretRef.Name,
							},
						},
					},
				)

				volumeMounts = append(volumeMounts, corev1.VolumeMount{
					Name:      "github-token",
					MountPath: "/shared/token",
					ReadOnly:  true,
				})

				refresherEnv := []corev1.EnvVar{
					{
						Name: "APP_ID",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "appID",
							},
						},
					},
					{
						Name: "INSTALLATION_ID",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "installationID",
							},
						},
					},
				}
				if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
					refresherEnv = append(refresherEnv, corev1.EnvVar{
						Name:  "GITHUB_API_BASE_URL",
						Value: apiBaseURL,
					})
				}

				restartPolicyAlways := corev1.ContainerRestartPolicyAlways
				initContainers = append(initContainers, corev1.Container{
					Name:            "token-refresher",
					Image:           b.TokenRefresherImage,
					ImagePullPolicy: b.TokenRefresherImagePullPolicy,
					RestartPolicy:   &restartPolicyAlways,
					Env:             refresherEnv,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "github-token",
							MountPath: "/shared/token",
						},
						{
							Name:      "github-app-secret",
							MountPath: "/etc/github-app",
							ReadOnly:  true,
						},
					},
				})
			} else {
				// PAT: inject GITHUB_TOKEN from secret
				envVars = append(envVars, corev1.EnvVar{
					Name: "GITHUB_TOKEN",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{
								Name: workspace.SecretRef.Name,
							},
							Key: "GITHUB_TOKEN",
						},
					},
				})
			}
		}
	}

	if ts.Spec.When.Jira != nil {
		jira := ts.Spec.When.Jira
		args = append(args,
			"--jira-base-url="+jira.BaseURL,
			"--jira-project="+jira.Project,
		)
		if jira.JQL != "" {
			args = append(args, "--jira-jql="+jira.JQL)
		}

		// JIRA_USER is optional: when present, Jira Cloud basic auth is used
		// (email + API token). When absent, Bearer token auth is used for
		// Jira Data Center/Server PATs.
		optional := true
		envVars = append(envVars,
			corev1.EnvVar{
				Name: "JIRA_USER",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: jira.SecretRef.Name,
						},
						Key:      "JIRA_USER",
						Optional: &optional,
					},
				},
			},
			corev1.EnvVar{
				Name: "JIRA_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: jira.SecretRef.Name,
						},
						Key: "JIRA_TOKEN",
					},
				},
			},
		)
	}

	labels := map[string]string{
		"app.kubernetes.io/name":       "kelos",
		"app.kubernetes.io/component":  "spawner",
		"app.kubernetes.io/managed-by": "kelos-controller",
		"kelos.dev/taskspawner":        ts.Name,
	}

	return spawnerPodParts{
		args:           args,
		envVars:        envVars,
		volumes:        volumes,
		volumeMounts:   volumeMounts,
		initContainers: initContainers,
		labels:         labels,
	}
}

// Build creates a Deployment for the given TaskSpawner.
// The workspace parameter provides the repository URL and optional secretRef
// for GitHub API authentication. The isGitHubApp parameter indicates whether
// the workspace secret contains GitHub App credentials, which requires a
// token refresher sidecar.
func (b *DeploymentBuilder) Build(ts *kelosv1alpha1.TaskSpawner, workspace *kelosv1alpha1.WorkspaceSpec, isGitHubApp bool) *appsv1.Deployment {
	replicas := int32(1)
	p := b.buildPodParts(ts, workspace, isGitHubApp)

	spawnerContainer := corev1.Container{
		Name:            "spawner",
		Image:           b.SpawnerImage,
		ImagePullPolicy: b.SpawnerImagePullPolicy,
		Args:            p.args,
		Env:             p.envVars,
		VolumeMounts:    p.volumeMounts,
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ts.Name,
			Namespace: ts.Namespace,
			Labels:    p.labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: p.labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: p.labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: SpawnerServiceAccount,
					RestartPolicy:      corev1.RestartPolicyAlways,
					Volumes:            p.volumes,
					InitContainers:     p.initContainers,
					Containers:         []corev1.Container{spawnerContainer},
				},
			},
		},
	}
}

// BuildCronJob creates a CronJob for a cron-based TaskSpawner.
// Instead of running a long-lived Deployment with pollInterval, the CronJob
// runs the spawner in one-shot mode on the cron schedule itself.
// The workspace and isGitHubApp parameters are passed through to buildPodParts
// so that CronJob pods get the same GitHub auth and repo args as Deployments.
func (b *DeploymentBuilder) BuildCronJob(ts *kelosv1alpha1.TaskSpawner, workspace *kelosv1alpha1.WorkspaceSpec, isGitHubApp bool) *batchv1.CronJob {
	p := b.buildPodParts(ts, workspace, isGitHubApp)

	// Add --one-shot flag so the spawner runs a single cycle and exits.
	// Copy to avoid mutating the shared slice from buildPodParts.
	args := make([]string, len(p.args), len(p.args)+1)
	copy(args, p.args)
	args = append(args, "--one-shot")

	spawnerContainer := corev1.Container{
		Name:            "spawner",
		Image:           b.SpawnerImage,
		ImagePullPolicy: b.SpawnerImagePullPolicy,
		Args:            args,
		Env:             p.envVars,
		VolumeMounts:    p.volumeMounts,
	}

	backoffLimit := int32(0)
	// Keep the last 3 successful and 1 failed jobs for debugging.
	successfulJobsHistoryLimit := int32(3)
	failedJobsHistoryLimit := int32(1)

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ts.Name,
			Namespace: ts.Namespace,
			Labels:    p.labels,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   ts.Spec.When.Cron.Schedule,
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: &successfulJobsHistoryLimit,
			FailedJobsHistoryLimit:     &failedJobsHistoryLimit,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: p.labels,
				},
				Spec: batchv1.JobSpec{
					BackoffLimit: &backoffLimit,
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: p.labels,
						},
						Spec: corev1.PodSpec{
							ServiceAccountName: SpawnerServiceAccount,
							RestartPolicy:      corev1.RestartPolicyNever,
							Volumes:            p.volumes,
							InitContainers:     p.initContainers,
							Containers:         []corev1.Container{spawnerContainer},
						},
					},
				},
			},
		},
	}
}

// httpsRepoRe matches HTTPS-style repository URLs: https://host/owner/repo
var httpsRepoRe = regexp.MustCompile(`https?://([^/]+)/([^/]+)/([^/.]+)`)

// sshRepoRe matches SSH-style repository URLs: git@host:owner/repo
var sshRepoRe = regexp.MustCompile(`git@([^:]+):([^/]+)/([^/.]+)`)

// parseGitHubRepo extracts the host, owner, and repo from a GitHub repository URL.
// Supports HTTPS (https://github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git)
// for both github.com and GitHub Enterprise hosts.
func parseGitHubRepo(repoURL string) (host, owner, repo string) {
	repoURL = strings.TrimSuffix(repoURL, ".git")

	if m := httpsRepoRe.FindStringSubmatch(repoURL); len(m) == 4 {
		return m[1], m[2], m[3]
	}
	if m := sshRepoRe.FindStringSubmatch(repoURL); len(m) == 4 {
		return m[1], m[2], m[3]
	}

	// Fallback: try splitting by '/' and taking last two segments
	parts := strings.Split(strings.TrimSuffix(repoURL, "/"), "/")
	if len(parts) >= 2 {
		return "", parts[len(parts)-2], parts[len(parts)-1]
	}

	return "", "", fmt.Sprintf("unknown-repo-%s", repoURL)
}

// parseGitHubOwnerRepo extracts owner and repo from a GitHub repository URL.
// Supports HTTPS (https://github.com/owner/repo.git) and SSH (git@github.com:owner/repo.git).
func parseGitHubOwnerRepo(repoURL string) (owner, repo string) {
	_, owner, repo = parseGitHubRepo(repoURL)
	return owner, repo
}

// gitHubAPIBaseURL returns the GitHub API base URL for the given host.
// For github.com (or empty host) it returns an empty string, as the spawner uses the default API endpoint.
// For GitHub Enterprise hosts it returns "https://<host>/api/v3".
func gitHubAPIBaseURL(host string) string {
	if host == "" || host == "github.com" {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: host, Path: "/api/v3"}).String()
}
