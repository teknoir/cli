package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	kit "github.com/teknoir/cli/agentkit"
	"github.com/teknoir/cli/pkg/agentkit"
)

// agentFlags are the flags the `tnctl agent` subcommands share: which agents, and where the kit goes.
type agentFlags struct {
	agents  []string
	user    bool
	project bool
	managed string
}

// addScopeFlags adds --agent and the three scopes (mutually exclusive) to a command.
func (f *agentFlags) addScopeFlags(c *cobra.Command, withAgents bool) {
	if withAgents {
		c.Flags().StringSliceVar(&f.agents, "agent", []string{"all"},
			"Agents: claude, codex, opencode or all (repeat or comma-separate)")
	}
	c.Flags().BoolVar(&f.user, "user", false, "User scope: ~/.claude and ~/.agents (the default)")
	c.Flags().BoolVar(&f.project, "project", false,
		"Project scope: ./.claude, ./.agents and the agent config files of this directory")
	c.Flags().StringVar(&f.managed, "managed", "", "Managed scope: skills and hooks below DIR, and nothing else")
	c.MarkFlagsMutuallyExclusive("user", "project", "managed")
}

// options builds the agentkit options of the scope the flags name.
func (f *agentFlags) options() (*agentkit.Options, error) {
	agents, err := agentkit.ResolveAgents(f.agents)
	if err != nil {
		return nil, usageErr("%v", err)
	}
	o := &agentkit.Options{Agents: agents, Scope: agentkit.ScopeUser, FS: kit.FS, KitVersion: kit.Version,
		TnctlVersion: Version}
	if home, err := os.UserHomeDir(); err == nil {
		o.Home = home
	}
	// The user-scope config directories the agents themselves read: an empty variable is unset.
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		o.ClaudeConfigDir = dir
		o.ClaudeConfigFile = filepath.Join(dir, ".claude.json")
	}
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		o.CodexHome = dir
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		o.OpencodeConfigDir = filepath.Join(dir, "opencode")
	}
	if wd, err := os.Getwd(); err == nil {
		o.ProjectDir = wd
	}
	switch {
	case f.managed != "":
		o.Scope = agentkit.ScopeManaged
		o.ManagedDir = f.managed
		if !filepath.IsAbs(o.ManagedDir) && o.ProjectDir != "" {
			o.ManagedDir = filepath.Join(o.ProjectDir, o.ManagedDir)
		}
	case f.project:
		o.Scope = agentkit.ScopeProject
	}
	return o, nil
}

// listOptions are the scopes `agent list` visits: both the user and the project scope unless a flag
// names one.
func (f *agentFlags) listOptions() ([]*agentkit.Options, error) {
	o, err := f.options()
	if err != nil {
		return nil, err
	}
	if f.user || f.project || f.managed != "" {
		return []*agentkit.Options{o}, nil
	}
	user, project := *o, *o
	user.Scope, project.Scope = agentkit.ScopeUser, agentkit.ScopeProject
	return []*agentkit.Options{&user, &project}, nil
}

func newAgentCmd(e *cliEnv) *cobra.Command {
	c := &cobra.Command{
		Use:   "agent",
		Short: "Install the Teknoir agent kit for coding agents (Claude Code, Codex, OpenCode)",
		Long: `Install the Teknoir agent kit: the Agent Skills, the guard hook and the registration of the
read-only tnctl MCP server, for the coding agents on this computer.

The kit ships inside tnctl, so it installs offline and matches the tnctl that installs it.`,
		Example: `  tnctl agent install --agent claude --mcp
  tnctl agent list --check`,
	}
	c.AddCommand(newAgentInstallCmd(e), newAgentListCmd(e), newAgentUninstallCmd(e))
	return c
}

func newAgentListCmd(_ *cliEnv) *cobra.Command {
	var f agentFlags
	var check bool
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "list [--check] [--user|--project|--managed DIR]",
		Short:        "Show the agent kit installs of this computer and whether they match this tnctl",
		Long: `Show the agent kit installs of this computer: every root that has a manifest, the kit version
it holds and the one this tnctl carries, the files missing, changed or outdated, and which agent config
files register the teknoir MCP server.

Both scopes are visited unless --user, --project or --managed names one.

--check exits non-zero when anything differs from the kit this tnctl carries (a missing or changed file,
or another kit version): for CI and for the TeknoirBot image test.`,
		Example: `  tnctl agent list
  tnctl agent list --check
  tnctl agent list --managed /etc/claude-code/.claude --check`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			scopes, err := f.listOptions()
			if err != nil {
				return err
			}
			res := &agentkit.ListResult{Roots: []agentkit.RootStatus{}, MCP: []agentkit.MCPStatus{}}
			seen := map[string]bool{}
			for _, o := range scopes {
				got, err := agentkit.List(o)
				if err != nil {
					return classifyAgentErr(err)
				}
				res.Roots = append(res.Roots, got.Roots...)
				for _, m := range got.MCP {
					if !seen[m.Path] {
						seen[m.Path] = true
						res.MCP = append(res.MCP, m)
					}
				}
			}
			if format != "table" {
				if err := writeStructured(cmd.OutOrStdout(), format, marshalRaw(res)); err != nil {
					return err
				}
			} else {
				printAgentList(cmd.OutOrStdout(), res)
			}
			if check && !res.InSync() {
				return newErr(codeRejected, "an agent kit install differs from this tnctl's kit: run tnctl agent install")
			}
			return nil
		},
	}
	f.addScopeFlags(c, false)
	c.Flags().BoolVar(&check, "check", false, "Exit non-zero when an install differs from this tnctl's kit")
	return c
}

func newAgentUninstallCmd(_ *cliEnv) *cobra.Command {
	var f agentFlags
	var mcp, hooks, agentsMD bool
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "uninstall [--agent ...] [--user|--project|--managed DIR] [--mcp] [--hooks]",
		Short:        "Remove the agent kit from this computer",
		Long: `Remove the agent kit from the scope given: exactly the files the kit's manifests record, while
they still match, and the manifests. A file you changed is kept and reported.

--mcp also removes the teknoir MCP server from the agent config files, --hooks the guard hook and its
PreToolUse entry, --agents-md the section from ./AGENTS.md.`,
		Example: `  tnctl agent uninstall
  tnctl agent uninstall --agent claude --mcp --hooks
  tnctl agent uninstall --project --mcp --agents-md`,
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
			res, err := agentkit.Uninstall(o)
			if res != nil && (err == nil || len(res.Actions) > 0) {
				if perr := printAgentResult(cmd.OutOrStdout(), format, res, false); perr != nil {
					return perr
				}
			}
			if err != nil {
				return classifyAgentErr(err)
			}
			return nil
		},
	}
	f.addScopeFlags(c, true)
	c.Flags().BoolVar(&mcp, "mcp", false, "Also remove the teknoir MCP server from the agent config files")
	c.Flags().BoolVar(&hooks, "hooks", false, "Also remove the guard hook and its PreToolUse entry")
	c.Flags().BoolVar(&agentsMD, "agents-md", false, "Also remove the kit's section from ./AGENTS.md")
	return c
}

// classifyAgentErr gives an agentkit error its exit code: a refusal (a file the user changed, or a JSONC
// file with comments) is exit 8, the conflict code: the file needs a person's decision first.
func classifyAgentErr(err error) error {
	if err == nil {
		return nil
	}
	if re, ok := agentkit.Refused(err); ok {
		return newErr(codeConflict, "%v", re)
	}
	return classify(err)
}

// printAgentResult prints one line per action and a summary, or the actions as JSON with -o json.
func printAgentResult(w io.Writer, format string, res *agentkit.Result, dryRun bool) error {
	if format != "table" {
		return writeStructured(w, format, marshalRaw(res.Actions))
	}
	prefix := ""
	if dryRun {
		prefix = "would "
	}
	for _, a := range res.Actions {
		line := fmt.Sprintf("%s%s %s", prefix, a.Action, a.Path)
		if a.Note != "" {
			line += " (" + a.Note + ")"
		}
		if a.Agent != "" {
			line += " [" + a.Agent + "]"
		}
		fmt.Fprintln(w, line)
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warn)
	}
	if len(res.Actions) == 0 {
		fmt.Fprintln(w, "nothing to do")
		return nil
	}
	counts := map[string]int{}
	for _, a := range res.Actions {
		counts[a.Action]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	fmt.Fprintf(w, "summary: %s\n", strings.Join(parts, ", "))
	return nil
}

// printAgentList prints the installs and the MCP registrations of a scope, one line each.
func printAgentList(w io.Writer, res *agentkit.ListResult) {
	if len(res.Roots) == 0 {
		fmt.Fprintln(w, "no agent kit install found: run tnctl agent install")
	}
	for _, r := range res.Roots {
		if r.Problem != "" {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Root, r.Agent, r.Problem)
			continue
		}
		state := "ok"
		if !r.InSync() {
			var why []string
			if r.VersionOutdated {
				why = append(why, "kit "+r.KitVersion+" installed, "+r.EmbeddedVersion+" embedded")
			}
			if n := len(r.Missing); n > 0 {
				why = append(why, fmt.Sprintf("%d missing", n))
			}
			if n := len(r.Changed); n > 0 {
				why = append(why, fmt.Sprintf("%d changed", n))
			}
			if n := len(r.Outdated); n > 0 {
				why = append(why, fmt.Sprintf("%d outdated since the last install", n))
			}
			if n := len(r.Extra); n > 0 {
				why = append(why, fmt.Sprintf("%d no longer in the kit", n))
			}
			state = strings.Join(why, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\tkit %s (%s)\t%s\n", r.Root, r.Agent, r.KitVersion, r.Kind, state)
	}
	for _, m := range res.MCP {
		state := "teknoir not registered"
		if m.Registered {
			state = "teknoir registered"
		} else if m.Note != "" {
			state = "not registered: " + m.Note
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.Path, m.Agent, state)
	}
}
