package reporting

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

type fakeSecretReader struct {
	headers map[string]map[string]string // namespace/name -> headers
}

func (f *fakeSecretReader) ReadHeaders(_ context.Context, namespace, name string) (map[string]string, error) {
	return f.headers[namespace+"/"+name], nil
}

func newFakeClient(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	utilruntime.Must(kelosv1alpha1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(&kelosv1alpha1.Task{}).Build()
}

func TestWebhookReporter_ReportWebhooks(t *testing.T) {
	tests := []struct {
		name             string
		task             *kelosv1alpha1.Task
		serverStatus     int
		wantRequests     int
		wantPayload      *WebhookPayload
		wantAuthHeader   string
		wantHeaders      map[string]string
		wantAnnotation   string
		wantErr          bool
	}{
		{
			name: "sends webhook on task succeeded",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-1",
					Namespace: "default",
					Labels:    map[string]string{"kelos.dev/taskspawner": "my-spawner"},
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"slack-alert","webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:  "claude-code",
					Model: "claude-sonnet-4-20250514",
				},
				Status: kelosv1alpha1.TaskStatus{
					Phase:   kelosv1alpha1.TaskPhaseSucceeded,
					Message: "Task completed successfully",
					Outputs: []string{"https://github.com/org/repo/pull/1"},
					Results: map[string]string{"cost-usd": "0.42"},
				},
			},
			serverStatus: 200,
			wantRequests: 1,
			wantPayload: &WebhookPayload{
				Task:      "test-task-1",
				Namespace: "default",
				Spawner:   "my-spawner",
				Phase:     "Succeeded",
				Message:   "Task completed successfully",
				AgentType: "claude-code",
				Model:     "claude-sonnet-4-20250514",
				Outputs:   []string{"https://github.com/org/repo/pull/1"},
				Results:   map[string]string{"cost-usd": "0.42"},
			},
			wantAnnotation: "Succeeded",
		},
		{
			name: "sends webhook on task failed",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-2",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Spec: kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{
					Phase:   kelosv1alpha1.TaskPhaseFailed,
					Message: "OOM killed",
				},
			},
			serverStatus:   200,
			wantRequests:   1,
			wantAnnotation: "Failed",
		},
		{
			name: "skips non-terminal phase",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-3",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseRunning},
			},
			serverStatus: 200,
			wantRequests: 0,
		},
		{
			name: "skips already reported phase",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-4",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion:       `[{"name":"alert","webhook":{"url":"PLACEHOLDER"}}]`,
						AnnotationWebhookReportPhase: "Succeeded",
					},
				},
				Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus: 200,
			wantRequests: 0,
		},
		{
			name: "filters by phase - only Failed configured but task Succeeded",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-5",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","phases":["Failed"],"webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus:   200,
			wantRequests:   0,
			wantAnnotation: "Succeeded",
		},
		{
			name: "persists annotation on delivery failure to prevent duplicates",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-fail-delivery",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER"}}]`,
					},
				},
				Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus:   500,
			wantRequests:   1,
			wantErr:        true,
			wantAnnotation: "Succeeded",
		},
		{
			name: "includes auth header from secret",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-6",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER","secretRef":{"name":"webhook-secret"}}}]`,
					},
				},
				Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus:   200,
			wantRequests:   1,
			wantAuthHeader: "Bearer my-token",
			wantAnnotation: "Succeeded",
		},
		{
			name: "sets multiple headers from secret",
			task: &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-task-7",
					Namespace: "default",
					Annotations: map[string]string{
						AnnotationOnCompletion: `[{"name":"alert","webhook":{"url":"PLACEHOLDER","secretRef":{"name":"multi-header-secret"}}}]`,
					},
				},
				Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
				Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
			},
			serverStatus: 200,
			wantRequests: 1,
			wantHeaders: map[string]string{
				"Authorization": "Bearer multi-token",
				"X-Api-Key":     "key-123",
			},
			wantAnnotation: "Succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestCount := 0
			var lastBody []byte
			var lastAuthHeader string
			var lastHeaders http.Header

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				lastAuthHeader = r.Header.Get("Authorization")
				lastHeaders = r.Header.Clone()
				lastBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(tt.serverStatus)
			}))
			defer server.Close()

			// Replace PLACEHOLDER URL in annotations with actual test server URL.
			if ann := tt.task.Annotations[AnnotationOnCompletion]; ann != "" {
				var hooks []json.RawMessage
				json.Unmarshal([]byte(ann), &hooks)
				for i := range hooks {
					var h map[string]interface{}
					json.Unmarshal(hooks[i], &h)
					if wh, ok := h["webhook"].(map[string]interface{}); ok {
						wh["url"] = server.URL
					}
					hooks[i], _ = json.Marshal(h)
				}
				updated, _ := json.Marshal(hooks)
				tt.task.Annotations[AnnotationOnCompletion] = string(updated)
			}

			cl := newFakeClient(tt.task)

			secretReader := &fakeSecretReader{
				headers: map[string]map[string]string{
					"default/webhook-secret": {
						"Authorization": "Bearer my-token",
					},
					"default/multi-header-secret": {
						"Authorization": "Bearer multi-token",
						"X-Api-Key":     "key-123",
					},
				},
			}

			wr := &WebhookReporter{
				Client:            cl,
				HTTPClient:        server.Client(),
				SecretReader:      secretReader,
				skipURLValidation: true,
			}

			err := wr.ReportWebhooks(context.Background(), tt.task)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if requestCount != tt.wantRequests {
				t.Errorf("expected %d requests, got %d", tt.wantRequests, requestCount)
			}

			if tt.wantPayload != nil && lastBody != nil {
				var got WebhookPayload
				if err := json.Unmarshal(lastBody, &got); err != nil {
					t.Fatalf("unmarshal payload: %v", err)
				}
				if got.Task != tt.wantPayload.Task {
					t.Errorf("payload.Task = %q, want %q", got.Task, tt.wantPayload.Task)
				}
				if got.Phase != tt.wantPayload.Phase {
					t.Errorf("payload.Phase = %q, want %q", got.Phase, tt.wantPayload.Phase)
				}
				if got.Spawner != tt.wantPayload.Spawner {
					t.Errorf("payload.Spawner = %q, want %q", got.Spawner, tt.wantPayload.Spawner)
				}
				if got.AgentType != tt.wantPayload.AgentType {
					t.Errorf("payload.AgentType = %q, want %q", got.AgentType, tt.wantPayload.AgentType)
				}
			}

			if tt.wantAuthHeader != "" && lastAuthHeader != tt.wantAuthHeader {
				t.Errorf("Authorization header = %q, want %q", lastAuthHeader, tt.wantAuthHeader)
			}

			for k, v := range tt.wantHeaders {
				if got := lastHeaders.Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}

			// Verify annotation persistence for cases that dispatched webhooks.
			if tt.wantAnnotation != "" {
				var updated kelosv1alpha1.Task
				if err := cl.Get(context.Background(), client.ObjectKeyFromObject(tt.task), &updated); err != nil {
					t.Fatalf("fetching task: %v", err)
				}
				got := updated.Annotations[AnnotationWebhookReportPhase]
				if got != tt.wantAnnotation {
					t.Errorf("annotation %s = %q, want %q", AnnotationWebhookReportPhase, got, tt.wantAnnotation)
				}
			}

		})
	}
}

func TestReportWebhooks_Idempotency(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(200)
	}))
	defer server.Close()

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "idem-task",
			Namespace: "default",
			Annotations: map[string]string{
				AnnotationOnCompletion: `[{"name":"hook","webhook":{"url":"` + server.URL + `"}}]`,
			},
		},
		Spec:   kelosv1alpha1.TaskSpec{Type: "claude-code"},
		Status: kelosv1alpha1.TaskStatus{Phase: kelosv1alpha1.TaskPhaseSucceeded},
	}

	cl := newFakeClient(task)
	wr := &WebhookReporter{Client: cl, HTTPClient: server.Client(), skipURLValidation: true}

	// First call should dispatch and persist.
	if err := wr.ReportWebhooks(context.Background(), task); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if requestCount != 1 {
		t.Fatalf("expected 1 request on first call, got %d", requestCount)
	}

	// Re-read task with updated annotations.
	var updated kelosv1alpha1.Task
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(task), &updated); err != nil {
		t.Fatalf("fetching task: %v", err)
	}

	// Second call should be a no-op (annotation already set) — same server,
	// so any duplicate delivery would increment requestCount.
	if err := wr.ReportWebhooks(context.Background(), &updated); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if requestCount != 1 {
		t.Errorf("expected no additional requests on second call, got %d total", requestCount)
	}
}

func TestBuildWebhookPayload(t *testing.T) {
	startTime := metav1.NewTime(time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC))
	completionTime := metav1.NewTime(time.Date(2026, 3, 20, 10, 5, 30, 0, time.UTC))

	task := &kelosv1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-task",
			Namespace: "prod",
			Labels:    map[string]string{"kelos.dev/taskspawner": "my-spawner"},
		},
		Spec: kelosv1alpha1.TaskSpec{
			Type:  "claude-code",
			Model: "claude-sonnet-4-20250514",
		},
		Status: kelosv1alpha1.TaskStatus{
			Phase:          kelosv1alpha1.TaskPhaseSucceeded,
			Message:        "Task completed successfully",
			StartTime:      &startTime,
			CompletionTime: &completionTime,
			Outputs:        []string{"https://github.com/org/repo/pull/123"},
			Results:        map[string]string{"cost-usd": "0.42", "input-tokens": "15000"},
		},
	}

	payload := buildWebhookPayload(task)

	if payload.Task != "my-task" {
		t.Errorf("Task = %q, want %q", payload.Task, "my-task")
	}
	if payload.Namespace != "prod" {
		t.Errorf("Namespace = %q, want %q", payload.Namespace, "prod")
	}
	if payload.Spawner != "my-spawner" {
		t.Errorf("Spawner = %q, want %q", payload.Spawner, "my-spawner")
	}
	if payload.Phase != "Succeeded" {
		t.Errorf("Phase = %q, want %q", payload.Phase, "Succeeded")
	}
	if payload.StartTime == nil || !payload.StartTime.Equal(startTime.Time) {
		t.Errorf("StartTime = %v, want %v", payload.StartTime, startTime.Time)
	}
	if payload.CompletionTime == nil || !payload.CompletionTime.Equal(completionTime.Time) {
		t.Errorf("CompletionTime = %v, want %v", payload.CompletionTime, completionTime.Time)
	}
	if len(payload.Outputs) != 1 || payload.Outputs[0] != "https://github.com/org/repo/pull/123" {
		t.Errorf("Outputs = %v, want [https://github.com/org/repo/pull/123]", payload.Outputs)
	}
	if payload.Results["cost-usd"] != "0.42" {
		t.Errorf("Results[cost-usd] = %q, want %q", payload.Results["cost-usd"], "0.42")
	}
}

func TestHttpClient_AppliesSSRFTransport(t *testing.T) {
	injected := &http.Client{Timeout: 5 * time.Second}
	wr := &WebhookReporter{HTTPClient: injected}
	cl := wr.httpClient()
	if cl.CheckRedirect == nil {
		t.Fatal("expected CheckRedirect to be set on cloned client")
	}
	if cl.Transport == nil {
		t.Fatal("expected Transport to be set on cloned client")
	}
	if cl == injected {
		t.Fatal("expected a clone, not the original client")
	}
}

func TestValidateWebhookURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		{"https://hooks.slack.com/services/T00/B00/xxx", false},
		{"https://example.com/webhook", false},
		{"http://example.com/webhook", true},
		{"https://user:pass@example.com/webhook", true},
		{"https://token@example.com/webhook", true},
	}
	for _, tt := range tests {
		err := validateWebhookURL(tt.url)
		if (err != nil) != tt.wantErr {
			t.Errorf("validateWebhookURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
		}
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"0.0.0.0", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"fe80::1", true},
		{"fd00::1", true},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
	}
	for _, tt := range tests {
		ip := net.ParseIP(tt.ip)
		if got := isPrivateIP(ip); got != tt.want {
			t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

func TestPhaseMatches(t *testing.T) {
	tests := []struct {
		configured []kelosv1alpha1.TerminalTaskPhase
		actual     kelosv1alpha1.TaskPhase
		want       bool
	}{
		{nil, kelosv1alpha1.TaskPhaseSucceeded, true},
		{nil, kelosv1alpha1.TaskPhaseFailed, true},
		{[]kelosv1alpha1.TerminalTaskPhase{"Failed"}, kelosv1alpha1.TaskPhaseFailed, true},
		{[]kelosv1alpha1.TerminalTaskPhase{"Failed"}, kelosv1alpha1.TaskPhaseSucceeded, false},
		{[]kelosv1alpha1.TerminalTaskPhase{"Succeeded", "Failed"}, kelosv1alpha1.TaskPhaseSucceeded, true},
	}

	for _, tt := range tests {
		got := phaseMatches(tt.configured, tt.actual)
		if got != tt.want {
			t.Errorf("phaseMatches(%v, %v) = %v, want %v", tt.configured, tt.actual, got, tt.want)
		}
	}
}
