/*
Copyright 2026 Kelos contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha2

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkerPoolPhase represents the current phase of a WorkerPool.
type WorkerPoolPhase string

const (
	// WorkerPoolPhasePending means the WorkerPool has been accepted but workers are not yet running.
	WorkerPoolPhasePending WorkerPoolPhase = "Pending"
	// WorkerPoolPhaseReady means the desired number of workers are running and available.
	WorkerPoolPhaseReady WorkerPoolPhase = "Ready"
	// WorkerPoolPhaseScaling means the WorkerPool is adjusting the number of workers.
	WorkerPoolPhaseScaling WorkerPoolPhase = "Scaling"
	// WorkerPoolPhaseFailed means the WorkerPool has failed to reach a healthy state.
	WorkerPoolPhaseFailed WorkerPoolPhase = "Failed"
)

const (
	// AnnotationWorkerAssignedTask is set on worker pods to indicate which Task is assigned.
	AnnotationWorkerAssignedTask = "kelos.dev/assigned-task"

	// AnnotationWorkerTaskStatus is set on worker pods by the runner to report task status.
	AnnotationWorkerTaskStatus = "kelos.dev/task-status"

	// AnnotationWorkerTasksCompleted tracks the number of tasks completed by a worker pod.
	AnnotationWorkerTasksCompleted = "kelos.dev/tasks-completed"

	// AnnotationWorkerTaskFailReason is set by the runner to indicate why a task failed.
	AnnotationWorkerTaskFailReason = "kelos.dev/task-failure-reason"
)

// WorkerPoolSpec defines the desired state of WorkerPool.
//
// +kubebuilder:validation:XValidation:rule="has(self.worker.type) && size(self.worker.type) > 0",message="worker.type is required"
// +kubebuilder:validation:XValidation:rule="has(self.worker.credentials)",message="worker.credentials is required"
// +kubebuilder:validation:XValidation:rule="has(self.worker.workspaceRef)",message="worker.workspaceRef is required"
type WorkerPoolSpec struct {
	// Worker defines the execution environment for workers in this pool.
	// The type, credentials, and workspaceRef fields are required.
	// +kubebuilder:validation:Required
	Worker WorkerSpec `json:"worker"`

	// Replicas is the desired number of persistent worker pods.
	// Defaults to 1 if not specified. Set to 0 to pause the pool.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// VolumeClaimTemplate defines the persistent volume claim spec for each worker pod.
	// +kubebuilder:validation:Required
	VolumeClaimTemplate corev1.PersistentVolumeClaimSpec `json:"volumeClaimTemplate"`
}

// WorkerPoolStatus defines the observed state of WorkerPool.
type WorkerPoolStatus struct {
	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase represents the current phase of the WorkerPool.
	// +optional
	// +kubebuilder:validation:Enum=Pending;Ready;Scaling;Failed;""
	Phase WorkerPoolPhase `json:"phase,omitempty"`

	// StatefulSetName is the name of the StatefulSet managing the worker pods.
	// +optional
	StatefulSetName string `json:"statefulSetName,omitempty"`

	// ServiceName is the name of the headless Service for the worker pods.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// Replicas is the current number of worker pods.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of worker pods in a ready state.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// Message provides additional information about the current status.
	// +optional
	Message string `json:"message,omitempty"`

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
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.worker.type`
// +kubebuilder:printcolumn:name="Replicas",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Workspace",type=string,JSONPath=`.spec.worker.workspaceRef.name`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// WorkerPool is the Schema for the workerpools API.
// A WorkerPool manages a fleet of persistent worker pods backed by a
// StatefulSet and headless Service. Tasks reference a WorkerPool via
// spec.workerPoolRef to execute on pre-warmed infrastructure instead of
// creating per-task Jobs.
type WorkerPool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkerPoolSpec   `json:"spec,omitempty"`
	Status WorkerPoolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkerPoolList contains a list of WorkerPool.
type WorkerPoolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkerPool `json:"items"`
}

// WorkerPoolReference refers to a WorkerPool resource by name.
type WorkerPoolReference struct {
	// Name is the name of the WorkerPool resource.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

func init() {
	SchemeBuilder.Register(&WorkerPool{}, &WorkerPoolList{})
}
