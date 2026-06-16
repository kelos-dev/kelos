package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kelos "github.com/kelos-dev/kelos/api/v1alpha2"
)

func newCreateAgentConfigCommand(cfg *ClientConfig) *cobra.Command {
	var (
		agentsMD      string
		skillFlags    []string
		agentFlags    []string
		mcpFlags      []string
		skillsShFlags []string
		kanonRepo     string
		kanonRef      string
		kanonSecret   string
		dryRun        bool
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
			if kanonRepo == "" && (kanonRef != "" || kanonSecret != "") {
				return fmt.Errorf("--kanon-repo is required when --kanon-ref or --kanon-secret is set")
			}
			if kanonRepo != "" && (agentsMD != "" || len(skillFlags) > 0 || len(agentFlags) > 0 || len(mcpFlags) > 0 || len(skillsShFlags) > 0) {
				return fmt.Errorf("--kanon-repo cannot be combined with --agents-md, --skill, --agent, --mcp, or --skills-sh")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cl, ns, err := newClientOrDryRun(cfg, dryRun)
			if err != nil {
				return err
			}

			acSpec := kelos.AgentConfigSpec{}

			if kanonRepo != "" {
				acSpec.Kanon = &kelos.KanonSourceSpec{
					Repo: kanonRepo,
					Ref:  kanonRef,
				}
				if kanonSecret != "" {
					acSpec.Kanon.SecretRef = &kelos.SecretReference{Name: kanonSecret}
				}
			}

			resolvedMD, err := resolveContent(agentsMD)
			if err != nil {
				return fmt.Errorf("resolving --agents-md: %w", err)
			}
			acSpec.AgentsMD = resolvedMD

			if len(skillFlags) > 0 || len(agentFlags) > 0 {
				plugin := kelos.PluginSpec{Name: "kelos"}

				for _, s := range skillFlags {
					sn, sc, err := parseNameContent(s, "skill")
					if err != nil {
						return err
					}
					plugin.Skills = append(plugin.Skills, kelos.SkillDefinition{
						Name: sn, Content: sc,
					})
				}

				for _, a := range agentFlags {
					an, ac, err := parseNameContent(a, "agent")
					if err != nil {
						return err
					}
					plugin.Agents = append(plugin.Agents, kelos.AgentDefinition{
						Name: an, Content: ac,
					})
				}

				acSpec.Plugins = []kelos.PluginSpec{plugin}
			}

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

			skillsSeen := make(map[string]bool, len(skillsShFlags))
			for _, s := range skillsShFlags {
				spec, err := parseSkillsShFlag(s)
				if err != nil {
					return err
				}
				key := spec.Source + ":" + spec.Skill
				if skillsSeen[key] {
					return fmt.Errorf("duplicate --skills-sh entry %q", s)
				}
				skillsSeen[key] = true
				acSpec.Skills = append(acSpec.Skills, spec)
			}

			acObj := &kelos.AgentConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: ns,
				},
				Spec: acSpec,
			}

			acObj.SetGroupVersionKind(kelos.GroupVersion.WithKind("AgentConfig"))

			if dryRun {
				return printYAML(os.Stdout, acObj)
			}

			if err := createAgentConfig(context.Background(), cl, acObj); err != nil {
				return fmt.Errorf("creating agentconfig: %w", err)
			}
			fmt.Fprintf(os.Stdout, "agentconfig/%s created\n", name)
			return nil
		},
	}

	cmd.Flags().StringVar(&agentsMD, "agents-md", "", "agent instructions (content or @file path)")
	cmd.Flags().StringArrayVar(&skillFlags, "skill", nil, "skill definition as name=content or name=@file")
	cmd.Flags().StringArrayVar(&agentFlags, "agent", nil, "agent definition as name=content or name=@file")
	cmd.Flags().StringArrayVar(&mcpFlags, "mcp", nil, "MCP server as name=JSON or name=@file (e.g. github='{\"type\":\"http\",\"url\":\"https://api.githubcopilot.com/mcp/\"}')")
	cmd.Flags().StringArrayVar(&skillsShFlags, "skills-sh", nil, "skills.sh package as source or source:skill (e.g. vercel-labs/agent-skills:deploy)")
	cmd.Flags().StringVar(&kanonRepo, "kanon-repo", "", "Kanon source repository URL containing kanon.yaml at the repository root")
	cmd.Flags().StringVar(&kanonRef, "kanon-ref", "", "branch, tag, or commit to check out from --kanon-repo")
	cmd.Flags().StringVar(&kanonSecret, "kanon-secret", "", "secret containing GITHUB_TOKEN for cloning a private Kanon source repository")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resource that would be created without submitting it")

	return cmd
}
