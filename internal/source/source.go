package source

import "context"

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
}

// Source discovers work items from an external system.
type Source interface {
	Discover(ctx context.Context) ([]WorkItem, error)
}
