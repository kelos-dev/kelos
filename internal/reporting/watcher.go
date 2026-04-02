package reporting

import (
	"context"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	// AnnotationSlackReplyTS stores the message timestamp of the status
	// reply so subsequent updates edit the same message.
	AnnotationSlackReplyTS = "kelos.dev/slack-reply-ts"

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

// SlackTaskReporter watches Tasks and reports status changes to Slack
// as thread replies on the originating message.
type SlackTaskReporter struct {
	Client   client.Client
	Reporter SlackMessenger
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
		return nil
	}

	var msg SlackMessage
	switch desiredPhase {
	case "accepted":
		msg = FormatSlackAccepted(task.Name)
	case "succeeded":
		msg = FormatSlackSucceeded(task.Name, task.Status.Results)
	case "failed":
		msg = FormatSlackFailed(task.Name, task.Status.Message, task.Status.Results)
	}

	log.Info("Posting Slack thread reply", "task", task.Name, "channel", channel, "phase", desiredPhase)
	replyTS, err := tr.Reporter.PostThreadReply(ctx, channel, threadTS, msg)
	if err != nil {
		return fmt.Errorf("posting Slack reply for task %s: %w", task.Name, err)
	}

	if err := tr.persistSlackReportingState(ctx, task, replyTS, desiredPhase); err != nil {
		return err
	}

	return nil
}

func (tr *SlackTaskReporter) persistSlackReportingState(ctx context.Context, task *kelosv1alpha1.Task, replyTS, desiredPhase string) error {
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var current kelosv1alpha1.Task
		if err := tr.Client.Get(ctx, client.ObjectKeyFromObject(task), &current); err != nil {
			return err
		}

		if current.Annotations == nil {
			current.Annotations = make(map[string]string)
		}
		current.Annotations[AnnotationSlackReplyTS] = replyTS
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
