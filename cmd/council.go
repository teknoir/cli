package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/council"
	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
)

// councilWorkDir is where the Coder works by default.
const councilWorkDir = "/work"

// newCouncilCmd is the hidden council command of a TeknoirBot.
func newCouncilCmd(e *cliEnv) *cobra.Command {
	c := &cobra.Command{
		Use:   "council",
		Short: "Run the council of a TeknoirBot",
		Long: `Run the council of a TeknoirBot: the Captain, Fleet, the Coder and the Critic.

Inside a TeknoirBot's computer the council takes the chat turns the platform queues for it and
answers them. It runs the bot's own key, on the bot's own computer; it is not a person's command.`,
		Hidden: true,
	}
	c.AddCommand(newCouncilServeCmd(e))
	return c
}

// newCouncilServeCmd is "tnctl council serve": the council daemon.
func newCouncilServeCmd(e *cliEnv) *cobra.Command {
	var maxTurns int
	var workDir string
	cmd := &cobra.Command{
		Use:          "serve",
		Short:        "Take chat turns from the platform and answer them",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if maxTurns < 1 || maxTurns > 2 {
				return usageErr("--max-turns must be 1 or 2")
			}
			on, apiBase, ns, keyFile, err := botMode()
			if err != nil {
				return err
			}
			if !on {
				return usageErr("tnctl council serve runs only inside a TeknoirBot")
			}
			ctx, stop := signalContext(cmd.Context())
			defer stop()
			return serveCouncil(ctx, e, cmd, apiBase, ns, keyFile, maxTurns, workDir)
		},
	}
	cmd.Flags().IntVar(&maxTurns, "max-turns", 2, "how many turns to run at once: 1 or 2")
	cmd.Flags().StringVar(&workDir, "work-dir", councilWorkDir, "the directory the Coder works in")
	return cmd
}

// serveCouncil builds the council's clients and serves until ctx ends.
func serveCouncil(ctx context.Context, e *cliEnv, cmd *cobra.Command, apiBase, ns, keyFile string, maxTurns int, workDir string) error {
	logf := func(format string, args ...any) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "council: "+format+"\n", args...)
	}
	h, err := hub.New(apiBase, keyFile, Version, nil)
	if err != nil {
		return classify(err)
	}
	m, err := model.New(model.Config{APIBase: apiBase, KeyFile: keyFile, Version: Version})
	if err != nil {
		return classify(err)
	}
	// The council cuts results itself, at 16 KiB with a reference for the rest: this server must not
	// cut in front of it, or a whole answer reaches the council as a wrapper cut twice, with counts
	// that disagree. Its captures are its own, and go with the session.
	captures := newCouncilCaptureStore(filepath.Join(workDir, "captures"))
	defer captures.removeAll()
	ts, err := tools.New(ctx, newMCPServerOpts(e, mcpCouncilOptions(captures)))
	if err != nil {
		return err
	}
	defer func() { _ = ts.Close() }()
	runner := coder.New(coder.Config{
		WorkRoot: workDir,
		Home:     workDir + "/home",
		Logf:     logf,
	})
	whoami := func(ctx context.Context) (string, json.RawMessage, error) {
		who, raw, err := botFleet(apiBase, ns, keyFile).BotWhoami(ctx)
		if err != nil {
			return "", nil, err
		}
		return who.Bot.Access, raw, nil
	}
	d, err := council.New(council.Config{
		Hub:      council.HubOf(h),
		Model:    m,
		Tools:    ts,
		Coder:    runner,
		Whoami:   whoami,
		MaxTurns: maxTurns,
		Logf:     logf,
	})
	if err != nil {
		return err
	}
	if err := d.Serve(ctx); err != nil {
		return classify(err)
	}
	return nil
}

func init() { rootCmd.AddCommand(newCouncilCmd(defaultEnv)) }

// newCouncilCaptureStore is the council daemon's capture store over dir. Only this daemon uses the
// dir, so whatever an earlier process left there can never be read again: it is swept whatever its
// age.
func newCouncilCaptureStore(dir string) *mcpCaptureStore {
	s := newCaptureStore(dir)
	s.sweepAll()
	return s
}

// mcpCouncilOptions are the options of the council's MCP server. The council cuts results itself,
// at 16 KiB with a reference for the rest, so the server does not cut in front of it (MaxResult),
// and its mqtt_read pages, spilled indexes and inline mqtt_get_sample answers are sized to fit
// under that cut (PageEscapedMax and IndexMax, 14 KiB each).
func mcpCouncilOptions(captures *mcpCaptureStore) mcpServerOptions {
	return mcpServerOptions{MaxResult: mcpCouncilMaxResult, Captures: captures, PageEscapedMax: 14 << 10, IndexMax: 14 << 10}
}
