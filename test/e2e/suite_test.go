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
	SkipMessage    string
}

var agentConfigs = []agentTestConfig{
	{
		AgentType:      "claude-code",
		CredentialType: kelosv1alpha1.CredentialTypeOAuth,
		SecretName:     "claude-credentials",
		SecretKey:      "CLAUDE_CODE_OAUTH_TOKEN",
		SecretValue:    &oauthToken,
		Model:          testModel,
		SkipMessage:    "CLAUDE_CODE_OAUTH_TOKEN not set",
	},
	{
		AgentType:      "codex",
		CredentialType: kelosv1alpha1.CredentialTypeOAuth,
		SecretName:     "codex-credentials",
		SecretKey:      "CODEX_AUTH_JSON",
		SecretValue:    &codexAuthJSON,
		Model:          "gpt-5.1-codex-mini",
		SkipMessage:    "CODEX_AUTH_JSON not set",
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

	if oauthToken == "" && codexAuthJSON == "" {
		Skip("Neither CLAUDE_CODE_OAUTH_TOKEN nor CODEX_AUTH_JSON set, skipping e2e tests")
	}
})
