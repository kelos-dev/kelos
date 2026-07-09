package source

import "strings"

// GitHubRepositoryName returns a repository reference in owner/repo form.
func GitHubRepositoryName(repoRef string) string {
	repoRef = strings.TrimSuffix(repoRef, "/")
	repoRef = strings.TrimSuffix(repoRef, ".git")
	if repoRef == "" {
		return ""
	}

	parts := strings.SplitN(repoRef, "/", 2)
	if len(parts) == 2 && !strings.Contains(parts[0], ":") && !strings.Contains(parts[0], ".") {
		return repoRef
	}
	if idx := strings.Index(repoRef, ":"); idx > 0 && !strings.HasPrefix(repoRef, "http") {
		repoRef = repoRef[idx+1:]
	}
	parts = strings.Split(repoRef, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}
