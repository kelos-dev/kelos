package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	stdlog "log"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	goslack "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/api/v1alpha1"
	"github.com/kelos-dev/kelos/internal/reporting"
	"github.com/kelos-dev/kelos/internal/taskbuilder"
)

const enrichCallTimeout = 5 * time.Second

// SlackHandler handles Slack messages via Socket Mode and routes them to
// matching TaskSpawners. It is the centralized equivalent of the per-TaskSpawner
// SlackSource that previously ran in each spawner pod.
type SlackHandler struct {
	client      client.Client
	log         logr.Logger
	taskBuilder *taskbuilder.TaskBuilder
	api         *goslack.Client
	sm          *socketmode.Client
	botUserID   string
	cancel      context.CancelFunc
}

// NewSlackHandler creates a new handler. Call Start to begin listening.
func NewSlackHandler(ctx context.Context, cl client.Client, botToken, appToken string, log logr.Logger) (*SlackHandler, error) {
	api := goslack.New(botToken, goslack.OptionAppLevelToken(appToken))

	authResp, err := api.AuthTestContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("Slack auth test failed: %w", err)
	}

	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		return nil, fmt.Errorf("Creating task builder: %w", err)
	}

	log.Info("Authenticated with Slack", "botUserID", authResp.UserID)

	return &SlackHandler{
		client:      cl,
		log:         log,
		taskBuilder: tb,
		api:         api,
		sm:          newSocketModeClient(api),
		botUserID:   authResp.UserID,
	}, nil
}

// Start connects to Slack via Socket Mode and begins listening for events.
// It blocks until the context is cancelled.
func (h *SlackHandler) Start(ctx context.Context) error {
	bgCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel

	go func() {
		if err := h.sm.RunContext(bgCtx); err != nil {
			h.log.Error(err, "Socket Mode connection closed with error")
		} else {
			h.log.Info("Socket Mode connection closed cleanly")
		}
	}()

	for {
		select {
		case <-bgCtx.Done():
			return bgCtx.Err()
		case evt, ok := <-h.sm.Events:
			if !ok {
				h.log.Info("Socket Mode events channel closed, exiting listener")
				return fmt.Errorf("Socket Mode events channel closed unexpectedly")
			}
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				h.handleEventsAPI(bgCtx, evt)
			case socketmode.EventTypeSlashCommand:
				h.handleSlashCommand(bgCtx, evt)
			default:
				h.log.V(1).Info("Unhandled Socket Mode event type", "type", evt.Type)
			}
		}
	}
}

// Stop shuts down the Socket Mode listener.
func (h *SlackHandler) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
}

func (h *SlackHandler) handleEventsAPI(ctx context.Context, evt socketmode.Event) {
	eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		h.sm.Ack(*evt.Request)
		return
	}
	h.sm.Ack(*evt.Request)

	innerEvent, ok := eventsAPIEvent.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok {
		return
	}

	if !shouldProcess(innerEvent.User, innerEvent.SubType, innerEvent.Text, h.botUserID) {
		h.log.V(1).Info("Message filtered by shouldProcess",
			"user", innerEvent.User, "subtype", innerEvent.SubType, "channel", innerEvent.Channel)
		return
	}

	// Enrich message with user info, permalink, channel name
	msg := h.enrichMessage(ctx, innerEvent)

	// For thread replies, fetch full thread context so the agent sees
	// the entire conversation. Spawner filters (mentionUserIDs,
	// triggerCommand) decide whether to process the message.
	if innerEvent.ThreadTimeStamp != "" {
		body, err := FetchThreadContext(ctx, h.api, innerEvent.Channel, innerEvent.ThreadTimeStamp, h.botUserID)
		if err != nil {
			h.log.Error(err, "Failed to fetch thread context", "channel", innerEvent.Channel, "threadTS", innerEvent.ThreadTimeStamp)
			return
		}
		msg.Body = body
		msg.HasThreadContext = true
	}

	h.routeMessage(ctx, msg)
}

func (h *SlackHandler) handleSlashCommand(ctx context.Context, evt socketmode.Event) {
	cmd, ok := evt.Data.(goslack.SlashCommand)
	if !ok {
		h.sm.Ack(*evt.Request)
		return
	}
	h.sm.Ack(*evt.Request)

	if cmd.UserID == h.botUserID {
		return
	}

	body := strings.TrimSpace(cmd.Text)
	if body == "" {
		return
	}

	msg := &SlackMessageData{
		UserID:         cmd.UserID,
		ChannelID:      cmd.ChannelID,
		ChannelName:    cmd.ChannelName,
		UserName:       cmd.UserName,
		Text:           cmd.Text,
		Body:           body,
		IsSlashCommand: true,
		SlashCommandID: fmt.Sprintf("%s:%s:%s", cmd.ChannelID, cmd.Command, cmd.TriggerID),
	}

	h.routeMessage(ctx, msg)
}

// routeMessage finds all matching TaskSpawners and creates tasks for each.
func (h *SlackHandler) routeMessage(ctx context.Context, msg *SlackMessageData) {
	spawners, err := h.getMatchingSpawners(ctx)
	if err != nil {
		h.log.Error(err, "Failed to get matching spawners")
		return
	}

	if len(spawners) == 0 {
		h.log.V(1).Info("No matching TaskSpawners for Slack message", "channel", msg.ChannelID)
		return
	}

	for _, spawner := range spawners {
		spawnerLog := h.log.WithValues("spawner", spawner.Name, "namespace", spawner.Namespace)

		// Check if suspended
		if spawner.Spec.Suspend != nil && *spawner.Spec.Suspend {
			spawnerLog.V(1).Info("Skipping suspended TaskSpawner")
			continue
		}

		// Check max concurrency
		if spawner.Spec.MaxConcurrency != nil && *spawner.Spec.MaxConcurrency > 0 {
			if int32(spawner.Status.ActiveTasks) >= *spawner.Spec.MaxConcurrency {
				spawnerLog.Info("Max concurrency reached, dropping message",
					"activeTasks", spawner.Status.ActiveTasks,
					"maxConcurrency", *spawner.Spec.MaxConcurrency)
				continue
			}
		}

		slackCfg := spawner.Spec.When.Slack

		// Check channel and user filters
		if !MatchesSpawner(slackCfg, msg) {
			spawnerLog.V(1).Info("Message did not match spawner filters",
				"channel", msg.ChannelID, "mentionRequired", len(slackCfg.MentionUserIDs) > 0)
			continue
		}

		// Slash commands skip the trigger check — the command name itself acts
		// as the trigger and cmd.Text only contains the arguments.
		var body string
		var ok bool
		if msg.IsSlashCommand {
			body, ok = msg.Body, true
		} else {
			body, ok = ProcessTriggerCommand(msg.Text, slackCfg.TriggerCommand)
		}
		if !ok {
			spawnerLog.V(1).Info("Message did not match trigger command",
				"triggerCommand", slackCfg.TriggerCommand)
			continue
		}

		// Use thread context body if available (thread replies), otherwise use trigger-processed body
		taskMsg := *msg
		if !msg.HasThreadContext {
			taskMsg.Body = body
		}

		spawnerLog.Info("Message matches TaskSpawner — creating task", "channel", msg.ChannelID, "user", msg.UserID)

		if err := h.createTask(ctx, spawner, &taskMsg); err != nil {
			spawnerLog.Error(err, "Failed to create task")
			continue
		}
	}
}

// getMatchingSpawners returns all TaskSpawners that have a Slack source configured.
func (h *SlackHandler) getMatchingSpawners(ctx context.Context) ([]*v1alpha1.TaskSpawner, error) {
	var spawnerList v1alpha1.TaskSpawnerList
	if err := h.client.List(ctx, &spawnerList, &client.ListOptions{}); err != nil {
		return nil, err
	}

	var matching []*v1alpha1.TaskSpawner
	for i := range spawnerList.Items {
		spawner := &spawnerList.Items[i]
		if spawner.Spec.When.Slack != nil {
			matching = append(matching, spawner)
		}
	}

	return matching, nil
}

// createTask creates a Task for the given TaskSpawner from a Slack message.
func (h *SlackHandler) createTask(ctx context.Context, spawner *v1alpha1.TaskSpawner, msg *SlackMessageData) error {
	templateVars := ExtractSlackWorkItem(msg)

	// Build unique task name using a hash of the message identifier
	hashInput := fmt.Sprintf("%s-%s", msg.ChannelID, msg.Timestamp)
	if msg.IsSlashCommand {
		hashInput = msg.SlashCommandID
	}
	sum := sha256.Sum256([]byte(hashInput))
	shortHash := hex.EncodeToString(sum[:])[:12]
	taskName := fmt.Sprintf("%s-slack-%s", spawner.Name, shortHash)
	if len(taskName) > 63 {
		taskName = strings.TrimRight(taskName[:63], "-.")
	}

	// Resolve GVK for owner reference
	gvks, _, err := h.client.Scheme().ObjectKinds(spawner)
	if err != nil || len(gvks) == 0 {
		return fmt.Errorf("Failed to get GVK for TaskSpawner: %w", err)
	}
	gvk := gvks[0]

	task, err := h.taskBuilder.BuildTask(
		taskName,
		spawner.Namespace,
		&spawner.Spec.TaskTemplate,
		templateVars,
		&taskbuilder.SpawnerRef{
			Name:       spawner.Name,
			UID:        string(spawner.UID),
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
		},
	)
	if err != nil {
		return fmt.Errorf("Building task: %w", err)
	}

	// Add Slack reporting annotations
	if task.Annotations == nil {
		task.Annotations = make(map[string]string)
	}
	task.Annotations[reporting.AnnotationSlackReporting] = "enabled"
	task.Annotations[reporting.AnnotationSlackChannel] = msg.ChannelID

	// Only set thread_ts for real message timestamps (not slash command composite IDs)
	if !msg.IsSlashCommand {
		threadTS := msg.Timestamp
		if msg.ThreadTS != "" {
			threadTS = msg.ThreadTS
		}
		task.Annotations[reporting.AnnotationSlackThreadTS] = threadTS
	}

	if err := h.client.Create(ctx, task); err != nil {
		if apierrors.IsAlreadyExists(err) {
			h.log.Info("Task already exists, skipping", "task", taskName)
			return nil
		}
		return fmt.Errorf("Creating task: %w", err)
	}

	h.log.Info("Created task from Slack message", "task", taskName, "spawner", spawner.Name)
	return nil
}

// enrichMessage builds a SlackMessageData from a raw Slack message event,
// enriching it with user info, permalink, and channel name.
func (h *SlackHandler) enrichMessage(ctx context.Context, event *slackevents.MessageEvent) *SlackMessageData {
	userName := event.User
	userCtx, userCancel := context.WithTimeout(ctx, enrichCallTimeout)
	defer userCancel()
	if info, err := h.api.GetUserInfoContext(userCtx, event.User); err == nil {
		userName = info.RealName
		if userName == "" {
			userName = info.Name
		}
	}

	permalink := ""
	linkCtx, linkCancel := context.WithTimeout(ctx, enrichCallTimeout)
	defer linkCancel()
	if link, err := h.api.GetPermalinkContext(linkCtx, &goslack.PermalinkParameters{
		Channel: event.Channel,
		Ts:      event.TimeStamp,
	}); err == nil {
		permalink = link
	}

	channelName := event.Channel
	chanCtx, chanCancel := context.WithTimeout(ctx, enrichCallTimeout)
	defer chanCancel()
	if info, err := h.api.GetConversationInfoContext(chanCtx, &goslack.GetConversationInfoInput{
		ChannelID: event.Channel,
	}); err == nil {
		channelName = info.Name
	}

	return &SlackMessageData{
		UserID:      event.User,
		ChannelID:   event.Channel,
		ChannelName: channelName,
		UserName:    userName,
		Text:        event.Text,
		Body:        event.Text,
		ThreadTS:    event.ThreadTimeStamp,
		Timestamp:   event.TimeStamp,
		Permalink:   permalink,
	}
}

// newSocketModeClient creates a Socket Mode client with an stderr logger.
// Set SLACK_SOCKET_DEBUG=1 to enable verbose WebSocket frame logging.
func newSocketModeClient(api *goslack.Client) *socketmode.Client {
	opts := []socketmode.Option{
		socketmode.OptionLog(stdlog.New(os.Stderr, "socketmode: ", stdlog.LstdFlags|stdlog.Lshortfile)),
	}
	if os.Getenv("SLACK_SOCKET_DEBUG") == "1" {
		opts = append(opts, socketmode.OptionDebug(true))
	}
	return socketmode.New(api, opts...)
}

// shouldProcess decides whether a Slack message should be processed.
// It filters out bot messages, self-messages, and message subtypes we don't handle.
func shouldProcess(userID, subtype, text, selfUserID string) bool {
	if userID == selfUserID {
		return false
	}
	switch subtype {
	case "bot_message", "message_changed", "message_deleted", "message_replied":
		return false
	}
	return text != ""
}
