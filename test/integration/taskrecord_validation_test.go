package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("TaskRecord API validation", func() {
	const ns = "test-taskrecord-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	It("accepts a well-formed TaskRecord", func() {
		completionTime := metav1.Now()
		record := &kelos.TaskRecord{
			ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: ns},
			Spec: kelos.TaskRecordSpec{
				TaskRef:        kelos.TaskReference{Name: "task-1", UID: types.UID("task-uid")},
				Phase:          kelos.TaskPhaseSucceeded,
				CompletionTime: &completionTime,
			},
		}
		Expect(k8sClient.Create(ctx, record)).Should(Succeed())
	})

	It("rejects a spec-less TaskRecord", func() {
		record := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "kelos.dev/v1alpha2",
				"kind":       "TaskRecord",
				"metadata": map[string]interface{}{
					"name":      "no-spec",
					"namespace": ns,
				},
			},
		}
		record.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "TaskRecord"})
		Expect(k8sClient.Create(ctx, record)).ShouldNot(Succeed())
	})

	It("rejects a TaskRecord without completionTime", func() {
		record := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "kelos.dev/v1alpha2",
				"kind":       "TaskRecord",
				"metadata": map[string]interface{}{
					"name":      "no-completion-time",
					"namespace": ns,
				},
				"spec": map[string]interface{}{
					"taskRef": map[string]interface{}{
						"name": "task-1",
						"uid":  "task-uid",
					},
					"phase": "Succeeded",
				},
			},
		}
		record.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "TaskRecord"})
		Expect(k8sClient.Create(ctx, record)).ShouldNot(Succeed())
	})
})
