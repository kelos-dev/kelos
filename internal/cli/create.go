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
	cmd.AddCommand(newCreateAgentConfigCommand(cfg))

	return cmd
}

func newCreateWorkspaceCommand(cfg *ClientConfig) *cobra.Command {
	var (
		repo   string
		ref    string
		secret string
		token  string
		dryRun bool
		yes    bool
	)

	cmd := &cobra.Command{
		Use:     "workspace <name>",
		Aliases: []string{"ws"},
		Short:   "Create a workspace",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("workspace name is required\nUsage: %s", cmd.Use)
			}
			if len(args) > 1 {
				return fmt.Errorf("too many arguments: expected 1 workspace name, got %d\nUsage: %s", len(args), cmd.Use)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if secret != "" && token != "" {
				return fmt.Errorf("cannot specify both --secret and --token")
			}

			cl, ns, err := newClientOrDryRun(cfg, dryRun)
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
				if !dryRun {
					if err := ensureCredentialSecret(cfg, secretName, "GITHUB_TOKEN", token, yes); err != nil {
						return err
					}
				}
				ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
					Name: secretName,
				}
			} else if secret != "" {
				ws.Spec.SecretRef = &axonv1alpha1.SecretReference{
					Name: secret,
				}
			}

			ws.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("Workspace"))

			if dryRun {
				return printYAML(os.Stdout, ws)
			}

			if err := cl.Create(context.Background(), ws); err != nil {
				return fmt.Errorf("creating workspace: %w", err)
			}
			fmt.Fprintf(os.Stdout, "workspace/%s created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "git repository URL (required)")
	cmd.Flags().StringVar(&ref, "ref", "", "git reference (branch, tag, or commit SHA)")
	cmd.Flags().StringVar(&secret, "secret", "", "secret name containing GITHUB_TOKEN for git authentication")
	cmd.Flags().StringVar(&token, "token", "", "GitHub token (auto-creates a secret)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resource that would be created without submitting it")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip confirmation prompts")

	cmd.MarkFlagRequired("repo")

	cmd.ValidArgsFunction = completeWorkspaceNames(cfg)

	return cmd
}
