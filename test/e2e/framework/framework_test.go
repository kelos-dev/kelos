package framework

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestGetJobLogs(t *testing.T) {
	failed := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-attempt"},
		Status:     corev1.PodStatus{Phase: corev1.PodFailed},
	}
	succeeded := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "successful-attempt"},
		Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	running := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "running-attempt"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	for _, tt := range []struct {
		name string
		pods []corev1.Pod
		want string
	}{
		{name: "successful retry after failed attempt", pods: []corev1.Pod{failed, succeeded}, want: succeeded.Name},
		{name: "successful retry before failed attempt", pods: []corev1.Pod{succeeded, failed}, want: succeeded.Name},
		{name: "failed job diagnostics", pods: []corev1.Pod{failed}, want: failed.Name},
		{name: "running job", pods: []corev1.Pod{running}, want: running.Name},
	} {
		t.Run(tt.name, func(t *testing.T) {
			gomega.RegisterTestingT(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/namespaces/test/pods":
					if got := r.URL.Query().Get("labelSelector"); got != "job-name=task" {
						t.Errorf("pod label selector = %q, want job-name=task", got)
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(corev1.PodList{Items: tt.pods})
				default:
					fmt.Fprint(w, r.URL.Path)
				}
			}))
			defer server.Close()
			cs, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			f := Framework{Namespace: "test", Clientset: cs}
			if got, want := f.GetJobLogs("task"), "/api/v1/namespaces/test/pods/"+tt.want+"/log"; got != want {
				t.Fatalf("GetJobLogs() = %q, want %q", got, want)
			}
		})
	}
}
