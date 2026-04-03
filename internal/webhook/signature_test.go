package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// computeHMAC is a test helper that computes the HMAC-SHA256 hex digest for a payload.
func computeHMAC(payload, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestValidateGitHubSignature(t *testing.T) {
	secret := []byte("my-secret-key")
	payload := []byte(`{"action":"opened","number":1}`)
	validSig := "sha256=" + computeHMAC(payload, secret)

	tests := []struct {
		name      string
		signature string
		wantErr   bool
	}{
		{
			name:      "valid signature",
			signature: validSig,
			wantErr:   false,
		},
		{
			name:      "invalid signature",
			signature: "sha256=invalid",
			wantErr:   true,
		},
		{
			name:      "missing prefix",
			signature: computeHMAC(payload, secret),
			wantErr:   true,
		},
		{
			name:      "empty signature",
			signature: "",
			wantErr:   true,
		},
		{
			name:      "wrong prefix",
			signature: "sha1=" + computeHMAC(payload, secret),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubSignature(payload, tt.signature, secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitHubSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateLinearSignature(t *testing.T) {
	secret := []byte("linear-secret")
	payload := []byte(`{"action":"create","data":{"id":"123"}}`)
	validSig := computeHMAC(payload, secret)

	tests := []struct {
		name      string
		signature string
		wantErr   bool
	}{
		{
			name:      "valid signature",
			signature: validSig,
			wantErr:   false,
		},
		{
			name:      "invalid signature",
			signature: "invalid",
			wantErr:   true,
		},
		{
			name:      "empty signature",
			signature: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateLinearSignature(payload, tt.signature, secret)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateLinearSignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
