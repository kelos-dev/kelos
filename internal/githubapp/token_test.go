/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func generateTestKey(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return key, keyPEM
}

func generateTestKeyPKCS8(t *testing.T) (*rsa.PrivateKey, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling PKCS8 key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: der,
	})
	return key, keyPEM
}

func TestIsGitHubApp(t *testing.T) {
	tests := []struct {
		name string
		data map[string][]byte
		want bool
	}{
		{
			name: "PAT secret",
			data: map[string][]byte{"GITHUB_TOKEN": []byte("ghp_xxx")},
			want: false,
		},
		{
			name: "GitHub App secret",
			data: map[string][]byte{
				"appID":          []byte("12345"),
				"installationID": []byte("67890"),
				"privateKey":     []byte("fake-key"),
			},
			want: true,
		},
		{
			name: "Missing installationID",
			data: map[string][]byte{
				"appID":      []byte("12345"),
				"privateKey": []byte("fake-key"),
			},
			want: false,
		},
		{
			name: "Empty secret",
			data: map[string][]byte{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsGitHubApp(tt.data)
			if got != tt.want {
				t.Errorf("IsGitHubApp() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseCredentials(t *testing.T) {
	_, keyPEM := generateTestKey(t)
	_, keyPKCS8PEM := generateTestKeyPKCS8(t)

	tests := []struct {
		name    string
		data    map[string][]byte
		wantErr bool
	}{
		{
			name: "Valid PKCS1 key",
			data: map[string][]byte{
				"appID":          []byte("12345"),
				"installationID": []byte("67890"),
				"privateKey":     keyPEM,
			},
		},
		{
			name: "Valid PKCS8 key",
			data: map[string][]byte{
				"appID":          []byte("12345"),
				"installationID": []byte("67890"),
				"privateKey":     keyPKCS8PEM,
			},
		},
		{
			name: "Missing appID",
			data: map[string][]byte{
				"installationID": []byte("67890"),
				"privateKey":     keyPEM,
			},
			wantErr: true,
		},
		{
			name: "Missing installationID",
			data: map[string][]byte{
				"appID":      []byte("12345"),
				"privateKey": keyPEM,
			},
			wantErr: true,
		},
		{
			name: "Missing privateKey",
			data: map[string][]byte{
				"appID":          []byte("12345"),
				"installationID": []byte("67890"),
			},
			wantErr: true,
		},
		{
			name: "Invalid PEM",
			data: map[string][]byte{
				"appID":          []byte("12345"),
				"installationID": []byte("67890"),
				"privateKey":     []byte("not-a-pem"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds, err := ParseCredentials(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseCredentials() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCredentials() unexpected error: %v", err)
			}
			if creds.AppID != "12345" {
				t.Errorf("AppID = %q, want %q", creds.AppID, "12345")
			}
			if creds.InstallationID != "67890" {
				t.Errorf("InstallationID = %q, want %q", creds.InstallationID, "67890")
			}
			if creds.PrivateKey == nil {
				t.Error("PrivateKey is nil")
			}
		})
	}
}

func TestGenerateInstallationToken(t *testing.T) {
	_, keyPEM := generateTestKey(t)

	expiresAt := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Second)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/app/installations/67890/access_tokens" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		auth := r.Header.Get("Authorization")
		if auth == "" || len(auth) < 8 {
			t.Error("missing or invalid Authorization header")
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"token":      "ghs_test_token_123",
			"expires_at": expiresAt.Format(time.RFC3339),
		})
	}))
	defer server.Close()

	creds, err := ParseCredentials(map[string][]byte{
		"appID":          []byte("12345"),
		"installationID": []byte("67890"),
		"privateKey":     keyPEM,
	})
	if err != nil {
		t.Fatalf("ParseCredentials: %v", err)
	}

	tc := &TokenClient{
		BaseURL: server.URL,
		Client:  server.Client(),
	}

	resp, err := tc.GenerateInstallationToken(context.Background(), creds)
	if err != nil {
		t.Fatalf("GenerateInstallationToken: %v", err)
	}

	if resp.Token != "ghs_test_token_123" {
		t.Errorf("Token = %q, want %q", resp.Token, "ghs_test_token_123")
	}
	if !resp.ExpiresAt.Equal(expiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", resp.ExpiresAt, expiresAt)
	}
}

func TestGenerateInstallationToken_Error(t *testing.T) {
	_, keyPEM := generateTestKey(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer server.Close()

	creds, err := ParseCredentials(map[string][]byte{
		"appID":          []byte("12345"),
		"installationID": []byte("67890"),
		"privateKey":     keyPEM,
	})
	if err != nil {
		t.Fatalf("ParseCredentials: %v", err)
	}

	tc := &TokenClient{
		BaseURL: server.URL,
		Client:  server.Client(),
	}

	_, err = tc.GenerateInstallationToken(context.Background(), creds)
	if err == nil {
		t.Error("expected error for 401 response")
	}
}
