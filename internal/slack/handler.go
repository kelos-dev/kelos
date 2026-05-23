package slack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	stdlog "log"
	"os"
	"strconv"
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

const (
	enrichCallTimeout               = 5 * time.Second
	postMessageTimeout              = 5 * time.Second
	defaultSelfHandoffMaxPerThread  = 4
	selfHandoffEnabledEnv           = "SLACK_SELF_HANDOFF_ENABLED"
	selfHandoffMaxPerThreadEnv      = "SLACK_SELF_HANDOFF_MAX_PER_THREAD"
	selfHandoffStopNoticeFormatText = "Auto-handoff stopped after %d Cody handoffs in this thread. Please continue manually if needed."
)

// SlackHandler handles Slack messages via Socket Mode and routes them to
// matching TaskSpawners. It is the centralized equivalent of the per-TaskSpawner
// SlackSource that previously ran in each spawner pod.
type SlackHandler struct {
	client                  client.Client
	log                     logr.Logger
	taskBuilder             *taskbuilder.TaskBuilder
	api                     *goslack.Client
	sm                      *socketmode.Client
	botUserID               string
	botID                   string
	joinMessage             string
	selfHandoffEnabled      bool
	selfHandoffMaxPerThread int
	cancel                  context.CancelFunc
}

// NewSlackHandler creates a new handler. Call Start to begin listening.
// If joinMessageFile is non-empty, the bot posts its contents when added to a channel.
func NewSlackHandler(ctx context.Context, cl client.Client, botToken, appToken, joinMessageFile string, log logr.Logger) (*SlackHandler, error) {
	api := goslack.New(botToken, goslack.OptionAppLevelToken(appToken))

	authResp, err := api.AuthTestContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("Slack auth test failed: %w", err)
	}

	tb, err := taskbuilder.NewTaskBuilder(cl)
	if err != nil {
		return nil, fmt.Errorf("Creating task builder: %w", err)
	}

	log.Info("Authenticated with Slack", "botUserID", authResp.UserID, "botID", authResp.BotID)
	selfHandoffEnabled := parseBoolEnv(os.Getenv(selfHandoffEnabledEnv))
	selfHandoffMaxPerThread := parsePositiveIntEnv(os.Getenv(selfHandoffMaxPerThreadEnv), defaultSelfHandoffMaxPerThread)
	if selfHandoffEnabled {
		log.Info("Slack self-handoff enabled", "maxPerThread", selfHandoffMaxPerThread)
	}

	var joinMessage string
	if joinMessageFile != "" {
		data, err := os.ReadFile(joinMessageFile)
		if err != nil {
			return nil, fmt.Errorf("reading join message file %s: %w", joinMessageFile, err)
		}
		joinMessage = strings.TrimSpace(string(data))
		if joinMessage != "" {
			log.Info("Join channel message enabled", "file", joinMessageFile)
		}
	}

	return &SlackHandler{
		client:                  cl,
		log:                     log,
		taskBuilder:             tb,
		api:                     api,
		sm:                      newSocketModeClient(api),
		botUserID:               authResp.UserID,
		botID:                   authResp.BotID,
		joinMessage:             joinMessage,
		selfHandoffEnabled:      selfHandoffEnabled,
		selfHandoffMaxPerThread: selfHandoffMaxPerThread,
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

	switch inner := eventsAPIEvent.InnerEvent.Data.(type) {
	case *slackevents.MemberJoinedChannelEvent:
		h.handleMemberJoinedChannel(ctx, inner)
	case *slackevents.AppMentionEvent:
		h.handleAppMentionEvent(ctx, inner)
	case *slackevents.MessageEvent:
		h.handleMessageEvent(ctx, inner)
	default:
		return
	}
}

// handleMemberJoinedChannel posts a welcome message when the bot itself is
// added to a channel. The message text is read from the file pointed to by
// joinMessageFile. If the file is not configured or cannot be read, the
// event is silently ignored.
func (h *SlackHandler) handleMemberJoinedChannel(ctx context.Context, evt *slackevents.MemberJoinedChannelEvent) {
	if evt.User != h.botUserID {
		return
	}

	if h.joinMessage == "" {
		return
	}

	h.log.Info("Bot added to channel, posting join message", "channel", evt.Channel)

	postCtx, cancel := context.WithTimeout(ctx, postMessageTimeout)
	defer cancel()
	if _, _, err := h.api.PostMessageContext(postCtx, evt.Channel, goslack.MsgOptionText(h.joinMessage, false)); err != nil {
		h.log.Error(err, "Failed to post join message", "channel", evt.Channel)
	}
}

func (h *SlackHandler) handleMessageEvent(ctx context.Context, innerEvent *slackevents.MessageEvent) {
	hasContent := innerEvent.Text != "" ||
		(innerEvent.Message != nil && len(innerEvent.Message.Attachments) > 0)
	if h.handleSelfHandoffEvent(ctx, selfHandoffEvent{
		UserID:    innerEvent.User,
		BotID:     innerEvent.BotID,
		Text:      innerEvent.Text,
		ChannelID: innerEvent.Channel,
		ThreadTS:  innerEvent.ThreadTimeStamp,
		Timestamp: innerEvent.TimeStamp,
	}, hasContent) {
		return
	}
	if !shouldProcess(innerEvent.User, innerEvent.SubType, hasContent, h.botUserID, innerEvent.BotID, h.botID) {
		h.log.V(1).Info("Message filtered by shouldProcess",
			"user", innerEvent.User, "subtype", innerEvent.SubType, "botID", innerEvent.BotID, "channel", innerEvent.Channel)
		return
	}

	// Enrich message with user info, permalink, channel name
	msg := h.enrichMessage(ctx, innerEvent)

	// For thread replies, fetch full thread context so the agent sees
	// the entire conversation. Spawner filters (mention + triggers)
	// decide whether to process the message.
	if innerEvent.ThreadTimeStamp != "" {
		body, err := FetchThreadContext(ctx, h.api, innerEvent.Channel, innerEvent.ThreadTimeStamp, h.botUserID)
		if err != nil {
			h.log.Error(err, "Failed to fetch thread context, falling back to message text",
				"channel", innerEvent.Channel, "threadTS", innerEvent.ThreadTimeStamp)
		} else {
			msg.Body = body
			msg.HasThreadContext = true
		}
	}

	h.routeMessage(ctx, msg)
}

func (h *SlackHandler) handleAppMentionEvent(ctx context.Context, innerEvent *slackevents.AppMentionEvent) {
	hasContent := innerEvent.Text != ""
	if h.handleSelfHandoffEvent(ctx, selfHandoffEvent{
		UserID:    innerEvent.User,
		BotID:     innerEvent.BotID,
		Text:      innerEvent.Text,
		ChannelID: innerEvent.Channel,
		ThreadTS:  innerEvent.ThreadTimeStamp,
		Timestamp: innerEvent.TimeStamp,
	}, hasContent) {
		return
	}
	if !shouldProcess(innerEvent.User, "", hasContent, h.botUserID, innerEvent.BotID, h.botID) {
		h.log.V(1).Info("App mention filtered by shouldProcess",
			"user", innerEvent.User, "botID", innerEvent.BotID, "channel", innerEvent.Channel)
		return
	}

	msg := h.enrichAppMention(ctx, innerEvent)

	if innerEvent.ThreadTimeStamp != "" {
		body, err := FetchThreadContext(ctx, h.api, innerEvent.Channel, innerEvent.ThreadTimeStamp, h.botUserID)
		if err != nil {
			h.log.Error(err, "Failed to fetch thread context, falling back to message text",
				"channel", innerEvent.Channel, "threadTS", innerEvent.ThreadTimeStamp)
		} else {
			msg.Body = body
			msg.HasThreadContext = true
		}
	}

	h.routeMessage(ctx, msg)
}

type selfHandoffEvent struct {
	UserID    string
	BotID     string
	Text      string
	ChannelID string
	ThreadTS  string
	Timestamp string
}

func (h *SlackHandler) handleSelfHandoffEvent(ctx context.Context, event selfHandoffEvent, hasContent bool) bool {
	if !h.selfHandoffEnabled {
		return false
	}
	if !isSelfAuthoredSlackMessage(event.UserID, event.BotID, h.botUserID, h.botID) {
		return false
	}
	if !hasContent {
		h.log.V(1).Info("Self handoff ignored: message has no content", "channel", event.ChannelID)
		return true
	}
	if h.api == nil {
		h.log.V(1).Info("Self handoff ignored: Slack API client is not configured", "channel", event.ChannelID)
		return true
	}
	if event.ThreadTS == "" {
		h.log.V(1).Info("Self handoff ignored: message is not a thread reply", "channel", event.ChannelID)
		return true
	}

	commands := extractSelfHandoffCommands(event.Text, h.botUserID)
	if len(commands) != 1 {
		h.log.V(1).Info("Self handoff ignored: expected exactly one handoff command",
			"channel", event.ChannelID, "commandCount", len(commands))
		return true
	}
	command := commands[0]

	replies, err := FetchThreadReplies(ctx, h.api, event.ChannelID, event.ThreadTS)
	if err != nil {
		h.log.Error(err, "Self handoff ignored: failed to fetch thread context",
			"channel", event.ChannelID, "threadTS", event.ThreadTS)
		return true
	}

	priorCount, mostRecent := countPriorSelfHandoffCommands(replies, event.Timestamp, h.botUserID, h.botID)
	if priorCount >= h.selfHandoffMaxPerThread {
		h.log.Info("Self handoff stopped: max per thread reached",
			"channel", event.ChannelID, "threadTS", event.ThreadTS, "priorCount", priorCount, "max", h.selfHandoffMaxPerThread)
		h.postSelfHandoffStopNotice(ctx, event.ChannelID, event.ThreadTS, replies)
		return true
	}
	if mostRecent == command.Normalized {
		h.log.Info("Self handoff ignored: duplicate consecutive command",
			"channel", event.ChannelID, "threadTS", event.ThreadTS, "command", command.Normalized)
		return true
	}

	routeBotID := event.BotID
	if routeBotID == "" {
		routeBotID = h.botID
	}
	msg := &SlackMessageData{
		UserID:           event.UserID,
		ChannelID:        event.ChannelID,
		UserName:         h.resolveUserName(ctx, event.UserID, routeBotID),
		Text:             command.Normalized,
		Body:             FormatThreadContext(replies, h.botUserID),
		ThreadTS:         event.ThreadTS,
		Timestamp:        event.Timestamp,
		Permalink:        h.getPermalink(ctx, event.ChannelID, event.Timestamp),
		HasThreadContext: true,
		BotID:            routeBotID,
		IsBotMessage:     true,
	}

	h.log.Info("Routing self handoff command",
		"channel", event.ChannelID, "threadTS", event.ThreadTS, "target", command.Target, "priorCount", priorCount)
	h.routeMessage(ctx, msg)
	return true
}

// HandleReportedTerminalMessage evaluates a terminal Slack reporter message for
// a Cody self-handoff. This covers final responses written by the reporter via
// chat.update, which do not reliably arrive as ordinary new-message events.
func (h *SlackHandler) HandleReportedTerminalMessage(ctx context.Context, msg reporting.SlackTerminalMessage) error {
	h.handleSelfHandoffEvent(ctx, selfHandoffEvent{
		UserID:    h.botUserID,
		BotID:     h.botID,
		Text:      msg.Text,
		ChannelID: msg.ChannelID,
		ThreadTS:  msg.ThreadTS,
		Timestamp: msg.MessageTS,
	}, strings.TrimSpace(msg.Text) != "")
	return nil
}

func (h *SlackHandler) postSelfHandoffStopNotice(ctx context.Context, channelID, threadTS string, replies []goslack.Message) {
	if threadHasSelfHandoffStopNotice(replies, h.botUserID, h.botID) {
		return
	}

	text := fmt.Sprintf(selfHandoffStopNoticeFormatText, h.selfHandoffMaxPerThread)
	postCtx, cancel := context.WithTimeout(ctx, postMessageTimeout)
	defer cancel()
	if _, _, err := h.api.PostMessageContext(postCtx, channelID, goslack.MsgOptionText(text, false), goslack.MsgOptionTS(threadTS)); err != nil {
		h.log.Error(err, "Failed to post self handoff stop notice", "channel", channelID, "threadTS", threadTS)
	}
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
	if h.routeSessionFollowUp(ctx, msg) {
		return
	}

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

		// Check channel, mention, and trigger filters
		if !MatchesSpawner(slackCfg, msg, h.botUserID) {
			spawnerLog.V(1).Info("Message did not match spawner filters",
				"channel", msg.ChannelID, "triggerCount", len(slackCfg.Triggers), "botID", msg.BotID, "isBotMessage", msg.IsBotMessage)
			continue
		}

		taskMsg := *msg

		if slackSessionEnabled(slackCfg) {
			spawnerLog.Info("Message matches session-enabled TaskSpawner — creating AgentSession turn", "channel", msg.ChannelID, "user", msg.UserID)
			if err := h.createSessionAndFirstTurn(ctx, spawner, &taskMsg); err != nil {
				spawnerLog.Error(err, "Failed to create AgentSession turn")
				threadTS := rootThreadTS(msg)
				h.postSessionNotice(ctx, msg.ChannelID, threadTS, fmt.Sprintf("Could not start Cody session: %v", err))
			}
			continue
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
	// Truncate spawner name to leave room for "-slack-" (7) + hash (12) = 19 chars
	name := spawner.Name
	const maxPrefix = 63 - 7 - 12 // 44
	if len([]rune(name)) > maxPrefix {
		name = strings.TrimRight(string([]rune(name)[:maxPrefix]), "-.")
	}
	taskName := fmt.Sprintf("%s-slack-%s", name, shortHash)

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
	task.Annotations[reporting.AnnotationSlackUserID] = msg.UserID
	if msg.BotID != "" {
		task.Annotations["kelos.dev/slack-bot-id"] = msg.BotID
	}

	// Only enable Slack reporting label and thread_ts for real message
	// timestamps. Slash commands have no thread to reply to, so skip the
	// label to avoid the reporter listing them every cycle.
	if !msg.IsSlashCommand {
		if task.Labels == nil {
			task.Labels = make(map[string]string)
		}
		task.Labels[reporting.LabelSlackReporting] = "enabled"

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
// enriching it with user info and permalink.
func (h *SlackHandler) enrichMessage(ctx context.Context, event *slackevents.MessageEvent) *SlackMessageData {
	userName := h.resolveUserName(ctx, event.User, event.Username)
	permalink := h.getPermalink(ctx, event.Channel, event.TimeStamp)

	body := event.Text
	// Message is always non-nil after UnmarshalJSON (see MessageEvent docs).
	if event.Message != nil && len(event.Message.Attachments) > 0 {
		if attachText := formatAttachments(event.Message.Attachments); attachText != "" {
			if body != "" {
				body = body + "\n" + attachText
			} else {
				body = attachText
			}
		}
	}

	return &SlackMessageData{
		UserID:    event.User,
		ChannelID: event.Channel,
		UserName:  userName,
		Text:      event.Text,
		Body:      body,
		ThreadTS:  event.ThreadTimeStamp,
		Timestamp: event.TimeStamp,
		Permalink: permalink,
		BotID:     event.BotID,
		IsBotMessage: event.BotID != "" ||
			event.SubType == "bot_message",
	}
}

func (h *SlackHandler) enrichAppMention(ctx context.Context, event *slackevents.AppMentionEvent) *SlackMessageData {
	userName := h.resolveUserName(ctx, event.User, event.BotID)
	permalink := h.getPermalink(ctx, event.Channel, event.TimeStamp)

	return &SlackMessageData{
		UserID:       event.User,
		ChannelID:    event.Channel,
		UserName:     userName,
		Text:         event.Text,
		Body:         event.Text,
		ThreadTS:     event.ThreadTimeStamp,
		Timestamp:    event.TimeStamp,
		Permalink:    permalink,
		BotID:        event.BotID,
		IsBotMessage: event.BotID != "",
	}
}

func (h *SlackHandler) resolveUserName(ctx context.Context, userID, fallback string) string {
	userName := userID
	if userName == "" {
		userName = fallback
	}
	if userID == "" || h.api == nil {
		return userName
	}
	userCtx, userCancel := context.WithTimeout(ctx, enrichCallTimeout)
	defer userCancel()
	if info, err := h.api.GetUserInfoContext(userCtx, userID); err == nil {
		userName = info.RealName
		if userName == "" {
			userName = info.Name
		}
	}
	return userName
}

func (h *SlackHandler) getPermalink(ctx context.Context, channel, ts string) string {
	if h.api == nil || channel == "" || ts == "" {
		return ""
	}
	linkCtx, linkCancel := context.WithTimeout(ctx, enrichCallTimeout)
	defer linkCancel()
	link, err := h.api.GetPermalinkContext(linkCtx, &goslack.PermalinkParameters{
		Channel: channel,
		Ts:      ts,
	})
	if err != nil {
		return ""
	}
	return link
}

func parseBoolEnv(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "true" || value == "1" || value == "yes"
}

func parsePositiveIntEnv(value string, fallback int) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
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
// It filters out self messages and message subtypes we don't handle.
// Bot-authored messages with a non-self bot_id are routed to TaskSpawner
// matching, where each Slack config decides whether that bot is allowlisted.
// hasContent should be true when the message has text or attachments.
func shouldProcess(userID, subtype string, hasContent bool, selfUserID, botID, selfBotID string) bool {
	if !hasContent {
		return false
	}
	if userID != "" && userID == selfUserID {
		return false
	}
	if botID != "" {
		return selfBotID == "" || botID != selfBotID
	}
	switch subtype {
	case "bot_message", "message_changed", "message_deleted", "message_replied":
		return false
	default:
		return true
	}
}
