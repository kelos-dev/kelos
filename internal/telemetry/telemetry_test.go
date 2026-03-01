package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := kelosv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCollect(t *testing.T) {
	s := newScheme(t)

	tasks := []kelosv1alpha1.Task{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "ns-a"},
			Spec:       kelosv1alpha1.TaskSpec{Type: "claude-code"},
			Status: kelosv1alpha1.TaskStatus{
				Phase: kelosv1alpha1.TaskPhaseSucceeded,
				Results: map[string]string{
					"cost_usd":      "1.50",
					"input_tokens":  "1000",
					"output_tokens": "500",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-2", Namespace: "ns-a"},
			Spec:       kelosv1alpha1.TaskSpec{Type: "claude-code"},
			Status: kelosv1alpha1.TaskStatus{
				Phase: kelosv1alpha1.TaskPhaseFailed,
				Results: map[string]string{
					"cost_usd":      "0.50",
					"input_tokens":  "200",
					"output_tokens": "100",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-3", Namespace: "ns-b"},
			Spec:       kelosv1alpha1.TaskSpec{Type: "codex"},
			Status:     kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseRunning},
		},
	}

	spawners := []kelosv1alpha1.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "spawner-1", Namespace: "ns-a"},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{GitHubIssues: &kelosv1alpha1.GitHubIssues{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "spawner-2", Namespace: "ns-b"},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{Cron: &kelosv1alpha1.Cron{Schedule: "0 * * * *"}},
			},
		},
	}

	agentConfigs := []kelosv1alpha1.AgentConfig{
		{ObjectMeta: metav1.ObjectMeta{Name: "config-1", Namespace: "ns-a"}},
	}

	workspaces := []kelosv1alpha1.Workspace{
		{ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "ns-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ws-2", Namespace: "ns-c"}},
	}

	// Build the fake client with objects.
	objs := make([]runtime.Object, 0)
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
	// Pre-create the telemetry ConfigMap so we don't depend on Create.
	objs = append(objs, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
		Data:       map[string]string{installationIDKey: "test-install-id"},
	})

	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()

	cs := fakeclientset.NewSimpleClientset()

	report, err := collect(context.Background(), c, cs)
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	// Verify task counts.
	if report.Tasks.Total != 3 {
		t.Errorf("Tasks.Total = %d, want 3", report.Tasks.Total)
	}
	if report.Tasks.ByType["claude-code"] != 2 {
		t.Errorf("Tasks.ByType[claude-code] = %d, want 2", report.Tasks.ByType["claude-code"])
	}
	if report.Tasks.ByType["codex"] != 1 {
		t.Errorf("Tasks.ByType[codex] = %d, want 1", report.Tasks.ByType["codex"])
	}
	if report.Tasks.ByPhase["Succeeded"] != 1 {
		t.Errorf("Tasks.ByPhase[Succeeded] = %d, want 1", report.Tasks.ByPhase["Succeeded"])
	}
	if report.Tasks.ByPhase["Failed"] != 1 {
		t.Errorf("Tasks.ByPhase[Failed] = %d, want 1", report.Tasks.ByPhase["Failed"])
	}
	if report.Tasks.ByPhase["Running"] != 1 {
		t.Errorf("Tasks.ByPhase[Running] = %d, want 1", report.Tasks.ByPhase["Running"])
	}

	// Verify usage.
	if report.Usage.TotalCostUSD != 2.0 {
		t.Errorf("Usage.TotalCostUSD = %f, want 2.0", report.Usage.TotalCostUSD)
	}
	if report.Usage.TotalInputTokens != 1200 {
		t.Errorf("Usage.TotalInputTokens = %f, want 1200", report.Usage.TotalInputTokens)
	}
	if report.Usage.TotalOutputTokens != 600 {
		t.Errorf("Usage.TotalOutputTokens = %f, want 600", report.Usage.TotalOutputTokens)
	}

	// Verify features.
	if report.Features.TaskSpawners != 2 {
		t.Errorf("Features.TaskSpawners = %d, want 2", report.Features.TaskSpawners)
	}
	if report.Features.AgentConfigs != 1 {
		t.Errorf("Features.AgentConfigs = %d, want 1", report.Features.AgentConfigs)
	}
	if report.Features.Workspaces != 2 {
		t.Errorf("Features.Workspaces = %d, want 2", report.Features.Workspaces)
	}

	sort.Strings(report.Features.SourceTypes)
	if len(report.Features.SourceTypes) != 2 {
		t.Fatalf("Features.SourceTypes length = %d, want 2", len(report.Features.SourceTypes))
	}
	if report.Features.SourceTypes[0] != "cron" || report.Features.SourceTypes[1] != "github" {
		t.Errorf("Features.SourceTypes = %v, want [cron github]", report.Features.SourceTypes)
	}

	// Verify scale (ns-a, ns-b, ns-c = 3 namespaces).
	if report.Scale.Namespaces != 3 {
		t.Errorf("Scale.Namespaces = %d, want 3", report.Scale.Namespaces)
	}

	// Verify installation ID was read from ConfigMap.
	if report.InstallationID != "test-install-id" {
		t.Errorf("InstallationID = %q, want %q", report.InstallationID, "test-install-id")
	}
}

func TestCollectEmpty(t *testing.T) {
	s := newScheme(t)

	// Only the telemetry ConfigMap, no resources.
	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
			Data:       map[string]string{installationIDKey: "empty-id"},
		},
	).Build()

	cs := fakeclientset.NewSimpleClientset()
	report, err := collect(context.Background(), c, cs)
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
			t.Fatalf("reading request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	report := &Report{
		InstallationID: "test-id",
		Version:        "v0.1.0",
		K8sVersion:     "v1.30.0",
		Tasks: TaskReport{
			Total:   5,
			ByType:  map[string]int{"claude-code": 5},
			ByPhase: map[string]int{"Succeeded": 5},
		},
	}

	err := send(context.Background(), server.URL, report)
	if err != nil {
		t.Fatalf("send() error: %v", err)
	}

	if receivedContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", receivedContentType, "application/json")
	}
	if receivedUserAgent == "" {
		t.Error("User-Agent header was empty")
	}

	var received Report
	if err := json.Unmarshal(receivedBody, &received); err != nil {
		t.Fatalf("unmarshaling received body: %v", err)
	}
	if received.InstallationID != "test-id" {
		t.Errorf("received InstallationID = %q, want %q", received.InstallationID, "test-id")
	}
	if received.Tasks.Total != 5 {
		t.Errorf("received Tasks.Total = %d, want 5", received.Tasks.Total)
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
		t.Fatal("send() expected error for 500 status, got nil")
	}
}

func TestSendConnectionError(t *testing.T) {
	report := &Report{InstallationID: "test-id"}
	err := send(context.Background(), "http://localhost:1", report)
	if err == nil {
		t.Fatal("send() expected error for connection refused, got nil")
	}
}

func TestGetOrCreateInstallationID(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).Build()

	// First call should create the ConfigMap.
	id1, err := getOrCreateInstallationID(context.Background(), c, systemNamespace)
	if err != nil {
		t.Fatalf("getOrCreateInstallationID() error: %v", err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty installation ID")
	}

	// Second call should return the same ID.
	id2, err := getOrCreateInstallationID(context.Background(), c, systemNamespace)
	if err != nil {
		t.Fatalf("getOrCreateInstallationID() second call error: %v", err)
	}
	if id1 != id2 {
		t.Errorf("installation ID changed: %q -> %q", id1, id2)
	}
}

func TestGetOrCreateInstallationIDExistingEmptyConfigMap(t *testing.T) {
	s := newScheme(t)
	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
			Data:       map[string]string{},
		},
	).Build()

	id, err := getOrCreateInstallationID(context.Background(), c, systemNamespace)
	if err != nil {
		t.Fatalf("getOrCreateInstallationID() error: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty installation ID")
	}
}

func TestSourceTypeExtraction(t *testing.T) {
	s := newScheme(t)

	spawners := []kelosv1alpha1.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "ns"},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{GitHubIssues: &kelosv1alpha1.GitHubIssues{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s2", Namespace: "ns"},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{Cron: &kelosv1alpha1.Cron{Schedule: "0 * * * *"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s3", Namespace: "ns"},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{Jira: &kelosv1alpha1.Jira{
					BaseURL:   "https://jira.example.com",
					Project:   "PROJ",
					SecretRef: kelosv1alpha1.SecretReference{Name: "jira-secret"},
				}},
			},
		},
		// Duplicate GitHub source type — should only appear once.
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s4", Namespace: "ns"},
			Spec: kelosv1alpha1.TaskSpawnerSpec{
				When: kelosv1alpha1.When{GitHubIssues: &kelosv1alpha1.GitHubIssues{}},
			},
		},
	}

	objs := make([]runtime.Object, 0)
	for i := range spawners {
		objs = append(objs, &spawners[i])
	}
	objs = append(objs, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
		Data:       map[string]string{installationIDKey: "test-id"},
	})

	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()
	cs := fakeclientset.NewSimpleClientset()

	report, err := collect(context.Background(), c, cs)
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	sort.Strings(report.Features.SourceTypes)
	expected := []string{"cron", "github", "jira"}
	if len(report.Features.SourceTypes) != len(expected) {
		t.Fatalf("SourceTypes length = %d, want %d", len(report.Features.SourceTypes), len(expected))
	}
	for i, st := range expected {
		if report.Features.SourceTypes[i] != st {
			t.Errorf("SourceTypes[%d] = %q, want %q", i, report.Features.SourceTypes[i], st)
		}
	}
}

func TestRun(t *testing.T) {
	s := newScheme(t)

	var received Report
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
			Data:       map[string]string{installationIDKey: "run-test-id"},
		},
		&kelosv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "default"},
			Spec:       kelosv1alpha1.TaskSpec{Type: "claude-code"},
			Status:     kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
		},
	).Build()

	cs := fakeclientset.NewSimpleClientset()

	err := Run(context.Background(), logr.Discard(), c, cs, server.URL)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if received.InstallationID != "run-test-id" {
		t.Errorf("received InstallationID = %q, want %q", received.InstallationID, "run-test-id")
	}
	if received.Tasks.Total != 1 {
		t.Errorf("received Tasks.Total = %d, want 1", received.Tasks.Total)
	}
}
