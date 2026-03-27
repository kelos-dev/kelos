package source

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// githubAPIRequestsTotal counts the total number of GitHub API requests.
	githubAPIRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kelos_github_api_requests_total",
			Help: "Total number of GitHub API requests",
		},
		[]string{"method", "status_code", "resource"},
	)

	// githubAPIRequestDuration records the duration of GitHub API requests.
	githubAPIRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "kelos_github_api_request_duration_seconds",
			Help:    "Duration of GitHub API requests in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "resource"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		githubAPIRequestsTotal,
		githubAPIRequestDuration,
	)
}

type metricsTransport struct {
	base http.RoundTripper
}

// NewMetricsTransport wraps a base RoundTripper with Prometheus metrics
// that track the total number and duration of HTTP requests.
func NewMetricsTransport(base http.RoundTripper) http.RoundTripper {
	return &metricsTransport{base: base}
}

func (t *metricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resource := classifyResource(req.URL.Path)

	resp, err := t.base.RoundTrip(req)

	duration := time.Since(start).Seconds()
	githubAPIRequestDuration.WithLabelValues(req.Method, resource).Observe(duration)

	if err != nil {
		githubAPIRequestsTotal.WithLabelValues(req.Method, "error", resource).Inc()
		return nil, err
	}

	githubAPIRequestsTotal.WithLabelValues(req.Method, strconv.Itoa(resp.StatusCode), resource).Inc()

	return resp, nil
}

// classifyResource extracts the GitHub API resource type from a URL path.
// It walks backwards through the path segments and returns the first
// non-numeric segment that matches a known resource type, skipping
// unknown segments so that sub-resources like "events" do not shadow
// their parent (e.g. "issues").
func classifyResource(urlPath string) string {
	if i := strings.Index(urlPath, "?"); i != -1 {
		urlPath = urlPath[:i]
	}
	segments := strings.Split(strings.Trim(urlPath, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		if _, err := strconv.Atoi(segments[i]); err != nil {
			switch segments[i] {
			case "issues":
				return "issues"
			case "pulls":
				return "pulls"
			case "comments":
				return "comments"
			case "reviews":
				return "reviews"
			case "collaborators", "permission":
				return "collaborators"
			}
		}
	}
	return "other"
}
