package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	axonv1alpha1 "github.com/gjkim42/axon/api/v1alpha1"
)

func completeTaskNames(cfg *ClientConfig) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cl, ns, err := cfg.NewClient()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		taskList := &axonv1alpha1.TaskList{}
		if err := cl.List(ctx, taskList, client.InNamespace(ns)); err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var names []string
		for _, t := range taskList.Items {
			names = append(names, t.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeTaskSpawnerNames(cfg *ClientConfig) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cl, ns, err := cfg.NewClient()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		tsList := &axonv1alpha1.TaskSpawnerList{}
		if err := cl.List(ctx, tsList, client.InNamespace(ns)); err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var names []string
		for _, ts := range tsList.Items {
			names = append(names, ts.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}
