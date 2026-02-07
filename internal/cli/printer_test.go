package cli

import (
	"bytes"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func TestPrintWorkspaceTable(t *testing.T) {
	workspaces := []axonv1alpha1.Workspace{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ws-1",
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/repo1.git",
				Ref:  "main",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ws-2",
			},
			Spec: axonv1alpha1.WorkspaceSpec{
				Repo: "https://github.com/org/repo2.git",
			},
		},
	}

	var buf bytes.Buffer
	printWorkspaceTable(&buf, workspaces)

	output := buf.String()
	if !strings.Contains(output, "NAME") {
		t.Error("expected NAME header")
	}
	if !strings.Contains(output, "REPO") {
		t.Error("expected REPO header")
	}
	if !strings.Contains(output, "REF") {
		t.Error("expected REF header")
	}
	if !strings.Contains(output, "AGE") {
		t.Error("expected AGE header")
	}
	if !strings.Contains(output, "ws-1") {
		t.Error("expected ws-1 in output")
	}
	if !strings.Contains(output, "ws-2") {
		t.Error("expected ws-2 in output")
	}
	if !strings.Contains(output, "https://github.com/org/repo1.git") {
		t.Error("expected repo1 URL in output")
	}
}

func TestPrintWorkspaceTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	printWorkspaceTable(&buf, nil)

	output := buf.String()
	if !strings.Contains(output, "NAME") {
		t.Error("expected header even for empty list")
	}
}

func TestPrintWorkspaceDetail(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-ws",
			Namespace: "test-ns",
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
			Ref:  "develop",
			SecretRef: &axonv1alpha1.SecretReference{
				Name: "my-secret",
			},
		},
	}

	var buf bytes.Buffer
	printWorkspaceDetail(&buf, ws)

	output := buf.String()
	if !strings.Contains(output, "my-ws") {
		t.Error("expected workspace name in output")
	}
	if !strings.Contains(output, "test-ns") {
		t.Error("expected namespace in output")
	}
	if !strings.Contains(output, "https://github.com/org/repo.git") {
		t.Error("expected repo in output")
	}
	if !strings.Contains(output, "develop") {
		t.Error("expected ref in output")
	}
	if !strings.Contains(output, "my-secret") {
		t.Error("expected secret in output")
	}
}

func TestPrintWorkspaceDetail_Minimal(t *testing.T) {
	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "basic-ws",
			Namespace: "default",
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/org/repo.git",
		},
	}

	var buf bytes.Buffer
	printWorkspaceDetail(&buf, ws)

	output := buf.String()
	if !strings.Contains(output, "basic-ws") {
		t.Error("expected workspace name in output")
	}
	if strings.Contains(output, "Ref:") {
		t.Error("expected no Ref field when ref is empty")
	}
	if strings.Contains(output, "Secret:") {
		t.Error("expected no Secret field when secretRef is nil")
	}
}
