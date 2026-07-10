/*
Copyright 2026 Kelos contributors.

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

package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/kelos-dev/kelos/internal/githubapp"
)

// githubTokenSecretName returns the name of the derived Secret that holds the
// short-lived GitHub App installation token minted for the resource named
// ownerName (a Task or a WorkerPool).
func githubTokenSecretName(ownerName string) string {
	return ownerName + "-github-token"
}

// tokenClientForRepo returns a per-call TokenClient targeting the GitHub API
// host derived from repoURL, falling back to base's BaseURL. A per-call client
// avoids racing on the shared client's BaseURL across concurrent reconciles
// that resolve to different GitHub Enterprise hosts.
func tokenClientForRepo(base *githubapp.TokenClient, repoURL string) *githubapp.TokenClient {
	tc := &githubapp.TokenClient{
		BaseURL: base.BaseURL,
		Client:  base.Client,
	}
	if repoURL != "" {
		host, _, _ := parseGitHubRepo(repoURL)
		if apiBaseURL := gitHubAPIBaseURL(host); apiBaseURL != "" {
			tc.BaseURL = apiBaseURL
		}
	}
	return tc
}

// mintGitHubAppTokenSecret generates a new installation token from the given
// GitHub App credentials and writes it into a derived Secret named
// tokenSecretName, owner-referenced to owner so it is garbage-collected with
// it. The derived Secret carries annotations recording the source App Secret
// name (marking it refreshable) and the token expiry so the refresh path can
// re-mint it. repoURL resolves the GitHub API host. It returns the token
// expiry.
func mintGitHubAppTokenSecret(ctx context.Context, cl client.Client, scheme *runtime.Scheme, tokenClient *githubapp.TokenClient, owner client.Object, tokenSecretName, appSecretName, repoURL string, creds *githubapp.Credentials) (time.Time, error) {
	tc := tokenClientForRepo(tokenClient, repoURL)
	tokenResp, err := tc.GenerateInstallationToken(ctx, creds)
	if err != nil {
		return time.Time{}, fmt.Errorf("generating installation token: %w", err)
	}

	annotations := map[string]string{
		githubAppSecretAnnotation: appSecretName,
		tokenExpiresAtAnnotation:  tokenResp.ExpiresAt.UTC().Format(time.RFC3339),
	}
	tokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        tokenSecretName,
			Namespace:   owner.GetNamespace(),
			Annotations: annotations,
		},
		StringData: map[string]string{
			GitHubTokenSecretKey: tokenResp.Token,
		},
	}

	if err := controllerutil.SetControllerReference(owner, tokenSecret, scheme); err != nil {
		return time.Time{}, fmt.Errorf("setting owner reference on token secret: %w", err)
	}

	if err := cl.Create(ctx, tokenSecret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return time.Time{}, fmt.Errorf("creating token secret: %w", err)
		}
		// Update the existing secret in place. Only adopt a Secret this owner
		// already controls: a Task and a WorkerPool can share a name in a
		// namespace and both derive <name>-github-token, so overwriting a
		// Secret owned by a different resource would leak the wrong token into
		// it and tie garbage collection to the wrong owner. Re-assert the
		// controller reference and retry on conflict like the other update
		// paths in this package.
		updateErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			existing := &corev1.Secret{}
			if err := cl.Get(ctx, client.ObjectKey{Name: tokenSecretName, Namespace: owner.GetNamespace()}, existing); err != nil {
				return err
			}
			if !metav1.IsControlledBy(existing, owner) {
				return fmt.Errorf("token secret %q already exists and is not controlled by %q", tokenSecretName, owner.GetName())
			}
			existing.StringData = tokenSecret.StringData
			if existing.Annotations == nil {
				existing.Annotations = map[string]string{}
			}
			for k, v := range annotations {
				existing.Annotations[k] = v
			}
			if err := controllerutil.SetControllerReference(owner, existing, scheme); err != nil {
				return err
			}
			return cl.Update(ctx, existing)
		})
		if updateErr != nil {
			return time.Time{}, fmt.Errorf("updating token secret: %w", updateErr)
		}
	}

	return tokenResp.ExpiresAt, nil
}

// refreshGitHubAppTokenSecret re-mints the installation token held in the
// derived Secret named tokenSecretName when it is within tokenRefreshMargin of
// expiry, updating the Secret in place. repoURL resolves the GitHub API host.
//
// It returns the duration until the next refresh should be scheduled, the new
// token expiry, and whether a refresh was actually performed (so callers can
// emit an event). A return of (0, zero, false, nil) means the token Secret is
// absent or is not App-derived, i.e. no refresh is applicable.
func refreshGitHubAppTokenSecret(ctx context.Context, cl client.Client, tokenClient *githubapp.TokenClient, namespace, tokenSecretName, repoURL string) (time.Duration, time.Time, bool, error) {
	var tokenSecret corev1.Secret
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: tokenSecretName}, &tokenSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return 0, time.Time{}, false, nil
		}
		return 0, time.Time{}, false, fmt.Errorf("fetching token secret %q: %w", tokenSecretName, err)
	}

	appSecretName := tokenSecret.Annotations[githubAppSecretAnnotation]
	if appSecretName == "" {
		return 0, time.Time{}, false, nil
	}

	retryAfter := tokenRefreshRetryInterval
	expiresAtStr := tokenSecret.Annotations[tokenExpiresAtAnnotation]
	if expiresAtStr != "" {
		expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
		if err == nil {
			retryAfter = tokenRefreshFailureRetryAfter(expiresAt)
			refreshAt := expiresAt.Add(-tokenRefreshMargin)
			if time.Now().Before(refreshAt) {
				return time.Until(refreshAt), time.Time{}, false, nil
			}
		}
	}

	if tokenClient == nil {
		return retryAfter, time.Time{}, false, fmt.Errorf("GitHub App token refresh requested but TokenClient is not configured")
	}

	var appSecret corev1.Secret
	if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: appSecretName}, &appSecret); err != nil {
		return retryAfter, time.Time{}, false, fmt.Errorf("fetching GitHub App secret %q: %w", appSecretName, err)
	}
	if !githubapp.IsGitHubApp(appSecret.Data) {
		return 0, time.Time{}, false, nil
	}

	creds, err := githubapp.ParseCredentials(appSecret.Data)
	if err != nil {
		return retryAfter, time.Time{}, false, fmt.Errorf("parsing GitHub App credentials: %w", err)
	}

	tc := tokenClientForRepo(tokenClient, repoURL)
	tokenResp, err := tc.GenerateInstallationToken(ctx, creds)
	if err != nil {
		return retryAfter, time.Time{}, false, fmt.Errorf("generating refreshed installation token: %w", err)
	}

	// StringData wins over Data on the apiserver, so writing through
	// StringData is the idiomatic way to update a Secret in place and
	// matches the initial-mint path.
	tokenSecret.StringData = map[string]string{
		GitHubTokenSecretKey: tokenResp.Token,
	}
	if tokenSecret.Annotations == nil {
		tokenSecret.Annotations = map[string]string{}
	}
	tokenSecret.Annotations[tokenExpiresAtAnnotation] = tokenResp.ExpiresAt.UTC().Format(time.RFC3339)
	if err := cl.Update(ctx, &tokenSecret); err != nil {
		return retryAfter, time.Time{}, false, fmt.Errorf("updating token secret with refreshed token: %w", err)
	}

	next := time.Until(tokenResp.ExpiresAt.Add(-tokenRefreshMargin))
	if next < 0 {
		next = 0
	}
	return next, tokenResp.ExpiresAt, true, nil
}
