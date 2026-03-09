package e2e

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

const testModel = "haiku"

var (
	oauthToken    string
	codexAuthJSON string
	githubToken   string
)

type agentTestConfig struct {
	AgentType      string
	CredentialType kelosv1alpha1.CredentialType
	SecretName     string
	SecretKey      string
	SecretValue    *string
	Model          string
	EnvVar         string
}

var agentConfigs = []agentTestConfig{
	{
		AgentType:      "claude-code",
		CredentialType: kelosv1alpha1.CredentialTypeOAuth,
		SecretName:     "claude-credentials",
		SecretKey:      "CLAUDE_CODE_OAUTH_TOKEN",
		SecretValue:    &oauthToken,
		Model:          testModel,
		EnvVar:         "CLAUDE_CODE_OAUTH_TOKEN",
	},
	{
		AgentType:      "codex",
		CredentialType: kelosv1alpha1.CredentialTypeOAuth,
		SecretName:     "codex-credentials",
		SecretKey:      "CODEX_AUTH_JSON",
		SecretValue:    &codexAuthJSON,
		Model:          "gpt-5.1-codex-mini",
		EnvVar:         "CODEX_AUTH_JSON",
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

	// Each credential env var is checked individually so that a
	// misconfigured CI secret surfaces as a clear test failure
	// instead of silently skipping the related agent tests.
	for _, cfg := range agentConfigs {
		if _, ok := os.LookupEnv(cfg.EnvVar); ok && *cfg.SecretValue == "" {
			Fail(cfg.EnvVar + " is set but empty")
		}
	}
	if oauthToken == "" && codexAuthJSON == "" {
		Fail("No agent credentials set (CLAUDE_CODE_OAUTH_TOKEN, CODEX_AUTH_JSON)")
	}
})
