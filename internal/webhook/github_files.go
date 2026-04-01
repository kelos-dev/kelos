package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	defaultGitHubAPIBaseURL = "https://api.github.com"
	prFilesMaxPages         = 10
)

var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

type githubFile struct {
	Filename string `json:"filename"`
}

// fetchPRChangedFiles fetches the list of changed files for a pull request
// from the GitHub API. It resolves the GitHub token from the workspace's
// secretRef.
func fetchPRChangedFiles(ctx context.Context, cl client.Client, spawner *v1alpha1.TaskSpawner, owner, repo string, number int) ([]string, error) {
	token, err := resolveGitHubTokenFromWorkspace(ctx, cl, spawner)
	if err != nil {
		return nil, fmt.Errorf("resolving GitHub token for PR files: %w", err)
	}

	pageURL := fmt.Sprintf("%s/repos/%s/%s/pulls/%d/files?per_page=100",
		defaultGitHubAPIBaseURL, owner, repo, number)

	httpClient := &http.Client{}

	var allFiles []githubFile
	for page := 0; pageURL != "" && page < prFilesMaxPages; page++ {
		var files []githubFile
		nextURL, err := fetchGitHubFilesPage(ctx, httpClient, pageURL, token, &files)
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, files...)
		pageURL = nextURL
	}

	paths := make([]string, len(allFiles))
	for i, f := range allFiles {
		paths[i] = f.Filename
	}
	return paths, nil
}

// resolveGitHubTokenFromWorkspace reads the GITHUB_TOKEN from the workspace's
// secretRef. Returns an empty string (no auth) if no workspace or secret is
// configured.
func resolveGitHubTokenFromWorkspace(ctx context.Context, cl client.Client, spawner *v1alpha1.TaskSpawner) (string, error) {
	wsRef := spawner.Spec.TaskTemplate.WorkspaceRef
	if wsRef == nil {
		return "", nil
	}

	var ws v1alpha1.Workspace
	if err := cl.Get(ctx, types.NamespacedName{
		Name:      wsRef.Name,
		Namespace: spawner.Namespace,
	}, &ws); err != nil {
		return "", fmt.Errorf("fetching workspace %s: %w", wsRef.Name, err)
	}

	if ws.Spec.SecretRef == nil {
		return "", nil
	}

	var secret corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{
		Name:      ws.Spec.SecretRef.Name,
		Namespace: spawner.Namespace,
	}, &secret); err != nil {
		return "", fmt.Errorf("fetching secret %s: %w", ws.Spec.SecretRef.Name, err)
	}

	return string(secret.Data["GITHUB_TOKEN"]), nil
}

func fetchGitHubFilesPage(ctx context.Context, httpClient *http.Client, pageURL, token string, out *[]githubFile) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}

	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	matches := linkNextRe.FindStringSubmatch(resp.Header.Get("Link"))
	if len(matches) >= 2 {
		return matches[1], nil
	}
	return "", nil
}
