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

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func taskDuration(status *kelos.TaskStatus) string {
	if status.StartTime == nil {
		return "-"
	}
	if status.CompletionTime != nil {
		return duration.HumanDuration(status.CompletionTime.Time.Sub(status.StartTime.Time))
	}
	return duration.HumanDuration(time.Since(status.StartTime.Time))
}

func printTaskTable(w io.Writer, tasks []kelos.Task, allNamespaces bool) {
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
		if len(t.Spec.AgentConfigRefs) > 0 {
			names := make([]string, len(t.Spec.AgentConfigRefs))
			for i, ref := range t.Spec.AgentConfigRefs {
				names[i] = ref.Name
			}
			agentConfig = strings.Join(names, ",")
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

func printTaskDetail(w io.Writer, t *kelos.Task) {
	printField(w, "Name", t.Name)
	printField(w, "Namespace", t.Namespace)
	printField(w, "Type", t.Spec.Type)
	printField(w, "Phase", string(t.Status.Phase))
	printField(w, "Prompt", t.Spec.Prompt)
	if t.Spec.Credentials.SecretRef != nil {
		printField(w, "Secret", t.Spec.Credentials.SecretRef.Name)
	}
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
	if len(t.Spec.AgentConfigRefs) > 0 {
		names := make([]string, len(t.Spec.AgentConfigRefs))
		for i, ref := range t.Spec.AgentConfigRefs {
			names[i] = ref.Name
		}
		printField(w, "Agent Configs", strings.Join(names, ", "))
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

func printTaskSpawnerTable(w io.Writer, spawners []kelos.TaskSpawner, allNamespaces bool) {
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
			source = "GitHub Issues"
		} else if s.Spec.When.GitHubPullRequests != nil {
			source = "GitHub Pull Requests"
		} else if s.Spec.When.Jira != nil {
			source = s.Spec.When.Jira.Project
		} else if s.Spec.When.Cron != nil {
			source = "cron: " + s.Spec.When.Cron.Schedule
		} else if s.Spec.When.GitHubWebhook != nil {
			source = "GitHub Webhook"
			if s.Spec.When.GitHubWebhook.Repository != "" {
				source += " (" + s.Spec.When.GitHubWebhook.Repository + ")"
			}
		} else if s.Spec.When.LinearWebhook != nil {
			source = "Linear Webhook"
		} else if s.Spec.When.GenericWebhook != nil {
			source = "Generic Webhook (" + s.Spec.When.GenericWebhook.Source + ")"
		} else if s.Spec.When.Slack != nil {
			source = "Slack"
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

// effectivePollInterval returns the poll interval that the spawner actually
// uses, mirroring the resolution in cmd/kelos-spawner: the active source's
// pollInterval takes precedence over the default interval.
func effectivePollInterval(ts *kelos.TaskSpawner) string {
	switch {
	case ts.Spec.When.GitHubIssues != nil && ts.Spec.When.GitHubIssues.PollInterval != "":
		return ts.Spec.When.GitHubIssues.PollInterval
	case ts.Spec.When.GitHubPullRequests != nil && ts.Spec.When.GitHubPullRequests.PollInterval != "":
		return ts.Spec.When.GitHubPullRequests.PollInterval
	case ts.Spec.When.Jira != nil && ts.Spec.When.Jira.PollInterval != "":
		return ts.Spec.When.Jira.PollInterval
	}
	return "5m"
}

func printTaskSpawnerDetail(w io.Writer, ts *kelos.TaskSpawner) {
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
	} else if ts.Spec.When.GitHubPullRequests != nil {
		gh := ts.Spec.When.GitHubPullRequests
		printField(w, "Source", "GitHub Pull Requests")
		if gh.State != "" {
			printField(w, "State", gh.State)
		}
		if len(gh.Labels) > 0 {
			printField(w, "Labels", fmt.Sprintf("%v", gh.Labels))
		}
		if gh.ReviewState != "" {
			printField(w, "Review State", gh.ReviewState)
		}
	} else if ts.Spec.When.Jira != nil {
		jira := ts.Spec.When.Jira
		printField(w, "Source", "Jira")
		printField(w, "Project", jira.Project)
		if jira.JQL != "" {
			printField(w, "JQL", jira.JQL)
		}
	} else if ts.Spec.When.Cron != nil {
		printField(w, "Source", "Cron")
		printField(w, "Schedule", ts.Spec.When.Cron.Schedule)
	} else if ts.Spec.When.GitHubWebhook != nil {
		gh := ts.Spec.When.GitHubWebhook
		printField(w, "Source", "GitHub Webhook")
		printField(w, "Events", fmt.Sprintf("%v", gh.Events))
		if gh.Repository != "" {
			printField(w, "Repository", gh.Repository)
		}
		if len(gh.ExcludeAuthors) > 0 {
			printField(w, "Exclude Authors", fmt.Sprintf("%v", gh.ExcludeAuthors))
		}
	} else if ts.Spec.When.LinearWebhook != nil {
		lw := ts.Spec.When.LinearWebhook
		printField(w, "Source", "Linear Webhook")
		printField(w, "Types", fmt.Sprintf("%v", lw.Types))
	} else if ts.Spec.When.GenericWebhook != nil {
		gw := ts.Spec.When.GenericWebhook
		printField(w, "Source", "Generic Webhook")
		printField(w, "Webhook Source", gw.Source)
	} else if ts.Spec.When.Slack != nil {
		sl := ts.Spec.When.Slack
		printField(w, "Source", "Slack")
		if len(sl.Channels) > 0 {
			printField(w, "Channels", fmt.Sprintf("%v", sl.Channels))
		}
		if len(sl.Triggers) > 0 {
			patterns := make([]string, len(sl.Triggers))
			for i, tr := range sl.Triggers {
				patterns[i] = tr.Pattern
			}
			printField(w, "Triggers", fmt.Sprintf("%v", patterns))
		}
		if len(sl.ExcludePatterns) > 0 {
			printField(w, "Exclude Patterns", fmt.Sprintf("%v", sl.ExcludePatterns))
		}
	}
	printField(w, "Task Type", ts.Spec.TaskTemplate.Type)
	if ts.Spec.TaskTemplate.Model != "" {
		printField(w, "Model", ts.Spec.TaskTemplate.Model)
	}
	printField(w, "Poll Interval", effectivePollInterval(ts))
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

func printWorkspaceTable(w io.Writer, workspaces []kelos.Workspace, allNamespaces bool) {
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

func printWorkspaceDetail(w io.Writer, ws *kelos.Workspace) {
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

func printAgentConfigTable(w io.Writer, configs []kelos.AgentConfig, allNamespaces bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if allNamespaces {
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tKANON\tPLUGINS\tSKILLS\tMCP SERVERS\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tKANON\tPLUGINS\tSKILLS\tMCP SERVERS\tAGE")
	}
	for _, ac := range configs {
		age := duration.HumanDuration(time.Since(ac.CreationTimestamp.Time))
		kanon := "no"
		if ac.Spec.Kanon != nil {
			kanon = "yes"
		}
		plugins := fmt.Sprintf("%d", len(ac.Spec.Plugins))
		skills := fmt.Sprintf("%d", len(ac.Spec.Skills))
		mcpServers := fmt.Sprintf("%d", len(ac.Spec.MCPServers))
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", ac.Namespace, ac.Name, kanon, plugins, skills, mcpServers, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", ac.Name, kanon, plugins, skills, mcpServers, age)
		}
	}
	tw.Flush()
}

func printAgentConfigDetail(w io.Writer, ac *kelos.AgentConfig) {
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
	if ac.Spec.Kanon != nil {
		detail := ac.Spec.Kanon.Repo
		if ac.Spec.Kanon.Ref != "" {
			detail += " (ref=" + ac.Spec.Kanon.Ref + ")"
		}
		if ac.Spec.Kanon.SecretRef != nil {
			detail += " (secret=" + ac.Spec.Kanon.SecretRef.Name + ")"
		}
		printField(w, "Kanon", detail)
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
	if len(ac.Spec.Skills) > 0 {
		for i, s := range ac.Spec.Skills {
			detail := s.Source
			if s.Skill != "" {
				detail += " (skill=" + s.Skill + ")"
			}
			if i == 0 {
				printField(w, "Skills", detail)
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
