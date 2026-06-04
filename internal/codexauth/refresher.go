// Package codexauth refreshes Codex OAuth credentials stored in Kubernetes
// Secrets by running the Codex CLI against a file-backed auth.json.
package codexauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// RefreshLabel is the Secret label that opts a credentials Secret into
	// scheduled Codex OAuth refresh.
	RefreshLabel = "kelos.dev/codex-oauth-refresh"

	secretKey    = "CODEX_AUTH_JSON"
	staleRefresh = "1970-01-01T00:00:00Z"
)

// Runner invokes Codex with seeded auth.json bytes and returns the auth.json
// bytes Codex wrote back.
type Runner func(context.Context, []byte) ([]byte, error)

// Options configures a refresh run.
type Options struct {
	// Namespace is the namespace of the target Secret.
	Namespace string
	// SecretName is the name of the target Secret.
	SecretName string
	// Runner refreshes a single auth.json bundle. Defaults to DefaultRunner.
	Runner Runner
}

func (o *Options) applyDefaults() {
	if o.Runner == nil {
		o.Runner = DefaultRunner
	}
}

// Run refreshes one opted-in Codex OAuth credentials Secret. Secrets without
// CODEX_AUTH_JSON, without the opt-in label, or whose bundle carries no
// refresh_token are skipped.
func Run(ctx context.Context, clientset kubernetes.Interface, opts Options) error {
	opts.applyDefaults()
	if clientset == nil {
		return errors.New("clientset is required")
	}
	if opts.Namespace == "" {
		return errors.New("namespace is required")
	}
	if opts.SecretName == "" {
		return errors.New("secret name is required")
	}

	secret, err := clientset.CoreV1().Secrets(opts.Namespace).Get(ctx, opts.SecretName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting Codex OAuth Secret %s/%s: %w", opts.Namespace, opts.SecretName, err)
	}
	if secret.Labels[RefreshLabel] != "true" {
		log("Skipping Codex OAuth Secret without refresh label", "namespace", secret.Namespace, "name", secret.Name)
		return nil
	}

	changed, err := refreshSecret(ctx, clientset, secret, opts.Runner)
	if err != nil {
		return fmt.Errorf("secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}
	if !changed {
		log("Skipped Codex OAuth Secret", "namespace", secret.Namespace, "name", secret.Name)
	}
	return nil
}

func refreshSecret(ctx context.Context, clientset kubernetes.Interface, s *corev1.Secret, runner Runner) (bool, error) {
	raw, ok := s.Data[secretKey]
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return false, nil
	}

	seeded, ok, err := seedAuth(raw)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	updated, err := runner(ctx, seeded)
	if err != nil {
		return false, err
	}
	if bytes.Equal(bytes.TrimSpace(updated), bytes.TrimSpace(raw)) {
		return false, nil
	}

	current, err := clientset.CoreV1().Secrets(s.Namespace).Get(ctx, s.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("secret no longer exists: %w", err)
		}
		return false, fmt.Errorf("getting Secret: %w", err)
	}
	if current.Data == nil {
		current.Data = map[string][]byte{}
	}
	current.Data[secretKey] = updated
	if _, err := clientset.CoreV1().Secrets(s.Namespace).Update(ctx, current, metav1.UpdateOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Errorf("secret no longer exists: %w", err)
		}
		return false, fmt.Errorf("updating Secret: %w", err)
	}

	log("Refreshed Codex OAuth credential", "namespace", s.Namespace, "name", s.Name)
	return true, nil
}

func seedAuth(raw []byte) ([]byte, bool, error) {
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return nil, false, fmt.Errorf("parsing auth.json bundle: %w", err)
	}
	tokens, ok := bundle["tokens"].(map[string]any)
	if !ok {
		return nil, false, nil
	}
	refreshToken, _ := tokens["refresh_token"].(string)
	if refreshToken == "" {
		return nil, false, nil
	}
	bundle["last_refresh"] = staleRefresh

	seeded, err := json.Marshal(bundle)
	if err != nil {
		return nil, false, fmt.Errorf("building seeded auth.json bundle: %w", err)
	}
	return seeded, true, nil
}

// DefaultRunner refreshes auth.json by invoking the Codex CLI with
// cli_auth_credentials_store=file.
func DefaultRunner(ctx context.Context, seeded []byte) ([]byte, error) {
	root, err := os.MkdirTemp("", "kelos-codex-auth-")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(root)

	codexHome := filepath.Join(root, ".codex")
	workdir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return nil, fmt.Errorf("creating Codex home: %w", err)
	}
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return nil, fmt.Errorf("creating workspace: %w", err)
	}

	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath, seeded, 0o600); err != nil {
		return nil, fmt.Errorf("writing auth.json: %w", err)
	}
	configPath := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(configPath, []byte("cli_auth_credentials_store = \"file\"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("writing Codex config: %w", err)
	}

	cmd := exec.CommandContext(ctx, "codex",
		"exec",
		"--skip-git-repo-check",
		"--sandbox", "read-only",
		"--ask-for-approval", "never",
		"-C", workdir,
		"Reply with the single word OK.",
	)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome, "HOME="+root)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running Codex refresh command: %w", err)
	}

	refreshed, err := os.ReadFile(authPath)
	if err != nil {
		return nil, fmt.Errorf("reading refreshed auth.json: %w", err)
	}
	if err := verifyRefreshed(refreshed); err != nil {
		return nil, err
	}
	return refreshed, nil
}

func verifyRefreshed(raw []byte) error {
	var bundle map[string]any
	if err := json.Unmarshal(raw, &bundle); err != nil {
		return fmt.Errorf("parsing refreshed auth.json bundle: %w", err)
	}
	lastRefresh, _ := bundle["last_refresh"].(string)
	if lastRefresh == "" || lastRefresh == staleRefresh {
		return errors.New("Codex did not refresh auth.json")
	}
	return nil
}

func log(message string, fields ...any) {
	fmt.Fprint(os.Stderr, message)
	for i := 0; i+1 < len(fields); i += 2 {
		fmt.Fprintf(os.Stderr, " %s=%v", fields[i], fields[i+1])
	}
	fmt.Fprintln(os.Stderr)
}
