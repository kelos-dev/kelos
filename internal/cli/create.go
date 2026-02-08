package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
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
		name   string
		repo   string
		ref    string
		secret string
		token  string
	)

	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Create a workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			if secret != "" && token != "" {
				return fmt.Errorf("cannot specify both --secret and --token")
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

			if token != "" {
				secretName := name + "-credentials"
				if err := ensureCredentialSecret(cfg, secretName, "GITHUB_TOKEN", token); err != nil {
					return err
				}
				ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
					Name: secretName,
				}
			} else if secret != "" {
				ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
					Name: secret,
				}
			}

			if err := cl.Create(context.Background(), ws); err != nil {
				return fmt.Errorf("creating workspace: %w", err)
			}
			fmt.Fprintf(os.Stdout, "workspace/%s created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "workspace name (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "git repository URL (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "git reference (branch, tag, or commit SHA)")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name containing GITHUB_TOKEN for git authentication")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (auto-creates a secret)")

	cmd.MarkFlagRequired("name")
	cmd.MarkFlagRequired("repo")

	return cmd
}

func newCreateTaskSpawnerCommand(cfg *ClientConfig) *cobra.Command {
	var (
		name           string
		workspace      string
		secret         string
		credentialType string
		model          string
		schedule       string
		state          string
		promptTemplate string
	)

	cmd := &cobra.Command{
		Use:     "taskspawner",
		Aliases: []string{"ts"},
		Short:   "Create a task spawner",
		RunE: func(cmd *cobra.Command, args []string) error {
			if workspace == "" && schedule == "" {
				return fmt.Errorf("must specify either --workspace (for GitHub issues source) or --schedule (for cron source)")
			}
			if workspace != "" && schedule != "" {
				return fmt.Errorf("cannot specify both --workspace and --schedule")
			}

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
			}

			if secret == "" {
				return fmt.Errorf("no credentials configured (set secret in config file, or use --secret flag)")
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
					TaskTemplate: axonv1alpha1.TaskTemplate{
						Type: "claude-code",
						Credentials: axonv1alpha1.Credentials{
							Type: axonv1alpha1.CredentialType(credentialType),
							SecretRef: axonv1alpha1.SecretReference{
								Name: secret,
							},
						},
						Model:          model,
						PromptTemplate: promptTemplate,
					},
				},
			}

			if workspace != "" {
				ts.Spec.TaskTemplate.WorkspaceRef = &axonv1alpha1.WorkspaceReference{
					Name: workspace,
				}
				ts.Spec.When.GitHubIssues = &axonv1alpha1.GitHubIssues{
					State: state,
				}
			} else {
				ts.Spec.When.Cron = &axonv1alpha1.Cron{
					Schedule: schedule,
				}
			}

			if err := cl.Create(context.Background(), ts); err != nil {
				return fmt.Errorf("creating task spawner: %w", err)
			}
			fmt.Fprintf(os.Stdout, "taskspawner/%s created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "task spawner name (required)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace name (for GitHub issues source)")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name with credentials")
	cmd.Flags().StringVar(&credentialType, "credential-type", "api-key", "credential type (api-key or oauth)")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&schedule, "schedule", "", "cron schedule expression (for cron source)")
	cmd.Flags().StringVar(&state, "state", "open", "GitHub issue state filter (open, closed, all)")
	cmd.Flags().StringVar(&promptTemplate, "prompt-template", "", "Go text/template for rendering the task prompt")

	cmd.MarkFlagRequired("name")

	_ = cmd.RegisterFlagCompletionFunc("credential-type", cobra.FixedCompletions([]string{"api-key", "oauth"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("state", cobra.FixedCompletions([]string{"open", "closed", "all"}, cobra.ShellCompDirectiveNoFileComp))

	return cmd
}
