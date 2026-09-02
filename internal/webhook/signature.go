package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ValidateGitHubSignature validates a GitHub webhook signature.
// GitHub sends signatures in the format "sha256=<hex-digest>".
func ValidateGitHubSignature(payload []byte, signature string, secret []byte) error {
	if signature == "" {
		return fmt.Errorf("missing signature")
	}

	// GitHub signature format: "sha256=<hex-digest>"
	if !strings.HasPrefix(signature, "sha256=") {
		return fmt.Errorf("invalid signature format: expected sha256= prefix")
	}

	expectedSig := signature[7:] // Remove "sha256=" prefix
	return validateHMACSignature(payload, expectedSig, secret)
}

// ValidateLinearSignature validates a Linear webhook signature.
// Linear sends signatures as raw HMAC-SHA256 hex digest.
func ValidateLinearSignature(payload []byte, signature string, secret []byte) error {
	if signature == "" {
		return fmt.Errorf("missing signature")
	}

	return validateHMACSignature(payload, signature, secret)
}

// ValidateGitLabToken validates a GitLab webhook delivery. GitLab does not sign
// payloads; it sends the configured secret verbatim in the X-Gitlab-Token
// header, so the check is constant-time equality for equal-length inputs
// (hmac.Equal returns immediately if lengths differ).
func ValidateGitLabToken(token string, secret []byte) error {
	if token == "" {
		return fmt.Errorf("missing token")
	}
	if !hmac.Equal([]byte(token), secret) {
		return fmt.Errorf("token verification failed")
	}
	return nil
}

// validateHMACSignature performs HMAC-SHA256 validation against the expected hex digest.
func validateHMACSignature(payload []byte, expectedSig string, secret []byte) error {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	actualSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(actualSig), []byte(expectedSig)) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}
