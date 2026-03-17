package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"github.com/posthog/posthog-go"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
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
	InstallationID string        `json:"installationId"`
	Version        string        `json:"version"`
	K8sVersion     string        `json:"k8sVersion"`
	Environment    string        `json:"environment"`
	Tasks          TaskReport    `json:"tasks"`
	Features       FeatureReport `json:"features"`
	Scale          ScaleReport   `json:"scale"`
	Usage          UsageReport   `json:"usage"`
}

// TaskReport contains aggregate task counts.
type TaskReport struct {
	Total   int            `json:"total"`
	ByType  map[string]int `json:"byType"`
	ByPhase map[string]int `json:"byPhase"`
}

// FeatureReport contains feature adoption counts.
type FeatureReport struct {
	TaskSpawners int      `json:"taskSpawners"`
	AgentConfigs int      `json:"agentConfigs"`
	Workspaces   int      `json:"workspaces"`
	SourceTypes  []string `json:"sourceTypes"`
}

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
	log.Info("Collecting anonymous usage data (task counts, feature adoption, scale metrics). " +
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
		Features: FeatureReport{},
		Scale:    ScaleReport{},
		Usage:    UsageReport{},
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

	// Collect task data.
	namespaces := make(map[string]struct{})

	var tasks kelosv1alpha1.TaskList
	if err := c.List(ctx, &tasks); err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	report.Tasks.Total = len(tasks.Items)
	for _, t := range tasks.Items {
		report.Tasks.ByType[t.Spec.Type]++
		if t.Status.Phase != "" {
			report.Tasks.ByPhase[string(t.Status.Phase)]++
		}
		namespaces[t.Namespace] = struct{}{}

		// Aggregate usage from results.
		if t.Status.Results != nil {
			if v, ok := t.Status.Results["cost_usd"]; ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					report.Usage.TotalCostUSD += f
				}
			}
			if v, ok := t.Status.Results["input_tokens"]; ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					report.Usage.TotalInputTokens += f
				}
			}
			if v, ok := t.Status.Results["output_tokens"]; ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					report.Usage.TotalOutputTokens += f
				}
			}
		}
	}

	// Collect TaskSpawner data.
	var spawners kelosv1alpha1.TaskSpawnerList
	if err := c.List(ctx, &spawners); err != nil {
		return nil, fmt.Errorf("listing task spawners: %w", err)
	}
	report.Features.TaskSpawners = len(spawners.Items)
	sourceTypes := make(map[string]struct{})
	for _, s := range spawners.Items {
		namespaces[s.Namespace] = struct{}{}
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

	// Collect AgentConfig data.
	var agentConfigs kelosv1alpha1.AgentConfigList
	if err := c.List(ctx, &agentConfigs); err != nil {
		return nil, fmt.Errorf("listing agent configs: %w", err)
	}
	report.Features.AgentConfigs = len(agentConfigs.Items)
	for _, ac := range agentConfigs.Items {
		namespaces[ac.Namespace] = struct{}{}
	}

	// Collect Workspace data.
	var workspaces kelosv1alpha1.WorkspaceList
	if err := c.List(ctx, &workspaces); err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	report.Features.Workspaces = len(workspaces.Items)
	for _, w := range workspaces.Items {
		namespaces[w.Namespace] = struct{}{}
	}

	report.Scale.Namespaces = len(namespaces)

	return report, nil
}

func send(phClient PostHogClient, report *Report) error {
	properties := posthog.NewProperties().
		Set("version", report.Version).
		Set("k8s_version", report.K8sVersion).
		Set("environment", report.Environment).
		Set("tasks_total", report.Tasks.Total).
		Set("tasks_by_type", report.Tasks.ByType).
		Set("tasks_by_phase", report.Tasks.ByPhase).
		Set("feature_task_spawners", report.Features.TaskSpawners).
		Set("feature_agent_configs", report.Features.AgentConfigs).
		Set("feature_workspaces", report.Features.Workspaces).
		Set("feature_source_types", report.Features.SourceTypes).
		Set("scale_namespaces", report.Scale.Namespaces).
		Set("usage_total_cost_usd", report.Usage.TotalCostUSD).
		Set("usage_total_input_tokens", report.Usage.TotalInputTokens).
		Set("usage_total_output_tokens", report.Usage.TotalOutputTokens)

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
