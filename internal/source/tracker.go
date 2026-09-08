package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// trackerItem is a WorkItem plus the author that the comment policy judges.
// Providers convert their own API types to it once, when listing.
type trackerItem struct {
	WorkItem
	Author string
}

// trackerPoller is the per-provider half of polling discovery. discoverTracker
// owns the pipeline (candidate walk, comment policy, trigger time); a provider
// owns everything that depends on one code host's API: listing and cheap
// filtering, per-item enrichment and gates, and comment retrieval.
type trackerPoller interface {
	// list returns the candidate items after the provider's own cheap filters
	// (labels, authors, state, draft, file patterns).
	list(ctx context.Context) ([]trackerItem, error)
	// enrich loads one item's detail (reviews, pipeline, comments), fills the
	// item's Comments, ReviewComments, ReviewState, and pipeline fields, and
	// reports whether the item passes the provider's gates. thread is what the
	// comment policy evaluates; triggerTime is the provider's own
	// re-engagement signal (a newer review or pipeline run), zero when no gate
	// is configured.
	enrich(ctx context.Context, item *trackerItem) (keep bool, thread []commentEntry, triggerTime time.Time, err error)
	// commentPolicy returns the configured commands and the authorizer that
	// judges their authors. The authorizer may be nil when no command is set.
	commentPolicy(ctx context.Context) (commentCommands, commentAuthorizer, error)
}

// discoverTracker runs the shared polling pipeline over a provider: list
// candidates, enrich and gate each one, apply the comment policy, and record
// the later of the provider's and the policy's trigger times so a finished
// Task is re-run when either signal is newer.
func discoverTracker(ctx context.Context, p trackerPoller) ([]WorkItem, error) {
	items, err := p.list(ctx)
	if err != nil {
		return nil, err
	}
	cmds, authorizer, err := p.commentPolicy(ctx)
	if err != nil {
		return nil, err
	}

	var out []WorkItem
	for i := range items {
		item := &items[i]
		keep, thread, gateTime, err := p.enrich(ctx, item)
		if err != nil {
			return nil, err
		}
		if !keep {
			continue
		}

		var commentTime time.Time
		if cmds.enabled() {
			allowed, t, err := evaluateCommentPolicy(ctx, cmds, item.Body, item.Author, thread, authorizer)
			if err != nil {
				return nil, fmt.Errorf("evaluating comment policy for %s %s: %w", item.Kind, item.ID, err)
			}
			if !allowed {
				continue
			}
			commentTime = t
		}

		item.TriggerTime = commentTime
		if gateTime.After(commentTime) {
			item.TriggerTime = gateTime
		}
		out = append(out, item.WorkItem)
	}
	return out, nil
}

// restClient is the HTTP plumbing shared by tracker sources: authenticated
// JSON GETs and page walking. The provider supplies the credentials header
// and how the next page is discovered.
type restClient struct {
	// name labels API errors ("GitHub", "GitLab").
	name      string
	client    *http.Client
	authorize func(*http.Request)
	// nextPage returns the URL of the page after resp, or "" on the last one.
	nextPage func(pageURL string, resp *http.Response) string
}

func (c restClient) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return http.DefaultClient
}

// get performs an authenticated GET and returns the response for a 200
// status; any other status is returned as an error with the response body.
func (c restClient) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	c.authorize(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, fmt.Errorf("%s API returned status %d: %s", c.name, resp.StatusCode, string(body))
	}
	return resp, nil
}

// getJSON fetches a single JSON document and returns the response headers
// for pagination.
func (c restClient) getJSON(ctx context.Context, url string, out interface{}) (http.Header, error) {
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return resp.Header, nil
}

// fetchAllPages walks a list endpoint up to maxPages and returns every
// element. complete is false when pages remained after the cap, so callers
// that cannot act on partial data can refuse.
func fetchAllPages[T any](ctx context.Context, c restClient, pageURL string) (items []T, complete bool, err error) {
	for page := 0; pageURL != "" && page < maxPages; page++ {
		resp, err := c.get(ctx, pageURL)
		if err != nil {
			return nil, false, err
		}
		var chunk []T
		err = json.NewDecoder(resp.Body).Decode(&chunk)
		resp.Body.Close()
		if err != nil {
			return nil, false, fmt.Errorf("decoding response: %w", err)
		}
		items = append(items, chunk...)
		if len(chunk) == 0 {
			return items, true, nil
		}
		pageURL = c.nextPage(pageURL, resp)
	}
	return items, pageURL == "", nil
}
