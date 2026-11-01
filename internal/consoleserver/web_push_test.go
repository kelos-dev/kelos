package consoleserver

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func enableTestWebPush(t *testing.T, s *Server) {
	t.Helper()
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	s.webPush, err = newWebPush(WebPushConfig{
		Namespace: "console-system", Subject: "mailto:admin@example.com", PublicKey: publicKey, PrivateKey: privateKey,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
}

func testPushSubscription(t *testing.T) webpush.Subscription {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return webpush.Subscription{
		Endpoint: "https://web.push.apple.com/device",
		Keys:     webpush.Keys{Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16)), P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())},
	}
}

func pushRequest(t *testing.T, s *Server, method, namespace string, subscription webpush.Subscription) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(map[string]any{"namespace": namespace, "subscription": subscription})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "/api/push-subscription", bytes.NewReader(data))
	request.Header.Set("Authorization", "Bearer secret-token")
	if s.proxyAuth != nil {
		request = oidcRequest(method, "/api/push-subscription", string(data))
	}
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	return response
}

func saveTestPushSubscription(t *testing.T, s *Server) webpush.Subscription {
	t.Helper()
	subscription := testPushSubscription(t)
	response := pushRequest(t, s, http.MethodPut, "team", subscription)
	if response.Code != http.StatusOK {
		t.Fatalf("subscribe status = %d: %s", response.Code, response.Body.String())
	}
	return subscription
}

func setPushSession(t *testing.T, s *Server, name, namespace, uid, id string, kind kelos.SessionNotificationType) {
	t.Helper()
	var session kelos.Session
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := s.client.Get(context.Background(), key, &session); err != nil {
		session = kelos.Session{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, UID: types.UID(uid)}}
		if err := s.client.Create(context.Background(), &session); err != nil {
			t.Fatal(err)
		}
	}
	session.Status.LastNotification = &kelos.SessionNotification{ID: id, Type: kind, EventTime: metav1.Now()}
	if err := s.client.Update(context.Background(), &session); err != nil {
		t.Fatal(err)
	}
}

func pushSecrets(t *testing.T, s *Server) []corev1.Secret {
	t.Helper()
	var secrets corev1.SecretList
	if err := s.client.List(context.Background(), &secrets, client.InNamespace("console-system"), client.MatchingLabels{pushSubscriptionLabel: "true"}); err != nil {
		t.Fatal(err)
	}
	return secrets.Items
}

func TestWebPushDeliveryWithoutBrowser(t *testing.T) {
	s := testServer(t)
	enableTestWebPush(t, s)
	setPushSession(t, s, "worker", "team", "uid-1", "journal/1", kelos.SessionNotificationTypeTurnCompleted)
	subscription := saveTestPushSubscription(t, s)
	var deliveries []map[string]json.RawMessage
	s.webPush.send = func(ctx context.Context, payload []byte, target *webpush.Subscription, options *webpush.Options) (*http.Response, error) {
		if target.Endpoint != subscription.Endpoint || options.VAPIDPrivateKey != s.webPush.config.PrivateKey || options.Subscriber != "admin@example.com" || options.TTL != 3600 {
			t.Fatalf("invalid target or options: %#v %#v", target, options)
		}
		var message map[string]json.RawMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			t.Fatal(err)
		}
		deliveries = append(deliveries, message)
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	deliver := func() {
		t.Helper()
		if err := s.deliverWebPushOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	deliver()
	if len(deliveries) != 0 {
		t.Fatal("existing history produced a notification")
	}
	for i, kind := range []kelos.SessionNotificationType{kelos.SessionNotificationTypeTurnCompleted, kelos.SessionNotificationTypeInputRequested, kelos.SessionNotificationTypeTurnFailed} {
		setPushSession(t, s, "worker", "team", "uid-1", "journal/"+string(rune('2'+i)), kind)
		deliver()
		deliver()
		if len(deliveries) != i+1 {
			t.Fatalf("deliveries = %d, want %d", len(deliveries), i+1)
		}
	}
	if string(deliveries[0]["title"]) != `"Work completed · worker"` {
		t.Fatalf("title = %s", deliveries[0]["title"])
	}
	var options struct {
		Tag      string `json:"tag"`
		Renotify bool   `json:"renotify"`
		Data     struct {
			ID      string                                `json:"id"`
			Session struct{ Name, Namespace, UID string } `json:"session"`
		} `json:"data"`
	}
	if err := json.Unmarshal(deliveries[0]["options"], &options); err != nil {
		t.Fatal(err)
	}
	if !options.Renotify || options.Tag != "kelos-session-team/worker/uid-1" || options.Data.ID != "journal/2" || options.Data.Session.Name != "worker" || options.Data.Session.Namespace != "team" || options.Data.Session.UID != "uid-1" {
		t.Fatalf("options = %#v", options)
	}
	// A replacement server reads its delivery cursor from Kubernetes.
	restarted := testServer(t)
	restarted.client, restarted.webPush = s.client, s.webPush
	if err := restarted.deliverWebPushOnce(context.Background()); err != nil || len(deliveries) != 3 {
		t.Fatalf("restart redelivered history: err=%v deliveries=%d", err, len(deliveries))
	}
}

func TestWebPushRetriesAndRemovesRejectedEndpoints(t *testing.T) {
	for _, failure := range []string{"network", "408", "429", "500", "401", "403", "404", "410"} {
		t.Run(failure, func(t *testing.T) {
			s := testServer(t)
			enableTestWebPush(t, s)
			saveTestPushSubscription(t, s)
			setPushSession(t, s, "worker", "team", "uid", "event", kelos.SessionNotificationTypeTurnCompleted)
			attempts := 0
			s.webPush.send = func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
				attempts++
				if failure == "network" {
					return nil, errors.New("offline")
				}
				status := map[string]int{"408": 408, "429": 429, "500": 500, "401": 401, "403": 403, "404": 404, "410": 410}[failure]
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			_ = s.deliverWebPushOnce(context.Background())
			_ = s.deliverWebPushOnce(context.Background())
			removed := failure == "401" || failure == "403" || failure == "404" || failure == "410"
			if removed && (attempts != 1 || len(pushSecrets(t, s)) != 0) {
				t.Fatalf("expired endpoint retained: attempts=%d", attempts)
			}
			if !removed {
				if attempts != 2 {
					t.Fatalf("attempts = %d", attempts)
				}
				var stored pushSubscription
				_ = json.Unmarshal(pushSecrets(t, s)[0].Data["subscription"], &stored)
				if stored.Seen["uid"] != "" {
					t.Fatal("failed event marked delivered")
				}
				s.webPush.send = func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
					return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				_ = s.deliverWebPushOnce(context.Background())
				_ = json.Unmarshal(pushSecrets(t, s)[0].Data["subscription"], &stored)
				if stored.Seen["uid"] != "event" {
					t.Fatal("successful retry not persisted")
				}
			}
		})
	}
}

func TestWebPushSubscriptionLifecycle(t *testing.T) {
	s := testServer(t)
	enableTestWebPush(t, s)
	subscription := saveTestPushSubscription(t, s)
	if len(pushSecrets(t, s)) != 1 {
		t.Fatal("subscription not stored")
	}
	if response := pushRequest(t, s, http.MethodPut, "team", subscription); response.Code != 200 || len(pushSecrets(t, s)) != 1 {
		t.Fatalf("refresh was not idempotent: %s", response.Body.String())
	}
	setPushSession(t, s, "other", "another", "uid-other", "existing", kelos.SessionNotificationTypeTurnFailed)
	if response := pushRequest(t, s, http.MethodPut, "another", subscription); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var stored pushSubscription
	_ = json.Unmarshal(pushSecrets(t, s)[0].Data["subscription"], &stored)
	if stored.Namespace != "another" || stored.Seen["uid-other"] != "existing" {
		t.Fatalf("namespace baseline = %#v", stored)
	}
	if response := pushRequest(t, s, http.MethodDelete, "", subscription); response.Code != 204 || len(pushSecrets(t, s)) != 0 {
		t.Fatalf("unsubscribe failed: %s", response.Body.String())
	}
	if response := pushRequest(t, s, http.MethodDelete, "", subscription); response.Code != 204 {
		t.Fatal("unsubscribe not idempotent")
	}
}

func TestWebPushAuthorizationAndExpiry(t *testing.T) {
	for _, reason := range []string{"namespace denied", "session denied", "review error", "expired", "token rotated"} {
		t.Run(reason, func(t *testing.T) {
			deny := false
			s := oidcTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
				if deny && reason == "review error" {
					return nil, errors.New("offline")
				}
				review.Status.Allowed = !deny || (reason == "session denied" && review.Spec.ResourceAttributes.Verb == "list")
				return review, nil
			}))
			if reason == "token rotated" {
				s = testServer(t)
			}
			enableTestWebPush(t, s)
			saveTestPushSubscription(t, s)
			setPushSession(t, s, "worker", "team", "uid", "event", kelos.SessionNotificationTypeTurnCompleted)
			calls := 0
			s.webPush.send = func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected send")
			}
			deny = true
			if reason == "expired" {
				secret := pushSecrets(t, s)[0]
				var stored pushSubscription
				_ = json.Unmarshal(secret.Data["subscription"], &stored)
				stored.ExpiresAt = time.Now().Add(-time.Hour)
				secret.Data["subscription"], _ = json.Marshal(stored)
				if err := s.client.Update(context.Background(), &secret); err != nil {
					t.Fatal(err)
				}
			}
			if reason == "token rotated" {
				s.cookieValue = "different token"
			}
			if err := s.deliverWebPushOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("unauthorized subscription delivered")
			}
			if reason != "expired" && reason != "token rotated" && len(pushSecrets(t, s)) != 1 {
				t.Fatal("authorization failure removed the subscription")
			}
			if (reason == "expired" || reason == "token rotated") && len(pushSecrets(t, s)) != 0 {
				t.Fatal("expired identity retained")
			}
		})
	}
}

func TestWebPushValidation(t *testing.T) {
	s := testServer(t)
	enableTestWebPush(t, s)
	for _, endpoint := range []string{"http://web.push.apple.com/device", "https://127.0.0.1/device", "https://web.push.apple.com.evil.example/device", "https://web.push.apple.com:8443/device", "https://user@web.push.apple.com/device"} {
		subscription := testPushSubscription(t)
		subscription.Endpoint = endpoint
		if response := pushRequest(t, s, http.MethodPut, "team", subscription); response.Code != 400 {
			t.Fatalf("endpoint %q accepted: %s", endpoint, response.Body.String())
		}
	}
	for _, mutate := range []func(*WebPushConfig){
		func(c *WebPushConfig) { c.Namespace = "" }, func(c *WebPushConfig) { c.Logger = nil },
		func(c *WebPushConfig) { c.Subject = "" }, func(c *WebPushConfig) { c.Subject = "mailto:" },
		func(c *WebPushConfig) { c.PublicKey = "bad" }, func(c *WebPushConfig) { c.PrivateKey = "bad" },
	} {
		config := s.webPush.config
		mutate(&config)
		if _, err := newWebPush(config, http.DefaultClient); err == nil {
			t.Fatalf("invalid config accepted: %#v", config)
		}
	}
	request := httptest.NewRequest("PUT", "/api/push-subscription", nil)
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatal("subscription API does not require authentication")
	}
}

func TestWebPushSubscriptionRequiresOwnerAndNamespaceAccess(t *testing.T) {
	allowed := true
	var identities []string
	s := oidcTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		identities = append(identities, review.Spec.User)
		review.Status.Allowed = allowed
		return review, nil
	}))
	enableTestWebPush(t, s)
	subscription := testPushSubscription(t)
	allowed = false
	if response := pushRequest(t, s, http.MethodPut, "team", subscription); response.Code != http.StatusForbidden || len(pushSecrets(t, s)) != 0 {
		t.Fatal("subscription created without namespace access")
	}
	allowed = true
	if response := pushRequest(t, s, http.MethodPut, "team", subscription); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	payload, _ := json.Marshal(map[string]any{"namespace": "team", "subscription": subscription})
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		request := oidcRequest(method, "/api/push-subscription", string(payload))
		request.Header.Set("X-Kelos-User", "bob")
		response := httptest.NewRecorder()
		s.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("another user modified subscription: %d %s", response.Code, response.Body.String())
		}
	}
	setPushSession(t, s, "worker", "team", "uid", "event", kelos.SessionNotificationTypeTurnCompleted)
	calls := 0
	s.webPush.send = func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if err := s.deliverWebPushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || identities[len(identities)-1] != "oidc:alice" {
		t.Fatalf("delivery did not use subscriber identity: calls=%d identities=%v", calls, identities)
	}
}

func TestWebPushContinuesAfterRejectedNotification(t *testing.T) {
	for _, rejection := range []int{http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(rejection), func(t *testing.T) {
			s := testServer(t)
			enableTestWebPush(t, s)
			saveTestPushSubscription(t, s)
			setPushSession(t, s, "first", "team", "uid-first", "event-first", kelos.SessionNotificationTypeTurnCompleted)
			setPushSession(t, s, "second", "team", "uid-second", "event-second", kelos.SessionNotificationTypeTurnCompleted)
			attempts := 0
			s.webPush.send = func(_ context.Context, payload []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				status := http.StatusCreated
				if strings.Contains(string(payload), "uid-first") {
					status = rejection
					attempts++
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			if err := s.deliverWebPushOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			var stored pushSubscription
			if err := json.Unmarshal(pushSecrets(t, s)[0].Data["subscription"], &stored); err != nil {
				t.Fatal(err)
			}
			if stored.Seen["uid-first"] != "event-first" || stored.Seen["uid-second"] != "event-second" {
				t.Fatalf("delivery cursors = %v", stored.Seen)
			}
			if err := s.deliverWebPushOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			if attempts != 1 {
				t.Fatalf("permanent rejection attempts = %d", attempts)
			}
			var session kelos.Session
			if err := s.client.Get(t.Context(), client.ObjectKey{Namespace: "team", Name: "second"}, &session); err != nil {
				t.Fatal(err)
			}
			if err := s.client.Delete(t.Context(), &session); err != nil {
				t.Fatal(err)
			}
			if err := s.deliverWebPushOnce(t.Context()); err != nil {
				t.Fatal(err)
			}
			stored = pushSubscription{}
			if err := json.Unmarshal(pushSecrets(t, s)[0].Data["subscription"], &stored); err != nil {
				t.Fatal(err)
			}
			if _, exists := stored.Seen["uid-second"]; exists {
				t.Fatal("deleted Session retained its delivery cursor")
			}
		})
	}
}

func TestWebPushSubscriptionStorageLimit(t *testing.T) {
	stored := pushSubscription{Seen: map[string]string{"session": strings.Repeat("x", maxPushSubscriptionBytes)}}
	if _, err := marshalPushSubscription(stored); !errors.Is(err, errPushSubscriptionTooLarge) {
		t.Fatalf("oversized subscription error = %v", err)
	}
}

type pushListCountingClient struct {
	client.Client
	sessionLists int
}

func (c *pushListCountingClient) List(ctx context.Context, list client.ObjectList, options ...client.ListOption) error {
	if _, sessions := list.(*kelos.SessionList); sessions {
		c.sessionLists++
	}
	return c.Client.List(ctx, list, options...)
}

func TestWebPushSharesNamespaceSnapshotAndSkipsIdleReviews(t *testing.T) {
	reviews := 0
	s := oidcTestServer(t, reviewFunc(func(_ context.Context, review *authorizationv1.SubjectAccessReview, _ metav1.CreateOptions) (*authorizationv1.SubjectAccessReview, error) {
		reviews++
		review.Status.Allowed = true
		return review, nil
	}))
	enableTestWebPush(t, s)
	counting := &pushListCountingClient{Client: s.client}
	s.client = counting
	saveTestPushSubscription(t, s)
	second := testPushSubscription(t)
	second.Endpoint += "-second"
	if response := pushRequest(t, s, http.MethodPut, "team", second); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	counting.sessionLists, reviews = 0, 0
	if err := s.deliverWebPushOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if counting.sessionLists != 1 || reviews != 0 {
		t.Fatalf("idle calls: lists=%d reviews=%d", counting.sessionLists, reviews)
	}
	setPushSession(t, s, "worker", "team", "uid", "event", kelos.SessionNotificationTypeTurnCompleted)
	sends := 0
	s.webPush.send = func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
		sends++
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if err := s.deliverWebPushOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if counting.sessionLists != 2 || reviews != 4 || sends != 2 {
		t.Fatalf("pending calls: lists=%d reviews=%d sends=%d", counting.sessionLists, reviews, sends)
	}
	if err := s.deliverWebPushOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if counting.sessionLists != 3 || reviews != 4 || sends != 2 {
		t.Fatalf("acknowledged calls: lists=%d reviews=%d sends=%d", counting.sessionLists, reviews, sends)
	}
}
