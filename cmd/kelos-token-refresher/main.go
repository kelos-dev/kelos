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

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kelos-dev/kelos/internal/githubapp"
)

const (
	refreshInterval  = 45 * time.Minute
	initialRetryWait = 10 * time.Second
	maxInitialRetry  = 6
	tokenDir         = "/shared/token"
	tokenFile        = "GITHUB_TOKEN"
	privateKeyPath   = "/etc/github-app/privateKey"
)

func main() {
	appID := os.Getenv("APP_ID")
	installationID := os.Getenv("INSTALLATION_ID")

	if appID == "" || installationID == "" {
		fmt.Fprintln(os.Stderr, "APP_ID and INSTALLATION_ID environment variables are required")
		os.Exit(1)
	}

	keyData, err := os.ReadFile(privateKeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Reading private key from %s: %v\n", privateKeyPath, err)
		os.Exit(1)
	}

	creds, err := githubapp.ParseCredentials(map[string][]byte{
		"appID":          []byte(appID),
		"installationID": []byte(installationID),
		"privateKey":     keyData,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Parsing credentials: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	tc := githubapp.NewTokenClient()
	if apiURL := os.Getenv("GITHUB_API_BASE_URL"); apiURL != "" {
		tc.BaseURL = apiURL
	}

	fmt.Println("Starting token refresher")

	// Initial token generation with retries
	var initialized bool
	for attempt := 0; attempt < maxInitialRetry; attempt++ {
		if err := refreshToken(ctx, tc, creds); err != nil {
			fmt.Fprintf(os.Stderr, "Initial token generation attempt %d/%d failed: %v\n", attempt+1, maxInitialRetry, err)
			select {
			case <-ctx.Done():
				fmt.Println("Shutting down")
				return
			case <-time.After(initialRetryWait):
			}
			continue
		}
		fmt.Println("Initial token generated successfully")
		initialized = true
		break
	}
	if !initialized {
		fmt.Fprintln(os.Stderr, "Failed to generate initial token after all retries")
		os.Exit(1)
	}

	// Periodic refresh
	for {
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down")
			return
		case <-time.After(refreshInterval):
		}

		if err := refreshToken(ctx, tc, creds); err != nil {
			fmt.Fprintf(os.Stderr, "Refreshing token: %v\n", err)
		} else {
			fmt.Println("Token refreshed successfully")
		}
	}
}

func refreshToken(ctx context.Context, tc *githubapp.TokenClient, creds *githubapp.Credentials) error {
	resp, err := tc.GenerateInstallationToken(ctx, creds)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(tokenDir, 0o755); err != nil {
		return fmt.Errorf("creating token directory: %w", err)
	}

	// Atomic write: write to temp file, then rename
	tmpFile := filepath.Join(tokenDir, "."+tokenFile+".tmp")
	if err := os.WriteFile(tmpFile, []byte(resp.Token), 0o644); err != nil {
		return fmt.Errorf("writing temp token file: %w", err)
	}
	if err := os.Rename(tmpFile, filepath.Join(tokenDir, tokenFile)); err != nil {
		return fmt.Errorf("renaming token file: %w", err)
	}

	return nil
}
