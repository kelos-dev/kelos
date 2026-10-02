package consoleserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type adminResourceItem struct {
	consoleResourceSummary
	CanGet    bool `json:"canGet"`
	CanUpdate bool `json:"canUpdate"`
	CanDelete bool `json:"canDelete"`
}

type adminResourceCollection struct {
	Resource  string              `json:"resource"`
	Kind      string              `json:"kind"`
	Label     string              `json:"label"`
	CanCreate bool                `json:"canCreate"`
	Items     []adminResourceItem `json:"items"`
}

type adminManifest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Metadata   struct {
		Name            string            `json:"name"`
		Namespace       string            `json:"namespace"`
		ResourceVersion string            `json:"resourceVersion,omitempty"`
		Labels          map[string]string `json:"labels,omitempty"`
		Annotations     map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec json.RawMessage `json:"spec"`
}

func adminResource(resource string) bool {
	return resource == "workspaces" || resource == "agentconfigs" || resource == "workerpools"
}

func (s *Server) admin(writer http.ResponseWriter, request *http.Request, parts []string) {
	if len(parts) == 0 && request.Method == http.MethodGet {
		s.listAdminResources(writer, request)
		return
	}
	if len(parts) < 2 || len(parts) > 3 || !adminResource(parts[0]) {
		writeError(writer, http.StatusNotFound, "not found")
		return
	}
	definition, _ := consoleResourceDefinitionFor(parts[0])
	namespace := parts[1]
	if len(parts) == 2 && request.Method == http.MethodPost {
		s.saveAdminResource(writer, request, definition, namespace, "")
		return
	}
	if len(parts) != 3 {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := parts[2]
	switch request.Method {
	case http.MethodGet:
		if !s.requireAccess(writer, request, access("get", definition.Resource, namespace, name)) {
			return
		}
		object := definition.New()
		if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, object); err != nil {
			writeAdminError(writer, definition.Kind, name, err)
			return
		}
		data, err := json.Marshal(object)
		if err != nil {
			writeAdminError(writer, definition.Kind, name, err)
			return
		}
		var manifest adminManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			writeAdminError(writer, definition.Kind, name, err)
			return
		}
		manifest.APIVersion, manifest.Kind = kelos.GroupVersion.String(), definition.Kind
		data, err = yaml.Marshal(manifest)
		if err != nil {
			writeAdminError(writer, definition.Kind, name, err)
			return
		}
		writeJSON(writer, http.StatusOK, consoleResourceDetail{Resource: definition.Resource, Kind: definition.Kind, Name: name, Namespace: namespace, YAML: string(data)})
	case http.MethodPut:
		s.saveAdminResource(writer, request, definition, namespace, name)
	case http.MethodDelete:
		if !s.requireAccess(writer, request, access("delete", definition.Resource, namespace, name)) {
			return
		}
		object := definition.New()
		object.SetNamespace(namespace)
		object.SetName(name)
		if err := s.client.Delete(request.Context(), object); err != nil {
			writeAdminError(writer, definition.Kind, name, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]bool{"deleted": true})
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) listAdminResources(writer http.ResponseWriter, request *http.Request) {
	namespace := s.requestNamespace(request)
	collections := make([]adminResourceCollection, 0, 3)
	for _, resource := range []string{"workspaces", "agentconfigs", "workerpools"} {
		allowed, err := s.allowed(request, access("list", resource, namespace, ""))
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
			return
		}
		if !allowed {
			continue
		}
		definition, _ := consoleResourceDefinitionFor(resource)
		collection := adminResourceCollection{Resource: resource, Kind: definition.Kind, Label: definition.Label, Items: []adminResourceItem{}}
		collection.CanCreate, err = s.allowed(request, access("create", resource, namespace, ""))
		if err != nil {
			writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
			return
		}
		list := definition.NewList()
		if err := s.client.List(request.Context(), list, client.InNamespace(namespace)); err != nil {
			writeAdminError(writer, definition.Kind, "", err)
			return
		}
		objects, err := consoleResourceObjects(definition, list)
		if err != nil {
			writeAdminError(writer, definition.Kind, "", err)
			return
		}
		for _, object := range objects {
			item := adminResourceItem{consoleResourceSummary: object.Summary}
			for verb, target := range map[string]*bool{"get": &item.CanGet, "update": &item.CanUpdate, "delete": &item.CanDelete} {
				*target, err = s.allowed(request, access(verb, resource, namespace, item.Name))
				if err != nil {
					writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
					return
				}
			}
			collection.Items = append(collection.Items, item)
		}
		collections = append(collections, collection)
	}
	writeJSON(writer, http.StatusOK, collections)
}

func (s *Server) saveAdminResource(writer http.ResponseWriter, request *http.Request, definition consoleResourceDefinition, namespace, name string) {
	verb := "create"
	if name != "" {
		verb = "update"
	}
	if !s.requireAccess(writer, request, access(verb, definition.Resource, namespace, name)) {
		return
	}
	manifest, err := decodeAdminManifest(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if manifest.APIVersion != kelos.GroupVersion.String() || manifest.Kind != definition.Kind || manifest.Metadata.Name == "" {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("manifest must be a named %s %s", kelos.GroupVersion.String(), definition.Kind))
		return
	}
	if manifest.Metadata.Namespace != "" && manifest.Metadata.Namespace != namespace {
		writeError(writer, http.StatusBadRequest, "manifest namespace must match the selected namespace")
		return
	}
	manifest.Metadata.Namespace = namespace
	if name != "" && (manifest.Metadata.Name != name || manifest.Metadata.ResourceVersion == "") {
		writeError(writer, http.StatusBadRequest, "updates must preserve metadata.name and metadata.resourceVersion")
		return
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		writeAdminError(writer, definition.Kind, manifest.Metadata.Name, err)
		return
	}
	desired := definition.New()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(desired); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("invalid %s manifest: %v", definition.Kind, err))
		return
	}
	if name == "" {
		err = s.client.Create(request.Context(), desired)
	} else {
		if !s.requireAccess(writer, request, access("get", definition.Resource, namespace, name)) {
			return
		}
		current := definition.New()
		err = s.client.Get(request.Context(), client.ObjectKeyFromObject(desired), current)
		if err == nil {
			// Preserve controller-owned metadata and status while replacing the editable fields.
			switch object := current.(type) {
			case *kelos.Workspace:
				object.Spec = desired.(*kelos.Workspace).Spec
			case *kelos.AgentConfig:
				object.Spec = desired.(*kelos.AgentConfig).Spec
			case *kelos.WorkerPool:
				object.Spec = desired.(*kelos.WorkerPool).Spec
			}
			current.SetLabels(desired.GetLabels())
			current.SetAnnotations(desired.GetAnnotations())
			current.SetResourceVersion(desired.GetResourceVersion())
			err = s.client.Update(request.Context(), current)
		}
	}
	if err != nil {
		writeAdminError(writer, definition.Kind, manifest.Metadata.Name, err)
		return
	}
	status := http.StatusOK
	if name == "" {
		status = http.StatusCreated
	}
	writeJSON(writer, status, map[string]string{"name": manifest.Metadata.Name, "namespace": namespace})
}

func decodeAdminManifest(reader io.Reader) (*adminManifest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, requestBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("reading manifest: %w", err)
	}
	if len(data) > requestBodyLimit {
		return nil, fmt.Errorf("manifest exceeds %d bytes", requestBodyLimit)
	}
	documents := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var manifest *adminManifest
	for {
		document, err := documents.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading manifest: %w", err)
		}
		if len(bytes.TrimSpace(document)) == 0 {
			continue
		}
		if manifest != nil {
			return nil, errors.New("manifest must contain exactly one YAML document")
		}
		data, err := yaml.YAMLToJSONStrict(document)
		if err != nil {
			return nil, fmt.Errorf("invalid YAML: %w", err)
		}
		manifest = &adminManifest{}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(manifest); err != nil {
			return nil, fmt.Errorf("invalid manifest: %w", err)
		}
	}
	if manifest == nil || len(manifest.Spec) == 0 || bytes.Equal(manifest.Spec, []byte("null")) {
		return nil, errors.New("manifest with spec is required")
	}
	return manifest, nil
}

func writeAdminError(writer http.ResponseWriter, kind, name string, err error) {
	status := http.StatusInternalServerError
	switch {
	case apierrors.IsNotFound(err):
		status = http.StatusNotFound
	case apierrors.IsForbidden(err):
		status = http.StatusForbidden
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		status = http.StatusBadRequest
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		status = http.StatusConflict
	}
	writeError(writer, status, fmt.Sprintf("%s %q: %v", kind, name, err))
}
