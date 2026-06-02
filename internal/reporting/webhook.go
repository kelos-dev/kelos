package reporting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// AnnotationOnCompletion stores the serialized onCompletion hooks config
	// so the reporting loop can dispatch without looking up the TaskSpawner.
	AnnotationOnCompletion = "kelos.dev/on-completion"

	// AnnotationWebhookReportPhase records the last Task phase that was
	// reported via webhook hooks, preventing duplicate deliveries.
	AnnotationWebhookReportPhase = "kelos.dev/webhook-report-phase"
)

// WebhookPayload is the JSON body sent to onCompletion webhook endpoints.
type WebhookPayload struct {
	Task           string            `json:"task"`
	Namespace      string            `json:"namespace"`
	Spawner        string            `json:"spawner,omitempty"`
	Phase          string            `json:"phase"`
	Message        string            `json:"message,omitempty"`
	AgentType      string            `json:"agentType,omitempty"`
	Model          string            `json:"model,omitempty"`
	StartTime      *time.Time        `json:"startTime,omitempty"`
	CompletionTime *time.Time        `json:"completionTime,omitempty"`
	Outputs        []string          `json:"outputs,omitempty"`
	Results        map[string]string `json:"results,omitempty"`
}

// WebhookReporter dispatches onCompletion webhook notifications for Tasks.
type WebhookReporter struct {
	Client     client.Client
	HTTPClient *http.Client
	// SecretReader resolves secret values. When nil, secretRef is ignored.
	SecretReader SecretReader
	// skipURLValidation disables SSRF checks (for testing only).
	skipURLValidation bool
	// safeClient is lazily initialized by httpClient().
	safeClient *http.Client
}

// SecretReader reads headers from a named Secret in a namespace.
type SecretReader interface {
	// ReadHeaders returns all key-value pairs from the named Secret.
	// Each key is used as an HTTP header name and the value as its value.
	ReadHeaders(ctx context.Context, namespace, name string) (map[string]string, error)
}

// ReportWebhooks checks whether the task has onCompletion hooks configured
// and dispatches webhooks for terminal phases that haven't been reported yet.
func (wr *WebhookReporter) ReportWebhooks(ctx context.Context, task *kelosv1alpha1.Task) error {
	log := ctrl.Log.WithName("webhook-reporter")

	annotations := task.Annotations
	if annotations == nil {
		return nil
	}

	hooksJSON := annotations[AnnotationOnCompletion]
	if hooksJSON == "" {
		return nil
	}

	// Only fire for terminal phases.
	if task.Status.Phase != kelosv1alpha1.TaskPhaseSucceeded && task.Status.Phase != kelosv1alpha1.TaskPhaseFailed {
		return nil
	}

	// Skip if already reported this phase.
	if annotations[AnnotationWebhookReportPhase] == string(task.Status.Phase) {
		return nil
	}

	var hooks []kelosv1alpha1.NotificationHook
	if err := json.Unmarshal([]byte(hooksJSON), &hooks); err != nil {
		return fmt.Errorf("parsing on-completion hooks annotation: %w", err)
	}

	payload := buildWebhookPayload(task)

	var lastErr error
	dispatched := 0
	for _, hook := range hooks {
		if !phaseMatches(hook.Phases, task.Status.Phase) {
			continue
		}

		dispatched++
		if err := wr.sendWebhook(ctx, task.Namespace, hook, payload); err != nil {
			log.Error(err, "Sending webhook", "task", task.Name, "hook", hook.Name)
			lastErr = err
			continue
		}
		log.Info("Sent webhook notification", "task", task.Name, "hook", hook.Name, "phase", task.Status.Phase)
	}

	// Persist the annotation to prevent re-evaluation on future cycles.
	// This is done even on partial failure to avoid duplicate deliveries
	// to hooks that already succeeded.
	if err := wr.persistWebhookReportPhase(ctx, task, string(task.Status.Phase)); err != nil {
		return err
	}

	return lastErr
}

func (wr *WebhookReporter) sendWebhook(ctx context.Context, namespace string, hook kelosv1alpha1.NotificationHook, payload WebhookPayload) error {
	if !wr.skipURLValidation {
		if err := validateWebhookURL(hook.Webhook.URL); err != nil {
			return err
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hook.Webhook.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if hook.Webhook.SecretRef != nil && wr.SecretReader != nil {
		headers, err := wr.SecretReader.ReadHeaders(ctx, namespace, hook.Webhook.SecretRef.Name)
		if err != nil {
			return fmt.Errorf("reading webhook secret %q: %w", hook.Webhook.SecretRef.Name, err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}

	httpClient := wr.httpClient()

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook to hook %q: %w", hook.Name, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %q returned status %d", hook.Name, resp.StatusCode)
	}

	return nil
}

// httpClient returns an HTTP client with SSRF-safe transport and redirect
// policy. The result is cached after first construction.
func (wr *WebhookReporter) httpClient() *http.Client {
	if wr.skipURLValidation && wr.HTTPClient != nil {
		return wr.HTTPClient
	}
	if wr.safeClient != nil {
		return wr.safeClient
	}
	if wr.HTTPClient == nil {
		wr.safeClient = defaultWebhookHTTPClient
	} else {
		clone := *wr.HTTPClient
		clone.CheckRedirect = ssrfCheckRedirect
		clone.Transport = ssrfSafeTransport(clone.Transport)
		wr.safeClient = &clone
	}
	return wr.safeClient
}

var defaultWebhookHTTPClient = &http.Client{
	Timeout:       10 * time.Second,
	Transport:     ssrfSafeTransport(nil),
	CheckRedirect: ssrfCheckRedirect,
}

func ssrfSafeTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	t, ok := base.(*http.Transport)
	if !ok {
		ctrl.Log.WithName("webhook-reporter").Info("Custom transport is not *http.Transport, using default transport for SSRF protection")
		t = http.DefaultTransport.(*http.Transport)
	}
	clone := t.Clone()
	clone.DialContext = ssrfDialContext
	return clone
}

func ssrfDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses found for host %q", host)
	}
	for _, ip := range ips {
		if isPrivateIP(ip.IP) {
			return nil, fmt.Errorf("webhook URL must not target private/internal addresses")
		}
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

func ssrfCheckRedirect(req *http.Request, via []*http.Request) error {
	if err := validateWebhookURL(req.URL.String()); err != nil {
		return err
	}
	if len(via) >= 10 {
		return fmt.Errorf("too many redirects")
	}
	return nil
}

// validateWebhookURL rejects URLs that are not HTTPS or contain embedded
// credentials (userinfo). IP-level SSRF checks are enforced at dial time
// via the transport's DialContext.
func validateWebhookURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("webhook URL must use HTTPS")
	}
	if u.User != nil {
		return fmt.Errorf("webhook URL must not contain embedded credentials")
	}
	return nil
}

var privateRanges = []net.IPNet{
	{IP: net.IPv4(0, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(10, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(127, 0, 0, 0), Mask: net.CIDRMask(8, 32)},
	{IP: net.IPv4(169, 254, 0, 0), Mask: net.CIDRMask(16, 32)},
	{IP: net.IPv4(172, 16, 0, 0), Mask: net.CIDRMask(12, 32)},
	{IP: net.IPv4(192, 168, 0, 0), Mask: net.CIDRMask(16, 32)},
	{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
	{IP: net.ParseIP("fe80::"), Mask: net.CIDRMask(10, 128)},
	{IP: net.ParseIP("fc00::"), Mask: net.CIDRMask(7, 128)},
}

func isPrivateIP(ip net.IP) bool {
	for _, r := range privateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

func (wr *WebhookReporter) persistWebhookReportPhase(ctx context.Context, task *kelosv1alpha1.Task, phase string) error {
	return persistAnnotationRetry(ctx, wr.Client, task, map[string]string{
		AnnotationWebhookReportPhase: phase,
	})
}

func buildWebhookPayload(task *kelosv1alpha1.Task) WebhookPayload {
	p := WebhookPayload{
		Task:      task.Name,
		Namespace: task.Namespace,
		Spawner:   task.Labels["kelos.dev/taskspawner"],
		Phase:     string(task.Status.Phase),
		Message:   task.Status.Message,
		AgentType: task.Spec.Type,
		Model:     task.Spec.Model,
		Outputs:   task.Status.Outputs,
		Results:   task.Status.Results,
	}
	if task.Status.StartTime != nil {
		t := task.Status.StartTime.Time
		p.StartTime = &t
	}
	if task.Status.CompletionTime != nil {
		t := task.Status.CompletionTime.Time
		p.CompletionTime = &t
	}
	return p
}

func phaseMatches(configured []kelosv1alpha1.TerminalTaskPhase, actual kelosv1alpha1.TaskPhase) bool {
	if len(configured) == 0 {
		return true
	}
	for _, p := range configured {
		if kelosv1alpha1.TaskPhase(p) == actual {
			return true
		}
	}
	return false
}

// persistAnnotationRetry updates annotations on a Task using a merge patch
// with retry on conflict, avoiding full-object writes that could clobber
// concurrent changes from other controllers.
func persistAnnotationRetry(ctx context.Context, cl client.Client, task *kelosv1alpha1.Task, annotations map[string]string) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.Task
		if err := cl.Get(ctx, client.ObjectKeyFromObject(task), &current); err != nil {
			return err
		}
		base := current.DeepCopy()
		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		for k, v := range annotations {
			current.Annotations[k] = v
		}
		if err := cl.Patch(ctx, &current, client.MergeFrom(base)); err != nil {
			return err
		}
		task.Annotations = current.Annotations
		return nil
	}); err != nil {
		return fmt.Errorf("persisting webhook annotations on task %s: %w", task.Name, err)
	}
	return nil
}
