package sessionruntime

import "errors"

func (s *Server) loadPrompts(requestID, value string) (Event, error) {
	cursor := sessionHistoryCursor{ItemLimit: DefaultHistoryItemLimit, ByteLimit: DefaultHistoryByteLimit}
	if value != "" {
		var err error
		cursor, err = decodeHistoryCursor(value)
		if err != nil {
			return Event{}, err
		}
	}
	bounds, events := s.journal.SnapshotWithBounds()
	if cursor.JournalID != "" && cursor.JournalID != bounds.JournalID {
		return Event{}, errors.New("Session prompt history cursor expired; reload prompts")
	}
	cursor.JournalID = bounds.JournalID
	page, beforeEventID := historyItemsPage(promptHistoryItems(events), cursor.BeforeEventID, cursor.ItemLimit, cursor.ByteLimit)
	prompts := make([]Prompt, 0, len(page))
	for _, event := range page {
		prompts = append(prompts, Prompt{ID: event.ID, Text: event.Text, Timestamp: event.Timestamp, Attachments: event.Attachments})
	}
	nextCursor := ""
	if beforeEventID > 0 {
		cursor.BeforeEventID = beforeEventID
		nextCursor = encodeHistoryCursor(cursor)
	}
	return Event{Type: EventPrompts, RequestID: requestID, Prompts: prompts, HistoryCursor: nextCursor}, nil
}

func promptHistoryItems(events []Event) []historyItem {
	items := make([]historyItem, 0)
	turns := make(map[string]int)
	for _, event := range events {
		index, exists := turns[event.TurnID]
		switch event.Type {
		case EventUserMessage, EventUserMessageUpdated:
			if event.TurnID != "" && exists {
				prompt := &items[index].events[0]
				prompt.Text = event.Text
				prompt.Attachments = event.Attachments
				continue
			}
			if event.TurnID != "" {
				turns[event.TurnID] = len(items)
			}
			items = append(items, historyItem{firstEventID: event.ID, lastEventID: event.ID, events: []Event{event}})
		case EventUserMessageRemoved, EventTurnCompleted:
			if exists && (event.Type == EventUserMessageRemoved || event.Status == "merged") {
				items[index].events = nil
				delete(turns, event.TurnID)
			}
		}
	}
	retained := items[:0]
	for _, item := range items {
		if len(item.events) > 0 {
			retained = append(retained, item)
		}
	}
	return retained
}
