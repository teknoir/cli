package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/teknoir/cli/pkg/council"
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

// TestCouncilCmdVisible is a person's command group, with the bot's serve daemon hidden below it.
func TestCouncilCmdVisible(t *testing.T) {
	noBotMode(t)
	root := newCouncilCmd(defaultEnv)
	if root.Hidden {
		t.Error("council is hidden")
	}
	if root.RunE == nil {
		t.Error("council is not runnable")
	}
	var serve *cobra.Command
	subs := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		subs[c.Name()] = c
		if c.Name() == "serve" {
			serve = c
		}
	}
	if serve == nil {
		t.Fatal("council has no serve subcommand")
	}
	if !serve.Hidden {
		t.Error("serve is not hidden")
	}
	if serve.Flags().Lookup("max-turns") == nil || serve.Flags().Lookup("work-dir") == nil {
		t.Error("serve lacks --max-turns or --work-dir")
	}
	// The kit dir is a flag of the image's own, not of a person's: it is hidden, and defaults to
	// where the image installs the guide set.
	kit := serve.Flags().Lookup("kit-dir")
	if kit == nil {
		t.Fatal("serve lacks --kit-dir")
	}
	if kit.Hidden != true {
		t.Error("--kit-dir is not hidden")
	}
	if kit.DefValue != council.DefaultKitDir {
		t.Errorf("--kit-dir defaults to %q, want %q", kit.DefValue, council.DefaultKitDir)
	}
	role := subs["role"]
	if role == nil {
		t.Fatal("council has no role subcommand")
	}
	got := map[string]bool{}
	for _, c := range role.Commands() {
		got[c.Name()] = true
	}
	for _, want := range []string{"list", "get", "export", "new", "archive", "apply"} {
		if !got[want] {
			t.Errorf("council role has no %s subcommand", want)
		}
	}
	if subs["apply"] == nil {
		t.Error("council has no apply subcommand")
	}

	out, _, err := councilArgs(t, &cliEnv{})
	if err != nil {
		t.Fatalf("council: %v", err)
	}
	if !strings.Contains(out, "tnctl council role") {
		t.Errorf("council's help does not name its role commands: %q", out)
	}
	if want := "Read and change a TeknoirBot's council: its members and councils"; root.Short != want {
		t.Errorf("council Short %q, want %q", root.Short, want)
	}
	wantLong := "A council is the set of members that answers a chat with a TeknoirBot: a lead (the Captain)\n" +
		"plans each turn, members each do one job, and a reviewer (the Critic, or Skeptic) checks the\n" +
		"members' reports when the turn needs it, not on every turn.\n" +
		"\n" +
		"  tnctl council role list|get|export   the members a bot's councils can use\n" +
		"  tnctl council role new|archive       make a member from its job in words, or archive one\n" +
		"  tnctl council apply -f <file>        apply members and councils from YAML (an export, edited)\n" +
		"\n" +
		"These are a person's commands: a TeknoirBot cannot run them (it runs only tnctl council serve,\n" +
		"inside its computer)."
	if root.Long != wantLong {
		t.Errorf("council Long:\n%q\nwant:\n%q", root.Long, wantLong)
	}
	if want := "List, read, make and archive the members a bot's councils can use"; role.Short != want {
		t.Errorf("council role Short %q, want %q", role.Short, want)
	}
	// A mistyped subcommand is a usage error with cobra's suggestions; the hidden serve is never one.
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"aply"}, `unknown command "aply" for "tnctl council": did you mean apply?`},
		{[]string{"rol"}, `unknown command "rol" for "tnctl council": did you mean role?`},
		{[]string{"rle"}, `unknown command "rle" for "tnctl council": did you mean role?`},
		{[]string{"serv"}, `unknown command "serv" for "tnctl council"`},
		{[]string{"role", "lsit"}, `unknown command "lsit" for "tnctl council role": did you mean list?`},
		{[]string{"role", "lst"}, `unknown command "lst" for "tnctl council role": did you mean list or get?`},
		{[]string{"role", "exprt"}, `unknown command "exprt" for "tnctl council role": did you mean export?`},
		{[]string{"role", "archve"}, `unknown command "archve" for "tnctl council role": did you mean archive?`},
	} {
		_, _, err = councilArgs(t, &cliEnv{}, c.args...)
		if err == nil || exitCodeOf(err) != codeUsage || err.Error() != c.want {
			t.Errorf("council %v: exit %d, %v; want exit 2 %q", c.args, exitCodeOf(err), err, c.want)
		}
	}
}
