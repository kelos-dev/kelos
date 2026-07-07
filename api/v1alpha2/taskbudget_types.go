package v1alpha2

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BudgetPeriodType defines budget reset intervals.
type BudgetPeriodType string

const (
	// BudgetPeriodDaily resets at midnight in the configured timezone.
	BudgetPeriodDaily BudgetPeriodType = "Daily"
)

// BudgetPeriod defines the accounting window for a TaskBudget.
type BudgetPeriod struct {
	// Type is the period boundary used for budget accounting.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Daily
	Type BudgetPeriodType `json:"type"`

	// Timezone is the IANA timezone used to compute period boundaries.
	// Defaults to UTC. The XValidation rule rejects names that the controller
	// cannot load (getHours errors on an unknown IANA zone), so an invalid
	// timezone cannot be stored and fail closed at admission.
	// +optional
	// +kubebuilder:default="UTC"
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9/_+-]+$`
	// +kubebuilder:validation:XValidation:rule="timestamp('2000-01-01T00:00:00Z').getHours(self) >= 0",message="timezone must be a valid IANA time zone"
	Timezone string `json:"timezone,omitempty"`
}

// TaskBudgetSpec defines observed-spend admission limits for Tasks.
//
// +kubebuilder:validation:XValidation:rule="has(self.maxCostUSD) || has(self.maxInputTokens) || has(self.maxOutputTokens)",message="at least one of maxCostUSD, maxInputTokens, or maxOutputTokens must be set"
type TaskBudgetSpec struct {
	// TaskSelector selects Tasks and TaskRecords in the same namespace.
	// An empty selector ({}) selects all Tasks in the namespace.
	// +kubebuilder:validation:Required
	TaskSelector metav1.LabelSelector `json:"taskSelector"`

	// Period defines the accounting window for this budget.
	// +kubebuilder:validation:Required
	Period BudgetPeriod `json:"period"`

	// MaxCostUSD is the maximum observed completed-task cost admitted in the period.
	// +optional
	// +kubebuilder:validation:XValidation:rule="type(self) == int ? self >= 0 : !quantity(self).isLessThan(quantity('0'))",message="maxCostUSD must be non-negative"
	MaxCostUSD *resource.Quantity `json:"maxCostUSD,omitempty"`

	// MaxInputTokens is the maximum observed input tokens admitted in the period.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxInputTokens *int64 `json:"maxInputTokens,omitempty"`

	// MaxOutputTokens is the maximum observed output tokens admitted in the period.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxOutputTokens *int64 `json:"maxOutputTokens,omitempty"`
}

// TaskBudgetStatus tracks the current accounting period and usage.
type TaskBudgetStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// CurrentPeriodStart is the inclusive start of the current accounting period.
	// +optional
	CurrentPeriodStart *metav1.Time `json:"currentPeriodStart,omitempty"`

	// CurrentPeriodEnd is the exclusive end of the current accounting period.
	// +optional
	CurrentPeriodEnd *metav1.Time `json:"currentPeriodEnd,omitempty"`

	// Used contains usage summed from matching TaskRecords in the current period.
	// +optional
	Used *TaskUsage `json:"used,omitempty"`

	// Conditions provides detailed status information.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchMergeKey=type
	// +patchStrategy=merge
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tb
// +kubebuilder:printcolumn:name="Period",type=string,JSONPath=`.spec.period.type`
// +kubebuilder:printcolumn:name="Max Cost",type=string,JSONPath=`.spec.maxCostUSD`,priority=1
// +kubebuilder:printcolumn:name="Used Cost",type=string,JSONPath=`.status.used.costUSD`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskBudget defines observed-spend admission limits for Tasks.
// When a Task's labels match a TaskBudget's taskSelector and the budget
// is exceeded, the Task stays in Waiting phase with a BudgetBlocked
// condition until the period resets.
type TaskBudget struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Spec is required. A spec-less TaskBudget would decode to an empty selector
	// (matching all Tasks) and an empty period, which fails closed and blocks
	// Task admission for the whole namespace.
	// +kubebuilder:validation:Required
	Spec   TaskBudgetSpec   `json:"spec"`
	Status TaskBudgetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskBudgetList contains a list of TaskBudget.
type TaskBudgetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskBudget `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskBudget{}, &TaskBudgetList{})
}
