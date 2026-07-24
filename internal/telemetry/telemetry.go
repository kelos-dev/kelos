package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/version"
)

const (
	// configMapName is the name of the ConfigMap used to store the installation ID.
	configMapName = "kelos-telemetry"
	// installationIDKey is the key in the ConfigMap that stores the installation ID.
	installationIDKey = "installationId"
	// systemNamespace is the namespace where the telemetry ConfigMap is stored.
	systemNamespace = "kelos-system"

	posthogAPIKey = "phc_G9PwwTbT9r7eEGbsuuS3LyzolVGtBUxRNQqy0YMzKzF"

	// DefaultPostHogEndpoint is the default PostHog ingestion endpoint.
	DefaultPostHogEndpoint = "https://us.i.posthog.com"
)

// PostHogClient abstracts the PostHog client for testing.
type PostHogClient interface {
	Enqueue(msg posthog.Message) error
	Close() error
}

// Report contains anonymous aggregate telemetry data.
type Report struct {
	InstallationID string            `json:"installationId"`
	Version        string            `json:"version"`
	K8sVersion     string            `json:"k8sVersion"`
	Environment    string            `json:"environment"`
	Tasks          TaskReport        `json:"tasks"`
	Sessions       SessionReport     `json:"sessions"`
	TaskSpawners   TaskSpawnerReport `json:"taskSpawners"`
	WorkerPools    WorkerPoolReport  `json:"workerPools"`
	Features       FeatureReport     `json:"features"`
	Resources      ResourceReport    `json:"resources"`
	Scale          ScaleReport       `json:"scale"`
	Usage          UsageReport       `json:"usage"`
}

// TaskReport contains aggregate task counts.
type TaskReport struct {
	Total   int            `json:"total"`
	ByType  map[string]int `json:"byType"`
	ByPhase map[string]int `json:"byPhase"`
}

// SessionReport contains aggregate Session counts.
type SessionReport struct {
	Total   int            `json:"total"`
	ByType  map[string]int `json:"byType"`
	ByPhase map[string]int `json:"byPhase"`
}

// TaskSpawnerReport contains aggregate TaskSpawner counts.
type TaskSpawnerReport struct {
	Total    int            `json:"total"`
	BySource map[string]int `json:"bySource"`
}

// WorkerPoolReport contains aggregate WorkerPool counts and replica totals.
type WorkerPoolReport struct {
	Total           int            `json:"total"`
	ByPhase         map[string]int `json:"byPhase"`
	DesiredReplicas int64          `json:"desiredReplicas"`
	CurrentReplicas int64          `json:"currentReplicas"`
	ReadyReplicas   int64          `json:"readyReplicas"`
}

// FeatureReport contains feature adoption counts.
type FeatureReport struct {
	TaskSpawners int      `json:"taskSpawners"`
	AgentConfigs int      `json:"agentConfigs"`
	Workspaces   int      `json:"workspaces"`
	SourceTypes  []string `json:"sourceTypes"`
}

// ResourceReport contains counts keyed by Kubernetes resource name.
type ResourceReport map[string]int

// ScaleReport contains scale metrics.
type ScaleReport struct {
	Namespaces int `json:"namespaces"`
}

// UsageReport contains aggregate usage metrics.
type UsageReport struct {
	TotalCostUSD      float64 `json:"totalCostUsd"`
	TotalInputTokens  float64 `json:"totalInputTokens"`
	TotalOutputTokens float64 `json:"totalOutputTokens"`
}

// NewPostHogClient creates a new PostHog client with the given endpoint.
func NewPostHogClient(endpoint string) (PostHogClient, error) {
	return posthog.NewWithConfig(posthogAPIKey, posthog.Config{
		Endpoint: endpoint,
	})
}

// Run collects anonymous aggregate telemetry and sends it to PostHog.
func Run(ctx context.Context, log logr.Logger, c client.Client, clientset kubernetes.Interface, phClient PostHogClient, env string) error {
	log.Info("Collecting anonymous usage data (resource counts, task and Session breakdowns, TaskSpawner sources, WorkerPool scale, usage totals). " +
		"No personal data is collected. " +
		"To disable: kelos install --disable-heartbeat")

	report, err := collect(ctx, c, clientset, env)
	if err != nil {
		return fmt.Errorf("collecting telemetry: %w", err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling report: %w", err)
	}
	log.Info("Telemetry report collected", "payload", string(data))

	if err := send(phClient, report); err != nil {
		log.Error(err, "Failed to send telemetry report (non-fatal)")
		return nil
	}

	log.Info("Telemetry report sent successfully")
	return nil
}

func collect(ctx context.Context, c client.Client, clientset kubernetes.Interface, env string) (*Report, error) {
	report := &Report{
		Version:     version.Version,
		Environment: env,
		Tasks: TaskReport{
			ByType:  make(map[string]int),
			ByPhase: make(map[string]int),
		},
		Sessions: SessionReport{
			ByType:  make(map[string]int),
			ByPhase: make(map[string]int),
		},
		TaskSpawners: TaskSpawnerReport{
			BySource: make(map[string]int),
		},
		WorkerPools: WorkerPoolReport{
			ByPhase: make(map[string]int),
		},
		Features:  FeatureReport{},
		Resources: make(ResourceReport),
		Scale:     ScaleReport{},
		Usage:     UsageReport{},
	}

	// Get or create installation ID.
	id, err := getOrCreateInstallationID(ctx, c, systemNamespace)
	if err != nil {
		return nil, fmt.Errorf("getting installation ID: %w", err)
	}
	report.InstallationID = id

	// Get Kubernetes server version.
	sv, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return nil, fmt.Errorf("getting server version: %w", err)
	}
	report.K8sVersion = sv.GitVersion

	// Collect counts for every resource in the latest Kelos API version.
	namespaces := make(map[string]struct{})
	report.Resources, err = collectResourceCounts(ctx, c, namespaces)
	if err != nil {
		return nil, fmt.Errorf("collecting resource counts: %w", err)
	}

	// Collect WorkerPool data before Tasks so WorkerPool-backed Tasks can be
	// attributed to the pool's effective agent type.
	var workerPools kelos.WorkerPoolList
	if err := c.List(ctx, &workerPools); err != nil {
		return nil, fmt.Errorf("listing worker pools: %w", err)
	}
	report.WorkerPools.Total = len(workerPools.Items)
	workerPoolTypes := make(map[types.NamespacedName]string, len(workerPools.Items))
	for i := range workerPools.Items {
		pool := &workerPools.Items[i]
		workerPoolTypes[client.ObjectKeyFromObject(pool)] = normalizeAgentType(pool.Spec.Worker.Type)
		report.WorkerPools.ByPhase[normalizeWorkerPoolPhase(pool.Status.Phase)]++

		desiredReplicas := int32(1)
		if pool.Spec.Replicas != nil {
			desiredReplicas = *pool.Spec.Replicas
		}
		report.WorkerPools.DesiredReplicas += int64(desiredReplicas)
		report.WorkerPools.CurrentReplicas += int64(pool.Status.Replicas)
		report.WorkerPools.ReadyReplicas += int64(pool.Status.ReadyReplicas)
	}

	// TaskRecords are the canonical source for completed usage because they
	// remain available after Tasks are deleted by TTL.
	var taskRecords kelos.TaskRecordList
	if err := c.List(ctx, &taskRecords); err != nil {
		return nil, fmt.Errorf("listing task records: %w", err)
	}
	recordedTaskUIDs := make(map[types.UID]struct{}, len(taskRecords.Items))
	for i := range taskRecords.Items {
		record := &taskRecords.Items[i]
		if record.Spec.Usage == nil {
			continue
		}
		addUsage(&report.Usage, record.Spec.Usage)
		if record.Spec.TaskRef.UID != "" {
			recordedTaskUIDs[record.Spec.TaskRef.UID] = struct{}{}
		}
	}

	// Collect Task data.
	var tasks kelos.TaskList
	if err := c.List(ctx, &tasks); err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	report.Tasks.Total = len(tasks.Items)
	for i := range tasks.Items {
		task := &tasks.Items[i]
		report.Tasks.ByType[effectiveTaskType(task, workerPoolTypes)]++
		report.Tasks.ByPhase[normalizeTaskPhase(task.Status.Phase)]++

		if task.UID != "" {
			if _, recorded := recordedTaskUIDs[task.UID]; recorded {
				continue
			}
		}

		if task.Status.Usage != nil {
			addUsage(&report.Usage, task.Status.Usage)
		} else {
			addLegacyResultUsage(&report.Usage, task.Status.Results)
		}
	}

	// Collect TaskSpawner data.
	var spawners kelos.TaskSpawnerList
	if err := c.List(ctx, &spawners); err != nil {
		return nil, fmt.Errorf("listing task spawners: %w", err)
	}
	report.Features.TaskSpawners = len(spawners.Items)
	report.TaskSpawners.Total = len(spawners.Items)
	sourceTypes := make(map[string]struct{})
	for i := range spawners.Items {
		s := &spawners.Items[i]
		report.TaskSpawners.BySource[taskSpawnerSource(s.Spec.When)]++

		if s.Spec.When.GitHubIssues != nil {
			sourceTypes["github"] = struct{}{}
		}
		if s.Spec.When.Cron != nil {
			sourceTypes["cron"] = struct{}{}
		}
		if s.Spec.When.Jira != nil {
			sourceTypes["jira"] = struct{}{}
		}
	}
	for st := range sourceTypes {
		report.Features.SourceTypes = append(report.Features.SourceTypes, st)
	}
	sort.Strings(report.Features.SourceTypes)

	// Collect Session data.
	var sessions kelos.SessionList
	if err := c.List(ctx, &sessions); err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	report.Sessions.Total = len(sessions.Items)
	for i := range sessions.Items {
		session := &sessions.Items[i]
		report.Sessions.ByType[normalizeAgentType(session.Spec.Worker.Type)]++
		report.Sessions.ByPhase[normalizeSessionPhase(session.Status.Phase)]++
	}

	// Collect AgentConfig data (v1alpha2 is the storage version).
	var agentConfigs kelos.AgentConfigList
	if err := c.List(ctx, &agentConfigs); err != nil {
		return nil, fmt.Errorf("listing agent configs: %w", err)
	}
	report.Features.AgentConfigs = len(agentConfigs.Items)

	// Collect Workspace data.
	var workspaces kelos.WorkspaceList
	if err := c.List(ctx, &workspaces); err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	report.Features.Workspaces = len(workspaces.Items)

	report.Scale.Namespaces = len(namespaces)

	return report, nil
}

func effectiveTaskType(task *kelos.Task, workerPoolTypes map[types.NamespacedName]string) string {
	if task.Spec.Worker != nil {
		return normalizeAgentType(task.Spec.Worker.Type)
	}
	if task.Spec.WorkerPoolRef != nil {
		if workerType, ok := workerPoolTypes[types.NamespacedName{
			Namespace: task.Namespace,
			Name:      task.Spec.WorkerPoolRef.Name,
		}]; ok {
			return workerType
		}
		return "unknown"
	}
	return normalizeAgentType(task.Spec.Type)
}

func normalizeAgentType(agentType string) string {
	switch agentType {
	case "claude-code", "codex", "gemini", "opencode", "cursor":
		return agentType
	default:
		return "unknown"
	}
}

func normalizeTaskPhase(phase kelos.TaskPhase) string {
	switch phase {
	case kelos.TaskPhasePending,
		kelos.TaskPhaseRunning,
		kelos.TaskPhaseSucceeded,
		kelos.TaskPhaseFailed,
		kelos.TaskPhaseWaiting:
		return string(phase)
	default:
		return "Unknown"
	}
}

func normalizeSessionPhase(phase kelos.SessionPhase) string {
	switch phase {
	case kelos.SessionPhasePending, kelos.SessionPhaseReady, kelos.SessionPhaseFailed:
		return string(phase)
	default:
		return "Unknown"
	}
}

func normalizeWorkerPoolPhase(phase kelos.WorkerPoolPhase) string {
	switch phase {
	case kelos.WorkerPoolPhasePending,
		kelos.WorkerPoolPhaseReady,
		kelos.WorkerPoolPhaseScaling,
		kelos.WorkerPoolPhaseFailed:
		return string(phase)
	default:
		return "Unknown"
	}
}

func taskSpawnerSource(when kelos.When) string {
	switch {
	case when.GitHubIssues != nil:
		return "github_issues"
	case when.GitHubPullRequests != nil:
		return "github_pull_requests"
	case when.GitHubWebhook != nil:
		return "github_webhook"
	case when.LinearWebhook != nil:
		return "linear_webhook"
	case when.GenericWebhook != nil:
		return "generic_webhook"
	case when.Cron != nil:
		return "cron"
	case when.Jira != nil:
		return "jira"
	case when.Slack != nil:
		return "slack"
	default:
		return "unknown"
	}
}

func addUsage(report *UsageReport, usage *kelos.TaskUsage) {
	if usage.CostUSD != nil {
		report.TotalCostUSD += usage.CostUSD.AsApproximateFloat64()
	}
	if usage.InputTokens != nil {
		report.TotalInputTokens += float64(*usage.InputTokens)
	}
	if usage.OutputTokens != nil {
		report.TotalOutputTokens += float64(*usage.OutputTokens)
	}
}

func addLegacyResultUsage(report *UsageReport, results map[string]string) {
	if v, ok := results["cost_usd"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			report.TotalCostUSD += f
		}
	}
	if v, ok := results["input_tokens"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			report.TotalInputTokens += f
		}
	}
	if v, ok := results["output_tokens"]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			report.TotalOutputTokens += f
		}
	}
}

func collectResourceCounts(ctx context.Context, c client.Client, namespaces map[string]struct{}) (ResourceReport, error) {
	var listGVKs []schema.GroupVersionKind
	for gvk := range c.Scheme().AllKnownTypes() {
		if gvk.GroupVersion() == kelos.GroupVersion && strings.HasSuffix(gvk.Kind, "List") {
			listGVKs = append(listGVKs, gvk)
		}
	}
	sort.Slice(listGVKs, func(i, j int) bool {
		return listGVKs[i].Kind < listGVKs[j].Kind
	})

	counts := make(ResourceReport, len(listGVKs))
	for _, listGVK := range listGVKs {
		listObject, err := c.Scheme().New(listGVK)
		if err != nil {
			return nil, fmt.Errorf("creating %s: %w", listGVK.Kind, err)
		}
		list, ok := listObject.(client.ObjectList)
		if !ok {
			return nil, fmt.Errorf("%s does not implement client.ObjectList", listGVK.Kind)
		}
		if err := c.List(ctx, list); err != nil {
			return nil, fmt.Errorf("listing %s: %w", listGVK.Kind, err)
		}

		itemGVK := listGVK
		itemGVK.Kind = strings.TrimSuffix(listGVK.Kind, "List")
		resource, _ := meta.UnsafeGuessKindToResource(itemGVK)
		count := 0
		if err := meta.EachListItem(list, func(item runtime.Object) error {
			count++
			object, ok := item.(metav1.Object)
			if !ok {
				return fmt.Errorf("%T does not implement metav1.Object", item)
			}
			if namespace := object.GetNamespace(); namespace != "" {
				namespaces[namespace] = struct{}{}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("reading %s: %w", listGVK.Kind, err)
		}
		counts[resource.Resource] = count
	}

	return counts, nil
}

func send(phClient PostHogClient, report *Report) error {
	properties := posthog.NewProperties().
		Set("version", report.Version).
		Set("k8s_version", report.K8sVersion).
		Set("environment", report.Environment).
		Set("tasks_total", report.Tasks.Total).
		Set("tasks_by_type", report.Tasks.ByType).
		Set("tasks_by_phase", report.Tasks.ByPhase).
		Set("sessions_total", report.Sessions.Total).
		Set("sessions_by_type", report.Sessions.ByType).
		Set("sessions_by_phase", report.Sessions.ByPhase).
		Set("taskspawners_total", report.TaskSpawners.Total).
		Set("taskspawners_by_source", report.TaskSpawners.BySource).
		Set("workerpools_total", report.WorkerPools.Total).
		Set("workerpools_by_phase", report.WorkerPools.ByPhase).
		Set("workerpools_desired_replicas", report.WorkerPools.DesiredReplicas).
		Set("workerpools_current_replicas", report.WorkerPools.CurrentReplicas).
		Set("workerpools_ready_replicas", report.WorkerPools.ReadyReplicas).
		Set("feature_task_spawners", report.Features.TaskSpawners).
		Set("feature_agent_configs", report.Features.AgentConfigs).
		Set("feature_workspaces", report.Features.Workspaces).
		Set("feature_source_types", report.Features.SourceTypes).
		Set("scale_namespaces", report.Scale.Namespaces).
		Set("usage_total_cost_usd", report.Usage.TotalCostUSD).
		Set("usage_total_input_tokens", report.Usage.TotalInputTokens).
		Set("usage_total_output_tokens", report.Usage.TotalOutputTokens).
		Set("$process_person_profile", false)
	for resource, count := range report.Resources {
		properties.Set("resources_"+resource, count)
	}

	if err := phClient.Enqueue(posthog.Capture{
		DistinctId: report.InstallationID,
		Event:      "telemetry_report",
		Properties: properties,
	}); err != nil {
		return fmt.Errorf("enqueuing event: %w", err)
	}

	if err := phClient.Close(); err != nil {
		return fmt.Errorf("flushing events: %w", err)
	}

	return nil
}

func getOrCreateInstallationID(ctx context.Context, c client.Client, namespace string) (string, error) {
	var cm corev1.ConfigMap
	key := types.NamespacedName{Name: configMapName, Namespace: namespace}

	err := c.Get(ctx, key, &cm)
	if err == nil {
		if id, ok := cm.Data[installationIDKey]; ok && id != "" {
			return id, nil
		}
	}
	if err != nil && !errors.IsNotFound(err) {
		return "", fmt.Errorf("getting config map: %w", err)
	}

	id := uuid.New().String()
	if errors.IsNotFound(err) {
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: namespace,
			},
			Data: map[string]string{
				installationIDKey: id,
			},
		}
		if err := c.Create(ctx, &cm); err != nil {
			return "", fmt.Errorf("creating config map: %w", err)
		}
		return id, nil
	}

	// ConfigMap exists but has no installation ID.
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[installationIDKey] = id
	if err := c.Update(ctx, &cm); err != nil {
		return "", fmt.Errorf("updating config map: %w", err)
	}
	return id, nil
}
