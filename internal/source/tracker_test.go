package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// stubPoller is a trackerPoller with one item that always passes the gate;
// the thread, gate time, and commands are fixed by the test.
type stubPoller struct {
	gateTime time.Time
	thread   []commentEntry
	trigger  string
	excludes []string
	body     string
}

func (p stubPoller) list(context.Context) ([]trackerItem, error) {
	return []trackerItem{{WorkItem: WorkItem{ID: "1", Number: 1, Kind: "Issue", Body: p.body}, Author: "author"}}, nil
}

func (p stubPoller) enrich(_ context.Context, item *trackerItem) (bool, []commentEntry, time.Time, error) {
	item.Comments = "enriched"
	return true, p.thread, p.gateTime, nil
}

func (p stubPoller) commentPolicy(context.Context) (commentCommands, commentAuthorizer, error) {
	return commentCommands{Trigger: p.trigger, Excludes: p.excludes}, allowAll{}, nil
}

type allowAll struct{}

func (allowAll) isAuthorized(context.Context, string) (bool, error) { return true, nil }

func TestDiscoverTrackerAppliesCommentPolicy(t *testing.T) {
	t1 := "2026-01-01T00:00:00Z"
	tests := []struct {
		name      string
		poller    stubPoller
		wantItems int
		wantTime  string
	}{
		{name: "no policy keeps the item", poller: stubPoller{}, wantItems: 1},
		{name: "trigger missing drops the item", poller: stubPoller{trigger: "/go"}, wantItems: 0},
		{name: "trigger in thread keeps the item with its time", poller: stubPoller{trigger: "/go", thread: []commentEntry{{Body: "/go", Author: "a", CreatedAt: t1}}}, wantItems: 1, wantTime: t1},
		{name: "trigger in body carries no time", poller: stubPoller{trigger: "/go", body: "/go"}, wantItems: 1},
		{name: "exclude in thread drops the item", poller: stubPoller{excludes: []string{"/stop"}, thread: []commentEntry{{Body: "/stop", Author: "a"}}}, wantItems: 0},
		{name: "enrichment survives into the work item", poller: stubPoller{}, wantItems: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := discoverTracker(context.Background(), tt.poller)
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != tt.wantItems {
				t.Fatalf("items = %+v, want %d", items, tt.wantItems)
			}
			if tt.wantItems == 0 {
				return
			}
			if items[0].Comments != "enriched" {
				t.Errorf("enrich changes must reach the WorkItem, got %+v", items[0])
			}
			want, _ := time.Parse(time.RFC3339, tt.wantTime)
			if !items[0].TriggerTime.Equal(want) {
				t.Errorf("TriggerTime = %v, want %v", items[0].TriggerTime, want)
			}
		})
	}
}

func TestFetchAllPagesStopsAtCapAndReportsCompleteness(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		n, _ := strconv.Atoi(page)
		if n == 0 {
			n = 1
		}
		w.Header().Set("X-Next-Page", strconv.Itoa(n+1))
		json.NewEncoder(w).Encode([]int{n})
	}))
	defer server.Close()

	rest := restClient{
		name:      "Test",
		authorize: func(*http.Request) {},
		nextPage: func(pageURL string, resp *http.Response) string {
			return server.URL + "/items?page=" + resp.Header.Get("X-Next-Page")
		},
	}
	items, complete, err := fetchAllPages[int](context.Background(), rest, server.URL+"/items?page=1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != maxPages || complete {
		t.Errorf("items = %v, complete = %v; want %d pages and complete=false", items, complete, maxPages)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pages) != maxPages {
		t.Errorf("fetched %d pages, want the %d-page cap", len(pages), maxPages)
	}
}

func TestFetchAllPagesEndsOnEmptyPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode([]int{})
			return
		}
		json.NewEncoder(w).Encode([]int{1})
	}))
	defer server.Close()

	rest := restClient{
		name:      "Test",
		authorize: func(*http.Request) {},
		nextPage:  func(string, *http.Response) string { return server.URL + "/items?page=2" },
	}
	items, complete, err := fetchAllPages[int](context.Background(), rest, server.URL+"/items?page=1")
	if err != nil || len(items) != 1 || !complete {
		t.Errorf("items = %v, complete = %v, err = %v; want [1], true, nil", items, complete, err)
	}
}

func TestRestClientReportsAPIErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token secret" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"message":"bad credentials"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]int{"id": 7})
	}))
	defer server.Close()

	rest := githubREST("secret", nil)
	var out map[string]int
	if _, err := rest.getJSON(context.Background(), server.URL, &out); err != nil || out["id"] != 7 {
		t.Fatalf("getJSON() = %v, %v", out, err)
	}
	if _, err := githubREST("", nil).getJSON(context.Background(), server.URL, &out); err == nil || err.Error() != `GitHub API returned status 401: {"message":"bad credentials"}` {
		t.Errorf("unexpected error %q", err)
	}
}
