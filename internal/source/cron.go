package source

import (
	"context"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// CronSource discovers work items based on cron schedule ticks since the last discovery.
type CronSource struct {
	Schedule          string
	LastDiscoveryTime time.Time
}

// Discover returns a WorkItem for each cron tick between LastDiscoveryTime and now.
func (s *CronSource) Discover(_ context.Context) ([]WorkItem, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(s.Schedule)
	if err != nil {
		return nil, fmt.Errorf("parsing cron schedule %q: %w", s.Schedule, err)
	}

	now := time.Now().UTC()
	cursor := s.LastDiscoveryTime.UTC()

	var items []WorkItem
	for {
		next := sched.Next(cursor)
		if next.After(now) {
			break
		}
		items = append(items, WorkItem{
			ID:       next.Format("20060102-1504"),
			Title:    next.Format(time.RFC3339),
			Time:     next.Format(time.RFC3339),
			Schedule: s.Schedule,
		})
		cursor = next
	}

	return items, nil
}
