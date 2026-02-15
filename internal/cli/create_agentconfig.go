package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	axonv1alpha1 "github.com/axon-core/axon/api/v1alpha1"
)

func newCreateAgentConfigCommand(cfg *ClientConfig) *cobra.Command {
	var (
		agentsMD   string
		skillFlags []string
		agentFlags []string
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:     "agentconfig <name>",
		Aliases: []string{"ac"},
		Short:   "Create an AgentConfig resource",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return fmt.Errorf("agentconfig name is required\nUsage: %s", cmd.Use)
			}
			if len(args) > 1 {
				return fmt.Errorf("too many arguments: expected 1 agentconfig name, got %d\nUsage: %s", len(args), cmd.Use)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cl, ns, err := newClientOrDryRun(cfg, dryRun)
			if err != nil {
				return err
			}

			acSpec := axonv1alpha1.AgentConfigSpec{}

			resolvedMD, err := resolveContent(agentsMD)
			if err != nil {
				return fmt.Errorf("resolving --agents-md: %w", err)
			}
			acSpec.AgentsMD = resolvedMD

			if len(skillFlags) > 0 || len(agentFlags) > 0 {
				plugin := axonv1alpha1.PluginSpec{Name: "axon"}

				for _, s := range skillFlags {
					sn, sc, err := parseNameContent(s, "skill")
					if err != nil {
						return err
					}
					plugin.Skills = append(plugin.Skills, axonv1alpha1.SkillDefinition{
						Name: sn, Content: sc,
					})
				}

				for _, a := range agentFlags {
					an, ac, err := parseNameContent(a, "agent")
					if err != nil {
						return err
					}
					plugin.Agents = append(plugin.Agents, axonv1alpha1.AgentDefinition{
						Name: an, Content: ac,
					})
				}

				acSpec.Plugins = []axonv1alpha1.PluginSpec{plugin}
			}

			acObj := &axonv1alpha1.AgentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: acSpec,
			}

			acObj.SetGroupVersionKind(axonv1alpha1.GroupVersion.WithKind("AgentConfig"))

			if dryRun {
				return printYAML(os.Stdout, acObj)
			}

			if err := cl.Create(context.Background(), acObj); err != nil {
				return fmt.Errorf("creating agentconfig: %w", err)
			}
			fmt.Fprintf(os.Stdout, "agentconfig/%s created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&agentsMD, "agents-md", "", "agent instructions (content or @file path)")
	cmd.Flags().StringArrayVar(&skillFlags, "skill", nil, "skill definition as name=content or name=@file")
	cmd.Flags().StringArrayVar(&agentFlags, "agent", nil, "agent definition as name=content or name=@file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resource that would be created without submitting it")

	return cmd
}
