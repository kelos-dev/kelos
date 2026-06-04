package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/kelos-dev/kelos/internal/codexauth"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DefaultCodexAuthRefreshSchedule = "0 */6 * * *"

	codexAuthRefresherCronJobNameMaxLength = 52
	codexAuthRefresherCronJobNamePrefix    = "kelos-codex-auth-"
	codexAuthRefresherComponentLabel       = "codex-auth-refresher"
	codexAuthSecretNamespaceAnnotation     = "kelos.dev/codex-auth-secret-namespace"
	codexAuthSecretNameAnnotation          = "kelos.dev/codex-auth-secret-name"
)

var nonDNSNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

type CodexAuthRefresherBuilder struct {
	Schedule        string
	CodexImage      string
	ImagePullPolicy corev1.PullPolicy
}

func NewCodexAuthRefresherBuilder() *CodexAuthRefresherBuilder {
	return &CodexAuthRefresherBuilder{
		Schedule:   DefaultCodexAuthRefreshSchedule,
		CodexImage: CodexImage,
	}
}

func (b *CodexAuthRefresherBuilder) Build(secret *corev1.Secret) *batchv1.CronJob {
	schedule := b.Schedule
	if schedule == "" {
		schedule = DefaultCodexAuthRefreshSchedule
	}
	image := b.CodexImage
	if image == "" {
		image = CodexImage
	}

	backoffLimit := int32(3)
	activeDeadlineSeconds := int64(600)
	successfulJobsHistoryLimit := int32(1)
	failedJobsHistoryLimit := int32(1)
	runAsNonRoot := true
	allowPrivilegeEscalation := false

	labels := codexAuthRefresherLabels()
	annotations := codexAuthSecretAnnotations(secret)

	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:        CodexAuthRefresherCronJobName(secret.Namespace, secret.Name),
			Namespace:   secret.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: batchv1.CronJobSpec{
			Schedule:                   schedule,
			ConcurrencyPolicy:          batchv1.ForbidConcurrent,
			SuccessfulJobsHistoryLimit: &successfulJobsHistoryLimit,
			FailedJobsHistoryLimit:     &failedJobsHistoryLimit,
			JobTemplate: batchv1.JobTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: batchv1.JobSpec{
					BackoffLimit:          &backoffLimit,
					ActiveDeadlineSeconds: &activeDeadlineSeconds,
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{Labels: labels},
						Spec: corev1.PodSpec{
							ServiceAccountName: codexAuthRefresherServiceAccountName(secret.Namespace, secret.Name),
							SecurityContext: &corev1.PodSecurityContext{
								RunAsNonRoot: &runAsNonRoot,
							},
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers: []corev1.Container{{
								Name:            "codex-auth-refresher",
								Image:           image,
								ImagePullPolicy: b.ImagePullPolicy,
								Command:         []string{"/kelos/kelos-codex-auth-refresh"},
								Args: []string{
									"--namespace=" + secret.Namespace,
									"--secret=" + secret.Name,
								},
								SecurityContext: &corev1.SecurityContext{
									AllowPrivilegeEscalation: &allowPrivilegeEscalation,
									Capabilities: &corev1.Capabilities{
										Drop: []corev1.Capability{"ALL"},
									},
								},
								Resources: corev1.ResourceRequirements{
									Limits: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("100m"),
										corev1.ResourceMemory: resource.MustParse("64Mi"),
									},
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("10m"),
										corev1.ResourceMemory: resource.MustParse("32Mi"),
									},
								},
							}},
						},
					},
				},
			},
		},
	}
}

func (b *CodexAuthRefresherBuilder) BuildServiceAccount(secret *corev1.Secret) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:        codexAuthRefresherServiceAccountName(secret.Namespace, secret.Name),
			Namespace:   secret.Namespace,
			Labels:      codexAuthRefresherLabels(),
			Annotations: codexAuthSecretAnnotations(secret),
		},
	}
}

func (b *CodexAuthRefresherBuilder) BuildRole(secret *corev1.Secret) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:        CodexAuthRefresherCronJobName(secret.Namespace, secret.Name),
			Namespace:   secret.Namespace,
			Labels:      codexAuthRefresherLabels(),
			Annotations: codexAuthSecretAnnotations(secret),
		},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{secret.Name},
			Verbs:         []string{"get", "update"},
		}},
	}
}

func (b *CodexAuthRefresherBuilder) BuildRoleBinding(secret *corev1.Secret) *rbacv1.RoleBinding {
	name := CodexAuthRefresherCronJobName(secret.Namespace, secret.Name)
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   secret.Namespace,
			Labels:      codexAuthRefresherLabels(),
			Annotations: codexAuthSecretAnnotations(secret),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     name,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      codexAuthRefresherServiceAccountName(secret.Namespace, secret.Name),
			Namespace: secret.Namespace,
		}},
	}
}

func CodexAuthRefresherCronJobName(namespace, secretName string) string {
	source := namespace + "-" + secretName
	hash := sha256.Sum256([]byte(namespace + "/" + secretName))
	suffix := hex.EncodeToString(hash[:])[:10]
	baseMax := codexAuthRefresherCronJobNameMaxLength - len(codexAuthRefresherCronJobNamePrefix) - len("-") - len(suffix)
	base := dnsNameFragment(source)
	if len(base) > baseMax {
		base = strings.Trim(base[:baseMax], "-")
	}
	if base == "" {
		base = "secret"
	}
	return codexAuthRefresherCronJobNamePrefix + base + "-" + suffix
}

func codexAuthRefresherServiceAccountName(namespace, secretName string) string {
	return CodexAuthRefresherCronJobName(namespace, secretName)
}

func dnsNameFragment(s string) string {
	s = strings.ToLower(s)
	s = nonDNSNameChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

func IsCodexAuthRefreshable(secret *corev1.Secret) bool {
	return secret.Labels[codexauth.RefreshLabel] == "true" && len(secret.Data["CODEX_AUTH_JSON"]) > 0
}

func codexAuthRefresherLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":      "kelos",
		"app.kubernetes.io/component": codexAuthRefresherComponentLabel,
		"kelos.dev/managed-by":        "kelos-controller",
	}
}

func codexAuthSecretAnnotations(secret *corev1.Secret) map[string]string {
	return map[string]string{
		codexAuthSecretNamespaceAnnotation: secret.Namespace,
		codexAuthSecretNameAnnotation:      secret.Name,
	}
}
