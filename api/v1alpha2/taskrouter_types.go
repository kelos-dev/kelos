package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TaskRouterPhase represents the current phase of a TaskRouter.
type TaskRouterPhase string

const (
	// TaskRouterPhaseRunning means the TaskRouter is consuming requests.
	TaskRouterPhaseRunning TaskRouterPhase = "Running"
	// TaskRouterPhaseFailed means the TaskRouter can dispatch nowhere: no route
	// target resolves, or the last dispatch failed. A router with some routes
	// resolving stays Running and reports the rest on the RoutesResolved
	// condition, because requests selecting a working route still dispatch.
	TaskRouterPhaseFailed TaskRouterPhase = "Failed"
	// TaskRouterPhaseSuspended means the TaskRouter is paused by the user.
	TaskRouterPhaseSuspended TaskRouterPhase = "Suspended"
)

// RouterTargetKind is the kind a route dispatches to.
type RouterTargetKind string

const (
	// RouterTargetKindTaskSpawner dispatches by building a Task from the
	// referenced TaskSpawner's taskTemplate.
	RouterTargetKindTaskSpawner RouterTargetKind = "TaskSpawner"
)

// RouterFallbackAction selects what happens when the decision selects no route.
type RouterFallbackAction string

const (
	// RouterFallbackActionRoute dispatches to the route named by fallback.route.
	RouterFallbackActionRoute RouterFallbackAction = "Route"
	// RouterFallbackActionReply answers the initiator with the decision's
	// explanation and dispatches nothing.
	RouterFallbackActionReply RouterFallbackAction = "Reply"
	// RouterFallbackActionFail records the failure on status and dispatches nothing.
	RouterFallbackActionFail RouterFallbackAction = "Fail"
)

// RouterDecision configures the agent that chooses a route. The decision runs
// on the same worker machinery as a Task, so it accepts the same execution
// environment fields, including podOverrides.env for extra environment
// variables. As with a Task, podOverrides is unavailable when dispatching to a
// pre-warmed pool through workerPoolRef.
//
// +kubebuilder:validation:XValidation:rule="has(self.workerPoolRef) || (has(self.worker) && has(self.worker.type) && size(self.worker.type) > 0)",message="either workerPoolRef or worker with type is required"
// +kubebuilder:validation:XValidation:rule="!has(self.workerPoolRef) || !has(self.worker)",message="workerPoolRef is mutually exclusive with worker"
// +kubebuilder:validation:XValidation:rule="!has(self.worker) || has(self.worker.credentials)",message="worker.credentials is required for inline execution"
// +kubebuilder:validation:XValidation:rule="!has(self.workerPoolRef) || !has(self.timeoutSeconds)",message="timeoutSeconds is not supported with workerPoolRef; bound a pooled decision on the WorkerPool"
type RouterDecision struct {
	// Worker defines the execution environment for the routing decision.
	// +optional
	Worker *WorkerSpec `json:"worker,omitempty"`

	// WorkerPoolRef runs the routing decision on a pre-warmed WorkerPool,
	// which avoids a cold pod start for each request.
	// +optional
	WorkerPoolRef *WorkerPoolReference `json:"workerPoolRef,omitempty"`

	// TTLSecondsAfterHandled is how long a decision Task is kept after the
	// router has acted on it — dispatched the work, answered the initiator, or
	// recorded a failure. Expired decisions are garbage-collected during
	// reconciliation, which bounds what the router and the Slack ingress scan
	// and keeps status.recordedDecisions from growing without limit. A decision
	// the router has not acted on is never collected, however old it is, so a
	// controller outage cannot drop a request.
	//
	// Set it higher to retain decisions for auditing.
	// +optional
	// +kubebuilder:default=86400
	// +kubebuilder:validation:Minimum=1
	TTLSecondsAfterHandled *int32 `json:"ttlSecondsAfterHandled,omitempty"`

	// TimeoutSeconds bounds one routing decision, defaulting to 300. It is
	// applied as the decision pod's activeDeadlineSeconds when
	// worker.podOverrides does not set one of its own, so a decision that
	// exceeds it fails and spec.fallback applies.
	//
	// It is rejected with workerPoolRef: a pooled decision runs on a pod the
	// pool owns, which this cannot bound, so a value there would be a knob that
	// does nothing. Bound those on the WorkerPool.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// RouterTargetRef identifies the resource a route dispatches to. Targets are
// resolved in the TaskRouter's own namespace.
type RouterTargetRef struct {
	// Kind is the kind of the target resource.
	// +kubebuilder:validation:Enum=TaskSpawner
	Kind RouterTargetKind `json:"kind"`

	// Name is the name of the target resource.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// RouterRoute is one dispatch candidate offered to the routing decision.
type RouterRoute struct {
	// Name identifies this route. The decision selects a route by this name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// Description tells the decision what belongs on this route. It is the
	// routing contract, and is the only per-route text the decision sees.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Description string `json:"description"`

	// TargetRef is the resource this route dispatches to.
	TargetRef RouterTargetRef `json:"targetRef"`
}

// RouterFallback controls what happens when the decision selects no route,
// returns a name that is not listed in spec.routes, or fails — which for an
// inline decision includes exceeding decision.timeoutSeconds, since that
// becomes the decision pod's deadline. A decision that never reaches a
// terminal phase leaves the request unrouted rather than falling back.
// Omitting spec.fallback is equivalent to Fail.
//
// +kubebuilder:validation:XValidation:rule="(self.action == 'Route') == has(self.route)",message="route is required when action is Route, and is not allowed otherwise"
type RouterFallback struct {
	// Action selects the behavior. Route dispatches to the route named by
	// Route. Reply answers the initiator with the decision's explanation and
	// dispatches nothing. Fail records the failure on status and dispatches
	// nothing.
	// +kubebuilder:validation:Enum=Route;Reply;Fail
	Action RouterFallbackAction `json:"action"`

	// Route names the route to dispatch to. Required when action is Route.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Route string `json:"route,omitempty"`
}

// RouterWhen defines the requests a TaskRouter consumes. It deliberately does
// not reuse the TaskSpawner When type: a router serves only the sources that
// have an ingress creating routing decisions, and enumerating them here keeps
// adding a TaskSpawner source from silently widening what a router accepts.
// The source configuration itself is the same type a TaskSpawner uses.
type RouterWhen struct {
	// Slack routes messages the kelos-slack-server receives.
	// +kubebuilder:validation:Required
	Slack *Slack `json:"slack,omitempty"`
}

// TaskRouterSpec defines the desired state of a TaskRouter.
//
// +kubebuilder:validation:XValidation:rule="!has(self.fallback) || !has(self.fallback.route) || self.routes.exists(r, r.name == self.fallback.route)",message="fallback.route must name a route listed in routes"
type TaskRouterSpec struct {
	// When defines the requests this router consumes.
	// +kubebuilder:validation:Required
	When RouterWhen `json:"when"`

	// Decision configures the agent that chooses a route.
	// +kubebuilder:validation:Required
	Decision RouterDecision `json:"decision"`

	// Prompt is the routing instruction given to the decision, rendered with
	// the request and the route menu.
	// +kubebuilder:validation:MinLength=1
	Prompt string `json:"prompt"`

	// Routes are the dispatch candidates. A decision may only select a route
	// listed here; it cannot name a target of its own.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	// +listType=map
	// +listMapKey=name
	Routes []RouterRoute `json:"routes"`

	// Fallback controls what happens when no route is selected. When unset,
	// an unrouted request fails and is recorded on status.
	// +optional
	Fallback *RouterFallback `json:"fallback,omitempty"`

	// MaxConcurrency limits the number of in-flight routing decisions. When
	// unset or zero, there is no limit.
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxConcurrency *int32 `json:"maxConcurrency,omitempty"`

	// Suspend tells the router to stop consuming requests. In-flight decisions
	// and dispatched work are not affected. Defaults to false.
	// +optional
	// +kubebuilder:default=false
	Suspend *bool `json:"suspend,omitempty"`
}

// RouterLastDecision records the most recent routing decision.
type RouterLastDecision struct {
	// Route is the route the decision was acted on with, which is the fallback
	// route when the decision itself selected none. It is always a name listed
	// in spec.routes, never the raw answer. Empty when nothing was dispatched.
	// +optional
	// +kubebuilder:validation:MaxLength=63
	Route string `json:"route,omitempty"`

	// DispatchedTask is the name of the Task this decision created. Empty when
	// the decision dispatched nothing.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	DispatchedTask string `json:"dispatchedTask,omitempty"`

	// Time is when the decision completed.
	// +optional
	Time *metav1.Time `json:"time,omitempty"`
}

// TaskRouterStatus defines the observed state of a TaskRouter.
type TaskRouterStatus struct {
	// ObservedGeneration is the most recent generation processed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Phase represents the current phase of the TaskRouter.
	// +optional
	Phase TaskRouterPhase `json:"phase,omitempty"`

	// RecordedDecisions is how many routing decisions this router currently
	// retains. It is a live count of the decision Tasks the router owns, not a
	// cumulative total: it falls as decisions age out under
	// spec.decision.ttlSecondsAfterHandled.
	// +optional
	RecordedDecisions int32 `json:"recordedDecisions,omitempty"`

	// DispatchedDecisions is how many of the retained decisions dispatched
	// work, and is a live count for the same reason.
	// +optional
	DispatchedDecisions int32 `json:"dispatchedDecisions,omitempty"`

	// LastDecision records the most recent routing decision.
	// +optional
	LastDecision *RouterLastDecision `json:"lastDecision,omitempty"`

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
// +kubebuilder:resource:shortName=tro
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Decisions",type=integer,JSONPath=`.status.recordedDecisions`
// +kubebuilder:printcolumn:name="Dispatched",type=integer,JSONPath=`.status.dispatchedDecisions`
// +kubebuilder:printcolumn:name="Route",type=string,JSONPath=`.status.lastDecision.route`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// TaskRouter chooses which of several TaskSpawners should handle an incoming
// request, using an agent of its own, and dispatches the work there.
type TaskRouter struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:Required
	Spec   TaskRouterSpec   `json:"spec"`
	Status TaskRouterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// TaskRouterList contains a list of TaskRouter resources.
type TaskRouterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaskRouter `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TaskRouter{}, &TaskRouterList{})
}
