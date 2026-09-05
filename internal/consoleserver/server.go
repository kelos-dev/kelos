package consoleserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/uuid"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionattachment"
	"github.com/kelos-dev/kelos/internal/sessionreset"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"github.com/kelos-dev/kelos/internal/sessionsuspend"
)

const (
	authCookieName               = "kelos_console_auth"
	sessionRuntimeClient         = "/kelos/bin/kelos-session-runtime"
	sessionApplyManager          = "kelos-console-server"
	sessionDisplayNameAnnotation = "kelos.dev/session-display-name"
	sessionSectionAnnotation     = "kelos.dev/session-section"
	maxSessionDisplayNameLength  = 64
	maxSessionSectionLength      = 64
	requestBodyLimit             = 1024 * 1024
	attachmentRequestLimit       = sessionruntime.MaxAttachmentBytes + 1024*1024
	taskLogTailLineLimit         = 2000
	taskLogTailByteLimit         = 2 * 1024 * 1024
	taskLogScannerMaxTokenSize   = 10 * 1024 * 1024
	taskLogOmittedMessage        = "[... earlier log output omitted ...]\n"
	workerTaskLogLineLimit       = taskLogTailLineLimit + 2
	workerTaskLogSinceMargin     = 5 * time.Second
)

var errTaskLogSegmentUnavailable = errors.New("Task log segment is unavailable in the recent WorkerPool log window")

//go:embed web/*
var webFiles embed.FS

// Config contains dependencies and authentication configuration for the Console server.
type Config struct {
	Token            string
	Client           client.Client
	Clientset        *kubernetes.Clientset
	RESTConfig       *rest.Config
	DefaultNamespace string
	SecureCookie     bool
}

// Server serves the Kelos Console and its Kubernetes-backed API.
type Server struct {
	token            []byte
	cookieValue      string
	client           client.Client
	clientset        *kubernetes.Clientset
	restConfig       *rest.Config
	defaultNamespace string
	secureCookie     bool
	handler          http.Handler
	upgrader         websocket.Upgrader
	bridge           func(context.Context, *sessionSocket, string, string, func() error) error
	attachments      sessionAttachmentTransfer
	taskLogStream    func(context.Context, *kelos.Task, int64) (io.ReadCloser, error)
}

type sessionAttachmentTransfer interface {
	Upload(context.Context, string, string, string, io.Reader) (sessionruntime.Attachment, error)
	Download(context.Context, string, string, string) (sessionruntime.Attachment, []byte, error)
}

type sessionSocket struct {
	*websocket.Conn
	writeMu sync.Mutex
}

func (c *sessionSocket) WriteJSON(value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteJSON(value)
}

func (c *sessionSocket) WriteMessage(messageType int, data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.Conn.WriteMessage(messageType, data)
}

type sessionSummary struct {
	Name            string                    `json:"name"`
	DisplayName     string                    `json:"displayName"`
	Namespace       string                    `json:"namespace"`
	UID             string                    `json:"uid,omitempty"`
	Provider        string                    `json:"provider"`
	Model           string                    `json:"model,omitempty"`
	Phase           kelos.SessionPhase        `json:"phase,omitempty"`
	Active          *bool                     `json:"active,omitempty"`
	WaitingForInput bool                      `json:"waitingForInput,omitempty"`
	CreatedAt       *metav1.Time              `json:"createdAt,omitempty"`
	LastActivityAt  *metav1.Time              `json:"lastActivityAt,omitempty"`
	Message         string                    `json:"message,omitempty"`
	Branch          string                    `json:"branch,omitempty"`
	PullRequest     *kelos.SessionPullRequest `json:"pullRequest,omitempty"`
	Section         string                    `json:"section,omitempty"`
	Resetting       bool                      `json:"resetting,omitempty"`
	UserSuspended   bool                      `json:"userSuspended,omitempty"`
	IdleSuspended   bool                      `json:"idleSuspended,omitempty"`
}

type sessionOptions struct {
	Credentials  []credentialOption `json:"credentials"`
	Workspaces   []string           `json:"workspaces"`
	AgentConfigs []string           `json:"agentConfigs"`
	Sessions     []string           `json:"sessions"`
}

type credentialOption struct {
	Name     string               `json:"name"`
	Type     kelos.CredentialType `json:"type"`
	Provider string               `json:"provider"`
}

type createSessionRequest struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Section   string `json:"section,omitempty"`

	kelos.SessionSpec `json:",inline"`
}

type updateSessionSectionRequest struct {
	Section string `json:"section"`
}

type updateSessionDisplayNameRequest struct {
	DisplayName string `json:"displayName"`
}

type sessionManifest struct {
	APIVersion string                  `json:"apiVersion"`
	Kind       string                  `json:"kind"`
	Metadata   sessionManifestMetadata `json:"metadata"`
	Spec       kelos.SessionSpec       `json:"spec"`
}

type sessionManifestMetadata struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type sessionSourceDetail struct {
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	Manifest  sessionManifest `json:"manifest"`
	YAML      string          `json:"yaml"`
}

type consoleResourceDefinition struct {
	Resource string
	Kind     string
	Label    string
	Group    string
	NewList  func() client.ObjectList
	New      func() client.Object
}

type consoleResourceGroup struct {
	Name      string                      `json:"name"`
	Resources []consoleResourceCollection `json:"resources"`
}

type consoleResourceCollection struct {
	Resource string                   `json:"resource"`
	Kind     string                   `json:"kind"`
	Label    string                   `json:"label"`
	Items    []consoleResourceSummary `json:"items"`
}

type consoleResourceSummary struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	CreatedAt string `json:"createdAt,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Message   string `json:"message,omitempty"`
}

type consoleResourceReference struct {
	Resource string `json:"resource"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

type consoleResourceRelationship struct {
	Source       consoleResourceReference `json:"source"`
	Target       consoleResourceReference `json:"target"`
	Relationship string                   `json:"relationship"`
	Inferred     bool                     `json:"inferred,omitempty"`
}

type consoleResourceObject struct {
	Definition consoleResourceDefinition
	Summary    consoleResourceSummary
	Object     client.Object
}

type consoleResourceDetail struct {
	Resource  string `json:"resource"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	YAML      string `json:"yaml"`
}

var consoleResourceDefinitions = []consoleResourceDefinition{
	{Resource: "sessions", Kind: "Session", Label: "Sessions", Group: "Workloads", NewList: func() client.ObjectList { return &kelos.SessionList{} }, New: func() client.Object { return &kelos.Session{} }},
	{Resource: "tasks", Kind: "Task", Label: "Tasks", Group: "Workloads", NewList: func() client.ObjectList { return &kelos.TaskList{} }, New: func() client.Object { return &kelos.Task{} }},
	{Resource: "taskpipelines", Kind: "TaskPipeline", Label: "Task pipelines", Group: "Workloads", NewList: func() client.ObjectList { return &kelos.TaskPipelineList{} }, New: func() client.Object { return &kelos.TaskPipeline{} }},
	{Resource: "taskrecords", Kind: "TaskRecord", Label: "Task records", Group: "Workloads", NewList: func() client.ObjectList { return &kelos.TaskRecordList{} }, New: func() client.Object { return &kelos.TaskRecord{} }},
	{Resource: "taskspawners", Kind: "TaskSpawner", Label: "Task spawners", Group: "Automation", NewList: func() client.ObjectList { return &kelos.TaskSpawnerList{} }, New: func() client.Object { return &kelos.TaskSpawner{} }},
	{Resource: "sessionspawners", Kind: "SessionSpawner", Label: "Session spawners", Group: "Automation", NewList: func() client.ObjectList { return &kelos.SessionSpawnerList{} }, New: func() client.Object { return &kelos.SessionSpawner{} }},
	{Resource: "workerpools", Kind: "WorkerPool", Label: "Worker pools", Group: "Capacity", NewList: func() client.ObjectList { return &kelos.WorkerPoolList{} }, New: func() client.Object { return &kelos.WorkerPool{} }},
	{Resource: "taskbudgets", Kind: "TaskBudget", Label: "Task budgets", Group: "Capacity", NewList: func() client.ObjectList { return &kelos.TaskBudgetList{} }, New: func() client.Object { return &kelos.TaskBudget{} }},
	{Resource: "workspaces", Kind: "Workspace", Label: "Workspaces", Group: "Configuration", NewList: func() client.ObjectList { return &kelos.WorkspaceList{} }, New: func() client.Object { return &kelos.Workspace{} }},
	{Resource: "agentconfigs", Kind: "AgentConfig", Label: "Agent configs", Group: "Configuration", NewList: func() client.ObjectList { return &kelos.AgentConfigList{} }, New: func() client.Object { return &kelos.AgentConfig{} }},
}

// New validates config and creates the HTTP handler.
func New(config Config) (*Server, error) {
	if strings.TrimSpace(config.Token) == "" {
		return nil, errors.New("static authentication token must not be empty")
	}
	defaultNamespace := strings.TrimSpace(config.DefaultNamespace)
	if defaultNamespace == "" {
		return nil, errors.New("default Session namespace must not be empty")
	}
	if config.Client == nil || config.Clientset == nil || config.RESTConfig == nil {
		return nil, errors.New("Kubernetes clients and REST config are required")
	}
	attachmentClient, err := sessionattachment.New(config.RESTConfig)
	if err != nil {
		return nil, err
	}
	digest := hmac.New(sha256.New, []byte(config.Token))
	_, _ = digest.Write([]byte("kelos-console-cookie-v1"))
	server := &Server{
		token:            []byte(config.Token),
		cookieValue:      base64.RawURLEncoding.EncodeToString(digest.Sum(nil)),
		client:           config.Client,
		clientset:        config.Clientset,
		restConfig:       config.RESTConfig,
		defaultNamespace: defaultNamespace,
		secureCookie:     config.SecureCookie,
		attachments:      attachmentClient,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  16 * 1024,
			WriteBufferSize: 16 * 1024,
			CheckOrigin: func(request *http.Request) bool {
				return request.Header.Get("Origin") == "" || sameOrigin(request)
			},
		},
	}
	server.bridge = server.bridgeExec
	server.taskLogStream = server.openTaskLogStream
	server.handler = server.routes()
	return server, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(writer, request)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("GET /public/", s.publicAsset)
	mux.Handle("/api/", s.requireAuth(http.HandlerFunc(s.api)))
	mux.Handle("/assets/", s.requireAuth(http.HandlerFunc(s.asset)))
	mux.Handle("/", s.requireAuth(http.HandlerFunc(s.index)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) login(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if !s.validToken(payload.Token) {
		writeError(writer, http.StatusUnauthorized, "invalid token")
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     authCookieName,
		Value:    s.cookieValue,
		Path:     "/",
		MaxAge:   7 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   s.secureCookie,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": true})
}

func (s *Server) validToken(value string) bool {
	if len(value) != len(s.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), s.token) == 1
}

func (s *Server) authenticated(request *http.Request) bool {
	authorization := request.Header.Get("Authorization")
	if strings.HasPrefix(authorization, "Bearer ") && s.validToken(strings.TrimPrefix(authorization, "Bearer ")) {
		return true
	}
	cookie, err := request.Cookie(authCookieName)
	if err != nil || len(cookie.Value) != len(s.cookieValue) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.cookieValue)) == 1
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if s.authenticated(request) {
			next.ServeHTTP(writer, request)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/api/") {
			writeError(writer, http.StatusUnauthorized, "authentication required")
			return
		}
		http.Redirect(writer, request, "/login", http.StatusFound)
	})
}

func (s *Server) api(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/")
	if path == "config" && request.Method == http.MethodGet {
		writeJSON(writer, http.StatusOK, map[string]string{"defaultNamespace": s.defaultNamespace})
		return
	}
	if path == "options" && request.Method == http.MethodGet {
		s.listSessionOptions(writer, request)
		return
	}
	if path == "logout" && request.Method == http.MethodPost {
		http.SetCookie(writer, &http.Cookie{Name: authCookieName, Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		writeJSON(writer, http.StatusOK, map[string]bool{"authenticated": false})
		return
	}
	if path == "sessions" {
		switch request.Method {
		case http.MethodGet:
			s.listSessions(writer, request)
		case http.MethodPost:
			s.createSession(writer, request)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if path == "sessions/apply" && request.Method == http.MethodPost {
		s.applySession(writer, request)
		return
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "resources" && request.Method == http.MethodGet {
		s.listConsoleResources(writer, request)
		return
	}
	if len(parts) == 4 && parts[0] == "resources" && request.Method == http.MethodGet {
		s.getConsoleResource(writer, request, parts[1], parts[2], parts[3])
		return
	}
	if len(parts) == 5 && parts[0] == "resources" && parts[1] == "tasks" && parts[4] == "logs" && request.Method == http.MethodGet {
		s.getTaskLogs(writer, request, parts[2], parts[3])
		return
	}
	if len(parts) < 3 || parts[0] != "sessions" {
		writeError(writer, http.StatusNotFound, "not found")
		return
	}
	namespace, name := parts[1], parts[2]
	if len(parts) == 4 && parts[3] == "connect" && request.Method == http.MethodGet {
		s.connectSession(writer, request, namespace, name)
		return
	}
	if len(parts) == 4 && parts[3] == "attachments" && request.Method == http.MethodPost {
		s.uploadSessionAttachment(writer, request, namespace, name)
		return
	}
	if len(parts) == 5 && parts[3] == "attachments" && request.Method == http.MethodGet {
		s.downloadSessionAttachment(writer, request, namespace, name, parts[4])
		return
	}
	if len(parts) == 4 && parts[3] == "reset" && request.Method == http.MethodPost {
		s.resetSession(writer, request, namespace, name)
		return
	}
	if len(parts) == 4 && parts[3] == "suspend" && request.Method == http.MethodPost {
		s.suspendSession(writer, request, namespace, name)
		return
	}
	if len(parts) == 4 && parts[3] == "resume" && request.Method == http.MethodPost {
		s.resumeSession(writer, request, namespace, name)
		return
	}
	if len(parts) == 4 && parts[3] == "section" && request.Method == http.MethodPatch {
		s.updateSessionSection(writer, request, namespace, name)
		return
	}
	if len(parts) == 4 && parts[3] == "display-name" && request.Method == http.MethodPatch {
		s.updateSessionDisplayName(writer, request, namespace, name)
		return
	}
	if len(parts) != 3 {
		writeError(writer, http.StatusNotFound, "not found")
		return
	}
	switch request.Method {
	case http.MethodGet:
		s.getSessionSource(writer, request, namespace, name)
	case http.MethodDelete:
		s.deleteSession(writer, request, namespace, name)
	default:
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *Server) listConsoleResources(writer http.ResponseWriter, request *http.Request) {
	namespace := s.requestNamespace(request)
	groups := make([]consoleResourceGroup, 0, 4)
	groupIndexes := make(map[string]int)
	allObjects := make([]consoleResourceObject, 0)
	for _, definition := range consoleResourceDefinitions {
		list := definition.NewList()
		if err := s.client.List(request.Context(), list, client.InNamespace(namespace)); err != nil {
			writeError(writer, http.StatusInternalServerError, fmt.Sprintf("listing %s: %v", definition.Label, err))
			return
		}
		objects, err := consoleResourceObjects(definition, list)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, fmt.Sprintf("summarizing %s: %v", definition.Label, err))
			return
		}
		allObjects = append(allObjects, objects...)
		items := make([]consoleResourceSummary, 0, len(objects))
		for _, object := range objects {
			items = append(items, object.Summary)
		}
		index, ok := groupIndexes[definition.Group]
		if !ok {
			index = len(groups)
			groupIndexes[definition.Group] = index
			groups = append(groups, consoleResourceGroup{Name: definition.Group})
		}
		groups[index].Resources = append(groups[index].Resources, consoleResourceCollection{
			Resource: definition.Resource,
			Kind:     definition.Kind,
			Label:    definition.Label,
			Items:    items,
		})
	}
	relationships, err := consoleResourceRelationships(allObjects)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("relating resources: %v", err))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"namespace": namespace, "groups": groups, "relationships": relationships})
}

func consoleResourceObjects(definition consoleResourceDefinition, list client.ObjectList) ([]consoleResourceObject, error) {
	objects, err := apiMeta.ExtractList(list)
	if err != nil {
		return nil, err
	}
	items := make([]consoleResourceObject, 0, len(objects))
	for _, object := range objects {
		resourceObject, ok := object.(client.Object)
		if !ok {
			return nil, fmt.Errorf("%T does not implement client.Object", object)
		}
		content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
		if err != nil {
			return nil, err
		}
		phase, _, _ := unstructured.NestedString(content, "status", "phase")
		if phase == "" {
			phase, _, _ = unstructured.NestedString(content, "spec", "phase")
		}
		if phase == "" {
			phase = consoleResourceCondition(content)
		}
		message, _, _ := unstructured.NestedString(content, "status", "message")
		createdAt := ""
		created := resourceObject.GetCreationTimestamp()
		if !created.IsZero() {
			createdAt = created.UTC().Format(time.RFC3339)
		}
		items = append(items, consoleResourceObject{
			Definition: definition,
			Summary: consoleResourceSummary{
				Name:      resourceObject.GetName(),
				Namespace: resourceObject.GetNamespace(),
				CreatedAt: createdAt,
				Phase:     phase,
				Message:   message,
			},
			Object: resourceObject,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Summary.CreatedAt != items[j].Summary.CreatedAt {
			return items[i].Summary.CreatedAt > items[j].Summary.CreatedAt
		}
		return items[i].Summary.Name < items[j].Summary.Name
	})
	return items, nil
}

func consoleResourceRelationships(objects []consoleResourceObject) ([]consoleResourceRelationship, error) {
	relationships := make(map[string]consoleResourceRelationship)
	add := func(source, target consoleResourceReference, relationship string, inferred bool) {
		if source.Name == "" || target.Name == "" {
			return
		}
		key := strings.Join([]string{source.Resource, source.Name, target.Resource, target.Name, relationship}, "\x00")
		relationships[key] = consoleResourceRelationship{Source: source, Target: target, Relationship: relationship, Inferred: inferred}
	}

	for _, object := range objects {
		source := consoleReference(object.Definition, object.Summary.Name)
		switch value := object.Object.(type) {
		case *kelos.Task:
			consoleWorkerRelationships(source, value.Spec.Worker, add)
			consoleLegacyWorkerRelationships(source, value.Spec.WorkspaceRef, value.Spec.AgentConfigRefs, add)
			if value.Spec.WorkerPoolRef != nil {
				add(source, consoleReferenceFor("workerpools", value.Spec.WorkerPoolRef.Name), "runs on", false)
			}
			for _, dependency := range value.Spec.DependsOn {
				add(source, consoleReferenceFor("tasks", dependency), "depends on", false)
			}
			if consoleOwnerRelationship(object.Object, source, "TaskPipeline", "creates", add) {
				break
			}
			if !consoleOwnerRelationship(object.Object, source, "TaskSpawner", "creates", add) {
				origin := value.Annotations["kelos.dev/created-from-taskspawner"]
				if origin != "" {
					add(consoleReferenceFor("taskspawners", origin), source, "creates", false)
				} else {
					add(consoleReferenceFor("taskspawners", value.Labels["kelos.dev/taskspawner"]), source, "creates", true)
				}
			}
		case *kelos.Session:
			consoleWorkerRelationships(source, &value.Spec.Worker, add)
			if !consoleOwnerRelationship(object.Object, source, "SessionSpawner", "creates", add) {
				add(consoleReferenceFor("sessionspawners", value.Annotations["kelos.dev/sessionspawner-name"]), source, "creates", false)
			}
		case *kelos.TaskSpawner:
			consoleWorkerRelationships(source, value.Spec.TaskTemplate.Worker, add)
			consoleLegacyWorkerRelationships(source, value.Spec.TaskTemplate.WorkspaceRef, value.Spec.TaskTemplate.AgentConfigRefs, add)
			if value.Spec.TaskTemplate.WorkerPoolRef != nil {
				add(source, consoleReferenceFor("workerpools", value.Spec.TaskTemplate.WorkerPoolRef.Name), "runs Tasks on", false)
			}
			for _, dependency := range value.Spec.TaskTemplate.DependsOn {
				add(source, consoleReferenceFor("tasks", dependency), "spawned Tasks depend on", false)
			}
		case *kelos.TaskPipeline:
			for _, stage := range value.Spec.Stages {
				consoleWorkerRelationships(source, stage.TaskTemplate.Worker, add)
				if stage.TaskTemplate.WorkerPoolRef != nil {
					add(source, consoleReferenceFor("workerpools", stage.TaskTemplate.WorkerPoolRef.Name), "runs Tasks on", false)
				}
			}
		case *kelos.SessionSpawner:
			consoleWorkerRelationships(source, &value.Spec.SessionTemplate.Worker, add)
		case *kelos.WorkerPool:
			consoleWorkerRelationships(source, &value.Spec.Worker, add)
		case *kelos.TaskRecord:
			add(consoleReferenceFor("tasks", value.Spec.TaskRef.Name), source, "recorded as", false)
		}
	}

	budgetCandidates := make([]consoleResourceObject, 0)
	for _, object := range objects {
		switch object.Object.(type) {
		case *kelos.Task, *kelos.TaskRecord:
			budgetCandidates = append(budgetCandidates, object)
		}
	}
	for _, object := range objects {
		budget, ok := object.Object.(*kelos.TaskBudget)
		if !ok {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(&budget.Spec.TaskSelector)
		if err != nil {
			return nil, fmt.Errorf("TaskBudget %q selector: %w", budget.Name, err)
		}
		source := consoleReference(object.Definition, object.Summary.Name)
		for _, candidate := range budgetCandidates {
			if !selector.Matches(labels.Set(candidate.Object.GetLabels())) {
				continue
			}
			target := consoleReference(candidate.Definition, candidate.Summary.Name)
			switch candidate.Object.(type) {
			case *kelos.Task:
				add(source, target, "limits", true)
			case *kelos.TaskRecord:
				add(source, target, "accounts for", true)
			}
		}
	}

	result := make([]consoleResourceRelationship, 0, len(relationships))
	for _, relationship := range relationships {
		result = append(result, relationship)
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i]
		right := result[j]
		return strings.Join([]string{left.Source.Resource, left.Source.Name, left.Target.Resource, left.Target.Name, left.Relationship}, "\x00") <
			strings.Join([]string{right.Source.Resource, right.Source.Name, right.Target.Resource, right.Target.Name, right.Relationship}, "\x00")
	})
	return result, nil
}

func consoleReference(definition consoleResourceDefinition, name string) consoleResourceReference {
	return consoleResourceReference{Resource: definition.Resource, Kind: definition.Kind, Name: name}
}

func consoleReferenceFor(resource, name string) consoleResourceReference {
	definition, ok := consoleResourceDefinitionFor(resource)
	if !ok {
		return consoleResourceReference{Resource: resource, Name: name}
	}
	return consoleReference(definition, name)
}

func consoleWorkerRelationships(source consoleResourceReference, worker *kelos.WorkerSpec, add func(consoleResourceReference, consoleResourceReference, string, bool)) {
	if worker == nil {
		return
	}
	consoleLegacyWorkerRelationships(source, worker.WorkspaceRef, worker.AgentConfigRefs, add)
}

func consoleLegacyWorkerRelationships(source consoleResourceReference, workspace *kelos.WorkspaceReference, agentConfigs []kelos.AgentConfigReference, add func(consoleResourceReference, consoleResourceReference, string, bool)) {
	if workspace != nil {
		add(source, consoleReferenceFor("workspaces", workspace.Name), "uses", false)
	}
	for _, agentConfig := range agentConfigs {
		add(source, consoleReferenceFor("agentconfigs", agentConfig.Name), "uses", false)
	}
}

func consoleOwnerRelationship(object client.Object, target consoleResourceReference, ownerKind, relationship string, add func(consoleResourceReference, consoleResourceReference, string, bool)) bool {
	definition, ok := consoleResourceDefinitionForKind(ownerKind)
	if !ok {
		return false
	}
	for _, owner := range object.GetOwnerReferences() {
		if owner.Kind == ownerKind {
			add(consoleReference(definition, owner.Name), target, relationship, false)
			return true
		}
	}
	return false
}

func consoleResourceCondition(content map[string]any) string {
	conditions, found, _ := unstructured.NestedSlice(content, "status", "conditions")
	if !found {
		return ""
	}
	for _, value := range conditions {
		condition, ok := value.(map[string]any)
		if !ok || condition["status"] != string(metav1.ConditionTrue) {
			continue
		}
		conditionType, _ := condition["type"].(string)
		if conditionType != "" {
			return conditionType
		}
	}
	return ""
}

func (s *Server) getConsoleResource(writer http.ResponseWriter, request *http.Request, resource, namespace, name string) {
	definition, ok := consoleResourceDefinitionFor(resource)
	if !ok {
		writeError(writer, http.StatusNotFound, "not found")
		return
	}
	object := definition.New()
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, object); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting %s %q", definition.Kind, name), err)
		return
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(object)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("converting %s %q: %v", definition.Kind, name, err))
		return
	}
	content["apiVersion"] = kelos.GroupVersion.String()
	content["kind"] = definition.Kind
	unstructured.RemoveNestedField(content, "metadata", "managedFields")
	data, err := yaml.Marshal(content)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("marshaling %s %q: %v", definition.Kind, name, err))
		return
	}
	writeJSON(writer, http.StatusOK, consoleResourceDetail{
		Resource:  definition.Resource,
		Kind:      definition.Kind,
		Name:      name,
		Namespace: namespace,
		YAML:      string(data),
	})
}

func (s *Server) getTaskLogs(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	var task kelos.Task
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &task); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Task %q", name), err)
		return
	}
	if task.Status.PodName == "" {
		writeError(writer, http.StatusConflict, fmt.Sprintf("Task %q has no pod yet", name))
		return
	}

	logs, err := s.readTaskLogs(request.Context(), &task)
	if err != nil {
		if errors.Is(err, errTaskLogSegmentUnavailable) {
			writeError(writer, http.StatusNotFound, fmt.Sprintf("Task %q log segment is unavailable in the recent WorkerPool log window", name))
			return
		}
		writeKubernetesError(writer, fmt.Sprintf("getting logs for Task %q", name), err)
		return
	}

	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, logs)
}

func (s *Server) readTaskLogs(ctx context.Context, task *kelos.Task) (string, error) {
	if task.Spec.WorkerPoolRef == nil {
		stream, err := s.taskLogStream(ctx, task, taskLogTailLineLimit)
		if err != nil {
			return "", err
		}
		defer stream.Close()
		return tailLogLines(stream, taskLogTailLineLimit, taskLogTailByteLimit)
	}

	stream, err := s.taskLogStream(ctx, task, workerTaskLogLineLimit)
	if err != nil {
		return "", err
	}
	logs, found, err := tailWorkerTaskLogSegment(stream, task.Name, taskLogTailLineLimit, taskLogTailByteLimit, true)
	_ = stream.Close()
	if err != nil {
		return "", err
	}
	if !found {
		return "", errTaskLogSegmentUnavailable
	}
	return logs, nil
}

func (s *Server) openTaskLogStream(ctx context.Context, task *kelos.Task, tailLines int64) (io.ReadCloser, error) {
	return s.clientset.CoreV1().Pods(task.Namespace).GetLogs(task.Status.PodName, taskPodLogOptions(task, tailLines)).Stream(ctx)
}

func taskPodLogOptions(task *kelos.Task, tailLines int64) *corev1.PodLogOptions {
	options := &corev1.PodLogOptions{Container: kelos.AgentContainerName}
	if tailLines > 0 {
		options.TailLines = &tailLines
	}
	if task.Spec.WorkerPoolRef == nil {
		return options
	}

	// The worker can observe its Pod assignment just before StartTime is persisted.
	if task.Status.StartTime != nil {
		sinceTime := metav1.NewTime(task.Status.StartTime.Add(-workerTaskLogSinceMargin))
		options.SinceTime = &sinceTime
	}
	if !task.CreationTimestamp.IsZero() && (options.SinceTime == nil || task.CreationTimestamp.After(options.SinceTime.Time)) {
		createdAt := task.CreationTimestamp
		options.SinceTime = &createdAt
	}
	return options
}

type boundedLogTail struct {
	lines     []string
	start     int
	byteSize  int
	maxLines  int
	maxBytes  int
	truncated bool
}

func newBoundedLogTail(maxLines, maxBytes int) *boundedLogTail {
	maxBytes -= len(taskLogOmittedMessage)
	return &boundedLogTail{
		lines:    make([]string, 0, maxLines),
		maxLines: maxLines,
		maxBytes: maxBytes,
	}
}

func (tail *boundedLogTail) reset() {
	tail.lines = tail.lines[:0]
	tail.start = 0
	tail.byteSize = 0
	tail.truncated = false
}

func (tail *boundedLogTail) add(line string) {
	if len(line)+1 > tail.maxBytes {
		start := len(line) - (tail.maxBytes - 1)
		for start < len(line) && !utf8.RuneStart(line[start]) {
			start++
		}
		line = line[start:]
		tail.truncated = true
	}
	lineBytes := len(line) + 1
	for len(tail.lines)-tail.start > 0 && (len(tail.lines)-tail.start >= tail.maxLines || tail.byteSize+lineBytes > tail.maxBytes) {
		tail.byteSize -= len(tail.lines[tail.start]) + 1
		tail.lines[tail.start] = ""
		tail.start++
		tail.truncated = true
	}
	if tail.start >= tail.maxLines {
		copy(tail.lines, tail.lines[tail.start:])
		tail.lines = tail.lines[:len(tail.lines)-tail.start]
		tail.start = 0
	}
	tail.lines = append(tail.lines, line)
	tail.byteSize += lineBytes
}

func (tail *boundedLogTail) string() string {
	if len(tail.lines) == tail.start {
		return ""
	}
	var result strings.Builder
	result.Grow(tail.byteSize)
	if tail.truncated {
		result.WriteString(taskLogOmittedMessage)
	}
	for _, line := range tail.lines[tail.start:] {
		result.WriteString(line)
		result.WriteByte('\n')
	}
	return result.String()
}

func tailLogLines(reader io.Reader, maxLines, maxBytes int) (string, error) {
	tail := newBoundedLogTail(maxLines, maxBytes)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), taskLogScannerMaxTokenSize)
	for scanner.Scan() {
		tail.add(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return tail.string(), nil
}

func tailWorkerTaskLogSegment(reader io.Reader, taskName string, maxLines, maxBytes int, acceptPartial bool) (string, bool, error) {
	startMarker := "---KELOS_TASK_START--- " + taskName
	endMarker := "---KELOS_TASK_END--- " + taskName
	tail := newBoundedLogTail(maxLines, maxBytes)
	inSegment := false
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), taskLogScannerMaxTokenSize)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == startMarker:
			inSegment = true
			tail.reset()
		case line == endMarker && (inSegment || acceptPartial):
			return tail.string(), true, nil
		case inSegment || acceptPartial:
			tail.add(line)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	if inSegment {
		return tail.string(), true, nil
	}
	return "", false, nil
}

func consoleResourceDefinitionFor(resource string) (consoleResourceDefinition, bool) {
	for _, definition := range consoleResourceDefinitions {
		if definition.Resource == resource {
			return definition, true
		}
	}
	return consoleResourceDefinition{}, false
}

func consoleResourceDefinitionForKind(kind string) (consoleResourceDefinition, bool) {
	for _, definition := range consoleResourceDefinitions {
		if definition.Kind == kind {
			return definition, true
		}
	}
	return consoleResourceDefinition{}, false
}

func (s *Server) listSessions(writer http.ResponseWriter, request *http.Request) {
	var list kelos.SessionList
	if err := s.client.List(request.Context(), &list, client.InNamespace(s.requestNamespace(request))); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("listing Sessions: %v", err))
		return
	}
	sort.Slice(list.Items, func(i, j int) bool {
		activityI := sessionActivityTime(&list.Items[i])
		activityJ := sessionActivityTime(&list.Items[j])
		if !activityI.Equal(activityJ) {
			return activityI.After(activityJ)
		}
		return list.Items[i].CreationTimestamp.After(list.Items[j].CreationTimestamp.Time)
	})
	items := make([]sessionSummary, 0, len(list.Items))
	for i := range list.Items {
		items = append(items, summarize(&list.Items[i]))
	}
	writeJSON(writer, http.StatusOK, items)
}

func (s *Server) listSessionOptions(writer http.ResponseWriter, request *http.Request) {
	namespace := s.requestNamespace(request)
	var sessions kelos.SessionList
	if err := s.client.List(request.Context(), &sessions, client.InNamespace(namespace)); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("listing Sessions for form options: %v", err))
		return
	}
	var workspaces kelos.WorkspaceList
	if err := s.client.List(request.Context(), &workspaces, client.InNamespace(namespace)); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("listing Workspaces for form options: %v", err))
		return
	}
	var agentConfigs kelos.AgentConfigList
	if err := s.client.List(request.Context(), &agentConfigs, client.InNamespace(namespace)); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("listing AgentConfigs for form options: %v", err))
		return
	}

	options := sessionOptions{
		Credentials:  credentialOptions(sessions.Items),
		Workspaces:   objectNames(workspaces.Items, func(item kelos.Workspace) string { return item.Name }),
		AgentConfigs: objectNames(agentConfigs.Items, func(item kelos.AgentConfig) string { return item.Name }),
		Sessions:     objectNames(sessions.Items, func(item kelos.Session) string { return item.Name }),
	}
	writeJSON(writer, http.StatusOK, options)
}

func (s *Server) getSessionSource(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting source Session %q", name), err)
		return
	}
	manifest := sessionManifestFromSession(&session)
	data, err := yaml.Marshal(manifest)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("marshaling source Session %q: %v", name, err))
		return
	}
	writeJSON(writer, http.StatusOK, sessionSourceDetail{
		Name:      name,
		Namespace: namespace,
		Manifest:  manifest,
		YAML:      string(data),
	})
}

func sessionManifestFromSession(session *kelos.Session) sessionManifest {
	return sessionManifest{
		APIVersion: kelos.GroupVersion.String(),
		Kind:       "Session",
		Metadata: sessionManifestMetadata{
			Namespace: session.Namespace,
		},
		Spec: *session.Spec.DeepCopy(),
	}
}

func (s *Server) requestNamespace(request *http.Request) string {
	namespace := strings.TrimSpace(request.URL.Query().Get("namespace"))
	if namespace == "" {
		return s.defaultNamespace
	}
	return namespace
}

func credentialOptions(sessions []kelos.Session) []credentialOption {
	seen := make(map[credentialOption]struct{})
	for i := range sessions {
		credentials := sessions[i].Spec.Worker.Credentials
		if credentials == nil || credentials.SecretRef == nil || credentials.SecretRef.Name == "" {
			continue
		}
		seen[credentialOption{
			Name:     credentials.SecretRef.Name,
			Type:     credentials.Type,
			Provider: sessions[i].Spec.Worker.Type,
		}] = struct{}{}
	}
	options := make([]credentialOption, 0, len(seen))
	for option := range seen {
		options = append(options, option)
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Provider != options[j].Provider {
			return options[i].Provider < options[j].Provider
		}
		if options[i].Name != options[j].Name {
			return options[i].Name < options[j].Name
		}
		return options[i].Type < options[j].Type
	})
	return options
}

func objectNames[T any](items []T, name func(T) string) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, name(item))
	}
	sort.Strings(names)
	return names
}

func (s *Server) createSession(writer http.ResponseWriter, request *http.Request) {
	var payload createSessionRequest
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	payload.Name = strings.TrimSpace(payload.Name)
	payload.Namespace = strings.TrimSpace(payload.Namespace)
	if payload.Namespace == "" {
		payload.Namespace = s.defaultNamespace
	}
	if payload.Name == "" || payload.Namespace == "" {
		writeError(writer, http.StatusBadRequest, "name and namespace are required")
		return
	}
	section, err := normalizeSessionSection(payload.Section)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	session := &kelos.Session{
		TypeMeta: metav1.TypeMeta{APIVersion: kelos.GroupVersion.String(), Kind: "Session"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      payload.Name,
			Namespace: payload.Namespace,
		},
		Spec: payload.SessionSpec,
	}
	if section != "" {
		session.Annotations = map[string]string{sessionSectionAnnotation: section}
	}
	if err := s.client.Create(request.Context(), session); err != nil {
		status := http.StatusInternalServerError
		if apierrors.IsAlreadyExists(err) || apierrors.IsInvalid(err) {
			status = http.StatusConflict
		}
		writeError(writer, status, fmt.Sprintf("creating Session %q: %v", payload.Name, err))
		return
	}
	writeJSON(writer, http.StatusCreated, summarize(session))
}

func (s *Server) applySession(writer http.ResponseWriter, request *http.Request) {
	session, err := decodeSessionYAML(request.Body)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if session.APIVersion != kelos.GroupVersion.String() || session.Kind != "Session" {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("manifest must be a %s Session", kelos.GroupVersion.String()))
		return
	}
	session.Name = strings.TrimSpace(session.Name)
	session.Namespace = strings.TrimSpace(session.Namespace)
	namespace := s.requestNamespace(request)
	if session.Namespace == "" {
		session.Namespace = namespace
	}
	if session.Name == "" {
		writeError(writer, http.StatusBadRequest, "Session metadata.name is required")
		return
	}
	if session.Namespace != namespace {
		writeError(writer, http.StatusForbidden, fmt.Sprintf("namespace %q is not active", session.Namespace))
		return
	}

	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(session)
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Sprintf("converting Session manifest: %v", err))
		return
	}
	delete(object, "status")
	manifest := &unstructured.Unstructured{Object: object}
	if err := s.client.Apply(
		request.Context(),
		client.ApplyConfigurationFromUnstructured(manifest),
		client.FieldOwner(sessionApplyManager),
		client.ForceOwnership,
	); err != nil {
		status := http.StatusInternalServerError
		switch {
		case apierrors.IsInvalid(err):
			status = http.StatusBadRequest
		case apierrors.IsForbidden(err):
			status = http.StatusForbidden
		case apierrors.IsConflict(err):
			status = http.StatusConflict
		}
		writeError(writer, status, fmt.Sprintf("applying Session %q: %v", session.Name, err))
		return
	}

	var applied kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKeyFromObject(session), &applied); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting applied Session %q", session.Name), err)
		return
	}
	writeJSON(writer, http.StatusOK, summarize(&applied))
}

func (s *Server) deleteSession(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	session := &kelos.Session{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	if err := s.client.Delete(request.Context(), session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("deleting Session %q", name), err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) resetSession(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	session, _, err := sessionreset.Request(
		request.Context(),
		s.client,
		client.ObjectKey{Namespace: namespace, Name: name},
		string(uuid.NewUUID()),
	)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case apierrors.IsNotFound(err):
			status = http.StatusNotFound
		case apierrors.IsForbidden(err):
			status = http.StatusForbidden
		case apierrors.IsConflict(err):
			status = http.StatusConflict
		}
		writeError(writer, status, err.Error())
		return
	}
	writeJSON(writer, http.StatusAccepted, summarize(session))
}

func (s *Server) suspendSession(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Session %q to suspend", name), err)
		return
	}
	if session.Spec.Suspend != nil && *session.Spec.Suspend {
		writeJSON(writer, http.StatusAccepted, summarize(&session))
		return
	}

	original := session.DeepCopy()
	suspend := true
	session.Spec.Suspend = &suspend
	if err := s.client.Patch(
		request.Context(),
		&session,
		client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
	); err != nil {
		status := http.StatusInternalServerError
		switch {
		case apierrors.IsNotFound(err):
			status = http.StatusNotFound
		case apierrors.IsInvalid(err):
			status = http.StatusBadRequest
		case apierrors.IsForbidden(err):
			status = http.StatusForbidden
		case apierrors.IsConflict(err):
			status = http.StatusConflict
		}
		writeError(writer, status, fmt.Sprintf("suspending Session %q: %v", name, err))
		return
	}
	writeJSON(writer, http.StatusAccepted, summarize(&session))
}

func (s *Server) resumeSession(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	session, _, err := sessionsuspend.RequestResume(
		request.Context(),
		s.client,
		client.ObjectKey{Namespace: namespace, Name: name},
	)
	if err != nil {
		status := http.StatusInternalServerError
		switch {
		case apierrors.IsNotFound(err):
			status = http.StatusNotFound
		case apierrors.IsForbidden(err):
			status = http.StatusForbidden
		case apierrors.IsConflict(err):
			status = http.StatusConflict
		}
		writeError(writer, status, err.Error())
		return
	}
	if session.Spec.Suspend != nil && *session.Spec.Suspend {
		original := session.DeepCopy()
		suspend := false
		session.Spec.Suspend = &suspend
		if err := s.client.Patch(
			request.Context(),
			session,
			client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
		); err != nil {
			status := http.StatusInternalServerError
			switch {
			case apierrors.IsNotFound(err):
				status = http.StatusNotFound
			case apierrors.IsInvalid(err):
				status = http.StatusBadRequest
			case apierrors.IsForbidden(err):
				status = http.StatusForbidden
			case apierrors.IsConflict(err):
				status = http.StatusConflict
			}
			writeError(writer, status, fmt.Sprintf("resuming Session %q: %v", name, err))
			return
		}
		session, _, err = sessionsuspend.RequestResume(
			request.Context(),
			s.client,
			client.ObjectKey{Namespace: namespace, Name: name},
		)
		if err != nil {
			status := http.StatusInternalServerError
			switch {
			case apierrors.IsNotFound(err):
				status = http.StatusNotFound
			case apierrors.IsForbidden(err):
				status = http.StatusForbidden
			case apierrors.IsConflict(err):
				status = http.StatusConflict
			}
			writeError(writer, status, err.Error())
			return
		}
	}
	writeJSON(writer, http.StatusAccepted, summarize(session))
}

func (s *Server) updateSessionSection(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	var payload updateSessionSectionRequest
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	section, err := normalizeSessionSection(payload.Section)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}

	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Session %q to update its section", name), err)
		return
	}
	original := session.DeepCopy()
	if section == "" {
		delete(session.Annotations, sessionSectionAnnotation)
	} else {
		if session.Annotations == nil {
			session.Annotations = map[string]string{}
		}
		session.Annotations[sessionSectionAnnotation] = section
	}
	if err := s.client.Patch(
		request.Context(),
		&session,
		client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
	); err != nil {
		status := http.StatusInternalServerError
		switch {
		case apierrors.IsNotFound(err):
			status = http.StatusNotFound
		case apierrors.IsInvalid(err):
			status = http.StatusBadRequest
		case apierrors.IsForbidden(err):
			status = http.StatusForbidden
		case apierrors.IsConflict(err):
			status = http.StatusConflict
		}
		writeError(writer, status, fmt.Sprintf("updating section for Session %q: %v", name, err))
		return
	}
	writeJSON(writer, http.StatusOK, summarize(&session))
}

func normalizeSessionSection(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxSessionSectionLength {
		return "", fmt.Errorf("section must be at most %d characters", maxSessionSectionLength)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("section must not contain control characters")
		}
	}
	return value, nil
}

func (s *Server) updateSessionDisplayName(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	var payload updateSessionDisplayNameRequest
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	displayName, err := normalizeSessionDisplayName(payload.DisplayName)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}

	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Session %q to update its display name", name), err)
		return
	}
	original := session.DeepCopy()
	if displayName == "" {
		delete(session.Annotations, sessionDisplayNameAnnotation)
	} else {
		if session.Annotations == nil {
			session.Annotations = map[string]string{}
		}
		session.Annotations[sessionDisplayNameAnnotation] = displayName
	}
	if err := s.client.Patch(
		request.Context(),
		&session,
		client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}),
	); err != nil {
		status := http.StatusInternalServerError
		switch {
		case apierrors.IsNotFound(err):
			status = http.StatusNotFound
		case apierrors.IsInvalid(err):
			status = http.StatusBadRequest
		case apierrors.IsForbidden(err):
			status = http.StatusForbidden
		case apierrors.IsConflict(err):
			status = http.StatusConflict
		}
		writeError(writer, status, fmt.Sprintf("updating display name for Session %q: %v", name, err))
		return
	}
	writeJSON(writer, http.StatusOK, summarize(&session))
}

func normalizeSessionDisplayName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > maxSessionDisplayNameLength {
		return "", fmt.Errorf("display name must be at most %d characters", maxSessionDisplayNameLength)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", errors.New("display name must not contain control characters")
		}
	}
	return value, nil
}

func summarize(session *kelos.Session) sessionSummary {
	summary := sessionSummary{
		Name:           session.Name,
		DisplayName:    sessionDisplayName(session),
		Namespace:      session.Namespace,
		UID:            string(session.UID),
		Provider:       session.Spec.Worker.Type,
		Model:          session.Status.Model,
		Phase:          session.Status.Phase,
		LastActivityAt: session.Status.LastActivityTime,
		Message:        session.Status.Message,
		Branch:         session.Status.Branch,
		PullRequest:    session.Status.PullRequest,
		Section:        session.Annotations[sessionSectionAnnotation],
		Resetting:      session.Annotations[sessionreset.RequestAnnotation] != "",
		UserSuspended:  session.Spec.Suspend != nil && *session.Spec.Suspend,
		IdleSuspended:  sessionsuspend.IsIdlePolicySuspended(session),
	}
	if !session.CreationTimestamp.IsZero() {
		createdAt := session.CreationTimestamp
		summary.CreatedAt = &createdAt
	}
	if condition := sessionActiveCondition(session); condition != nil && condition.Status != metav1.ConditionUnknown {
		active := condition.Status == metav1.ConditionTrue
		summary.Active = &active
		summary.WaitingForInput = active && condition.Reason == "WaitingForInput"
	}
	return summary
}

func sessionDisplayName(session *kelos.Session) string {
	if displayName := strings.TrimSpace(session.Annotations[sessionDisplayNameAnnotation]); displayName != "" {
		return displayName
	}
	return session.Name
}

func sessionActiveCondition(session *kelos.Session) *metav1.Condition {
	return apiMeta.FindStatusCondition(session.Status.Conditions, kelos.SessionConditionActive)
}

func sessionActivityTime(session *kelos.Session) time.Time {
	activity := session.CreationTimestamp.Time
	if session.Status.LastActivityTime != nil {
		if session.Status.LastActivityTime.After(activity) {
			return session.Status.LastActivityTime.Time
		}
		return activity
	}
	condition := sessionActiveCondition(session)
	if condition != nil && condition.Status != metav1.ConditionUnknown && condition.LastTransitionTime.After(activity) {
		activity = condition.LastTransitionTime.Time
	}
	return activity
}

func (s *Server) uploadSessionAttachment(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	session, ok := s.readySession(writer, request, namespace, name)
	if !ok {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, attachmentRequestLimit)
	reader, err := request.MultipartReader()
	if err != nil {
		writeError(writer, http.StatusBadRequest, "attachment upload must use multipart form data")
		return
	}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			writeError(writer, http.StatusBadRequest, "attachment file is required")
			return
		}
		if err != nil {
			writeError(writer, http.StatusBadRequest, fmt.Sprintf("reading attachment upload: %v", err))
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		attachment, err := s.attachments.Upload(request.Context(), namespace, session.Status.PodName, part.FileName(), part)
		_ = part.Close()
		if err != nil {
			status := attachmentUploadStatus(err)
			writeError(writer, status, fmt.Sprintf("uploading attachment to Session %q: %v", name, err))
			return
		}
		writeJSON(writer, http.StatusCreated, attachment)
		return
	}
}

func (s *Server) downloadSessionAttachment(writer http.ResponseWriter, request *http.Request, namespace, name, id string) {
	session, ok := s.readySession(writer, request, namespace, name)
	if !ok {
		return
	}
	attachment, data, err := s.attachments.Download(request.Context(), namespace, session.Status.PodName, id)
	if err != nil {
		status := http.StatusBadGateway
		if strings.Contains(strings.ToLower(err.Error()), "attachment not found") {
			status = http.StatusNotFound
		}
		writeError(writer, status, fmt.Sprintf("downloading attachment from Session %q: %v", name, err))
		return
	}
	disposition := "attachment"
	if strings.HasPrefix(attachment.MediaType, "image/") {
		disposition = "inline"
	}
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Content-Type", attachment.MediaType)
	writer.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": attachment.Name}))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)
}

func attachmentUploadStatus(err error) int {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "too large") || strings.Contains(message, "exceeds the"):
		return http.StatusRequestEntityTooLarge
	case strings.Contains(message, "storage quota exceeded"):
		return http.StatusInsufficientStorage
	case strings.Contains(message, "storing session attachment failed"):
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

func (s *Server) readySession(writer http.ResponseWriter, request *http.Request, namespace, name string) (*kelos.Session, bool) {
	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Session %q", name), err)
		return nil, false
	}
	if session.Status.Phase != kelos.SessionPhaseReady || session.Status.PodName == "" {
		writeError(writer, http.StatusConflict, fmt.Sprintf("Session %q is not ready", name))
		return nil, false
	}
	return &session, true
}

func (s *Server) connectSession(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Session %q", name), err)
		return
	}
	if session.Status.Phase == kelos.SessionPhaseSuspended {
		writeError(writer, http.StatusConflict, fmt.Sprintf("Session %q is suspended", name))
		return
	}
	if session.Status.Phase != kelos.SessionPhaseReady || session.Status.PodName == "" {
		writeError(writer, http.StatusConflict, fmt.Sprintf("Session %q is not ready", name))
		return
	}
	connection, err := s.upgrader.Upgrade(writer, request, nil)
	if err != nil {
		return
	}
	socket := &sessionSocket{Conn: connection}
	defer socket.Close()
	var acknowledgeResume func() error
	if sessionsuspend.ResumeRequested(&session) {
		requestValue := session.Annotations[sessionsuspend.ResumeRequestAnnotation]
		acknowledgeResume = func() error {
			_, err := sessionsuspend.AcknowledgeResume(request.Context(), s.client, client.ObjectKeyFromObject(&session), requestValue)
			return err
		}
	}
	if err := s.bridge(request.Context(), socket, namespace, session.Status.PodName, acknowledgeResume); err != nil {
		_ = socket.WriteJSON(map[string]any{"type": "error", "text": err.Error(), "status": "failed"})
	}
}

func (s *Server) bridgeExec(ctx context.Context, connection *sessionSocket, namespace, podName string, acknowledgeResume func() error) error {
	request := s.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec")
	request.VersionedParams(&corev1.PodExecOptions{
		Container: kelos.AgentContainerName,
		Command:   []string{sessionRuntimeClient, "client"},
		Stdin:     true,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, clientgoscheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(s.restConfig, http.MethodPost, request.URL())
	if err != nil {
		return fmt.Errorf("creating Session exec connection: %w", err)
	}

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	defer stdinReader.Close()
	defer stdinWriter.Close()
	defer stdoutReader.Close()
	defer stdoutWriter.Close()
	defer stderrReader.Close()
	defer stderrWriter.Close()

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- executor.StreamWithContext(ctx, remotecommand.StreamOptions{
			Stdin:  stdinReader,
			Stdout: stdoutWriter,
			Stderr: stderrWriter,
			Tty:    false,
		})
		_ = stdoutWriter.Close()
		_ = stderrWriter.Close()
	}()

	outputDone := make(chan error, 2)
	resumeAcknowledged := acknowledgeResume == nil
	forward := func(reader io.Reader, stderr bool) {
		scanner := newJSONLineScanner(reader)
		for scanner.Scan() {
			payload := append([]byte(nil), scanner.Bytes()...)
			if stderr {
				encoded, _ := json.Marshal(map[string]any{"type": "error", "text": string(payload), "status": "runtime"})
				payload = encoded
			}
			err := connection.WriteMessage(websocket.TextMessage, payload)
			if err != nil {
				outputDone <- err
				return
			}
			if !stderr && !resumeAcknowledged {
				var event sessionruntime.Event
				if json.Unmarshal(payload, &event) == nil && event.Type == sessionruntime.EventHistoryEnd {
					if err := acknowledgeResume(); err != nil {
						outputDone <- err
						return
					}
					resumeAcknowledged = true
				}
			}
		}
		outputDone <- scanner.Err()
	}
	go forward(stdoutReader, false)
	go forward(stderrReader, true)

	readDone := make(chan error, 1)
	go func() {
		connection.SetReadLimit(1024 * 1024)
		for {
			messageType, payload, err := connection.ReadMessage()
			if err != nil {
				readDone <- err
				return
			}
			if messageType != websocket.TextMessage {
				continue
			}
			payload = append(payload, '\n')
			if _, err := stdinWriter.Write(payload); err != nil {
				readDone <- err
				return
			}
		}
	}()

	select {
	case err := <-streamDone:
		return err
	case err := <-outputDone:
		return err
	case err := <-readDone:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newJSONLineScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 8*1024*1024)
	return scanner
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return origin == "http://"+request.Host || origin == "https://"+request.Host
}

func decodeJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, requestBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	return nil
}

func decodeSessionYAML(reader io.Reader) (*kelos.Session, error) {
	data, err := io.ReadAll(io.LimitReader(reader, requestBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("reading Session manifest: %w", err)
	}
	if len(data) > requestBodyLimit {
		return nil, fmt.Errorf("Session manifest exceeds %d bytes", requestBodyLimit)
	}

	documents := yamlutil.NewYAMLReader(bufio.NewReader(bytes.NewReader(data)))
	var session *kelos.Session
	for {
		document, err := documents.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading Session manifest: %w", err)
		}
		if len(bytes.TrimSpace(document)) == 0 {
			continue
		}
		if session != nil {
			return nil, errors.New("Session manifest must contain exactly one YAML document")
		}
		jsonData, err := yaml.YAMLToJSONStrict(document)
		if err != nil {
			return nil, fmt.Errorf("invalid Session YAML: %w", err)
		}
		decoded := &sessionManifest{}
		decoder := json.NewDecoder(bytes.NewReader(jsonData))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(decoded); err != nil {
			return nil, fmt.Errorf("invalid Session manifest: %w", err)
		}
		session = &kelos.Session{
			TypeMeta: metav1.TypeMeta{APIVersion: decoded.APIVersion, Kind: decoded.Kind},
			ObjectMeta: metav1.ObjectMeta{
				Name:        decoded.Metadata.Name,
				Namespace:   decoded.Metadata.Namespace,
				Labels:      decoded.Metadata.Labels,
				Annotations: decoded.Metadata.Annotations,
			},
			Spec: decoded.Spec,
		}
	}
	if session == nil {
		return nil, errors.New("Session manifest is empty")
	}
	return session, nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeKubernetesError(writer http.ResponseWriter, operation string, err error) {
	status := http.StatusInternalServerError
	if apierrors.IsNotFound(err) {
		status = http.StatusNotFound
	}
	writeError(writer, status, fmt.Sprintf("%s: %v", operation, err))
}

func (s *Server) index(writer http.ResponseWriter, request *http.Request) {
	serveEmbedded(writer, "web/index.html", "text/html; charset=utf-8")
}

func (s *Server) loginPage(writer http.ResponseWriter, request *http.Request) {
	if s.authenticated(request) {
		http.Redirect(writer, request, "/", http.StatusFound)
		return
	}
	serveEmbedded(writer, "web/login.html", "text/html; charset=utf-8")
}

func (s *Server) asset(writer http.ResponseWriter, request *http.Request) {
	name := strings.TrimPrefix(request.URL.Path, "/assets/")
	contentType := "application/octet-stream"
	if strings.HasSuffix(name, ".css") {
		contentType = "text/css; charset=utf-8"
	} else if strings.HasSuffix(name, ".js") {
		contentType = "text/javascript; charset=utf-8"
	}
	serveEmbedded(writer, "web/"+name, contentType)
}

func (s *Server) publicAsset(writer http.ResponseWriter, request *http.Request) {
	name := strings.TrimPrefix(request.URL.Path, "/public/")
	if name != "login.css" && name != "login.js" {
		writeError(writer, http.StatusNotFound, "not found")
		return
	}
	contentType := "text/css; charset=utf-8"
	if strings.HasSuffix(name, ".js") {
		contentType = "text/javascript; charset=utf-8"
	}
	serveEmbedded(writer, "web/"+name, contentType)
}

func serveEmbedded(writer http.ResponseWriter, name, contentType string) {
	data, err := fs.ReadFile(webFiles, name)
	if err != nil {
		writeError(writer, http.StatusNotFound, "not found")
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(data)
}
