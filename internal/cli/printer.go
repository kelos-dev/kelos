package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/yaml"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
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
	if t.Spec.Workspace != nil {
		printField(w, "Workspace Repo", t.Spec.Workspace.Repo)
		if t.Spec.Workspace.Ref != "" {
			printField(w, "Workspace Ref", t.Spec.Workspace.Ref)
		}
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
