package webhook

import (
	"testing"
)

func TestValidateGitHubSignature(t *testing.T) {
	secret := []byte("my-secret-key")
	payload := []byte(`{"action":"opened","number":1}`)

	tests := []struct {
		name      string
		signature string
		wantErr   bool
	}{
		{
			name:      "valid signature",
			signature: "sha256=fb463367c1f8d533acc23e11f8d09ff396c4e2ed73668fcf782f221f779e0424",
			wantErr:   false,
		},
		{
			name:      "invalid signature",
			signature: "sha256=invalid",
			wantErr:   true,
		},
		{
			name:      "missing prefix",
			signature: "fb463367c1f8d533acc23e11f8d09ff396c4e2ed73668fcf782f221f779e0424",
			wantErr:   true,
		},
		{
			name:      "empty signature",
			signature: "",
			wantErr:   true,
		},
		{
			name:      "wrong prefix",
			signature: "sha1=fb463367c1f8d533acc23e11f8d09ff396c4e2ed73668fcf782f221f779e0424",
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

	tests := []struct {
		name      string
		signature string
		wantErr   bool
	}{
		{
			name:      "valid signature",
			signature: "21a519179a2a5cb3cc9d6d86d2f8850ac21c048c672922d0cd0640438d645941",
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
