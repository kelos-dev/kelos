package controller

import (
	"testing"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func int64Ptr(v int64) *int64 { return &v }

func TestBuildJobRunAsUser(t *testing.T) {
	baseTask := func() *axonv1alpha1.Task {
		return &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-task",
				Namespace: "default",
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   AgentTypeClaudeCode,
				Prompt: "Fix the bug",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeAPIKey,
					SecretRef: axonv1alpha1.SecretReference{Name: "test-secret"},
				},
			},
		}
	}

	baseWorkspace := &axonv1alpha1.WorkspaceSpec{
		Repo: "https://github.com/example/repo.git",
		Ref:  "main",
	}

	tests := []struct {
		name       string
		runAsUser  *int64
		workspace  *axonv1alpha1.WorkspaceSpec
		wantUID    int64
		wantNoSC   bool // expect no pod security context
		wantNoInit bool // expect no init containers
	}{
		{
			name:      "Default UID with workspace",
			runAsUser: nil,
			workspace: baseWorkspace,
			wantUID:   ClaudeCodeUID,
		},
		{
			name:      "Custom UID with workspace",
			runAsUser: int64Ptr(1000),
			workspace: baseWorkspace,
			wantUID:   1000,
		},
		{
			name:      "UID 0 with workspace",
			runAsUser: int64Ptr(0),
			workspace: baseWorkspace,
			wantUID:   0,
		},
		{
			name:       "No workspace omits security context",
			runAsUser:  int64Ptr(1000),
			workspace:  nil,
			wantNoSC:   true,
			wantNoInit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := baseTask()
			task.Spec.RunAsUser = tt.runAsUser

			builder := NewJobBuilder()
			job, err := builder.Build(task, tt.workspace)
			if err != nil {
				t.Fatalf("Build() error = %v", err)
			}

			podSpec := job.Spec.Template.Spec

			if tt.wantNoSC {
				if podSpec.SecurityContext != nil {
					t.Errorf("Expected no pod security context, got %+v", podSpec.SecurityContext)
				}
			} else {
				if podSpec.SecurityContext == nil {
					t.Fatal("Expected pod security context to be set")
				}
				if podSpec.SecurityContext.FSGroup == nil {
					t.Fatal("Expected FSGroup to be set")
				}
				if *podSpec.SecurityContext.FSGroup != tt.wantUID {
					t.Errorf("FSGroup = %d, want %d", *podSpec.SecurityContext.FSGroup, tt.wantUID)
				}
			}

			if tt.wantNoInit {
				if len(podSpec.InitContainers) != 0 {
					t.Errorf("Expected no init containers, got %d", len(podSpec.InitContainers))
				}
			} else {
				if len(podSpec.InitContainers) != 1 {
					t.Fatalf("Expected 1 init container, got %d", len(podSpec.InitContainers))
				}
				initContainer := podSpec.InitContainers[0]
				if initContainer.SecurityContext == nil {
					t.Fatal("Expected init container security context to be set")
				}
				if initContainer.SecurityContext.RunAsUser == nil {
					t.Fatal("Expected init container RunAsUser to be set")
				}
				if *initContainer.SecurityContext.RunAsUser != tt.wantUID {
					t.Errorf("Init container RunAsUser = %d, want %d", *initContainer.SecurityContext.RunAsUser, tt.wantUID)
				}
			}
		})
	}
}
