package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

// credentialNone is a reserved value for apiKey or oauthToken in the config
// file that indicates an empty credential. This is useful for agents like
// OpenCode that support free-tier models requiring no authentication.
const credentialNone = "none"

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
		prompt          string
		promptFile      string
		agentType       string
		secret          string
		credentialType  string
		model           string
		effort          string
		image           string
		name            string
		watch           bool
		workspace       string
		dryRun          bool
		yes             bool
		timeout         string
		envFlags        []string
		agentConfigRefs []string
		dependsOn       []string
		branch          string
		from            string
		valuesFile      string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create and run a new Task",
		RunE: func(cmd *cobra.Command, args []string) error {
			if from != "" {
				if err := validateTaskSpawnerRunFlags(cmd); err != nil {
					return err
				}
				spawnerName, err := parseTaskSpawnerReference(from)
				if err != nil {
					return err
				}
				values, err := readTaskTemplateValues(valuesFile, cmd.Flags().Changed("values"), cmd.InOrStdin())
				if err != nil {
					return err
				}
				cl, ns, err := cfg.NewClient()
				if err != nil {
					return err
				}
				options := runFromTaskSpawnerOptions{Name: name, Values: values, ErrorWriter: os.Stderr}
				if dryRun {
					task, err := buildTaskFromTaskSpawner(cmd.Context(), cl, ns, spawnerName, options)
					if err != nil {
						return err
					}
					return printYAML(os.Stdout, task)
				}
				task, err := createTaskFromTaskSpawner(cmd.Context(), cl, ns, spawnerName, options)
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "task/%s created\n", task.Name)
				if watch {
					return watchTask(cmd.Context(), cl, task.Name, ns, os.Stdout, os.Stderr)
				}
				return nil
			}
			if cmd.Flags().Changed("values") {
				return fmt.Errorf("--values requires --from")
			}

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
				if !cmd.Flags().Changed("effort") && c.Effort != "" {
					effort = c.Effort
				}
				if !cmd.Flags().Changed("workspace") && c.Workspace.Name != "" {
					workspace = c.Workspace.Name
				}
				if !cmd.Flags().Changed("agent-config") && c.AgentConfig != "" {
					agentConfigRefs = []string{c.AgentConfig}
				}
			}

			if !cmd.Flags().Changed("prompt") && !cmd.Flags().Changed("prompt-file") {
				return fmt.Errorf("either --prompt or --prompt-file is required")
			}
			if cmd.Flags().Changed("prompt-file") {
				var err error
				if promptFile == "-" {
					data, readErr := io.ReadAll(os.Stdin)
					if readErr != nil {
						return fmt.Errorf("reading prompt from stdin: %w", readErr)
					}
					prompt = strings.TrimRight(string(data), "\n")
				} else {
					prompt, err = resolveContent("@" + promptFile)
					if err != nil {
						return fmt.Errorf("resolving prompt file: %w", err)
					}
				}
				if prompt == "" {
					return fmt.Errorf("prompt file is empty")
				}
			}

			// Auto-create secret from token if no explicit secret is set.
			if secret == "" && cfg.Config != nil {
				sources := 0
				if cfg.Config.OAuthToken != "" {
					sources++
				}
				if cfg.Config.APIKey != "" {
					sources++
				}
				if sources > 1 {
					return fmt.Errorf("config file must specify only one of oauthToken or apiKey")
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

			if secret == "" && credentialType != "none" {
				return fmt.Errorf("no credentials configured (set oauthToken/apiKey in config file, or use --secret flag)")
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
					ws := &kelos.Workspace{
						ObjectMeta: metav1.ObjectMeta{
							Name:      wsName,
							Namespace: ns,
						},
						Spec: kelos.WorkspaceSpec{
							Repo: wsCfg.Repo,
							Ref:  wsCfg.Ref,
						},
					}
					if wsCfg.Token != "" {
						if err := ensureCredentialSecret(cfg, "kelos-workspace-credentials", "GITHUB_TOKEN", wsCfg.Token, yes); err != nil {
							return err
						}
						ws.Spec.SecretRef = &kelos.SecretReference{
							Name: "kelos-workspace-credentials",
						}
					} else if wsCfg.GitHubApp != nil {
						if err := ensureGitHubAppSecret(cfg, "kelos-workspace-credentials", wsCfg.GitHubApp, yes); err != nil {
							return err
						}
						ws.Spec.SecretRef = &kelos.SecretReference{
							Name: "kelos-workspace-credentials",
						}
					}
					ctx := context.Background()
					if err := cl.Create(ctx, ws); err != nil {
						if !apierrors.IsAlreadyExists(err) {
							return fmt.Errorf("creating workspace: %w", err)
						}
						existing := &kelos.Workspace{}
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

			creds := kelos.Credentials{
				Type: kelos.CredentialType(credentialType),
			}
			if secret != "" {
				creds.SecretRef = &kelos.SecretReference{Name: secret}
			}

			task := &kelos.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: kelos.TaskSpec{
					Type:        agentType,
					Prompt:      prompt,
					Credentials: &creds,
					Model:       model,
					Effort:      effort,
					Image:       image,
				},
			}

			if len(dependsOn) > 0 {
				task.Spec.DependsOn = dependsOn
			}
			if branch != "" {
				task.Spec.Branch = branch
			}

			if workspace != "" {
				task.Spec.WorkspaceRef = &kelos.WorkspaceReference{
					Name: workspace,
				}
			}

			if len(agentConfigRefs) > 0 {
				for _, name := range agentConfigRefs {
					task.Spec.AgentConfigRefs = append(task.Spec.AgentConfigRefs, kelos.AgentConfigReference{
						Name: name,
					})
				}
			}

			// Build PodOverrides from --timeout and --env flags.
			var po *kelos.PodOverrides
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
					po = &kelos.PodOverrides{}
				}
				po.ActiveDeadlineSeconds = &secs
			}
			// Merge env vars: start with config file env, then --env flags override.
			var envVars []corev1.EnvVar
			if c := cfg.Config; c != nil && len(c.Env) > 0 {
				for _, e := range c.Env {
					ev := corev1.EnvVar{
						Name:      e.Name,
						ValueFrom: e.ValueFrom.ToCorev1EnvVarSource(),
					}
					if e.Value != nil {
						ev.Value = *e.Value
					}
					envVars = append(envVars, ev)
				}
			}
			if len(envFlags) > 0 {
				for _, e := range envFlags {
					parts := strings.SplitN(e, "=", 2)
					if len(parts) != 2 || parts[0] == "" {
						return fmt.Errorf("invalid --env value %q: must be NAME=VALUE", e)
					}
					envVars = append(envVars, corev1.EnvVar{
						Name:  parts[0],
						Value: parts[1],
					})
				}
			}
			// Deduplicate: last occurrence wins (--env overrides config).
			// Iterate in reverse so the last occurrence keeps its position.
			if len(envVars) > 0 {
				seen := make(map[string]struct{})
				deduped := make([]corev1.EnvVar, 0, len(envVars))
				for i := len(envVars) - 1; i >= 0; i-- {
					if _, ok := seen[envVars[i].Name]; ok {
						continue
					}
					seen[envVars[i].Name] = struct{}{}
					deduped = append(deduped, envVars[i])
				}
				// Reverse to restore original relative order.
				for i, j := 0, len(deduped)-1; i < j; i, j = i+1, j-1 {
					deduped[i], deduped[j] = deduped[j], deduped[i]
				}
				if po == nil {
					po = &kelos.PodOverrides{}
				}
				po.Env = deduped
			}
			if po != nil {
				task.Spec.PodOverrides = po
			}

			task.SetGroupVersionKind(kelos.GroupVersion.WithKind("Task"))

			if dryRun {
				return printYAML(os.Stdout, task)
			}

			ctx := context.Background()
			if err := cl.Create(ctx, task); err != nil {
				return fmt.Errorf("creating task: %w", err)
			}
			fmt.Fprintf(os.Stdout, "task/%s created\n", name)

			if watch {
				return watchTask(ctx, cl, name, ns, os.Stdout, os.Stderr)
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&prompt, "prompt", "p", "", "task prompt (required unless --prompt-file or --from is set)")
	cmd.Flags().StringVar(&promptFile, "prompt-file", "", "read task prompt from a file (use - for stdin)")
	cmd.Flags().StringVarP(&agentType, "type", "t", "claude-code", "agent type (claude-code, codex, gemini, opencode, cursor)")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name with credentials (overrides oauthToken/apiKey in config)")
	cmd.Flags().StringVar(&credentialType, "credential-type", "api-key", "credential type (api-key, oauth, none)")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&effort, "effort", "", "agent reasoning effort")
	cmd.Flags().StringVar(&image, "image", "", "custom agent image (must implement agent image interface)")
	cmd.Flags().StringVar(&name, "name", "", "task name (auto-generated if omitted)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "name of Workspace resource to use")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "watch task status after creation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resource that would be created without submitting it")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompts")
	cmd.Flags().StringVar(&timeout, "timeout", "", "maximum execution time for the agent (e.g. 30m, 1h)")
	cmd.Flags().StringArrayVar(&envFlags, "env", nil, "additional environment variables for the agent (NAME=VALUE)")
	cmd.Flags().StringArrayVar(&agentConfigRefs, "agent-config", nil, "name of AgentConfig resource(s) to use (repeatable)")
	cmd.Flags().StringArrayVar(&dependsOn, "depends-on", nil, "Task names this task depends on (repeatable)")
	cmd.Flags().StringVar(&branch, "branch", "", "Git branch to work on")
	cmd.Flags().StringVar(&from, "from", "", "TaskSpawner reference in taskspawner/name form")
	cmd.Flags().StringVarP(&valuesFile, "values", "f", "", "template values file in YAML or JSON format (use - for stdin)")

	cmd.MarkFlagsMutuallyExclusive("prompt", "prompt-file")

	_ = cmd.RegisterFlagCompletionFunc("credential-type", cobra.FixedCompletions([]string{"api-key", "oauth", "none"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"claude-code", "codex", "gemini", "opencode", "cursor"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}

func watchTask(ctx context.Context, cl client.Client, name, namespace string, out, errOut io.Writer) error {
	var lastPhase kelos.TaskPhase
	for {
		task := &kelos.Task{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, task); err != nil {
			return fmt.Errorf("getting task: %w", err)
		}

		if task.Status.Phase != lastPhase {
			fmt.Fprintf(out, "task/%s %s\n", name, task.Status.Phase)
			lastPhase = task.Status.Phase
		}

		switch task.Status.Phase {
		case kelos.TaskPhaseSucceeded:
			return nil
		case kelos.TaskPhaseFailed:
			fmt.Fprintf(errOut, "Run 'kelos logs %s' to view agent output, or 'kelos get tasks %s -d' for details.\n", name, name)
			return fmt.Errorf("task %s failed", name)
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
	case "cursor":
		return "CURSOR_API_KEY"
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
	case "cursor":
		return "CURSOR_API_KEY"
	default:
		return "CLAUDE_CODE_OAUTH_TOKEN"
	}
}

// ensureGitHubAppSecret creates or updates a Secret with GitHub App credentials.
// If skipConfirm is false and the secret already exists, the user is prompted
// before overriding.
func ensureGitHubAppSecret(cfg *ClientConfig, name string, appCfg *GitHubAppConfig, skipConfirm bool) error {
	privateKeyPath, err := expandHome(appCfg.PrivateKeyPath)
	if err != nil {
		return fmt.Errorf("expanding privateKeyPath %s: %w", appCfg.PrivateKeyPath, err)
	}
	privateKey, err := os.ReadFile(privateKeyPath)
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
