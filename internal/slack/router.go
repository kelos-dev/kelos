package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/router"
)

// routeToRouters creates a routing decision Task for every TaskRouter whose
// Slack source matches the message. The router controller dispatches the work
// once the decision completes.
func (h *SlackHandler) routeToRouters(ctx context.Context, msg *SlackMessageData) {
	routers, err := h.getMatchingRouters(ctx)
	if err != nil {
		h.log.Error(err, "Failed to get matching TaskRouters")
		return
	}

	for _, taskRouter := range routers {
		routerLog := h.log.WithValues("taskRouter", taskRouter.Name, "namespace", taskRouter.Namespace)

		if taskRouter.Spec.Suspend != nil && *taskRouter.Spec.Suspend {
			routerLog.V(1).Info("Skipping suspended TaskRouter")
			continue
		}
		if !MatchesSpawner(taskRouter.Spec.When.Slack, msg, h.botUserID) {
			continue
		}

		atLimit, err := h.routerAtConcurrencyLimit(ctx, taskRouter)
		if err != nil {
			routerLog.Error(err, "Failed to count in-flight routing decisions")
			continue
		}
		if atLimit {
			routerLog.Info("Max concurrency reached, dropping message", "channel", msg.ChannelID)
			continue
		}

		routerLog.Info("Message matches TaskRouter — creating routing decision", "channel", msg.ChannelID, "user", msg.UserID)
		if err := h.createDecisionTask(ctx, taskRouter, msg); err != nil {
			routerLog.Error(err, "Failed to create routing decision Task")
		}
	}
}

// getMatchingRouters returns the TaskRouters that have a Slack source.
func (h *SlackHandler) getMatchingRouters(ctx context.Context) ([]*kelos.TaskRouter, error) {
	var routerList kelos.TaskRouterList
	if err := h.client.List(ctx, &routerList, &client.ListOptions{}); err != nil {
		return nil, err
	}

	matching := make([]*kelos.TaskRouter, 0, len(routerList.Items))
	for i := range routerList.Items {
		taskRouter := &routerList.Items[i]
		if taskRouter.Spec.When.Slack != nil {
			matching = append(matching, taskRouter)
		}
	}
	return matching, nil
}

// routerAtConcurrencyLimit reports whether the router already has as many
// in-flight decisions as spec.maxConcurrency allows.
func (h *SlackHandler) routerAtConcurrencyLimit(ctx context.Context, taskRouter *kelos.TaskRouter) (bool, error) {
	limit := taskRouter.Spec.MaxConcurrency
	if limit == nil || *limit <= 0 {
		return false, nil
	}

	var tasks kelos.TaskList
	if err := h.client.List(ctx, &tasks,
		client.InNamespace(taskRouter.Namespace),
		client.MatchingLabels{router.LabelTaskRouter: taskRouter.Name},
	); err != nil {
		return false, err
	}

	active := int32(0)
	for i := range tasks.Items {
		task := &tasks.Items[i]
		// The label can match a Task left behind by a since-recreated router of
		// the same name; only this router's own decisions count against it.
		if !metav1.IsControlledBy(task, taskRouter) {
			continue
		}
		if task.Status.Phase != kelos.TaskPhaseSucceeded && task.Status.Phase != kelos.TaskPhaseFailed {
			active++
		}
	}
	return active >= *limit, nil
}

// createDecisionTask creates the Task that decides where a Slack message goes.
// It carries the initiator's reporting metadata, so the decision's own
// explanation reaches the originating thread and the dispatched Task inherits
// the same destination.
func (h *SlackHandler) createDecisionTask(ctx context.Context, taskRouter *kelos.TaskRouter, msg *SlackMessageData) error {
	vars := ExtractSlackWorkItem(msg)
	itemID, _ := vars["ID"].(string)

	task, err := router.BuildDecisionTask(taskRouter, itemID, vars)
	if err != nil {
		return err
	}
	task.Annotations[router.AnnotationDispatchSuffix] = slackDispatchSuffix(msg)

	if err := controllerutil.SetControllerReference(taskRouter, task, h.client.Scheme()); err != nil {
		return fmt.Errorf("setting TaskRouter %s owner on Task %s: %w", taskRouter.Name, task.Name, err)
	}

	// The address travels with the decision so the dispatched Task can inherit
	// it, but the decision itself stays silent: the reporting cycle only lists
	// Tasks carrying the reporting label, and the router adds that label after
	// the fact, on the one outcome whose own output is the answer. A routed
	// request is therefore narrated only by the work it dispatched.
	task.Annotations[reporting.AnnotationSlackChannel] = msg.ChannelID
	task.Annotations[reporting.AnnotationSlackUserID] = msg.UserID
	if !msg.IsSlashCommand {
		threadTS := msg.Timestamp
		if msg.ThreadTS != "" {
			threadTS = msg.ThreadTS
		}
		task.Annotations[reporting.AnnotationSlackThreadTS] = threadTS
	}

	if err := h.client.Create(ctx, task); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The decision Task name is derived from the message, so a
			// redelivery decides once rather than twice.
			h.log.Info("Routing decision already exists, skipping", "task", task.Name)
			return nil
		}
		return fmt.Errorf("creating routing decision Task %s: %w", task.Name, err)
	}

	h.log.Info("Created routing decision from Slack message", "task", task.Name, "taskRouter", taskRouter.Name)
	return nil
}

// slackDispatchSuffix returns the name suffix the matching TaskSpawner would
// have used for this message, so a router dispatch and a direct spawner fire
// collapse into one Task.
func slackDispatchSuffix(msg *SlackMessageData) string {
	hashInput := fmt.Sprintf("%s-%s", msg.ChannelID, msg.Timestamp)
	if msg.IsSlashCommand {
		hashInput = msg.SlashCommandID
	}
	sum := sha256.Sum256([]byte(hashInput))
	return fmt.Sprintf("slack-%s", hex.EncodeToString(sum[:])[:12])
}
