package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/scoring"
)

func taskScoreUnstructured(name, namespace string, spec map[string]interface{}) *unstructured.Unstructured {
	score := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kelos.dev/v1alpha2",
			"kind":       "TaskScore",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": spec,
		},
	}
	score.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "TaskScore"})
	return score
}

var _ = Describe("TaskScore API validation", func() {
	const ns = "test-taskscore-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	It("accepts a well-formed TaskScore", func() {
		observedAt := metav1.Now()
		score := &kelos.TaskScore{
			ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: ns},
			Spec: kelos.TaskScoreSpec{
				TaskRef: kelos.TaskReference{Name: "task-1", UID: types.UID("task-uid")},
				Verdict: kelos.ScoreVerdictPositive,
				Source: kelos.ScoreSource{
					Type:   kelos.ScoreSourceSlackReaction,
					Actor:  "U777",
					Signal: "+1",
					URI:    scoring.SlackURI("C0123456789", "1712345678.123456"),
				},
				ObservedAt: &observedAt,
			},
		}
		Expect(k8sClient.Create(ctx, score)).Should(Succeed())
	})

	It("rejects a spec-less TaskScore", func() {
		score := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "kelos.dev/v1alpha2",
				"kind":       "TaskScore",
				"metadata": map[string]interface{}{
					"name":      "no-spec",
					"namespace": ns,
				},
			},
		}
		score.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "TaskScore"})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects a TaskScore without taskRef.uid", func() {
		score := taskScoreUnstructured("no-task-uid", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1"},
			"verdict": "Positive",
			// signal is supplied so this case fails only on the missing uid.
			"source": map[string]interface{}{"type": "SlackReaction", "signal": "+1"},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	// An empty UID passes the required-key check but leaves the score with nothing
	// tying it to a Task, so it could never be summarized, adopted, or collected.
	It("rejects an empty taskRef.uid", func() {
		score := taskScoreUnstructured("empty-task-uid", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": ""},
			"verdict": "Positive",
			"source":  map[string]interface{}{"type": "SlackReaction", "signal": "+1"},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects a TaskScore without source.signal", func() {
		score := taskScoreUnstructured("no-signal", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source":  map[string]interface{}{"type": "SlackReaction"},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects an empty source.signal", func() {
		score := taskScoreUnstructured("empty-signal", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source":  map[string]interface{}{"type": "SlackReaction", "signal": ""},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects an empty source.actor", func() {
		score := taskScoreUnstructured("empty-actor", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source":  map[string]interface{}{"type": "SlackReaction", "signal": "+1", "actor": ""},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	// An absent actor is legitimate: it means the source has no distinct actor.
	It("accepts an absent source.actor", func() {
		score := taskScoreUnstructured("absent-actor", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source":  map[string]interface{}{"type": "SlackReaction", "signal": "merged"},
		})
		Expect(k8sClient.Create(ctx, score)).Should(Succeed())
	})

	It("rejects an unknown verdict", func() {
		score := taskScoreUnstructured("bad-verdict", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Mixed",
			"source":  map[string]interface{}{"type": "SlackReaction", "signal": "+1"},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects an unknown source type", func() {
		score := taskScoreUnstructured("bad-source-type", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source":  map[string]interface{}{"type": "CarrierPigeon", "signal": "+1"},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects a TaskScore with no source type", func() {
		score := taskScoreUnstructured("no-source-type", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source":  map[string]interface{}{"actor": "U777", "signal": "+1"},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects an over-long source actor", func() {
		score := taskScoreUnstructured("long-actor", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source": map[string]interface{}{
				"type":   "SlackReaction",
				"signal": "+1",
				"actor":  stringOfLength(257),
			},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects an over-long source uri", func() {
		score := taskScoreUnstructured("long-uri", ns, map[string]interface{}{
			"taskRef": map[string]interface{}{"name": "task-1", "uid": "task-uid"},
			"verdict": "Positive",
			"source": map[string]interface{}{
				"type":   "SlackReaction",
				"signal": "+1",
				"uri":    stringOfLength(1025),
			},
		})
		Expect(k8sClient.Create(ctx, score)).ShouldNot(Succeed())
	})

	It("rejects a spec change after creation", func() {
		score := &kelos.TaskScore{
			ObjectMeta: metav1.ObjectMeta{Name: "immutable", Namespace: ns},
			Spec: kelos.TaskScoreSpec{
				TaskRef: kelos.TaskReference{Name: "task-1", UID: types.UID("task-uid")},
				Verdict: kelos.ScoreVerdictPositive,
				Source:  kelos.ScoreSource{Type: kelos.ScoreSourceSlackReaction, Actor: "U777", Signal: "+1"},
			},
		}
		Expect(k8sClient.Create(ctx, score)).Should(Succeed())

		score.Spec.Verdict = kelos.ScoreVerdictNegative
		Expect(k8sClient.Update(ctx, score)).ShouldNot(Succeed())
	})

	// A score is adopted by its TaskRecord after creation, which is a
	// metadata-only write against an immutable spec.
	It("allows a metadata-only update", func() {
		score := &kelos.TaskScore{
			ObjectMeta: metav1.ObjectMeta{Name: "adoptable", Namespace: ns},
			Spec: kelos.TaskScoreSpec{
				TaskRef: kelos.TaskReference{Name: "task-1", UID: types.UID("task-uid")},
				Verdict: kelos.ScoreVerdictPositive,
				Source:  kelos.ScoreSource{Type: kelos.ScoreSourceSlackReaction, Actor: "U777", Signal: "+1"},
			},
		}
		Expect(k8sClient.Create(ctx, score)).Should(Succeed())

		score.Labels = map[string]string{scoring.LabelTaskUID: "task-uid"}
		Expect(k8sClient.Update(ctx, score)).Should(Succeed())
	})
})

var _ = Describe("TaskRecord score status", func() {
	const ns = "test-taskrecord-scores"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	newRecord := func(name string) *kelos.TaskRecord {
		completionTime := metav1.Now()
		return &kelos.TaskRecord{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: kelos.TaskRecordSpec{
				TaskRef:        kelos.TaskReference{Name: "task-1", UID: types.UID(name)},
				Phase:          kelos.TaskPhaseSucceeded,
				CompletionTime: &completionTime,
			},
		}
	}

	It("accepts a score summary written to the status subresource", func() {
		record := newRecord("with-scores")
		Expect(k8sClient.Create(ctx, record)).Should(Succeed())

		observedAt := metav1.Now()
		record.Status.Scores = &kelos.TaskScoreSummary{
			Positive:       2,
			Negative:       1,
			Total:          3,
			LastObservedAt: &observedAt,
		}
		Expect(k8sClient.Status().Update(ctx, record)).Should(Succeed())

		var fetched kelos.TaskRecord
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(record), &fetched)).Should(Succeed())
		Expect(fetched.Status.Scores).ShouldNot(BeNil())
		Expect(fetched.Status.Scores.Total).Should(Equal(int32(3)))
	})

	// The reporter labels an existing record with the result-message identity.
	// That is a metadata-only write against a spec carrying an immutability CEL
	// rule, which only a real API server evaluates.
	It("allows labelling an existing record without touching its spec", func() {
		record := newRecord("labelled")
		Expect(k8sClient.Create(ctx, record)).Should(Succeed())

		record.Labels = map[string]string{
			scoring.LabelSlackResultChannel: "C0123456789",
			scoring.LabelSlackResultTS:      "1712345678.123456",
		}
		Expect(k8sClient.Update(ctx, record)).Should(Succeed())
	})

	It("still rejects a spec change on an existing record", func() {
		record := newRecord("immutable-spec")
		Expect(k8sClient.Create(ctx, record)).Should(Succeed())

		record.Spec.Phase = kelos.TaskPhaseFailed
		Expect(k8sClient.Update(ctx, record)).ShouldNot(Succeed())
	})
})

func stringOfLength(n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = 'a'
	}
	return string(out)
}

var _ = Describe("TaskSpawner Slack scoring validation", func() {
	const ns = "test-slack-scoring-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	// A minimal spawner that satisfies the taskTemplate CEL rules, so a rejection
	// can only come from the scoring block under test.
	scoringSpawner := func(name string, reactions []interface{}) *unstructured.Unstructured {
		spawner := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "kelos.dev/v1alpha2",
				"kind":       "TaskSpawner",
				"metadata": map[string]interface{}{
					"name":      name,
					"namespace": ns,
				},
				"spec": map[string]interface{}{
					"when": map[string]interface{}{
						"slack": map[string]interface{}{
							"scoring": map[string]interface{}{
								"reactions": reactions,
							},
						},
					},
					"taskTemplate": map[string]interface{}{
						"promptTemplate": "do the thing",
						"worker": map[string]interface{}{
							"type":        "claude-code",
							"credentials": map[string]interface{}{"type": "none"},
						},
					},
				},
			},
		}
		spawner.SetGroupVersionKind(schema.GroupVersionKind{Group: "kelos.dev", Version: "v1alpha2", Kind: "TaskSpawner"})
		return spawner
	}

	It("accepts a well-formed reaction mapping", func() {
		spawner := scoringSpawner("valid-scoring", []interface{}{
			map[string]interface{}{"name": "+1", "verdict": "Positive"},
			map[string]interface{}{"name": "white_check_mark", "verdict": "Positive"},
			map[string]interface{}{"name": "-1", "verdict": "Negative"},
		})
		Expect(k8sClient.Create(ctx, spawner)).Should(Succeed())
	})

	It("rejects a colon-wrapped reaction name", func() {
		spawner := scoringSpawner("colon-wrapped", []interface{}{
			map[string]interface{}{"name": ":+1:", "verdict": "Positive"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	// Two entries differing only by modifier would name the same reaction while
	// mapping it to different verdicts, so a modifier is rejected outright.
	It("rejects a skin-tone-qualified reaction name", func() {
		spawner := scoringSpawner("skin-tone", []interface{}{
			map[string]interface{}{"name": "+1::skin-tone-4", "verdict": "Positive"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("rejects an uppercase reaction name", func() {
		spawner := scoringSpawner("uppercase-name", []interface{}{
			map[string]interface{}{"name": "White_Check_Mark", "verdict": "Positive"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("rejects a reaction name containing a space", func() {
		spawner := scoringSpawner("spaced-name", []interface{}{
			map[string]interface{}{"name": "thumbs up", "verdict": "Positive"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("rejects a literal emoji character as a reaction name", func() {
		spawner := scoringSpawner("emoji-char", []interface{}{
			map[string]interface{}{"name": "👍", "verdict": "Positive"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("accepts the +1 and -1 aliases", func() {
		spawner := scoringSpawner("plus-minus", []interface{}{
			map[string]interface{}{"name": "+1", "verdict": "Positive"},
			map[string]interface{}{"name": "-1", "verdict": "Negative"},
		})
		Expect(k8sClient.Create(ctx, spawner)).Should(Succeed())
	})

	It("rejects an empty reaction name", func() {
		spawner := scoringSpawner("empty-name", []interface{}{
			map[string]interface{}{"name": "", "verdict": "Positive"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("rejects an over-long reaction name", func() {
		spawner := scoringSpawner("long-name", []interface{}{
			map[string]interface{}{"name": stringOfLength(65), "verdict": "Positive"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("rejects an unknown verdict", func() {
		spawner := scoringSpawner("bad-verdict", []interface{}{
			map[string]interface{}{"name": "+1", "verdict": "Mixed"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("rejects a reaction entry without a verdict", func() {
		spawner := scoringSpawner("no-verdict", []interface{}{
			map[string]interface{}{"name": "+1"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	// listMapKey=name makes the same reaction twice unexpressible, which is what
	// removes the need for a precedence rule between conflicting verdicts.
	It("rejects the same reaction name twice", func() {
		spawner := scoringSpawner("duplicate-name", []interface{}{
			map[string]interface{}{"name": "+1", "verdict": "Positive"},
			map[string]interface{}{"name": "+1", "verdict": "Negative"},
		})
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})

	It("rejects more than 32 reactions", func() {
		reactions := make([]interface{}, 0, 33)
		for i := 0; i < 33; i++ {
			reactions = append(reactions, map[string]interface{}{
				"name":    "emoji-" + stringOfLength(i+1),
				"verdict": "Positive",
			})
		}
		spawner := scoringSpawner("too-many", reactions)
		Expect(k8sClient.Create(ctx, spawner)).ShouldNot(Succeed())
	})
})
