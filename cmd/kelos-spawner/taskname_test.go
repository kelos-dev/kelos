package main

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
	"github.com/kelos-dev/kelos/internal/source"
)

func TestBuildTaskName(t *testing.T) {
	cases := []struct {
		name    string
		spawner string
		id      string
		want    string
	}{
		{"jira key is lowercased", "my-spawner", "PROJECT-1234", "my-spawner-project-1234"},
		{"github numeric id unchanged", "spawner", "42", "spawner-42"},
		{"cron timestamp unchanged", "spawner", "20060102-1504", "spawner-20060102-1504"},
		{"invalid characters replaced", "spawner", "PROJ_1", "spawner-proj-1"},
		{"consecutive dashes collapsed", "spawner", "PROJ--1", "spawner-proj-1"},
		{"dots treated as separators", "spawner", "a..b", "spawner-a-b"},
		{"trailing separator trimmed", "spawner", "ABC-", "spawner-abc"},
		{"all-invalid id falls back to spawner", "spawner", "@@@", "spawner"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildTaskName(tc.spawner, tc.id)
			if got != tc.want {
				t.Errorf("buildTaskName(%q, %q) = %q, want %q", tc.spawner, tc.id, got, tc.want)
			}
			// The result must be a name the Kubernetes API server accepts.
			if errs := validation.IsDNS1123Subdomain(got); len(errs) != 0 {
				t.Errorf("buildTaskName(%q, %q) = %q is not a valid RFC 1123 name: %v", tc.spawner, tc.id, got, errs)
			}
		})
	}
}

func TestRunCycleWithSource_NormalizesJiraKeyAndDedupes(t *testing.T) {
	ts := newTaskSpawner("spawner", "default", nil)
	cl, key := setupTest(t, ts)

	src := &fakeSource{
		items: []source.WorkItem{
			{ID: "PROJECT-1234", Number: 1234, Title: "Fix the thing"},
		},
	}

	// First cycle: the Task is created with a normalized name.
	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("first cycle: unexpected error: %v", err)
	}

	var taskList kelos.TaskList
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("after first cycle: expected 1 task, got %d", len(taskList.Items))
	}
	if got, want := taskList.Items[0].Name, "spawner-project-1234"; got != want {
		t.Errorf("task name = %q, want %q", got, want)
	}

	// Second cycle, same item: must not create a duplicate. This proves the
	// dedup lookup name and the creation name are derived identically.
	if err := runCycleWithSource(context.Background(), cl, key, src); err != nil {
		t.Fatalf("second cycle: unexpected error: %v", err)
	}
	if err := cl.List(context.Background(), &taskList, client.InNamespace("default")); err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(taskList.Items) != 1 {
		t.Fatalf("after second cycle: expected 1 task (no duplicate), got %d", len(taskList.Items))
	}
}
