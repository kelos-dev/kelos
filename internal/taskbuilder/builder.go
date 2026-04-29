package taskbuilder

import (
	"bytes"
	"fmt"
	"text/template"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

// TaskBuilder creates Tasks from templates and work item data.
type TaskBuilder struct {
	client client.Client
}

// SpawnerRef identifies the TaskSpawner that owns a created Task.
// When set, BuildTask adds the kelos.dev/taskspawner label and an owner reference.
type SpawnerRef struct {
	Name       string
	UID        string
	APIVersion string
	Kind       string
}

// NewTaskBuilder creates a new task builder.
func NewTaskBuilder(client client.Client) (*TaskBuilder, error) {
	return &TaskBuilder{
		client: client,
	}, nil
}

// BuildTask creates a Task from a template and template variables.
// If spawnerRef is non-nil the kelos.dev/taskspawner label and a controller
// owner reference are set on the resulting Task.
func (tb *TaskBuilder) BuildTask(
	name, namespace string,
	taskTemplate *v1alpha1.TaskTemplate,
	templateVars map[string]interface{},
	spawnerRef *SpawnerRef,
) (*v1alpha1.Task, error) {
	// Render the prompt template
	promptTemplate := taskTemplate.PromptTemplate
	if promptTemplate == "" {
		promptTemplate = "{{.Title}}" // Default template
	}

	prompt, err := renderTemplate("prompt", promptTemplate, templateVars)
	if err != nil {
		return nil, fmt.Errorf("failed to render prompt template: %w", err)
	}

	// Render the branch template
	branch := taskTemplate.Branch
	if branch != "" {
		branch, err = renderTemplate("branch", branch, templateVars)
		if err != nil {
			return nil, fmt.Errorf("failed to render branch template: %w", err)
		}
	}

	// Create the Task
	task := &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1alpha1.TaskSpec{
			Type:        taskTemplate.Type,
			Credentials: taskTemplate.Credentials,
			Prompt:      prompt,
		},
	}

	// Set optional fields
	if taskTemplate.Model != "" {
		task.Spec.Model = taskTemplate.Model
	}
	if taskTemplate.Image != "" {
		task.Spec.Image = taskTemplate.Image
	}
	if taskTemplate.WorkspaceRef != nil {
		task.Spec.WorkspaceRef = taskTemplate.WorkspaceRef
	}
	if taskTemplate.AgentConfigRef != nil {
		task.Spec.AgentConfigRef = taskTemplate.AgentConfigRef
	}
	if len(taskTemplate.AgentConfigRefs) > 0 {
		task.Spec.AgentConfigRefs = taskTemplate.AgentConfigRefs
	}
	if len(taskTemplate.DependsOn) > 0 {
		task.Spec.DependsOn = taskTemplate.DependsOn
	}
	if branch != "" {
		task.Spec.Branch = branch
	}
	if taskTemplate.TTLSecondsAfterFinished != nil {
		task.Spec.TTLSecondsAfterFinished = taskTemplate.TTLSecondsAfterFinished
	}
	if taskTemplate.PodOverrides != nil {
		task.Spec.PodOverrides = taskTemplate.PodOverrides
	}
	if taskTemplate.UpstreamRepo != "" {
		task.Spec.UpstreamRepo = taskTemplate.UpstreamRepo
	}

	// Apply template metadata
	if taskTemplate.Metadata != nil {
		// Render labels
		if len(taskTemplate.Metadata.Labels) > 0 {
			if task.Labels == nil {
				task.Labels = make(map[string]string)
			}
			for key, valueTemplate := range taskTemplate.Metadata.Labels {
				value, err := renderTemplate(fmt.Sprintf("label[%s]", key), valueTemplate, templateVars)
				if err != nil {
					return nil, fmt.Errorf("failed to render label %s: %w", key, err)
				}
				task.Labels[key] = value
			}
		}

		// Render annotations
		if len(taskTemplate.Metadata.Annotations) > 0 {
			if task.Annotations == nil {
				task.Annotations = make(map[string]string)
			}
			for key, valueTemplate := range taskTemplate.Metadata.Annotations {
				value, err := renderTemplate(fmt.Sprintf("annotation[%s]", key), valueTemplate, templateVars)
				if err != nil {
					return nil, fmt.Errorf("failed to render annotation %s: %w", key, err)
				}
				task.Annotations[key] = value
			}
		}
	}

	// Set spawner label and owner reference when a SpawnerRef is provided.
	if spawnerRef != nil {
		if task.Labels == nil {
			task.Labels = make(map[string]string)
		}
		task.Labels["kelos.dev/taskspawner"] = spawnerRef.Name

		isController := true
		task.OwnerReferences = append(task.OwnerReferences, metav1.OwnerReference{
			APIVersion: spawnerRef.APIVersion,
			Kind:       spawnerRef.Kind,
			Name:       spawnerRef.Name,
			UID:        types.UID(spawnerRef.UID),
			Controller: &isController,
		})
	}

	return task, nil
}

// renderTemplate renders a Go text template with the given variables.
func renderTemplate(name, templateStr string, vars map[string]interface{}) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(templateStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}
