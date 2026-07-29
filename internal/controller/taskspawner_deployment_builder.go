package controller

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	SpawnerImageRepository = "ghcr.io/kelos-dev/kelos-spawner"

	// DefaultSpawnerImage is the default image for the spawner binary.
	DefaultSpawnerImage = SpawnerImageRepository + ":latest"

	// SpawnerServiceAccount is the service account used by spawner Deployments.
	SpawnerServiceAccount = "kelos-spawner"

	// SpawnerClusterRole is the ClusterRole referenced by spawner RoleBindings.
	SpawnerClusterRole = "kelos-spawner-role"
)

// DeploymentBuilder constructs Kubernetes Deployments for TaskSpawners.
type DeploymentBuilder struct {
	SpawnerImage           string
	SpawnerImagePullPolicy corev1.PullPolicy
	SpawnerResources       *corev1.ResourceRequirements
}

// NewDeploymentBuilder creates a new DeploymentBuilder.
func NewDeploymentBuilder() *DeploymentBuilder {
	return &DeploymentBuilder{
		SpawnerImage: DefaultSpawnerImage,
	}
}

// spawnerPodParts holds the components needed to build a spawner pod template.
type spawnerPodParts struct {
	args    []string
	envVars []corev1.EnvVar
	labels  map[string]string
}

// buildPodParts computes the args, env, volumes, and labels that are shared
// between a Deployment pod and a CronJob pod for the given TaskSpawner.
func (b *DeploymentBuilder) buildPodParts(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool) spawnerPodParts {
	args := []string{
		"--taskspawner-name=" + ts.Name,
		"--taskspawner-namespace=" + ts.Namespace,
	}

	var envVars []corev1.EnvVar

	if workspace != nil {
		host, owner, repo := parseGitHubRepo(workspace.Repo)

		// Override with an explicit GitHub source repo if set (fork workflow).
		if repoOverride := githubSourceRepoOverride(ts); repoOverride != "" {
			overrideHost, overrideOwner, overrideRepo := parseGitHubRepo(repoOverride)
			owner = overrideOwner
			repo = overrideRepo
			// Only override the host when the override itself provides one.
			// Shorthand "owner/repo" returns an empty host from parseGitHubRepo;
			// in that case keep the workspace host so GHES API URLs are preserved.
			if overrideHost != "" {
				host = overrideHost
			}
		}

		args = append(args,
			"--github-owner="+owner,
			"--github-repo="+repo,
		)
		if workspaceUsesGHProxy(workspace) && ts.Spec.TaskTemplate.WorkspaceRef != nil {
			args = append(args, "--gh-proxy-url="+WorkspaceGHProxyServiceURL(ts.Namespace, ts.Spec.TaskTemplate.WorkspaceRef.Name))
		}
		if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
			args = append(args, "--github-api-base-url="+apiBaseURL)
		}
		if workspace.SecretRef != nil && taskSpawnerNeedsGitHubToken(ts, workspaceUsesGHProxy(workspace)) {
			if isGitHubApp {
				// GitHub App: inject credentials as env vars for in-process token generation
				envVars = append(envVars,
					corev1.EnvVar{
						Name: "GITHUB_APP_ID",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "appID",
							},
						},
					},
					corev1.EnvVar{
						Name: "GITHUB_APP_INSTALLATION_ID",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "installationID",
							},
						},
					},
					corev1.EnvVar{
						Name: "GITHUB_APP_PRIVATE_KEY",
						ValueFrom: &corev1.EnvVarSource{
							SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: workspace.SecretRef.Name,
								},
								Key: "privateKey",
							},
						},
					},
				)
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

	// Propagate the controller's OTLP configuration to the spawner pod so its
	// discovery traces and metrics reach the same collector. No-op when the
	// controller was not configured with an OTLP endpoint.
	envVars = append(envVars, otelEnvForSpawner()...)

	labels := map[string]string{
		"kelos.dev/name":        "kelos",
		"kelos.dev/component":   "spawner",
		"kelos.dev/managed-by":  "kelos-controller",
		"kelos.dev/taskspawner": ts.Name,
	}

	return spawnerPodParts{
		args:    args,
		envVars: envVars,
		labels:  labels,
	}
}

// otelEnvForSpawner mirrors the controller's OTEL_* environment onto the
// spawner pod so it exports to the same OTLP endpoint. It returns nil when no
// OTLP endpoint is configured, preserving Prometheus-only behavior. The
// spawner's service.name is set to kelos-spawner unless the operator pins one
// via OTEL_RESOURCE_ATTRIBUTES.
func otelEnvForSpawner() []corev1.EnvVar {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" &&
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" &&
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		return nil
	}
	var envVars []corev1.EnvVar
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_RESOURCE_ATTRIBUTES",
	} {
		if v := os.Getenv(name); v != "" {
			envVars = append(envVars, corev1.EnvVar{Name: name, Value: v})
		}
	}

	// OTEL_EXPORTER_OTLP_HEADERS commonly carries a bearer token. When the
	// controller was configured with the header via a Secret, propagate it to
	// the spawner as a secretKeyRef instead of a literal value so the token is
	// never written into the spawner Deployment manifest. The referenced Secret
	// must exist in the TaskSpawner's namespace. Otherwise fall back to copying
	// the literal header value (for non-sensitive headers).
	if secretName := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS_SECRET_NAME"); secretName != "" {
		key := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS_SECRET_KEY")
		if key == "" {
			key = "OTEL_EXPORTER_OTLP_HEADERS"
		}
		envVars = append(envVars, corev1.EnvVar{
			Name: "OTEL_EXPORTER_OTLP_HEADERS",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  key,
				},
			},
		})
	} else if v := os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"); v != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_HEADERS", Value: v})
	}
	return envVars
}

// Build creates a Deployment for the given TaskSpawner.
// The workspace parameter provides the repository URL and optional secretRef
// for GitHub API authentication. The isGitHubApp parameter indicates whether
// the workspace secret contains GitHub App credentials, which are injected as
// env vars for in-process token generation.
func (b *DeploymentBuilder) Build(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool) *appsv1.Deployment {
	replicas := int32(1)
	p := b.buildPodParts(ts, workspace, isGitHubApp)

	spawnerContainer := corev1.Container{
		Name:            "spawner",
		Image:           b.SpawnerImage,
		ImagePullPolicy: b.SpawnerImagePullPolicy,
		Args:            p.args,
		Env:             p.envVars,
		Ports: []corev1.ContainerPort{
			{
				Name:          "metrics",
				ContainerPort: 8080,
				Protocol:      corev1.ProtocolTCP,
			},
		},
	}
	if b.SpawnerResources != nil {
		spawnerContainer.Resources = *b.SpawnerResources
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
func (b *DeploymentBuilder) BuildCronJob(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec, isGitHubApp bool) *batchv1.CronJob {
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
	}
	if b.SpawnerResources != nil {
		spawnerContainer.Resources = *b.SpawnerResources
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

func githubSourceRepoOverride(ts *kelos.TaskSpawner) string {
	if ts.Spec.When.GitHubIssues != nil && ts.Spec.When.GitHubIssues.Repo != "" {
		return ts.Spec.When.GitHubIssues.Repo
	}
	if ts.Spec.When.GitHubPullRequests != nil && ts.Spec.When.GitHubPullRequests.Repo != "" {
		return ts.Spec.When.GitHubPullRequests.Repo
	}
	return ""
}

func taskSpawnerNeedsGitHubToken(ts *kelos.TaskSpawner, ghProxyConfigured bool) bool {
	if ts.Spec.When.GitHubIssues != nil {
		return !ghProxyConfigured || gitHubReportingNeedsToken(ts.Spec.When.GitHubIssues.Reporting)
	}
	if ts.Spec.When.GitHubPullRequests != nil {
		return !ghProxyConfigured || gitHubReportingNeedsToken(ts.Spec.When.GitHubPullRequests.Reporting)
	}
	return false
}

func gitHubReportingNeedsToken(reporting *kelos.GitHubReporting) bool {
	return reporting != nil && (reporting.Enabled || reporting.Checks != nil)
}

func workspaceUsesGHProxy(workspace *kelos.WorkspaceSpec) bool {
	return workspace != nil && workspace.GHProxy != nil
}

func validateWorkspaceGHProxyRepoOverride(ts *kelos.TaskSpawner, workspace *kelos.WorkspaceSpec) error {
	if workspace == nil {
		return nil
	}
	repoOverride := githubSourceRepoOverride(ts)
	if repoOverride == "" {
		return nil
	}

	workspaceHost, _, _ := parseGitHubRepo(workspace.Repo)
	overrideHost, _, _ := parseGitHubRepo(repoOverride)
	if overrideHost == "" || workspaceHost == "" || overrideHost == workspaceHost {
		return nil
	}

	return fmt.Errorf("github source repo override host %q does not match Workspace host %q", overrideHost, workspaceHost)
}

// ParseResourceList parses a comma-separated "name=value" string into a
// corev1.ResourceList. An empty string returns nil. Each value must pass
// Kubernetes quantity parsing.
func ParseResourceList(s string) (corev1.ResourceList, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	rl := corev1.ResourceList{}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid resource entry %q: expected name=value", entry)
		}
		name := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if name == "" || value == "" {
			return nil, fmt.Errorf("invalid resource entry %q: expected name=value", entry)
		}
		qty, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("invalid quantity for %q: %w", name, err)
		}
		rl[corev1.ResourceName(name)] = qty
	}
	return rl, nil
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
