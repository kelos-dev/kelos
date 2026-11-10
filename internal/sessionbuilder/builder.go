package sessionbuilder

import (
	"bytes"
	"fmt"
	"text/template"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/spawnercredentials"
)

const (
	// LabelSessionSpawner identifies the owning SessionSpawner by UID so every
	// valid Kubernetes resource name can be used without exceeding label limits.
	LabelSessionSpawner = "kelos.dev/sessionspawner"
	// AnnotationSessionSpawnerName retains the human-readable owner name.
	AnnotationSessionSpawnerName = "kelos.dev/sessionspawner-name"
)

// SpawnerRef identifies the SessionSpawner that owns a generated Session.
type SpawnerRef struct {
	Name       string
	UID        types.UID
	APIVersion string
	Kind       string
}

// Build creates a Session from a SessionSpawner template and template values.
func Build(
	name, namespace string,
	sessionTemplate *kelos.SessionTemplate,
	templateVars map[string]interface{},
	spawnerRef SpawnerRef,
) (*kelos.Session, error) {
	spec := sessionTemplate.SessionSpec.DeepCopy()

	initialPrompt, err := Render("initialPrompt", spec.InitialPrompt, templateVars)
	if err != nil {
		return nil, fmt.Errorf("rendering Session initialPrompt: %w", err)
	}
	spec.InitialPrompt = initialPrompt

	if spec.InitialBranch != "" {
		initialBranch, err := Render("initialBranch", spec.InitialBranch, templateVars)
		if err != nil {
			return nil, fmt.Errorf("rendering Session initialBranch: %w", err)
		}
		spec.InitialBranch = initialBranch
	}

	controller := true
	return &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				LabelSessionSpawner: string(spawnerRef.UID),
			},
			Annotations: map[string]string{
				AnnotationSessionSpawnerName: spawnerRef.Name,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: spawnerRef.APIVersion,
				Kind:       spawnerRef.Kind,
				Name:       spawnerRef.Name,
				UID:        spawnerRef.UID,
				Controller: &controller,
			}},
		},
		Spec: *spec,
	}, nil
}

// AssignSpawnerCredential randomly selects a configured SessionSpawner
// credential and pins it to session.
func AssignSpawnerCredential(spawner *kelos.SessionSpawner, session *kelos.Session) error {
	if len(spawner.Spec.Credentials) == 0 {
		return nil
	}
	if session.Spec.Worker.Credentials != nil {
		return fmt.Errorf("SessionSpawner credentials cannot be combined with sessionTemplate.worker.credentials")
	}

	selected := spawnercredentials.Select(spawner.Spec.Credentials)
	session.Spec.Worker.Credentials = spawnercredentials.Materialize(selected)
	if session.Labels == nil {
		session.Labels = make(map[string]string)
	}
	session.Labels[spawnercredentials.AssignmentLabel] = selected.Name
	return nil
}

// Render executes a SessionSpawner text template with strict missing-key handling.
func Render(name, value string, templateVars map[string]interface{}) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").Parse(value)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, templateVars); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return output.String(), nil
}
