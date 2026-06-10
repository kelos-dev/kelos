package e2e

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const (
	claudeCodeModel = "haiku"
	codexModel      = "gpt-5.3-codex-spark"
)

var (
	oauthToken    string
	codexAuthJSON string
	githubToken   string
)

type agentTestConfig struct {
	AgentType        string
	CredentialType   kelosv1alpha1.CredentialType
	SecretName       string
	SecretKey        string
	SecretValue      *string
	Model            string
	EnvVar           string
	SupportsResponse bool
	SupportsCost     bool
}

var agentConfigs = []agentTestConfig{
	{
		AgentType:        "claude-code",
		CredentialType:   kelosv1alpha1.CredentialTypeOAuth,
		SecretName:       "claude-credentials",
		SecretKey:        "CLAUDE_CODE_OAUTH_TOKEN",
		SecretValue:      &oauthToken,
		Model:            claudeCodeModel,
		EnvVar:           "CLAUDE_CODE_OAUTH_TOKEN",
		SupportsResponse: true,
		SupportsCost:     true,
	},
	{
		AgentType:        "codex",
		CredentialType:   kelosv1alpha1.CredentialTypeOAuth,
		SecretName:       "codex-credentials",
		SecretKey:        "CODEX_AUTH_JSON",
		SecretValue:      &codexAuthJSON,
		Model:            codexModel,
		EnvVar:           "CODEX_AUTH_JSON",
		SupportsResponse: true,
	},
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	oauthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	codexAuthJSON = os.Getenv("CODEX_AUTH_JSON")
	githubToken = os.Getenv("GITHUB_TOKEN")

	// All listed agent credentials must be set to run the suite.
	for _, cfg := range agentConfigs {
		if *cfg.SecretValue == "" {
			Fail(cfg.EnvVar + " must be set")
		}
	}
})
