package cmd

import (
	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/agentkit"
)

func newAgentInstallCmd(_ *cliEnv) *cobra.Command {
	var f agentFlags
	var mcp, hooks, agentsMD, dryRun, force bool
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "install [--agent claude|codex|opencode|all] [--user|--project|--managed DIR]",
		Short:        "Install the agent kit: the skills, and with --mcp/--hooks the MCP server and the guard hook",
		Long: `Install the Teknoir agent kit for the coding agents of this computer.

Every agent is installed for, in the user scope, unless --agent, --project or --managed says otherwise.
Skills go to ~/.claude/skills (Claude Code) and ~/.agents/skills (Codex and OpenCode); --project
installs below the current directory instead.

--mcp registers the read-only "teknoir" MCP server (tnctl mcp) in the agent's config file, merging into
what is there and keeping every other server and key. --hooks installs the guard hook for Claude Code and
registers it as a PreToolUse hook for Bash and the teknoir MCP tools; the other agents get a note that
hooks are Claude Code only. --managed always installs the guard, and --hooks only adds the settings entry
to copy into Claude Code's managed settings. --agents-md adds the kit's section to ./AGENTS.md (project
scope).

A file you changed is never overwritten without --force: it is listed, the rest of the install runs, and
the exit code is non-zero. Every root gets a .tnctl-agent-kit.json manifest, so a later install upgrades
what it wrote and uninstall removes exactly that.`,
		Example: `  tnctl agent install
  tnctl agent install --agent claude --mcp --hooks
  tnctl agent install --agent codex,opencode --project --mcp
  tnctl agent install --managed /etc/claude-code/.claude --hooks
  tnctl agent install --dry-run`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if agentsMD && !f.project {
				return usageErr("--agents-md needs --project")
			}
			format, err := outputFormat()
			if err != nil {
				return err
			}
			o, err := f.options()
			if err != nil {
				return err
			}
			o.MCP = mcp
			o.Hooks = hooks
			o.AgentsMD = agentsMD
			o.DryRun = dryRun
			o.Force = force
			res, err := agentkit.Install(o)
			if res != nil && (err == nil || len(res.Actions) > 0) {
				if perr := printAgentResult(cmd.OutOrStdout(), cmd.ErrOrStderr(), format, res, dryRun); perr != nil {
					return perr
				}
			}
			return classifyAgentErr(err)
		},
	}
	f.addScopeFlags(c, true, "User scope: ~/.claude and ~/.agents (the default)")
	c.Flags().BoolVar(&mcp, "mcp", false, "Also register the read-only teknoir MCP server (tnctl mcp)")
	c.Flags().BoolVar(&hooks, "hooks", false, "Also install the guard hook (Claude Code only)")
	c.Flags().BoolVar(&agentsMD, "agents-md", false, "Add the kit's section to ./AGENTS.md (with --project)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would be done and write nothing")
	c.Flags().BoolVar(&force, "force", false,
		"Overwrite files the user changed and replace a differing teknoir MCP entry")
	return c
}

func init() { rootCmd.AddCommand(newAgentCmd(defaultEnv)) }
