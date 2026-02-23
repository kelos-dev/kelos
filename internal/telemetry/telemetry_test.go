package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	discoveryfake "k8s.io/client-go/discovery/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/version"
)

func newScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	_ = axonv1alpha1.AddToScheme(s)
	return s
}

func TestCollect(t *testing.T) {
	scheme := newScheme()

	tasks := []axonv1alpha1.Task{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "ns-a"},
			Spec:       axonv1alpha1.TaskSpec{Type: "claude-code"},
			Status: axonv1alpha1.TaskStatus{
				Phase: axonv1alpha1.TaskPhaseSucceeded,
				Results: map[string]string{
					"cost-usd":      "1.50",
					"input-tokens":  "1000",
					"output-tokens": "200",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-2", Namespace: "ns-a"},
			Spec:       axonv1alpha1.TaskSpec{Type: "claude-code"},
			Status:     axonv1alpha1.TaskStatus{Phase: axonv1alpha1.TaskPhaseFailed},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-3", Namespace: "ns-b"},
			Spec:       axonv1alpha1.TaskSpec{Type: "codex"},
			Status: axonv1alpha1.TaskStatus{
				Phase: axonv1alpha1.TaskPhaseSucceeded,
				Results: map[string]string{
					"cost-usd":      "0.50",
					"input-tokens":  "500",
					"output-tokens": "100",
				},
			},
		},
	}

	spawners := []axonv1alpha1.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "spawner-1", Namespace: "ns-a"},
			Spec: axonv1alpha1.TaskSpawnerSpec{
				When: axonv1alpha1.When{
					GitHubIssues: &axonv1alpha1.GitHubIssues{},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "spawner-2", Namespace: "ns-b"},
			Spec: axonv1alpha1.TaskSpawnerSpec{
				When: axonv1alpha1.When{
					Cron: &axonv1alpha1.Cron{Schedule: "0 * * * *"},
				},
			},
		},
	}

	agentConfigs := []axonv1alpha1.AgentConfig{
		{ObjectMeta: metav1.ObjectMeta{Name: "config-1", Namespace: "ns-a"}},
	}

	workspaces := []axonv1alpha1.Workspace{
		{ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "ns-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ws-2", Namespace: "ns-c"}},
	}

	// Pre-create the telemetry ConfigMap for installation ID.
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: defaultNamespace},
		Data:       map[string]string{installationIDKey: "test-install-id"},
	}

	objs := []runtime.Object{cm}
	for i := range tasks {
		objs = append(objs, &tasks[i])
	}
	for i := range spawners {
		objs = append(objs, &spawners[i])
	}
	for i := range agentConfigs {
		objs = append(objs, &agentConfigs[i])
	}
	for i := range workspaces {
		objs = append(objs, &workspaces[i])
	}

	c := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(objs...).
		Build()

	clientset := k8sfake.NewSimpleClientset()

	report, err := collect(context.Background(), c, clientset)
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	if report.InstallationID != "test-install-id" {
		t.Errorf("InstallationID = %q, want %q", report.InstallationID, "test-install-id")
	}

	if report.Tasks.Total != 3 {
		t.Errorf("Tasks.Total = %d, want 3", report.Tasks.Total)
	}
	if report.Tasks.ByType["claude-code"] != 2 {
		t.Errorf("Tasks.ByType[claude-code] = %d, want 2", report.Tasks.ByType["claude-code"])
	}
	if report.Tasks.ByType["codex"] != 1 {
		t.Errorf("Tasks.ByType[codex] = %d, want 1", report.Tasks.ByType["codex"])
	}
	if report.Tasks.ByPhase["Succeeded"] != 2 {
		t.Errorf("Tasks.ByPhase[Succeeded] = %d, want 2", report.Tasks.ByPhase["Succeeded"])
	}
	if report.Tasks.ByPhase["Failed"] != 1 {
		t.Errorf("Tasks.ByPhase[Failed] = %d, want 1", report.Tasks.ByPhase["Failed"])
	}

	if report.Usage.TotalCostUSD != 2.0 {
		t.Errorf("Usage.TotalCostUSD = %f, want 2.0", report.Usage.TotalCostUSD)
	}
	if report.Usage.TotalInputTokens != 1500 {
		t.Errorf("Usage.TotalInputTokens = %f, want 1500", report.Usage.TotalInputTokens)
	}
	if report.Usage.TotalOutputTokens != 300 {
		t.Errorf("Usage.TotalOutputTokens = %f, want 300", report.Usage.TotalOutputTokens)
	}

	if report.Features.TaskSpawners != 2 {
		t.Errorf("Features.TaskSpawners = %d, want 2", report.Features.TaskSpawners)
	}
	if report.Features.AgentConfigs != 1 {
		t.Errorf("Features.AgentConfigs = %d, want 1", report.Features.AgentConfigs)
	}
	if report.Features.Workspaces != 2 {
		t.Errorf("Features.Workspaces = %d, want 2", report.Features.Workspaces)
	}

	// Source types should be sorted.
	if len(report.Features.SourceTypes) != 2 {
		t.Fatalf("Features.SourceTypes length = %d, want 2", len(report.Features.SourceTypes))
	}
	if report.Features.SourceTypes[0] != "cron" || report.Features.SourceTypes[1] != "github" {
		t.Errorf("Features.SourceTypes = %v, want [cron, github]", report.Features.SourceTypes)
	}

	// ns-a, ns-b, ns-c from tasks/spawners/workspaces + axon-system from ConfigMap lookup.
	if report.Scale.Namespaces != 3 {
		t.Errorf("Scale.Namespaces = %d, want 3", report.Scale.Namespaces)
	}
}

func TestCollectEmpty(t *testing.T) {
	scheme := newScheme()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: defaultNamespace},
		Data:       map[string]string{installationIDKey: "empty-cluster-id"},
	}

	c := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(cm).
		Build()

	clientset := k8sfake.NewSimpleClientset()

	report, err := collect(context.Background(), c, clientset)
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	if report.Tasks.Total != 0 {
		t.Errorf("Tasks.Total = %d, want 0", report.Tasks.Total)
	}
	if report.Features.TaskSpawners != 0 {
		t.Errorf("Features.TaskSpawners = %d, want 0", report.Features.TaskSpawners)
	}
	if report.Features.AgentConfigs != 0 {
		t.Errorf("Features.AgentConfigs = %d, want 0", report.Features.AgentConfigs)
	}
	if report.Features.Workspaces != 0 {
		t.Errorf("Features.Workspaces = %d, want 0", report.Features.Workspaces)
	}
	if report.Scale.Namespaces != 0 {
		t.Errorf("Scale.Namespaces = %d, want 0", report.Scale.Namespaces)
	}
	if report.Usage.TotalCostUSD != 0 {
		t.Errorf("Usage.TotalCostUSD = %f, want 0", report.Usage.TotalCostUSD)
	}
}

func TestSend(t *testing.T) {
	var receivedBody []byte
	var receivedContentType string
	var receivedUserAgent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedContentType = r.Header.Get("Content-Type")
		receivedUserAgent = r.Header.Get("User-Agent")
		var err error
		receivedBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	report := &Report{
		InstallationID: "test-id",
		Version:        "v0.1.0",
		K8sVersion:     "v1.28.0",
		Tasks:          TaskReport{Total: 5, ByType: map[string]int{"claude-code": 5}},
	}

	err := send(context.Background(), server.URL, report)
	if err != nil {
		t.Fatalf("send() error: %v", err)
	}

	if receivedContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", receivedContentType, "application/json")
	}
	wantUA := "axon-telemetry/" + version.Version
	if receivedUserAgent != wantUA {
		t.Errorf("User-Agent = %q, want %q", receivedUserAgent, wantUA)
	}

	var decoded Report
	if err := json.Unmarshal(receivedBody, &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if decoded.InstallationID != "test-id" {
		t.Errorf("InstallationID = %q, want %q", decoded.InstallationID, "test-id")
	}
	if decoded.Tasks.Total != 5 {
		t.Errorf("Tasks.Total = %d, want 5", decoded.Tasks.Total)
	}
}

func TestSendFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	report := &Report{InstallationID: "test-id"}

	err := send(context.Background(), server.URL, report)
	if err == nil {
		t.Fatal("send() expected error for 500 response, got nil")
	}
}

func TestGetOrCreateInstallationID(t *testing.T) {
	scheme := newScheme()

	t.Run("creates new ConfigMap", func(t *testing.T) {
		c := clientfake.NewClientBuilder().WithScheme(scheme).Build()

		id, err := getOrCreateInstallationID(context.Background(), c, defaultNamespace)
		if err != nil {
			t.Fatalf("getOrCreateInstallationID() error: %v", err)
		}
		if id == "" {
			t.Fatal("expected non-empty installation ID")
		}

		// Verify ConfigMap was created.
		var cm corev1.ConfigMap
		if err := c.Get(context.Background(), types.NamespacedName{Name: configMapName, Namespace: defaultNamespace}, &cm); err != nil {
			t.Fatalf("getting configmap: %v", err)
		}
		if cm.Data[installationIDKey] != id {
			t.Errorf("ConfigMap ID = %q, want %q", cm.Data[installationIDKey], id)
		}
	})

	t.Run("returns existing ID", func(t *testing.T) {
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: defaultNamespace},
			Data:       map[string]string{installationIDKey: "existing-uuid"},
		}
		c := clientfake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(existingCM).
			Build()

		id, err := getOrCreateInstallationID(context.Background(), c, defaultNamespace)
		if err != nil {
			t.Fatalf("getOrCreateInstallationID() error: %v", err)
		}
		if id != "existing-uuid" {
			t.Errorf("ID = %q, want %q", id, "existing-uuid")
		}
	})

	t.Run("idempotent reads", func(t *testing.T) {
		c := clientfake.NewClientBuilder().WithScheme(scheme).Build()

		id1, err := getOrCreateInstallationID(context.Background(), c, defaultNamespace)
		if err != nil {
			t.Fatalf("first call error: %v", err)
		}

		id2, err := getOrCreateInstallationID(context.Background(), c, defaultNamespace)
		if err != nil {
			t.Fatalf("second call error: %v", err)
		}

		if id1 != id2 {
			t.Errorf("IDs differ: %q != %q", id1, id2)
		}
	})
}

func TestSourceTypeExtraction(t *testing.T) {
	tests := []struct {
		name string
		when *axonv1alpha1.When
		want []string
	}{
		{
			name: "github only",
			when: &axonv1alpha1.When{GitHubIssues: &axonv1alpha1.GitHubIssues{}},
			want: []string{"github"},
		},
		{
			name: "cron only",
			when: &axonv1alpha1.When{Cron: &axonv1alpha1.Cron{Schedule: "0 * * * *"}},
			want: []string{"cron"},
		},
		{
			name: "jira only",
			when: &axonv1alpha1.When{Jira: &axonv1alpha1.Jira{BaseURL: "https://example.atlassian.net", Project: "TEST"}},
			want: []string{"jira"},
		},
		{
			name: "empty when",
			when: &axonv1alpha1.When{},
			want: nil,
		},
		{
			name: "multiple sources",
			when: &axonv1alpha1.When{
				GitHubIssues: &axonv1alpha1.GitHubIssues{},
				Cron:         &axonv1alpha1.Cron{Schedule: "0 * * * *"},
			},
			want: []string{"github", "cron"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSourceTypes(tt.when)
			if len(got) != len(tt.want) {
				t.Fatalf("extractSourceTypes() = %v, want %v", got, tt.want)
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Errorf("extractSourceTypes()[%d] = %q, want %q", i, v, tt.want[i])
				}
			}
		})
	}
}

func TestCollectK8sVersionFallback(t *testing.T) {
	scheme := newScheme()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: defaultNamespace},
		Data:       map[string]string{installationIDKey: "test-id"},
	}

	c := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(cm).
		Build()

	// The fake clientset returns an empty version. Verify we handle it.
	clientset := k8sfake.NewSimpleClientset()
	fakeDiscovery, ok := clientset.Discovery().(*discoveryfake.FakeDiscovery)
	if ok {
		fakeDiscovery.FakedServerVersion = nil
	}

	report, err := collect(context.Background(), c, clientset)
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	// The fake discovery returns empty version, not an error.
	if report.K8sVersion == "" {
		t.Error("expected non-empty K8sVersion")
	}
}

func TestRun(t *testing.T) {
	scheme := newScheme()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: defaultNamespace},
		Data:       map[string]string{installationIDKey: "run-test-id"},
	}

	c := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithRuntimeObjects(cm).
		Build()

	clientset := k8sfake.NewSimpleClientset()

	var receivedReport Report
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &receivedReport)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := Run(context.Background(), c, clientset, server.URL)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if receivedReport.InstallationID != "run-test-id" {
		t.Errorf("InstallationID = %q, want %q", receivedReport.InstallationID, "run-test-id")
	}
}
