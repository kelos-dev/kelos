package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskPipelinePhase represents the current phase of a TaskPipeline.
type TaskPipelinePhase string

const (
	// TaskPipelinePhasePending means the pipeline has not started any Tasks.
	TaskPipelinePhasePending TaskPipelinePhase = "Pending"
	// TaskPipelinePhaseRunning means at least one pipeline Task has started.
	TaskPipelinePhaseRunning TaskPipelinePhase = "Running"
	// TaskPipelinePhaseSucceeded means every stage completed successfully.
	TaskPipelinePhaseSucceeded TaskPipelinePhase = "Succeeded"
	// TaskPipelinePhaseFailed means the pipeline cannot complete successfully.
	TaskPipelinePhaseFailed TaskPipelinePhase = "Failed"
)

const (
	// TaskPipelineConditionReady reports whether every pipeline stage succeeded.
	TaskPipelineConditionReady = "Ready"
)

// PipelineMatrixParameter defines one dimension of a matrix expansion.
type PipelineMatrixParameter struct {
	// Name is the key exposed under .Matrix in the stage's templates.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-zA-Z_][a-zA-Z0-9_]*$`
	Name string `json:"name"`

	// Values contains the values to expand for this parameter.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=set
	Values []string `json:"values"`
}

// PipelineMatrix expands a stage into one Task for each combination of parameter values.
type PipelineMatrix struct {
	// Parameters defines the matrix dimensions. Their Cartesian product
	// determines the Tasks created for the stage.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=name
	Parameters []PipelineMatrixParameter `json:"parameters"`
}

// PipelineTaskTemplate defines Tasks created for a pipeline stage.
// Prompt and branch support Go text/template expressions. The current matrix
// values are available as .Matrix. Results from completed stages are available
// as .Stages, keyed by stage name.
//
// +kubebuilder:validation:XValidation:rule="has(self.workerPoolRef) || (has(self.worker) && has(self.worker.type) && size(self.worker.type) > 0)",message="either workerPoolRef or worker with type is required"
// +kubebuilder:validation:XValidation:rule="!has(self.workerPoolRef) || !has(self.worker)",message="workerPoolRef is mutually exclusive with worker"
// +kubebuilder:validation:XValidation:rule="!has(self.worker) || has(self.worker.credentials)",message="worker.credentials is required for inline execution"
// +kubebuilder:validation:XValidation:rule="!has(self.workerPoolRef) || !has(self.branch) || size(self.branch) == 0",message="branch is not supported with workerPoolRef"
type PipelineTaskTemplate struct {
	// Worker defines the execution environment for inline Task execution.
	// +optional
	Worker *WorkerSpec `json:"worker,omitempty"`

	// WorkerPoolRef dispatches Tasks to a pre-warmed WorkerPool.
	// +optional
	WorkerPoolRef *WorkerPoolReference `json:"workerPoolRef,omitempty"`

	// Prompt is the template rendered into the child Task prompt.
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// Branch is the optional template rendered into the child Task branch.
	// +optional
	Branch string `json:"branch,omitempty"`
}

// PipelineStage defines one step in a TaskPipeline.
type PipelineStage struct {
	// Name identifies this stage within the pipeline.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// TaskTemplate defines the Tasks created for this stage.
	TaskTemplate PipelineTaskTemplate `json:"taskTemplate"`

	// Matrix expands the stage into parallel Tasks.
	// +optional
	Matrix *PipelineMatrix `json:"matrix,omitempty"`
}

// TaskPipelineSpec defines an ordered sequence of stages.
//
// +kubebuilder:validation:XValidation:rule="self.stages.all(stage, self.stages.exists_one(candidate, candidate.name == stage.name))",message="stage names must be unique"
// +kubebuilder:validation:XValidation:rule="self.stages == oldSelf.stages",message="TaskPipeline stages are immutable after creation"
type TaskPipelineSpec struct {
	// Stages execute in order. Tasks within a matrix stage execute in parallel.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	Stages []PipelineStage `json:"stages"`
}

// PipelineStageStatus summarizes the Tasks created for one pipeline stage.
type PipelineStageStatus struct {
	// Name identifies the pipeline stage.
	Name string `json:"name"`

	// Phase is the aggregate phase of this stage's Tasks.
	// +optional
	Phase TaskPhase `json:"phase,omitempty"`

	// Total is the number of Tasks expected for this stage.
	Total int32 `json:"total"`

	// Succeeded is the number of successful Tasks.
	Succeeded int32 `json:"succeeded"`

	// Failed is the number of failed Tasks.
	Failed int32 `json:"failed"`

	// Active is the number of Tasks that have not reached a terminal phase.
	Active int32 `json:"active"`
}

// TaskPipelineStatus defines the observed state of a TaskPipeline.
type TaskPipelineStatus struct {
	// ObservedGeneration is the most recent generation processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase represents the current phase of the pipeline.
	// +optional
	Phase TaskPipelinePhase `json:"phase,omitempty"`

	// StageStatuses contains aggregate status for every pipeline stage.
	// +optional
	// +listType=map
	// +listMapKey=name
	StageStatuses []PipelineStageStatus `json:"stageStatuses,omitempty"`

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
// +kubebuilder:resource:shortName=tp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskPipeline orchestrates an ordered sequence of Task stages as one managed resource.
type TaskPipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   TaskPipelineSpec   `json:"spec"`
	Status TaskPipelineStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskPipelineList contains a list of TaskPipeline resources.
type TaskPipelineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskPipeline `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskPipeline{}, &TaskPipelineList{})
}
