package controller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func TestIsWebhookBased(t *testing.T) {
	tests := []struct {
		name string
		ts   *kelosv1alpha1.TaskSpawner
		want bool
	}{
		{
			name: "GitHub webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "Linear webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						LinearWebhook: &kelosv1alpha1.LinearWebhook{
							Types: []string{"Issue"},
						},
					},
				},
			},
			want: true,
		},
		{
			name: "polling TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubIssues: &kelosv1alpha1.GitHubIssues{},
					},
				},
			},
			want: false,
		},
		{
			name: "cron TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						Cron: &kelosv1alpha1.Cron{
							Schedule: "0 9 * * 1",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "generic webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GenericWebhook: &kelosv1alpha1.GenericWebhook{
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWebhookBased(tt.ts)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReconcileWebhook(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, kelosv1alpha1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, batchv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		ts             *kelosv1alpha1.TaskSpawner
		existingObjs   []client.Object
		isSuspended    bool
		wantPhase      kelosv1alpha1.TaskSpawnerPhase
		wantMessage    string
		wantDeployment bool
		wantCronJob    bool
	}{
		{
			name: "active webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			isSuspended: false,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseRunning,
			wantMessage: "Webhook-driven TaskSpawner ready",
		},
		{
			name: "suspended GitHub webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
			},
			isSuspended: true,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseSuspended,
			wantMessage: "Suspended by user",
		},
		{
			name: "suspended Linear webhook TaskSpawner",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook-linear",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						LinearWebhook: &kelosv1alpha1.LinearWebhook{
							Types: []string{"Issue"},
						},
					},
				},
			},
			isSuspended: true,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseSuspended,
			wantMessage: "Suspended by user",
		},
		{
			name: "webhook TaskSpawner with stale deployment",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
				Status: kelosv1alpha1.TaskSpawnerStatus{
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
								APIVersion: "kelos.dev/v1alpha1",
								Kind:       "TaskSpawner",
								Name:       "test-webhook",
								Controller: func() *bool { b := true; return &b }(),
							},
						},
					},
				},
			},
			isSuspended:    false,
			wantPhase:      kelosv1alpha1.TaskSpawnerPhaseRunning,
			wantMessage:    "Webhook-driven TaskSpawner ready",
			wantDeployment: false, // Should be deleted
		},
		{
			name: "webhook TaskSpawner with stale cronjob",
			ts: &kelosv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-webhook",
					Namespace: "default",
				},
				Spec: kelosv1alpha1.TaskSpawnerSpec{
					When: kelosv1alpha1.When{
						GitHubWebhook: &kelosv1alpha1.GitHubWebhook{
							Events: []string{"issues"},
						},
					},
				},
				Status: kelosv1alpha1.TaskSpawnerStatus{
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
								APIVersion: "kelos.dev/v1alpha1",
								Kind:       "TaskSpawner",
								Name:       "test-webhook",
								Controller: func() *bool { b := true; return &b }(),
							},
						},
					},
				},
			},
			isSuspended: false,
			wantPhase:   kelosv1alpha1.TaskSpawnerPhaseRunning,
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
				WithStatusSubresource(&kelosv1alpha1.TaskSpawner{}).
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
			var finalTs kelosv1alpha1.TaskSpawner
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
