package cmd

// The tool-group contract: every tool the council's MCP server offers is in the platform's group
// table, and the table names no tool the server has not. The table is a fixture the platform owns
// (council-templates/fixtures/tool-groups.json); this repo keeps a copy, and the test keeps the copy
// true.

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/agentkit"
	"github.com/teknoir/cli/pkg/council"
	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	ctools "github.com/teknoir/cli/pkg/council/tools"
)

// toolGroupFixture is the path of this repo's copy of the platform's group table.
const toolGroupFixture = "../pkg/council/testdata/tool_groups.json"

// councilToolNames is the tools the council's MCP server offers at the creator level, sorted.
func councilToolNames(t *testing.T) []string {
	t.Helper()
	// The council's server, with the council's options: the same read tools the daemon lists. The
	// platform it points at answers nothing: the tools are listed, not called.
	srv, _ := mcpSSE(t, func(http.ResponseWriter) {}, false)
	set, err := ctools.New(context.Background(), newMCPServerOpts(mcpSampleEnv(t, srv), mcpCouncilOptions(councilStore(t))))
	if err != nil {
		t.Fatalf("list the council's tools: %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	names := set.Names(ctools.LevelCreator)
	if len(names) == 0 {
		t.Fatal("the council's server offers no tools at the creator level")
	}
	return names
}

// TestCouncilToolGroupsMatch covers both directions: a tool the server has but the table does not
// cannot be granted to any role, and a name in the table that the server does not have is stale.
func TestCouncilToolGroupsMatch(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(toolGroupFixture))
	if err != nil {
		t.Fatalf("read %s: %v", toolGroupFixture, err)
	}
	var table map[string]string
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatalf("%s is not a JSON object of tool name → group: %v", toolGroupFixture, err)
	}
	if len(table) == 0 {
		t.Fatalf("%s is empty", toolGroupFixture)
	}
	const hint = "update the tool-group table in the Backstage repo (council-templates/fixtures/tool-groups.json) and copy it here"

	names := councilToolNames(t)
	real := make(map[string]bool, len(names))
	for _, name := range names {
		real[name] = true
	}
	var missing, extra []string
	for _, name := range names {
		if _, ok := table[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range table {
		if !real[name] {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("the table has no group for %s, so no role can ever be granted %s: %s",
			strings.Join(missing, ", "), itOrThem(len(missing)), hint)
	}
	if len(extra) > 0 {
		t.Errorf("the table names %s, which the council's server does not offer: %s",
			strings.Join(extra, ", "), hint)
	}
}

// itOrThem is "it" for one tool and "them" for more.
func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// TestCouncilCapsAreValid: the daemon's own caps pass the platform's rules — the tools its MCP
// server offers, the guides of its own book and the kit it installed. If they do not, every bot runs
// proto 1 with one log line and no other sign.
func TestCouncilCapsAreValid(t *testing.T) {
	srv, _ := mcpSSE(t, func(http.ResponseWriter) {}, false)
	set, err := ctools.New(context.Background(), newMCPServerOpts(mcpSampleEnv(t, srv), mcpCouncilOptions(councilStore(t))))
	if err != nil {
		t.Fatalf("list the council's tools: %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	d, err := council.New(council.Config{
		Hub: stubHub{}, Model: stubModel{}, Tools: stubTools{}, Coder: stubCoder{},
		Whoami: func(context.Context) (string, json.RawMessage, error) { return "read", nil, nil },
		Skills: agentkit.FS,
	})
	if err != nil {
		t.Fatalf("council.New: %v", err)
	}
	caps := councilCaps(set, d)
	if len(caps.Tools) == 0 {
		t.Fatal("the caps announce no tools")
	}
	if got := len(caps.Skills); got != 7 {
		t.Errorf("the caps announce %d guides, want the embedded kit's seven", got)
	}
	if err := new(hub.Client).SetCaps(caps); err != nil {
		t.Fatalf("the daemon's own caps are refused, so every bot would run proto 1: %v", err)
	}
}

// The stubs a daemon needs to be built: this test reads its caps, not its turns.
type stubHub struct{}

func (stubHub) Next(context.Context, int) (*hub.WorkTurn, error) { return nil, nil }

func (stubHub) NewPoster(*hub.WorkTurn, time.Time, func(string)) council.Poster { return nil }

type stubModel struct{}

func (stubModel) Stream(context.Context, model.Request, func(string)) (*anthropic.Message, model.Usage, error) {
	return nil, model.Usage{}, nil
}

type stubTools struct{}

func (stubTools) Names(string) []string                           { return nil }
func (stubTools) For(string, []string) []anthropic.ToolUnionParam { return nil }

func (stubTools) Call(context.Context, string, []string, string, json.RawMessage) (ctools.Result, error) {
	return ctools.Result{}, nil
}

type stubCoder struct{}

func (stubCoder) Run(context.Context, coder.Task) coder.Report { return coder.Report{} }
