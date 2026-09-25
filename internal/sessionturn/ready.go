package sessionturn

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/sessionsuspend"
)

// DefaultReadyPollInterval is how often WaitReady rechecks a Session that is
// not yet Ready.
const DefaultReadyPollInterval = 2 * time.Second

// WaitReady blocks until the Session reports Ready and returns the name of the
// Pod hosting its runtime. A Session suspended by its idle policy is asked to
// resume first; a Session suspended through spec.suspend is not, because only
// its owner should decide when it runs again.
func WaitReady(ctx context.Context, cl client.Client, key client.ObjectKey, poll time.Duration) (string, error) {
	if poll <= 0 {
		poll = DefaultReadyPollInterval
	}
	for {
		var session kelos.Session
		if err := cl.Get(ctx, key, &session); apierrors.IsNotFound(err) {
			// The caller typically reads through an informer cache moments
			// after creating the Session, so a miss means the watch has not
			// caught up rather than that the Session is gone. Keep waiting.
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("waiting for Session %q to appear: %w", key.Name, ctx.Err())
			case <-time.After(poll):
			}
			continue
		} else if err != nil {
			return "", fmt.Errorf("getting Session %q: %w", key.Name, err)
		}
		switch {
		case session.Status.Phase == kelos.SessionPhaseReady && session.Status.PodName != "":
			return session.Status.PodName, nil
		case session.Status.Phase == kelos.SessionPhaseFailed:
			return "", fmt.Errorf("Session %q failed: %s", key.Name, session.Status.Message)
		case sessionsuspend.IsIdlePolicySuspended(&session):
			if _, _, err := sessionsuspend.RequestResume(ctx, cl, key); err != nil {
				return "", err
			}
		case session.Spec.Suspend != nil && *session.Spec.Suspend:
			return "", fmt.Errorf("Session %q is suspended", key.Name)
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for Session %q to become ready: %w", key.Name, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// AcknowledgeResume records that a client connected for a pending idle-resume
// request, which extends the window during which the Session is protected from
// being suspended again. A Session with no pending request is left untouched.
func AcknowledgeResume(ctx context.Context, cl client.Client, key client.ObjectKey) error {
	var session kelos.Session
	if err := cl.Get(ctx, key, &session); err != nil {
		return fmt.Errorf("getting Session %q: %w", key.Name, err)
	}
	if !sessionsuspend.ResumeRequested(&session) || sessionsuspend.ResumeAcknowledged(&session) {
		return nil
	}
	_, err := sessionsuspend.AcknowledgeResume(ctx, cl, key, session.Annotations[sessionsuspend.ResumeRequestAnnotation])
	return err
}
