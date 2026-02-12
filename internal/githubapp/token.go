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
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultGitHubAPIURL = "https://api.github.com"
)

// Credentials holds parsed GitHub App credentials.
type Credentials struct {
	AppID          string
	InstallationID string
	PrivateKey     *rsa.PrivateKey
}

// TokenResponse holds the result of a token exchange.
type TokenResponse struct {
	Token     string
	ExpiresAt time.Time
}

// TokenClient generates GitHub App installation tokens.
type TokenClient struct {
	BaseURL string
	Client  *http.Client
}

// NewTokenClient creates a new TokenClient with default settings.
func NewTokenClient() *TokenClient {
	return &TokenClient{
		BaseURL: defaultGitHubAPIURL,
		Client:  http.DefaultClient,
	}
}

// IsGitHubApp returns true if the Secret data contains GitHub App keys.
func IsGitHubApp(secretData map[string][]byte) bool {
	_, hasAppID := secretData["appID"]
	_, hasInstID := secretData["installationID"]
	_, hasKey := secretData["privateKey"]
	return hasAppID && hasInstID && hasKey
}

// ParseCredentials parses GitHub App credentials from Secret data.
func ParseCredentials(data map[string][]byte) (*Credentials, error) {
	appID, ok := data["appID"]
	if !ok || len(appID) == 0 {
		return nil, fmt.Errorf("missing appID in secret data")
	}
	installationID, ok := data["installationID"]
	if !ok || len(installationID) == 0 {
		return nil, fmt.Errorf("missing installationID in secret data")
	}
	privateKeyPEM, ok := data["privateKey"]
	if !ok || len(privateKeyPEM) == 0 {
		return nil, fmt.Errorf("missing privateKey in secret data")
	}

	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}

	return &Credentials{
		AppID:          strings.TrimSpace(string(appID)),
		InstallationID: strings.TrimSpace(string(installationID)),
		PrivateKey:     key,
	}, nil
}

// parsePrivateKey supports PKCS1 and PKCS8 PEM-encoded RSA private keys.
func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in private key")
	}

	// Try PKCS1 first
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	// Try PKCS8
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key as PKCS1 or PKCS8: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PKCS8 key is not RSA")
	}
	return key, nil
}

// GenerateInstallationToken exchanges GitHub App credentials for an installation token.
func (tc *TokenClient) GenerateInstallationToken(ctx context.Context, creds *Credentials) (*TokenResponse, error) {
	jwt, err := generateJWT(creds)
	if err != nil {
		return nil, fmt.Errorf("generating JWT: %w", err)
	}

	url := fmt.Sprintf("%s/app/installations/%s/access_tokens", tc.baseURL(), creds.InstallationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := tc.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting installation token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("GitHub API returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &TokenResponse{
		Token:     result.Token,
		ExpiresAt: result.ExpiresAt,
	}, nil
}

func (tc *TokenClient) baseURL() string {
	if tc.BaseURL != "" {
		return tc.BaseURL
	}
	return defaultGitHubAPIURL
}

func (tc *TokenClient) httpClient() *http.Client {
	if tc.Client != nil {
		return tc.Client
	}
	return http.DefaultClient
}

// generateJWT creates a signed RS256 JWT for GitHub App authentication.
func generateJWT(creds *Credentials) (string, error) {
	now := time.Now()
	header := base64URLEncode([]byte(`{"alg":"RS256","typ":"JWT"}`))

	payload := fmt.Sprintf(`{"iss":%q,"iat":%d,"exp":%d}`,
		creds.AppID,
		now.Add(-60*time.Second).Unix(),
		now.Add(10*time.Minute).Unix(),
	)
	encodedPayload := base64URLEncode([]byte(payload))

	signingInput := header + "." + encodedPayload
	hash := sha256.Sum256([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, creds.PrivateKey, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("signing JWT: %w", err)
	}

	return signingInput + "." + base64URLEncode(sig), nil
}

func base64URLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}
