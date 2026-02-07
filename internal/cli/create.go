package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func newCreateCommand(cfg *ClientConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create resources",
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.Help()
			return fmt.Errorf("must specify a resource type")
		},
	}

	cmd.AddCommand(newCreateWorkspaceCommand(cfg))
	cmd.AddCommand(newCreateTaskSpawnerCommand(cfg))

	return cmd
}

func newCreateWorkspaceCommand(cfg *ClientConfig) *cobra.Command {
	var (
		repo   string
		ref    string
		secret string
		token  string
	)

	cmd := &cobra.Command{
		Use:   "workspace <name>",
		Short: "Create a new workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if token != "" && secret != "" {
				return fmt.Errorf("cannot specify both --token and --secret")
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ws := &axonv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: axonv1alpha1.WorkspaceSpec{
					Repo: repo,
					Ref:  ref,
				},
			}

			if secret != "" {
				ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
					Name: secret,
				}
			} else if token != "" {
				secretName := name + "-credentials"
				if err := ensureCredentialSecret(cfg, secretName, "GITHUB_TOKEN", token); err != nil {
					return err
				}
				ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
					Name: secretName,
				}
			}

			ctx := context.Background()
			if err := cl.Create(ctx, ws); err != nil {
				return fmt.Errorf("creating workspace: %w", err)
			}
			fmt.Fprintf(os.Stdout, "workspace/%s created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "git repository URL (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "git reference to checkout (branch, tag, or commit SHA)")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name containing GITHUB_TOKEN")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (auto-creates a secret)")

	cmd.MarkFlagRequired("repo")

	return cmd
}

func newCreateTaskSpawnerCommand(cfg *ClientConfig) *cobra.Command {
	var (
		workspace      string
		agentType      string
		secret         string
		credentialType string
		model          string
		promptTemplate string
		pollInterval   string
		labels         []string
		excludeLabels  []string
		types          []string
		state          string
	)

	cmd := &cobra.Command{
		Use:     "taskspawner <name>",
		Aliases: []string{"ts"},
		Short:   "Create a new task spawner",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

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

			if workspace == "" {
				return fmt.Errorf("workspace is required (use --workspace flag or set workspace.name in config)")
			}

			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			ts := &axonv1alpha1.TaskSpawner{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: axonv1alpha1.TaskSpawnerSpec{
					When: axonv1alpha1.When{
						GitHubIssues: &axonv1alpha1.GitHubIssues{
							WorkspaceRef: &axonv1alpha1.WorkspaceReference{
								Name: workspace,
							},
							Types:         types,
							Labels:        labels,
							ExcludeLabels: excludeLabels,
							State:         state,
						},
					},
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: agentType,
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialType(credentialType),
							SecretRef: axonv1alpha1.SecretReference{
								Name: secret,
							},
						},
						Model:          model,
						PromptTemplate: promptTemplate,
					},
					PollInterval: pollInterval,
				},
			}

			ctx := context.Background()
			if err := cl.Create(ctx, ts); err != nil {
				return fmt.Errorf("creating task spawner: %w", err)
			}
			fmt.Fprintf(os.Stdout, "taskspawner/%s created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&workspace, "workspace", "", "name of Workspace resource (required)")
	cmd.Flags().StringVarP(&agentType, "type", "t", "claude-code", "agent type")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name with credentials (overrides oauthToken/apiKey in config)")
	cmd.Flags().StringVar(&credentialType, "credential-type", "api-key", "credential type (api-key or oauth)")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&promptTemplate, "prompt-template", "", "Go text/template for rendering the task prompt")
	cmd.Flags().StringVar(&pollInterval, "poll-interval", "5m", "how often to poll the source for new items")
	cmd.Flags().StringSliceVar(&labels, "labels", nil, "filter issues by labels")
	cmd.Flags().StringSliceVar(&excludeLabels, "exclude-labels", nil, "exclude issues with these labels")
	cmd.Flags().StringSliceVar(&types, "types", []string{"issues"}, "item types to discover (issues, pulls)")
	cmd.Flags().StringVar(&state, "state", "open", "filter issues by state (open, closed, all)")

	_ = cmd.RegisterFlagCompletionFunc("credential-type", cobra.FixedCompletions([]string{"api-key", "oauth"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("state", cobra.FixedCompletions([]string{"open", "closed", "all"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("types", cobra.FixedCompletions([]string{"issues", "pulls"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}
