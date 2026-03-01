package cli

import (
	"strings"
	"testing"
)

func TestParseGitHubPluginFlag(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantName   string
		wantRepo   string
		wantRef    string
		wantHost   string
		wantSecret string
		wantErr    bool
		wantErrStr string
	}{
		{
			name:     "basic public repo",
			input:    "my-plugin=acme/tools",
			wantName: "my-plugin",
			wantRepo: "acme/tools",
		},
		{
			name:     "repo with ref",
			input:    "my-plugin=acme/tools@v1.2.0",
			wantName: "my-plugin",
			wantRepo: "acme/tools",
			wantRef:  "v1.2.0",
		},
		{
			name:     "repo with host",
			input:    "my-plugin=acme/tools,host=github.corp.com",
			wantName: "my-plugin",
			wantRepo: "acme/tools",
			wantHost: "github.corp.com",
		},
		{
			name:       "repo with secret",
			input:      "my-plugin=acme/tools,secret=my-token",
			wantName:   "my-plugin",
			wantRepo:   "acme/tools",
			wantSecret: "my-token",
		},
		{
			name:       "all options",
			input:      "my-plugin=acme/tools@main,host=ghe.corp.com,secret=tok",
			wantName:   "my-plugin",
			wantRepo:   "acme/tools",
			wantRef:    "main",
			wantHost:   "ghe.corp.com",
			wantSecret: "tok",
		},
		{
			name:       "missing name",
			input:      "=acme/tools",
			wantErr:    true,
			wantErrStr: "invalid --github-plugin value",
		},
		{
			name:       "no equals sign",
			input:      "just-a-name",
			wantErr:    true,
			wantErrStr: "invalid --github-plugin value",
		},
		{
			name:       "empty string",
			input:      "",
			wantErr:    true,
			wantErrStr: "invalid --github-plugin value",
		},
		{
			name:       "invalid repo format",
			input:      "my-plugin=not-a-repo",
			wantErr:    true,
			wantErrStr: "owner/repo",
		},
		{
			name:       "too many slashes in repo",
			input:      "my-plugin=a/b/c",
			wantErr:    true,
			wantErrStr: "owner/repo",
		},
		{
			name:       "empty repo part",
			input:      "my-plugin=acme/",
			wantErr:    true,
			wantErrStr: "owner/repo",
		},
		{
			name:       "unknown option key",
			input:      "my-plugin=acme/tools,bogus=val",
			wantErr:    true,
			wantErrStr: "unknown --github-plugin option",
		},
		{
			name:       "trailing @ with empty ref",
			input:      "my-plugin=acme/tools@",
			wantErr:    true,
			wantErrStr: "ref must not be empty",
		},
		{
			name:       "empty repo",
			input:      "my-plugin=",
			wantErr:    true,
			wantErrStr: "repo is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := parseGitHubPluginFlag(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Expected error containing %q, got nil", tt.wantErrStr)
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("Expected error containing %q, got: %v", tt.wantErrStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if spec.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", spec.Name, tt.wantName)
			}
			if spec.GitHub == nil {
				t.Fatal("Expected GitHub source to be set")
			}
			if spec.GitHub.Repo != tt.wantRepo {
				t.Errorf("Repo = %q, want %q", spec.GitHub.Repo, tt.wantRepo)
			}
			if spec.GitHub.Ref != tt.wantRef {
				t.Errorf("Ref = %q, want %q", spec.GitHub.Ref, tt.wantRef)
			}
			if spec.GitHub.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", spec.GitHub.Host, tt.wantHost)
			}
			secretName := ""
			if spec.GitHub.SecretRef != nil {
				secretName = spec.GitHub.SecretRef.Name
			}
			if secretName != tt.wantSecret {
				t.Errorf("SecretRef.Name = %q, want %q", secretName, tt.wantSecret)
			}
		})
	}
}

func TestValidateMarketplacePluginFlag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errStr  string
	}{
		{
			name:  "valid plugin",
			input: "github@anthropics-claude-code",
		},
		{
			name:  "valid plugin with dashes",
			input: "commit-commands@claude-plugins-official",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
			errStr:  "must not be empty",
		},
		{
			name:    "no @ sign",
			input:   "just-a-name",
			wantErr: true,
			errStr:  "plugin-name@marketplace-name",
		},
		{
			name:    "leading @ sign",
			input:   "@marketplace",
			wantErr: true,
			errStr:  "plugin-name@marketplace-name",
		},
		{
			name:    "trailing @ sign",
			input:   "plugin@",
			wantErr: true,
			errStr:  "marketplace name must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMarketplacePluginFlag(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Expected error containing %q, got nil", tt.errStr)
				}
				if tt.errStr != "" && !strings.Contains(err.Error(), tt.errStr) {
					t.Errorf("Expected error containing %q, got: %v", tt.errStr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
		})
	}
}
