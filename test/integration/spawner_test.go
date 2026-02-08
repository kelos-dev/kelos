package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
	"github.com/axon-core/axon/internal/spawner"
)

type fakeIssue struct {
	Number      int         `json:"number"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	HTMLURL     string      `json:"html_url"`
	Labels      []fakeLabel `json:"labels"`
	PullRequest *struct{}   `json:"pull_request,omitempty"`
}

type fakeLabel struct {
	Name string `json:"name"`
}

type fakeComment struct {
	Body string `json:"body"`
}

// newFakeGitHub creates an httptest.Server that mimics the GitHub API endpoints
// used by the spawner (issues list and issue comments).
func newFakeGitHub(issues []fakeIssue, commentsByIssue map[int][]fakeComment) *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/test-owner/test-repo/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(issues); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	mux.HandleFunc("/repos/test-owner/test-repo/issues/", func(w http.ResponseWriter, r *http.Request) {
		var issueNum int
		n, err := fmt.Sscanf(r.URL.Path, "/repos/test-owner/test-repo/issues/%d/comments", &issueNum)
		if err != nil || n != 1 {
			http.NotFound(w, r)
			return
		}
		comments := commentsByIssue[issueNum]
		if comments == nil {
			comments = []fakeComment{}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(comments); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	return httptest.NewServer(mux)
}

// runCycleWithRetry runs spawner.RunCycle, retrying on conflict errors that
// occur due to the background controller also reconciling the TaskSpawner.
func runCycleWithRetry(key types.NamespacedName, opts spawner.Options) {
	Eventually(func() error {
		return spawner.RunCycle(ctx, k8sClient, key, opts)
	}, time.Second*10, time.Millisecond*250).Should(Succeed())
}

func createSpawnerNamespace(name string) string {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, ns)).Should(Succeed())

	ws := &axonv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "spawner-test-workspace",
			Namespace: name,
		},
		Spec: axonv1alpha1.WorkspaceSpec{
			Repo: "https://github.com/test-owner/test-repo.git",
			Ref:  "main",
		},
	}
	ExpectWithOffset(1, k8sClient.Create(ctx, ws)).Should(Succeed())

	return name
}

func newSpawnerTaskSpawner(name, namespace string) *axonv1alpha1.TaskSpawner {
	return &axonv1alpha1.TaskSpawner{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: axonv1alpha1.TaskSpawnerSpec{
			When: axonv1alpha1.When{
				GitHubIssues: &axonv1alpha1.GitHubIssues{
					WorkspaceRef: &axonv1alpha1.WorkspaceReference{
						Name: "spawner-test-workspace",
					},
				},
			},
			TaskTemplate: axonv1alpha1.TaskTemplate{
				Type: "claude-code",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "creds"},
				},
			},
		},
	}
}

var _ = Describe("Spawner with fake GitHub", func() {
	const (
		timeout  = time.Second * 10
		interval = time.Millisecond * 250
	)

	It("Should discover issues and create Tasks", func() {
		ns := createSpawnerNamespace("test-spawner-discover")

		By("Setting up a fake GitHub server")
		issues := []fakeIssue{
			{Number: 10, Title: "Bug report", Body: "Something is broken", HTMLURL: "https://github.com/test/10"},
			{Number: 20, Title: "Feature request", Body: "Add new feature", HTMLURL: "https://github.com/test/20"},
		}
		gh := newFakeGitHub(issues, nil)
		defer gh.Close()

		By("Creating a TaskSpawner")
		ts := newSpawnerTaskSpawner("spawner-discover", ns)
		Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

		By("Running a spawner cycle against the fake GitHub")
		key := types.NamespacedName{Name: ts.Name, Namespace: ns}
		opts := spawner.Options{
			GitHubOwner:   "test-owner",
			GitHubRepo:    "test-repo",
			GitHubBaseURL: gh.URL,
		}
		runCycleWithRetry(key, opts)

		By("Verifying Tasks were created")
		var taskList axonv1alpha1.TaskList
		Expect(k8sClient.List(ctx, &taskList,
			client.InNamespace(ns),
			client.MatchingLabels{"axon.io/taskspawner": ts.Name},
		)).Should(Succeed())
		Expect(taskList.Items).To(HaveLen(2))

		By("Verifying Task names match issue numbers")
		taskNames := map[string]bool{}
		for _, t := range taskList.Items {
			taskNames[t.Name] = true
		}
		Expect(taskNames).To(HaveKey("spawner-discover-10"))
		Expect(taskNames).To(HaveKey("spawner-discover-20"))

		By("Verifying TaskSpawner status reflects the spawner cycle")
		Eventually(func() int {
			var ts2 axonv1alpha1.TaskSpawner
			if err := k8sClient.Get(ctx, key, &ts2); err != nil {
				return -1
			}
			return ts2.Status.TotalTasksCreated
		}, timeout, interval).Should(Equal(2))
	})

	It("Should deduplicate existing Tasks", func() {
		ns := createSpawnerNamespace("test-spawner-dedup")

		By("Setting up a fake GitHub server")
		issues := []fakeIssue{
			{Number: 1, Title: "Existing issue", Body: "Already spawned"},
			{Number: 2, Title: "New issue", Body: "Not yet spawned"},
		}
		gh := newFakeGitHub(issues, nil)
		defer gh.Close()

		By("Creating a TaskSpawner")
		ts := newSpawnerTaskSpawner("spawner-dedup", ns)
		Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

		By("Pre-creating a Task for issue #1")
		existingTask := &axonv1alpha1.Task{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "spawner-dedup-1",
				Namespace: ns,
				Labels:    map[string]string{"axon.io/taskspawner": ts.Name},
			},
			Spec: axonv1alpha1.TaskSpec{
				Type:   "claude-code",
				Prompt: "existing",
				Credentials: axonv1alpha1.Credentials{
					Type:      axonv1alpha1.CredentialTypeOAuth,
					SecretRef: axonv1alpha1.SecretReference{Name: "creds"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, existingTask)).Should(Succeed())

		By("Running a spawner cycle")
		key := types.NamespacedName{Name: ts.Name, Namespace: ns}
		opts := spawner.Options{
			GitHubOwner:   "test-owner",
			GitHubRepo:    "test-repo",
			GitHubBaseURL: gh.URL,
		}
		runCycleWithRetry(key, opts)

		By("Verifying only 1 new Task was created (issue #2)")
		var taskList axonv1alpha1.TaskList
		Expect(k8sClient.List(ctx, &taskList,
			client.InNamespace(ns),
			client.MatchingLabels{"axon.io/taskspawner": ts.Name},
		)).Should(Succeed())
		Expect(taskList.Items).To(HaveLen(2))
	})

	It("Should use custom prompt templates", func() {
		ns := createSpawnerNamespace("test-spawner-template")

		By("Setting up a fake GitHub server")
		issues := []fakeIssue{
			{Number: 42, Title: "Custom template issue", Body: "Body text"},
		}
		gh := newFakeGitHub(issues, nil)
		defer gh.Close()

		By("Creating a TaskSpawner with a custom prompt template")
		ts := newSpawnerTaskSpawner("spawner-template", ns)
		ts.Spec.TaskTemplate.PromptTemplate = "Fix {{.Kind}} #{{.Number}}: {{.Title}}"
		Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

		By("Running a spawner cycle")
		key := types.NamespacedName{Name: ts.Name, Namespace: ns}
		opts := spawner.Options{
			GitHubOwner:   "test-owner",
			GitHubRepo:    "test-repo",
			GitHubBaseURL: gh.URL,
		}
		runCycleWithRetry(key, opts)

		By("Verifying the Task has the custom prompt")
		var task axonv1alpha1.Task
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "spawner-template-42", Namespace: ns}, &task)).Should(Succeed())
		Expect(task.Spec.Prompt).To(Equal("Fix Issue #42: Custom template issue"))
	})

	It("Should include comments in prompts", func() {
		ns := createSpawnerNamespace("test-spawner-comments")

		By("Setting up a fake GitHub server with comments")
		issues := []fakeIssue{
			{Number: 5, Title: "Issue with comments", Body: "Main body"},
		}
		comments := map[int][]fakeComment{
			5: {
				{Body: "First comment"},
				{Body: "Second comment"},
			},
		}
		gh := newFakeGitHub(issues, comments)
		defer gh.Close()

		By("Creating a TaskSpawner")
		ts := newSpawnerTaskSpawner("spawner-comments", ns)
		Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

		By("Running a spawner cycle")
		key := types.NamespacedName{Name: ts.Name, Namespace: ns}
		opts := spawner.Options{
			GitHubOwner:   "test-owner",
			GitHubRepo:    "test-repo",
			GitHubBaseURL: gh.URL,
		}
		runCycleWithRetry(key, opts)

		By("Verifying the Task prompt includes comments")
		var task axonv1alpha1.Task
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "spawner-comments-5", Namespace: ns}, &task)).Should(Succeed())
		Expect(task.Spec.Prompt).To(ContainSubstring("First comment"))
		Expect(task.Spec.Prompt).To(ContainSubstring("Second comment"))
	})

	It("Should handle empty issue list", func() {
		ns := createSpawnerNamespace("test-spawner-empty")

		By("Setting up a fake GitHub server with no issues")
		gh := newFakeGitHub([]fakeIssue{}, nil)
		defer gh.Close()

		By("Creating a TaskSpawner")
		ts := newSpawnerTaskSpawner("spawner-empty", ns)
		Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

		By("Running a spawner cycle")
		key := types.NamespacedName{Name: ts.Name, Namespace: ns}
		opts := spawner.Options{
			GitHubOwner:   "test-owner",
			GitHubRepo:    "test-repo",
			GitHubBaseURL: gh.URL,
		}
		runCycleWithRetry(key, opts)

		By("Verifying no Tasks were created")
		var taskList axonv1alpha1.TaskList
		Expect(k8sClient.List(ctx, &taskList,
			client.InNamespace(ns),
			client.MatchingLabels{"axon.io/taskspawner": ts.Name},
		)).Should(Succeed())
		Expect(taskList.Items).To(BeEmpty())

		By("Verifying status was still updated")
		var updatedTS axonv1alpha1.TaskSpawner
		Expect(k8sClient.Get(ctx, key, &updatedTS)).Should(Succeed())
		Expect(updatedTS.Status.TotalDiscovered).To(Equal(0))
	})

	It("Should accumulate status across multiple cycles", func() {
		ns := createSpawnerNamespace("test-spawner-multicycle")

		By("Setting up a fake GitHub server")
		issues := []fakeIssue{
			{Number: 1, Title: "Issue 1", Body: "Body 1"},
		}
		gh := newFakeGitHub(issues, nil)
		defer gh.Close()

		By("Creating a TaskSpawner")
		ts := newSpawnerTaskSpawner("spawner-multicycle", ns)
		Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

		key := types.NamespacedName{Name: ts.Name, Namespace: ns}
		opts := spawner.Options{
			GitHubOwner:   "test-owner",
			GitHubRepo:    "test-repo",
			GitHubBaseURL: gh.URL,
		}

		By("Running cycle 1")
		runCycleWithRetry(key, opts)

		var updatedTS axonv1alpha1.TaskSpawner
		Expect(k8sClient.Get(ctx, key, &updatedTS)).Should(Succeed())
		Expect(updatedTS.Status.TotalTasksCreated).To(Equal(1))

		By("Adding a second issue to the fake server and running cycle 2")
		gh.Close()
		issues = append(issues, fakeIssue{Number: 2, Title: "Issue 2", Body: "Body 2"})
		gh2 := newFakeGitHub(issues, nil)
		defer gh2.Close()
		opts.GitHubBaseURL = gh2.URL

		runCycleWithRetry(key, opts)

		By("Verifying TotalTasksCreated accumulated")
		Expect(k8sClient.Get(ctx, key, &updatedTS)).Should(Succeed())
		Expect(updatedTS.Status.TotalTasksCreated).To(Equal(2))
		Expect(updatedTS.Status.TotalDiscovered).To(Equal(2))
	})

	It("Should return error on GitHub API failure", func() {
		ns := createSpawnerNamespace("test-spawner-apierror")

		By("Setting up a fake GitHub server that returns errors")
		errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"rate limit exceeded"}`, http.StatusForbidden)
		}))
		defer errorServer.Close()

		By("Creating a TaskSpawner")
		ts := newSpawnerTaskSpawner("spawner-apierror", ns)
		Expect(k8sClient.Create(ctx, ts)).Should(Succeed())

		By("Running a spawner cycle")
		key := types.NamespacedName{Name: ts.Name, Namespace: ns}
		opts := spawner.Options{
			GitHubOwner:   "test-owner",
			GitHubRepo:    "test-repo",
			GitHubBaseURL: errorServer.URL,
		}
		err := spawner.RunCycle(ctx, k8sClient, key, opts)
		Expect(err).Should(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("403"))
	})
})
