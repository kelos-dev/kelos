package source

import "testing"

func TestGitHubRepositoryName(t *testing.T) {
	tests := map[string]string{
		"owner/repo":                            "owner/repo",
		"https://github.com/owner/repo.git":     "owner/repo",
		"https://github.example.com/owner/repo": "owner/repo",
		"git@github.com:owner/repo.git":         "owner/repo",
		"":                                      "",
	}
	for repoRef, want := range tests {
		if got := GitHubRepositoryName(repoRef); got != want {
			t.Errorf("GitHubRepositoryName(%q) = %q, want %q", repoRef, got, want)
		}
	}
}
