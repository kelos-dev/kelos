package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/kelos-dev/kelos/internal/codexauth"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCodexAuthRefresherBuilderBuildsPerSecretCronJob(t *testing.T) {
	secret := codexAuthSecret("prod", "codex-creds")
	builder := NewCodexAuthRefresherBuilder()
	builder.Schedule = "15 */4 * * *"
	builder.CodexImage = "codex:test"
	builder.ImagePullPolicy = corev1.PullAlways

	cronJob := builder.Build(secret)

	if cronJob.Namespace != "prod" {
		t.Fatalf("cronJob namespace = %q, want prod", cronJob.Namespace)
	}
	if len(cronJob.Name) > codexAuthRefresherCronJobNameMaxLength {
		t.Fatalf("cronJob name length = %d, want <= %d", len(cronJob.Name), codexAuthRefresherCronJobNameMaxLength)
	}
	if !strings.HasPrefix(cronJob.Name, "kelos-codex-auth-prod-codex-creds-") {
		t.Fatalf("cronJob name = %q", cronJob.Name)
	}
	if got := cronJob.Annotations[codexAuthSecretNamespaceAnnotation]; got != "prod" {
		t.Fatalf("source namespace annotation = %q, want prod", got)
	}
	if got := cronJob.Annotations[codexAuthSecretNameAnnotation]; got != "codex-creds" {
		t.Fatalf("source name annotation = %q, want codex-creds", got)
	}
	if cronJob.Spec.Schedule != "15 */4 * * *" {
		t.Fatalf("schedule = %q", cronJob.Spec.Schedule)
	}
	if cronJob.Spec.ConcurrencyPolicy != batchv1.ForbidConcurrent {
		t.Fatalf("concurrencyPolicy = %q, want Forbid", cronJob.Spec.ConcurrencyPolicy)
	}

	podSpec := cronJob.Spec.JobTemplate.Spec.Template.Spec
	wantServiceAccountName := codexAuthRefresherServiceAccountName("prod", "codex-creds")
	if podSpec.ServiceAccountName != wantServiceAccountName {
		t.Fatalf("serviceAccountName = %q, want %s", podSpec.ServiceAccountName, wantServiceAccountName)
	}
	if len(podSpec.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(podSpec.Containers))
	}
	container := podSpec.Containers[0]
	if container.Image != "codex:test" {
		t.Fatalf("image = %q, want codex:test", container.Image)
	}
	if got := container.Command; len(got) != 1 || got[0] != "/kelos/kelos-codex-auth-refresh" {
		t.Fatalf("command = %v", got)
	}
	wantArgs := []string{"--namespace=prod", "--secret=codex-creds"}
	if strings.Join(container.Args, ",") != strings.Join(wantArgs, ",") {
		t.Fatalf("args = %v, want %v", container.Args, wantArgs)
	}

	serviceAccount := builder.BuildServiceAccount(secret)
	if serviceAccount.Namespace != "prod" || serviceAccount.Name != wantServiceAccountName {
		t.Fatalf("serviceAccount = %s/%s", serviceAccount.Namespace, serviceAccount.Name)
	}
	if got := serviceAccount.Annotations[codexAuthSecretNameAnnotation]; got != "codex-creds" {
		t.Fatalf("serviceAccount source name annotation = %q, want codex-creds", got)
	}
	otherServiceAccount := builder.BuildServiceAccount(codexAuthSecret("prod", "other"))
	if otherServiceAccount.Name == serviceAccount.Name {
		t.Fatalf("serviceAccount name is shared across Secrets: %q", serviceAccount.Name)
	}

	role := builder.BuildRole(secret)
	if role.Namespace != "prod" || role.Name != cronJob.Name {
		t.Fatalf("role = %s/%s, want prod/%s", role.Namespace, role.Name, cronJob.Name)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("role rules = %d, want 1", len(role.Rules))
	}
	if got := strings.Join(role.Rules[0].ResourceNames, ","); got != "codex-creds" {
		t.Fatalf("role resourceNames = %q, want codex-creds", got)
	}
	if got := strings.Join(role.Rules[0].Verbs, ","); got != "get,update" {
		t.Fatalf("role verbs = %q, want get,update", got)
	}

	roleBinding := builder.BuildRoleBinding(secret)
	if roleBinding.Namespace != "prod" || roleBinding.Name != cronJob.Name {
		t.Fatalf("roleBinding = %s/%s, want prod/%s", roleBinding.Namespace, roleBinding.Name, cronJob.Name)
	}
	if roleBinding.RoleRef.Kind != "Role" || roleBinding.RoleRef.Name != role.Name {
		t.Fatalf("roleBinding roleRef = %s/%s, want Role/%s", roleBinding.RoleRef.Kind, roleBinding.RoleRef.Name, role.Name)
	}
	if len(roleBinding.Subjects) != 1 || roleBinding.Subjects[0].Name != wantServiceAccountName || roleBinding.Subjects[0].Namespace != "prod" {
		t.Fatalf("roleBinding subjects = %v", roleBinding.Subjects)
	}
}

func TestCodexAuthRefresherReconcilerCreatesCronJobForLabeledSecret(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	reconciler := newCodexAuthRefresherTestReconciler(secret)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var cronJob batchv1.CronJob
	key := types.NamespacedName{Namespace: "default", Name: CodexAuthRefresherCronJobName("default", "codex")}
	if err := reconciler.Get(ctx, key, &cronJob); err != nil {
		t.Fatalf("getting CronJob: %v", err)
	}
	if cronJob.Spec.Schedule != "0 */6 * * *" {
		t.Fatalf("schedule = %q", cronJob.Spec.Schedule)
	}
	wantServiceAccountName := codexAuthRefresherServiceAccountName("default", "codex")
	if got := cronJob.Spec.JobTemplate.Spec.Template.Spec.ServiceAccountName; got != wantServiceAccountName {
		t.Fatalf("serviceAccountName = %q, want %s", got, wantServiceAccountName)
	}
	assertNonBlockingOwnerReference(t, &cronJob, "codex")

	var serviceAccount corev1.ServiceAccount
	if err := reconciler.Get(ctx, types.NamespacedName{Namespace: "default", Name: wantServiceAccountName}, &serviceAccount); err != nil {
		t.Fatalf("getting ServiceAccount: %v", err)
	}
	assertNonBlockingOwnerReference(t, &serviceAccount, "codex")
	var role rbacv1.Role
	if err := reconciler.Get(ctx, key, &role); err != nil {
		t.Fatalf("getting Role: %v", err)
	}
	if len(role.Rules) != 1 || strings.Join(role.Rules[0].ResourceNames, ",") != "codex" || strings.Join(role.Rules[0].Verbs, ",") != "get,update" {
		t.Fatalf("role rules = %v", role.Rules)
	}
	assertNonBlockingOwnerReference(t, &role, "codex")
	var roleBinding rbacv1.RoleBinding
	if err := reconciler.Get(ctx, key, &roleBinding); err != nil {
		t.Fatalf("getting RoleBinding: %v", err)
	}
	if len(roleBinding.Subjects) != 1 || roleBinding.Subjects[0].Name != wantServiceAccountName {
		t.Fatalf("roleBinding subjects = %v", roleBinding.Subjects)
	}
	assertNonBlockingOwnerReference(t, &roleBinding, "codex")
}

func TestCodexAuthRefresherReconcilerUpdatesCronJobDrift(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	old := NewCodexAuthRefresherBuilder().Build(secret)
	old.Spec.Schedule = "0 0 * * *"
	old.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image = "old"
	reconciler := newCodexAuthRefresherTestReconciler(secret, old)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var cronJob batchv1.CronJob
	key := types.NamespacedName{Namespace: "default", Name: old.Name}
	if err := reconciler.Get(ctx, key, &cronJob); err != nil {
		t.Fatalf("getting CronJob: %v", err)
	}
	if cronJob.Spec.Schedule != "0 */6 * * *" {
		t.Fatalf("schedule = %q", cronJob.Spec.Schedule)
	}
	if image := cronJob.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Image; image != CodexImage {
		t.Fatalf("image = %q, want %q", image, CodexImage)
	}
}

func TestCodexAuthRefresherReconcilerIgnoresAPIDefaultedCronJobFields(t *testing.T) {
	secret := codexAuthSecret("default", "codex")
	desired := NewCodexAuthRefresherBuilder().Build(secret)
	current := desired.DeepCopy()
	podSpec := &current.Spec.JobTemplate.Spec.Template.Spec
	terminationGracePeriodSeconds := int64(30)
	enableServiceLinks := true
	podSpec.DNSPolicy = corev1.DNSClusterFirst
	podSpec.SchedulerName = "default-scheduler"
	podSpec.TerminationGracePeriodSeconds = &terminationGracePeriodSeconds
	podSpec.EnableServiceLinks = &enableServiceLinks
	podSpec.Containers[0].ImagePullPolicy = corev1.PullIfNotPresent
	podSpec.Containers[0].TerminationMessagePath = "/dev/termination-log"
	podSpec.Containers[0].TerminationMessagePolicy = corev1.TerminationMessageReadFile
	podSpec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("0.010")
	podSpec.Containers[0].Resources.Limits[corev1.ResourceCPU] = resource.MustParse("0.1")

	if updateCodexAuthRefresherCronJob(current, desired) {
		t.Fatalf("updateCodexAuthRefresherCronJob() = true, want false for API defaulted fields")
	}
	if podSpec.TerminationGracePeriodSeconds == nil || *podSpec.TerminationGracePeriodSeconds != terminationGracePeriodSeconds {
		t.Fatalf("terminationGracePeriodSeconds = %v, want API default preserved", podSpec.TerminationGracePeriodSeconds)
	}
	if got := podSpec.Containers[0].ImagePullPolicy; got != corev1.PullIfNotPresent {
		t.Fatalf("imagePullPolicy = %q, want API default preserved", got)
	}
}

func TestCodexAuthRefresherReconcilerDeletesCronJobWhenLabelRemoved(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	secret.Labels = nil
	reconciler := newCodexAuthRefresherTestReconciler(secret, cronJob)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got batchv1.CronJob
	key := types.NamespacedName{Namespace: "default", Name: cronJob.Name}
	if err := reconciler.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("CronJob get error = %v, want NotFound", err)
	}
}

func TestCodexAuthRefresherReconcilerDeletesCronJobWhenSecretDeleted(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	reconciler := newCodexAuthRefresherTestReconciler(cronJob)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got batchv1.CronJob
	key := types.NamespacedName{Namespace: "default", Name: cronJob.Name}
	if err := reconciler.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("CronJob get error = %v, want NotFound", err)
	}
}

func TestCodexAuthRefresherReconcilerDeletesCronJobWhenAuthJSONRemoved(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	secret.Data = nil
	reconciler := newCodexAuthRefresherTestReconciler(secret, cronJob)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	var got batchv1.CronJob
	key := types.NamespacedName{Namespace: "default", Name: cronJob.Name}
	if err := reconciler.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("CronJob get error = %v, want NotFound", err)
	}
}

func TestCodexAuthRefresherReconcilerDeletesAccessWhenLabelRemoved(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("default", "codex")
	builder := NewCodexAuthRefresherBuilder()
	cronJob := builder.Build(secret)
	serviceAccount := builder.BuildServiceAccount(secret)
	role := builder.BuildRole(secret)
	roleBinding := builder.BuildRoleBinding(secret)
	secret.Labels = nil
	reconciler := newCodexAuthRefresherTestReconciler(secret, cronJob, serviceAccount, role, roleBinding)

	_, err := reconciler.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "codex"}})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	key := types.NamespacedName{Namespace: "default", Name: cronJob.Name}
	var gotCronJob batchv1.CronJob
	if err := reconciler.Get(ctx, key, &gotCronJob); !apierrors.IsNotFound(err) {
		t.Fatalf("CronJob get error = %v, want NotFound", err)
	}
	var gotRoleBinding rbacv1.RoleBinding
	if err := reconciler.Get(ctx, key, &gotRoleBinding); !apierrors.IsNotFound(err) {
		t.Fatalf("RoleBinding get error = %v, want NotFound", err)
	}
	var gotRole rbacv1.Role
	if err := reconciler.Get(ctx, key, &gotRole); !apierrors.IsNotFound(err) {
		t.Fatalf("Role get error = %v, want NotFound", err)
	}
	var gotServiceAccount corev1.ServiceAccount
	serviceAccountKey := types.NamespacedName{Namespace: "default", Name: codexAuthRefresherServiceAccountName("default", "codex")}
	if err := reconciler.Get(ctx, serviceAccountKey, &gotServiceAccount); !apierrors.IsNotFound(err) {
		t.Fatalf("ServiceAccount get error = %v, want NotFound", err)
	}
}

func TestCodexAuthRefresherReconcilerRequestsForManagedCronJob(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("prod", "codex-creds")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	reconciler := newCodexAuthRefresherTestReconciler()

	if !isManagedCodexAuthRefresherCronJob(cronJob) {
		t.Fatalf("CronJob should be recognized as managed")
	}

	requests := reconciler.requestsForCronJob(ctx, cronJob)
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	want := types.NamespacedName{Namespace: "prod", Name: "codex-creds"}
	if requests[0].NamespacedName != want {
		t.Fatalf("request = %v, want %v", requests[0].NamespacedName, want)
	}
}

func TestCodexAuthRefresherReconcilerRequestsForManagedServiceAccount(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("prod", "codex-creds")
	otherNamespaceSecret := codexAuthSecret("other", "codex-creds")
	unlabeledSecret := codexAuthSecret("prod", "unlabeled")
	unlabeledSecret.Labels = nil
	emptySecret := codexAuthSecret("prod", "empty")
	emptySecret.Data = nil
	serviceAccount := NewCodexAuthRefresherBuilder().BuildServiceAccount(secret)
	reconciler := newCodexAuthRefresherTestReconciler(secret, otherNamespaceSecret, unlabeledSecret, emptySecret)

	if !isManagedCodexAuthRefresherServiceAccount(serviceAccount) {
		t.Fatalf("ServiceAccount should be recognized as managed")
	}

	requests := reconciler.requestsForServiceAccount(ctx, serviceAccount)
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	want := types.NamespacedName{Namespace: "prod", Name: "codex-creds"}
	if requests[0].NamespacedName != want {
		t.Fatalf("request = %v, want %v", requests[0].NamespacedName, want)
	}

	serviceAccount.Labels = nil
	serviceAccount.Annotations = nil
	requests = reconciler.requestsForServiceAccount(ctx, serviceAccount)
	if len(requests) != 1 || requests[0].NamespacedName != want {
		t.Fatalf("drifted ServiceAccount requests = %v, want %v", requests, want)
	}
}

func TestCodexAuthRefresherReconcilerRequestsForManagedRBACObject(t *testing.T) {
	ctx := context.Background()
	secret := codexAuthSecret("prod", "codex-creds")
	otherSecret := codexAuthSecret("prod", "other")
	unlabeledSecret := codexAuthSecret("prod", "unlabeled")
	unlabeledSecret.Labels = nil
	emptySecret := codexAuthSecret("prod", "empty")
	emptySecret.Data = nil
	builder := NewCodexAuthRefresherBuilder()
	reconciler := newCodexAuthRefresherTestReconciler(secret, otherSecret, unlabeledSecret, emptySecret)
	want := types.NamespacedName{Namespace: "prod", Name: "codex-creds"}

	for _, tt := range []struct {
		name string
		obj  client.Object
	}{
		{name: "Role", obj: builder.BuildRole(secret)},
		{name: "RoleBinding", obj: builder.BuildRoleBinding(secret)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if !isManagedCodexAuthRefresherObject(tt.obj) {
				t.Fatalf("%s should be recognized as managed", tt.name)
			}

			requests := reconciler.requestsForManagedObject(ctx, tt.obj)
			if len(requests) != 1 || requests[0].NamespacedName != want {
				t.Fatalf("requests = %v, want %v", requests, want)
			}

			tt.obj.SetLabels(nil)
			tt.obj.SetAnnotations(nil)
			requests = reconciler.requestsForManagedObject(ctx, tt.obj)
			if len(requests) != 1 || requests[0].NamespacedName != want {
				t.Fatalf("drifted %s requests = %v, want %v", tt.name, requests, want)
			}

			tt.obj.SetName("other")
			if requests := reconciler.requestsForManagedObject(ctx, tt.obj); len(requests) != 0 {
				t.Fatalf("renamed %s requests = %v, want none", tt.name, requests)
			}
		})
	}
}

func TestCodexAuthRefresherCronJobPredicateRequiresManagedLabelsAndSourceAnnotations(t *testing.T) {
	secret := codexAuthSecret("prod", "codex-creds")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)

	cronJob.Annotations = nil
	if isManagedCodexAuthRefresherCronJob(cronJob) {
		t.Fatalf("CronJob without source annotations should not be recognized as managed")
	}

	cronJob = NewCodexAuthRefresherBuilder().Build(secret)
	cronJob.Labels["app.kubernetes.io/component"] = "other"

	if isManagedCodexAuthRefresherCronJob(cronJob) {
		t.Fatalf("CronJob without managed labels should not be recognized as managed")
	}
}

func TestCodexAuthRefresherServiceAccountPredicateRequiresManagedMetadata(t *testing.T) {
	secret := codexAuthSecret("prod", "codex-creds")
	serviceAccount := NewCodexAuthRefresherBuilder().BuildServiceAccount(secret)

	if !isManagedCodexAuthRefresherServiceAccount(serviceAccount) {
		t.Fatalf("ServiceAccount should be recognized as managed")
	}

	serviceAccount.Name = "other"
	if isManagedCodexAuthRefresherServiceAccount(serviceAccount) {
		t.Fatalf("ServiceAccount with different name should not be recognized as managed")
	}

	serviceAccount = NewCodexAuthRefresherBuilder().BuildServiceAccount(secret)
	serviceAccount.Labels["app.kubernetes.io/component"] = "other"
	if isManagedCodexAuthRefresherServiceAccount(serviceAccount) {
		t.Fatalf("ServiceAccount without managed labels should not be recognized as managed")
	}

	serviceAccount = NewCodexAuthRefresherBuilder().BuildServiceAccount(secret)
	serviceAccount.Annotations = nil
	if isManagedCodexAuthRefresherServiceAccount(serviceAccount) {
		t.Fatalf("ServiceAccount without source annotations should not be recognized as managed")
	}
}

func TestCodexAuthRefresherReconcilerRequestsForCronJobRequiresSourceAnnotations(t *testing.T) {
	secret := codexAuthSecret("prod", "codex-creds")
	cronJob := NewCodexAuthRefresherBuilder().Build(secret)
	reconciler := newCodexAuthRefresherTestReconciler()

	cronJob.Annotations = nil
	if requests := reconciler.requestsForCronJob(context.Background(), cronJob); len(requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(requests))
	}
}

func newCodexAuthRefresherTestReconciler(objects ...client.Object) *CodexAuthRefresherReconciler {
	scheme := runtime.NewScheme()
	utilruntime.Must(corev1.AddToScheme(scheme))
	utilruntime.Must(batchv1.AddToScheme(scheme))
	utilruntime.Must(rbacv1.AddToScheme(scheme))
	return &CodexAuthRefresherReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		Scheme: scheme,
		Builder: &CodexAuthRefresherBuilder{
			Schedule:   DefaultCodexAuthRefreshSchedule,
			CodexImage: CodexImage,
		},
	}
}

func assertNonBlockingOwnerReference(t *testing.T, obj metav1.Object, ownerName string) {
	t.Helper()
	refs := obj.GetOwnerReferences()
	if len(refs) != 1 {
		t.Fatalf("ownerReferences = %d, want 1", len(refs))
	}
	if refs[0].Name != ownerName {
		t.Fatalf("ownerReference name = %q, want %q", refs[0].Name, ownerName)
	}
	if refs[0].BlockOwnerDeletion == nil || *refs[0].BlockOwnerDeletion {
		t.Fatalf("blockOwnerDeletion = %v, want false", refs[0].BlockOwnerDeletion)
	}
}

func codexAuthSecret(namespace, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				codexauth.RefreshLabel: "true",
			},
		},
		Data: map[string][]byte{
			"CODEX_AUTH_JSON": []byte(`{"tokens":{"refresh_token":"refresh"}}`),
		},
	}
}
