package taskbuilder

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/contextfetch"
	"github.com/kelos-dev/kelos/internal/source"
)

const (
	annotationCreatedFromTaskSpawner = "kelos.dev/created-from-taskspawner"
	annotationManualTrigger          = "kelos.dev/trigger-type"
	annotationManualTriggerTime      = "kelos.dev/trigger-time"
)

// ManualTaskOptions configures a standalone run of a TaskSpawner template.
type ManualTaskOptions struct {
	Name       string
	Values     map[string]interface{}
	Now        time.Time
	HTTPClient *http.Client
	Logger     logr.Logger
}

// BuildManualTask renders a standalone Task without applying the spawner's
// source filters, suspension, or concurrency limits. HTTPClient is required.
func BuildManualTask(ctx context.Context, cl client.Client, spawner *kelos.TaskSpawner, opts ManualTaskOptions) (*kelos.Task, error) {
	if opts.HTTPClient == nil {
		return nil, fmt.Errorf("HTTP client is required for TaskSpawner %s", spawner.Name)
	}
	if spawner.Spec.PipelineTemplate != nil {
		return nil, fmt.Errorf("TaskSpawner %s uses pipelineTemplate, which manual runs do not support", spawner.Name)
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	manualID := "manual-" + rand.String(5)
	vars := map[string]interface{}{
		"ID":          manualID,
		"Title":       "Manual run of TaskSpawner " + spawner.Name,
		"Kind":        "Manual",
		"TriggerType": "manual",
		"TriggerTime": now.Format(time.RFC3339),
	}
	for key, value := range opts.Values {
		vars[key] = value
	}
	if spawner.Spec.When.Cron != nil {
		vars["Time"] = now.Format(time.RFC3339)
		vars["Schedule"] = spawner.Spec.When.Cron.Schedule
	}

	if len(spawner.Spec.TaskTemplate.ContextSources) > 0 {
		fetcher := &contextfetch.Fetcher{
			Client:     cl,
			HTTPClient: opts.HTTPClient,
			Namespace:  spawner.Namespace,
			Logger:     opts.Logger,
		}
		contextData, err := fetcher.FetchAll(ctx, spawner.Spec.TaskTemplate.ContextSources, vars)
		if err != nil {
			return nil, fmt.Errorf("fetching context sources for TaskSpawner %s: %w", spawner.Name, err)
		}
		vars["Context"] = contextData
	}

	taskName := opts.Name
	if taskName == "" {
		taskName = manualTaskName(spawner.Name, strings.TrimPrefix(manualID, "manual-"))
	}
	builder, err := NewTaskBuilder(cl)
	if err != nil {
		return nil, fmt.Errorf("creating Task builder for TaskSpawner %s: %w", spawner.Name, err)
	}
	// Manual runs use an explicit name or a random suffix. The spawner's
	// nameTemplate targets automated deduplication and may require source variables.
	taskTemplate := spawner.Spec.TaskTemplate
	taskTemplate.NameTemplate = ""
	task, err := builder.BuildTask(taskName, spawner.Namespace, &taskTemplate, vars, nil)
	if err != nil {
		return nil, fmt.Errorf("building task %s from TaskSpawner %s: %w", taskName, spawner.Name, err)
	}
	if err := builder.AssignSpawnerCredential(spawner, task); err != nil {
		return nil, fmt.Errorf("assigning TaskSpawner credential to task %s from TaskSpawner %s: %w", taskName, spawner.Name, err)
	}
	if task.Spec.UpstreamRepo == "" {
		task.Spec.UpstreamRepo = taskSpawnerUpstreamRepo(spawner)
	}
	delete(task.Labels, "kelos.dev/taskspawner")
	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}
	task.Annotations[annotationCreatedFromTaskSpawner] = spawner.Name
	task.Annotations[annotationManualTrigger] = "manual"
	task.Annotations[annotationManualTriggerTime] = now.Format(time.RFC3339)
	task.SetGroupVersionKind(kelos.GroupVersion.WithKind("Task"))
	return task, nil
}

func taskSpawnerUpstreamRepo(spawner *kelos.TaskSpawner) string {
	var repo string
	if spawner.Spec.When.GitHubIssues != nil {
		repo = spawner.Spec.When.GitHubIssues.Repo
	} else if spawner.Spec.When.GitHubPullRequests != nil {
		repo = spawner.Spec.When.GitHubPullRequests.Repo
	}
	return source.GitHubRepositoryName(repo)
}

func manualTaskName(spawnerName, suffix string) string {
	const separator = "-manual-"
	maxPrefix := 63 - len(separator) - len(suffix)
	prefix := spawnerName
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], "-.")
	}
	return prefix + separator + suffix
}
