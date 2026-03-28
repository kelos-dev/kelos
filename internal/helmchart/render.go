package helmchart

import (
	"bytes"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
)

// Render loads a Helm chart from the given embedded filesystem, merges the
// provided values with the chart defaults, renders the templates, and returns
// the result as a multi-document YAML byte slice suitable for parseManifests.
// CRDs under templates/ participate like any other template, so callers that
// need a controller-only manifest should disable CRD templates via values.
func Render(chartFS fs.FS, values map[string]interface{}) ([]byte, error) {
	ch, err := loadChart(chartFS)
	if err != nil {
		return nil, fmt.Errorf("loading chart: %w", err)
	}

	releaseOpts := chartutil.ReleaseOptions{
		Name:      "kelos",
		Namespace: "kelos-system",
	}
	vals, err := chartutil.ToRenderValues(ch, values, releaseOpts, nil)
	if err != nil {
		return nil, fmt.Errorf("preparing render values: %w", err)
	}

	rendered, err := engine.Render(ch, vals)
	if err != nil {
		return nil, fmt.Errorf("rendering templates: %w", err)
	}

	// Sort template names for deterministic base order.
	names := make([]string, 0, len(rendered))
	for name := range rendered {
		names = append(names, name)
	}
	sort.Strings(names)

	// Collect individual YAML documents from all rendered templates so we
	// can sort them by Kubernetes resource kind.  This ensures resources
	// like Namespace are applied before namespaced resources.
	type yamlDoc struct {
		content string
		order   int
		seqIdx  int // preserve original order for same-kind resources
	}
	var allDocs []yamlDoc
	seq := 0
	for _, name := range names {
		content := rendered[name]
		parts := strings.Split(content, "---\n")
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			if len(trimmed) == 0 {
				continue
			}
			allDocs = append(allDocs, yamlDoc{
				content: part,
				order:   kindOrder(extractKind(part)),
				seqIdx:  seq,
			})
			seq++
		}
	}

	sort.SliceStable(allDocs, func(i, j int) bool {
		if allDocs[i].order != allDocs[j].order {
			return allDocs[i].order < allDocs[j].order
		}
		return allDocs[i].seqIdx < allDocs[j].seqIdx
	})

	var buf bytes.Buffer
	for _, d := range allDocs {
		if buf.Len() > 0 {
			buf.WriteString("---\n")
		}
		buf.WriteString(d.content)
		if len(d.content) > 0 && d.content[len(d.content)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

// installOrderMap defines the order in which Kubernetes resource kinds should
// be applied to avoid dependency issues (e.g., Namespace before namespaced
// resources).  This follows the same ordering conventions as Helm.
var installOrderMap = map[string]int{
	"CustomResourceDefinition": -1,
	"Namespace":                0,
	"ServiceAccount":           1,
	"Secret":                   2,
	"ConfigMap":                3,
	"ClusterRole":              4,
	"ClusterRoleBinding":       5,
	"Role":                     6,
	"RoleBinding":              7,
	"Service":                  8,
	"Deployment":               9,
	"StatefulSet":              10,
	"Job":                      11,
	"CronJob":                  12,
}

func kindOrder(kind string) int {
	if order, ok := installOrderMap[kind]; ok {
		return order
	}
	return 100
}

func extractKind(doc string) string {
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "kind:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "kind:"))
		}
	}
	return ""
}

// loadChart reads chart files from the embedded FS and constructs a Helm chart.
func loadChart(chartFS fs.FS) (*chart.Chart, error) {
	var files []*loader.BufferedFile
	err := fs.WalkDir(chartFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(chartFS, path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		files = append(files, &loader.BufferedFile{
			Name: path,
			Data: data,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking chart filesystem: %w", err)
	}
	return loader.LoadFiles(files)
}
