package webhook

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// GitLabEventData represents parsed GitLab webhook data. Fields are populated
// by ParseGitLabWebhook from the nested payload structure, so no JSON tags are
// used. Kind is "Issue" for issue events and notes on issues, "MR" for merge
// request events, notes on merge requests, and pipelines attached to a merge
// request, and "webhook" otherwise.
type GitLabEventData struct {
	// Event is the payload object_kind (issue, merge_request, note, pipeline, push, tag_push).
	Event string
	// Action is object_attributes.action for issue and merge_request events.
	Action string
	// Sender is the username of the user who triggered the event.
	Sender string
	// Project is the full project path (group/subgroup/project).
	Project    string
	ProjectURL string
	// Ref is the git ref for push, tag_push, and pipeline events.
	Ref string
	// Raw parsed event payload for template access.
	Payload map[string]interface{}
	// Standard template variables. ID is "<iid>" for issues, "mr-<iid>" for
	// merge requests, "pipeline-<id>" for pipelines, and the checkout SHA for
	// pushes, matching the identities used by the polling GitLab source.
	ID     string
	Title  string
	Number int
	Body   string
	URL    string
	Kind   string
	// Branch is the merge request source branch, or the branch of a push or
	// pipeline event.
	Branch string
	State  string
	Labels []string
	Draft  bool
	// NoteOn is the noteable_type of a note event (Issue, MergeRequest, Commit, Snippet).
	NoteOn      string
	CommentBody string
	CommentURL  string
	// PipelineStatus and PipelineURL are set for pipeline events.
	PipelineStatus string
	PipelineURL    string
	// HeadSHA is the merge request head commit, pipeline commit, or push checkout SHA.
	HeadSHA string
}

// ParseGitLabWebhook parses a GitLab webhook payload of any supported object_kind.
func ParseGitLabWebhook(payload []byte) (*GitLabEventData, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON payload: %w", err)
	}

	data := &GitLabEventData{
		Payload: raw,
		Event:   mapString(raw, "object_kind"),
		Kind:    "webhook",
	}

	user := mapObject(raw, "user")
	data.Sender = mapString(user, "username")
	if data.Sender == "" {
		// Push and tag_push events carry the user at the top level.
		data.Sender = mapString(raw, "user_username")
	}

	project := mapObject(raw, "project")
	data.Project = mapString(project, "path_with_namespace")
	data.ProjectURL = mapString(project, "web_url")

	attrs := mapObject(raw, "object_attributes")
	data.Action = mapString(attrs, "action")

	switch data.Event {
	case "issue":
		data.applyIssue(attrs, raw)
	case "merge_request":
		data.applyMergeRequest(attrs, raw)
	case "note":
		data.NoteOn = mapString(attrs, "noteable_type")
		data.CommentBody = mapString(attrs, "note")
		data.CommentURL = mapString(attrs, "url")
		switch data.NoteOn {
		case "Issue":
			data.applyIssue(mapObject(raw, "issue"), raw)
		case "MergeRequest":
			data.applyMergeRequest(mapObject(raw, "merge_request"), raw)
		default:
			data.ID = strconv.Itoa(mapInt(attrs, "id"))
			data.Title = data.NoteOn + " comment"
			data.URL = data.CommentURL
		}
		// Notes have no labels of their own; the commented item's labels are
		// only present on the top level for some GitLab versions.
		if len(data.Labels) == 0 {
			data.Labels = labelTitles(raw["labels"])
		}
	case "pipeline":
		data.PipelineStatus = mapString(attrs, "status")
		data.Ref = mapString(attrs, "ref")
		data.Branch = data.Ref
		data.HeadSHA = mapString(attrs, "sha")
		pipelineID := mapInt(attrs, "id")
		data.ID = "pipeline-" + strconv.Itoa(pipelineID)
		data.PipelineURL = mapString(attrs, "url")
		if data.PipelineURL == "" && data.ProjectURL != "" && pipelineID > 0 {
			data.PipelineURL = fmt.Sprintf("%s/-/pipelines/%d", data.ProjectURL, pipelineID)
		}
		data.Title = fmt.Sprintf("Pipeline %s on %s", data.PipelineStatus, data.Ref)
		data.URL = data.PipelineURL
		if mr := mapObject(raw, "merge_request"); len(mr) > 0 {
			data.Kind = "MR"
			data.Number = mapInt(mr, "iid")
			data.ID = "mr-" + strconv.Itoa(data.Number)
			data.Branch = mapString(mr, "source_branch")
			data.Title = mapString(mr, "title")
			data.URL = mapString(mr, "url")
		}
	case "push", "tag_push":
		data.Ref = mapString(raw, "ref")
		data.Branch = strings.TrimPrefix(strings.TrimPrefix(data.Ref, "refs/heads/"), "refs/tags/")
		data.HeadSHA = mapString(raw, "checkout_sha")
		if data.HeadSHA == "" {
			data.HeadSHA = mapString(raw, "after")
		}
		data.ID = data.HeadSHA
		data.Title = "Push to " + data.Branch
		data.URL = data.ProjectURL
	}

	return data, nil
}

// applyIssue fills the issue identity from an issue object, either the
// object_attributes of an issue event or the issue block of a note event.
func (d *GitLabEventData) applyIssue(issue, raw map[string]interface{}) {
	d.Kind = "Issue"
	d.Number = mapInt(issue, "iid")
	d.ID = strconv.Itoa(d.Number)
	d.Title = mapString(issue, "title")
	d.Body = mapString(issue, "description")
	d.URL = mapString(issue, "url")
	d.State = mapString(issue, "state")
	d.Labels = labelTitles(issue["labels"])
	if len(d.Labels) == 0 {
		d.Labels = labelTitles(raw["labels"])
	}
}

// applyMergeRequest fills the merge request identity from a merge request
// object, either the object_attributes of a merge_request event or the
// merge_request block of a note event.
func (d *GitLabEventData) applyMergeRequest(mr, raw map[string]interface{}) {
	d.Kind = "MR"
	d.Number = mapInt(mr, "iid")
	d.ID = "mr-" + strconv.Itoa(d.Number)
	d.Title = mapString(mr, "title")
	d.Body = mapString(mr, "description")
	d.URL = mapString(mr, "url")
	d.State = mapString(mr, "state")
	d.Branch = mapString(mr, "source_branch")
	d.HeadSHA = mapString(mapObject(mr, "last_commit"), "id")
	if draft, ok := mr["draft"].(bool); ok {
		d.Draft = draft
	} else if wip, ok := mr["work_in_progress"].(bool); ok {
		d.Draft = wip
	}
	d.Labels = labelTitles(mr["labels"])
	if len(d.Labels) == 0 {
		d.Labels = labelTitles(raw["labels"])
	}
}

// labelTitles extracts label names from a GitLab label array, whose entries
// carry the name under "title".
func labelTitles(value interface{}) []string {
	list, ok := value.([]interface{})
	if !ok {
		return nil
	}
	var labels []string
	for _, entry := range list {
		if label, ok := entry.(map[string]interface{}); ok {
			if title := mapString(label, "title"); title != "" {
				labels = append(labels, title)
			}
		}
	}
	return labels
}

func mapObject(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return nil
	}
	obj, _ := m[key].(map[string]interface{})
	return obj
}

func mapString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

func mapInt(m map[string]interface{}, key string) int {
	if m == nil {
		return 0
	}
	f, _ := m[key].(float64)
	return int(f)
}

// MatchesGitLabEvent reports whether a parsed GitLab event matches the
// spawner's gitlabWebhook configuration.
func MatchesGitLabEvent(config *kelos.GitLabWebhook, eventData *GitLabEventData) (bool, error) {
	eventAllowed := false
	for _, e := range config.Events {
		if e == eventData.Event {
			eventAllowed = true
			break
		}
	}
	if !eventAllowed {
		return false, nil
	}

	if config.Project != "" && !strings.EqualFold(config.Project, eventData.Project) {
		return false, nil
	}
	if containsFold(config.ExcludeAuthors, eventData.Sender) {
		return false, nil
	}

	if len(config.Filters) == 0 {
		return true, nil
	}

	applicable := false
	for i := range config.Filters {
		filter := &config.Filters[i]
		if filter.Event != eventData.Event {
			continue
		}
		applicable = true
		matched, err := matchesGitLabFilter(filter, eventData)
		if err != nil {
			return false, err
		}
		if matched {
			return true, nil
		}
	}
	// An event kind with no filter of its own is accepted as listed in Events.
	return !applicable, nil
}

func matchesGitLabFilter(filter *kelos.GitLabWebhookFilter, eventData *GitLabEventData) (bool, error) {
	if filter.Action != "" && !strings.EqualFold(filter.Action, eventData.Action) {
		return false, nil
	}
	if filter.State != "" && !strings.EqualFold(filter.State, eventData.State) {
		return false, nil
	}
	if filter.Status != "" && !strings.EqualFold(filter.Status, eventData.PipelineStatus) {
		return false, nil
	}
	if filter.NoteOn != "" && filter.NoteOn != eventData.NoteOn {
		return false, nil
	}
	if filter.Draft != nil && *filter.Draft != eventData.Draft {
		return false, nil
	}
	if filter.Author != "" && !strings.EqualFold(filter.Author, eventData.Sender) {
		return false, nil
	}
	if containsFold(filter.ExcludeAuthors, eventData.Sender) {
		return false, nil
	}

	if filter.Branch != "" {
		matched, err := filepath.Match(filter.Branch, eventData.Branch)
		if err != nil {
			return false, fmt.Errorf("invalid branch pattern %q: %w", filter.Branch, err)
		}
		if !matched {
			return false, nil
		}
	}

	if filter.BodyPattern != "" {
		matched, err := matchesPattern(eventData.CommentBody, filter.BodyPattern)
		if err != nil || !matched {
			return false, err
		}
	}
	if len(filter.ExcludeBodyPatterns) > 0 {
		excluded, err := matchesAnyPattern(eventData.CommentBody, filter.ExcludeBodyPatterns)
		if err != nil || excluded {
			return false, err
		}
	}

	if len(filter.Labels) > 0 || len(filter.ExcludeLabels) > 0 {
		present := make(map[string]bool, len(eventData.Labels))
		for _, l := range eventData.Labels {
			present[strings.ToLower(l)] = true
		}
		for _, required := range filter.Labels {
			if !present[strings.ToLower(required)] {
				return false, nil
			}
		}
		for _, excluded := range filter.ExcludeLabels {
			if present[strings.ToLower(excluded)] {
				return false, nil
			}
		}
	}

	return true, nil
}

// gitlabInstanceURL reduces a project web URL to the instance URL
// (scheme and host), which is the API base for that GitLab.
func gitlabInstanceURL(projectURL string) string {
	parsed, err := url.Parse(projectURL)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
}

func containsFold(list []string, value string) bool {
	for _, entry := range list {
		if strings.EqualFold(entry, value) {
			return true
		}
	}
	return false
}

// ExtractGitLabWorkItem converts GitLab webhook data to template variables.
// Every key is always present so templates never trip on a missing key.
func ExtractGitLabWorkItem(eventData *GitLabEventData) map[string]interface{} {
	return map[string]interface{}{
		"ID":             eventData.ID,
		"Title":          eventData.Title,
		"Kind":           eventData.Kind,
		"Number":         eventData.Number,
		"Body":           eventData.Body,
		"URL":            eventData.URL,
		"Event":          eventData.Event,
		"Action":         eventData.Action,
		"Sender":         eventData.Sender,
		"Ref":            eventData.Ref,
		"Branch":         eventData.Branch,
		"State":          eventData.State,
		"Labels":         strings.Join(eventData.Labels, ", "),
		"Repository":     eventData.Project,
		"RepositoryURL":  eventData.ProjectURL,
		"NoteOn":         eventData.NoteOn,
		"CommentBody":    eventData.CommentBody,
		"CommentURL":     eventData.CommentURL,
		"PipelineStatus": eventData.PipelineStatus,
		"PipelineURL":    eventData.PipelineURL,
		"HeadSHA":        eventData.HeadSHA,
		"Payload":        eventData.Payload,
	}
}
