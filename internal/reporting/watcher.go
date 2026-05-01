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
	// AnnotationGitHubReporting indicates that GitHub comment reporting is
	// enabled for this Task.
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

	// AnnotationGitHubChecks indicates that GitHub Check Run reporting is
	// enabled for this Task.
	AnnotationGitHubChecks = "kelos.dev/github-checks"

	// AnnotationGitHubCheckRunID stores the GitHub Check Run ID so
	// subsequent updates target the same check run.
	AnnotationGitHubCheckRunID = "kelos.dev/github-check-run-id"

	// AnnotationGitHubCheckReportPhase records the last Task phase that was
	// reported via the Checks API.
	AnnotationGitHubCheckReportPhase = "kelos.dev/github-check-report-phase"

	// AnnotationSourceSHA records the head commit SHA for pull request sources.
	AnnotationSourceSHA = "kelos.dev/source-sha"

	// AnnotationGitHubCheckName stores the Check Run name configured on the
	// TaskSpawner so the reporter can use it without access to the spec.
	AnnotationGitHubCheckName = "kelos.dev/github-check-name"
)

// TaskReporter watches Tasks and reports status changes to GitHub.
type TaskReporter struct {
	Client         client.Client
	Reporter       *GitHubReporter
	ChecksReporter *ChecksReporter
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
	commentID  int64
	phase      string
	checkRunID int64
	checkPhase string
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
	e := c.entries[uid]
	e.commentID = commentID
	e.phase = phase
	c.entries[uid] = e
}

func (c *ReportStateCache) storeCheckRun(uid types.UID, checkRunID int64, checkPhase string) {
	if c == nil || uid == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[uid]
	e.checkRunID = checkRunID
	e.checkPhase = checkPhase
	c.entries[uid] = e
}

// ReportTaskStatus checks a Task's current phase against its last reported
// phase and creates or updates the GitHub status comment and/or Check Run as
// needed.
func (tr *TaskReporter) ReportTaskStatus(ctx context.Context, task *kelosv1alpha1.Task) error {
	annotations := task.Annotations
	if annotations == nil {
		return nil
	}

	commentEnabled := annotations[AnnotationGitHubReporting] == "enabled"
	checksEnabled := annotations[AnnotationGitHubChecks] == "enabled"

	if !commentEnabled && !checksEnabled {
		return nil
	}

	if commentEnabled {
		if err := tr.reportViaComment(ctx, task); err != nil {
			return err
		}
	}

	if checksEnabled {
		if tr.ChecksReporter == nil {
			ctrl.Log.WithName("reporter").Info("Checks reporting annotation is set but ChecksReporter is nil, skipping", "task", task.Name)
		} else if err := tr.reportViaCheckRun(ctx, task); err != nil {
			return err
		}
	}

	return nil
}

// reportViaComment creates or updates a GitHub issue/PR comment.
func (tr *TaskReporter) reportViaComment(ctx context.Context, task *kelosv1alpha1.Task) error {
	log := ctrl.Log.WithName("reporter")

	annotations := task.Annotations
	numberStr, ok := annotations[AnnotationSourceNumber]
	if !ok {
		return nil
	}
	number, err := strconv.Atoi(numberStr)
	if err != nil {
		return fmt.Errorf("parsing source number %q: %w", numberStr, err)
	}

	var desiredPhase string
	switch task.Status.Phase {
	case kelosv1alpha1.TaskPhasePending, kelosv1alpha1.TaskPhaseRunning, kelosv1alpha1.TaskPhaseWaiting:
		desiredPhase = "accepted"
	case kelosv1alpha1.TaskPhaseSucceeded:
		desiredPhase = "succeeded"
	case kelosv1alpha1.TaskPhaseFailed:
		desiredPhase = "failed"
	default:
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

// reportViaCheckRun creates or updates a GitHub Check Run.
func (tr *TaskReporter) reportViaCheckRun(ctx context.Context, task *kelosv1alpha1.Task) error {
	log := ctrl.Log.WithName("reporter")

	annotations := task.Annotations
	headSHA := annotations[AnnotationSourceSHA]
	if headSHA == "" {
		log.Info("Skipping Check Run: source SHA annotation is not set", "task", task.Name)
		return nil
	}

	checkName := annotations[AnnotationGitHubCheckName]
	if checkName == "" {
		spawnerName := task.Labels["kelos.dev/taskspawner"]
		if spawnerName == "" {
			spawnerName = task.Name
		}
		checkName = "Kelos: " + spawnerName
	}

	var desiredPhase string
	var status, conclusion string
	var output *checkRunOutput
	switch task.Status.Phase {
	case kelosv1alpha1.TaskPhasePending, kelosv1alpha1.TaskPhaseRunning, kelosv1alpha1.TaskPhaseWaiting:
		desiredPhase = "in_progress"
		status = "in_progress"
		output = &checkRunOutput{
			Title:   checkName + " — In Progress",
			Summary: fmt.Sprintf("Agent task `%s` is in progress", task.Name),
		}
	case kelosv1alpha1.TaskPhaseSucceeded:
		desiredPhase = "succeeded"
		status = "completed"
		conclusion = "success"
		output = &checkRunOutput{
			Title:   checkName + " — Succeeded",
			Summary: fmt.Sprintf("Agent task `%s` has succeeded", task.Name),
		}
	case kelosv1alpha1.TaskPhaseFailed:
		desiredPhase = "failed"
		status = "completed"
		conclusion = "failure"
		output = &checkRunOutput{
			Title:   checkName + " — Failed",
			Summary: fmt.Sprintf("Agent task `%s` has failed", task.Name),
		}
	default:
		return nil
	}

	// The in-memory cache is the source of truth when an entry exists — the
	// reporter writes it before persisting the annotation, so it is always at
	// least as fresh as the informer-backed read.
	var (
		lastCheckPhase string
		checkRunID     int64
	)
	cached, hasCached := tr.Cache.load(task.UID)
	if hasCached && cached.checkRunID != 0 {
		lastCheckPhase = cached.checkPhase
		checkRunID = cached.checkRunID
	} else {
		lastCheckPhase = annotations[AnnotationGitHubCheckReportPhase]
		if idStr, ok := annotations[AnnotationGitHubCheckRunID]; ok {
			var err error
			checkRunID, err = strconv.ParseInt(idStr, 10, 64)
			if err != nil {
				return fmt.Errorf("parsing %s annotation %q: %w", AnnotationGitHubCheckRunID, idStr, err)
			}
		}
	}

	if lastCheckPhase == desiredPhase {
		if !hasCached || cached.checkRunID == 0 {
			return nil
		}
		if annotations[AnnotationGitHubCheckReportPhase] == desiredPhase &&
			annotations[AnnotationGitHubCheckRunID] == strconv.FormatInt(checkRunID, 10) {
			return nil
		}
		return tr.persistCheckRunState(ctx, task, checkRunID, desiredPhase)
	}

	if checkRunID == 0 {
		log.Info("Creating GitHub Check Run", "task", task.Name, "name", checkName, "phase", desiredPhase)
		newID, err := tr.ChecksReporter.CreateCheckRun(ctx, checkName, headSHA, status, conclusion, output)
		if err != nil {
			return fmt.Errorf("creating GitHub Check Run for task %s: %w", task.Name, err)
		}
		checkRunID = newID
	} else {
		log.Info("Updating GitHub Check Run", "task", task.Name, "checkRunID", checkRunID, "phase", desiredPhase)
		if err := tr.ChecksReporter.UpdateCheckRun(ctx, checkRunID, status, conclusion, output); err != nil {
			return fmt.Errorf("updating GitHub Check Run %d for task %s: %w", checkRunID, task.Name, err)
		}
	}

	// Record the latest state before persisting the annotation so a concurrent
	// reconcile that races the annotation Update still sees the correct check
	// run ID via the in-memory cache and skips re-creation.
	tr.Cache.storeCheckRun(task.UID, checkRunID, desiredPhase)

	return tr.persistCheckRunState(ctx, task, checkRunID, desiredPhase)
}

func (tr *TaskReporter) persistReportingState(ctx context.Context, task *kelosv1alpha1.Task, commentID int64, desiredPhase string) error {
	return tr.persistAnnotations(ctx, task, map[string]string{
		AnnotationGitHubCommentID:   strconv.FormatInt(commentID, 10),
		AnnotationGitHubReportPhase: desiredPhase,
	})
}

func (tr *TaskReporter) persistCheckRunState(ctx context.Context, task *kelosv1alpha1.Task, checkRunID int64, desiredPhase string) error {
	return tr.persistAnnotations(ctx, task, map[string]string{
		AnnotationGitHubCheckRunID:       strconv.FormatInt(checkRunID, 10),
		AnnotationGitHubCheckReportPhase: desiredPhase,
	})
}

func (tr *TaskReporter) persistAnnotations(ctx context.Context, task *kelosv1alpha1.Task, annotations map[string]string) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.Task
		if err := tr.Client.Get(ctx, client.ObjectKeyFromObject(task), &current); err != nil {
			return err
		}

		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		for k, v := range annotations {
			current.Annotations[k] = v
		}

		if err := tr.Client.Update(ctx, &current); err != nil {
			return err
		}

		task.Annotations = current.Annotations
		return nil
	}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("persisting annotations on task %s: task no longer exists", task.Name)
		}
		return fmt.Errorf("persisting annotations on task %s: %w", task.Name, err)
	}

	return nil
}
