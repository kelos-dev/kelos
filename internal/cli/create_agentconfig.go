package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelosv1alpha1 "github.com/kelos-dev/kelos/api/v1alpha1"
)

func newCreateAgentConfigCommand(cfg *ClientConfig) *cobra.Command {
	var (
		agentsMD               string
		skillFlags             []string
		agentFlags             []string
		githubPluginFlags      []string
		marketplacePluginFlags []string
		mcpFlags               []string
		dryRun                 bool
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

			acSpec := kelosv1alpha1.AgentConfigSpec{}

			resolvedMD, err := resolveContent(agentsMD)
			if err != nil {
				return fmt.Errorf("resolving --agents-md: %w", err)
			}
			acSpec.AgentsMD = resolvedMD

			if len(skillFlags) > 0 || len(agentFlags) > 0 {
				plugin := kelosv1alpha1.PluginSpec{Name: "kelos"}

				for _, s := range skillFlags {
					sn, sc, err := parseNameContent(s, "skill")
					if err != nil {
						return err
					}
					plugin.Skills = append(plugin.Skills, kelosv1alpha1.SkillDefinition{
						Name: sn, Content: sc,
					})
				}

				for _, a := range agentFlags {
					an, ac, err := parseNameContent(a, "agent")
					if err != nil {
						return err
					}
					plugin.Agents = append(plugin.Agents, kelosv1alpha1.AgentDefinition{
						Name: an, Content: ac,
					})
				}

				acSpec.Plugins = append(acSpec.Plugins, plugin)
			}

			pluginNameSeen := make(map[string]bool)
			if len(skillFlags) > 0 || len(agentFlags) > 0 {
				pluginNameSeen["kelos"] = true
			}
			for _, gp := range githubPluginFlags {
				gpSpec, err := parseGitHubPluginFlag(gp)
				if err != nil {
					return err
				}
				if pluginNameSeen[gpSpec.Name] {
					return fmt.Errorf("duplicate --github-plugin name %q", gpSpec.Name)
				}
				pluginNameSeen[gpSpec.Name] = true
				acSpec.Plugins = append(acSpec.Plugins, gpSpec)
			}

			mpSeen := make(map[string]bool, len(marketplacePluginFlags))
			for _, mp := range marketplacePluginFlags {
				if err := validateMarketplacePluginFlag(mp); err != nil {
					return err
				}
				if mpSeen[mp] {
					return fmt.Errorf("duplicate --marketplace-plugin %q", mp)
				}
				mpSeen[mp] = true
			}
			acSpec.MarketplacePlugins = marketplacePluginFlags

			mcpSeen := make(map[string]bool, len(mcpFlags))
			for _, m := range mcpFlags {
				mcpSpec, err := parseMCPFlag(m)
				if err != nil {
					return err
				}
				if mcpSeen[mcpSpec.Name] {
					return fmt.Errorf("duplicate --mcp server name %q", mcpSpec.Name)
				}
				mcpSeen[mcpSpec.Name] = true
				acSpec.MCPServers = append(acSpec.MCPServers, mcpSpec)
			}

			acObj := &kelosv1alpha1.AgentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: acSpec,
			}

			acObj.SetGroupVersionKind(kelosv1alpha1.GroupVersion.WithKind("AgentConfig"))

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
	cmd.Flags().StringArrayVar(&githubPluginFlags, "github-plugin", nil, "GitHub plugin as name=owner/repo[@ref][,host=HOST][,secret=SECRET]")
	cmd.Flags().StringArrayVar(&marketplacePluginFlags, "marketplace-plugin", nil, "marketplace plugin as plugin-name@marketplace-name")
	cmd.Flags().StringArrayVar(&mcpFlags, "mcp", nil, "MCP server as name=JSON or name=@file (e.g. github='{\"type\":\"http\",\"url\":\"https://api.githubcopilot.com/mcp/\"}')")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resource that would be created without submitting it")

	return cmd
}
