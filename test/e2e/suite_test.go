package e2e

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

const (
	claudeCodeModel = "haiku"
	codexModel      = "gpt-5.3-codex-spark"
	openCodeModel   = "opencode/big-pickle"
)

var (
	oauthToken        string
	codexAuthJSON     string
	openCodeAPIKey    string
	githubToken       string
	skillsGithubToken string
)

type agentTestConfig struct {
	AgentType        string
	CredentialType   kelos.CredentialType
	SecretName       string
	SecretKey        string
	SecretValue      *string
	Model            string
	EnvVar           string
	NeedsCredentials bool
	SupportsResponse bool
	SupportsCost     bool
}

func (cfg agentTestConfig) credentialsMissing() bool {
	return cfg.NeedsCredentials && *cfg.SecretValue == ""
}

var agentConfigs = []agentTestConfig{
	{
		AgentType:        "claude-code",
		CredentialType:   kelos.CredentialTypeOAuth,
		SecretName:       "claude-credentials",
		SecretKey:        "CLAUDE_CODE_OAUTH_TOKEN",
		SecretValue:      &oauthToken,
		Model:            claudeCodeModel,
		EnvVar:           "CLAUDE_CODE_OAUTH_TOKEN",
		NeedsCredentials: true,
		SupportsResponse: true,
		SupportsCost:     true,
	},
	{
		AgentType:        "codex",
		CredentialType:   kelos.CredentialTypeOAuth,
		SecretName:       "codex-credentials",
		SecretKey:        "CODEX_AUTH_JSON",
		SecretValue:      &codexAuthJSON,
		Model:            codexModel,
		EnvVar:           "CODEX_AUTH_JSON",
		NeedsCredentials: true,
		SupportsResponse: true,
	},
	{
		AgentType:      "opencode",
		CredentialType: kelos.CredentialTypeAPIKey,
		SecretName:     "opencode-credentials",
		SecretKey:      "OPENCODE_API_KEY",
		SecretValue:    &openCodeAPIKey,
		Model:          openCodeModel,
		EnvVar:         "OPENCODE_API_KEY",
	},
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	oauthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	codexAuthJSON = os.Getenv("CODEX_AUTH_JSON")
	openCodeAPIKey = os.Getenv("OPENCODE_API_KEY")
	githubToken = os.Getenv("GITHUB_TOKEN")
	skillsGithubToken = os.Getenv("E2E_SKILLS_GITHUB_TOKEN")

	// All required agent credentials must be set to run the suite.
	for _, cfg := range agentConfigs {
		if cfg.credentialsMissing() {
			Fail(cfg.EnvVar + " must be set")
		}
	}
})
