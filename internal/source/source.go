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
	Branch   string
	// Author is the username of the user who created the issue or pull request.
	Author string
	// State is the state of the issue or pull request (e.g. "open", "closed").
	State string
	// Action is the webhook action that triggered this work item (e.g. "opened", "reopened").
	Action string
	// Draft indicates whether a pull request is a draft.
	Draft bool
	// ReviewState is the aggregated pull request review state for GitHub PR sources.
	ReviewState string
	// ReviewComments contains formatted inline review comments for GitHub PR sources.
	ReviewComments string
	Time           string // Cron trigger time (RFC3339)
	Schedule       string // Cron schedule expression

	// TriggerTime is the source-provided re-engagement time for this work item.
	// For GitHub issues it is the most recent matching trigger comment time.
	// For GitHub pull requests it is the most recent qualifying review time or
	// matching trigger comment time that re-enabled the PR.
	// The spawner uses this to retrigger completed tasks when the trigger time
	// is newer than the task's completion time.
	TriggerTime time.Time
}

// Source discovers work items from an external system.
type Source interface {
	Discover(ctx context.Context) ([]WorkItem, error)
}

// WebhookAcknowledger is implemented by webhook-based sources to support
// deferred event acknowledgment. Matching events are not marked as processed
// during Discover; the caller must acknowledge them after task creation or
// deduplication so that events skipped due to maxConcurrency or budget limits
// are rediscovered on the next cycle.
type WebhookAcknowledger interface {
	// AcknowledgeItems marks the webhook events associated with the given
	// work item IDs as processed. IDs not in the pending set are ignored.
	AcknowledgeItems(ctx context.Context, ids []string)
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
