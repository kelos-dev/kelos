package admission

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cradmission "sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// +kubebuilder:webhookconfiguration:name=kelos-validating-webhook-configuration,mutating=false
// +kubebuilder:webhook:path=/validate-kelos-dev-v1alpha2-taskbudget,mutating=false,failurePolicy=fail,sideEffects=None,groups=kelos.dev,resources=taskbudgets,verbs=create;update,versions=v1alpha2,name=vtaskbudget.kelos.dev,admissionReviewVersions=v1,serviceName=kelos-webhook,serviceNamespace=kelos-system,servicePort=443

// TaskBudgetValidator validates TaskBudget resources at admission.
type TaskBudgetValidator struct{}

func (v *TaskBudgetValidator) ValidateCreate(_ context.Context, obj *kelos.TaskBudget) (cradmission.Warnings, error) {
	return nil, v.validate(obj)
}

func (v *TaskBudgetValidator) ValidateUpdate(_ context.Context, _, newObj *kelos.TaskBudget) (cradmission.Warnings, error) {
	return nil, v.validate(newObj)
}

func (v *TaskBudgetValidator) ValidateDelete(_ context.Context, _ *kelos.TaskBudget) (cradmission.Warnings, error) {
	return nil, nil
}

func (*TaskBudgetValidator) validate(obj *kelos.TaskBudget) error {
	if _, err := metav1.LabelSelectorAsSelector(&obj.Spec.TaskSelector); err != nil {
		return fmt.Errorf("taskSelector is invalid: %w", err)
	}
	return nil
}
