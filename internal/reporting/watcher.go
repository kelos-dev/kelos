package reporting

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	// AnnotationGitHubReporting indicates that GitHub reporting is enabled for
	// this Task.
	AnnotationGitHubReporting = "kelos.dev/github-reporting"

	// AnnotationSourceKind records whether the source item is an issue or pull-request.
	AnnotationSourceKind = "kelos.dev/source-kind"

	// AnnotationSourceNumber records the issue or pull request number.
	AnnotationSourceNumber = "kelos.dev/source-number"

	// AnnotationSourceOwner records the GitHub repository owner the event came
	// from. The webhook reporter uses this so it can post comments on the
	// originating repository even when it differs from the Task's Workspace.
	AnnotationSourceOwner = "kelos.dev/source-owner"

	// AnnotationSourceRepo records the GitHub repository name the event came
	// from. Pairs with AnnotationSourceOwner.
	AnnotationSourceRepo = "kelos.dev/source-repo"

	// AnnotationGitHubCommentID stores the GitHub comment ID for the status
	// comment created by the reporter so subsequent updates edit the same
	// comment.
	AnnotationGitHubCommentID = "kelos.dev/github-comment-id"

	// AnnotationGitHubReportPhase records the last Task phase that was
	// reported to GitHub, preventing duplicate API calls on re-list.
	AnnotationGitHubReportPhase = "kelos.dev/github-report-phase"
)

// TaskReporter watches Tasks and reports status changes to GitHub.
type TaskReporter struct {
	Client   client.Client
	Reporter *GitHubReporter
	// Cache backstops AnnotationGitHubCommentID and AnnotationGitHubReportPhase
	// when the persisted Update has not yet propagated to the controller-runtime
	// cache the caller reads from. Optional; when nil, the reporter relies on
	// annotations alone (which is sufficient for poll-driven callers).
	Cache *ReportStateCache
}

// ReportStateCache tracks the most recent comment ID and reported phase per
// Task UID so an event-driven reporter does not duplicate-create comments
// when two reconciles fire faster than the annotation Update propagates to
// the informer cache.
//
// NOTE: entries are not garbage-collected; for the expected workload (a few
// hundred Tasks per day) the footprint stays small. Add eviction if that
// changes.
type ReportStateCache struct {
	mu      sync.Mutex
	entries map[types.UID]reportStateEntry
}

type reportStateEntry struct {
	commentID int64
	phase     string
}

// NewReportStateCache returns an empty cache safe for concurrent use.
func NewReportStateCache() *ReportStateCache {
	return &ReportStateCache{entries: make(map[types.UID]reportStateEntry)}
}

func (c *ReportStateCache) load(uid types.UID) (reportStateEntry, bool) {
	if c == nil || uid == "" {
		return reportStateEntry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[uid]
	return e, ok
}

func (c *ReportStateCache) store(uid types.UID, commentID int64, phase string) {
	if c == nil || uid == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[uid] = reportStateEntry{commentID: commentID, phase: phase}
}

// ReportTaskStatus checks a Task's current phase against its last reported
// phase and creates or updates the GitHub status comment as needed.
func (tr *TaskReporter) ReportTaskStatus(ctx context.Context, task *kelosv1alpha1.Task) error {
	log := ctrl.Log.WithName("reporter")

	annotations := task.Annotations
	if annotations == nil {
		return nil
	}

	// Only process tasks with GitHub reporting enabled
	if annotations[AnnotationGitHubReporting] != "enabled" {
		return nil
	}

	numberStr, ok := annotations[AnnotationSourceNumber]
	if !ok {
		return nil
	}
	number, err := strconv.Atoi(numberStr)
	if err != nil {
		return fmt.Errorf("parsing source number %q: %w", numberStr, err)
	}

	// Determine the desired report phase based on the Task's current phase.
	var desiredPhase string
	switch task.Status.Phase {
	case kelosv1alpha1.TaskPhasePending, kelosv1alpha1.TaskPhaseRunning, kelosv1alpha1.TaskPhaseWaiting:
		desiredPhase = "accepted"
	case kelosv1alpha1.TaskPhaseSucceeded:
		desiredPhase = "succeeded"
	case kelosv1alpha1.TaskPhaseFailed:
		desiredPhase = "failed"
	default:
		// Task phase not yet set (empty string) — nothing to report
		return nil
	}

	// The in-memory cache is the source of truth when an entry exists — the
	// reporter writes it before persisting the annotation, so it is always at
	// least as fresh as the informer-backed read. The annotation is consulted
	// only when the cache has no entry (e.g., right after a controller
	// restart, before the cache has been repopulated).
	var (
		lastReportedPhase string
		commentID         int64
	)
	cached, hasCached := tr.Cache.load(task.UID)
	if hasCached {
		lastReportedPhase = cached.phase
		commentID = cached.commentID
	} else {
		lastReportedPhase = annotations[AnnotationGitHubReportPhase]
		if idStr, ok := annotations[AnnotationGitHubCommentID]; ok {
			parsed, err := strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				return fmt.Errorf("parsing %s annotation %q: %w", AnnotationGitHubCommentID, idStr, err)
			}
			commentID = parsed
		}
	}

	if lastReportedPhase == desiredPhase {
		// Annotation alone records this phase — nothing to do.
		if !hasCached {
			return nil
		}
		// Cache says we already reported. If the annotation also matches,
		// nothing to do; otherwise it lags (e.g., previous persist failed)
		// and we re-attempt persistence so the comment side stays untouched.
		if annotations[AnnotationGitHubReportPhase] == desiredPhase &&
			annotations[AnnotationGitHubCommentID] == strconv.FormatInt(commentID, 10) {
			return nil
		}
		return tr.persistReportingState(ctx, task, commentID, desiredPhase)
	}

	var body string
	switch desiredPhase {
	case "accepted":
		body = FormatAcceptedComment(task.Name)
	case "succeeded":
		body = FormatSucceededComment(task.Name)
	case "failed":
		body = FormatFailedComment(task.Name)
	}

	if commentID == 0 {
		log.Info("Creating GitHub status comment", "task", task.Name, "number", number, "phase", desiredPhase)
		newID, err := tr.Reporter.CreateComment(ctx, number, body)
		if err != nil {
			return fmt.Errorf("creating GitHub comment for task %s: %w", task.Name, err)
		}
		commentID = newID
	} else {
		log.Info("Updating GitHub status comment", "task", task.Name, "number", number, "phase", desiredPhase, "commentID", commentID)
		if err := tr.Reporter.UpdateComment(ctx, commentID, body); err != nil {
			return fmt.Errorf("updating GitHub comment %d for task %s: %w", commentID, task.Name, err)
		}
	}

	// Record the latest state before persisting the annotation so a concurrent
	// reconcile that races the annotation Update still sees the correct comment
	// ID via the in-memory cache and skips re-creation.
	tr.Cache.store(task.UID, commentID, desiredPhase)

	return tr.persistReportingState(ctx, task, commentID, desiredPhase)
}

func (tr *TaskReporter) persistReportingState(ctx context.Context, task *kelosv1alpha1.Task, commentID int64, desiredPhase string) error {
	commentIDStr := strconv.FormatInt(commentID, 10)

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.Task
		if err := tr.Client.Get(ctx, client.ObjectKeyFromObject(task), &current); err != nil {
			return err
		}

		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[AnnotationGitHubCommentID] = commentIDStr
		current.Annotations[AnnotationGitHubReportPhase] = desiredPhase

		if err := tr.Client.Update(ctx, &current); err != nil {
			return err
		}

		task.Annotations = current.Annotations
		return nil
	}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("persisting reporting annotations on task %s: task no longer exists", task.Name)
		}
		return fmt.Errorf("persisting reporting annotations on task %s: %w", task.Name, err)
	}

	return nil
}
