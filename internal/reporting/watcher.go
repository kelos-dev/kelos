package reporting

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/slack-go/slack"
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

	// AnnotationSlackReporting indicates that Slack reporting is enabled
	// for this Task.
	AnnotationSlackReporting = "kelos.dev/slack-reporting"

	// AnnotationSlackChannel records the Slack channel ID where the
	// originating message was posted.
	AnnotationSlackChannel = "kelos.dev/slack-channel"

	// AnnotationSlackThreadTS records the originating message timestamp,
	// used as thread_ts for posting replies.
	AnnotationSlackThreadTS = "kelos.dev/slack-thread-ts"

	// AnnotationSlackUserID records the Slack user ID of the person who
	// triggered the task.
	AnnotationSlackUserID = "kelos.dev/slack-user-id"

	// AnnotationSlackReportPhase records the last Task phase that was
	// reported to Slack, preventing duplicate API calls on re-list.
	AnnotationSlackReportPhase = "kelos.dev/slack-report-phase"

	// LabelSlackReporting is applied to Tasks created from Slack so that
	// the reporting and activity loops can list only relevant Tasks.
	LabelSlackReporting = "kelos.dev/slack-reporting"
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

// SlackMessenger is the interface for posting and updating Slack messages.
type SlackMessenger interface {
	PostThreadReply(ctx context.Context, channel, threadTS string, msg SlackMessage) (string, error)
	UpdateMessage(ctx context.Context, channel, messageTS string, msg SlackMessage) error
}

// activityState tracks the target message for activity indicator updates.
// The activity indicator is rendered as an additional context element on
// whichever message is currently the latest in the thread (the accepted
// message initially, then each progress snapshot as they are posted).
type activityState struct {
	// MessageTS is the Slack timestamp of the message being updated with
	// the activity context element.
	MessageTS string
	// BaseMsg holds the original blocks and text of the target message,
	// before the activity context element was appended.
	BaseMsg SlackMessage
	// LastText is the last activity string appended, used for deduplication.
	LastText string
	// Tick is incremented on each activity cycle for rotating idle phrases.
	Tick int
}

// SlackTaskReporter watches Tasks and reports status changes to Slack
// as thread replies on the originating message. When a ProgressReader is
// configured, it also posts periodic progress updates extracted from the
// agent's pod logs while the task is running. When an ActivityReader is
// configured, it posts and updates short activity indicators between
// progress snapshots.
type SlackTaskReporter struct {
	Client         client.Client
	Reporter       SlackMessenger
	ProgressReader ProgressReader
	ActivityReader ActivityReader

	mu           sync.Mutex
	lastProgress map[types.UID]string         // taskUID -> last posted text
	progressTS   map[types.UID]string         // taskUID -> message ts of the progress reply
	activity     map[types.UID]*activityState // taskUID -> current activity indicator
}

// ReportTaskStatus checks a Task's current phase against its last reported
// phase and creates or updates the Slack thread reply as needed.
func (tr *SlackTaskReporter) ReportTaskStatus(ctx context.Context, task *kelosv1alpha1.Task) error {
	log := ctrl.Log.WithName("slack-reporter")

	annotations := task.Annotations
	if annotations == nil {
		return nil
	}

	if annotations[AnnotationSlackReporting] != "enabled" {
		return nil
	}

	channel := annotations[AnnotationSlackChannel]
	threadTS := annotations[AnnotationSlackThreadTS]
	if channel == "" || threadTS == "" {
		return nil
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

	if annotations[AnnotationSlackReportPhase] == desiredPhase {
		// Task is still running and we already posted the "accepted" message.
		// Try to post a progress update from the agent's pod logs.
		if desiredPhase == "accepted" {
			return tr.updateProgress(ctx, task)
		}
		return nil
	}

	msgs := FormatSlackTransitionMessage(desiredPhase, task.Name, task.Status.Message, task.Status.Results)

	// For terminal phases, try to edit the existing progress message
	// in-place. When the response is a single message, this keeps the
	// thread compact. When the response was split into multiple messages,
	// replace the progress message with the first part and post the rest
	// as new replies.
	if desiredPhase == "succeeded" || desiredPhase == "failed" {
		if progressTS := tr.getProgressTS(task.UID); progressTS != "" {
			log.Info("Updating Slack progress message with final result", "task", task.Name, "channel", channel, "phase", desiredPhase)
			if err := tr.Reporter.UpdateMessage(ctx, channel, progressTS, msgs[0]); err != nil {
				log.Error(err, "Failed to update progress message with final result, posting new reply", "task", task.Name)
			} else {
				// Post any continuation messages as new thread replies.
				for _, msg := range msgs[1:] {
					if _, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg); err != nil {
						log.Error(err, "Failed to post continuation message", "task", task.Name)
					}
				}
				tr.clearProgressCache(task.UID)
				tr.clearActivityState(task.UID)
				return tr.persistSlackReportingState(ctx, task, desiredPhase)
			}
		}
	}

	// Post all messages as thread replies.
	var firstReplyTS string
	for i, msg := range msgs {
		log.Info("Posting Slack thread reply", "task", task.Name, "channel", channel, "phase", desiredPhase, "part", i+1, "total", len(msgs))
		replyTS, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg)
		if err != nil {
			return fmt.Errorf("posting Slack reply for task %s (part %d/%d): %w", task.Name, i+1, len(msgs), err)
		}
		if i == 0 {
			firstReplyTS = replyTS
		}
	}

	// Track the accepted message so the activity loop can update it.
	if desiredPhase == "accepted" && firstReplyTS != "" {
		tr.setActivityTarget(task.UID, firstReplyTS, msgs[0])
	}

	// Clean up caches when reporting a terminal phase.
	if desiredPhase == "succeeded" || desiredPhase == "failed" {
		tr.clearProgressCache(task.UID)
		tr.clearActivityState(task.UID)
	}

	if err := tr.persistSlackReportingState(ctx, task, desiredPhase); err != nil {
		return err
	}

	return nil
}

func (tr *SlackTaskReporter) persistSlackReportingState(ctx context.Context, task *kelosv1alpha1.Task, desiredPhase string) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.Task
		if err := tr.Client.Get(ctx, client.ObjectKeyFromObject(task), &current); err != nil {
			return err
		}

		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[AnnotationSlackReportPhase] = desiredPhase

		if err := tr.Client.Update(ctx, &current); err != nil {
			return err
		}

		task.Annotations = current.Annotations
		return nil
	}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("persisting Slack reporting annotations on task %s: task no longer exists", task.Name)
		}
		return fmt.Errorf("persisting Slack reporting annotations on task %s: %w", task.Name, err)
	}

	return nil
}

// updateProgress reads the agent's pod logs and updates the progress message
// in the Slack thread. On the first call for a task, it posts a new reply and
// records the message timestamp. Subsequent calls edit the same message
// in-place so the thread stays clean. All errors are non-fatal — progress
// updates are best-effort.
func (tr *SlackTaskReporter) updateProgress(ctx context.Context, task *kelosv1alpha1.Task) error {
	if tr.ProgressReader == nil {
		return nil
	}

	log := ctrl.Log.WithName("slack-reporter")

	podName := task.Status.PodName
	if podName == "" {
		return nil
	}

	annotations := task.Annotations
	channel := annotations[AnnotationSlackChannel]
	threadTS := annotations[AnnotationSlackThreadTS]
	if channel == "" || threadTS == "" {
		return nil
	}

	containerName := task.Spec.Type
	if containerName == "" {
		containerName = "claude-code"
	}

	text := tr.ProgressReader.ReadProgress(ctx, task.Namespace, podName, containerName, task.Spec.Type)
	if text == "" {
		return nil
	}

	if !tr.shouldPostProgress(task.UID, text) {
		return nil
	}

	msg := FormatProgressMessage(text, task.Name)

	// If we already have a progress message for this task, edit it in-place.
	if replyTS := tr.getProgressTS(task.UID); replyTS != "" {
		log.V(1).Info("Updating Slack progress message", "task", task.Name)
		if err := tr.Reporter.UpdateMessage(ctx, channel, replyTS, msg); err != nil {
			log.Error(err, "Failed to update Slack progress message", "task", task.Name)
			// Clear the stale TS so the next tick posts a fresh reply.
			tr.setProgressTS(task.UID, "")
			return nil
		}
		tr.setLastProgress(task.UID, text)
		// Update the activity indicator's base message so subsequent
		// activity ticks render against the new progress content.
		tr.setActivityTarget(task.UID, replyTS, msg)
		return nil
	}

	// First progress update — post a new reply and record its timestamp.
	log.V(1).Info("Posting Slack progress update", "task", task.Name)
	replyTS, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg)
	if err != nil {
		log.Error(err, "Failed to post Slack progress update", "task", task.Name)
		return nil
	}

	tr.setLastProgress(task.UID, text)
	tr.setProgressTS(task.UID, replyTS)

	// Atomically switch the activity target to the new progress message and
	// reset the old message's indicator so only one message in the thread
	// shows an active indicator at a time.
	if replyTS != "" {
		tr.resetAndSetActivityTarget(ctx, task.UID, channel, replyTS, msg)
	}

	return nil
}

// shouldPostProgress returns true if the text differs from the last posted
// progress for this task.
func (tr *SlackTaskReporter) shouldPostProgress(uid types.UID, text string) bool {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.lastProgress == nil {
		return true
	}
	return tr.lastProgress[uid] != text
}

// setLastProgress records the most recently posted progress text for a task.
func (tr *SlackTaskReporter) setLastProgress(uid types.UID, text string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.lastProgress == nil {
		tr.lastProgress = make(map[types.UID]string)
	}
	tr.lastProgress[uid] = text
}

// getProgressTS returns the Slack message timestamp of the progress reply
// for a task, or empty string if no progress has been posted yet.
func (tr *SlackTaskReporter) getProgressTS(uid types.UID) string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.progressTS == nil {
		return ""
	}
	return tr.progressTS[uid]
}

// setProgressTS records the Slack message timestamp for the progress reply.
func (tr *SlackTaskReporter) setProgressTS(uid types.UID, ts string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.progressTS == nil {
		tr.progressTS = make(map[types.UID]string)
	}
	tr.progressTS[uid] = ts
}

// clearProgressCache removes the cached progress for a task.
func (tr *SlackTaskReporter) clearProgressCache(uid types.UID) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.lastProgress, uid)
	delete(tr.progressTS, uid)
}

// SweepProgressCache removes entries for tasks that are no longer active.
// Call this after each reporting cycle with the set of UIDs seen in the
// current task list to prevent leaked entries from deleted tasks.
func (tr *SlackTaskReporter) SweepProgressCache(activeUIDs map[types.UID]bool) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	for uid := range tr.lastProgress {
		if !activeUIDs[uid] {
			delete(tr.lastProgress, uid)
			delete(tr.progressTS, uid)
		}
	}
	// Also clean progressTS entries that don't have a lastProgress entry.
	for uid := range tr.progressTS {
		if !activeUIDs[uid] {
			delete(tr.progressTS, uid)
		}
	}
	for uid := range tr.activity {
		if !activeUIDs[uid] {
			delete(tr.activity, uid)
		}
	}
}

// UpdateActivityIndicator reads the agent's current action from pod logs and
// updates the context block of the latest thread message (accepted or progress
// snapshot) to include a short activity line. This is called on a faster
// cadence (e.g. 5s) than the progress snapshot loop. All errors are non-fatal.
func (tr *SlackTaskReporter) UpdateActivityIndicator(ctx context.Context, task *kelosv1alpha1.Task) {
	if tr.ActivityReader == nil {
		return
	}

	log := ctrl.Log.WithName("slack-activity")

	annotations := task.Annotations
	if annotations == nil {
		return
	}
	if annotations[AnnotationSlackReporting] != "enabled" {
		return
	}
	if annotations[AnnotationSlackReportPhase] != "accepted" {
		return
	}

	// Only update activity for running tasks.
	if task.Status.Phase != kelosv1alpha1.TaskPhaseRunning {
		return
	}

	podName := task.Status.PodName
	if podName == "" {
		return
	}

	channel := annotations[AnnotationSlackChannel]
	if channel == "" {
		return
	}

	containerName := task.Spec.Type
	if containerName == "" {
		containerName = "claude-code"
	}

	text := tr.ActivityReader.ReadActivity(ctx, task.Namespace, podName, containerName, task.Spec.Type)

	tr.mu.Lock()
	state := tr.activity[task.UID]
	if state == nil || state.MessageTS == "" {
		// No target message yet — the accepted message hasn't been posted.
		tr.mu.Unlock()
		return
	}
	tick := state.Tick
	state.Tick++
	if text == "" {
		// No tool activity — use a rotating idle phrase.
		text = IdlePhrase(string(task.UID), tick)
	}
	if state.LastText == text {
		tr.mu.Unlock()
		return
	}
	messageTS := state.MessageTS
	baseMsg := state.BaseMsg
	tr.mu.Unlock()

	// Rebuild the message: base blocks + activity context element.
	msg := appendActivityContext(baseMsg, text)

	// appendActivityContext is a no-op for text-only messages (no blocks).
	// Skip the API call to avoid wasting Slack rate-limit quota.
	if len(msg.Blocks) == 0 {
		tr.mu.Lock()
		if s := tr.activity[task.UID]; s != nil && s.MessageTS == messageTS {
			s.LastText = text
		}
		tr.mu.Unlock()
		return
	}

	if err := tr.Reporter.UpdateMessage(ctx, channel, messageTS, msg); err != nil {
		log.V(1).Info("Failed to update activity indicator", "task", task.Name, "error", err)
		return
	}

	tr.mu.Lock()
	if s := tr.activity[task.UID]; s != nil && s.MessageTS == messageTS {
		s.LastText = text
	}
	tr.mu.Unlock()
}

// setActivityTarget records the message that the activity loop should update
// with context block activity indicators.
func (tr *SlackTaskReporter) setActivityTarget(uid types.UID, messageTS string, baseMsg SlackMessage) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if tr.activity == nil {
		tr.activity = make(map[types.UID]*activityState)
	}
	tr.activity[uid] = &activityState{
		MessageTS: messageTS,
		BaseMsg:   baseMsg,
	}
}

// resetAndSetActivityTarget atomically replaces the activity target for a task
// and then resets the old message (removing its activity indicator). By setting
// the new target under the lock before issuing the UpdateMessage call, a
// concurrent activity tick will always see the new target and never race
// against the in-flight reset of the old message.
func (tr *SlackTaskReporter) resetAndSetActivityTarget(ctx context.Context, uid types.UID, channel, newMessageTS string, newMsg SlackMessage) {
	tr.mu.Lock()
	state := tr.activity[uid]
	var oldTS string
	var baseMsg SlackMessage
	if state != nil && state.MessageTS != "" && state.MessageTS != newMessageTS {
		oldTS = state.MessageTS
		baseMsg = state.BaseMsg
	}
	// Set the new target atomically before releasing the lock.
	if tr.activity == nil {
		tr.activity = make(map[types.UID]*activityState)
	}
	tr.activity[uid] = &activityState{
		MessageTS: newMessageTS,
		BaseMsg:   newMsg,
	}
	tr.mu.Unlock()

	// Reset the old message outside the lock (best-effort).
	if oldTS != "" {
		log := ctrl.Log.WithName("slack-activity")
		if err := tr.Reporter.UpdateMessage(ctx, channel, oldTS, baseMsg); err != nil {
			log.V(1).Info("Failed to reset activity indicator on previous message", "messageTS", oldTS, "error", err)
		}
	}
}

// clearActivityState removes all activity state for a task.
func (tr *SlackTaskReporter) clearActivityState(uid types.UID) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.activity, uid)
}

// appendActivityContext returns a copy of baseMsg with an additional context
// element showing the current activity text. If the last block in baseMsg is
// already a ContextBlock, the activity element is appended to it. Otherwise
// a new ContextBlock is added.
func appendActivityContext(baseMsg SlackMessage, activityText string) SlackMessage {
	// If baseMsg has no blocks, there is nothing safe to attach to
	// without hiding the text content — skip the update.
	if len(baseMsg.Blocks) == 0 {
		return baseMsg
	}

	activityElement := slack.NewTextBlockObject(slack.MarkdownType, activityText, false, false)

	blocks := make([]slack.Block, len(baseMsg.Blocks))
	copy(blocks, baseMsg.Blocks)

	if ctx, ok := blocks[len(blocks)-1].(*slack.ContextBlock); ok {
		// Clone the context block and append the activity element.
		newElements := make([]slack.MixedElement, len(ctx.ContextElements.Elements), len(ctx.ContextElements.Elements)+1)
		copy(newElements, ctx.ContextElements.Elements)
		newElements = append(newElements, activityElement)
		newCtx := slack.NewContextBlock(ctx.BlockID, newElements...)
		blocks[len(blocks)-1] = newCtx
		return SlackMessage{Text: baseMsg.Text, Blocks: blocks}
	}

	// Last block is not a context block — append a new one.
	blocks = append(blocks, slack.NewContextBlock("", activityElement))
	return SlackMessage{Text: baseMsg.Text, Blocks: blocks}
}
