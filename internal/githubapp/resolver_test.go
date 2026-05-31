package githubapp

import (
	"context"
	"testing"
)

func TestNewSecretTokenResolver(t *testing.T) {
	t.Run("personal access token", func(t *testing.T) {
		r, err := NewSecretTokenResolver(map[string][]byte{"GITHUB_TOKEN": []byte("tok")}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r == nil {
			t.Fatal("expected a resolver")
		}
		got, err := r(context.Background())
		if err != nil || got != "tok" {
			t.Errorf("resolver returned (%q, %v), want (tok, nil)", got, err)
		}
	})

	t.Run("no credentials returns nil resolver", func(t *testing.T) {
		r, err := NewSecretTokenResolver(map[string][]byte{}, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r != nil {
			t.Error("expected nil resolver when no credentials are present")
		}
	})

	t.Run("partial GitHub App credentials error", func(t *testing.T) {
		// appID without installationID/privateKey is a misconfiguration, not
		// "no credentials".
		_, err := NewSecretTokenResolver(map[string][]byte{"appID": []byte("123")}, "")
		if err == nil {
			t.Error("expected an error for partial GitHub App credentials")
		}
	})
}
