package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

const watchPollInterval = 2 * time.Second

// watchTasks polls for tasks and prints a new row whenever a task's
// phase changes. It blocks until the context is cancelled.
func watchTasks(ctx context.Context, cl client.Client, listOpts []client.ListOption, allNamespaces bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	// Print initial table.
	taskList := &axonv1alpha1.TaskList{}
	if err := cl.List(ctx, taskList, listOpts...); err != nil {
		return fmt.Errorf("listing tasks: %w", err)
	}
	printTaskTable(os.Stdout, taskList.Items, allNamespaces)

	known := make(map[string]axonv1alpha1.TaskPhase)
	for i := range taskList.Items {
		t := &taskList.Items[i]
		known[taskKey(t.Namespace, t.Name)] = t.Status.Phase
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(watchPollInterval):
		}

		taskList = &axonv1alpha1.TaskList{}
		if err := cl.List(ctx, taskList, listOpts...); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("listing tasks: %w", err)
		}

		for i := range taskList.Items {
			t := &taskList.Items[i]
			key := taskKey(t.Namespace, t.Name)
			prev, exists := known[key]
			if !exists || prev != t.Status.Phase {
				printTaskRow(os.Stdout, t, allNamespaces)
				known[key] = t.Status.Phase
			}
		}
	}
}

// watchTaskSpawners polls for task spawners and prints a new row
// whenever a spawner's status changes.
func watchTaskSpawners(ctx context.Context, cl client.Client, listOpts []client.ListOption, allNamespaces bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	tsList := &axonv1alpha1.TaskSpawnerList{}
	if err := cl.List(ctx, tsList, listOpts...); err != nil {
		return fmt.Errorf("listing task spawners: %w", err)
	}
	printTaskSpawnerTable(os.Stdout, tsList.Items, allNamespaces)

	type spawnerState struct {
		phase            axonv1alpha1.TaskSpawnerPhase
		totalDiscovered  int
		totalTaskCreated int
	}
	known := make(map[string]spawnerState)
	for i := range tsList.Items {
		s := &tsList.Items[i]
		known[taskKey(s.Namespace, s.Name)] = spawnerState{
			phase:            s.Status.Phase,
			totalDiscovered:  s.Status.TotalDiscovered,
			totalTaskCreated: s.Status.TotalTasksCreated,
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(watchPollInterval):
		}

		tsList = &axonv1alpha1.TaskSpawnerList{}
		if err := cl.List(ctx, tsList, listOpts...); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("listing task spawners: %w", err)
		}

		for i := range tsList.Items {
			s := &tsList.Items[i]
			key := taskKey(s.Namespace, s.Name)
			cur := spawnerState{
				phase:            s.Status.Phase,
				totalDiscovered:  s.Status.TotalDiscovered,
				totalTaskCreated: s.Status.TotalTasksCreated,
			}
			if prev, exists := known[key]; !exists || prev != cur {
				printTaskSpawnerRow(os.Stdout, s, allNamespaces)
				known[key] = cur
			}
		}
	}
}

// watchWorkspaces polls for workspaces and prints a new row whenever a
// workspace changes.
func watchWorkspaces(ctx context.Context, cl client.Client, listOpts []client.ListOption, allNamespaces bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()

	wsList := &axonv1alpha1.WorkspaceList{}
	if err := cl.List(ctx, wsList, listOpts...); err != nil {
		return fmt.Errorf("listing workspaces: %w", err)
	}
	printWorkspaceTable(os.Stdout, wsList.Items, allNamespaces)

	known := make(map[string]string) // key -> resourceVersion
	for i := range wsList.Items {
		ws := &wsList.Items[i]
		known[taskKey(ws.Namespace, ws.Name)] = ws.ResourceVersion
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(watchPollInterval):
		}

		wsList = &axonv1alpha1.WorkspaceList{}
		if err := cl.List(ctx, wsList, listOpts...); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("listing workspaces: %w", err)
		}

		for i := range wsList.Items {
			ws := &wsList.Items[i]
			key := taskKey(ws.Namespace, ws.Name)
			if prev, exists := known[key]; !exists || prev != ws.ResourceVersion {
				printWorkspaceRow(os.Stdout, ws, allNamespaces)
				known[key] = ws.ResourceVersion
			}
		}
	}
}

func taskKey(namespace, name string) string {
	return namespace + "/" + name
}

// printTaskRow prints a single task row without the header.
func printTaskRow(w io.Writer, t *axonv1alpha1.Task, allNamespaces bool) {
	age := duration.HumanDuration(time.Since(t.CreationTimestamp.Time))
	if allNamespaces {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", t.Namespace, t.Name, t.Spec.Type, t.Status.Phase, age)
	} else {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.Name, t.Spec.Type, t.Status.Phase, age)
	}
}

// printTaskSpawnerRow prints a single task spawner row without the header.
func printTaskSpawnerRow(w io.Writer, s *axonv1alpha1.TaskSpawner, allNamespaces bool) {
	age := duration.HumanDuration(time.Since(s.CreationTimestamp.Time))
	source := ""
	if s.Spec.When.GitHubIssues != nil {
		if s.Spec.TaskTemplate.WorkspaceRef != nil {
			source = s.Spec.TaskTemplate.WorkspaceRef.Name
		} else {
			source = "GitHub Issues"
		}
	} else if s.Spec.When.Cron != nil {
		source = "cron: " + s.Spec.When.Cron.Schedule
	}
	if allNamespaces {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
			s.Namespace, s.Name, source, s.Status.Phase,
			s.Status.TotalDiscovered, s.Status.TotalTasksCreated, age)
	} else {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\n",
			s.Name, source, s.Status.Phase,
			s.Status.TotalDiscovered, s.Status.TotalTasksCreated, age)
	}
}

// printWorkspaceRow prints a single workspace row without the header.
func printWorkspaceRow(w io.Writer, ws *axonv1alpha1.Workspace, allNamespaces bool) {
	age := duration.HumanDuration(time.Since(ws.CreationTimestamp.Time))
	if allNamespaces {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", ws.Namespace, ws.Name, ws.Spec.Repo, ws.Spec.Ref, age)
	} else {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ws.Name, ws.Spec.Repo, ws.Spec.Ref, age)
	}
}
