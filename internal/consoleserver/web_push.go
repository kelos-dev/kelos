package consoleserver

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	pushSubscriptionLabel    = "console.kelos.dev/push-subscription"
	maxPushSubscriptionBytes = 256 * 1024
)

var errPushSubscriptionTooLarge = errors.New("Web Push subscription exceeds the 256 KiB storage limit")

// WebPushConfig enables background notifications using a persistent VAPID key pair.
// Namespace holds subscription Secrets and the delivery leader's Lease.
type WebPushConfig struct {
	Namespace  string
	Subject    string
	PublicKey  string
	PrivateKey string
	Logger     *slog.Logger
}

type webPushDelivery struct {
	config WebPushConfig
	send   func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error)
	client *http.Client
}

type pushSubscription struct {
	Subscription webpush.Subscription `json:"subscription"`
	Namespace    string               `json:"namespace"`
	Username     string               `json:"username"`
	Groups       []string             `json:"groups"`
	Owner        string               `json:"owner"`
	ExpiresAt    time.Time            `json:"expiresAt"`
	Seen         map[string]string    `json:"seen"`
}

func marshalPushSubscription(stored pushSubscription) ([]byte, error) {
	data, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if len(data) > maxPushSubscriptionBytes {
		return nil, errPushSubscriptionTooLarge
	}
	return data, nil
}

func newWebPush(config WebPushConfig, httpClient *http.Client) (*webPushDelivery, error) {
	if config.Namespace == "" || config.Logger == nil {
		return nil, errors.New("Web Push storage namespace and logger are required")
	}
	subject, err := url.Parse(config.Subject)
	if err != nil || (subject.Scheme != "mailto" && subject.Scheme != "https") ||
		(subject.Scheme == "mailto" && subject.Opaque == "") || (subject.Scheme == "https" && subject.Hostname() == "") {
		return nil, errors.New("Web Push subject must be a mailto: or HTTPS contact URL")
	}
	privateBytes, err := base64.RawURLEncoding.DecodeString(config.PrivateKey)
	if err != nil {
		return nil, errors.New("invalid Web Push private key encoding")
	}
	privateKey, err := ecdh.P256().NewPrivateKey(privateBytes)
	if err != nil {
		return nil, errors.New("invalid Web Push private key")
	}
	publicBytes, err := base64.RawURLEncoding.DecodeString(config.PublicKey)
	if err != nil || !bytes.Equal(privateKey.PublicKey().Bytes(), publicBytes) {
		return nil, errors.New("Web Push public key does not match the private key")
	}
	pushClient := *httpClient
	pushClient.Timeout = 10 * time.Second
	// Subscription endpoints must not redirect requests to arbitrary destinations.
	pushClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &webPushDelivery{config: config, send: webpush.SendNotificationWithContext, client: &pushClient}, nil
}

func validatePushSubscription(subscription webpush.Subscription) error {
	endpoint, err := url.Parse(subscription.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Port() != "" || endpoint.Fragment != "" || len(subscription.Endpoint) > 4096 {
		return errors.New("invalid Web Push endpoint")
	}
	// Only browser-operated push services may receive server-side requests.
	host := endpoint.Hostname()
	if host != "web.push.apple.com" && host != "fcm.googleapis.com" && host != "updates.push.services.mozilla.com" &&
		!strings.HasSuffix(host, ".notify.windows.com") {
		return errors.New("unsupported Web Push service")
	}
	auth, err := base64.RawURLEncoding.DecodeString(subscription.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("invalid Web Push authentication key")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(subscription.Keys.P256dh)
	if err != nil {
		return errors.New("invalid Web Push encryption key")
	}
	if _, err := ecdh.P256().NewPublicKey(publicKey); err != nil {
		return errors.New("invalid Web Push encryption key")
	}
	return nil
}

func pushSecretName(endpoint string) string {
	digest := sha256.Sum256([]byte(endpoint))
	return fmt.Sprintf("kelos-console-push-%x", digest[:20])
}

func (s *Server) pushOwner(identity principal) string {
	if s.proxyAuth == nil {
		digest := sha256.Sum256([]byte(s.cookieValue))
		return AuthModeStaticToken + ":" + base64.RawURLEncoding.EncodeToString(digest[:])
	}
	return s.authMode + ":" + identity.username
}

func (s *Server) webPushSubscription(writer http.ResponseWriter, request *http.Request) {
	if s.webPush == nil {
		writeError(writer, http.StatusServiceUnavailable, "Web Push is not configured")
		return
	}
	var payload struct {
		Subscription webpush.Subscription `json:"subscription"`
		Namespace    string               `json:"namespace"`
	}
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	if err := validatePushSubscription(payload.Subscription); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	identity, _ := request.Context().Value(principalKey{}).(principal)
	key := client.ObjectKey{Namespace: s.webPush.config.Namespace, Name: pushSecretName(payload.Subscription.Endpoint)}
	if request.Method == http.MethodDelete {
		var secret corev1.Secret
		if err := s.client.Get(request.Context(), key, &secret); apierrors.IsNotFound(err) {
			writer.WriteHeader(http.StatusNoContent)
			return
		} else if err != nil {
			writeKubernetesError(writer, "getting Web Push subscription", err)
			return
		}
		var stored pushSubscription
		if err := json.Unmarshal(secret.Data["subscription"], &stored); err != nil || stored.Owner != s.pushOwner(identity) {
			writeError(writer, http.StatusForbidden, "Web Push subscription belongs to another user")
			return
		}
		if err := s.client.Delete(request.Context(), &secret, client.Preconditions{ResourceVersion: &secret.ResourceVersion}); err != nil && !apierrors.IsNotFound(err) {
			writeKubernetesError(writer, "deleting Web Push subscription", err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	if payload.Namespace == "" {
		writeError(writer, http.StatusBadRequest, "namespace is required")
		return
	}
	if !s.requireAccess(writer, request, access("list", "sessions", payload.Namespace, "")) {
		return
	}
	var sessions kelos.SessionList
	if err := s.client.List(request.Context(), &sessions, client.InNamespace(payload.Namespace)); err != nil {
		writeKubernetesError(writer, "listing Sessions for Web Push", err)
		return
	}
	err := retry.OnError(retry.DefaultRetry, func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }, func() error {
		secret := corev1.Secret{}
		err := s.client.Get(request.Context(), key, &secret)
		create := apierrors.IsNotFound(err)
		if err != nil && !create {
			return err
		}
		stored := pushSubscription{}
		if !create {
			if err := json.Unmarshal(secret.Data["subscription"], &stored); err != nil {
				return err
			}
			if stored.Owner != s.pushOwner(identity) {
				return apierrors.NewForbidden(corev1.Resource("secrets"), key.Name, errors.New("subscription belongs to another user; unsubscribe this device first"))
			}
		}
		if create || stored.Namespace != payload.Namespace || time.Now().After(stored.ExpiresAt) {
			stored.Seen = map[string]string{}
			for _, session := range sessions.Items {
				if notification := session.Status.LastNotification; notification != nil {
					stored.Seen[string(session.UID)] = notification.ID
				}
			}
		}
		stored.Subscription = payload.Subscription
		stored.Namespace = payload.Namespace
		stored.Username = identity.username
		stored.Groups = identity.groups
		stored.Owner = s.pushOwner(identity)
		lifetime := 7 * 24 * time.Hour
		if s.proxyAuth != nil {
			lifetime = 8 * time.Hour
		}
		stored.ExpiresAt = time.Now().Add(lifetime)
		data, err := marshalPushSubscription(stored)
		if err != nil {
			return err
		}
		secret.Namespace, secret.Name = key.Namespace, key.Name
		secret.Labels = map[string]string{pushSubscriptionLabel: "true"}
		secret.Data = map[string][]byte{"subscription": data}
		if create {
			return s.client.Create(request.Context(), &secret)
		}
		return s.client.Update(request.Context(), &secret)
	})
	if err != nil {
		if errors.Is(err, errPushSubscriptionTooLarge) {
			writeError(writer, http.StatusRequestEntityTooLarge, err.Error())
		} else if apierrors.IsForbidden(err) {
			writeError(writer, http.StatusForbidden, err.Error())
		} else {
			writeKubernetesError(writer, "saving Web Push subscription", err)
		}
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"subscribed": true})
}

// RunWebPush delivers notifications independently of browser connections. Only
// the elected replica sends; retained cursors survive leader changes and restarts.
func (s *Server) RunWebPush(ctx context.Context) error {
	if s.webPush == nil {
		return nil
	}
	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{Namespace: s.webPush.config.Namespace, Name: "kelos-console-web-push"},
			Client:    s.clientset.CoordinationV1(), LockConfig: resourcelock.ResourceLockConfig{Identity: string(uuid.NewUUID())},
		},
		LeaseDuration: 15 * time.Second, RenewDeadline: 10 * time.Second, RetryPeriod: 2 * time.Second,
		ReleaseOnCancel: true,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: s.deliverWebPush,
			OnStoppedLeading: func() {},
		},
	})
	if err != nil {
		return err
	}
	elector.Run(ctx)
	if ctx.Err() == nil {
		return errors.New("Web Push delivery leadership lost")
	}
	return nil
}

func (s *Server) deliverWebPush(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.deliverWebPushOnce(ctx); err != nil && ctx.Err() == nil {
			s.webPush.config.Logger.ErrorContext(ctx, "Web Push delivery failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type pushNamespaceSnapshot struct {
	sessions []kelos.Session
	err      error
}

func (s *Server) deliverWebPushOnce(ctx context.Context) error {
	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets, client.InNamespace(s.webPush.config.Namespace), client.MatchingLabels{pushSubscriptionLabel: "true"}); err != nil {
		return err
	}
	snapshots := make(map[string]pushNamespaceSnapshot)
	for i := range secrets.Items {
		secret := &secrets.Items[i]
		if err := s.deliverSubscription(ctx, secret, snapshots); err != nil && ctx.Err() == nil {
			s.webPush.config.Logger.ErrorContext(ctx, "Web Push subscription delivery failed", "subscription", secret.Name, "error", err)
		}
	}
	return nil
}

func (s *Server) deliverSubscription(ctx context.Context, secret *corev1.Secret, snapshots map[string]pushNamespaceSnapshot) error {
	var stored pushSubscription
	if err := json.Unmarshal(secret.Data["subscription"], &stored); err != nil {
		return err
	}
	if err := validatePushSubscription(stored.Subscription); err != nil {
		return err
	}
	identity := principal{username: stored.Username, groups: stored.Groups}
	if time.Now().After(stored.ExpiresAt) || stored.Owner != s.pushOwner(identity) {
		return client.IgnoreNotFound(s.client.Delete(ctx, secret, client.Preconditions{ResourceVersion: &secret.ResourceVersion}))
	}
	snapshot, cached := snapshots[stored.Namespace]
	if !cached {
		var list kelos.SessionList
		snapshot.err = s.client.List(ctx, &list, client.InNamespace(stored.Namespace))
		snapshot.sessions = list.Items
		snapshots[stored.Namespace] = snapshot
	}
	if snapshot.err != nil {
		return snapshot.err
	}
	sessions := snapshot.sessions
	current := make(map[string]bool, len(sessions))
	pending := false
	for _, session := range sessions {
		current[string(session.UID)] = true
		notification := session.Status.LastNotification
		pending = pending || (notification != nil && stored.Seen[string(session.UID)] != notification.ID && !summarize(&session).Resetting)
	}
	dirty := false
	for id := range stored.Seen {
		if !current[id] {
			delete(stored.Seen, id)
			dirty = true
		}
	}
	if stored.Seen == nil {
		stored.Seen = map[string]string{}
	}
	request, _ := http.NewRequestWithContext(context.WithValue(ctx, principalKey{}, identity), http.MethodGet, "/", nil)
	if pending {
		allowed, err := s.allowed(request, access("list", "sessions", stored.Namespace, ""))
		if err != nil || !allowed {
			return err
		}
	}
	for _, session := range sessions {
		id := string(session.UID)
		notification := session.Status.LastNotification
		if notification == nil || stored.Seen[id] == notification.ID || summarize(&session).Resetting {
			continue
		}
		allowed, err := s.allowed(request, access("get", "sessions", session.Namespace, session.Name))
		if err != nil {
			return err
		}
		if !allowed {
			continue
		}
		titles := map[kelos.SessionNotificationType]string{
			kelos.SessionNotificationTypeInputRequested: "Input needed",
			kelos.SessionNotificationTypeTurnCompleted:  "Work completed",
			kelos.SessionNotificationTypeTurnFailed:     "Work failed",
		}
		title, ok := titles[notification.Type]
		if !ok {
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"title": title + " · " + sessionDisplayName(&session),
			"options": map[string]any{
				"body":      session.Namespace + " · Open the conversation for details.",
				"tag":       "kelos-session-" + session.Namespace + "/" + session.Name + "/" + id,
				"timestamp": notification.EventTime.UnixMilli(),
				"renotify":  true,
				"data":      map[string]any{"id": notification.ID, "session": map[string]string{"name": session.Name, "namespace": session.Namespace, "uid": id}},
			},
		})
		if err != nil {
			return err
		}
		topic := sha256.Sum256([]byte(id))
		// The push library adds the mailto: scheme to email contacts.
		response, err := s.webPush.send(ctx, payload, &stored.Subscription, &webpush.Options{
			HTTPClient: s.webPush.client, Subscriber: strings.TrimPrefix(s.webPush.config.Subject, "mailto:"),
			VAPIDPublicKey: s.webPush.config.PublicKey, VAPIDPrivateKey: s.webPush.config.PrivateKey,
			TTL: 3600, Urgency: webpush.UrgencyHigh, Topic: base64.RawURLEncoding.EncodeToString(topic[:24]),
		})
		if err != nil {
			var urlError *url.Error
			if errors.As(err, &urlError) {
				err = urlError.Err
			}
			s.webPush.config.Logger.ErrorContext(ctx, "Web Push notification failed", "namespace", session.Namespace, "session", session.Name, "error", err)
			continue
		}
		response.Body.Close()
		if response.StatusCode == http.StatusGone || response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			s.webPush.config.Logger.ErrorContext(ctx, "Web Push subscription rejected", "subscription", secret.Name, "statusCode", response.StatusCode)
			return client.IgnoreNotFound(s.client.Delete(ctx, secret, client.Preconditions{ResourceVersion: &secret.ResourceVersion}))
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			s.webPush.config.Logger.ErrorContext(ctx, "Web Push notification rejected", "namespace", session.Namespace, "session", session.Name, "statusCode", response.StatusCode)
			retryable := response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError
			if retryable {
				continue
			}
		}
		stored.Seen[id] = notification.ID
		// Save accepted and permanently rejected messages. A crash between
		// acceptance and this write can deliver the same event again.
		data, err := marshalPushSubscription(stored)
		if err != nil {
			return err
		}
		secret.Data["subscription"] = data
		if err := s.client.Update(ctx, secret); err != nil {
			return err
		}
	}
	if dirty {
		data, err := marshalPushSubscription(stored)
		if err != nil {
			return err
		}
		secret.Data["subscription"] = data
		return s.client.Update(ctx, secret)
	}
	return nil
}
