package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func TestIsWebhookBased(t *testing.T) {
	tests := []struct {
		name string
		ts   *kelos.TaskSpawner
		want bool
	}{
		{
			name: "GitHub webhook TaskSpawner",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubWebhook: &kelos.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "Linear webhook TaskSpawner",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						LinearWebhook: &kelos.LinearWebhook{
							Types: []string{"Issue"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "polling TaskSpawner",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{},
					},
				},
			},
			want: false,
		},
		{
			name: "cron TaskSpawner",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						Cron: &kelos.Cron{
							Schedule: "0 9 * * 1",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "generic webhook TaskSpawner",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GenericWebhook: &kelos.GenericWebhook{
							Source: "notion",
							FieldMapping: map[string]string{
								"id": "$.data.id",
							},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "Slack TaskSpawner",
			ts: &kelos.TaskSpawner{
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						Slack: &kelos.Slack{
							Channels: []string{"C0123456789"},
						},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWebhookBased(tt.ts)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReconcileDeploymentResolvesEffectiveWorkspace(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelos.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name     string
		template kelos.TaskTemplate
		extraObj client.Object
		wantURL  string
	}{
		{
			name: "worker workspaceRef",
			template: kelos.TaskTemplate{
				Worker: &kelos.WorkerSpec{
					WorkspaceRef: &kelos.WorkspaceReference{Name: "worker-workspace"},
				},
			},
			wantURL: WorkspaceGHProxyServiceURL("default", "worker-workspace"),
		},
		{
			name: "workerPoolRef workspace",
			template: kelos.TaskTemplate{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool"},
			},
			extraObj: &kelos.WorkerPool{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "pool",
					Namespace: "default",
				},
				Spec: kelos.WorkerPoolSpec{
					Worker: kelos.WorkerSpec{
						WorkspaceRef: &kelos.WorkspaceReference{Name: "worker-workspace"},
					},
				},
			},
			wantURL: WorkspaceGHProxyServiceURL("default", "worker-workspace"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "spawner",
					Namespace: "default",
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubIssues: &kelos.GitHubIssues{},
					},
					TaskTemplate: tt.template,
				},
			}
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "worker-workspace",
					Namespace: "default",
				},
				Spec: kelos.WorkspaceSpec{
					Repo:    "https://github.com/example/repo.git",
					GHProxy: &kelos.WorkspaceGHProxy{},
				},
			}
			objects := []client.Object{ts, ws}
			if tt.extraObj != nil {
				objects = append(objects, tt.extraObj)
			}

			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.TaskSpawner{}).
				WithObjects(objects...).
				Build()
			r := &TaskSpawnerReconciler{
				Client:            cl,
				Scheme:            scheme,
				DeploymentBuilder: NewDeploymentBuilder(),
			}

			_, err := r.reconcileDeployment(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "spawner", Namespace: "default"},
			}, ts, false)
			require.NoError(t, err)

			var deploy appsv1.Deployment
			err = cl.Get(context.Background(), types.NamespacedName{Name: "spawner", Namespace: "default"}, &deploy)
			require.NoError(t, err)
			args := deploy.Spec.Template.Spec.Containers[0].Args
			assert.Contains(t, args, "--github-owner=example")
			assert.Contains(t, args, "--github-repo=repo")
			assert.Contains(t, args, "--gh-proxy-url="+tt.wantURL)
		})
	}
}

func TestReconcileDeploymentRequeuesWhenWorkspaceSecretMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelos.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ts := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner",
			Namespace: "default",
		},
		Spec: kelos.TaskSpawnerSpec{
			When: kelos.When{
				GitHubIssues: &kelos.GitHubIssues{},
			},
			TaskTemplate: kelos.TaskTemplate{
				WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"},
			},
		},
	}
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workspace",
			Namespace: "default",
		},
		Spec: kelos.WorkspaceSpec{
			Repo: "https://github.com/example/repo.git",
			SecretRef: &kelos.SecretReference{
				Name: "github-app-creds",
			},
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskSpawner{}).
		WithObjects(ts, ws).
		Build()
	r := &TaskSpawnerReconciler{
		Client:            cl,
		Scheme:            scheme,
		DeploymentBuilder: NewDeploymentBuilder(),
	}

	result, err := r.reconcileDeployment(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "spawner", Namespace: "default"},
	}, ts, false)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, result.RequeueAfter)

	var deploy appsv1.Deployment
	err = cl.Get(context.Background(), types.NamespacedName{Name: "spawner", Namespace: "default"}, &deploy)
	assert.True(t, apierrors.IsNotFound(err), "expected no Deployment while workspace secret is missing")
}

func TestReconcileDeploymentFailsOnInvalidWorkspace(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelos.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	tests := []struct {
		name        string
		when        kelos.When
		workspace   kelos.WorkspaceSpec
		secret      *corev1.Secret
		wantMessage string
	}{
		{
			name:        "gitlab source with github workspace",
			when:        kelos.When{GitLab: &kelos.GitLab{}},
			workspace:   kelos.WorkspaceSpec{Repo: "https://gitlab.example.com/group/repo.git", SecretRef: &kelos.SecretReference{Name: "token"}},
			secret:      &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}, Data: map[string][]byte{"GITHUB_TOKEN": []byte("glpat")}},
			wantMessage: "requires a Workspace with provider gitlab",
		},
		{
			name:        "gitlab workspace secret without GITLAB_TOKEN",
			when:        kelos.When{GitLab: &kelos.GitLab{}},
			workspace:   kelos.WorkspaceSpec{Repo: "https://gitlab.example.com/group/repo.git", Provider: kelos.WorkspaceProviderGitLab, SecretRef: &kelos.SecretReference{Name: "token"}},
			secret:      &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}, Data: map[string][]byte{"GITHUB_TOKEN": []byte("glpat")}},
			wantMessage: `secret "token" has no GITLAB_TOKEN key`,
		},
		{
			name:        "github source with gitlab workspace",
			when:        kelos.When{GitHubIssues: &kelos.GitHubIssues{}},
			workspace:   kelos.WorkspaceSpec{Repo: "https://gitlab.example.com/group/repo.git", Provider: kelos.WorkspaceProviderGitLab, SecretRef: &kelos.SecretReference{Name: "token"}},
			secret:      &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}, Data: map[string][]byte{"GITLAB_TOKEN": []byte("glpat")}},
			wantMessage: "requires a Workspace with provider github",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{Name: "spawner", Namespace: "default"},
				Spec: kelos.TaskSpawnerSpec{
					When:         tt.when,
					TaskTemplate: kelos.TaskTemplate{WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"}},
				},
			}
			ws := &kelos.Workspace{
				ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: "default"},
				Spec:       tt.workspace,
			}
			// A Deployment left over from an earlier, valid configuration must
			// be removed so the stale spawner stops polling.
			staleDeploy := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "spawner", Namespace: "default"},
				Spec: appsv1.DeploymentSpec{
					Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"kelos.dev/taskspawner": "spawner"}},
				},
			}

			cl := fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kelos.TaskSpawner{}).
				WithObjects(ts, ws, tt.secret, staleDeploy).
				Build()
			r := &TaskSpawnerReconciler{
				Client:            cl,
				Scheme:            scheme,
				DeploymentBuilder: NewDeploymentBuilder(),
			}

			result, err := r.reconcileDeployment(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "spawner", Namespace: "default"},
			}, ts, false)
			require.NoError(t, err)
			assert.Equal(t, ctrl.Result{}, result, "invalid workspace must not be requeued")

			var updated kelos.TaskSpawner
			require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "spawner", Namespace: "default"}, &updated))
			assert.Equal(t, kelos.TaskSpawnerPhaseFailed, updated.Status.Phase)
			assert.Contains(t, updated.Status.Message, tt.wantMessage)

			var deploy appsv1.Deployment
			err = cl.Get(context.Background(), types.NamespacedName{Name: "spawner", Namespace: "default"}, &deploy)
			assert.True(t, apierrors.IsNotFound(err), "expected the stale Deployment to be deleted")
		})
	}
}

func TestReconcileWebhookFailsOnInvalidWorkspace(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelos.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ts := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "spawner", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			When:         kelos.When{GitLabWebhook: &kelos.GitLabWebhook{Events: []string{"note"}}},
			TaskTemplate: kelos.TaskTemplate{WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"}},
		},
	}
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: "default"},
		Spec:       kelos.WorkspaceSpec{Repo: "https://gitlab.example.com/group/repo.git", SecretRef: &kelos.SecretReference{Name: "token"}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "token", Namespace: "default"}, Data: map[string][]byte{"GITHUB_TOKEN": []byte("glpat")}}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskSpawner{}).
		WithObjects(ts, ws, secret).
		Build()
	r := &TaskSpawnerReconciler{Client: cl, Scheme: scheme, DeploymentBuilder: NewDeploymentBuilder()}

	result, err := r.reconcileWebhook(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "spawner", Namespace: "default"},
	}, ts, false)
	require.NoError(t, err)
	assert.Equal(t, ctrl.Result{}, result, "invalid workspace must not be requeued")

	var updated kelos.TaskSpawner
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "spawner", Namespace: "default"}, &updated))
	assert.Equal(t, kelos.TaskSpawnerPhaseFailed, updated.Status.Phase)
	assert.Contains(t, updated.Status.Message, "requires a Workspace with provider gitlab")
}

func TestReconcileDeploymentGitLabWorkspaceInjectsGitLabToken(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelos.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))

	ts := &kelos.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{Name: "spawner", Namespace: "default"},
		Spec: kelos.TaskSpawnerSpec{
			When:         kelos.When{GitLab: &kelos.GitLab{}},
			TaskTemplate: kelos.TaskTemplate{WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"}},
		},
	}
	ws := &kelos.Workspace{
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: "default"},
		Spec: kelos.WorkspaceSpec{
			Repo:      "https://gitlab.example.com/group/repo.git",
			Provider:  kelos.WorkspaceProviderGitLab,
			SecretRef: &kelos.SecretReference{Name: "gitlab-token"},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gitlab-token", Namespace: "default"},
		Data:       map[string][]byte{"GITLAB_TOKEN": []byte("glpat")},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&kelos.TaskSpawner{}).
		WithObjects(ts, ws, secret).
		Build()
	r := &TaskSpawnerReconciler{Client: cl, Scheme: scheme, DeploymentBuilder: NewDeploymentBuilder()}

	_, err := r.reconcileDeployment(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "spawner", Namespace: "default"},
	}, ts, false)
	require.NoError(t, err)

	var deploy appsv1.Deployment
	require.NoError(t, cl.Get(context.Background(), types.NamespacedName{Name: "spawner", Namespace: "default"}, &deploy))
	env := deploy.Spec.Template.Spec.Containers[0].Env
	require.Len(t, env, 1)
	assert.Equal(t, "GITLAB_TOKEN", env[0].Name)
	assert.Equal(t, "gitlab-token", env[0].ValueFrom.SecretKeyRef.Name)
	assert.Equal(t, "GITLAB_TOKEN", env[0].ValueFrom.SecretKeyRef.Key)
}

func TestReconcileWebhook(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelos.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		ts             *kelos.TaskSpawner
		existingObjs   []client.Object
		isSuspended    bool
		wantPhase      kelos.TaskSpawnerPhase
		wantMessage    string
		wantDeployment bool
		wantCronJob    bool
	}{
		{
			name: "active webhook TaskSpawner",
			ts: &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubWebhook: &kelos.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			isSuspended: false,
			wantPhase:   kelos.TaskSpawnerPhaseRunning,
			wantMessage: "Webhook-driven TaskSpawner ready",
		},
		{
			name: "suspended GitHub webhook TaskSpawner",
			ts: &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubWebhook: &kelos.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			isSuspended: true,
			wantPhase:   kelos.TaskSpawnerPhaseSuspended,
			wantMessage: "Suspended by user",
		},
		{
			name: "suspended Linear webhook TaskSpawner",
			ts: &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook-linear",
					Namespace: "default",
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						LinearWebhook: &kelos.LinearWebhook{
							Types: []string{"Issue"},
						},
					},
				},
			},
			isSuspended: true,
			wantPhase:   kelos.TaskSpawnerPhaseSuspended,
			wantMessage: "Suspended by user",
		},
		{
			name: "webhook TaskSpawner with stale deployment",
			ts: &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubWebhook: &kelos.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
				Status: kelos.TaskSpawnerStatus{
					DeploymentName: "test-webhook",
				},
			},
			existingObjs: []client.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-webhook",
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kelos.dev/v1alpha2",
								Kind:       "TaskSpawner",
								Name:       "test-webhook",
								Controller: func() *bool { b := true; return &b }(),
							},
						},
					},
				},
			},
			isSuspended:    false,
			wantPhase:      kelos.TaskSpawnerPhaseRunning,
			wantMessage:    "Webhook-driven TaskSpawner ready",
			wantDeployment: false, // Should be deleted
		},
		{
			name: "webhook TaskSpawner with stale cronjob",
			ts: &kelos.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelos.TaskSpawnerSpec{
					When: kelos.When{
						GitHubWebhook: &kelos.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
				Status: kelos.TaskSpawnerStatus{
					CronJobName: "test-webhook",
				},
			},
			existingObjs: []client.Object{
				&batchv1.CronJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-webhook",
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "kelos.dev/v1alpha2",
								Kind:       "TaskSpawner",
								Name:       "test-webhook",
								Controller: func() *bool { b := true; return &b }(),
							},
						},
					},
				},
			},
			isSuspended: false,
			wantPhase:   kelos.TaskSpawnerPhaseRunning,
			wantMessage: "Webhook-driven TaskSpawner ready",
			wantCronJob: false, // Should be deleted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := append([]client.Object{tt.ts}, tt.existingObjs...)
			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				WithStatusSubresource(&kelos.TaskSpawner{}).
				Build()

			reconciler := &TaskSpawnerReconciler{
				Client: client,
				Scheme: scheme,
			}

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.ts.Name,
					Namespace: tt.ts.Namespace,
				},
			}

			_, err := reconciler.reconcileWebhook(context.Background(), req, tt.ts, tt.isSuspended)
			require.NoError(t, err)

			// Check final TaskSpawner status
			var finalTs kelos.TaskSpawner
			err = client.Get(context.Background(), req.NamespacedName, &finalTs)
			require.NoError(t, err)

			assert.Equal(t, tt.wantPhase, finalTs.Status.Phase)
			assert.Equal(t, tt.wantMessage, finalTs.Status.Message)
			assert.Empty(t, finalTs.Status.DeploymentName, "DeploymentName should be cleared")
			assert.Empty(t, finalTs.Status.CronJobName, "CronJobName should be cleared")

			// Check that stale resources are deleted
			var deployment appsv1.Deployment
			err = client.Get(context.Background(), req.NamespacedName, &deployment)
			if tt.wantDeployment {
				assert.NoError(t, err, "Deployment should exist")
			} else {
				assert.True(t, apierrors.IsNotFound(err), "Deployment should not exist")
			}

			var cronJob batchv1.CronJob
			err = client.Get(context.Background(), req.NamespacedName, &cronJob)
			if tt.wantCronJob {
				assert.NoError(t, err, "CronJob should exist")
			} else {
				assert.True(t, apierrors.IsNotFound(err), "CronJob should not exist")
			}
		})
	}
}
