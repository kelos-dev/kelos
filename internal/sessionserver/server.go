package sessionserver

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
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	authCookieName       = "kelos_session_auth"
	sessionRuntimeClient = "/kelos/bin/kelos-session-runtime"
	sessionApplyManager  = "kelos-session-server"
	requestBodyLimit     = 1024 * 1024
)

//go:embed web/*
var webFiles embed.FS

// Config contains dependencies and authentication configuration for the web server.
type Config struct {
	Token            string
	Client           client.Client
	Clientset        *kubernetes.Clientset
	RESTConfig       *rest.Config
	DefaultNamespace string
	SecureCookie     bool
}

// Server serves the Session web application and Kubernetes-backed API.
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
	bridge           func(context.Context, *sessionSocket, string, string) error
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
	Name        string                    `json:"name"`
	Namespace   string                    `json:"namespace"`
	UID         string                    `json:"uid,omitempty"`
	Provider    string                    `json:"provider"`
	Phase       kelos.SessionPhase        `json:"phase,omitempty"`
	Message     string                    `json:"message,omitempty"`
	Branch      string                    `json:"branch,omitempty"`
	PullRequest *kelos.SessionPullRequest `json:"pullRequest,omitempty"`
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
	Name                string                            `json:"name"`
	Namespace           string                            `json:"namespace"`
	Worker              kelos.WorkerSpec                  `json:"worker"`
	VolumeClaimTemplate *corev1.PersistentVolumeClaimSpec `json:"volumeClaimTemplate,omitempty"`
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
	digest := hmac.New(sha256.New, []byte(config.Token))
	_, _ = digest.Write([]byte("kelos-session-web-cookie-v1"))
	server := &Server{
		token:            []byte(config.Token),
		cookieValue:      base64.RawURLEncoding.EncodeToString(digest.Sum(nil)),
		client:           config.Client,
		clientset:        config.Clientset,
		restConfig:       config.RESTConfig,
		defaultNamespace: defaultNamespace,
		secureCookie:     config.SecureCookie,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  16 * 1024,
			WriteBufferSize: 16 * 1024,
			CheckOrigin: func(request *http.Request) bool {
				return request.Header.Get("Origin") == "" || sameOrigin(request)
			},
		},
	}
	server.bridge = server.bridgeExec
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
	if len(parts) < 3 || parts[0] != "sessions" {
		writeError(writer, http.StatusNotFound, "not found")
		return
	}
	namespace, name := parts[1], parts[2]
	if len(parts) == 4 && parts[3] == "connect" && request.Method == http.MethodGet {
		s.connectSession(writer, request, namespace, name)
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

func (s *Server) listSessions(writer http.ResponseWriter, request *http.Request) {
	var list kelos.SessionList
	if err := s.client.List(request.Context(), &list, client.InNamespace(s.requestNamespace(request))); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Sprintf("listing Sessions: %v", err))
		return
	}
	sort.Slice(list.Items, func(i, j int) bool {
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
	session := &kelos.Session{
		TypeMeta: metav1.TypeMeta{APIVersion: kelos.GroupVersion.String(), Kind: "Session"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      payload.Name,
			Namespace: payload.Namespace,
		},
		Spec: kelos.SessionSpec{
			Worker:              payload.Worker,
			VolumeClaimTemplate: payload.VolumeClaimTemplate,
		},
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

func summarize(session *kelos.Session) sessionSummary {
	return sessionSummary{
		Name:        session.Name,
		Namespace:   session.Namespace,
		UID:         string(session.UID),
		Provider:    session.Spec.Worker.Type,
		Phase:       session.Status.Phase,
		Message:     session.Status.Message,
		Branch:      session.Status.Branch,
		PullRequest: session.Status.PullRequest,
	}
}

func (s *Server) connectSession(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	var session kelos.Session
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &session); err != nil {
		writeKubernetesError(writer, fmt.Sprintf("getting Session %q", name), err)
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
	if err := s.bridge(request.Context(), socket, namespace, session.Status.PodName); err != nil {
		_ = socket.WriteJSON(map[string]any{"type": "error", "text": err.Error(), "status": "failed"})
	}
}

func (s *Server) bridgeExec(ctx context.Context, connection *sessionSocket, namespace, podName string) error {
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
