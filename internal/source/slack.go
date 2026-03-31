package source

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SlackSource discovers work items from Slack messages via Socket Mode.
// A background goroutine listens for Slack events and accumulates WorkItems
// in an internal queue. Discover() drains the queue on each call.
type SlackSource struct {
	// BotToken is the Bot User OAuth Token (xoxb-...).
	BotToken string
	// AppToken is the App-Level Token for Socket Mode (xapp-...).
	AppToken string
	// TriggerCommand is an optional slash command or message prefix.
	// When empty, every non-threaded message triggers a task.
	TriggerCommand string
	// Channels restricts listening to specific channel IDs. Empty = all.
	Channels []string
	// AllowedUsers restricts which user IDs can trigger tasks. Empty = all.
	AllowedUsers []string

	mu         sync.Mutex
	pending    []WorkItem
	counter    int
	startOnce  sync.Once
	startErr   error
	selfUserID string
	api        *slack.Client
	cancel     context.CancelFunc
}

// Discover returns accumulated WorkItems since the last call.
// On the first call it starts the Socket Mode listener.
func (s *SlackSource) Discover(ctx context.Context) ([]WorkItem, error) {
	s.startOnce.Do(func() {
		s.startErr = s.Start(ctx)
	})
	if s.startErr != nil {
		return nil, fmt.Errorf("Starting Slack source: %w", s.startErr)
	}

	s.mu.Lock()
	items := s.pending
	s.pending = nil
	s.mu.Unlock()

	return items, nil
}

// Start connects to Slack via Socket Mode and begins listening for events.
func (s *SlackSource) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("slack-source")

	s.api = slack.New(
		s.BotToken,
		slack.OptionAppLevelToken(s.AppToken),
	)

	authResp, err := s.api.AuthTestContext(ctx)
	if err != nil {
		return fmt.Errorf("Slack auth test failed: %w", err)
	}
	s.selfUserID = authResp.UserID
	log.Info("Authenticated with Slack", "botUserID", s.selfUserID)

	sm := socketmode.New(s.api)

	bgCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	go func() {
		if err := sm.RunContext(bgCtx); err != nil {
			log.Error(err, "Socket Mode connection closed")
		}
	}()

	go func() {
		for evt := range sm.Events {
			switch evt.Type {
			case socketmode.EventTypeEventsAPI:
				s.handleEventsAPI(sm, evt)
			case socketmode.EventTypeSlashCommand:
				s.handleSlashCommand(sm, evt)
			}
		}
	}()

	return nil
}

// Stop shuts down the Socket Mode listener.
func (s *SlackSource) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *SlackSource) handleEventsAPI(sm *socketmode.Client, evt socketmode.Event) {
	log := ctrl.Log.WithName("slack-source")

	eventsAPIEvent, ok := evt.Data.(slackevents.EventsAPIEvent)
	if !ok {
		sm.Ack(*evt.Request)
		return
	}
	sm.Ack(*evt.Request)

	innerEvent, ok := eventsAPIEvent.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok {
		return
	}

	body, ok := shouldProcess(innerEvent.User, innerEvent.SubType, innerEvent.ThreadTimeStamp, innerEvent.Text, s.selfUserID, s.TriggerCommand)
	if !ok {
		return
	}

	if !matchesChannel(innerEvent.Channel, s.Channels) {
		return
	}
	if !matchesUser(innerEvent.User, s.AllowedUsers) {
		return
	}

	userName := innerEvent.User
	enrichCtx, enrichCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer enrichCancel()

	if info, err := s.api.GetUserInfoContext(enrichCtx, innerEvent.User); err == nil {
		userName = info.RealName
		if userName == "" {
			userName = info.Name
		}
	}

	permalink := ""
	if link, err := s.api.GetPermalinkContext(enrichCtx, &slack.PermalinkParameters{
		Channel: innerEvent.Channel,
		Ts:      innerEvent.TimeStamp,
	}); err == nil {
		permalink = link
	}

	channelName := innerEvent.Channel
	if info, err := s.api.GetConversationInfoContext(enrichCtx, &slack.GetConversationInfoInput{
		ChannelID: innerEvent.Channel,
	}); err == nil {
		channelName = info.Name
	}

	s.mu.Lock()
	s.counter++
	item := buildWorkItem(innerEvent.TimeStamp, s.counter, userName, body, permalink, channelName, innerEvent.Channel)
	s.pending = append(s.pending, item)
	s.mu.Unlock()

	log.Info("Queued Slack message as work item", "number", item.Number, "user", userName, "channel", channelName)
}

func (s *SlackSource) handleSlashCommand(sm *socketmode.Client, evt socketmode.Event) {
	log := ctrl.Log.WithName("slack-source")

	cmd, ok := evt.Data.(slack.SlashCommand)
	if !ok {
		sm.Ack(*evt.Request)
		return
	}
	sm.Ack(*evt.Request)

	if cmd.UserID == s.selfUserID {
		return
	}
	if !matchesChannel(cmd.ChannelID, s.Channels) {
		return
	}
	if !matchesUser(cmd.UserID, s.AllowedUsers) {
		return
	}

	body := strings.TrimSpace(cmd.Text)
	if body == "" {
		return
	}

	userName := cmd.UserName
	channelName := cmd.ChannelName

	s.mu.Lock()
	s.counter++
	itemID := fmt.Sprintf("%s:%s:%s", cmd.ChannelID, cmd.Command, cmd.TriggerID)
	item := buildWorkItem(itemID, s.counter, userName, body, "", channelName, cmd.ChannelID)
	s.pending = append(s.pending, item)
	s.mu.Unlock()

	log.Info("Queued slash command as work item", "number", item.Number, "user", userName, "channel", channelName)
}

// shouldProcess decides whether a Slack message should become a WorkItem.
// It returns the processed body text and true if the message should trigger,
// or an empty string and false if it should be ignored.
func shouldProcess(userID, subtype, threadTS, text, selfUserID, triggerCmd string) (string, bool) {
	if userID == selfUserID {
		return "", false
	}
	switch subtype {
	case "bot_message", "message_changed", "message_deleted", "message_replied":
		return "", false
	}
	if threadTS != "" {
		return "", false
	}
	if text == "" {
		return "", false
	}

	if triggerCmd != "" {
		if !strings.HasPrefix(text, triggerCmd) {
			return "", false
		}
		body := strings.TrimSpace(strings.TrimPrefix(text, triggerCmd))
		if body == "" {
			return "", false
		}
		return body, true
	}

	return text, true
}

// matchesChannel returns true if channelID is in the allowed list,
// or if the allowed list is empty (all channels permitted).
func matchesChannel(channelID string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if id == channelID {
			return true
		}
	}
	return false
}

// matchesUser returns true if userID is in the allowed list,
// or if the allowed list is empty (all users permitted).
func matchesUser(userID string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, id := range allowed {
		if id == userID {
			return true
		}
	}
	return false
}

// buildWorkItem constructs a WorkItem from Slack message fields.
func buildWorkItem(id string, number int, userName, body, permalink, channelName, channelID string) WorkItem {
	return WorkItem{
		ID:     id,
		Number: number,
		Title:  userName,
		Body:   body,
		URL:    permalink,
		Labels: []string{channelName, channelID},
		Kind:   "SlackMessage",
	}
}
