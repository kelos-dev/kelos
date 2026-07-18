package sessionruntime

import (
	"context"
	"encoding/json"
	"fmt"

	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	clientv1alpha2 "github.com/kelos-dev/kelos/pkg/generated/clientset/versioned/typed/api/v1alpha2"
)

// ObservedSessionStatus contains the status fields owned by one Session runtime.
type ObservedSessionStatus struct {
	Active bool
	// WorkspaceStatus is omitted when the runtime cannot inspect the workspace.
	WorkspaceStatus *WorkspaceStatus
}

// SessionStatusPublisher publishes runtime-observed state for one Session Pod.
type SessionStatusPublisher func(context.Context, ObservedSessionStatus) error

type sessionStatusPatchOperation struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value"`
}

// NewSessionStatusPublisher creates a publisher scoped to one Session Pod.
func NewSessionStatusPublisher(client clientv1alpha2.SessionInterface, sessionName string, podUID types.UID) (SessionStatusPublisher, error) {
	if client == nil || sessionName == "" || podUID == "" {
		return nil, fmt.Errorf("Session client, name, and Pod UID are required")
	}
	return func(ctx context.Context, status ObservedSessionStatus) error {
		session, err := client.Get(ctx, sessionName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("getting Session %q before publishing runtime status: %w", sessionName, err)
		}
		conditionStatus := metav1.ConditionFalse
		reason := "Idle"
		message := "Session runtime is idle"
		if status.Active {
			conditionStatus = metav1.ConditionTrue
			reason = "TurnActive"
			message = "Session runtime has an unfinished turn"
		}
		conditions := append([]metav1.Condition(nil), session.Status.Conditions...)
		apiMeta.SetStatusCondition(&conditions, metav1.Condition{
			Type:               kelos.SessionConditionActive,
			Status:             conditionStatus,
			ObservedGeneration: session.Generation,
			Reason:             reason,
			Message:            message,
		})
		operations := make([]sessionStatusPatchOperation, 0, 6)
		if session.ResourceVersion != "" {
			operations = append(operations, sessionStatusPatchOperation{Op: "test", Path: "/metadata/resourceVersion", Value: session.ResourceVersion})
		}
		operations = append(operations,
			sessionStatusPatchOperation{Op: "test", Path: "/status/podUID", Value: string(podUID)},
			sessionStatusPatchOperation{Op: "test", Path: "/status/phase", Value: kelos.SessionPhaseReady},
			sessionStatusPatchOperation{Op: "add", Path: "/status/conditions", Value: conditions},
		)
		if status.WorkspaceStatus != nil {
			operations = append(operations,
				sessionStatusPatchOperation{Op: "add", Path: "/status/branch", Value: status.WorkspaceStatus.Branch},
				sessionStatusPatchOperation{Op: "add", Path: "/status/pullRequest", Value: status.WorkspaceStatus.PullRequest},
			)
		}
		patch, err := json.Marshal(operations)
		if err != nil {
			return fmt.Errorf("encoding Session runtime status patch: %w", err)
		}
		if _, err := client.Patch(ctx, sessionName, types.JSONPatchType, patch, metav1.PatchOptions{}, "status"); err != nil {
			return fmt.Errorf("publishing Session %q runtime status: %w", sessionName, err)
		}
		return nil
	}, nil
}
