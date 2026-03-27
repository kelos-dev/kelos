package source

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func counterValue(cv *prometheus.CounterVec, labels ...string) float64 {
	m := &dto.Metric{}
	if err := cv.WithLabelValues(labels...).Write(m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}

func histogramCount(hv *prometheus.HistogramVec, labels ...string) uint64 {
	m := &dto.Metric{}
	observer, err := hv.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	if err := observer.(prometheus.Metric).Write(m); err != nil {
		return 0
	}
	return m.GetHistogram().GetSampleCount()
}

func TestMetricsTransport_CountsRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewMetricsTransport(http.DefaultTransport)}

	before := counterValue(githubAPIRequestsTotal, "GET", "200", "issues")

	resp, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	after := counterValue(githubAPIRequestsTotal, "GET", "200", "issues")
	if after-before != 1 {
		t.Fatalf("Expected counter to increment by 1, got %f", after-before)
	}
}

func TestMetricsTransport_CountsErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewMetricsTransport(http.DefaultTransport)}

	before := counterValue(githubAPIRequestsTotal, "GET", "403", "issues")

	resp, err := client.Get(srv.URL + "/repos/owner/repo/issues")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	after := counterValue(githubAPIRequestsTotal, "GET", "403", "issues")
	if after-before != 1 {
		t.Fatalf("Expected counter to increment by 1 for 403, got %f", after-before)
	}
}

func TestMetricsTransport_RecordsDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewMetricsTransport(http.DefaultTransport)}

	beforeCount := histogramCount(githubAPIRequestDuration, "GET", "pulls")

	resp, err := client.Get(srv.URL + "/repos/owner/repo/pulls")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	afterCount := histogramCount(githubAPIRequestDuration, "GET", "pulls")
	if afterCount-beforeCount != 1 {
		t.Fatalf("Expected histogram sample count to increment by 1, got %d", afterCount-beforeCount)
	}
}

func TestMetricsTransport_CountsTransportErrors(t *testing.T) {
	client := &http.Client{Transport: NewMetricsTransport(http.DefaultTransport)}

	before := counterValue(githubAPIRequestsTotal, "GET", "error", "other")

	// Request to a closed server to trigger a transport error.
	_, err := client.Get("http://127.0.0.1:1/unknown")
	if err == nil {
		t.Fatal("Expected transport error")
	}

	after := counterValue(githubAPIRequestsTotal, "GET", "error", "other")
	if after-before != 1 {
		t.Fatalf("Expected error counter to increment by 1, got %f", after-before)
	}
}

func TestMetricsTransport_TracksPOST(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewMetricsTransport(http.DefaultTransport)}

	before := counterValue(githubAPIRequestsTotal, "POST", "201", "comments")

	resp, err := client.Post(srv.URL+"/repos/owner/repo/issues/1/comments", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	after := counterValue(githubAPIRequestsTotal, "POST", "201", "comments")
	if after-before != 1 {
		t.Fatalf("Expected POST counter to increment by 1, got %f", after-before)
	}
}

func TestClassifyResource(t *testing.T) {
	tests := []struct {
		path     string
		expected string
	}{
		{"/repos/owner/repo/issues", "issues"},
		{"/repos/owner/repo/issues?per_page=100", "issues"},
		{"/repos/owner/repo/issues/1/comments", "comments"},
		{"/repos/owner/repo/issues/1/comments?per_page=100", "comments"},
		{"/repos/owner/repo/pulls", "pulls"},
		{"/repos/owner/repo/pulls/1/reviews", "reviews"},
		{"/repos/owner/repo/pulls/1/comments", "comments"},
		{"/repos/owner/repo/issues/comments/123", "comments"},
		{"/repos/owner/repo/collaborators/user/permission", "collaborators"},
		{"/repos/owner/repo/issues/123/events", "issues"},
		{"/repos/owner/repo/pulls/1/requested_reviewers", "pulls"},
		{"/unknown/path", "other"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := classifyResource(tt.path)
			if got != tt.expected {
				t.Errorf("classifyResource(%q) = %q, want %q", tt.path, got, tt.expected)
			}
		})
	}
}
