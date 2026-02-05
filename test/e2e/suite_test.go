package e2e

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const testModel = "haiku"

var oauthToken string

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	oauthToken = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if oauthToken == "" {
		Skip("CLAUDE_CODE_OAUTH_TOKEN not set, skipping e2e tests")
	}
})
