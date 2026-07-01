package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
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

		taskList := &kelos.TaskList{}
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

		tsList := &kelos.TaskSpawnerList{}
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

func completeAgentConfigNames(cfg *ClientConfig) cobra.CompletionFunc {
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

		items, _, err := listAgentConfigs(ctx, cl, client.InNamespace(ns))
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var names []string
		for _, ac := range items {
			names = append(names, ac.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeWorkspaceNames(cfg *ClientConfig) cobra.CompletionFunc {
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

		wsList := &kelos.WorkspaceList{}
		if err := cl.List(ctx, wsList, client.InNamespace(ns)); err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var names []string
		for _, ws := range wsList.Items {
			names = append(names, ws.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

func completeWorkerPoolNames(cfg *ClientConfig) cobra.CompletionFunc {
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

		wpList := &kelos.WorkerPoolList{}
		if err := cl.List(ctx, wpList, client.InNamespace(ns)); err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var names []string
		for _, wp := range wpList.Items {
			names = append(names, wp.Name)
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}
