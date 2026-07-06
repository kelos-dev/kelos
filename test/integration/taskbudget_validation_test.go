package integration

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

var _ = Describe("TaskBudget API validation", func() {
	const ns = "test-taskbudget-validation"

	BeforeEach(func() {
		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
		_ = k8sClient.Create(ctx, namespace)
	})

	quantity := func(s string) *resource.Quantity {
		q := resource.MustParse(s)
		return &q
	}

	It("accepts a well-formed TaskBudget", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "valid", Namespace: ns},
			Spec: kelos.TaskBudgetSpec{
				TaskSelector: metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "team", Operator: metav1.LabelSelectorOpIn, Values: []string{"platform"}},
						{Key: "tier", Operator: metav1.LabelSelectorOpExists},
					},
				},
				Period:     kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily, Timezone: "UTC"},
				MaxCostUSD: quantity("50"),
			},
		}
		Expect(k8sClient.Create(ctx, budget)).Should(Succeed())
	})

	It("rejects a spec-less TaskBudget", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "no-spec", Namespace: ns},
		}
		Expect(k8sClient.Create(ctx, budget)).ShouldNot(Succeed())
	})

	It("rejects a TaskBudget with no limits", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "no-limits", Namespace: ns},
			Spec: kelos.TaskBudgetSpec{
				TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
				Period:       kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
			},
		}
		Expect(k8sClient.Create(ctx, budget)).ShouldNot(Succeed())
	})

	It("rejects an In matchExpression without values", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "in-no-values", Namespace: ns},
			Spec: kelos.TaskBudgetSpec{
				TaskSelector: metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "team", Operator: metav1.LabelSelectorOpIn},
					},
				},
				Period:     kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
				MaxCostUSD: quantity("50"),
			},
		}
		Expect(k8sClient.Create(ctx, budget)).ShouldNot(Succeed())
	})

	It("rejects an Exists matchExpression with values", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "exists-with-values", Namespace: ns},
			Spec: kelos.TaskBudgetSpec{
				TaskSelector: metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "team", Operator: metav1.LabelSelectorOpExists, Values: []string{"x"}},
					},
				},
				Period:     kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
				MaxCostUSD: quantity("50"),
			},
		}
		Expect(k8sClient.Create(ctx, budget)).ShouldNot(Succeed())
	})

	It("accepts a valid non-UTC IANA timezone", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "valid-tz", Namespace: ns},
			Spec: kelos.TaskBudgetSpec{
				TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
				Period:       kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily, Timezone: "America/New_York"},
				MaxCostUSD:   quantity("50"),
			},
		}
		Expect(k8sClient.Create(ctx, budget)).Should(Succeed())
	})

	It("rejects an unknown IANA timezone", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-tz", Namespace: ns},
			Spec: kelos.TaskBudgetSpec{
				TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
				Period:       kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily, Timezone: "Not/AZone"},
				MaxCostUSD:   quantity("50"),
			},
		}
		Expect(k8sClient.Create(ctx, budget)).ShouldNot(Succeed())
	})

	It("rejects a negative maxCostUSD", func() {
		budget := &kelos.TaskBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "negative-cost", Namespace: ns},
			Spec: kelos.TaskBudgetSpec{
				TaskSelector: metav1.LabelSelector{MatchLabels: map[string]string{"team": "platform"}},
				Period:       kelos.BudgetPeriod{Type: kelos.BudgetPeriodDaily},
				MaxCostUSD:   quantity("-1"),
			},
		}
		Expect(k8sClient.Create(ctx, budget)).ShouldNot(Succeed())
	})
})
