package controller

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

const (
	// DefaultSpawnerImage is the default image for the spawner binary.
	DefaultSpawnerImage = "gjkim42/axon-spawner:latest"

	// DefaultTokenRefresherImage is the default image for the token refresher sidecar.
	DefaultTokenRefresherImage = "gjkim42/axon-token-refresher:latest"

	// SpawnerServiceAccount is the service account used by spawner Deployments.
	SpawnerServiceAccount = "axon-spawner"

	// SpawnerClusterRole is the ClusterRole referenced by spawner RoleBindings.
	SpawnerClusterRole = "axon-spawner-role"
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

// Build creates a Deployment for the given TaskSpawner.
// The workspace parameter provides the repository URL and optional secretRef
// for GitHub API authentication. The isGitHubApp parameter indicates whether
// the workspace secret contains GitHub App credentials, which requires a
// token refresher sidecar.
func (b *DeploymentBuilder) Build(ts *axonv1alpha1.TaskSpawner, workspace *axonv1alpha1.WorkspaceSpec, isGitHubApp bool) *appsv1.Deployment {
	replicas := int32(1)

	args := []string{
		"--taskspawner-name=" + ts.Name,
		"--taskspawner-namespace=" + ts.Namespace,
	}

	var envVars []corev1.EnvVar
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	var sidecars []corev1.Container

	if workspace != nil {
		host, owner, repo := parseGitHubRepo(workspace.Repo)
		args = append(args,
			"--github-owner="+owner,
			"--github-repo="+repo,
		)
		if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
			args = append(args, "--github-api-base-url="+apiBaseURL)
		}

		if workspace.SecretRef != nil {
			if isGitHubApp {
				// GitHub App: add token refresher sidecar
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

				sidecars = append(sidecars, corev1.Container{
					Name:            "token-refresher",
					Image:           b.TokenRefresherImage,
					ImagePullPolicy: b.TokenRefresherImagePullPolicy,
					Env: []corev1.EnvVar{
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
					},
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

	labels := map[string]string{
		"app.kubernetes.io/name":       "axon",
		"app.kubernetes.io/component":  "spawner",
		"app.kubernetes.io/managed-by": "axon-controller",
		"axon.io/taskspawner":          ts.Name,
	}

	spawnerContainer := corev1.Container{
		Name:            "spawner",
		Image:           b.SpawnerImage,
		ImagePullPolicy: b.SpawnerImagePullPolicy,
		Args:            args,
		Env:             envVars,
		VolumeMounts:    volumeMounts,
	}

	containers := []corev1.Container{spawnerContainer}
	containers = append(containers, sidecars...)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ts.Name,
			Namespace: ts.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: SpawnerServiceAccount,
					RestartPolicy:      corev1.RestartPolicyAlways,
					Volumes:            volumes,
					Containers:         containers,
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
