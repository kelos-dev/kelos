package telemetry

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/go-logr/logr"
	"github.com/posthog/posthog-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// fakePostHogClient captures events for testing.
type fakePostHogClient struct {
	events     []posthog.Capture
	closeErr   error
	closed     bool
	enqueueErr error
}

func (f *fakePostHogClient) Enqueue(msg posthog.Message) error {
	if f.enqueueErr != nil {
		return f.enqueueErr
	}
	if capture, ok := msg.(posthog.Capture); ok {
		f.events = append(f.events, capture)
	}
	return nil
}

func (f *fakePostHogClient) Close() error {
	f.closed = true
	return f.closeErr
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(s); err != nil {
		t.Fatalf("failed to add v1alpha2 scheme: %v", err)
	}
	return s
}

func TestCollect(t *testing.T) {
	s := newScheme(t)

	tasks := []kelos.Task{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-1", Namespace: "ns-a"},
			Spec:       kelos.TaskSpec{Type: "claude-code"},
			Status: kelos.TaskStatus{
				Phase: kelos.TaskPhaseSucceeded,
				Results: map[string]string{
					"cost_usd":      "1.50",
					"input_tokens":  "1000",
					"output_tokens": "500",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-2", Namespace: "ns-a"},
			Spec:       kelos.TaskSpec{Type: "claude-code"},
			Status: kelos.TaskStatus{
				Phase: kelos.TaskPhaseFailed,
				Results: map[string]string{
					"cost_usd":      "0.50",
					"input_tokens":  "200",
					"output_tokens": "100",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-3", Namespace: "ns-b"},
			Spec: kelos.TaskSpec{
				Worker: &kelos.WorkerSpec{Type: "codex"},
			},
			Status: kelos.TaskStatus{Phase: kelos.TaskPhaseRunning},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "task-4", Namespace: "ns-b"},
			Spec: kelos.TaskSpec{
				WorkerPoolRef: &kelos.WorkerPoolReference{Name: "pool-1"},
			},
			Status: kelos.TaskStatus{Phase: kelos.TaskPhaseWaiting},
		},
	}

	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "spawner-1", Namespace: "ns-a"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{GitHubIssues: &kelos.GitHubIssues{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "spawner-2", Namespace: "ns-b"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Cron: &kelos.Cron{Schedule: "0 * * * *"}},
			},
		},
	}

	agentConfigs := []kelos.AgentConfig{
		{ObjectMeta: metav1.ObjectMeta{Name: "config-1", Namespace: "ns-a"}},
	}

	workspaces := []kelos.Workspace{
		{ObjectMeta: metav1.ObjectMeta{Name: "ws-1", Namespace: "ns-a"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "ws-2", Namespace: "ns-c"}},
	}

	sessions := []kelos.Session{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "session-1", Namespace: "ns-d"},
			Spec:       kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
			Status:     kelos.SessionStatus{Phase: kelos.SessionPhaseReady},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "session-2", Namespace: "ns-d"},
			Spec:       kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex"}},
			Status:     kelos.SessionStatus{Phase: kelos.SessionPhaseFailed},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "session-3", Namespace: "ns-d"},
			Spec:       kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "claude-code"}},
			Status:     kelos.SessionStatus{Phase: kelos.SessionPhasePending},
		},
	}

	sessionSpawners := []kelos.SessionSpawner{
		{ObjectMeta: metav1.ObjectMeta{Name: "session-spawner-1", Namespace: "ns-d"}},
	}

	taskBudgets := []kelos.TaskBudget{
		{ObjectMeta: metav1.ObjectMeta{Name: "budget-1", Namespace: "ns-d"}},
	}

	taskRecords := []kelos.TaskRecord{
		{ObjectMeta: metav1.ObjectMeta{Name: "record-1", Namespace: "ns-d"}},
	}

	desiredReplicas := int32(3)
	pausedReplicas := int32(0)
	workerPools := []kelos.WorkerPool{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pool-1", Namespace: "ns-b"},
			Spec: kelos.WorkerPoolSpec{
				Worker:   kelos.WorkerSpec{Type: "opencode"},
				Replicas: &desiredReplicas,
			},
			Status: kelos.WorkerPoolStatus{
				Phase:         kelos.WorkerPoolPhaseReady,
				Replicas:      3,
				ReadyReplicas: 2,
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "pool-2", Namespace: "ns-d"},
			Spec: kelos.WorkerPoolSpec{
				Worker:   kelos.WorkerSpec{Type: "codex"},
				Replicas: &pausedReplicas,
			},
			Status: kelos.WorkerPoolStatus{Phase: kelos.WorkerPoolPhaseFailed},
		},
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
	for i := range sessions {
		objs = append(objs, &sessions[i])
	}
	for i := range sessionSpawners {
		objs = append(objs, &sessionSpawners[i])
	}
	for i := range taskBudgets {
		objs = append(objs, &taskBudgets[i])
	}
	for i := range taskRecords {
		objs = append(objs, &taskRecords[i])
	}
	for i := range workerPools {
		objs = append(objs, &workerPools[i])
	}
	// Pre-create the telemetry ConfigMap so we don't depend on Create.
	objs = append(objs, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
		Data:       map[string]string{installationIDKey: "test-install-id"},
	})

	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(objs...).Build()

	cs := fakeclientset.NewSimpleClientset()

	report, err := collect(context.Background(), c, cs, "test")
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	// Verify task counts.
	if report.Tasks.Total != 4 {
		t.Errorf("Tasks.Total = %d, want 4", report.Tasks.Total)
	}
	if report.Tasks.ByType["claude-code"] != 2 {
		t.Errorf("Tasks.ByType[claude-code] = %d, want 2", report.Tasks.ByType["claude-code"])
	}
	if report.Tasks.ByType["codex"] != 1 {
		t.Errorf("Tasks.ByType[codex] = %d, want 1", report.Tasks.ByType["codex"])
	}
	if report.Tasks.ByType["opencode"] != 1 {
		t.Errorf("Tasks.ByType[opencode] = %d, want 1", report.Tasks.ByType["opencode"])
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
	if report.Tasks.ByPhase["Waiting"] != 1 {
		t.Errorf("Tasks.ByPhase[Waiting] = %d, want 1", report.Tasks.ByPhase["Waiting"])
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
	if report.TaskSpawners.Total != 2 {
		t.Errorf("TaskSpawners.Total = %d, want 2", report.TaskSpawners.Total)
	}
	if report.TaskSpawners.BySource["github_issues"] != 1 {
		t.Errorf("TaskSpawners.BySource[github_issues] = %d, want 1", report.TaskSpawners.BySource["github_issues"])
	}
	if report.TaskSpawners.BySource["cron"] != 1 {
		t.Errorf("TaskSpawners.BySource[cron] = %d, want 1", report.TaskSpawners.BySource["cron"])
	}

	if report.Sessions.Total != 3 {
		t.Errorf("Sessions.Total = %d, want 3", report.Sessions.Total)
	}
	if report.Sessions.ByType["codex"] != 2 {
		t.Errorf("Sessions.ByType[codex] = %d, want 2", report.Sessions.ByType["codex"])
	}
	if report.Sessions.ByType["claude-code"] != 1 {
		t.Errorf("Sessions.ByType[claude-code] = %d, want 1", report.Sessions.ByType["claude-code"])
	}
	for _, phase := range []string{"Ready", "Failed", "Pending"} {
		if report.Sessions.ByPhase[phase] != 1 {
			t.Errorf("Sessions.ByPhase[%s] = %d, want 1", phase, report.Sessions.ByPhase[phase])
		}
	}

	if report.WorkerPools.Total != 2 {
		t.Errorf("WorkerPools.Total = %d, want 2", report.WorkerPools.Total)
	}
	if report.WorkerPools.ByPhase["Ready"] != 1 {
		t.Errorf("WorkerPools.ByPhase[Ready] = %d, want 1", report.WorkerPools.ByPhase["Ready"])
	}
	if report.WorkerPools.ByPhase["Failed"] != 1 {
		t.Errorf("WorkerPools.ByPhase[Failed] = %d, want 1", report.WorkerPools.ByPhase["Failed"])
	}
	if report.WorkerPools.DesiredReplicas != 3 {
		t.Errorf("WorkerPools.DesiredReplicas = %d, want 3", report.WorkerPools.DesiredReplicas)
	}
	if report.WorkerPools.CurrentReplicas != 3 {
		t.Errorf("WorkerPools.CurrentReplicas = %d, want 3", report.WorkerPools.CurrentReplicas)
	}
	if report.WorkerPools.ReadyReplicas != 2 {
		t.Errorf("WorkerPools.ReadyReplicas = %d, want 2", report.WorkerPools.ReadyReplicas)
	}

	expectedResources := ResourceReport{
		"agentconfigs":    1,
		"sessions":        3,
		"sessionspawners": 1,
		"taskbudgets":     1,
		"taskrecords":     1,
		"tasks":           4,
		"taskspawners":    2,
		"workerpools":     2,
		"workspaces":      2,
	}
	for resource, expected := range expectedResources {
		if report.Resources[resource] != expected {
			t.Errorf("Resources[%s] = %d, want %d", resource, report.Resources[resource], expected)
		}
	}

	sort.Strings(report.Features.SourceTypes)
	if len(report.Features.SourceTypes) != 2 {
		t.Fatalf("Features.SourceTypes length = %d, want 2", len(report.Features.SourceTypes))
	}
	if report.Features.SourceTypes[0] != "cron" || report.Features.SourceTypes[1] != "github" {
		t.Errorf("Features.SourceTypes = %v, want [cron github]", report.Features.SourceTypes)
	}

	// Verify scale (ns-a, ns-b, ns-c, ns-d = 4 namespaces).
	if report.Scale.Namespaces != 4 {
		t.Errorf("Scale.Namespaces = %d, want 4", report.Scale.Namespaces)
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
	report, err := collect(context.Background(), c, cs, "test")
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	if report.Tasks.Total != 0 {
		t.Errorf("Tasks.Total = %d, want 0", report.Tasks.Total)
	}
	if report.Sessions.Total != 0 {
		t.Errorf("Sessions.Total = %d, want 0", report.Sessions.Total)
	}
	if report.TaskSpawners.Total != 0 {
		t.Errorf("TaskSpawners.Total = %d, want 0", report.TaskSpawners.Total)
	}
	if report.WorkerPools.Total != 0 {
		t.Errorf("WorkerPools.Total = %d, want 0", report.WorkerPools.Total)
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
	for _, resource := range []string{
		"agentconfigs",
		"sessions",
		"sessionspawners",
		"taskbudgets",
		"taskrecords",
		"tasks",
		"taskspawners",
		"workerpools",
		"workspaces",
	} {
		if report.Resources[resource] != 0 {
			t.Errorf("Resources[%s] = %d, want 0", resource, report.Resources[resource])
		}
	}
	if report.Scale.Namespaces != 0 {
		t.Errorf("Scale.Namespaces = %d, want 0", report.Scale.Namespaces)
	}
	if report.Usage.TotalCostUSD != 0 {
		t.Errorf("Usage.TotalCostUSD = %f, want 0", report.Usage.TotalCostUSD)
	}
}

func TestSend(t *testing.T) {
	phClient := &fakePostHogClient{}

	report := &Report{
		InstallationID: "test-id",
		Version:        "v0.1.0",
		K8sVersion:     "v1.30.0",
		Tasks: TaskReport{
			Total:   5,
			ByType:  map[string]int{"claude-code": 5},
			ByPhase: map[string]int{"Succeeded": 5},
		},
		Sessions: SessionReport{
			Total:   4,
			ByType:  map[string]int{"codex": 4},
			ByPhase: map[string]int{"Ready": 4},
		},
		TaskSpawners: TaskSpawnerReport{
			Total:    2,
			BySource: map[string]int{"cron": 1, "github_issues": 1},
		},
		WorkerPools: WorkerPoolReport{
			Total:           1,
			ByPhase:         map[string]int{"Ready": 1},
			DesiredReplicas: 3,
			CurrentReplicas: 3,
			ReadyReplicas:   2,
		},
		Features: FeatureReport{
			TaskSpawners: 2,
			AgentConfigs: 1,
			Workspaces:   3,
			SourceTypes:  []string{"cron", "github"},
		},
		Resources: ResourceReport{
			"sessions":        4,
			"sessionspawners": 2,
			"workerpools":     1,
		},
		Scale: ScaleReport{Namespaces: 4},
		Usage: UsageReport{
			TotalCostUSD:      10.5,
			TotalInputTokens:  5000,
			TotalOutputTokens: 2000,
		},
	}

	err := send(phClient, report)
	if err != nil {
		t.Fatalf("send() error: %v", err)
	}

	if len(phClient.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(phClient.events))
	}

	event := phClient.events[0]
	if event.DistinctId != "test-id" {
		t.Errorf("DistinctId = %q, want %q", event.DistinctId, "test-id")
	}
	if event.Event != "telemetry_report" {
		t.Errorf("Event = %q, want %q", event.Event, "telemetry_report")
	}
	if event.Properties["version"] != "v0.1.0" {
		t.Errorf("version = %v, want %q", event.Properties["version"], "v0.1.0")
	}
	if event.Properties["k8s_version"] != "v1.30.0" {
		t.Errorf("k8s_version = %v, want %q", event.Properties["k8s_version"], "v1.30.0")
	}
	if event.Properties["tasks_total"] != 5 {
		t.Errorf("tasks_total = %v, want 5", event.Properties["tasks_total"])
	}
	if event.Properties["scale_namespaces"] != 4 {
		t.Errorf("scale_namespaces = %v, want 4", event.Properties["scale_namespaces"])
	}
	if event.Properties["usage_total_cost_usd"] != 10.5 {
		t.Errorf("usage_total_cost_usd = %v, want 10.5", event.Properties["usage_total_cost_usd"])
	}
	if event.Properties["sessions_total"] != 4 {
		t.Errorf("sessions_total = %v, want 4", event.Properties["sessions_total"])
	}
	sessionsByType, ok := event.Properties["sessions_by_type"].(map[string]int)
	if !ok || sessionsByType["codex"] != 4 {
		t.Errorf("sessions_by_type = %v, want codex=4", event.Properties["sessions_by_type"])
	}
	sessionsByPhase, ok := event.Properties["sessions_by_phase"].(map[string]int)
	if !ok || sessionsByPhase["Ready"] != 4 {
		t.Errorf("sessions_by_phase = %v, want Ready=4", event.Properties["sessions_by_phase"])
	}
	if event.Properties["taskspawners_total"] != 2 {
		t.Errorf("taskspawners_total = %v, want 2", event.Properties["taskspawners_total"])
	}
	taskSpawnersBySource, ok := event.Properties["taskspawners_by_source"].(map[string]int)
	if !ok || taskSpawnersBySource["github_issues"] != 1 {
		t.Errorf("taskspawners_by_source = %v, want github_issues=1", event.Properties["taskspawners_by_source"])
	}
	workerPoolsByPhase, ok := event.Properties["workerpools_by_phase"].(map[string]int)
	if !ok || workerPoolsByPhase["Ready"] != 1 {
		t.Errorf("workerpools_by_phase = %v, want Ready=1", event.Properties["workerpools_by_phase"])
	}
	if event.Properties["workerpools_desired_replicas"] != int64(3) {
		t.Errorf("workerpools_desired_replicas = %v, want 3", event.Properties["workerpools_desired_replicas"])
	}
	if event.Properties["workerpools_current_replicas"] != int64(3) {
		t.Errorf("workerpools_current_replicas = %v, want 3", event.Properties["workerpools_current_replicas"])
	}
	if event.Properties["workerpools_ready_replicas"] != int64(2) {
		t.Errorf("workerpools_ready_replicas = %v, want 2", event.Properties["workerpools_ready_replicas"])
	}
	if event.Properties["$process_person_profile"] != false {
		t.Errorf("$process_person_profile = %v, want false", event.Properties["$process_person_profile"])
	}
	if event.Properties["resources_sessions"] != 4 {
		t.Errorf("resources_sessions = %v, want 4", event.Properties["resources_sessions"])
	}
	if event.Properties["resources_sessionspawners"] != 2 {
		t.Errorf("resources_sessionspawners = %v, want 2", event.Properties["resources_sessionspawners"])
	}
	if event.Properties["resources_workerpools"] != 1 {
		t.Errorf("resources_workerpools = %v, want 1", event.Properties["resources_workerpools"])
	}

	if !phClient.closed {
		t.Error("PostHog client was not closed")
	}
}

func TestSendEnqueueError(t *testing.T) {
	phClient := &fakePostHogClient{
		enqueueErr: fmt.Errorf("enqueue failed"),
	}

	report := &Report{InstallationID: "test-id"}
	err := send(phClient, report)
	if err == nil {
		t.Fatal("send() expected error for enqueue failure, got nil")
	}
}

func TestSendCloseError(t *testing.T) {
	phClient := &fakePostHogClient{
		closeErr: fmt.Errorf("close failed"),
	}

	report := &Report{InstallationID: "test-id"}
	err := send(phClient, report)
	if err == nil {
		t.Fatal("send() expected error for close failure, got nil")
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

	spawners := []kelos.TaskSpawner{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s1", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{GitHubIssues: &kelos.GitHubIssues{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s2", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Cron: &kelos.Cron{Schedule: "0 * * * *"}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s3", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Jira: &kelos.Jira{
					BaseURL:   "https://jira.example.com",
					Project:   "PROJ",
					SecretRef: kelos.SecretReference{Name: "jira-secret"},
				}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s4", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{GitHubPullRequests: &kelos.GitHubPullRequests{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s5", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{GitHubWebhook: &kelos.GitHubWebhook{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s6", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{LinearWebhook: &kelos.LinearWebhook{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s7", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{GenericWebhook: &kelos.GenericWebhook{}},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s8", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{Slack: &kelos.Slack{}},
			},
		},
		// Duplicate GitHub Issues source — the detailed count should include
		// both while the legacy feature list remains unique.
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s9", Namespace: "ns"},
			Spec: kelos.TaskSpawnerSpec{
				When: kelos.When{GitHubIssues: &kelos.GitHubIssues{}},
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

	report, err := collect(context.Background(), c, cs, "test")
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

	expectedBySource := map[string]int{
		"github_issues":        2,
		"github_pull_requests": 1,
		"github_webhook":       1,
		"linear_webhook":       1,
		"generic_webhook":      1,
		"cron":                 1,
		"jira":                 1,
		"slack":                1,
	}
	for source, count := range expectedBySource {
		if report.TaskSpawners.BySource[source] != count {
			t.Errorf("TaskSpawners.BySource[%s] = %d, want %d", source, report.TaskSpawners.BySource[source], count)
		}
	}
}

func TestCollectUsagePrefersTaskRecords(t *testing.T) {
	s := newScheme(t)

	recordCost := resource.MustParse("1.25")
	taskCost := resource.MustParse("0.5")
	recordInput, recordOutput := int64(100), int64(50)
	taskInput, taskOutput := int64(20), int64(10)

	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
			Data:       map[string]string{installationIDKey: "usage-id"},
		},
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "recorded-task", Namespace: "ns", UID: types.UID("recorded-task-uid")},
			Spec:       kelos.TaskSpec{Worker: &kelos.WorkerSpec{Type: "codex"}},
			Status: kelos.TaskStatus{Usage: &kelos.TaskUsage{
				CostUSD:      &recordCost,
				InputTokens:  &recordInput,
				OutputTokens: &recordOutput,
			}},
		},
		&kelos.TaskRecord{
			ObjectMeta: metav1.ObjectMeta{Name: "record", Namespace: "ns"},
			Spec: kelos.TaskRecordSpec{
				TaskRef: kelos.TaskReference{UID: types.UID("recorded-task-uid")},
				Usage: &kelos.TaskUsage{
					CostUSD:      &recordCost,
					InputTokens:  &recordInput,
					OutputTokens: &recordOutput,
				},
			},
		},
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "structured-task", Namespace: "ns", UID: types.UID("structured-task-uid")},
			Spec:       kelos.TaskSpec{Worker: &kelos.WorkerSpec{Type: "codex"}},
			Status: kelos.TaskStatus{Usage: &kelos.TaskUsage{
				CostUSD:      &taskCost,
				InputTokens:  &taskInput,
				OutputTokens: &taskOutput,
			}},
		},
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-task", Namespace: "ns"},
			Spec:       kelos.TaskSpec{Type: "claude-code"},
			Status: kelos.TaskStatus{Results: map[string]string{
				"cost_usd":      "0.25",
				"input_tokens":  "5",
				"output_tokens": "2",
			}},
		},
	).Build()

	report, err := collect(context.Background(), c, fakeclientset.NewSimpleClientset(), "test")
	if err != nil {
		t.Fatalf("collect() error: %v", err)
	}

	if report.Usage.TotalCostUSD != 2 {
		t.Errorf("Usage.TotalCostUSD = %f, want 2", report.Usage.TotalCostUSD)
	}
	if report.Usage.TotalInputTokens != 125 {
		t.Errorf("Usage.TotalInputTokens = %f, want 125", report.Usage.TotalInputTokens)
	}
	if report.Usage.TotalOutputTokens != 62 {
		t.Errorf("Usage.TotalOutputTokens = %f, want 62", report.Usage.TotalOutputTokens)
	}
}

func TestRun(t *testing.T) {
	s := newScheme(t)

	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
			Data:       map[string]string{installationIDKey: "run-test-id"},
		},
		&kelos.Task{
			ObjectMeta: metav1.ObjectMeta{Name: "t1", Namespace: "default"},
			Spec:       kelos.TaskSpec{Type: "claude-code"},
			Status:     kelos.TaskStatus{Phase: kelos.TaskPhaseSucceeded},
		},
	).Build()

	cs := fakeclientset.NewSimpleClientset()
	phClient := &fakePostHogClient{}

	err := Run(context.Background(), logr.Discard(), c, cs, phClient, "test")
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if len(phClient.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(phClient.events))
	}

	event := phClient.events[0]
	if event.DistinctId != "run-test-id" {
		t.Errorf("DistinctId = %q, want %q", event.DistinctId, "run-test-id")
	}
	if event.Properties["tasks_total"] != 1 {
		t.Errorf("tasks_total = %v, want 1", event.Properties["tasks_total"])
	}

	if !phClient.closed {
		t.Error("PostHog client was not closed after Run")
	}
}

func TestRunSendFailureNonFatal(t *testing.T) {
	s := newScheme(t)

	c := fake.NewClientBuilder().WithScheme(s).WithRuntimeObjects(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: systemNamespace},
			Data:       map[string]string{installationIDKey: "run-test-id"},
		},
	).Build()

	cs := fakeclientset.NewSimpleClientset()
	phClient := &fakePostHogClient{
		enqueueErr: fmt.Errorf("network error"),
	}

	// Send failure should be non-fatal (Run returns nil).
	err := Run(context.Background(), logr.Discard(), c, cs, phClient, "test")
	if err != nil {
		t.Fatalf("Run() should not return error on send failure, got: %v", err)
	}
}
