package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
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
		workspaceRepo  string
		workspaceRef   string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create and run a new Task",
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
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

			if workspaceRepo != "" {
				task.Spec.Workspace = &axonv1alpha1.Workspace{
					Repo: workspaceRepo,
					Ref:  workspaceRef,
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
	cmd.Flags().StringVar(&secret, "secret", "", "secret name with credentials (required)")
	cmd.Flags().StringVar(&credentialType, "credential-type", "api-key", "credential type (api-key or oauth)")
	cmd.Flags().StringVar(&model, "model", "", "model override")
	cmd.Flags().StringVar(&name, "name", "", "task name (auto-generated if omitted)")
	cmd.Flags().StringVar(&workspaceRepo, "workspace-repo", "", "git repository URL to clone")
	cmd.Flags().StringVar(&workspaceRef, "workspace-ref", "", "git reference (branch, tag, or commit SHA)")
	cmd.Flags().BoolVarP(&watch, "watch", "w", false, "watch task status after creation")

	cmd.MarkFlagRequired("prompt")
	cmd.MarkFlagRequired("secret")

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
