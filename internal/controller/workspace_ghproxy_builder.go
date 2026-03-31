package controller

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// DefaultGHProxyImage is the default image for workspace ghproxy Deployments.
	DefaultGHProxyImage = "ghcr.io/kelos-dev/ghproxy:latest"

	workspaceProxyPort       = 8888
	workspaceProxyNamePrefix = "ghproxy-"
)

// WorkspaceGHProxyBuilder constructs Services and Deployments for workspace-scoped ghproxy instances.
type WorkspaceGHProxyBuilder struct {
	GHProxyImage                  string
	GHProxyImagePullPolicy        corev1.PullPolicy
	GHProxyResources              *corev1.ResourceRequirements
	TokenRefresherImage           string
	TokenRefresherImagePullPolicy corev1.PullPolicy
	TokenRefresherResources       *corev1.ResourceRequirements
}

// NewWorkspaceGHProxyBuilder creates a new WorkspaceGHProxyBuilder.
func NewWorkspaceGHProxyBuilder() *WorkspaceGHProxyBuilder {
	return &WorkspaceGHProxyBuilder{
		GHProxyImage:        DefaultGHProxyImage,
		TokenRefresherImage: DefaultTokenRefresherImage,
	}
}

func workspaceProxyLabels(workspaceName string) map[string]string {
	return map[string]string{
		"kelos.dev/name":       "kelos",
		"kelos.dev/component":  "ghproxy",
		"kelos.dev/managed-by": "kelos-controller",
		"kelos.dev/workspace":  workspaceName,
	}
}

// WorkspaceGHProxyName returns the deterministic resource name for a workspace-scoped proxy.
func WorkspaceGHProxyName(workspaceName string) string {
	name := workspaceProxyNamePrefix + workspaceName
	if len(name) <= 63 {
		return name
	}

	sum := sha1.Sum([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:8]
	maxPrefixLen := 63 - len(suffix) - 1
	return name[:maxPrefixLen] + "-" + suffix
}

// WorkspaceGHProxyServiceURL returns the in-cluster Service URL for a workspace-scoped proxy.
func WorkspaceGHProxyServiceURL(namespace, workspaceName string) string {
	return fmt.Sprintf("http://%s.%s:%d", WorkspaceGHProxyName(workspaceName), namespace, workspaceProxyPort)
}

func workspaceProxyUpstreamBaseURL(workspace *kelosv1alpha1.Workspace) string {
	host, _, _ := parseGitHubRepo(workspace.Spec.Repo)
	if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
		return apiBaseURL
	}
	return "https://api.github.com"
}

// BuildService creates a Service for the workspace ghproxy.
func (b *WorkspaceGHProxyBuilder) BuildService(workspace *kelosv1alpha1.Workspace) *corev1.Service {
	labels := workspaceProxyLabels(workspace.Name)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkspaceGHProxyName(workspace.Name),
			Namespace: workspace.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       workspaceProxyPort,
					TargetPort: intstrFromInt(workspaceProxyPort),
					Protocol:   corev1.ProtocolTCP,
				},
				{
					Name:       "metrics",
					Port:       9090,
					TargetPort: intstrFromInt(9090),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

// BuildDeployment creates a Deployment for the workspace ghproxy.
func (b *WorkspaceGHProxyBuilder) BuildDeployment(workspace *kelosv1alpha1.Workspace, isGitHubApp bool) *appsv1.Deployment {
	labels := workspaceProxyLabels(workspace.Name)
	args := []string{
		"--upstream-base-url=" + workspaceProxyUpstreamBaseURL(workspace),
	}

	var env []corev1.EnvVar
	var volumes []corev1.Volume
	var volumeMounts []corev1.VolumeMount
	var initContainers []corev1.Container

	if workspace.Spec.SecretRef != nil {
		if isGitHubApp {
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
							SecretName: workspace.Spec.SecretRef.Name,
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
							LocalObjectReference: corev1.LocalObjectReference{Name: workspace.Spec.SecretRef.Name},
							Key:                  "appID",
						},
					},
				},
				{
					Name: "INSTALLATION_ID",
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: workspace.Spec.SecretRef.Name},
							Key:                  "installationID",
						},
					},
				},
			}
			host, _, _ := parseGitHubRepo(workspace.Spec.Repo)
			if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
				refresherEnv = append(refresherEnv, corev1.EnvVar{
					Name:  "GITHUB_API_BASE_URL",
					Value: apiBaseURL,
				})
			}

			restartPolicyAlways := corev1.ContainerRestartPolicyAlways
			refresher := corev1.Container{
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
			}
			if b.TokenRefresherResources != nil {
				refresher.Resources = *b.TokenRefresherResources
			}
			initContainers = append(initContainers, refresher)
		} else {
			env = append(env, corev1.EnvVar{
				Name: "GITHUB_TOKEN",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: workspace.Spec.SecretRef.Name},
						Key:                  "GITHUB_TOKEN",
					},
				},
			})
		}
	}

	container := corev1.Container{
		Name:            "ghproxy",
		Image:           b.GHProxyImage,
		ImagePullPolicy: b.GHProxyImagePullPolicy,
		Args:            args,
		Env:             env,
		VolumeMounts:    volumeMounts,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptrTo(false),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: workspaceProxyPort,
				Protocol:      corev1.ProtocolTCP,
			},
			{
				Name:          "metrics",
				ContainerPort: 9090,
				Protocol:      corev1.ProtocolTCP,
			},
		},
	}
	if b.GHProxyResources != nil {
		container.Resources = *b.GHProxyResources
	}

	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkspaceGHProxyName(workspace.Name),
			Namespace: workspace.Namespace,
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
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrTo(true),
					},
					Volumes:        volumes,
					InitContainers: initContainers,
					Containers:     []corev1.Container{container},
				},
			},
		},
	}
}

func intstrFromInt(v int32) intstr.IntOrString {
	return intstr.FromInt32(v)
}

func ptrTo[T any](v T) *T {
	return &v
}
