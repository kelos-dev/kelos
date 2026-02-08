package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/yaml"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func printTaskTable(w io.Writer, tasks []axonv1alpha1.Task) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tPHASE\tAGE")
	for _, t := range tasks {
		age := duration.HumanDuration(time.Since(t.CreationTimestamp.Time))
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, t.Spec.Type, t.Status.Phase, age)
	}
	tw.Flush()
}

func printTaskDetail(w io.Writer, t *axonv1alpha1.Task) {
	printField(w, "Name", t.Name)
	printField(w, "Namespace", t.Namespace)
	printField(w, "Type", t.Spec.Type)
	printField(w, "Phase", string(t.Status.Phase))
	printField(w, "Prompt", t.Spec.Prompt)
	printField(w, "Secret", t.Spec.Credentials.SecretRef.Name)
	printField(w, "Credential Type", string(t.Spec.Credentials.Type))
	if t.Spec.Model != "" {
		printField(w, "Model", t.Spec.Model)
	}
	if t.Spec.WorkspaceRef != nil {
		printField(w, "Workspace", t.Spec.WorkspaceRef.Name)
	}
	if t.Status.JobName != "" {
		printField(w, "Job", t.Status.JobName)
	}
	if t.Status.PodName != "" {
		printField(w, "Pod", t.Status.PodName)
	}
	if t.Status.StartTime != nil {
		printField(w, "Start Time", t.Status.StartTime.Time.Format(time.RFC3339))
	}
	if t.Status.CompletionTime != nil {
		printField(w, "Completion Time", t.Status.CompletionTime.Time.Format(time.RFC3339))
	}
	if t.Status.Message != "" {
		printField(w, "Message", t.Status.Message)
	}
}

func printTaskSpawnerTable(w io.Writer, spawners []axonv1alpha1.TaskSpawner) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tPHASE\tDISCOVERED\tTASKS\tAGE")
	for _, s := range spawners {
		age := duration.HumanDuration(time.Since(s.CreationTimestamp.Time))
		source := ""
		if s.Spec.When.GitHubIssues != nil && s.Spec.When.GitHubIssues.WorkspaceRef != nil {
			source = s.Spec.When.GitHubIssues.WorkspaceRef.Name
		} else if s.Spec.When.Cron != nil {
			source = "cron: " + s.Spec.When.Cron.Schedule
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
			s.Name, source, s.Status.Phase,
			s.Status.TotalDiscovered, s.Status.TotalTasksCreated, age)
	}
	tw.Flush()
}

func printTaskSpawnerDetail(w io.Writer, ts *axonv1alpha1.TaskSpawner) {
	printField(w, "Name", ts.Name)
	printField(w, "Namespace", ts.Namespace)
	printField(w, "Phase", string(ts.Status.Phase))
	if ts.Spec.When.GitHubIssues != nil {
		gh := ts.Spec.When.GitHubIssues
		printField(w, "Source", "GitHub Issues")
		if gh.WorkspaceRef != nil {
			printField(w, "Workspace", gh.WorkspaceRef.Name)
		}
		if len(gh.Types) > 0 {
			printField(w, "Types", fmt.Sprintf("%v", gh.Types))
		}
		if gh.State != "" {
			printField(w, "State", gh.State)
		}
		if len(gh.Labels) > 0 {
			printField(w, "Labels", fmt.Sprintf("%v", gh.Labels))
		}
	} else if ts.Spec.When.Cron != nil {
		printField(w, "Source", "Cron")
		printField(w, "Schedule", ts.Spec.When.Cron.Schedule)
	}
	printField(w, "Task Type", ts.Spec.TaskTemplate.Type)
	if ts.Spec.TaskTemplate.Model != "" {
		printField(w, "Model", ts.Spec.TaskTemplate.Model)
	}
	printField(w, "Poll Interval", ts.Spec.PollInterval)
	if ts.Status.DeploymentName != "" {
		printField(w, "Deployment", ts.Status.DeploymentName)
	}
	printField(w, "Discovered", fmt.Sprintf("%d", ts.Status.TotalDiscovered))
	printField(w, "Tasks Created", fmt.Sprintf("%d", ts.Status.TotalTasksCreated))
	if ts.Status.LastDiscoveryTime != nil {
		printField(w, "Last Discovery", ts.Status.LastDiscoveryTime.Time.Format(time.RFC3339))
	}
	if ts.Status.Message != "" {
		printField(w, "Message", ts.Status.Message)
	}
}

func printField(w io.Writer, label, value string) {
	fmt.Fprintf(w, "%-20s%s\n", label+":", value)
}

func printYAML(w io.Writer, obj interface{}) error {
	data, err := yaml.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func printJSON(w io.Writer, obj interface{}) error {
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
