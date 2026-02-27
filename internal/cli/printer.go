package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/yaml"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func taskDuration(status *kelosv1alpha1.TaskStatus) string {
	if status.StartTime == nil {
		return "-"
	}
	if status.CompletionTime != nil {
		return duration.HumanDuration(status.CompletionTime.Time.Sub(status.StartTime.Time))
	}
	return duration.HumanDuration(time.Since(status.StartTime.Time))
}

func printTaskTable(w io.Writer, tasks []kelosv1alpha1.Task, allNamespaces bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if allNamespaces {
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tTYPE\tPHASE\tBRANCH\tWORKSPACE\tAGENT CONFIG\tDURATION\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tTYPE\tPHASE\tBRANCH\tWORKSPACE\tAGENT CONFIG\tDURATION\tAGE")
	}
	for _, t := range tasks {
		age := duration.HumanDuration(time.Since(t.CreationTimestamp.Time))
		branch := "-"
		if t.Spec.Branch != "" {
			branch = t.Spec.Branch
		}
		workspace := "-"
		if t.Spec.WorkspaceRef != nil {
			workspace = t.Spec.WorkspaceRef.Name
		}
		agentConfig := "-"
		if t.Spec.AgentConfigRef != nil {
			agentConfig = t.Spec.AgentConfigRef.Name
		}
		dur := taskDuration(&t.Status)
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				t.Namespace, t.Name, t.Spec.Type, t.Status.Phase, branch, workspace, agentConfig, dur, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				t.Name, t.Spec.Type, t.Status.Phase, branch, workspace, agentConfig, dur, age)
		}
	}
	tw.Flush()
}

func printTaskDetail(w io.Writer, t *kelosv1alpha1.Task) {
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
	if t.Spec.Image != "" {
		printField(w, "Image", t.Spec.Image)
	}
	if t.Spec.Branch != "" {
		printField(w, "Branch", t.Spec.Branch)
	}
	if len(t.Spec.DependsOn) > 0 {
		printField(w, "Depends On", strings.Join(t.Spec.DependsOn, ", "))
	}
	if t.Spec.WorkspaceRef != nil {
		printField(w, "Workspace", t.Spec.WorkspaceRef.Name)
	}
	if t.Spec.AgentConfigRef != nil {
		printField(w, "Agent Config", t.Spec.AgentConfigRef.Name)
	}
	if t.Spec.TTLSecondsAfterFinished != nil {
		printField(w, "TTL", fmt.Sprintf("%ds", *t.Spec.TTLSecondsAfterFinished))
	}
	if t.Spec.PodOverrides != nil && t.Spec.PodOverrides.ActiveDeadlineSeconds != nil {
		printField(w, "Timeout", fmt.Sprintf("%ds", *t.Spec.PodOverrides.ActiveDeadlineSeconds))
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
	dur := taskDuration(&t.Status)
	if dur != "-" {
		printField(w, "Duration", dur)
	}
	if t.Status.Message != "" {
		printField(w, "Message", t.Status.Message)
	}
	if len(t.Status.Outputs) > 0 {
		printField(w, "Outputs", t.Status.Outputs[0])
		for _, o := range t.Status.Outputs[1:] {
			fmt.Fprintf(w, "%-20s%s\n", "", o)
		}
	}
	if len(t.Status.Results) > 0 {
		keys := make([]string, 0, len(t.Status.Results))
		for k := range t.Status.Results {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			entry := fmt.Sprintf("%s=%s", k, t.Status.Results[k])
			if i == 0 {
				printField(w, "Results", entry)
			} else {
				fmt.Fprintf(w, "%-20s%s\n", "", entry)
			}
		}
	}
}

func printTaskSpawnerTable(w io.Writer, spawners []kelosv1alpha1.TaskSpawner, allNamespaces bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if allNamespaces {
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tSOURCE\tPHASE\tDISCOVERED\tTASKS\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tSOURCE\tPHASE\tDISCOVERED\tTASKS\tAGE")
	}
	for _, s := range spawners {
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
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%s\n",
				s.Namespace, s.Name, source, s.Status.Phase,
				s.Status.TotalDiscovered, s.Status.TotalTasksCreated, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n",
				s.Name, source, s.Status.Phase,
				s.Status.TotalDiscovered, s.Status.TotalTasksCreated, age)
		}
	}
	tw.Flush()
}

func printTaskSpawnerDetail(w io.Writer, ts *kelosv1alpha1.TaskSpawner) {
	printField(w, "Name", ts.Name)
	printField(w, "Namespace", ts.Namespace)
	printField(w, "Phase", string(ts.Status.Phase))
	if ts.Spec.TaskTemplate.WorkspaceRef != nil {
		printField(w, "Workspace", ts.Spec.TaskTemplate.WorkspaceRef.Name)
	}
	if ts.Spec.When.GitHubIssues != nil {
		gh := ts.Spec.When.GitHubIssues
		printField(w, "Source", "GitHub Issues")
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

func printWorkspaceTable(w io.Writer, workspaces []kelosv1alpha1.Workspace, allNamespaces bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if allNamespaces {
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tREPO\tREF\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tREPO\tREF\tAGE")
	}
	for _, ws := range workspaces {
		age := duration.HumanDuration(time.Since(ws.CreationTimestamp.Time))
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ws.Namespace, ws.Name, ws.Spec.Repo, ws.Spec.Ref, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ws.Name, ws.Spec.Repo, ws.Spec.Ref, age)
		}
	}
	tw.Flush()
}

func printWorkspaceDetail(w io.Writer, ws *kelosv1alpha1.Workspace) {
	printField(w, "Name", ws.Name)
	printField(w, "Namespace", ws.Namespace)
	printField(w, "Repo", ws.Spec.Repo)
	if ws.Spec.Ref != "" {
		printField(w, "Ref", ws.Spec.Ref)
	}
	if ws.Spec.SecretRef != nil {
		printField(w, "Secret", ws.Spec.SecretRef.Name)
	}
}

func printAgentConfigTable(w io.Writer, configs []kelosv1alpha1.AgentConfig, allNamespaces bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if allNamespaces {
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tPLUGINS\tMCP SERVERS\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tPLUGINS\tMCP SERVERS\tAGE")
	}
	for _, ac := range configs {
		age := duration.HumanDuration(time.Since(ac.CreationTimestamp.Time))
		plugins := fmt.Sprintf("%d", len(ac.Spec.Plugins))
		mcpServers := fmt.Sprintf("%d", len(ac.Spec.MCPServers))
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ac.Namespace, ac.Name, plugins, mcpServers, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ac.Name, plugins, mcpServers, age)
		}
	}
	tw.Flush()
}

func printAgentConfigDetail(w io.Writer, ac *kelosv1alpha1.AgentConfig) {
	printField(w, "Name", ac.Name)
	printField(w, "Namespace", ac.Namespace)
	if ac.Spec.AgentsMD != "" {
		// Truncate long agents-md content for display
		md := ac.Spec.AgentsMD
		if len(md) > 80 {
			md = md[:80] + "..."
		}
		printField(w, "Agents MD", md)
	}
	if len(ac.Spec.Plugins) > 0 {
		for i, p := range ac.Spec.Plugins {
			var parts []string
			if len(p.Skills) > 0 {
				skillNames := make([]string, len(p.Skills))
				for j, s := range p.Skills {
					skillNames[j] = s.Name
				}
				parts = append(parts, fmt.Sprintf("skills=[%s]", strings.Join(skillNames, ",")))
			}
			if len(p.Agents) > 0 {
				agentNames := make([]string, len(p.Agents))
				for j, a := range p.Agents {
					agentNames[j] = a.Name
				}
				parts = append(parts, fmt.Sprintf("agents=[%s]", strings.Join(agentNames, ",")))
			}
			detail := p.Name
			if len(parts) > 0 {
				detail += " (" + strings.Join(parts, ", ") + ")"
			}
			if i == 0 {
				printField(w, "Plugins", detail)
			} else {
				fmt.Fprintf(w, "%-20s%s\n", "", detail)
			}
		}
	}
	if len(ac.Spec.MCPServers) > 0 {
		for i, m := range ac.Spec.MCPServers {
			detail := fmt.Sprintf("%s (%s)", m.Name, m.Type)
			if i == 0 {
				printField(w, "MCP Servers", detail)
			} else {
				fmt.Fprintf(w, "%-20s%s\n", "", detail)
			}
		}
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
