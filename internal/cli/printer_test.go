package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func TestPrintWorkspaceTable(t *testing.T) {
	workspaces := []axonv1alpha1.Workspace{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-one",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-1 * time.Hour)),
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/repo.git",
				Ref:  "main",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "ws-two",
				CreationTimestamp: metav1.NewTime(time.Now().Add(-24 * time.Hour)),
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/other.git",
			},
		},
	}

	var buf bytes.Buffer
	printWorkspaceTable(&buf, workspaces)
	output := buf.String()

	if !strings.Contains(output, "NAME") {
		t.Errorf("expected header NAME in output, got %q", output)
	}
	if !strings.Contains(output, "REPO") {
		t.Errorf("expected header REPO in output, got %q", output)
	}
	if !strings.Contains(output, "ws-one") {
		t.Errorf("expected ws-one in output, got %q", output)
	}
	if !strings.Contains(output, "ws-two") {
		t.Errorf("expected ws-two in output, got %q", output)
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Errorf("expected repo URL in output, got %q", output)
	}
}

func TestPrintWorkspaceDetail(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-workspace",
			Namespace: "default",
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "main",
			SecretRef: &axonv1alpha1.SecretReference{
				Name: "gh-token",
			},
		},
	}

	var buf bytes.Buffer
	printWorkspaceDetail(&buf, ws)
	output := buf.String()

	if !strings.Contains(output, "my-workspace") {
		t.Errorf("expected workspace name in output, got %q", output)
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Errorf("expected repo URL in output, got %q", output)
	}
	if !strings.Contains(output, "main") {
		t.Errorf("expected ref in output, got %q", output)
	}
	if !strings.Contains(output, "gh-token") {
		t.Errorf("expected secret name in output, got %q", output)
	}
}

func TestPrintWorkspaceDetailWithoutOptionalFields(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "minimal-ws",
			Namespace: "default",
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
		},
	}

	var buf bytes.Buffer
	printWorkspaceDetail(&buf, ws)
	output := buf.String()

	if !strings.Contains(output, "minimal-ws") {
		t.Errorf("expected workspace name in output, got %q", output)
	}
	if strings.Contains(output, "Ref:") {
		t.Errorf("expected no Ref field when ref is empty, got %q", output)
	}
	if strings.Contains(output, "Secret:") {
		t.Errorf("expected no Secret field when secretRef is nil, got %q", output)
	}
}
