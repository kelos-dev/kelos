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

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/version"
)

var log = ctrl.Log.WithName("telemetry")

const (
	// configMapName is the name of the ConfigMap storing the installation ID.
	configMapName = "axon-telemetry"
	// installationIDKey is the key in the ConfigMap for the installation ID.
	installationIDKey = "installation-id"
	// defaultNamespace is the namespace for the telemetry ConfigMap.
	defaultNamespace = "axon-system"
	// sendTimeout is the HTTP timeout for sending telemetry reports.
	sendTimeout = 10 * time.Second
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

// FeatureReport contains feature usage counts.
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

// Run collects and sends a telemetry report. It is designed to be called
// as a one-shot operation from a CronJob.
func Run(ctx context.Context, c client.Client, clientset kubernetes.Interface, endpoint string) error {
	report, err := collect(ctx, c, clientset)
	if err != nil {
		return fmt.Errorf("collecting telemetry: %w", err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling report: %w", err)
	}
	log.Info("Telemetry report collected", "report", string(data))

	if err := send(ctx, endpoint, report); err != nil {
		return fmt.Errorf("sending telemetry: %w", err)
	}

	log.Info("Telemetry report sent successfully")
	return nil
}

// collect gathers aggregate data from the Kubernetes API.
func collect(ctx context.Context, c client.Client, clientset kubernetes.Interface) (*Report, error) {
	installID, err := getOrCreateInstallationID(ctx, c, defaultNamespace)
	if err != nil {
		return nil, fmt.Errorf("getting installation ID: %w", err)
	}

	// Get K8s server version.
	k8sVersion := "unknown"
	if clientset != nil {
		sv, err := clientset.Discovery().ServerVersion()
		if err != nil {
			log.Error(err, "Failed to get Kubernetes server version")
		} else {
			k8sVersion = sv.GitVersion
		}
	}

	report := &Report{
		InstallationID: installID,
		Version:        version.Version,
		K8sVersion:     k8sVersion,
		Timestamp:      time.Now().UTC(),
	}

	// Collect task data.
	namespaces := make(map[string]struct{})

	var taskList axonv1alpha1.TaskList
	if err := c.List(ctx, &taskList); err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}

	byType := make(map[string]int)
	byPhase := make(map[string]int)
	var totalCost, totalInput, totalOutput float64

	for i := range taskList.Items {
		task := &taskList.Items[i]
		namespaces[task.Namespace] = struct{}{}
		byType[task.Spec.Type]++
		if task.Status.Phase != "" {
			byPhase[string(task.Status.Phase)]++
		}

		// Aggregate cost/token data from results.
		if results := task.Status.Results; results != nil {
			if costStr, ok := results["cost-usd"]; ok {
				if cost, err := strconv.ParseFloat(costStr, 64); err == nil && cost > 0 {
					totalCost += cost
				}
			}
			if inputStr, ok := results["input-tokens"]; ok {
				if tokens, err := strconv.ParseFloat(inputStr, 64); err == nil && tokens > 0 {
					totalInput += tokens
				}
			}
			if outputStr, ok := results["output-tokens"]; ok {
				if tokens, err := strconv.ParseFloat(outputStr, 64); err == nil && tokens > 0 {
					totalOutput += tokens
				}
			}
		}
	}

	report.Tasks = TaskReport{
		Total:   len(taskList.Items),
		ByType:  byType,
		ByPhase: byPhase,
	}
	report.Usage = UsageReport{
		TotalCostUSD:      totalCost,
		TotalInputTokens:  totalInput,
		TotalOutputTokens: totalOutput,
	}

	// Collect TaskSpawner data.
	var spawnerList axonv1alpha1.TaskSpawnerList
	if err := c.List(ctx, &spawnerList); err != nil {
		return nil, fmt.Errorf("listing task spawners: %w", err)
	}

	sourceTypesSet := make(map[string]struct{})
	for i := range spawnerList.Items {
		spawner := &spawnerList.Items[i]
		namespaces[spawner.Namespace] = struct{}{}
		for _, st := range extractSourceTypes(&spawner.Spec.When) {
			sourceTypesSet[st] = struct{}{}
		}
	}

	sourceTypes := make([]string, 0, len(sourceTypesSet))
	for st := range sourceTypesSet {
		sourceTypes = append(sourceTypes, st)
	}
	sort.Strings(sourceTypes)

	// Collect AgentConfig data.
	var agentConfigList axonv1alpha1.AgentConfigList
	if err := c.List(ctx, &agentConfigList); err != nil {
		return nil, fmt.Errorf("listing agent configs: %w", err)
	}
	for i := range agentConfigList.Items {
		namespaces[agentConfigList.Items[i].Namespace] = struct{}{}
	}

	// Collect Workspace data.
	var workspaceList axonv1alpha1.WorkspaceList
	if err := c.List(ctx, &workspaceList); err != nil {
		return nil, fmt.Errorf("listing workspaces: %w", err)
	}
	for i := range workspaceList.Items {
		namespaces[workspaceList.Items[i].Namespace] = struct{}{}
	}

	report.Features = FeatureReport{
		TaskSpawners: len(spawnerList.Items),
		AgentConfigs: len(agentConfigList.Items),
		Workspaces:   len(workspaceList.Items),
		SourceTypes:  sourceTypes,
	}

	report.Scale = ScaleReport{
		Namespaces: len(namespaces),
	}

	return report, nil
}

// send posts the telemetry report to the given endpoint.
func send(ctx context.Context, endpoint string, report *Report) error {
	data, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("marshaling report: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "axon-telemetry/"+version.Version)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return nil
}

// getOrCreateInstallationID retrieves or creates a persistent installation ID
// stored in a ConfigMap. This ID is a random UUID with no correlation to any
// identifiable information.
func getOrCreateInstallationID(ctx context.Context, c client.Client, namespace string) (string, error) {
	var cm corev1.ConfigMap
	key := types.NamespacedName{Name: configMapName, Namespace: namespace}

	err := c.Get(ctx, key, &cm)
	switch {
	case err == nil:
		// ConfigMap exists; return the stored ID if present.
		if id, ok := cm.Data[installationIDKey]; ok && id != "" {
			return id, nil
		}
		// ConfigMap exists but missing the key — update it below.

	case errors.IsNotFound(err):
		// ConfigMap does not exist — create it with a new ID.
		id := uuid.New().String()
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/name":      "axon",
					"app.kubernetes.io/component": "telemetry",
				},
			},
			Data: map[string]string{
				installationIDKey: id,
			},
		}
		if err := c.Create(ctx, &cm); err != nil {
			return "", fmt.Errorf("creating configmap: %w", err)
		}
		return id, nil

	default:
		return "", fmt.Errorf("getting configmap: %w", err)
	}

	// ConfigMap exists but the installation ID key is missing or empty.
	id := uuid.New().String()
	if cm.Data == nil {
		cm.Data = make(map[string]string)
	}
	cm.Data[installationIDKey] = id
	if err := c.Update(ctx, &cm); err != nil {
		return "", fmt.Errorf("updating configmap: %w", err)
	}
	return id, nil
}

// extractSourceTypes returns the source types configured on a When spec.
func extractSourceTypes(when *axonv1alpha1.When) []string {
	var types []string
	if when.GitHubIssues != nil {
		types = append(types, "github")
	}
	if when.Cron != nil {
		types = append(types, "cron")
	}
	if when.Jira != nil {
		types = append(types, "jira")
	}
	return types
}
