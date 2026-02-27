package framework

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
	kelosclientset "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned"
)

// Framework provides a per-test namespace environment for e2e tests,
// similar to the Kubernetes e2e framework. Each Framework instance
// creates a unique namespace before each test and tears it down after.
type Framework struct {
	// BaseName is used as a prefix when generating the namespace name.
	BaseName string

	// Namespace is the Kubernetes namespace created for the current test.
	// It is set during BeforeEach and cleared during AfterEach.
	Namespace string

	// Clientset is a standard Kubernetes clientset for the test cluster.
	Clientset kubernetes.Interface

	// KelosClientset is a generated typed clientset for kelos.dev resources.
	KelosClientset kelosclientset.Interface
}

// NewFramework creates a new Framework and registers Ginkgo lifecycle
// hooks (BeforeEach/AfterEach) that manage the test namespace.
func NewFramework(baseName string) *Framework {
	f := &Framework{
		BaseName: baseName,
	}

	BeforeEach(f.beforeEach)
	AfterEach(f.afterEach)

	return f
}

func (f *Framework) beforeEach() {
	suffix := randomSuffix(6)
	f.Namespace = fmt.Sprintf("e2e-%s-%s", f.BaseName, suffix)
	// Kubernetes namespace names must be at most 63 characters and
	// conform to RFC 1123 DNS labels (no trailing hyphens).
	if len(f.Namespace) > 63 {
		f.Namespace = strings.TrimRight(f.Namespace[:63], "-")
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	Expect(err).NotTo(HaveOccurred(), "Failed to get kubeconfig")

	cs, err := kubernetes.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred(), "Failed to create kubernetes clientset")
	f.Clientset = cs

	ac, err := kelosclientset.NewForConfig(cfg)
	Expect(err).NotTo(HaveOccurred(), "Failed to create kelos clientset")
	f.KelosClientset = ac

	By(fmt.Sprintf("Creating test namespace %s", f.Namespace))
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: f.Namespace,
		},
	}
	_, err = f.Clientset.CoreV1().Namespaces().Create(context.TODO(), ns, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")
}

// randomSuffix returns a random hex string of the given length.
func randomSuffix(n int) string {
	b := make([]byte, (n+1)/2)
	_, err := rand.Read(b)
	if err != nil {
		// Fall back to timestamp if crypto/rand fails.
		s := fmt.Sprintf("%d", time.Now().UnixNano())
		if len(s) > n {
			s = s[:n]
		}
		return s
	}
	return fmt.Sprintf("%x", b)[:n]
}

func (f *Framework) afterEach() {
	if f.Namespace == "" {
		return
	}

	if CurrentSpecReport().Failed() {
		f.collectDebugInfo()
	}

	By(fmt.Sprintf("Deleting test namespace %s", f.Namespace))
	err := f.Clientset.CoreV1().Namespaces().Delete(context.TODO(), f.Namespace, metav1.DeleteOptions{})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Warning: failed to delete namespace %s: %v\n", f.Namespace, err)
	}

	f.Namespace = ""
}

// collectDebugInfo logs diagnostic information on test failure.
func (f *Framework) collectDebugInfo() {
	By("Collecting debug info on failure")
	ctx := context.TODO()

	// List tasks
	tasks, err := f.KelosClientset.ApiV1alpha1().Tasks(f.Namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, t := range tasks.Items {
			fmt.Fprintf(GinkgoWriter, "Task %s: phase=%s\n", t.Name, t.Status.Phase)
		}
	}

	// List taskspawners
	spawners, err := f.KelosClientset.ApiV1alpha1().TaskSpawners(f.Namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, s := range spawners.Items {
			fmt.Fprintf(GinkgoWriter, "TaskSpawner %s: phase=%s\n", s.Name, s.Status.Phase)
		}
	}

	// List pods and collect logs from failed ones
	pods, err := f.Clientset.CoreV1().Pods(f.Namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, p := range pods.Items {
			fmt.Fprintf(GinkgoWriter, "Pod %s: phase=%s\n", p.Name, p.Status.Phase)
			if p.Status.Phase == corev1.PodFailed || p.Status.Phase == corev1.PodSucceeded {
				for _, c := range p.Spec.Containers {
					tailLines := int64(30)
					req := f.Clientset.CoreV1().Pods(f.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{
						Container: c.Name,
						TailLines: &tailLines,
					})
					stream, sErr := req.Stream(ctx)
					if sErr != nil {
						continue
					}
					logBytes, _ := io.ReadAll(stream)
					stream.Close()
					fmt.Fprintf(GinkgoWriter, "Pod %s container %s logs (last 30 lines):\n%s\n", p.Name, c.Name, string(logBytes))
				}
			}
		}
	}

	// List jobs
	jobs, err := f.Clientset.BatchV1().Jobs(f.Namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, j := range jobs.Items {
			fmt.Fprintf(GinkgoWriter, "Job %s: active=%d succeeded=%d failed=%d\n",
				j.Name, j.Status.Active, j.Status.Succeeded, j.Status.Failed)
		}
	}

	// Controller manager logs (best-effort)
	managerPods, err := f.Clientset.CoreV1().Pods("kelos-system").List(ctx, metav1.ListOptions{
		LabelSelector: "control-plane=controller-manager",
	})
	if err == nil {
		tailLines := int64(50)
		for _, p := range managerPods.Items {
			req := f.Clientset.CoreV1().Pods("kelos-system").GetLogs(p.Name, &corev1.PodLogOptions{
				TailLines: &tailLines,
			})
			stream, err := req.Stream(ctx)
			if err != nil {
				continue
			}
			logBytes, _ := io.ReadAll(stream)
			stream.Close()
			fmt.Fprintf(GinkgoWriter, "Controller manager logs (%s):\n%s\n", p.Name, string(logBytes))
		}
	}
}

// CreateSecret creates a generic secret from literal key-value pairs
// in the test namespace.
func (f *Framework) CreateSecret(name string, literals ...string) {
	data := make(map[string][]byte)
	for _, l := range literals {
		parts := strings.SplitN(l, "=", 2)
		Expect(parts).To(HaveLen(2), "Literal must be in key=value format: %s", l)
		data[parts[0]] = []byte(parts[1])
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: f.Namespace,
		},
		Data: data,
	}
	_, err := f.Clientset.CoreV1().Secrets(f.Namespace).Create(context.TODO(), secret, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to create secret %s", name)
}

// CreateTask creates a Task in the test namespace using the kelos clientset.
func (f *Framework) CreateTask(task *kelosv1alpha1.Task) {
	if task.Namespace == "" {
		task.Namespace = f.Namespace
	}
	_, err := f.KelosClientset.ApiV1alpha1().Tasks(task.Namespace).Create(context.TODO(), task, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to create task %s", task.Name)
}

// CreateWorkspace creates a Workspace in the test namespace using the kelos clientset.
func (f *Framework) CreateWorkspace(ws *kelosv1alpha1.Workspace) {
	if ws.Namespace == "" {
		ws.Namespace = f.Namespace
	}
	_, err := f.KelosClientset.ApiV1alpha1().Workspaces(ws.Namespace).Create(context.TODO(), ws, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to create workspace %s", ws.Name)
}

// CreateTaskSpawner creates a TaskSpawner in the test namespace using the kelos clientset.
func (f *Framework) CreateTaskSpawner(ts *kelosv1alpha1.TaskSpawner) {
	if ts.Namespace == "" {
		ts.Namespace = f.Namespace
	}
	_, err := f.KelosClientset.ApiV1alpha1().TaskSpawners(ts.Namespace).Create(context.TODO(), ts, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to create taskspawner %s", ts.Name)
}

// DeleteTask deletes a Task by name from the test namespace using the kelos clientset.
func (f *Framework) DeleteTask(name string) {
	err := f.KelosClientset.ApiV1alpha1().Tasks(f.Namespace).Delete(context.TODO(), name, metav1.DeleteOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to delete task %s", name)
}

// DeleteWorkspace deletes a Workspace by name from the test namespace using the kelos clientset.
func (f *Framework) DeleteWorkspace(name string) {
	err := f.KelosClientset.ApiV1alpha1().Workspaces(f.Namespace).Delete(context.TODO(), name, metav1.DeleteOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to delete workspace %s", name)
}

// DeleteTaskSpawner deletes a TaskSpawner by name from the test namespace using the kelos clientset.
func (f *Framework) DeleteTaskSpawner(name string) {
	err := f.KelosClientset.ApiV1alpha1().TaskSpawners(f.Namespace).Delete(context.TODO(), name, metav1.DeleteOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to delete taskspawner %s", name)
}

// WaitForJobCreation waits for a Job with the given name to appear.
func (f *Framework) WaitForJobCreation(name string) {
	Eventually(func() error {
		_, err := f.Clientset.BatchV1().Jobs(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
		return err
	}, 30*time.Second, time.Second).Should(Succeed())
}

// WaitForJobCompletion waits for a Job to reach the complete condition.
func (f *Framework) WaitForJobCompletion(name string) {
	Eventually(func() bool {
		job, err := f.Clientset.BatchV1().Jobs(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, c := range job.Status.Conditions {
			if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, 5*time.Minute, 10*time.Second).Should(BeTrue())
}

// WaitForDeploymentAvailable waits for a Deployment to reach the available condition.
func (f *Framework) WaitForDeploymentAvailable(name string) {
	Eventually(func() bool {
		deploy, err := f.Clientset.AppsV1().Deployments(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, c := range deploy.Status.Conditions {
			if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
				return true
			}
		}
		return false
	}, 2*time.Minute, 10*time.Second).Should(BeTrue())
}

// GetTaskPhase returns the phase of a Task.
func (f *Framework) GetTaskPhase(name string) string {
	task, err := f.KelosClientset.ApiV1alpha1().Tasks(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to get task %s", name)
	return string(task.Status.Phase)
}

// GetTaskOutputs returns the outputs of a Task as a joined string.
func (f *Framework) GetTaskOutputs(name string) string {
	task, err := f.KelosClientset.ApiV1alpha1().Tasks(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to get task %s", name)
	return strings.Join(task.Status.Outputs, "\n")
}

// GetTaskResults returns the Results map of a Task.
func (f *Framework) GetTaskResults(name string) map[string]string {
	task, err := f.KelosClientset.ApiV1alpha1().Tasks(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to get task %s", name)
	return task.Status.Results
}

// GetTaskSpawnerPhase returns the phase of a TaskSpawner.
func (f *Framework) GetTaskSpawnerPhase(name string) string {
	ts, err := f.KelosClientset.ApiV1alpha1().TaskSpawners(f.Namespace).Get(context.TODO(), name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "Failed to get taskspawner %s", name)
	return string(ts.Status.Phase)
}

// ListTaskNames returns the names of all Tasks matching the given label selector.
func (f *Framework) ListTaskNames(labelSelector string) []string {
	tasks, err := f.KelosClientset.ApiV1alpha1().Tasks(f.Namespace).List(context.TODO(), metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to list tasks")
	var names []string
	for _, t := range tasks.Items {
		names = append(names, t.Name)
	}
	return names
}

// GetJobLogs returns the logs of a Job's pod.
func (f *Framework) GetJobLogs(name string) string {
	ctx := context.TODO()

	// Find the pod for this job
	pods, err := f.Clientset.CoreV1().Pods(f.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-name=" + name,
	})
	Expect(err).NotTo(HaveOccurred(), "Failed to list pods for job %s", name)
	Expect(pods.Items).NotTo(BeEmpty(), "No pods found for job %s", name)

	req := f.Clientset.CoreV1().Pods(f.Namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	Expect(err).NotTo(HaveOccurred(), "Failed to get logs for job %s", name)
	defer stream.Close()

	var buf bytes.Buffer
	_, err = io.Copy(&buf, stream)
	Expect(err).NotTo(HaveOccurred(), "Failed to read logs for job %s", name)
	return buf.String()
}

// KelosBin returns the path to the kelos binary.
func KelosBin() string {
	if bin := os.Getenv("KELOS_BIN"); bin != "" {
		return bin
	}
	return "kelos"
}

// Kelos executes an kelos CLI command with output directed to GinkgoWriter.
// It fails the test on error.
func Kelos(args ...string) {
	cmd := exec.Command(KelosBin(), args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred())
}

// KelosOutput executes an kelos CLI command and returns its stdout.
// It fails the test on error.
func KelosOutput(args ...string) string {
	cmd := exec.Command(KelosBin(), args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(out.String())
}

// KelosOutputWithStderr executes an kelos CLI command and returns both
// stdout and stderr.
func KelosOutputWithStderr(args ...string) (string, string) {
	cmd := exec.Command(KelosBin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred())
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String())
}

// KelosFail executes an kelos CLI command and expects it to fail.
func KelosFail(args ...string) {
	cmd := exec.Command(KelosBin(), args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	err := cmd.Run()
	Expect(err).To(HaveOccurred())
}

// KelosCommand creates an exec.Cmd for the kelos binary without running it.
func KelosCommand(args ...string) *exec.Cmd {
	cmd := exec.Command(KelosBin(), args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	return cmd
}
