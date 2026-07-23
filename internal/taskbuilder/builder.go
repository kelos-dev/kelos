package taskbuilder

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
	"text/template/parse"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// SpawnerLabel is set on Tasks created by a TaskSpawner and names the owning
// spawner.
const SpawnerLabel = "kelos.dev/taskspawner"

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
//
// When taskTemplate.NameTemplate is set it is rendered, sanitized into a valid
// Kubernetes resource name, and used as the Task name; otherwise the passed-in
// name is used unchanged.
func (tb *TaskBuilder) BuildTask(
	name, namespace string,
	taskTemplate *kelos.TaskTemplate,
	templateVars map[string]interface{},
	spawnerRef *SpawnerRef,
) (*kelos.Task, error) {
	// Resolve the Task name, rendering the name template when configured.
	name, err := ResolveTaskName(name, taskTemplate, templateVars)
	if err != nil {
		return nil, err
	}

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
	task := &kelos.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: kelos.TaskSpec{
			Type:        taskTemplate.Type,
			Credentials: taskTemplate.Credentials,
			Prompt:      prompt,
		},
	}

	// Set optional fields
	if taskTemplate.Model != "" {
		task.Spec.Model = taskTemplate.Model
	}
	if taskTemplate.Effort != "" {
		task.Spec.Effort = taskTemplate.Effort
	}
	if taskTemplate.Image != "" {
		task.Spec.Image = taskTemplate.Image
	}
	if taskTemplate.WorkspaceRef != nil {
		task.Spec.WorkspaceRef = taskTemplate.WorkspaceRef
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
	if taskTemplate.PodFailurePolicy != nil {
		task.Spec.PodFailurePolicy = taskTemplate.PodFailurePolicy
	}
	if taskTemplate.UpstreamRepo != "" {
		task.Spec.UpstreamRepo = taskTemplate.UpstreamRepo
	}
	if taskTemplate.WorkerPoolRef != nil {
		task.Spec.WorkerPoolRef = taskTemplate.WorkerPoolRef
	}
	if taskTemplate.Worker != nil {
		task.Spec.Worker = taskTemplate.Worker
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
		task.Labels[SpawnerLabel] = spawnerRef.Name

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

// ResolveTaskName determines the name of the Task to build. When
// taskTemplate.NameTemplate is set it is rendered with templateVars and
// sanitized into a valid Kubernetes resource name; otherwise defaultName is
// returned unchanged. Callers that deduplicate Tasks by name (e.g. the polling
// spawner) must use this to compute their lookup key so it matches the name
// BuildTask assigns.
func ResolveTaskName(defaultName string, taskTemplate *kelos.TaskTemplate, templateVars map[string]interface{}) (string, error) {
	if taskTemplate.NameTemplate == "" {
		return defaultName, nil
	}
	// A Task's identity must not depend on mutable external data, so context
	// sources are not permitted in nameTemplate. Reject any reference at parse
	// time — value-level guards (stripping the key + missingkey=error) cannot
	// catch every form, e.g. {{if index . "Context"}}x{{else}}y{{end}} consumes
	// the missing value and renders a constant.
	if err := checkNoContextReference(taskTemplate.NameTemplate); err != nil {
		return "", err
	}
	// Also strip the key and rely on missingkey=error as defense in depth.
	rendered, err := renderTemplate("name", taskTemplate.NameTemplate, nameTemplateVars(templateVars))
	if err != nil {
		return "", fmt.Errorf("failed to render name template: %w", err)
	}
	// Reject any other missing/unavailable value that rendered to Go's
	// "<no value>" placeholder (e.g. an index lookup of an absent key), which
	// would sanitize to a constant and collapse distinct work items to one name.
	if strings.Contains(rendered, "<no value>") {
		return "", fmt.Errorf("nameTemplate references a missing or unavailable value: %q", rendered)
	}
	name := sanitizeName(rendered)
	if name == "" {
		return "", fmt.Errorf("nameTemplate rendered to an empty name after sanitization: %q", rendered)
	}
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return "", fmt.Errorf("nameTemplate produced an invalid Task name %q: %s", name, strings.Join(errs, "; "))
	}
	return name, nil
}

// TaskBelongsToSpawner reports whether task was created by the identified
// TaskSpawner. When the Task carries a controller owner reference, its UID must
// equal spawnerUID — so a Task owned by a since-deleted spawner that shared this
// name is not mistaken for the current one. Only Tasks with no controller owner
// reference fall back to matching the kelos.dev/taskspawner label (legacy or
// manually-created Tasks). Used to decide whether an AlreadyExists collision is
// a genuine deduplication or a clash with an unrelated Task.
func TaskBelongsToSpawner(task *kelos.Task, spawnerName string, spawnerUID types.UID) bool {
	hasController := false
	for _, ref := range task.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			hasController = true
			if ref.UID == spawnerUID {
				return true
			}
		}
	}
	if hasController {
		return false
	}
	return task.Labels[SpawnerLabel] == spawnerName
}

// nameTemplateForbiddenKey is the template variable a nameTemplate must not
// reference: a Task's identity must not depend on mutable context data.
const nameTemplateForbiddenKey = "Context"

// checkNoContextReference parses tmplStr and returns an error if it references
// the Context variable in any form (.Context, .Context.x, {{index . "Context"}},
// or those nested in a conditional/pipeline). Parsing via the template package
// makes builtins like index available so the tree matches execution.
func checkNoContextReference(tmplStr string) error {
	tmpl, err := template.New("name").Option("missingkey=error").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse name template: %w", err)
	}
	if tmpl.Tree != nil && treeReferencesContext(tmpl.Tree.Root) {
		return fmt.Errorf("nameTemplate must not reference context sources (.Context or index %q)", nameTemplateForbiddenKey)
	}
	return nil
}

// treeReferencesContext reports whether any node in the parse tree references
// the forbidden Context key, as a field identifier or a string index key.
func treeReferencesContext(node parse.Node) bool {
	switch n := node.(type) {
	case nil:
		return false
	case *parse.ListNode:
		if n == nil {
			return false
		}
		for _, c := range n.Nodes {
			if treeReferencesContext(c) {
				return true
			}
		}
	case *parse.ActionNode:
		return treeReferencesContext(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return false
		}
		for _, cmd := range n.Cmds {
			if treeReferencesContext(cmd) {
				return true
			}
		}
	case *parse.CommandNode:
		for _, arg := range n.Args {
			if treeReferencesContext(arg) {
				return true
			}
		}
	case *parse.IfNode:
		return branchReferencesContext(n.Pipe, n.List, n.ElseList)
	case *parse.RangeNode:
		return branchReferencesContext(n.Pipe, n.List, n.ElseList)
	case *parse.WithNode:
		return branchReferencesContext(n.Pipe, n.List, n.ElseList)
	case *parse.TemplateNode:
		return treeReferencesContext(n.Pipe)
	case *parse.FieldNode:
		return len(n.Ident) > 0 && n.Ident[0] == nameTemplateForbiddenKey
	case *parse.ChainNode:
		if treeReferencesContext(n.Node) {
			return true
		}
		for _, f := range n.Field {
			if f == nameTemplateForbiddenKey {
				return true
			}
		}
	case *parse.VariableNode:
		for _, id := range n.Ident {
			if id == nameTemplateForbiddenKey {
				return true
			}
		}
	case *parse.StringNode:
		return n.Text == nameTemplateForbiddenKey
	}
	return false
}

func branchReferencesContext(pipe *parse.PipeNode, list, elseList *parse.ListNode) bool {
	return treeReferencesContext(pipe) || treeReferencesContext(list) || treeReferencesContext(elseList)
}

// nameTemplateVars returns templateVars without the "Context" key so context
// sources cannot be referenced from nameTemplate. The original map is not
// modified.
func nameTemplateVars(templateVars map[string]interface{}) map[string]interface{} {
	if _, ok := templateVars[nameTemplateForbiddenKey]; !ok {
		return templateVars
	}
	out := make(map[string]interface{}, len(templateVars))
	for k, v := range templateVars {
		if k != nameTemplateForbiddenKey {
			out[k] = v
		}
	}
	return out
}

// sanitizeName converts an arbitrary string into a valid Kubernetes resource
// name (RFC 1123 subdomain): lowercased, with any character that is not a
// lowercase alphanumeric, '-' or '.' replaced by '-', truncated to 63
// characters, and with each '.'-separated label trimmed of leading/trailing
// '-' and empty labels dropped so the result never contains an empty label or a
// label bounded by '-' (e.g. "a..b" and "a.-b" both become "a.b").
// Note: two inputs that differ only past character 63 sanitize to the same
// name; callers that rely on the name for deduplication must keep the
// identifying portion within the first 63 characters.
func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := b.String()
	if len(name) > 63 {
		name = name[:63]
	}
	// Normalize each dot-separated label so the name is a valid subdomain even
	// when the raw value has consecutive dots or '-' next to a dot. Truncation
	// happens first so trimming a label the cut left dangling is handled here.
	labels := strings.Split(name, ".")
	cleaned := make([]string, 0, len(labels))
	for _, label := range labels {
		if label = strings.Trim(label, "-"); label != "" {
			cleaned = append(cleaned, label)
		}
	}
	return strings.Join(cleaned, ".")
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
