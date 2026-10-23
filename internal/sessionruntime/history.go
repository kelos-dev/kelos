package sessionruntime

import (
	"encoding/json"
	"sort"
	"time"
)

const (
	maxHistoryItemLimit       = 100
	maxHistoryByteLimit       = 1024 * 1024
	maxHistoryMessageBytes    = 32 * 1024
	maxHistoryToolOutputBytes = 8 * 1024
	maxHistoryDiffBytes       = 32 * 1024
	maxHistoryNoticeBytes     = 8 * 1024
	maxHistoryIdentifierBytes = 1024
	maxHistoryQuestions       = 5
	maxHistoryOptions         = 10

	historyTruncationMarker = "\n… history item truncated …\n"
)

type historyItem struct {
	events       []Event
	firstEventID int64
	lastEventID  int64
}

type historyTurn struct {
	user            *Event
	userEventID     int64
	started         bool
	activityEventID int64
	startedAt       *time.Time
	completed       bool
	hidden          bool
	interrupting    bool
}

type historyAssistant struct {
	event        Event
	text         *historyTextBuffer
	finalText    string
	firstEventID int64
	lastEventID  int64
}

type historyPendingEvent struct {
	event        Event
	firstEventID int64
}

// projectHistory converts provider event streams into replayable display items
// and separates live conversation state from transcript page boundaries.
func projectHistory(source []Event) ([]historyItem, HistoryState, []Event) {
	events := make([]Event, len(source))
	copy(events, source)
	for index := range events {
		if events[index].ID <= 0 {
			events[index].ID = int64(index + 1)
		}
	}

	turns := make(map[string]*historyTurn)
	pendingInputs := make(map[string]Event)
	for index := range events {
		event := events[index]
		if event.TurnID != "" {
			turn := turns[event.TurnID]
			if turn == nil {
				turn = &historyTurn{}
				turns[event.TurnID] = turn
			}
			switch event.Type {
			case EventUserMessage:
				copy := event
				turn.user = &copy
				turn.userEventID = event.ID
			case EventUserMessageUpdated:
				if turn.userEventID == 0 {
					turn.userEventID = event.ID
				}
				copy := event
				turn.user = &copy
			case EventUserMessageRemoved:
				turn.completed = true
				turn.hidden = true
			case EventTurnStarted:
				turn.started = true
				turn.startedAt = event.Timestamp
			case EventTurnInterrupting:
				turn.interrupting = true
			case EventTurnCompleted:
				turn.completed = true
				turn.hidden = event.Status == "merged"
				turn.interrupting = false
			}
			if event.Type != EventUserMessage && event.Type != EventUserMessageUpdated && event.Type != EventUserMessageRemoved && event.Type != EventTurnCompleted {
				turn.activityEventID = event.ID
				if turn.startedAt == nil {
					turn.startedAt = event.Timestamp
				}
			}
		}
		switch event.Type {
		case EventInputRequested:
			if event.InputID != "" {
				pendingInputs[event.InputID] = event
			}
		case EventInputResolved:
			delete(pendingInputs, event.InputID)
		}
	}

	state := HistoryState{}
	var pending *HistoryPendingTurn
	var pendingEventID int64
	var activeEventID int64
	for turnID, turn := range turns {
		if turn.activityEventID > 0 && !turn.completed && turn.activityEventID >= activeEventID {
			state.ActiveTurnID = turnID
			state.ActiveTurnStarted = turn.startedAt
			state.TurnInterrupting = turn.interrupting
			activeEventID = turn.activityEventID
		}
		if turn.user != nil && !turn.started && turn.activityEventID == 0 && !turn.completed && turn.userEventID >= pendingEventID {
			pending = &HistoryPendingTurn{
				TurnID:      turnID,
				Text:        boundedHistoryText(turn.user.Text, maxHistoryMessageBytes),
				Revision:    max(1, turn.user.Revision),
				Attachments: turn.user.Attachments,
			}
			pendingEventID = turn.userEventID
		}
	}
	state.PendingTurn = pending
	state.WaitingForInput = len(pendingInputs) > 0

	items := make([]historyItem, 0)
	assistants := make(map[string]historyAssistant)
	tools := make(map[string]historyPendingEvent)
	inputs := make(map[string]historyPendingEvent)
	addItem := func(firstEventID, lastEventID int64, itemEvents ...Event) {
		if len(itemEvents) == 0 {
			return
		}
		items = append(items, historyItem{
			events:       itemEvents,
			firstEventID: firstEventID,
			lastEventID:  lastEventID,
		})
	}
	flushAssistant := func(turnID string, keepStreaming bool) {
		assistant, exists := assistants[turnID]
		if !exists {
			return
		}
		assistant.event.Type = EventAssistantMessage
		if keepStreaming && turnID == state.ActiveTurnID {
			assistant.event.Type = EventAssistantDelta
		}
		assistant.event.Text = assistant.finalText
		if assistant.event.Text == "" && assistant.text != nil {
			assistant.event.Text = assistant.text.String()
		}
		addItem(assistant.firstEventID, assistant.lastEventID, normalizedHistoryEvent(assistant.event))
		delete(assistants, turnID)
	}

	for _, event := range events {
		turn := turns[event.TurnID]
		if (pending != nil && event.TurnID == pending.TurnID) || (turn != nil && turn.hidden) {
			continue
		}
		if event.Type != EventAssistantDelta && event.Type != EventAssistantMessage {
			flushAssistant(event.TurnID, false)
		}

		switch event.Type {
		case EventUserMessage, EventUserMessageUpdated:
			if event.TurnID == "" {
				if event.Type == EventUserMessageUpdated {
					continue
				}
				event.Text = boundedHistoryText(event.Text, maxHistoryMessageBytes)
				addItem(event.ID, event.ID, normalizedHistoryEvent(event))
				continue
			}
			turn := turns[event.TurnID]
			if turn == nil || turn.userEventID != event.ID || turn.user == nil {
				continue
			}
			latest := *turn.user
			latest.ID = event.ID
			latest.Type = EventUserMessage
			latest.Text = boundedHistoryText(latest.Text, maxHistoryMessageBytes)
			addItem(event.ID, event.ID, normalizedHistoryEvent(latest))
		case EventAssistantDelta:
			assistant := assistants[event.TurnID]
			if assistant.firstEventID == 0 {
				assistant.firstEventID = event.ID
				assistant.event = event
				assistant.text = newHistoryTextBuffer(maxHistoryMessageBytes)
			}
			assistant.lastEventID = event.ID
			assistant.event.ID = event.ID
			assistant.text.WriteString(event.Text)
			assistants[event.TurnID] = assistant
		case EventAssistantMessage:
			assistant, exists := assistants[event.TurnID]
			if exists {
				if event.Text != "" {
					assistant.finalText = boundedHistoryText(event.Text, maxHistoryMessageBytes)
				}
				assistant.event.ID = event.ID
				assistant.lastEventID = event.ID
				assistants[event.TurnID] = assistant
				flushAssistant(event.TurnID, false)
				continue
			}
			if event.Text != "" {
				event.Text = boundedHistoryText(event.Text, maxHistoryMessageBytes)
				addItem(event.ID, event.ID, normalizedHistoryEvent(event))
			}
		case EventToolStarted:
			if event.ToolID == "" {
				addItem(event.ID, event.ID, normalizedHistoryEvent(event))
				continue
			}
			tools[historyToolKey(event)] = historyPendingEvent{event: event, firstEventID: event.ID}
		case EventToolDelta:
			// The completed tool event carries the bounded retained output.
		case EventToolCompleted:
			completion := normalizedHistoryEvent(event)
			completion.Output = boundedHistoryText(completion.Output, maxHistoryToolOutputBytes)
			toolKey := historyToolKey(event)
			started, exists := tools[toolKey]
			if !exists {
				start := normalizedHistoryEvent(Event{
					ID:       event.ID,
					Type:     EventToolStarted,
					ToolID:   event.ToolID,
					ToolName: event.ToolName,
					Status:   "running",
				})
				addItem(event.ID, event.ID, start, completion)
				continue
			}
			addItem(started.firstEventID, event.ID, normalizedHistoryEvent(started.event), completion)
			delete(tools, toolKey)
		case EventInputRequested:
			if _, pending := pendingInputs[event.InputID]; !pending {
				inputs[event.InputID] = historyPendingEvent{event: event, firstEventID: event.ID}
			}
		case EventInputResolved:
			resolved := normalizedHistoryEvent(event)
			requested, exists := inputs[event.InputID]
			if !exists {
				addItem(event.ID, event.ID, resolved)
				continue
			}
			addItem(requested.firstEventID, event.ID, normalizedHistoryEvent(requested.event), resolved)
			delete(inputs, event.InputID)
		case EventFileDiff:
			event.Diff = boundedHistoryText(event.Diff, maxHistoryDiffBytes)
			addItem(event.ID, event.ID, normalizedHistoryEvent(event))
		case EventGoalUpdated:
			if event.Goal != nil {
				event.Goal = cloneGoal(event.Goal)
				event.Goal.Objective = boundedHistoryText(event.Goal.Objective, maxHistoryMessageBytes)
			}
			addItem(event.ID, event.ID, normalizedHistoryEvent(event))
		case EventLoopUpdated:
			if event.Loop != nil {
				event.Loop = cloneLoop(event.Loop)
				event.Loop.Prompt = boundedHistoryText(event.Loop.Prompt, maxHistoryMessageBytes)
			}
			addItem(event.ID, event.ID, normalizedHistoryEvent(event))
		case EventRuntimeRecovered, EventError, EventTurnInterrupting:
			event.Text = boundedHistoryText(event.Text, maxHistoryNoticeBytes)
			addItem(event.ID, event.ID, normalizedHistoryEvent(event))
		case EventTurnCompleted:
			if event.Status == "completed" || event.Status == "failed" || event.Status == "interrupted" {
				event.Text = boundedHistoryText(event.Text, maxHistoryNoticeBytes)
				addItem(event.ID, event.ID, normalizedHistoryEvent(event))
			}
		}
	}
	for turnID := range assistants {
		flushAssistant(turnID, true)
	}
	for _, tool := range tools {
		addItem(tool.firstEventID, tool.firstEventID, normalizedHistoryEvent(tool.event))
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].firstEventID == items[j].firstEventID {
			return items[i].lastEventID < items[j].lastEventID
		}
		return items[i].firstEventID < items[j].firstEventID
	})

	essential := make([]Event, 0, len(pendingInputs))
	for _, event := range pendingInputs {
		pending := normalizedHistoryEvent(event)
		pending.InputID = event.InputID
		pending.Questions = event.Questions
		essential = append(essential, pending)
	}
	sort.Slice(essential, func(i, j int) bool { return essential[i].ID < essential[j].ID })
	return items, state, essential
}

func historyToolKey(event Event) string {
	return event.TurnID + "\x00" + event.ToolID
}

type historyTextBuffer struct {
	maxBytes  int
	head      []byte
	tail      []byte
	truncated bool
}

func newHistoryTextBuffer(maxBytes int) *historyTextBuffer {
	return &historyTextBuffer{maxBytes: maxBytes}
}

func (b *historyTextBuffer) WriteString(value string) {
	if value == "" || b.maxBytes <= 0 {
		return
	}
	if b.truncated {
		b.appendTail([]byte(value))
		return
	}
	if len(b.head)+len(value) <= b.maxBytes {
		b.head = append(b.head, value...)
		return
	}

	headLimit, _ := b.limits()
	retained := b.head
	retainedHeadBytes := min(headLimit, len(retained))
	b.head = append([]byte(nil), retained[:retainedHeadBytes]...)
	valueHeadBytes := min(headLimit-len(b.head), len(value))
	b.head = append(b.head, value[:valueHeadBytes]...)
	b.truncated = true
	b.appendTail(retained[retainedHeadBytes:])
	b.appendTail([]byte(value[valueHeadBytes:]))
}

func (b *historyTextBuffer) String() string {
	if !b.truncated {
		return string(b.head)
	}
	return string(validUTF8Prefix(b.head)) + historyTruncationMarker + string(validUTF8Suffix(b.tail))
}

func (b *historyTextBuffer) limits() (int, int) {
	available := max(0, b.maxBytes-len(historyTruncationMarker))
	head := available / 2
	return head, available - head
}

func (b *historyTextBuffer) appendTail(value []byte) {
	_, tailLimit := b.limits()
	if tailLimit == 0 {
		return
	}
	if len(value) >= tailLimit {
		b.tail = append(b.tail[:0], value[len(value)-tailLimit:]...)
		return
	}
	overflow := len(b.tail) + len(value) - tailLimit
	if overflow > 0 {
		copy(b.tail, b.tail[overflow:])
		b.tail = b.tail[:len(b.tail)-overflow]
	}
	b.tail = append(b.tail, value...)
}

// historyItemsPage returns at least one eligible item so every page advances,
// even when that item alone exceeds the requested byte limit.
func historyItemsPage(items []historyItem, beforeEventID int64, itemLimit, byteLimit int) ([]Event, int64) {
	eligible := make([]historyItem, 0, len(items))
	for _, item := range items {
		if beforeEventID == 0 || item.firstEventID < beforeEventID {
			eligible = append(eligible, item)
		}
	}
	if len(eligible) == 0 || itemLimit <= 0 || byteLimit <= 0 {
		return nil, 0
	}

	start := len(eligible)
	bytes := 0
	for start > 0 && len(eligible)-start < itemLimit {
		itemBytes := historyItemWireBytes(eligible[start-1])
		if start < len(eligible) && bytes+itemBytes > byteLimit {
			break
		}
		start--
		bytes += itemBytes
	}

	page := make([]Event, 0)
	for _, item := range eligible[start:] {
		page = append(page, item.events...)
	}
	if start == 0 {
		return page, 0
	}
	return page, eligible[start].firstEventID
}

func historyItemWireBytes(item historyItem) int {
	size := 0
	for _, event := range item.events {
		encoded, _ := json.Marshal(event)
		size += len(encoded) + 1
	}
	return size
}

func normalizedHistoryEvent(event Event) Event {
	event.Timestamp = nil
	event.RequestID = ""
	event.TurnID = ""
	event.FirstEventID = 0
	event.LastEventID = 0
	event.JournalID = ""
	event.Reset = false
	event.HistoryLimited = false
	event.HistoryPage = false
	event.HistoryCursor = ""
	event.HistoryState = nil
	event.Runtime = nil
	event.SessionCommand = false
	if event.Goal != nil {
		event.Goal = cloneGoal(event.Goal)
	}
	event.Loop = cloneLoop(event.Loop)
	event.ToolID = boundedHistoryText(event.ToolID, maxHistoryIdentifierBytes)
	event.ToolName = boundedHistoryText(event.ToolName, maxHistoryIdentifierBytes)
	event.InputID = boundedHistoryText(event.InputID, maxHistoryIdentifierBytes)
	event.Status = boundedHistoryText(event.Status, maxHistoryIdentifierBytes)
	event.Questions = boundedHistoryQuestions(event.Questions)
	return event
}

func boundedHistoryQuestions(questions []InputQuestion) []InputQuestion {
	if len(questions) > maxHistoryQuestions {
		questions = questions[:maxHistoryQuestions]
	}
	bounded := make([]InputQuestion, len(questions))
	for index, question := range questions {
		bounded[index] = question
		bounded[index].ID = boundedHistoryText(question.ID, maxHistoryIdentifierBytes)
		bounded[index].Header = boundedHistoryText(question.Header, 512)
		bounded[index].Question = boundedHistoryText(question.Question, 4*1024)
		options := question.Options
		if len(options) > maxHistoryOptions {
			options = options[:maxHistoryOptions]
		}
		bounded[index].Options = make([]InputOption, len(options))
		for optionIndex, option := range options {
			bounded[index].Options[optionIndex] = InputOption{
				Label:       boundedHistoryText(option.Label, 512),
				Description: boundedHistoryText(option.Description, 512),
			}
		}
	}
	return bounded
}

func boundedHistoryText(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	available := max(0, maxBytes-len(historyTruncationMarker))
	headBytes := available / 2
	tailBytes := available - headBytes
	head := validUTF8Prefix([]byte(value[:headBytes]))
	tail := validUTF8Suffix([]byte(value[len(value)-tailBytes:]))
	return string(head) + historyTruncationMarker + string(tail)
}
