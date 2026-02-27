package source

import (
	"context"
	"sort"
	"time"
)

// WorkItem represents a discovered work item from an external source.
type WorkItem struct {
	ID       string
	Number   int
	Title    string
	Body     string
	URL      string
	Labels   []string
	Comments string
	Kind     string // "Issue" or "PR"
	Time     string // Cron trigger time (RFC3339)
	Schedule string // Cron schedule expression

	// TriggerTime is the creation time of the most recent trigger comment
	// for this work item. It is only set when a TriggerComment filter is
	// configured and a matching comment was found. The spawner uses this
	// to retrigger completed tasks when the trigger comment is newer than
	// the task's completion time.
	TriggerTime time.Time
}

// Source discovers work items from an external system.
type Source interface {
	Discover(ctx context.Context) ([]WorkItem, error)
}

// SortByLabelPriority sorts items in place by the first matching label in
// priorityLabels. Items whose labels match an earlier index are sorted first.
// Items with no matching label are placed last. The sort is stable so items
// with equal priority retain their original order.
func SortByLabelPriority(items []WorkItem, priorityLabels []string) {
	if len(priorityLabels) == 0 {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		return labelPriorityIndex(items[i].Labels, priorityLabels) < labelPriorityIndex(items[j].Labels, priorityLabels)
	})
}

// labelPriorityIndex returns the index of the first matching priority label
// for the given item labels. Lower index means higher priority. If no label
// matches, len(priorityLabels) is returned (lowest priority).
func labelPriorityIndex(itemLabels []string, priorityLabels []string) int {
	best := len(priorityLabels)
	for _, il := range itemLabels {
		for idx, pl := range priorityLabels {
			if il == pl && idx < best {
				best = idx
				break
			}
		}
		if best == 0 {
			return 0
		}
	}
	return best
}
