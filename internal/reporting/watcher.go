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
	// AnnotationGitHubReporting indicates that GitHub reporting is enabled for
	// this Task.
	AnnotationGitHubReporting = "kelos.dev/github-reporting"

	// AnnotationSourceKind records whether the source item is an issue or pull-request.
	AnnotationSourceKind = "kelos.dev/source-kind"

	// AnnotationSourceNumber records the issue or pull request number.
	AnnotationSourceNumber = "kelos.dev/source-number"

	// AnnotationGitHubCommentID stores the GitHub comment ID for the status
	// comment created by the reporter so subsequent updates edit the same
	// comment.
	AnnotationGitHubCommentID = "kelos.dev/github-comment-id"

	// AnnotationGitHubReportPhase records the last Task phase that was
	// reported to GitHub, preventing duplicate API calls on re-list.
	AnnotationGitHubReportPhase = "kelos.dev/github-report-phase"

	// AnnotationSlackReporting indicates that Slack reporting is enabled
	// for this Task.
	AnnotationSlackReporting = "kelos.dev/slack-reporting"

	// AnnotationSlackChannel records the Slack channel ID where the
	// originating message was posted.
	AnnotationSlackChannel = "kelos.dev/slack-channel"

	// AnnotationSlackThreadTS records the originating message timestamp,
	// used as thread_ts for posting replies.
	AnnotationSlackThreadTS = "kelos.dev/slack-thread-ts"

	// AnnotationSlackReportPhase records the last Task phase that was
	// reported to Slack, preventing duplicate API calls on re-list.
	AnnotationSlackReportPhase = "kelos.dev/slack-report-phase"
)

// TaskReporter watches Tasks and reports status changes to GitHub.
type TaskReporter struct {
	Client   client.Client
	Reporter *GitHubReporter
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

	// Skip if we already reported this phase
	if annotations[AnnotationGitHubReportPhase] == desiredPhase {
		return nil
	}

	commentID := int64(0)
	if idStr, ok := annotations[AnnotationGitHubCommentID]; ok {
		var err error
		commentID, err = strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return fmt.Errorf("parsing %s annotation %q: %w", AnnotationGitHubCommentID, idStr, err)
		}
	}

	// Build the comment body
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
		// Create a new comment
		log.Info("Creating GitHub status comment", "task", task.Name, "number", number, "phase", desiredPhase)
		newID, err := tr.Reporter.CreateComment(ctx, number, body)
		if err != nil {
			return fmt.Errorf("creating GitHub comment for task %s: %w", task.Name, err)
		}
		commentID = newID
	} else {
		// Update the existing comment
		log.Info("Updating GitHub status comment", "task", task.Name, "number", number, "phase", desiredPhase, "commentID", commentID)
		if err := tr.Reporter.UpdateComment(ctx, commentID, body); err != nil {
			return fmt.Errorf("updating GitHub comment %d for task %s: %w", commentID, task.Name, err)
		}
	}

	if err := tr.persistReportingState(ctx, task, commentID, desiredPhase); err != nil {
		return err
	}

	return nil
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

	msg := FormatSlackTransitionMessage(desiredPhase, task.Name, task.Status.Message, task.Status.Results)

	log.Info("Posting Slack thread reply", "task", task.Name, "channel", channel, "phase", desiredPhase)
	replyTS, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg)
	if err != nil {
		return fmt.Errorf("posting Slack reply for task %s: %w", task.Name, err)
	}

	// Track the accepted message so the activity loop can update it.
	if desiredPhase == "accepted" && replyTS != "" {
		tr.setActivityTarget(task.UID, replyTS, msg)
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

// updateProgress reads the agent's pod logs and posts a progress update to the
// Slack thread if the latest assistant text has changed since the last post.
// All errors are non-fatal — progress updates are best-effort.
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

	msg := SlackMessage{Text: text}
	log.V(1).Info("Posting Slack progress update", "task", task.Name)
	replyTS, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg)
	if err != nil {
		log.Error(err, "Failed to post Slack progress update", "task", task.Name)
		return nil
	}

	tr.setLastProgress(task.UID, text)

	// Point the activity indicator at this new progress message so
	// subsequent activity ticks update its context block.
	if replyTS != "" {
		tr.setActivityTarget(task.UID, replyTS, msg)
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

// clearProgressCache removes the cached progress for a task.
func (tr *SlackTaskReporter) clearProgressCache(uid types.UID) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	delete(tr.lastProgress, uid)
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
