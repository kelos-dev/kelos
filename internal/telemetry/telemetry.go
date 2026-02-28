package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
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
	// httpTimeout is the timeout for HTTP requests to the telemetry endpoint.
	httpTimeout = 10 * time.Second
)

// Report contains anonymous aggregate telemetry data.
type Report struct {
	InstallationID string        `json:"installationId"`
	Version        string        `json:"version"`
	K8sVersion     string        `json:"k8sVersion"`
	Timestamp      time.Time     `json:"timestamp"`
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

// Run collects anonymous aggregate telemetry and sends it to the endpoint.
func Run(ctx context.Context, log logr.Logger, c client.Client, clientset kubernetes.Interface, endpoint string) error {
	report, err := collect(ctx, c, clientset)
	if err != nil {
		return fmt.Errorf("collecting telemetry: %w", err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling report: %w", err)
	}
	log.Info("Telemetry report collected", "payload", string(data))

	if err := send(ctx, endpoint, report); err != nil {
		log.Error(err, "Failed to send telemetry report (non-fatal)")
		return nil
	}

	log.Info("Telemetry report sent successfully")
	return nil
}

func collect(ctx context.Context, c client.Client, clientset kubernetes.Interface) (*Report, error) {
	report := &Report{
		Timestamp: time.Now().UTC(),
		Version:   version.Version,
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

func send(ctx context.Context, endpoint string, report *Report) error {
	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshaling report: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kelos-telemetry/"+version.Version)

	httpClient := &http.Client{Timeout: httpTimeout}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
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
