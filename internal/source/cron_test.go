package source

import (
	"context"
	"testing"
	"time"
)

func TestCronDiscover(t *testing.T) {
	// Schedule: every hour on the hour
	// Window: 3 hours
	start := time.Date(2026, 2, 7, 9, 0, 0, 0, time.UTC)
	s := &CronSource{
		Schedule:          "0 * * * *",
		LastDiscoveryTime: start,
	}

	// Freeze "now" by adjusting the source's perspective.
	// We test by setting LastDiscoveryTime 3 hours before a known time.
	// The Discover method uses time.Now(), so we'll just verify the shape.
	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Items should exist (we're testing from 9am, there should be ticks for each hour up to now)
	for _, item := range items {
		if item.Schedule != "0 * * * *" {
			t.Errorf("expected schedule '0 * * * *', got %q", item.Schedule)
		}
		if item.Time == "" {
			t.Error("expected Time to be set")
		}
		if item.ID == "" {
			t.Error("expected ID to be set")
		}
		if item.Title == "" {
			t.Error("expected Title to be set")
		}
	}
}

func TestCronDiscoverMultipleTicks(t *testing.T) {
	// Every minute, window of 5 minutes
	now := time.Now().UTC()
	start := now.Add(-5 * time.Minute).Truncate(time.Minute)

	s := &CronSource{
		Schedule:          "* * * * *",
		LastDiscoveryTime: start,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have approximately 5 items (one per minute)
	if len(items) < 4 || len(items) > 6 {
		t.Errorf("expected ~5 items, got %d", len(items))
	}

	// Verify items are ordered chronologically
	for i := 1; i < len(items); i++ {
		if items[i].ID <= items[i-1].ID {
			t.Errorf("items not in chronological order: %s <= %s", items[i].ID, items[i-1].ID)
		}
	}
}

func TestCronDiscoverNoTicks(t *testing.T) {
	// LastDiscoveryTime is very recent, no ticks should have occurred
	s := &CronSource{
		Schedule:          "0 0 1 1 *", // Once a year on Jan 1
		LastDiscoveryTime: time.Now().UTC(),
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
}

func TestCronDiscoverInvalidSchedule(t *testing.T) {
	s := &CronSource{
		Schedule:          "invalid",
		LastDiscoveryTime: time.Now().UTC().Add(-time.Hour),
	}

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid schedule, got nil")
	}
}

func TestCronDiscoverIDFormat(t *testing.T) {
	// Use a known time window to verify ID format
	start := time.Date(2026, 2, 7, 8, 59, 0, 0, time.UTC)
	// Schedule: Feb 7 at 9:00 every year
	s := &CronSource{
		Schedule:          "0 9 7 2 *", // Feb 7 at 9:00
		LastDiscoveryTime: start,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) == 0 {
		t.Skip("Test depends on current date being after Feb 7, 2026")
	}

	// First item should be the Feb 7 9:00 tick
	if items[0].ID != "20260207-0900" {
		t.Errorf("expected ID '20260207-0900', got %q", items[0].ID)
	}
	if items[0].Time != "2026-02-07T09:00:00Z" {
		t.Errorf("expected Time '2026-02-07T09:00:00Z', got %q", items[0].Time)
	}
}

func TestCronDiscoverZeroLastDiscoveryTime(t *testing.T) {
	s := &CronSource{
		Schedule: "* * * * *",
	}

	_, err := s.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error for zero LastDiscoveryTime, got nil")
	}
}

func TestCronDiscoverMaxTicks(t *testing.T) {
	// A very old LastDiscoveryTime with a minutely schedule would produce
	// millions of ticks, but we cap at maxCronTicks.
	s := &CronSource{
		Schedule:          "* * * * *",
		LastDiscoveryTime: time.Now().UTC().Add(-24 * 365 * time.Hour), // ~1 year ago
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != maxCronTicks {
		t.Errorf("expected %d items (capped), got %d", maxCronTicks, len(items))
	}
}

func TestCronDiscoverWorkItemFields(t *testing.T) {
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	s := &CronSource{
		Schedule:          "0 * * * *",
		LastDiscoveryTime: start,
	}

	items, err := s.Discover(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected at least 1 item")
	}

	item := items[0]
	// Verify cron-specific fields
	if item.Schedule != "0 * * * *" {
		t.Errorf("unexpected Schedule: %q", item.Schedule)
	}

	// Verify GitHub-specific fields are empty
	if item.Number != 0 {
		t.Errorf("expected Number 0, got %d", item.Number)
	}
	if item.Body != "" {
		t.Errorf("expected empty Body, got %q", item.Body)
	}
	if item.URL != "" {
		t.Errorf("expected empty URL, got %q", item.URL)
	}
	if item.Comments != "" {
		t.Errorf("expected empty Comments, got %q", item.Comments)
	}
	if len(item.Labels) != 0 {
		t.Errorf("expected no Labels, got %v", item.Labels)
	}
}
