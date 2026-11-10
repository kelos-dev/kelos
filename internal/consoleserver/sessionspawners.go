package consoleserver

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionbuilder"
)

func (s *Server) listSessionSpawners(writer http.ResponseWriter, request *http.Request) {
	namespace := s.requestNamespace(request)
	if !s.requireAccess(writer, request, access("list", "sessionspawners", namespace, "")) {
		return
	}
	var spawners kelos.SessionSpawnerList
	if err := s.client.List(request.Context(), &spawners, client.InNamespace(namespace)); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("listing SessionSpawners in namespace %q", namespace), err)
		return
	}
	writeJSON(writer, http.StatusOK, objectNames(spawners.Items, func(item kelos.SessionSpawner) string { return item.Name }))
}

func (s *Server) runSessionSpawner(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	if !s.requireAccess(writer, request, access("get", "sessionspawners", namespace, name), access("create", "sessions", namespace, "")) {
		return
	}
	var payload struct {
		Values string `json:"values"`
	}
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("running SessionSpawner %q: %v", name, err))
		return
	}
	var values map[string]interface{}
	if strings.TrimSpace(payload.Values) != "" {
		data, err := yaml.YAMLToJSON([]byte(payload.Values))
		if err == nil {
			err = json.Unmarshal(data, &values)
		}
		if err != nil || values == nil {
			writeError(writer, http.StatusBadRequest, fmt.Sprintf("running SessionSpawner %q: values must be a YAML or JSON object", name))
			return
		}
	}
	var spawner kelos.SessionSpawner
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &spawner); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting SessionSpawner %q", name), err)
		return
	}

	suffix := rand.String(5)
	now := time.Now().UTC().Format(time.RFC3339)
	vars := map[string]interface{}{
		"ID":          "manual-" + suffix,
		"Title":       "Manual run of SessionSpawner " + name,
		"Kind":        "Manual",
		"TriggerType": "manual",
		"TriggerTime": now,
	}
	for key, value := range values {
		vars[key] = value
	}
	prefix := name
	// Keep the random suffix within Kubernetes' 63-character name limit.
	if len(prefix) > 50 {
		prefix = strings.TrimRight(prefix[:50], "-.")
	}
	session, err := sessionbuilder.Build(prefix+"-manual-"+suffix, namespace, &spawner.Spec.SessionTemplate, vars, sessionbuilder.SpawnerRef{
		Name:       spawner.Name,
		UID:        spawner.UID,
		APIVersion: kelos.GroupVersion.String(),
		Kind:       "SessionSpawner",
	})
	if err == nil {
		err = sessionbuilder.AssignSpawnerCredential(&spawner, session)
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("running SessionSpawner %q: %v", name, err))
		return
	}
	session.Annotations["kelos.dev/trigger-type"] = "manual"
	session.Annotations["kelos.dev/trigger-time"] = now
	if err := s.client.Create(request.Context(), session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("creating Session %q from SessionSpawner %q", session.Name, name), err)
		return
	}
	writeJSON(writer, http.StatusCreated, summarizeForUser(request, session))
}
