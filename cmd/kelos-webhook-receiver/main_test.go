package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateGitHubSignature_ValidSignature(t *testing.T) {
	// Set up environment
	t.Setenv("GITHUB_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"opened"}`)

	// Compute expected signature
	// echo -n '{"action":"opened"}' | openssl dgst -sha256 -hmac 'test-secret'
	// Result: sha256=6e939b5b3d3e8eba83ff81dde0030a8f2190d965e8bec7a17842863e979c4d7d
	expectedSig := "sha256=6e939b5b3d3e8eba83ff81dde0030a8f2190d965e8bec7a17842863e979c4d7d"

	headers := http.Header{}
	headers.Set("X-Hub-Signature-256", expectedSig)

	err := validateGitHubSignature(headers, payload)
	if err != nil {
		t.Errorf("Expected valid signature, got error: %v", err)
	}
}

func TestValidateGitHubSignature_InvalidSignature(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"opened"}`)

	headers := http.Header{}
	headers.Set("X-Hub-Signature-256", "sha256=wrongsignature")

	err := validateGitHubSignature(headers, payload)
	if err == nil {
		t.Error("Expected error for invalid signature, got nil")
	}
}

func TestValidateGitHubSignature_MissingHeader(t *testing.T) {
	t.Setenv("GITHUB_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"opened"}`)
	headers := http.Header{}

	err := validateGitHubSignature(headers, payload)
	if err == nil {
		t.Error("Expected error for missing signature header, got nil")
	}
}

func TestValidateGitHubSignature_NoSecretConfigured(t *testing.T) {
	// Don't set GITHUB_WEBHOOK_SECRET - should skip validation
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")

	payload := []byte(`{"action":"opened"}`)
	headers := http.Header{}
	headers.Set("X-Hub-Signature-256", "sha256=anysignature")

	err := validateGitHubSignature(headers, payload)
	if err != nil {
		t.Errorf("Expected no error when secret not configured, got: %v", err)
	}
}

func TestWebhookHandler_MethodNotAllowed(t *testing.T) {
	handler := &webhookHandler{}

	req := httptest.NewRequest(http.MethodGet, "/webhook/github", nil)
	w := httptest.NewRecorder()

	handler.handle(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestWebhookHandler_MissingSource(t *testing.T) {
	handler := &webhookHandler{}

	req := httptest.NewRequest(http.MethodPost, "/webhook/", nil)
	w := httptest.NewRecorder()

	handler.handle(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestValidateLinearSignature_ValidSignature(t *testing.T) {
	t.Setenv("LINEAR_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"create","type":"Issue"}`)

	// Compute expected signature
	// echo -n '{"action":"create","type":"Issue"}' | openssl dgst -sha256 -hmac 'test-secret'
	expectedSig := "3b4c0e7668708bcb65b6103de3d28cae0bead64460615aaa232f645b96568741"

	headers := http.Header{}
	headers.Set("X-Linear-Signature", expectedSig)

	err := validateLinearSignature(headers, payload)
	if err != nil {
		t.Errorf("Expected valid signature, got error: %v", err)
	}
}

func TestValidateLinearSignature_InvalidSignature(t *testing.T) {
	t.Setenv("LINEAR_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"create","type":"Issue"}`)

	headers := http.Header{}
	headers.Set("X-Linear-Signature", "wrongsignature")

	err := validateLinearSignature(headers, payload)
	if err == nil {
		t.Error("Expected error for invalid signature, got nil")
	}
}

func TestValidateLinearSignature_MissingHeader(t *testing.T) {
	t.Setenv("LINEAR_WEBHOOK_SECRET", "test-secret")

	payload := []byte(`{"action":"create","type":"Issue"}`)
	headers := http.Header{}

	err := validateLinearSignature(headers, payload)
	if err == nil {
		t.Error("Expected error for missing signature header, got nil")
	}
}

func TestValidateLinearSignature_NoSecretConfigured(t *testing.T) {
	// Don't set LINEAR_WEBHOOK_SECRET - should skip validation
	t.Setenv("LINEAR_WEBHOOK_SECRET", "")

	payload := []byte(`{"action":"create","type":"Issue"}`)
	headers := http.Header{}
	headers.Set("X-Linear-Signature", "anysignature")

	err := validateLinearSignature(headers, payload)
	if err != nil {
		t.Errorf("Expected no error when secret not configured, got: %v", err)
	}
}
