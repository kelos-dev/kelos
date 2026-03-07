package source

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

const defaultPromptTemplate = `{{.Kind}} #{{.Number}}: {{.Title}}

{{.Body}}
{{- if .Comments}}

Comments:
{{.Comments}}
{{- end}}`

// RenderPrompt renders a prompt for the given work item using the provided template.
// If promptTemplate is empty, a default template is used.
func RenderPrompt(promptTemplate string, item WorkItem) (string, error) {
	tmplStr := promptTemplate
	if tmplStr == "" {
		tmplStr = defaultPromptTemplate
	}
	return RenderTemplate(tmplStr, item)
}

// RenderTemplate renders a Go text/template string with the given work item's fields.
// Available variables (all sources): {{.ID}}, {{.Title}}, {{.Kind}}
// GitHub issue/Jira sources: {{.Number}}, {{.Body}}, {{.URL}}, {{.Labels}}, {{.Comments}}
// GitHub pull request sources additionally expose: {{.Branch}}, {{.ReviewState}}, {{.ReviewComments}}
// Cron sources: {{.Time}}, {{.Schedule}}
func RenderTemplate(tmplStr string, item WorkItem) (string, error) {
	tmpl, err := template.New("tmpl").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	kind := item.Kind
	if kind == "" {
		kind = "Issue"
	}

	data := struct {
		ID             string
		Number         int
		Title          string
		Body           string
		URL            string
		Labels         string
		Comments       string
		Kind           string
		Branch         string
		ReviewState    string
		ReviewComments string
		Time           string
		Schedule       string
	}{
		ID:             item.ID,
		Number:         item.Number,
		Title:          item.Title,
		Body:           item.Body,
		URL:            item.URL,
		Labels:         strings.Join(item.Labels, ", "),
		Comments:       item.Comments,
		Kind:           kind,
		Branch:         item.Branch,
		ReviewState:    item.ReviewState,
		ReviewComments: item.ReviewComments,
		Time:           item.Time,
		Schedule:       item.Schedule,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}

	return buf.String(), nil
}
