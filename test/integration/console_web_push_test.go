package integration

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/consoleserver"
)

type consolePushTransport func(*http.Request) (*http.Response, error)

func (f consolePushTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestConsoleWebPush(t *testing.T) {
	environment := &envtest.Environment{CRDDirectoryPaths: []string{filepath.Join("..", "..", "internal", "manifests")}, ErrorIfCRDPathMissing: true}
	config, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := kelos.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"console-system", "team"} {
		if err := kube.Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	deviceKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	subscription := webpush.Subscription{
		Endpoint: "https://web.push.apple.com/device",
		Keys:     webpush.Keys{Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), P256dh: base64.RawURLEncoding.EncodeToString(deviceKey.PublicKey().Bytes())},
	}
	type delivery struct {
		replica   int
		encrypted bool
		subject   string
	}
	deliveries := make(chan delivery, 16)
	subjects := []string{"mailto:admin@example.com", "https://console.example/contact"}
	servers := make([]*consoleserver.Server, 2)
	for i := range servers {
		servers[i], err = consoleserver.New(consoleserver.Config{
			Token: "secret-token", Client: kube, Clientset: clientset, RESTConfig: config, DefaultNamespace: "team",
			WebPush: &consoleserver.WebPushConfig{Namespace: "console-system", Subject: subjects[i], PublicKey: publicKey, PrivateKey: privateKey, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
			HTTPClient: &http.Client{Transport: consolePushTransport(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				parts := strings.Split(strings.SplitN(strings.TrimPrefix(request.Header.Get("Authorization"), "vapid t="), ",", 2)[0], ".")
				var claims struct {
					Subject string `json:"sub"`
				}
				if len(parts) == 3 {
					decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
					if err == nil {
						_ = json.Unmarshal(decoded, &claims)
					}
				}
				deliveries <- delivery{replica: i, subject: claims.Subject, encrypted: request.URL.Host == "web.push.apple.com" && request.Header.Get("Content-Encoding") == "aes128gcm" &&
					strings.Contains(request.Header.Get("Authorization"), "vapid t=") && request.Header.Get("TTL") == "3600" &&
					len(body) > 0 && !bytes.Contains(body, []byte("Work completed"))}
				return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
			})},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	payload, _ := json.Marshal(map[string]any{"namespace": "team", "subscription": subscription})
	request := httptest.NewRequest(http.MethodPut, "/api/push-subscription", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	servers[0].ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("subscribe: %d %s", response.Code, response.Body.String())
	}

	cancels := make([]context.CancelFunc, len(servers))
	done := make([]chan error, len(servers))
	for i, server := range servers {
		workerCtx, cancelWorker := context.WithCancel(t.Context())
		cancels[i], done[i] = cancelWorker, make(chan error, 1)
		go func() { done[i] <- server.RunWebPush(workerCtx) }()
	}
	t.Cleanup(func() {
		for _, cancelWorker := range cancels {
			cancelWorker()
		}
	})
	session := &kelos.Session{
		ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "team"},
		Spec:       kelos.SessionSpec{Worker: kelos.WorkerSpec{Type: "codex", Credentials: &kelos.Credentials{Type: kelos.CredentialTypeNone}, WorkspaceRef: &kelos.WorkspaceReference{Name: "workspace"}}},
	}
	if err := kube.Create(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	updateEvent := func(id string) {
		t.Helper()
		if err := kube.Get(t.Context(), client.ObjectKeyFromObject(session), session); err != nil {
			t.Fatal(err)
		}
		session.Status.LastNotification = &kelos.SessionNotification{ID: id, Type: kelos.SessionNotificationTypeTurnCompleted, EventTime: metav1.Now()}
		if err := kube.Status().Update(t.Context(), session); err != nil {
			t.Fatal(err)
		}
	}
	awaitDelivery := func() delivery {
		t.Helper()
		select {
		case sent := <-deliveries:
			if sent.subject != subjects[sent.replica] {
				t.Fatalf("VAPID subject = %q, want %q", sent.subject, subjects[sent.replica])
			}
			if !sent.encrypted {
				t.Fatal("push request missing encryption or VAPID authentication")
			}
			return sent
		case <-time.After(30 * time.Second):
			t.Fatal("no background push delivery")
			return delivery{}
		}
	}
	awaitCursor := func(id string) {
		t.Helper()
		err := wait.PollUntilContextTimeout(t.Context(), 100*time.Millisecond, 10*time.Second, true, func(ctx context.Context) (bool, error) {
			var secrets corev1.SecretList
			if err := kube.List(ctx, &secrets, client.InNamespace("console-system"), client.MatchingLabels{"console.kelos.dev/push-subscription": "true"}); err != nil {
				return false, nil
			}
			if len(secrets.Items) != 1 {
				return false, nil
			}
			var stored struct {
				Seen map[string]string `json:"seen"`
			}
			if err := json.Unmarshal(secrets.Items[0].Data["subscription"], &stored); err != nil {
				return false, err
			}
			return stored.Seen[string(session.UID)] == id, nil
		})
		if err != nil {
			t.Fatalf("delivery cursor not saved: %v", err)
		}
	}
	updateEvent("journal/1")
	first := awaitDelivery()
	awaitCursor("journal/1")
	cancels[first.replica]()
	select {
	case err := <-done[first.replica]:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("delivery leader did not stop")
	}
	updateEvent("journal/2")
	second := awaitDelivery()
	if second.replica == first.replica {
		t.Fatal("cancelled replica continued sending")
	}
	awaitCursor("journal/2")
	cancels[second.replica]()
	select {
	case err := <-done[second.replica]:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("replacement leader did not stop")
	}
	select {
	case extra := <-deliveries:
		t.Fatalf("event redelivered during leader change: %#v", extra)
	default:
	}
}
