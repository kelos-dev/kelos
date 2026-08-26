package v1alpha2

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskPipelinePhase represents the current phase of a TaskPipeline.
type TaskPipelinePhase string

const (
	// TaskPipelinePhasePending means the pipeline has not started any Tasks.
	TaskPipelinePhasePending TaskPipelinePhase = "Pending"
	// TaskPipelinePhaseRunning means at least one pipeline Task has started.
	TaskPipelinePhaseRunning TaskPipelinePhase = "Running"
	// TaskPipelinePhaseSucceeded means every node completed successfully.
	TaskPipelinePhaseSucceeded TaskPipelinePhase = "Succeeded"
	// TaskPipelinePhaseFailed means the pipeline cannot complete successfully.
	TaskPipelinePhaseFailed TaskPipelinePhase = "Failed"
)

// PipelineMatrix expands a node into one Task for each combination of parameter
// values. Parameter names are available under .Matrix in the node's prompt and
// branch templates.
//
// +kubebuilder:validation:XValidation:rule="self.parameters.all(name, name.matches('^[a-zA-Z_][a-zA-Z0-9_]*$'))",message="matrix parameter names must be valid template identifiers"
// +kubebuilder:validation:XValidation:rule="self.parameters.all(name, size(self.parameters[name]) > 0)",message="matrix parameters must each contain at least one value"
// +kubebuilder:validation:XValidation:rule="self.parameters.all(name, size(self.parameters[name]) <= 64)",message="matrix parameters may each contain at most 64 values"
type PipelineMatrix struct {
	// Parameters maps each template variable to its possible values. The
	// Cartesian product of these lists determines the Tasks created for the node.
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=16
	Parameters map[string][]string `json:"parameters"`
}

// PipelineTaskTemplate defines a Task created for a pipeline node.
// Prompt and branch support Go text/template expressions. The current matrix
// values are available as .Matrix. Results from declared dependencies are
// available as .Tasks, keyed by node name.
//
// +kubebuilder:validation:XValidation:rule="has(self.workerPoolRef) || (has(self.worker) && has(self.worker.type) && size(self.worker.type) > 0)",message="either workerPoolRef or worker with type is required"
// +kubebuilder:validation:XValidation:rule="!has(self.workerPoolRef) || !has(self.worker)",message="workerPoolRef is mutually exclusive with worker"
// +kubebuilder:validation:XValidation:rule="!has(self.worker) || has(self.worker.credentials)",message="worker.credentials is required for inline execution"
// +kubebuilder:validation:XValidation:rule="!has(self.workerPoolRef) || !has(self.branch) || size(self.branch) == 0",message="branch is not supported with workerPoolRef"
// +kubebuilder:validation:XValidation:rule="!has(self.workerPoolRef) || !has(self.podFailurePolicy)",message="podFailurePolicy is not supported with workerPoolRef"
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

	// UpstreamRepo is copied to the child Task.
	// +optional
	UpstreamRepo string `json:"upstreamRepo,omitempty"`

	// PodFailurePolicy is copied to the child Task's backing Job.
	// +optional
	PodFailurePolicy *batchv1.PodFailurePolicy `json:"podFailurePolicy,omitempty"`
}

// PipelineNode is one named node in a TaskPipeline DAG.
type PipelineNode struct {
	// Name identifies this node within the pipeline.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// DependsOn lists node names that must succeed before this node starts.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	// +listType=set
	DependsOn []string `json:"dependsOn,omitempty"`

	// TaskTemplate defines the Task created for this node.
	TaskTemplate PipelineTaskTemplate `json:"taskTemplate"`

	// Matrix expands the node into parallel Tasks.
	// +optional
	Matrix *PipelineMatrix `json:"matrix,omitempty"`
}

// TaskPipelineSpec defines a DAG of Task templates.
//
// +kubebuilder:validation:XValidation:rule="self.tasks.all(task, !has(task.dependsOn) || task.dependsOn.all(dependency, dependency != task.name))",message="a pipeline node cannot depend on itself"
// +kubebuilder:validation:XValidation:rule="self.tasks.all(task, !has(task.dependsOn) || task.dependsOn.all(dependency, self.tasks.exists(candidate, candidate.name == dependency)))",message="dependsOn must reference another node in this pipeline"
type TaskPipelineSpec struct {
	// Tasks contains the named nodes in the pipeline DAG.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=name
	Tasks []PipelineNode `json:"tasks"`
}

// PipelineTaskResult contains one child Task's outputs and matrix parameters.
type PipelineTaskResult struct {
	// Name is the child Task name.
	Name string `json:"name"`

	// Matrix contains the parameters used to render this Task.
	// +optional
	Matrix map[string]string `json:"matrix,omitempty"`

	// Outputs contains the child Task's raw captured outputs.
	// +optional
	Outputs []string `json:"outputs,omitempty"`

	// Results contains the child Task's structured key-value results.
	// +optional
	Results map[string]string `json:"results,omitempty"`
}

// PipelineNodeStatus summarizes the Tasks created for one pipeline node.
type PipelineNodeStatus struct {
	// Name identifies the pipeline node.
	Name string `json:"name"`

	// Phase is the aggregate phase of this node's Tasks.
	// +optional
	Phase TaskPhase `json:"phase,omitempty"`

	// Total is the number of Tasks expected after matrix expansion.
	Total int32 `json:"total"`

	// Succeeded is the number of successful Tasks.
	Succeeded int32 `json:"succeeded"`

	// Failed is the number of failed Tasks.
	Failed int32 `json:"failed"`

	// Running is the number of Tasks that have not reached a terminal phase.
	Running int32 `json:"running"`

	// TaskNames lists child Tasks in matrix expansion order.
	// +optional
	TaskNames []string `json:"taskNames,omitempty"`

	// TaskResults aggregates captured outputs from terminal child Tasks.
	// +optional
	TaskResults []PipelineTaskResult `json:"taskResults,omitempty"`

	// Message provides additional information about this node.
	// +optional
	Message string `json:"message,omitempty"`
}

// TaskPipelineStatus defines the observed state of a TaskPipeline.
type TaskPipelineStatus struct {
	// ObservedGeneration is the most recent generation processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase represents the current phase of the pipeline.
	// +optional
	Phase TaskPipelinePhase `json:"phase,omitempty"`

	// NodeStatuses contains aggregate status for every pipeline node.
	// +optional
	// +listType=map
	// +listMapKey=name
	NodeStatuses []PipelineNodeStatus `json:"nodeStatuses,omitempty"`

	// StartTime is when the first child Task was created.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the pipeline reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message provides additional information about the pipeline.
	// +optional
	Message string `json:"message,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tp
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskPipeline orchestrates a DAG of Tasks as one managed resource.
type TaskPipeline struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="TaskPipeline spec is immutable after creation"
	Spec   TaskPipelineSpec   `json:"spec,omitempty"`
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
