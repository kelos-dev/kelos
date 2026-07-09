package cli

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/spf13/cobra"
	json "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/contextfetch"
	"github.com/kelos-dev/kelos/internal/source"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

const (
	annotationCreatedFromTaskSpawner = "kelos.dev/created-from-taskspawner"
	annotationManualTrigger          = "kelos.dev/trigger-type"
	annotationManualTriggerTime      = "kelos.dev/trigger-time"
)

type runFromTaskSpawnerOptions struct {
	Name        string
	Values      map[string]interface{}
	Now         time.Time
	HTTPClient  *http.Client
	ErrorWriter io.Writer
}

func validateTaskSpawnerRunFlags(cmd *cobra.Command) error {
	incompatible := []string{
		"prompt",
		"prompt-file",
		"type",
		"secret",
		"credential-type",
		"model",
		"effort",
		"image",
		"workspace",
		"yes",
		"timeout",
		"env",
		"agent-config",
		"depends-on",
		"branch",
	}
	for _, flag := range incompatible {
		if cmd.Flags().Changed(flag) {
			return fmt.Errorf("--%s cannot be used with --from", flag)
		}
	}
	return nil
}

func parseTaskSpawnerReference(ref string) (string, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("invalid TaskSpawner reference %q: expected taskspawner/name", ref)
	}
	switch parts[0] {
	case "taskspawner", "taskspawners", "ts":
		return parts[1], nil
	default:
		return "", fmt.Errorf("invalid TaskSpawner reference %q: expected taskspawner/name", ref)
	}
}

func readTaskTemplateValues(path string, provided bool, stdin io.Reader) (map[string]interface{}, error) {
	if !provided {
		return nil, nil
	}
	if path == "" {
		return nil, fmt.Errorf("--values must name a file or - for stdin")
	}

	var (
		data []byte
		err  error
	)
	if path == "-" {
		data, err = io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading --values from stdin: %w", err)
		}
	} else {
		expanded, expandErr := expandHome(path)
		if expandErr != nil {
			return nil, fmt.Errorf("expanding --values path %s: %w", path, expandErr)
		}
		data, err = os.ReadFile(expanded)
		if err != nil {
			return nil, fmt.Errorf("reading --values file %s: %w", path, err)
		}
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, fmt.Errorf("--values file must not be empty")
	}

	jsonData, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, fmt.Errorf("parsing --values file: %w", err)
	}
	var values map[string]interface{}
	if err := json.Unmarshal(jsonData, &values); err != nil {
		return nil, fmt.Errorf("parsing --values file: expected a top-level object: %w", err)
	}
	if values == nil {
		return nil, fmt.Errorf("parsing --values file: expected a top-level object")
	}
	return values, nil
}

func createTaskFromTaskSpawner(ctx context.Context, cl client.Client, namespace, spawnerName string, opts runFromTaskSpawnerOptions) (*kelos.Task, error) {
	task, err := buildTaskFromTaskSpawner(ctx, cl, namespace, spawnerName, opts)
	if err != nil {
		return nil, err
	}
	if err := cl.Create(ctx, task); err != nil {
		return nil, fmt.Errorf("creating task %s from TaskSpawner %s: %w", task.Name, spawnerName, err)
	}
	return task, nil
}

func buildTaskFromTaskSpawner(ctx context.Context, cl client.Client, namespace, spawnerName string, opts runFromTaskSpawnerOptions) (*kelos.Task, error) {
	var spawner kelos.TaskSpawner
	if err := cl.Get(ctx, client.ObjectKey{Name: spawnerName, Namespace: namespace}, &spawner); err != nil {
		return nil, fmt.Errorf("getting TaskSpawner %s: %w", spawnerName, err)
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

	httpClient := taskSpawnerHTTPClient(opts.HTTPClient)
	if len(spawner.Spec.TaskTemplate.ContextSources) > 0 {
		errorWriter := opts.ErrorWriter
		if errorWriter == nil {
			errorWriter = os.Stderr
		}
		warningLog := log.New(errorWriter, "Warning: ", 0)
		logger := funcr.New(func(prefix, args string) {
			warningLog.Print(strings.TrimSpace(prefix + " " + args))
		}, funcr.Options{})
		fetcher := &contextfetch.Fetcher{
			Client:     cl,
			HTTPClient: httpClient,
			Namespace:  namespace,
			Logger:     logger,
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
	builder, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		return nil, fmt.Errorf("creating Task builder for TaskSpawner %s: %w", spawner.Name, err)
	}
	task, err := builder.BuildTask(taskName, namespace, &spawner.Spec.TaskTemplate, vars, nil)
	if err != nil {
		return nil, fmt.Errorf("building task %s from TaskSpawner %s: %w", taskName, spawner.Name, err)
	}
	if task.Spec.UpstreamRepo == "" {
		task.Spec.UpstreamRepo = taskSpawnerUpstreamRepo(&spawner)
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

func taskSpawnerHTTPClient(configured *http.Client) *http.Client {
	if configured != nil {
		return configured
	}
	return http.DefaultClient
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
