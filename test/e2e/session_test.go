package e2e

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionruntime"
	"github.com/kelos-dev/kelos/test/e2e/framework"
)

const sessionWebTokenEnv = "E2E_SESSION_WEB_TOKEN"

//go:embed fixtures/fake-claude.js
var fakeClaude string

//go:embed fixtures/fake-opencode.js
var fakeOpenCode string

var portForwardAddressPattern = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:([0-9]+) ->`)

var _ = Describe("Session remote control", func() {
	f := framework.NewFramework("session-control")

	It("shares one provider session across terminal and web clients", func() {
		token := os.Getenv(sessionWebTokenEnv)
		if token == "" {
			Skip(sessionWebTokenEnv + " not set")
		}

		sessionName := strings.TrimPrefix(f.Namespace, "e2e-")
		configMapName := sessionName + "-provider"
		mode := int32(0555)
		_, err := f.Clientset.CoreV1().ConfigMaps(f.Namespace).Create(context.TODO(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: f.Namespace},
			Data:       map[string]string{"claude": fakeClaude},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = f.Clientset.CoreV1().ConfigMaps(f.Namespace).Delete(context.TODO(), configMapName, metav1.DeleteOptions{})
		})

		created := createSession(f, &kelos.Session{
			ObjectMeta: metav1.ObjectMeta{Name: sessionName, Namespace: f.Namespace},
			Spec: kelos.SessionSpec{
				Worker: kelos.WorkerSpec{
					Type:        "claude-code",
					Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
					PodOverrides: &kelos.PodOverrides{
						Env: []corev1.EnvVar{{
							Name:  "PATH",
							Value: "/workspace/fake-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
						}},
						Volumes: []corev1.Volume{{
							Name: "fake-provider",
							VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
								DefaultMode:          &mode,
							}},
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "fake-provider",
							MountPath: "/workspace/fake-bin",
							ReadOnly:  true,
						}},
					},
				},
				VolumeClaimTemplate: &corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					}},
				},
			},
		})
		DeferCleanup(func() {
			_ = f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).Delete(context.TODO(), sessionName, metav1.DeleteOptions{})
		})
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				collectSessionDebugInfo(f, f.Namespace, sessionName)
			}
		})

		current := waitForSessionPhase(f, f.Namespace, sessionName, kelos.SessionPhaseReady)
		pod, err := f.Clientset.CoreV1().Pods(f.Namespace).Get(context.TODO(), current.Status.PodName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(current.Status.PodUID).To(Equal(pod.UID))
		controllerRef := metav1.GetControllerOf(pod)
		Expect(controllerRef).NotTo(BeNil())
		statefulSet, err := f.Clientset.AppsV1().StatefulSets(f.Namespace).Get(context.TODO(), controllerRef.Name, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(metav1.IsControlledBy(statefulSet, created)).To(BeTrue())
		Expect(metav1.IsControlledBy(pod, statefulSet)).To(BeTrue())
		Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
		Expect(*statefulSet.Spec.Replicas).To(Equal(int32(1)))
		pods, err := f.Clientset.CoreV1().Pods(f.Namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "kelos.dev/session=" + sessionName,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(pods.Items).To(HaveLen(1))

		By("completing turns through separate terminal connections")
		runTerminalTurn(f.Namespace, sessionName, "terminal-one", "agent › turn 1: terminal-one")
		runTerminalTurn(f.Namespace, sessionName, "terminal-two", "agent › turn 2: terminal-two")

		By("authenticating to the shared Session web server")
		baseURL := startSessionServerPortForward()
		unauthenticatedClient := &http.Client{Timeout: 30 * time.Second}
		response, err := unauthenticatedClient.Get(baseURL + "/api/sessions")
		Expect(err).NotTo(HaveOccurred())
		Expect(response.StatusCode).To(Equal(http.StatusUnauthorized))
		response.Body.Close()

		webClient := loginSessionWeb(baseURL, token)
		Eventually(func() []string {
			return listWebSessions(webClient, baseURL, f.Namespace)
		}, 30*time.Second, 200*time.Millisecond).Should(ContainElement(sessionName))

		connection := connectSessionWebSocket(webClient, baseURL, f.Namespace, sessionName)
		DeferCleanup(func() { _ = connection.Close() })
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "subscribe"})
		seenFirstTurn := false
		seenSecondTurn := false
		for {
			event := readSessionEvent(connection)
			seenFirstTurn = seenFirstTurn || strings.Contains(event.Text, "turn 1: terminal-one")
			seenSecondTurn = seenSecondTurn || strings.Contains(event.Text, "turn 2: terminal-two")
			if event.Type == sessionruntime.EventHistoryEnd {
				break
			}
		}
		Expect(seenFirstTurn).To(BeTrue())
		Expect(seenSecondTurn).To(BeTrue())

		By("continuing the terminal conversation through web chat")
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "message", Text: "web"})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventAssistantDelta && event.Text == "turn 3: web"
		})
		waitForTurnCompletion(connection, "completed")

		By("answering a structured provider question")
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "message", Text: "question"})
		input := waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventInputRequested
		})
		Expect(input.Questions).To(HaveLen(1))
		Expect(input.Questions[0].Question).To(Equal("Which database?"))
		sendSessionRequest(connection, sessionruntime.ClientRequest{
			Type:    "input",
			InputID: input.InputID,
			Answers: map[string][]string{input.Questions[0].ID: {"PostgreSQL"}},
		})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventAssistantDelta && event.Text == "answer: PostgreSQL"
		})
		waitForTurnCompletion(connection, "completed")

		By("interrupting active work without ending the provider session")
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "message", Text: "block"})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventTurnStarted
		})
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "interrupt"})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventTurnInterrupting
		})
		waitForTurnCompletion(connection, "interrupted")

		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "message", Text: "after"})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventAssistantDelta && event.Text == "turn 6: after"
		})
		waitForTurnCompletion(connection, "completed")
		Expect(waitForSessionPhase(f, f.Namespace, sessionName, kelos.SessionPhaseReady).Status.PodUID).To(Equal(pod.UID))

		By("recovering conversation and workspace state after the Pod is deleted")
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "message", Text: "write-state"})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventAssistantDelta && event.Text == "turn 7: state written"
		})
		waitForTurnCompletion(connection, "completed")
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "message", Text: "block"})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventTurnStarted && event.TurnID == "turn-8"
		})
		oldPodUID := pod.UID
		oldClaimName := sessionWorkspaceClaimName(pod)
		Expect(oldClaimName).NotTo(BeEmpty())
		Expect(f.Clientset.CoreV1().Pods(f.Namespace).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})).To(Succeed())
		_ = connection.Close()

		recovered := waitForSessionPodReplacement(f, f.Namespace, sessionName, oldPodUID)
		replacement, err := f.Clientset.CoreV1().Pods(f.Namespace).Get(context.TODO(), recovered.Status.PodName, metav1.GetOptions{})
		Expect(err).NotTo(HaveOccurred())
		Expect(replacement.UID).NotTo(Equal(oldPodUID))
		Expect(sessionWorkspaceClaimName(replacement)).To(Equal(oldClaimName))

		connection = connectSessionWebSocket(webClient, baseURL, f.Namespace, sessionName)
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "subscribe"})
		seenRecovery := false
		seenInterruptedTurn := false
		for {
			event := readSessionEvent(connection)
			seenRecovery = seenRecovery || event.Type == sessionruntime.EventRuntimeRecovered
			seenInterruptedTurn = seenInterruptedTurn || (event.Type == sessionruntime.EventTurnCompleted && event.TurnID == "turn-8" && event.Status == "interrupted")
			if event.Type == sessionruntime.EventHistoryEnd {
				break
			}
		}
		Expect(seenRecovery).To(BeTrue())
		Expect(seenInterruptedTurn).To(BeTrue())
		sendSessionRequest(connection, sessionruntime.ClientRequest{Type: "message", Text: "read-state"})
		waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
			return event.Type == sessionruntime.EventAssistantDelta && event.Text == "turn 9: state preserved"
		})
		waitForTurnCompletion(connection, "completed")

		By("deleting the Session and its StatefulSet-backed Pod")
		Expect(f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).Delete(context.TODO(), sessionName, metav1.DeleteOptions{})).To(Succeed())
		waitForPodDeletion(f, f.Namespace, pod.Name)
		waitForPVCDeletion(f, f.Namespace, oldClaimName)
	})

	It("runs an OpenCode conversation through terminal chat", func() {
		sessionName := "opencode-session"
		configMapName := sessionName + "-provider"
		mode := int32(0555)
		_, err := f.Clientset.CoreV1().ConfigMaps(f.Namespace).Create(context.TODO(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: f.Namespace},
			Data:       map[string]string{"opencode": fakeOpenCode},
		}, metav1.CreateOptions{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_ = f.Clientset.CoreV1().ConfigMaps(f.Namespace).Delete(context.TODO(), configMapName, metav1.DeleteOptions{})
		})

		createSession(f, &kelos.Session{
			ObjectMeta: metav1.ObjectMeta{Name: sessionName},
			Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
				Type:        "opencode",
				Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone},
				PodOverrides: &kelos.PodOverrides{
					Env: []corev1.EnvVar{{
						Name:  "PATH",
						Value: "/workspace/fake-bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
					}},
					Volumes: []corev1.Volume{{
						Name: "fake-provider",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: configMapName},
							DefaultMode:          &mode,
						}},
					}},
					VolumeMounts: []corev1.VolumeMount{{
						Name:      "fake-provider",
						MountPath: "/workspace/fake-bin",
						ReadOnly:  true,
					}},
				},
			}},
		})
		DeferCleanup(func() {
			_ = f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).Delete(context.TODO(), sessionName, metav1.DeleteOptions{})
		})
		DeferCleanup(func() {
			if CurrentSpecReport().Failed() {
				collectSessionDebugInfo(f, f.Namespace, sessionName)
			}
		})

		current := waitForSessionPhase(f, f.Namespace, sessionName, kelos.SessionPhaseReady)
		runTerminalTurn(f.Namespace, sessionName, "hello", "agent › opencode: hello")
		Expect(f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).Delete(context.TODO(), sessionName, metav1.DeleteOptions{})).To(Succeed())
		waitForPodDeletion(f, f.Namespace, current.Status.PodName)
	})
})

func describeSessionProviderTests(cfg agentTestConfig) {
	Describe(fmt.Sprintf("Session provider [%s]", cfg.AgentType), func() {
		f := framework.NewFramework(fmt.Sprintf("session-%s", cfg.AgentType))

		BeforeEach(func() {
			if *cfg.SecretValue == "" {
				Skip(cfg.EnvVar + " not set")
			}
		})

		It("starts a provider conversation and accepts a terminal turn", func() {
			f.CreateSecret(cfg.SecretName, cfg.SecretKey+"="+*cfg.SecretValue)
			createSession(f, &kelos.Session{
				ObjectMeta: metav1.ObjectMeta{Name: "provider-session"},
				Spec: kelos.SessionSpec{Worker: kelos.WorkerSpec{
					Type:  cfg.AgentType,
					Model: cfg.Model,
					Credentials: &kelos.Credentials{
						Type:      cfg.CredentialType,
						SecretRef: &kelos.SecretReference{Name: cfg.SecretName},
					},
				}},
			})
			current := waitForSessionPhase(f, f.Namespace, "provider-session", kelos.SessionPhaseReady)
			runTerminalTurn(
				f.Namespace,
				"provider-session",
				"Join these fragments without spaces and reply with only the result: SESSION_, E2E_, READY",
				"agent › SESSION_E2E_READY",
			)
			Expect(f.KelosClientset.ApiV1alpha2().Sessions(f.Namespace).Delete(context.TODO(), "provider-session", metav1.DeleteOptions{})).To(Succeed())
			waitForPodDeletion(f, f.Namespace, current.Status.PodName)
		})
	})
}

var _ = func() bool {
	for _, cfg := range agentConfigs {
		describeSessionProviderTests(cfg)
	}
	return true
}()

func createSession(f *framework.Framework, session *kelos.Session) *kelos.Session {
	if session.Namespace == "" {
		session.Namespace = f.Namespace
	}
	created, err := f.KelosClientset.ApiV1alpha2().Sessions(session.Namespace).Create(context.TODO(), session, metav1.CreateOptions{})
	Expect(err).NotTo(HaveOccurred())
	return created
}

func waitForSessionPhase(f *framework.Framework, namespace, name string, phase kelos.SessionPhase) *kelos.Session {
	Eventually(func() kelos.SessionPhase {
		session, err := f.KelosClientset.ApiV1alpha2().Sessions(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil {
			return ""
		}
		return session.Status.Phase
	}, 3*time.Minute, time.Second).Should(Equal(phase), "Session %s/%s did not reach phase %s", namespace, name, phase)
	session, err := f.KelosClientset.ApiV1alpha2().Sessions(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred())
	return session
}

func waitForSessionPodReplacement(f *framework.Framework, namespace, name string, oldUID types.UID) *kelos.Session {
	var recovered *kelos.Session
	Eventually(func() bool {
		session, err := f.KelosClientset.ApiV1alpha2().Sessions(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		if err != nil || session.Status.Phase != kelos.SessionPhaseReady || session.Status.PodUID == "" || session.Status.PodUID == oldUID {
			return false
		}
		recovered = session
		return true
	}, 3*time.Minute, time.Second).Should(BeTrue(), "Session %s/%s did not recover from Pod loss", namespace, name)
	return recovered
}

func sessionWorkspaceClaimName(pod *corev1.Pod) string {
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "workspace" && volume.PersistentVolumeClaim != nil {
			return volume.PersistentVolumeClaim.ClaimName
		}
	}
	return ""
}

func waitForPodDeletion(f *framework.Framework, namespace, name string) {
	Eventually(func() bool {
		_, err := f.Clientset.CoreV1().Pods(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, 2*time.Minute, time.Second).Should(BeTrue(), "Pod %s/%s was not deleted", namespace, name)
}

func waitForPVCDeletion(f *framework.Framework, namespace, name string) {
	Eventually(func() bool {
		_, err := f.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.TODO(), name, metav1.GetOptions{})
		return apierrors.IsNotFound(err)
	}, 2*time.Minute, time.Second).Should(BeTrue(), "PersistentVolumeClaim %s/%s was not deleted", namespace, name)
}

func collectSessionDebugInfo(f *framework.Framework, namespace, name string) {
	session, err := f.KelosClientset.ApiV1alpha2().Sessions(namespace).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Session %s/%s: %v\n", namespace, name, err)
		return
	}
	fmt.Fprintf(GinkgoWriter, "Session %s/%s: phase=%s pod=%s message=%s\n", namespace, name, session.Status.Phase, session.Status.PodName, session.Status.Message)
	if session.Status.PodName == "" {
		return
	}
	pod, err := f.Clientset.CoreV1().Pods(namespace).Get(context.TODO(), session.Status.PodName, metav1.GetOptions{})
	if err != nil {
		fmt.Fprintf(GinkgoWriter, "Session Pod %s/%s: %v\n", namespace, session.Status.PodName, err)
		return
	}
	fmt.Fprintf(GinkgoWriter, "Session Pod %s/%s: phase=%s\n", namespace, pod.Name, pod.Status.Phase)
	if claimName := sessionWorkspaceClaimName(pod); claimName != "" {
		claim, err := f.Clientset.CoreV1().PersistentVolumeClaims(namespace).Get(context.TODO(), claimName, metav1.GetOptions{})
		if err != nil {
			fmt.Fprintf(GinkgoWriter, "Session PersistentVolumeClaim %s/%s: %v\n", namespace, claimName, err)
		} else {
			fmt.Fprintf(GinkgoWriter, "Session PersistentVolumeClaim %s/%s: phase=%s\n", namespace, claimName, claim.Status.Phase)
		}
	}
	containers := make([]corev1.Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
	containers = append(containers, pod.Spec.InitContainers...)
	containers = append(containers, pod.Spec.Containers...)
	for _, container := range containers {
		tailLines := int64(50)
		stream, err := f.Clientset.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
			Container: container.Name,
			TailLines: &tailLines,
		}).Stream(context.TODO())
		if err != nil {
			continue
		}
		logs, _ := io.ReadAll(stream)
		stream.Close()
		fmt.Fprintf(GinkgoWriter, "Pod %s container %s logs (last 50 lines):\n%s\n", pod.Name, container.Name, logs)
	}
}

func runTerminalTurn(namespace, name, prompt, expectedOutput string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, framework.KelosBin(), "session", "connect", name, "-n", namespace)
	output := &lockedBuffer{}
	command.Stdout = io.MultiWriter(GinkgoWriter, output)
	command.Stderr = GinkgoWriter
	stdin, err := command.StdinPipe()
	Expect(err).NotTo(HaveOccurred())
	Expect(command.Start()).To(Succeed())
	Eventually(output.String, 30*time.Second, 100*time.Millisecond).Should(ContainSubstring("Connected. Type a message"))
	_, err = io.WriteString(stdin, prompt+"\n")
	Expect(err).NotTo(HaveOccurred())
	Eventually(output.String, 3*time.Minute, 200*time.Millisecond).Should(ContainSubstring(expectedOutput))
	_, err = io.WriteString(stdin, "/quit\n")
	Expect(err).NotTo(HaveOccurred())
	Expect(stdin.Close()).To(Succeed())
	Expect(command.Wait()).To(Succeed(), "terminal output:\n%s", output.String())
}

func startSessionServerPortForward() string {
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, "kubectl", "--namespace", "kelos-system", "port-forward", "--address", "127.0.0.1", "service/kelos-session-server", ":80")
	output := &lockedBuffer{}
	command.Stdout = io.MultiWriter(GinkgoWriter, output)
	command.Stderr = io.MultiWriter(GinkgoWriter, output)
	Expect(command.Start()).To(Succeed())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	DeferCleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = command.Process.Kill()
		}
	})

	var port string
	Eventually(func() string {
		match := portForwardAddressPattern.FindStringSubmatch(output.String())
		if len(match) == 2 {
			port = match[1]
		}
		return port
	}, 30*time.Second, 100*time.Millisecond).ShouldNot(BeEmpty(), "kubectl port-forward output:\n%s", output.String())
	return "http://127.0.0.1:" + port
}

func loginSessionWeb(baseURL, token string) *http.Client {
	jar, err := cookiejar.New(nil)
	Expect(err).NotTo(HaveOccurred())
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}
	payload, err := json.Marshal(map[string]string{"token": token})
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Post(baseURL+"/api/login", "application/json", bytes.NewReader(payload))
	Expect(err).NotTo(HaveOccurred())
	defer response.Body.Close()
	Expect(response.StatusCode).To(Equal(http.StatusOK))
	return client
}

func listWebSessions(client *http.Client, baseURL, namespace string) []string {
	response, err := client.Get(baseURL + "/api/sessions?namespace=" + url.QueryEscape(namespace))
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var sessions []struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(response.Body).Decode(&sessions) != nil {
		return nil
	}
	names := make([]string, 0, len(sessions))
	for _, session := range sessions {
		names = append(names, session.Name)
	}
	return names
}

func connectSessionWebSocket(client *http.Client, baseURL, namespace, sessionName string) *websocket.Conn {
	parsed, err := url.Parse(baseURL)
	Expect(err).NotTo(HaveOccurred())
	header := http.Header{}
	for _, cookie := range client.Jar.Cookies(parsed) {
		header.Add("Cookie", cookie.String())
	}
	webSocketURL := "ws://" + parsed.Host + "/api/sessions/" + namespace + "/" + sessionName + "/connect"
	connection, response, err := websocket.DefaultDialer.Dial(webSocketURL, header)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	Expect(err).NotTo(HaveOccurred())
	return connection
}

func sendSessionRequest(connection *websocket.Conn, request sessionruntime.ClientRequest) {
	Expect(connection.SetWriteDeadline(time.Now().Add(30 * time.Second))).To(Succeed())
	Expect(connection.WriteJSON(request)).To(Succeed())
}

func readSessionEvent(connection *websocket.Conn) sessionruntime.Event {
	Expect(connection.SetReadDeadline(time.Now().Add(2 * time.Minute))).To(Succeed())
	var event sessionruntime.Event
	Expect(connection.ReadJSON(&event)).To(Succeed())
	if event.Type == sessionruntime.EventError {
		Fail(fmt.Sprintf("Session runtime error: %s (%s)", event.Text, event.Status))
	}
	return event
}

func waitForSessionEvent(connection *websocket.Conn, matches func(sessionruntime.Event) bool) sessionruntime.Event {
	for {
		event := readSessionEvent(connection)
		if matches(event) {
			return event
		}
	}
}

func waitForTurnCompletion(connection *websocket.Conn, status string) {
	event := waitForSessionEvent(connection, func(event sessionruntime.Event) bool {
		return event.Type == sessionruntime.EventTurnCompleted
	})
	Expect(event.Status).To(Equal(status))
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}
