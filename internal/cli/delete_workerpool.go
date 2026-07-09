package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newDeleteWorkerPoolCommand(cfg *ClientConfig) *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "workerpool [name]",
		Aliases: []string{"workerpools", "wp"},
		Short:   "Delete a worker pool",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return fmt.Errorf("cannot specify worker pool name with --all")
			}
			if !all {
				if len(args) == 0 {
					return fmt.Errorf("worker pool name is required (or use --all)\nUsage: %s", cmd.Use)
				}
				if len(args) > 1 {
					return fmt.Errorf("too many arguments: expected 1 worker pool name, got %d\nUsage: %s", len(args), cmd.Use)
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, ns, err := cfg.NewClient()
			if err != nil {
				return err
			}

			return runDeleteWorkerPool(context.Background(), cl, ns, args, all, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "Delete all worker pools in the namespace")
	cmd.ValidArgsFunction = completeWorkerPoolNames(cfg)

	return cmd
}

func runDeleteWorkerPool(ctx context.Context, cl client.Client, namespace string, args []string, all bool, out io.Writer) error {
	if all {
		wpList := &kelos.WorkerPoolList{}
		if err := cl.List(ctx, wpList, client.InNamespace(namespace)); err != nil {
			return fmt.Errorf("listing worker pools: %w", err)
		}
		if len(wpList.Items) == 0 {
			fmt.Fprintln(out, "No worker pools found")
			return nil
		}
		for i := range wpList.Items {
			if err := cl.Delete(ctx, &wpList.Items[i]); err != nil {
				return fmt.Errorf("deleting worker pool %s: %w", wpList.Items[i].Name, err)
			}
			fmt.Fprintf(out, "workerpool/%s deleted\n", wpList.Items[i].Name)
		}
		return nil
	}

	name := args[0]
	wp := &kelos.WorkerPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}

	if err := cl.Delete(ctx, wp); err != nil {
		return fmt.Errorf("deleting worker pool %s: %w", name, err)
	}
	fmt.Fprintf(out, "workerpool/%s deleted\n", name)
	return nil
}
