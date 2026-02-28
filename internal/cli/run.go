package cli

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

// credentialNone is a reserved value for apiKey or oauthToken in the config
// file that indicates an empty credential. This is useful for agents like
// OpenCode that support free-tier models requiring no authentication.
const credentialNone = "none"

// validAgentTypes lists the accepted values for the --type flag.
var validAgentTypes = []string{"claude-code", "codex", "gemini", "opencode", "custom"}

// validCredentialTypes lists the accepted values for the --credential-type flag.
var validCredentialTypes = []string{"api-key", "oauth"}

// resolveCredentialValue returns the actual credential value to store in the
// secret. The reserved value "none" maps to an empty string.
func resolveCredentialValue(v string) string {
	if v == credentialNone {
		return ""
	}
	return v
}

func newRunCommand(cfg *ClientConfig) *cobra.Command {
	var (
		prompt         string
		agentType      string
		secret         string
		credentialType string
		model          string
		image          string
		name           string
		watch          bool
		workspace      string
		dryRun         bool
		yes            bool
		timeout        string
		envFlags       []string
		agentConfigRef string
		dependsOn      []string
		branch         string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create and run a new Task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c := cfg.Config; c != nil {
				if !cmd.Flags().Changed("secret") && c.Secret != "" {
					secret = c.Secret
				}
				if !cmd.Flags().Changed("credential-type") && c.CredentialType != "" {
					credentialType = c.CredentialType
				}
				if !cmd.Flags().Changed("type") && c.Type != "" {
					agentType = c.Type
				}
				if !cmd.Flags().Changed("model") && c.Model != "" {
					model = c.Model
				}
				if !cmd.Flags().Changed("workspace") && c.Workspace.Name != "" {
					workspace = c.Workspace.Name
				}
				if !cmd.Flags().Changed("agent-config") && c.AgentConfig != "" {
					agentConfigRef = c.AgentConfig
				}
			}

			// Validate agent type.
			if !slices.Contains(validAgentTypes, agentType) {
				return fmt.Errorf("invalid agent type %q: must be one of: %s", agentType, strings.Join(validAgentTypes, ", "))
			}

			// Auto-create secret from token if no explicit secret is set.
			if secret == "" && cfg.Config != nil {
				if cfg.Config.OAuthToken != "" && cfg.Config.APIKey != "" {
					return fmt.Errorf("config file must specify either oauthToken or apiKey, not both")
				}
				if token := cfg.Config.OAuthToken; token != "" {
					resolved, err := resolveContent(token)
					if err != nil {
						return fmt.Errorf("resolving oauthToken: %w", err)
					}
					if !dryRun {
						oauthKey := oauthSecretKey(agentType)
						if err := ensureCredentialSecret(cfg, "kelos-credentials", oauthKey, resolveCredentialValue(resolved), yes); err != nil {
							return err
						}
					}
					secret = "kelos-credentials"
					credentialType = "oauth"
				} else if key := cfg.Config.APIKey; key != "" {
					resolved, err := resolveContent(key)
					if err != nil {
						return fmt.Errorf("resolving apiKey: %w", err)
					}
					if !dryRun {
						apiKey := apiKeySecretKey(agentType)
						if err := ensureCredentialSecret(cfg, "kelos-credentials", apiKey, resolveCredentialValue(resolved), yes); err != nil {
							return err
						}
					}
					secret = "kelos-credentials"
					credentialType = "api-key"
				}
			}

			if secret == "" {
				return fmt.Errorf("no credentials configured (set oauthToken/apiKey in config file, or use --secret flag)")
			}

			// Validate credential type (after auto-create may have set it).
			if !slices.Contains(validCredentialTypes, credentialType) {
				return fmt.Errorf("invalid credential type %q: must be one of: %s", credentialType, strings.Join(validCredentialTypes, ", "))
			}

			cl, ns, err := newClientOrDryRun(cfg, dryRun)
			if err != nil {
				return err
			}

			if dryRun {
				// Resolve workspace from inline config for dry-run.
				if workspace == "" && cfg.Config != nil && cfg.Config.Workspace.Repo != "" {
					workspace = "kelos-workspace"
				}
			} else {
				// Auto-create Workspace CR from inline config if no --workspace flag.
				if workspace == "" && cfg.Config != nil && cfg.Config.Workspace.Repo != "" {
					wsCfg := cfg.Config.Workspace

					if wsCfg.Token != "" && wsCfg.GitHubApp != nil {
						return fmt.Errorf("workspace config must specify either token or githubApp, not both")
					}

					wsName := "kelos-workspace"
					ws := &kelosv1alpha1.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name:      wsName,
							Namespace: ns,
						},
						Spec: kelosv1alpha1.WorkspaceSpec{
							Repo: wsCfg.Repo,
							Ref:  wsCfg.Ref,
						},
					}
					if wsCfg.Token != "" {
						if err := ensureCredentialSecret(cfg, "kelos-workspace-credentials", "GITHUB_TOKEN", wsCfg.Token, yes); err != nil {
							return err
						}
						ws.Spec.SecretRef = &kelosv1alpha1.SecretReference{
							Name: "kelos-workspace-credentials",
						}
					} else if wsCfg.GitHubApp != nil {
						if err := ensureGitHubAppSecret(cfg, "kelos-workspace-credentials", wsCfg.GitHubApp, yes); err != nil {
							return err
						}
						ws.Spec.SecretRef = &kelosv1alpha1.SecretReference{
							Name: "kelos-workspace-credentials",
						}
					}
					ctx := context.Background()
					if err := cl.Create(ctx, ws); err != nil {
						if !apierrors.IsAlreadyExists(err) {
							return fmt.Errorf("creating workspace: %w", err)
						}
						existing := &kelosv1alpha1.Workspace{}
						if err := cl.Get(ctx, client.ObjectKey{Name: wsName, Namespace: ns}, existing); err != nil {
							return fmt.Errorf("fetching existing workspace: %w", err)
						}
						if !reflect.DeepEqual(existing.Spec, ws.Spec) {
							if !yes {
								ok, confirmErr := confirmOverride(fmt.Sprintf("workspace/%s", wsName))
								if confirmErr != nil {
									return confirmErr
								}
								if !ok {
									return fmt.Errorf("aborted")
								}
							}
							existing.Spec = ws.Spec
							if err := cl.Update(ctx, existing); err != nil {
								return fmt.Errorf("updating workspace: %w", err)
							}
						}
					}
					workspace = wsName
				}
			}

			if name == "" {
				name = "task-" + rand.String(5)
			}

			task := &kelosv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: kelosv1alpha1.TaskSpec{
					Type:   agentType,
					Prompt: prompt,
					Credentials: kelosv1alpha1.Credentials{
						Type: kelosv1alpha1.CredentialType(credentialType),
						SecretRef: kelosv1alpha1.SecretReference{
							Name: secret,
						},
					},
					Model: model,
					Image: image,
				},
			}

			if len(dependsOn) > 0 {
				task.Spec.DependsOn = dependsOn
			}
			if branch != "" {
				task.Spec.Branch = branch
			}

			if workspace != "" {
				task.Spec.WorkspaceRef = &kelosv1alpha1.WorkspaceReference{
					Name: workspace,
				}
			}

			if agentConfigRef != "" {
				task.Spec.AgentConfigRef = &kelosv1alpha1.AgentConfigReference{
					Name: agentConfigRef,
				}
			}

			// Build PodOverrides from --timeout and --env flags.
			var po *kelosv1alpha1.PodOverrides
			if timeout != "" {
				d, err := time.ParseDuration(timeout)
				if err != nil {
					return fmt.Errorf("invalid --timeout value %q: %w", timeout, err)
				}
				secs := int64(d.Seconds())
				if secs < 1 {
					return fmt.Errorf("--timeout must be at least 1s")
				}
				if po == nil {
					po = &kelosv1alpha1.PodOverrides{}
				}
				po.ActiveDeadlineSeconds = &secs
			}
			if len(envFlags) > 0 {
				if po == nil {
					po = &kelosv1alpha1.PodOverrides{}
				}
				for _, e := range envFlags {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) != 2 || parts[0] == "" {
						return fmt.Errorf("invalid --env value %q: must be NAME=VALUE", e)
					}
					po.Env = append(po.Env, corev1.EnvVar{
						Name:  parts[0],
						Value: parts[1],
					})
				}
			}
			if po != nil {
				task.Spec.PodOverrides = po
			}

			task.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("Task"))

			if dryRun {
				return printYAML(os.Stdout, task)
			}

			ctx := context.Background()
			if err := cl.Create(ctx, task); err != nil {
				return fmt.Errorf("creating task: %w", err)
			}
			fmt.Fprintf(os.Stdout, "task/%s created\n", name)

			if watch {
				return watchTask(ctx, cl, name, ns)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "task prompt (required)")
	cmd.Flags().StringVarP(&agentType, "type", "t", "claude-code", fmt.Sprintf("agent type (%s)", strings.Join(validAgentTypes, ", ")))
	cmd.Flags().StringVar(&secret, "secret", "", "secret name with credentials (overrides oauthToken/apiKey in config)")
	cmd.Flags().StringVar(&credentialType, "credential-type", "api-key", fmt.Sprintf("credential type (%s)", strings.Join(validCredentialTypes, ", ")))
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&image, "image", "", "custom agent image (must implement agent image interface)")
	cmd.Flags().StringVar(&name, "name", "", "task name (auto-generated if omitted)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "name of Workspace resource to use")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "watch task status after creation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resource that would be created without submitting it")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompts")
	cmd.Flags().StringVar(&timeout, "timeout", "", "maximum execution time for the agent (e.g. 30m, 1h)")
	cmd.Flags().StringArrayVar(&envFlags, "env", nil, "additional environment variables for the agent (NAME=VALUE)")
	cmd.Flags().StringVar(&agentConfigRef, "agent-config", "", "name of AgentConfig resource to use")
	cmd.Flags().StringArrayVar(&dependsOn, "depends-on", nil, "Task names this task depends on (repeatable)")
	cmd.Flags().StringVar(&branch, "branch", "", "Git branch to work on")

	cmd.MarkFlagRequired("prompt")

	_ = cmd.RegisterFlagCompletionFunc("credential-type", cobra.FixedCompletions(validCredentialTypes, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions(validAgentTypes, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

func watchTask(ctx context.Context, cl client.Client, name, namespace string) error {
	var lastPhase kelosv1alpha1.TaskPhase
	for {
		task := &kelosv1alpha1.Task{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, task); err != nil {
			return fmt.Errorf("getting task: %w", err)
		}

		if task.Status.Phase != lastPhase {
			fmt.Fprintf(os.Stdout, "task/%s %s\n", name, task.Status.Phase)
			lastPhase = task.Status.Phase
		}

		if task.Status.Phase == kelosv1alpha1.TaskPhaseSucceeded || task.Status.Phase == kelosv1alpha1.TaskPhaseFailed {
			return nil
		}

		time.Sleep(2 * time.Second)
	}
}

// apiKeySecretKey returns the secret key name for API key credentials
// based on the agent type.
func apiKeySecretKey(agentType string) string {
	switch agentType {
	case "codex":
		return "CODEX_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "opencode":
		return "OPENCODE_API_KEY"
	case "custom":
		return "KELOS_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

// oauthSecretKey returns the secret key name for OAuth credentials
// based on the agent type.
func oauthSecretKey(agentType string) string {
	switch agentType {
	case "codex":
		return "CODEX_AUTH_JSON"
	case "gemini":
		return "GEMINI_API_KEY"
	case "opencode":
		return "OPENCODE_API_KEY"
	case "custom":
		return "KELOS_OAUTH_TOKEN"
	default:
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
}

// ensureGitHubAppSecret creates or updates a Secret with GitHub App credentials.
// If skipConfirm is false and the secret already exists, the user is prompted
// before overriding.
func ensureGitHubAppSecret(cfg *ClientConfig, name string, appCfg *GitHubAppConfig, skipConfirm bool) error {
	privateKey, err := os.ReadFile(appCfg.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("reading private key file %s: %w", appCfg.PrivateKeyPath, err)
	}

	cs, ns, err := cfg.NewClientset()
	if err != nil {
		return err
	}

	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		StringData: map[string]string{
			"appID":          appCfg.AppID,
			"installationID": appCfg.InstallationID,
			"privateKey":     string(privateKey),
		},
	}

	existing, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := cs.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating GitHub App secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking GitHub App secret: %w", err)
	}

	if !skipConfirm {
		ok, err := confirmOverride(fmt.Sprintf("secret/%s", name))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("aborted")
		}
	}

	existing.Data = nil
	existing.StringData = secret.StringData
	if _, err := cs.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating GitHub App secret: %w", err)
	}
	return nil
}

// ensureCredentialSecret creates or updates a Secret with the given credential key and value.
// If skipConfirm is false and the secret already exists with different data, the user is
// prompted before overriding. If the existing secret already contains the desired key/value
// and no other keys, the update is skipped.
func ensureCredentialSecret(cfg *ClientConfig, name, key, value string, skipConfirm bool) error {
	cs, ns, err := cfg.NewClientset()
	if err != nil {
		return err
	}

	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		StringData: map[string]string{
			key: value,
		},
	}

	existing, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := cs.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("creating credentials secret: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking credentials secret: %w", err)
	}

	// Skip update if the existing secret already has the exact same data.
	if len(existing.Data) == 1 {
		if v, ok := existing.Data[key]; ok && string(v) == value {
			return nil
		}
	}

	if !skipConfirm {
		ok, err := confirmOverride(fmt.Sprintf("secret/%s", name))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("aborted")
		}
	}

	// Update existing secret, clearing stale keys.
	existing.Data = nil
	existing.StringData = secret.StringData
	if _, err := cs.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating credentials secret: %w", err)
	}
	return nil
}
