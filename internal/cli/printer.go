package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/utils/ptr"
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

func taskDisplayType(t *kelos.Task) string {
	if t.Spec.Worker != nil && t.Spec.Worker.Type != "" {
		return t.Spec.Worker.Type
	}
	return t.Spec.Type
}

func taskDisplayCredentials(t *kelos.Task) *kelos.Credentials {
	if t.Spec.Worker != nil && t.Spec.Worker.Credentials != nil {
		return t.Spec.Worker.Credentials
	}
	return t.Spec.Credentials
}

func taskDisplayModel(t *kelos.Task) string {
	if t.Spec.Worker != nil && t.Spec.Worker.Model != "" {
		return t.Spec.Worker.Model
	}
	return t.Spec.Model
}

func taskDisplayImage(t *kelos.Task) string {
	if t.Spec.Worker != nil && t.Spec.Worker.Image != "" {
		return t.Spec.Worker.Image
	}
	return t.Spec.Image
}

func taskDisplayWorkspaceRef(t *kelos.Task) *kelos.WorkspaceReference {
	if t.Spec.Worker != nil && t.Spec.Worker.WorkspaceRef != nil {
		return t.Spec.Worker.WorkspaceRef
	}
	return t.Spec.WorkspaceRef
}

func taskDisplayAgentConfigRefs(t *kelos.Task) []kelos.AgentConfigReference {
	if t.Spec.Worker != nil && len(t.Spec.Worker.AgentConfigRefs) > 0 {
		return t.Spec.Worker.AgentConfigRefs
	}
	return t.Spec.AgentConfigRefs
}

func taskDisplayPodOverrides(t *kelos.Task) *kelos.PodOverrides {
	if t.Spec.Worker != nil && t.Spec.Worker.PodOverrides != nil {
		return t.Spec.Worker.PodOverrides
	}
	return t.Spec.PodOverrides
}

func agentConfigRefNames(refs []kelos.AgentConfigReference) []string {
	names := make([]string, len(refs))
	for i, ref := range refs {
		names[i] = ref.Name
	}
	return names
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
		if ref := taskDisplayWorkspaceRef(&t); ref != nil {
			workspace = ref.Name
		}
		agentConfig := "-"
		if refs := taskDisplayAgentConfigRefs(&t); len(refs) > 0 {
			names := agentConfigRefNames(refs)
			agentConfig = strings.Join(names, ",")
		}
		dur := taskDuration(&t.Status)
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				t.Namespace, t.Name, taskDisplayType(&t), t.Status.Phase, branch, workspace, agentConfig, dur, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				t.Name, taskDisplayType(&t), t.Status.Phase, branch, workspace, agentConfig, dur, age)
		}
	}
	tw.Flush()
}

func printTaskDetail(w io.Writer, t *kelos.Task) {
	printField(w, "Name", t.Name)
	printField(w, "Namespace", t.Namespace)
	printField(w, "Type", taskDisplayType(t))
	printField(w, "Phase", string(t.Status.Phase))
	printField(w, "Prompt", t.Spec.Prompt)
	if creds := taskDisplayCredentials(t); creds != nil {
		if creds.SecretRef != nil {
			printField(w, "Secret", creds.SecretRef.Name)
		}
		printField(w, "Credential Type", string(creds.Type))
	}
	if model := taskDisplayModel(t); model != "" {
		printField(w, "Model", model)
	}
	if image := taskDisplayImage(t); image != "" {
		printField(w, "Image", image)
	}
	if t.Spec.Branch != "" {
		printField(w, "Branch", t.Spec.Branch)
	}
	if len(t.Spec.DependsOn) > 0 {
		printField(w, "Depends On", strings.Join(t.Spec.DependsOn, ", "))
	}
	if ref := taskDisplayWorkspaceRef(t); ref != nil {
		printField(w, "Workspace", ref.Name)
	}
	if t.Spec.WorkerPoolRef != nil {
		printField(w, "Worker Pool", t.Spec.WorkerPoolRef.Name)
	}
	if refs := taskDisplayAgentConfigRefs(t); len(refs) > 0 {
		names := agentConfigRefNames(refs)
		printField(w, "Agent Configs", strings.Join(names, ", "))
	}
	if t.Spec.TTLSecondsAfterFinished != nil {
		printField(w, "TTL", fmt.Sprintf("%ds", *t.Spec.TTLSecondsAfterFinished))
	}
	if overrides := taskDisplayPodOverrides(t); overrides != nil && overrides.ActiveDeadlineSeconds != nil {
		printField(w, "Timeout", fmt.Sprintf("%ds", *overrides.ActiveDeadlineSeconds))
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

func printWorkerPoolTable(w io.Writer, pools []kelos.WorkerPool, allNamespaces bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if allNamespaces {
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tTYPE\tREPLICAS\tREADY\tPHASE\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tTYPE\tREPLICAS\tREADY\tPHASE\tAGE")
	}
	for _, wp := range pools {
		age := duration.HumanDuration(time.Since(wp.CreationTimestamp.Time))
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
				wp.Namespace, wp.Name, wp.Spec.Worker.Type, ptr.Deref(wp.Spec.Replicas, 1), wp.Status.ReadyReplicas, wp.Status.Phase, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n",
				wp.Name, wp.Spec.Worker.Type, ptr.Deref(wp.Spec.Replicas, 1), wp.Status.ReadyReplicas, wp.Status.Phase, age)
		}
	}
	tw.Flush()
}

func printWorkerPoolDetail(w io.Writer, wp *kelos.WorkerPool) {
	printField(w, "Name", wp.Name)
	printField(w, "Namespace", wp.Namespace)
	printField(w, "Type", wp.Spec.Worker.Type)
	printField(w, "Phase", string(wp.Status.Phase))
	printField(w, "Replicas", fmt.Sprintf("%d", ptr.Deref(wp.Spec.Replicas, 1)))
	printField(w, "Ready Replicas", fmt.Sprintf("%d", wp.Status.ReadyReplicas))
	if wp.Status.StatefulSetName != "" {
		printField(w, "StatefulSet", wp.Status.StatefulSetName)
	}
	if wp.Status.ServiceName != "" {
		printField(w, "Service", wp.Status.ServiceName)
	}
	if wp.Status.Message != "" {
		printField(w, "Message", wp.Status.Message)
	}
}

func printSessionTable(w io.Writer, sessions []kelos.Session, allNamespaces bool) {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	if allNamespaces {
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tTYPE\tPHASE\tPOD\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tTYPE\tPHASE\tPOD\tAGE")
	}
	for _, session := range sessions {
		age := duration.HumanDuration(time.Since(session.CreationTimestamp.Time))
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				session.Namespace, session.Name, session.Spec.Worker.Type, session.Status.Phase, session.Status.PodName, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				session.Name, session.Spec.Worker.Type, session.Status.Phase, session.Status.PodName, age)
		}
	}
	tw.Flush()
}

func printSessionDetail(w io.Writer, session *kelos.Session) {
	printField(w, "Name", session.Name)
	printField(w, "Namespace", session.Namespace)
	printField(w, "Type", session.Spec.Worker.Type)
	printField(w, "Phase", string(session.Status.Phase))
	if condition := apiMeta.FindStatusCondition(session.Status.Conditions, kelos.SessionConditionActive); condition != nil {
		printField(w, "Active", string(condition.Status))
	}
	if credentials := session.Spec.Worker.Credentials; credentials != nil {
		printField(w, "Credential Type", string(credentials.Type))
		if credentials.SecretRef != nil {
			printField(w, "Secret", credentials.SecretRef.Name)
		}
	}
	if session.Spec.Worker.Model != "" {
		printField(w, "Model", session.Spec.Worker.Model)
	}
	if session.Spec.Worker.Effort != "" {
		printField(w, "Effort", session.Spec.Worker.Effort)
	}
	if session.Spec.Worker.Image != "" {
		printField(w, "Image", session.Spec.Worker.Image)
	}
	if session.Spec.Worker.WorkspaceRef != nil {
		printField(w, "Workspace", session.Spec.Worker.WorkspaceRef.Name)
	}
	if len(session.Spec.Worker.AgentConfigRefs) > 0 {
		printField(w, "Agent Config", strings.Join(agentConfigRefNames(session.Spec.Worker.AgentConfigRefs), ","))
	}
	if claim := session.Spec.VolumeClaimTemplate; claim != nil {
		if storage, ok := claim.Resources.Requests[corev1.ResourceStorage]; ok {
			printField(w, "Storage", storage.String())
		}
		if claim.StorageClassName != nil {
			printField(w, "Storage Class", *claim.StorageClassName)
		}
	}
	if session.Status.PodName != "" {
		printField(w, "Pod", session.Status.PodName)
	}
	if session.Status.PodUID != "" {
		printField(w, "Pod UID", string(session.Status.PodUID))
	}
	if session.Status.Message != "" {
		printField(w, "Message", session.Status.Message)
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
		fmt.Fprintln(tw, "NAMESPACE\tNAME\tPLUGINS\tSKILLS\tMCP SERVERS\tAGE")
	} else {
		fmt.Fprintln(tw, "NAME\tPLUGINS\tSKILLS\tMCP SERVERS\tAGE")
	}
	for _, ac := range configs {
		age := duration.HumanDuration(time.Since(ac.CreationTimestamp.Time))
		plugins := fmt.Sprintf("%d", len(ac.Spec.Plugins))
		skills := fmt.Sprintf("%d", len(ac.Spec.Skills))
		mcpServers := fmt.Sprintf("%d", len(ac.Spec.MCPServers))
		if allNamespaces {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", ac.Namespace, ac.Name, plugins, skills, mcpServers, age)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ac.Name, plugins, skills, mcpServers, age)
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
