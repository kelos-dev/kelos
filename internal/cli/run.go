package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func newRunCommand(cfg *ClientConfig) *cobra.Command {
	var (
		prompt         string
		agentType      string
		secret         string
		credentialType string
		model          string
		name           string
		watch          bool
		workspace      string
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
				if !cmd.Flags().Changed("model") && c.Model != "" {
					model = c.Model
				}
				if !cmd.Flags().Changed("workspace") && c.Workspace.Name != "" {
					workspace = c.Workspace.Name
				}
			}

			// Auto-create secret from token if no explicit secret is set.
			if secret == "" && cfg.Config != nil {
				if cfg.Config.OAuthToken != "" && cfg.Config.APIKey != "" {
					return fmt.Errorf("config file must specify either oauthToken or apiKey, not both")
				}
				if token := cfg.Config.OAuthToken; token != "" {
					if err := ensureCredentialSecret(cfg, "axon-credentials", "CLAUDE_CODE_OAUTH_TOKEN", token); err != nil {
						return err
					}
					secret = "axon-credentials"
					credentialType = "oauth"
				} else if key := cfg.Config.APIKey; key != "" {
					if err := ensureCredentialSecret(cfg, "axon-credentials", "ANTHROPIC_API_KEY", key); err != nil {
						return err
					}
					secret = "axon-credentials"
					credentialType = "api-key"
				}
			}

			if secret == "" {
				return fmt.Errorf("no credentials configured (set oauthToken/apiKey in config file, or use --secret flag)")
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			// Auto-create Workspace CR from inline config if no --workspace flag.
			if workspace == "" && cfg.Config != nil && cfg.Config.Workspace.Repo != "" {
				wsCfg := cfg.Config.Workspace
				wsName := "axon-workspace"
				ws := &axonv1alpha1.Workspace{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wsName,
						Namespace: ns,
					},
					Spec: axonv1alpha1.WorkspaceSpec{
						Repo: wsCfg.Repo,
						Ref:  wsCfg.Ref,
					},
				}
				if wsCfg.Token != "" {
					if err := ensureCredentialSecret(cfg, "axon-workspace-credentials", "GITHUB_TOKEN", wsCfg.Token); err != nil {
						return err
					}
					ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
						Name: "axon-workspace-credentials",
					}
				}
				ctx := context.Background()
				if err := cl.Create(ctx, ws); err != nil {
					if !apierrors.IsAlreadyExists(err) {
						return fmt.Errorf("creating workspace: %w", err)
					}
					existing := &axonv1alpha1.Workspace{}
					if err := cl.Get(ctx, client.ObjectKey{Name: wsName, Namespace: ns}, existing); err != nil {
						return fmt.Errorf("fetching existing workspace: %w", err)
					}
					existing.Spec = ws.Spec
					if err := cl.Update(ctx, existing); err != nil {
						return fmt.Errorf("updating workspace: %w", err)
					}
				}
				workspace = wsName
			}

			if name == "" {
				name = "task-" + rand.String(5)
			}

			task := &axonv1alpha1.Task{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: axonv1alpha1.TaskSpec{
					Type:   agentType,
					Prompt: prompt,
					Credentials: axonv1alpha1.Credentials{
						Type: axonv1alpha1.CredentialType(credentialType),
						SecretRef: axonv1alpha1.SecretReference{
							Name: secret,
						},
					},
					Model: model,
				},
			}

			if workspace != "" {
				task.Spec.WorkspaceRef = &axonv1alpha1.WorkspaceReference{
					Name: workspace,
				}
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
	cmd.Flags().StringVarP(&agentType, "type", "t", "claude-code", "agent type")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name with credentials (overrides oauthToken/apiKey in config)")
	cmd.Flags().StringVar(&credentialType, "credential-type", "api-key", "credential type (api-key or oauth)")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&name, "name", "", "task name (auto-generated if omitted)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "name of Workspace resource to use")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "watch task status after creation")

	cmd.MarkFlagRequired("prompt")

	return cmd
}

func watchTask(ctx context.Context, cl client.Client, name, namespace string) error {
	var lastPhase axonv1alpha1.TaskPhase
	for {
		task := &axonv1alpha1.Task{}
		if err := cl.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, task); err != nil {
			return fmt.Errorf("getting task: %w", err)
		}

		if task.Status.Phase != lastPhase {
			fmt.Fprintf(os.Stdout, "task/%s %s\n", name, task.Status.Phase)
			lastPhase = task.Status.Phase
		}

		if task.Status.Phase == axonv1alpha1.TaskPhaseSucceeded || task.Status.Phase == axonv1alpha1.TaskPhaseFailed {
			return nil
		}

		time.Sleep(2 * time.Second)
	}
}

// ensureCredentialSecret creates or updates a Secret with the given credential key and value.
func ensureCredentialSecret(cfg *ClientConfig, name, key, value string) error {
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

	// Update existing secret, clearing stale keys.
	existing.Data = nil
	existing.StringData = secret.StringData
	if _, err := cs.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("updating credentials secret: %w", err)
	}
	return nil
}
