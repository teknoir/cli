package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// councilArgs runs "tnctl council ..." with the council command under a test root.
func councilArgs(t *testing.T, e *cliEnv, args ...string) (string, string, error) {
	t.Helper()
	return runCLIWith(t, e, []*cobra.Command{newCouncilCmd(e)}, append([]string{"council"}, args...)...)
}

// TestCouncilServeOutsideBotMode refuses the daemon outside a TeknoirBot, with the usage exit code.
func TestCouncilServeOutsideBotMode(t *testing.T) {
	noBotMode(t)
	e := &cliEnv{}
	_, _, err := councilArgs(t, e, "serve")
	if err == nil {
		t.Fatal("council serve ran outside bot mode")
	}
	if !strings.Contains(err.Error(), "runs only inside a TeknoirBot") {
		t.Errorf("error %q", err)
	}
	if code := exitCodeOf(err); code != codeUsage {
		t.Errorf("exit code %d, want %d", code, codeUsage)
	}
}

// TestCouncilServeMaxTurns refuses --max-turns outside 1..2 with the usage exit code.
func TestCouncilServeMaxTurns(t *testing.T) {
	p := newBotAPI(t)
	e := botEnv(t, p)
	for _, v := range []string{"3", "0", "-1"} {
		_, _, err := councilArgs(t, e, "serve", "--max-turns", v)
		if err == nil {
			t.Fatalf("--max-turns %s was accepted", v)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("--max-turns %s: exit code %d, want %d (%v)", v, code, codeUsage, err)
		}
	}
	if p.count() != 0 {
		t.Errorf("a refused command line reached the platform: %d requests", p.count())
	}
}

// TestCouncilCmdHidden keeps the command out of the help of its parent, and gives serve its flags.
func TestCouncilCmdHidden(t *testing.T) {
	root := newCouncilCmd(defaultEnv)
	if !root.Hidden {
		t.Error("council is not hidden")
	}
	var serve *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "serve" {
			serve = c
		}
	}
	if serve == nil {
		t.Fatal("council has no serve subcommand")
	}
	if serve.Flags().Lookup("max-turns") == nil || serve.Flags().Lookup("work-dir") == nil {
		t.Error("serve lacks --max-turns or --work-dir")
	}
}
