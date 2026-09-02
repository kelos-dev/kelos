package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

type gitlabCall struct {
	method string
	path   string
	body   map[string]string
	token  string
}

func newGitLabReporterServer(t *testing.T, notes []gitlabNote) (*httptest.Server, *[]gitlabCall) {
	t.Helper()
	var calls []gitlabCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := gitlabCall{method: r.Method, path: r.URL.EscapedPath(), token: r.Header.Get("PRIVATE-TOKEN")}
		json.NewDecoder(r.Body).Decode(&call.body)
		calls = append(calls, call)

		switch {
		case r.URL.Path == "/api/v4/user":
			json.NewEncoder(w).Encode(gitlabUser{Username: "kelos-bot"})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(gitlabNote{ID: 555})
		case r.Method == http.MethodPut:
			json.NewEncoder(w).Encode(gitlabNote{ID: 555})
		default:
			json.NewEncoder(w).Encode(notes)
		}
	}))
	return server, &calls
}

func TestGitLabReporterCreateComment(t *testing.T) {
	server, calls := newGitLabReporterServer(t, nil)
	defer server.Close()

	r := &GitLabReporter{BaseURL: server.URL, Project: "group/sub/repo", Token: "glpat"}
	id, err := r.CreateComment(context.Background(), CommentTarget{Kind: "issue", Number: 42}, "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 555 {
		t.Errorf("expected note id 555, got %d", id)
	}
	call := (*calls)[0]
	if call.method != http.MethodPost || call.path != "/api/v4/projects/group%2Fsub%2Frepo/issues/42/notes" {
		t.Errorf("unexpected request %s %s", call.method, call.path)
	}
	if call.body["body"] != "hello" || call.token != "glpat" {
		t.Errorf("unexpected body/token: %+v", call)
	}
}

func TestGitLabReporterMergeRequestEndpoint(t *testing.T) {
	server, calls := newGitLabReporterServer(t, nil)
	defer server.Close()

	r := &GitLabReporter{BaseURL: server.URL, Project: "group/repo", Token: "glpat"}
	if err := r.UpdateComment(context.Background(), CommentTarget{Kind: SourceKindMergeRequest, Number: 7}, 555, "updated"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	call := (*calls)[0]
	if call.method != http.MethodPut || call.path != "/api/v4/projects/group%2Frepo/merge_requests/7/notes/555" {
		t.Errorf("unexpected request %s %s", call.method, call.path)
	}
	if call.body["body"] != "updated" {
		t.Errorf("unexpected body %+v", call.body)
	}
}

func TestGitLabReporterFindCommentByMarker(t *testing.T) {
	marker := "<!-- marker -->"
	server, calls := newGitLabReporterServer(t, []gitlabNote{
		{ID: 1, Body: "unrelated", Author: gitlabUser{Username: "kelos-bot"}},
		{ID: 2, Body: "status " + marker, Author: gitlabUser{Username: "someone-else"}},
		{ID: 3, Body: "status " + marker, Author: gitlabUser{Username: "kelos-bot"}},
		{ID: 4, Body: "status " + marker, Author: gitlabUser{Username: "Kelos-Bot"}},
	})
	defer server.Close()

	r := &GitLabReporter{BaseURL: server.URL, Project: "group/repo", Token: "glpat"}
	id, err := r.FindCommentByMarker(context.Background(), CommentTarget{Kind: "issue", Number: 9}, marker)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 4 {
		t.Errorf("expected newest own note 4, got %d", id)
	}
	if (*calls)[0].path != "/api/v4/user" {
		t.Errorf("expected authenticated user lookup first, got %s", (*calls)[0].path)
	}
	if !strings.HasSuffix((*calls)[1].path, "/issues/9/notes") {
		t.Errorf("expected notes listing, got %s", (*calls)[1].path)
	}
}

func TestGitLabReporterAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"403 Forbidden"}`))
	}))
	defer server.Close()

	r := &GitLabReporter{BaseURL: server.URL, Project: "group/repo"}
	if _, err := r.CreateComment(context.Background(), CommentTarget{Kind: "issue", Number: 1}, "x"); err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Errorf("expected 403 error, got %v", err)
	}
}

func TestTaskReporterUsesGitLabReporter(t *testing.T) {
	server, calls := newGitLabReporterServer(t, nil)
	defer server.Close()

	task := newTaskWithAnnotations("mr-task", "default", kelos.TaskPhasePending, map[string]string{
		AnnotationGitHubReporting: "enabled",
		AnnotationSourceNumber:    "7",
		AnnotationSourceKind:      SourceKindMergeRequest,
	})
	tr := &TaskReporter{
		Client:   fake.NewClientBuilder().WithScheme(newTestScheme()).WithObjects(task).Build(),
		Reporter: &GitLabReporter{BaseURL: server.URL, Project: "group/repo", Token: "glpat"},
	}
	if err := tr.ReportTaskStatus(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*calls) != 1 || (*calls)[0].path != "/api/v4/projects/group%2Frepo/merge_requests/7/notes" {
		t.Errorf("expected one note posted on the merge request, got %+v", *calls)
	}
}
