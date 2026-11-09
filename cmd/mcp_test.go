package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/config"
)

// mcpToolNames are the tools of the server. Names are part of the contract.
var mcpToolNames = []string{
	"whoami",
	"list_teamspaces",
	"list_devices",
	"get_device",
	"list_robots",
	"get_robot",
	"list_robot_types",
	"list_fleets",
	"get_overview",
	"get_problems",
	"get_config",
	"list_kinds",
	"prom_query",
	"mqtt_sample",
	"inspect_pod",
	"inspect_logs",
	"inspect_release",
}

// mcpClient connects a client to s over an in-memory pipe (the server connects first, or the pipe
// hangs).
func mcpClient(t *testing.T, s *mcp.Server) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := s.Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// mcpCall calls one tool and fails on a protocol error.
func mcpCall(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

// mcpText is the one content block of a result.
func mcpText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("%d content blocks, want 1: %+v", len(res.Content), res.Content)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content %T", res.Content[0])
	}
	return tc.Text
}

// mcpIndented is body as the server prints it: indented JSON.
func mcpIndented(t *testing.T, body string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(body), "", "  "); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func mcpCloneArgs(args map[string]any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	return out
}

// ---------- the calls a tool makes ----------

// mcpWant is one request a call sends.
type mcpWant struct {
	method, path, query string
	// full: path is the fake's own path (a route of the fleet root, where the teamspace is in the
	// path); otherwise it is relative to the teamspace base.
	full bool
}

// mcpCase is one tool call of the tests that send a request: its arguments, the fake's answer and
// the requests it makes.
type mcpCase struct {
	tool   string
	args   map[string]any
	answer string
	// reqAnswers answer the requests one by one; without them every request gets answer. The
	// result holds the last answer.
	reqAnswers []string
	reqs       []mcpWant
}

// answerOf is the answer of request i.
func (c mcpCase) answerOf(i int) string {
	if len(c.reqAnswers) == len(c.reqs) {
		return c.reqAnswers[i]
	}
	return c.answer
}

// lastAnswer is the answer the result holds.
func (c mcpCase) lastAnswer() string {
	if n := len(c.reqAnswers); n > 0 && n == len(c.reqs) {
		return c.reqAnswers[n-1]
	}
	return c.answer
}

// mcpCases are the 15 tools that send a request (whoami sends none as a user).
func mcpCases() []mcpCase {
	return []mcpCase{
		{tool: "list_robots", answer: `[{"name":"r1"}]`,
			reqs: []mcpWant{{method: "GET", path: "/robots"}}},
		{tool: "get_robot", args: map[string]any{"name": "r1"}, answer: `{"name":"r1"}`,
			reqs: []mcpWant{{method: "GET", path: "/robots/r1"}}},
		{tool: "list_robot_types", answer: `[{"name":"vision"}]`,
			reqs: []mcpWant{{method: "GET", path: "/types"}}},
		{tool: "list_fleets", answer: `[]`,
			reqs: []mcpWant{{method: "GET", path: "/fleets"}}},
		{tool: "get_overview", answer: `{"devices":1}`,
			reqs: []mcpWant{{method: "GET", path: "/overview"}}},
		{tool: "get_problems", answer: `[]`,
			reqs: []mcpWant{{method: "GET", path: "/problems"}}},
		{tool: "get_problems", args: map[string]any{"robot": "r1"}, answer: `[]`,
			reqs: []mcpWant{{method: "GET", path: "/problems", query: "robot=r1"}}},
		{tool: "get_problems", args: map[string]any{"device": "d1"}, answer: `[]`,
			reqs: []mcpWant{{method: "GET", path: "/problems", query: "device=d1"}}},
		{tool: "get_problems", args: map[string]any{"robot": "r1", "device": "d1"}, answer: `[]`,
			reqs: []mcpWant{{method: "GET", path: "/problems", query: "device=d1&robot=r1"}}},
		{tool: "get_config", args: map[string]any{"target": "vs"}, answer: `{"target":{}}`,
			reqs: []mcpWant{{method: "GET", path: "/config/vs"}}},
		{tool: "get_config", args: map[string]any{"target": "vs", "resource": "HelmChart/default/notebook", "defaults": true},
			answer: `{"id":"HelmChart/default/notebook"}`,
			reqs:   []mcpWant{{method: "GET", path: "/config/vs/resource", query: "defaults=1&id=HelmChart%2Fdefault%2Fnotebook"}}},
		{tool: "get_config", args: map[string]any{"target": "vs", "resource": "notebook"},
			reqAnswers: []string{
				`{"configuration":{"effective":[{"kind":"HelmChart","name":"notebook","namespace":"default"}]}}`,
				`{"id":"HelmChart/default/notebook"}`,
			},
			reqs: []mcpWant{{method: "GET", path: "/config/vs"},
				{method: "GET", path: "/config/vs/resource", query: "id=HelmChart%2Fdefault%2Fnotebook"}}},
		{tool: "list_kinds", answer: `{"kinds":[],"refused":[]}`,
			reqs: []mcpWant{{method: "GET", path: "/local-kinds"}}},
		{tool: "prom_query", args: map[string]any{"query": "up"}, answer: `{"status":"success"}`,
			reqs: []mcpWant{{method: "GET", path: "/prometheus/query", query: "query=up"}}},
		{tool: "prom_query", args: map[string]any{"query": "up", "time": "1762682400"}, answer: `{"status":"success"}`,
			reqs: []mcpWant{{method: "GET", path: "/prometheus/query", query: "query=up&time=1762682400"}}},
		{tool: "list_devices", answer: `[{"name":"d1"}]`,
			reqs: []mcpWant{{method: "GET", path: "/devices"}}},
		{tool: "list_devices", args: map[string]any{"labels": map[string]any{"a": "b", "c": "d"}}, answer: `[{"name":"d1"}]`,
			reqs: []mcpWant{{method: "GET", path: "/devices", query: "label=a%3Db&label=c%3Dd"}}},
		{tool: "get_device", args: map[string]any{"name": "d1"}, answer: `{"name":"d1"}`,
			reqs: []mcpWant{{method: "GET", path: "/devices/d1"}}},
		{tool: "inspect_pod", args: map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "whoami-1"},
			answer: `{"pod":{"name":"whoami-1"}}`,
			reqs:   []mcpWant{{method: "GET", path: "/robots/vs/inspect/pods/default/whoami-1", query: "enable=false"}}},
		{tool: "inspect_logs", args: map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "whoami-1"},
			answer: `{"text":"hi"}`,
			reqs:   []mcpWant{{method: "GET", path: "/robots/vs/inspect/pods/default/whoami-1/logs", query: "enable=false&tail=200"}}},
		{tool: "inspect_logs", args: map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "whoami-1",
			"container": "c", "previous": true, "tail": 50, "since": "10m"},
			answer: `{"text":"hi"}`,
			reqs: []mcpWant{{method: "GET", path: "/robots/vs/inspect/pods/default/whoami-1/logs",
				query: "container=c&enable=false&previous=true&since=10m&tail=50"}}},
		{tool: "inspect_release", args: map[string]any{"robot": "vs", "release_namespace": "default", "release": "notebook"},
			answer: `{"name":"notebook"}`,
			reqs:   []mcpWant{{method: "GET", path: "/robots/vs/inspect/releases/default/notebook", query: "enable=false"}}},
		{tool: "list_teamspaces", answer: `[{"name":"teknoir-demo","access":"write"}]`,
			reqs: []mcpWant{{method: "GET", path: "/api/device/fleet/teamspaces", full: true}}},
	}
}

// mcpAcmePath is one of the teamspace base's paths as the fake sees it for another teamspace: the
// fake only strips the configured teamspace's prefix.
func mcpAcmePath(path string) string { return "/api/device/fleet/acme" + path }

// mcpRegister answers every route c needs: the configured teamspace's, and the other teamspace's.
func mcpRegister(p *fakePlatform, c mcpCase) {
	for i, r := range c.reqs {
		p.on(r.method, r.path, 200, c.answerOf(i))
		if !r.full {
			p.on(r.method, mcpAcmePath(r.path), 200, c.answerOf(i))
		}
	}
}

// mcpHarness is a fake platform and a connected client of a server over it.
func mcpHarness(t *testing.T, c mcpCase) (*fakePlatform, *mcp.ClientSession) {
	t.Helper()
	noBotMode(t)
	p := newFakePlatform(t)
	mcpRegister(p, c)
	e, _ := testEnv(t, p, nil)
	return p, mcpClient(t, newMCPServer(e))
}

// ---------- (a) the tool list ----------

func TestMCPToolListIsReadOnlyAndComplete(t *testing.T) {
	noBotMode(t)
	cs := mcpClient(t, newMCPServer(&cliEnv{}))

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := append([]string{}, mcpToolNames...)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}

	// namespace is optional everywhere: empty means the configured teamspace.
	required := map[string][]string{
		"whoami": {}, "list_teamspaces": {},
		"list_devices": {}, "get_device": {"name"},
		"list_robots": {}, "get_robot": {"name"},
		"list_robot_types": {}, "list_fleets": {},
		"get_overview": {}, "get_problems": {},
		"get_config": {"target"}, "list_kinds": {},
		"prom_query":      {"query"},
		"mqtt_sample":     {"topics"},
		"inspect_pod":     {"pod", "pod_namespace", "robot"},
		"inspect_logs":    {"pod", "pod_namespace", "robot"},
		"inspect_release": {"release", "release_namespace", "robot"},
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil {
			t.Errorf("%s: no annotations", tool.Name)
			continue
		}
		a := tool.Annotations
		if !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
			t.Errorf("%s: annotations %+v", tool.Name, a)
		}
		if tool.OutputSchema != nil {
			t.Errorf("%s: an output schema would send the result again as structured content", tool.Name)
		}
		for _, bad := range []string{"credential", "ssh", "exec", "login", "bootstrap", "delete", "set", "put", "post"} {
			if strings.Contains(tool.Name, bad) {
				t.Errorf("tool %q names %q: the server is read-only", tool.Name, bad)
			}
		}
		schema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("%s: schema %T", tool.Name, tool.InputSchema)
		}
		var got []string
		if reqs, ok := schema["required"].([]any); ok {
			for _, r := range reqs {
				got = append(got, fmt.Sprint(r))
			}
		}
		sort.Strings(got)
		if want := required[tool.Name]; len(got) != len(want) || (len(got) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("%s: required %v, want %v", tool.Name, got, required[tool.Name])
		}
	}

	for _, name := range []string{"inspect_logs", "inspect_pod", "inspect_release", "get_problems", "prom_query", "mqtt_sample"} {
		for _, tool := range tools.Tools {
			if tool.Name == name && !strings.HasSuffix(tool.Description, mcpUntrustedNote) {
				t.Errorf("%s: description does not end with the untrusted note: %q", name, tool.Description)
			}
		}
	}
	// The description is what an agent reads: every stop of the sample, the payload cut and the one
	// sample at a time must be in it.
	for _, tool := range tools.Tools {
		if tool.Name != "mqtt_sample" {
			continue
		}
		for _, want := range []string{"when the sample is full", "stoppedBy", "payloadTruncated", "One sample runs at a time"} {
			if !strings.Contains(tool.Description, want) {
				t.Errorf("the mqtt_sample description lacks %q: %q", want, tool.Description)
			}
		}
	}
	for _, tool := range tools.Tools {
		if strings.HasPrefix(tool.Name, "inspect_") && !strings.HasSuffix(tool.Description, mcpInspectNote+" "+mcpUntrustedNote) {
			t.Errorf("%s: the description lacks the Shell and access note: %q", tool.Name, tool.Description)
		}
	}
	if got := cs.InitializeResult().Instructions; got != mcpInstructions {
		t.Errorf("instructions %q", got)
	}
}

// The bot half of the note names the creator's own terminal access: the platform checks pods/exec on
// the creator, not only that it may change things.
func TestMCPInspectNoteNamesTheCreatorsTerminalAccess(t *testing.T) {
	bot := mcpInspectNote[strings.Index(mcpInspectNote, "TeknoirBot"):]
	if !strings.Contains(bot, "Act as me") || !strings.Contains(bot, "who has both") {
		t.Errorf("the bot half %q does not say the creator needs pods/exec", bot)
	}
}

// ---------- (b) no login needed to start ----------

func TestMCPServerStartsWithoutAFleet(t *testing.T) {
	noBotMode(t)
	var fleetCalls, domainCalls int
	e := &cliEnv{
		fleet: func() (*api.Fleet, string, string, error) {
			fleetCalls++
			return nil, "", "", errors.New("no fleet")
		},
		domainFleet: func() (*api.Fleet, string, error) {
			domainCalls++
			return nil, "", errors.New("no fleet")
		},
	}
	cs := mcpClient(t, newMCPServer(e))
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 17 {
		t.Fatalf("%d tools", len(tools.Tools))
	}
	if fleetCalls != 0 || domainCalls != 0 {
		t.Errorf("listing the tools resolved a fleet: %d %d", fleetCalls, domainCalls)
	}
	for _, name := range []string{"list_robots", "list_teamspaces"} {
		res := mcpCall(t, cs, name, nil)
		if !res.IsError || !strings.Contains(mcpText(t, res), "no fleet") {
			t.Errorf("%s: %+v %q", name, res, mcpText(t, res))
		}
	}
	if fleetCalls != 1 || domainCalls != 1 {
		t.Errorf("each call resolves its own client: %d %d", fleetCalls, domainCalls)
	}
}

// ---------- (c) what each tool calls ----------

func TestMCPToolsCallThePlatform(t *testing.T) {
	for _, c := range mcpCases() {
		t.Run(c.tool, func(t *testing.T) {
			p, cs := mcpHarness(t, c)
			res := mcpCall(t, cs, c.tool, c.args)
			if res.IsError {
				t.Fatalf("error result: %s", mcpText(t, res))
			}
			if got, want := mcpText(t, res), mcpIndented(t, c.lastAnswer()); got != want {
				t.Errorf("text %q, want %q", got, want)
			}
			if res.StructuredContent != nil {
				t.Errorf("structured content %s: the result would be sent twice, the second time uncut", res.StructuredContent)
			}
			for _, r := range c.reqs {
				seen := p.seen(r.method, r.path)
				if len(seen) == 0 {
					t.Fatalf("no %s %s: %+v", r.method, r.path, p.seen("GET", r.path))
				}
				if q := seen[len(seen)-1].Query; q != r.query {
					t.Errorf("%s %s query %q, want %q", r.method, r.path, q, r.query)
				}
			}
			if got, want := p.countAll(), len(c.reqs); got != want {
				t.Errorf("%d requests, want %d; the call sent arguments it was not given", got, want)
			}
		})
	}
}

// ---------- (d) another teamspace ----------

func TestMCPOtherTeamspace(t *testing.T) {
	for _, c := range mcpCases() {
		if c.tool == "list_teamspaces" {
			continue
		}
		t.Run(c.tool, func(t *testing.T) {
			for _, ns := range []string{"", "teknoir-demo", "acme"} {
				p, cs := mcpHarness(t, c)
				args := mcpCloneArgs(c.args)
				if ns != "" {
					args["namespace"] = ns
				}
				res := mcpCall(t, cs, c.tool, args)
				if res.IsError {
					t.Fatalf("namespace %q: %s", ns, mcpText(t, res))
				}
				for _, r := range c.reqs {
					path := r.path
					if !r.full && ns == "acme" {
						path = mcpAcmePath(r.path)
					}
					if len(p.seen(r.method, path)) == 0 {
						t.Fatalf("namespace %q: no %s %s", ns, r.method, path)
					}
				}
			}
			for _, ns := range []string{"../x", "A", strings.Repeat("a", 64)} {
				p, cs := mcpHarness(t, c)
				args := mcpCloneArgs(c.args)
				args["namespace"] = ns
				res := mcpCall(t, cs, c.tool, args)
				if want := fmt.Sprintf("namespace %q is not a teamspace name", ns); !res.IsError || mcpText(t, res) != want {
					t.Errorf("namespace %q: %+v %q, want %q", ns, res, mcpText(t, res), want)
				}
				if n := p.countAll(); n != 0 {
					t.Errorf("namespace %q sent %d request(s)", ns, n)
				}
			}
		})
	}
}

// ---------- (e) bot mode ----------

func TestMCPBotModeStaysInItsTeamspace(t *testing.T) {
	for _, c := range mcpCases() {
		if c.tool == "list_teamspaces" {
			continue
		}
		t.Run(c.tool, func(t *testing.T) {
			p := newBotAPI(t)
			for i, r := range c.reqs {
				p.on(r.method, "/fleet/teknoir-demo"+r.path, 200, c.answerOf(i))
			}
			cs := mcpClient(t, newMCPServer(botEnv(t, p)))

			args := mcpCloneArgs(c.args)
			args["namespace"] = "other"
			res := mcpCall(t, cs, c.tool, args)
			if want := "a TeknoirBot works only in its own teamspace (teknoir-demo)"; !res.IsError || mcpText(t, res) != want {
				t.Errorf("%+v %q, want %q", res, mcpText(t, res), want)
			}
			if n := p.count(); n != 0 {
				t.Fatalf("%d requests for another teamspace", n)
			}

			args["namespace"] = "teknoir-demo"
			if res := mcpCall(t, cs, c.tool, args); res.IsError {
				t.Errorf("own teamspace: %s", mcpText(t, res))
			}
			if n := p.count(); n != len(c.reqs) {
				t.Errorf("%d requests, want %d", n, len(c.reqs))
			}
		})
	}
}

func TestMCPBotRefusalIsAnOrdinaryErrorResult(t *testing.T) {
	const msg = "This TeknoirBot may only read (Read only). Its creator can switch it to Act as me on the Bots page."
	p := newBotAPI(t)
	p.on("GET", "/fleet/teknoir-demo/robots/vs/inspect/pods/default/whoami-1", 403,
		`{"error":{"name":"NotAllowedError","message":"`+msg+`"}}`)
	cs := mcpClient(t, newMCPServer(botEnv(t, p)))
	res := mcpCall(t, cs, "inspect_pod", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "whoami-1"})
	if !res.IsError || !strings.Contains(mcpText(t, res), msg) {
		t.Errorf("%+v %q", res, mcpText(t, res))
	}
}

// ---------- (f) whoami ----------

func TestMCPWhoami(t *testing.T) {
	t.Run("user", func(t *testing.T) {
		noBotMode(t)
		useLogin(t, map[string]any{"email": "a@b.c"}, "teknoir-demo")
		initConfig()
		p := newFakePlatform(t)
		e, _ := testEnv(t, p, nil)
		cs := mcpClient(t, newMCPServer(e))

		res := mcpCall(t, cs, "whoami", nil)
		if res.IsError {
			t.Fatal(mcpText(t, res))
		}
		if res.StructuredContent != nil {
			t.Errorf("structured content %s: the result would be sent twice", res.StructuredContent)
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(mcpText(t, res)), &got); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"mode": "user", "email": "a@b.c", "domain": "teknoir.cloud", "namespace": "teknoir-demo"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
		if n := p.countAll(); n != 0 {
			t.Errorf("whoami as a user sent %d request(s)", n)
		}
	})

	t.Run("the teamspace tnctl targets", func(t *testing.T) {
		// whoami names the teamspace the other tools use (config.GetNamespace, as defaultEnv.fleet).
		noBotMode(t)
		useLogin(t, map[string]any{"email": "a@b.c"}, "teknoir-demo")
		t.Setenv("TNCTL_NAMESPACE", "acme")
		initConfig()
		p := newFakePlatform(t)
		e, _ := testEnv(t, p, nil)
		cs := mcpClient(t, newMCPServer(e))
		var got map[string]any
		if err := json.Unmarshal([]byte(mcpText(t, mcpCall(t, cs, "whoami", nil))), &got); err != nil {
			t.Fatal(err)
		}
		if got["namespace"] != "acme" {
			t.Errorf("namespace %v, want acme (TNCTL_NAMESPACE)", got["namespace"])
		}
	})

	t.Run("not logged in", func(t *testing.T) {
		noBotMode(t)
		useConfigFile(t, "domain: teknoir.cloud\n")
		initConfig()
		p := newFakePlatform(t)
		e, _ := testEnv(t, p, nil)
		cs := mcpClient(t, newMCPServer(e))
		res := mcpCall(t, cs, "whoami", nil)
		if !res.IsError || !strings.Contains(mcpText(t, res), "not logged in") {
			t.Errorf("%+v %q", res, mcpText(t, res))
		}
	})

	t.Run("bot", func(t *testing.T) {
		p := newBotAPI(t)
		p.on("GET", "/whoami", 200, botWhoamiBody)
		cs := mcpClient(t, newMCPServer(botEnv(t, p)))
		res := mcpCall(t, cs, "whoami", nil)
		if res.IsError {
			t.Fatal(mcpText(t, res))
		}
		if res.StructuredContent != nil {
			t.Errorf("structured content %s: the result would be sent twice", res.StructuredContent)
		}
		if got, want := mcpText(t, res), mcpIndented(t, botWhoamiBody); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		for _, want := range []string{`"name": "scout"`, `"access": "read"`, `"email": "a@b.c"`} {
			if !strings.Contains(mcpText(t, res), want) {
				t.Errorf("the bot's whoami lacks %s", want)
			}
		}
	})
}

// A tnctl mcp that runs while another tnctl logs in, or runs `tnctl ns`, must see that on its next
// call: the login and the teamspace live in a file this process re-reads.
func TestMCPSeesALoginMadeWhileItRuns(t *testing.T) {
	noBotMode(t)
	useConfigFile(t, "domain: teknoir.cloud\n")
	initConfig()
	cs := mcpClient(t, newMCPServer(defaultEnv))
	res := mcpCall(t, cs, "whoami", nil)
	if !res.IsError || !strings.Contains(mcpText(t, res), "not logged in") {
		t.Fatalf("before the login: %v %q", res.IsError, mcpText(t, res))
	}
	write := func(ns string) { // what tnctl login and tnctl ns leave in the file
		auths := map[string]any{"teknoir_cloud": map[string]any{
			"access_token": testJWT(t, map[string]any{"email": "a@b.c"}), "namespace": ns}}
		b, _ := json.Marshal(map[string]any{"domain": "teknoir.cloud", "auths": auths})
		if err := os.WriteFile(cfgFile, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("teknoir-demo")
	res = mcpCall(t, cs, "whoami", nil)
	if res.IsError {
		t.Fatalf("after the login: %q", mcpText(t, res))
	}
	var v whoamiUserJSON
	if err := json.Unmarshal([]byte(mcpText(t, res)), &v); err != nil {
		t.Fatal(err)
	}
	if v.Namespace != "teknoir-demo" || v.Email != "a@b.c" {
		t.Errorf("after the login: %+v", v)
	}
	write("acme")                       // tnctl ns acme
	fl, err := mcpFleet(defaultEnv, "") // the client a teamspace tool would use; it sends nothing
	if err != nil || !strings.HasSuffix(fl.Base, "/api/device/fleet/acme") {
		t.Errorf("after tnctl ns acme the teamspace tools use %v (%v)", fl, err)
	}
	if res := mcpCall(t, cs, "whoami", nil); !strings.Contains(mcpText(t, res), `"namespace": "acme"`) {
		t.Errorf("after tnctl ns acme: %q", mcpText(t, res))
	}
}

// list_teamspaces reads the login again on every call, as the teamspace tools do: a tnctl login to
// another domain made while the server runs counts on the next call.
func TestMCPListTeamspacesSeesADomainChangedWhileItRuns(t *testing.T) {
	noBotMode(t)
	useLogin(t, map[string]any{"email": "a@b.c"}, "teknoir-demo")
	initConfig()
	p := newFakePlatform(t)
	p.on("GET", "/api/device/fleet/teamspaces", 200, `[]`)
	e, _ := testEnv(t, p, nil)
	fake := e.domainFleet
	e.domainFleet = func() (*api.Fleet, string, error) { // the domain defaultEnv.domainFleet would use
		cfg, err := config.Load()
		if err != nil {
			return nil, "", err
		}
		if cfg.Domain != "other.example" {
			return nil, "", fmt.Errorf("domain %q, want other.example", cfg.Domain)
		}
		return fake()
	}
	cs := mcpClient(t, newMCPServer(e))
	b, _ := json.Marshal(map[string]any{"domain": "other.example", "auths": map[string]any{
		"other_example": map[string]any{"access_token": testJWT(t, map[string]any{"email": "a@b.c"}), "namespace": "x"}}})
	if err := os.WriteFile(cfgFile, b, 0o600); err != nil { // tnctl login --domain other.example
		t.Fatal(err)
	}
	if res := mcpCall(t, cs, "list_teamspaces", nil); res.IsError {
		t.Errorf("after the login to other.example: %s", mcpText(t, res))
	}
}

// ---------- (g) input refusals ----------

func TestMCPRefusesBadInput(t *testing.T) {
	many := map[string]any{}
	for i := 0; i < 21; i++ {
		many[fmt.Sprintf("k%d", i)] = "v"
	}
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"get_device", map[string]any{"name": ".."}, `".." is not a valid name`},
		{"get_robot", map[string]any{"name": ".."}, `".." is not a valid name`},
		{"get_config", map[string]any{"target": ".."}, `".." is not a valid target`},
		{"get_config", map[string]any{"target": "vs", "defaults": true}, "defaults needs resource"},
		{"get_problems", map[string]any{"robot": ".."}, `".." is not a valid robot`},
		{"get_problems", map[string]any{"device": ".."}, `".." is not a valid device`},
		{"inspect_pod", map[string]any{"robot": "..", "pod_namespace": "default", "pod": "p"}, `".." is not a valid robot`},
		{"inspect_pod", map[string]any{"robot": "vs", "pod_namespace": "..", "pod": "p"}, `".." is not a valid pod_namespace`},
		{"inspect_pod", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "a/b"}, `"a/b" is not a valid pod`},
		{"inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p", "container": "a b"}, `"a b" is not a valid container`},
		{"inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p", "tail": 2001}, "tail must be from 1 to 2000"},
		{"inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p", "tail": -1}, "tail must be from 1 to 2000"},
		{"inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p", "since": "ten minutes"}, "since must be like 30s, 10m or 2h, at most 168h"},
		{"inspect_logs", map[string]any{"robot": "..", "pod_namespace": "default", "pod": "p"}, `".." is not a valid robot`},
		{"inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "..", "pod": "p"}, `".." is not a valid pod_namespace`},
		{"inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "a/b"}, `"a/b" is not a valid pod`},
		{"inspect_release", map[string]any{"robot": "..", "release_namespace": "default", "release": "x"}, `".." is not a valid robot`},
		{"inspect_release", map[string]any{"robot": "vs", "release_namespace": "..", "release": "x"}, `".." is not a valid release_namespace`},
		{"inspect_release", map[string]any{"robot": "vs", "release_namespace": "default", "release": ".."}, `".." is not a valid release`},
		{"prom_query", map[string]any{"query": "  "}, "query must be 1 to 4096 bytes"},
		{"prom_query", map[string]any{"query": strings.Repeat("a", 4097)}, "query must be 1 to 4096 bytes"},
		{"list_devices", map[string]any{"labels": many}, "at most 20 label filters are allowed, got 21"},
	}
	noBotMode(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))
	for _, c := range cases {
		res := mcpCall(t, cs, c.tool, c.args)
		if !res.IsError {
			t.Errorf("%s %v was accepted: %q", c.tool, c.args, mcpText(t, res))
			continue
		}
		if got := mcpText(t, res); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.tool, c.args, got, c.want)
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("a refused input sent %d request(s)", n)
	}
}

// A value at its limit is accepted: the refusals are for what is beyond it.
func TestMCPAcceptsTheLimits(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	ns63 := strings.Repeat("a", 63)
	p.on("GET", "/prometheus/query", 200, `{}`)
	p.on("GET", "/robots/vs/inspect/pods/default/p/logs", 200, `{}`)
	p.on("GET", "/api/device/fleet/"+ns63+"/robots", 200, `[]`)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))
	for _, c := range []struct {
		tool, path string
		args       map[string]any
	}{
		{"prom_query", "/prometheus/query", map[string]any{"query": strings.Repeat("a", 4096)}},
		{"inspect_logs", "/robots/vs/inspect/pods/default/p/logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p", "tail": 2000}},
		{"list_robots", "/api/device/fleet/" + ns63 + "/robots", map[string]any{"namespace": ns63}},
	} {
		if res := mcpCall(t, cs, c.tool, c.args); res.IsError {
			t.Errorf("%s at its limit was refused: %s", c.tool, mcpText(t, res))
		}
		if len(p.seen("GET", c.path)) != 1 {
			t.Errorf("%s: no GET %s", c.tool, c.path)
		}
	}
	if r := p.seen("GET", "/robots/vs/inspect/pods/default/p/logs"); len(r) == 1 && r[0].Query != "enable=false&tail=2000" {
		t.Errorf("inspect_logs query %q, want enable=false&tail=2000", r[0].Query)
	}
}

// ---------- (h) the schema refuses before the handler ----------

func TestMCPRefusesArgumentsTheSchemaDoesNotAllow(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"get_robot", map[string]any{}},
		{"list_robots", map[string]any{"bogus": 1}},
	} {
		res := mcpCall(t, cs, c.tool, c.args)
		if !res.IsError {
			t.Errorf("%s %v was accepted: %q", c.tool, c.args, mcpText(t, res))
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("%d request(s)", n)
	}
}

// ---------- (i) results and truncation ----------

func TestMCPResultIsOneIndentedBlock(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/devices/d1", 200, `{"name":"d1","labels":{"a":"b"}}`)
	p.on("GET", "/robots", 200, `[{"name":"r1","problems":[]}]`)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))

	for _, c := range []struct{ tool, args, body string }{
		{"get_device", `{"name":"d1"}`, `{"name":"d1","labels":{"a":"b"}}`},
		{"list_robots", `{}`, `[{"name":"r1","problems":[]}]`},
	} {
		args := map[string]any{}
		if err := json.Unmarshal([]byte(c.args), &args); err != nil {
			t.Fatal(err)
		}
		res := mcpCall(t, cs, c.tool, args)
		if res.IsError {
			t.Fatalf("%s: %s", c.tool, mcpText(t, res))
		}
		if res.StructuredContent != nil {
			t.Errorf("%s: structured content %+v", c.tool, res.StructuredContent)
		}
		if got, want := mcpText(t, res), mcpIndented(t, c.body); got != want {
			t.Errorf("%s: %q, want %q", c.tool, got, want)
		}
	}
}

func TestMCPTruncatesALongResultAtANewline(t *testing.T) {
	noBotMode(t)
	var items []string
	size := 0
	for i := 0; size < 1<<20; i++ {
		line := fmt.Sprintf(`{"name":"robot-%06d"}`, i)
		items = append(items, line)
		size += len(line) + 1
	}
	body := "[" + strings.Join(items, ",") + "]"
	p := newFakePlatform(t)
	p.on("GET", "/robots", 200, body)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))

	res := mcpCall(t, cs, "list_robots", nil)
	text := mcpText(t, res)
	if res.StructuredContent != nil {
		t.Error("structured content: the uncut answer is sent again")
	}
	full := mcpIndented(t, body)
	const limit = 48 << 10
	kept := strings.LastIndex(full[:limit], "\n")
	if kept < 24<<10 {
		t.Fatalf("the fixture has no newline in the last 24 KiB")
	}
	want := full[:kept] + fmt.Sprintf("\n… truncated, %d bytes more; narrow the query", len(full)-kept)
	if text != want {
		t.Errorf("text is %d bytes; want %d bytes ending %q", len(text), len(want), want[len(want)-60:])
	}
}

func TestMCPTruncatesASingleLongLineAtARuneBoundary(t *testing.T) {
	noBotMode(t)
	const limit = 48 << 10
	body := fmt.Sprintf(`{"text":%q}`, strings.Repeat("ü", 150<<10))
	p := newFakePlatform(t)
	p.on("GET", "/robots/vs/inspect/pods/default/p/logs", 200, body)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))

	res := mcpCall(t, cs, "inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p"})
	text := mcpText(t, res)
	if res.StructuredContent != nil {
		t.Error("structured content: the uncut answer is sent again")
	}
	full := mcpIndented(t, body)
	if !utf8.ValidString(text) {
		t.Error("the cut is not at a rune boundary")
	}
	// The line is cut where a rune starts, so at most three bytes short of the cap, and the note
	// counts the bytes that are gone.
	const prefix = "\n… truncated, "
	i := strings.Index(text, prefix)
	if i < 0 {
		t.Fatalf("no truncation note: ...%q", text[len(text)-80:])
	}
	kept := i
	var more int
	if _, err := fmt.Sscanf(text[i:], prefix+"%d bytes more; narrow the query", &more); err != nil {
		t.Fatalf("note %q: %v", text[i:], err)
	}
	if got, want := more, len(full)-kept; got != want {
		t.Errorf("note says %d bytes more, %d are gone", got, want)
	}
	if kept < 24<<10 || kept > limit || kept < limit-utf8.UTFMax {
		t.Errorf("kept %d of %d bytes", kept, len(full))
	}
}

func TestMCPSmallResultHasNoTruncationLine(t *testing.T) {
	c := mcpCase{tool: "get_overview", answer: `{"devices":1}`, reqs: []mcpWant{{method: "GET", path: "/overview"}}}
	_, cs := mcpHarness(t, c)
	if text := mcpText(t, mcpCall(t, cs, c.tool, nil)); strings.Contains(text, "truncated") {
		t.Errorf("a small answer was cut: %q", text)
	}
}

// The cut prefers the last newline before the cap only when it keeps at least 24 KiB; the cap is
// 48 KiB. Each case puts the indented answer's last newline before the cap at nl.
func TestMCPTruncationNewlineThreshold(t *testing.T) {
	noBotMode(t)
	for _, c := range []struct{ nl, kept int }{
		{24 << 10, 24 << 10},   // exactly 24 KiB kept: cut at the newline
		{24<<10 - 1, 48 << 10}, // one byte less: the line is cut at the cap
		{36 << 10, 36 << 10},
	} {
		body := fmt.Sprintf(`{"a":%q,"b":%q}`, strings.Repeat("x", c.nl-12), strings.Repeat("y", 150<<10))
		p := newFakePlatform(t)
		p.on("GET", "/overview", 200, body)
		e, _ := testEnv(t, p, nil)
		cs := mcpClient(t, newMCPServer(e))
		text := mcpText(t, mcpCall(t, cs, "get_overview", nil))
		full := mcpIndented(t, body)
		if got := strings.LastIndex(full[:48<<10], "\n"); got != c.nl {
			t.Fatalf("fixture: last newline before the cap at %d, want %d", got, c.nl)
		}
		want := full[:c.kept] + fmt.Sprintf("\n… truncated, %d bytes more; narrow the query", len(full)-c.kept)
		if text != want {
			t.Errorf("newline at %d: kept %d bytes, want %d", c.nl, strings.Index(text, "\n… truncated"), c.kept)
		}
	}
}

// An answer with no text is an empty object, not an empty result.
func TestMCPEmptyAnswerIsAnEmptyObject(t *testing.T) {
	for _, body := range []string{"", " \n"} {
		c := mcpCase{tool: "list_fleets", answer: body, reqs: []mcpWant{{method: "GET", path: "/fleets"}}}
		_, cs := mcpHarness(t, c)
		res := mcpCall(t, cs, c.tool, nil)
		if got := mcpText(t, res); res.IsError || got != "{}" {
			t.Errorf("the answer %q gave %q (error %v), want {}", body, got, res.IsError)
		}
	}
}

// ---------- (j) errors ----------

func TestMCPErrorsAreErrorResults(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots", 500, `{"error":{"message":"boom"}}`)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))

	res := mcpCall(t, cs, "list_robots", nil)
	if !res.IsError {
		t.Fatalf("a 500 was a success: %q", mcpText(t, res))
	}
	if got := mcpText(t, res); got != "boom (HTTP 500)" {
		t.Errorf("text %q", got)
	}
	if res.StructuredContent != nil {
		t.Errorf("structured content %+v", res.StructuredContent)
	}
}

func TestMCPTerminalOffIsThePlatformsOwnWords(t *testing.T) {
	const msg = "The Shell of vs is off; ask a person to turn it on."
	noBotMode(t)
	p := newFakePlatform(t)
	for _, path := range []string{
		"/robots/vs/inspect/pods/default/p",
		"/robots/vs/inspect/pods/default/p/logs",
		"/robots/vs/inspect/releases/default/r",
	} {
		p.on("GET", path, 409, `{"code":"terminal_off","message":"`+msg+`"}`)
	}
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{"inspect_pod", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p"}},
		{"inspect_logs", map[string]any{"robot": "vs", "pod_namespace": "default", "pod": "p"}},
		{"inspect_release", map[string]any{"robot": "vs", "release_namespace": "default", "release": "r"}},
	} {
		res := mcpCall(t, cs, c.tool, c.args)
		if !res.IsError {
			t.Errorf("%s: a 409 was a success: %q", c.tool, mcpText(t, res))
			continue
		}
		if got := mcpText(t, res); got != msg {
			t.Errorf("%s: %q, want %q", c.tool, got, msg)
		}
	}
}

// ---------- (k) concurrency ----------

func TestMCPConcurrentCallsKeepTheirTeamspace(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots", 200, `[{"name":"configured"}]`)
	p.on("GET", "/api/device/fleet/acme/robots", 200, `[{"name":"acme"}]`)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ns := ""
			want := `"name": "configured"`
			if i%2 == 1 {
				ns = "acme"
				want = `"name": "acme"`
			}
			args := map[string]any{}
			if ns != "" {
				args["namespace"] = ns
			}
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_robots", Arguments: args})
			if err != nil {
				t.Errorf("%d: %v", i, err)
				return
			}
			if res.IsError || !strings.Contains(mcpText(t, res), want) {
				t.Errorf("%d (%q): %q", i, ns, mcpText(t, res))
			}
		}(i)
	}
	wg.Wait()
	if namespace != "" {
		t.Errorf("a handler set the package's namespace to %q", namespace)
	}
}

// ---------- (l) a panic is one error result ----------

func TestMCPPanicBecomesAnErrorResult(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	p.on("GET", "/robots", 200, `[{"name":"r1"}]`)
	e, _ := testEnv(t, p, nil)
	s := newMCPServer(e)
	addTool(s, "boom", "a test tool that panics", func(context.Context, *mcp.CallToolRequest, mcpNoInput) (*mcp.CallToolResult, any, error) {
		var m map[string]int
		m["x"] = 1 //nolint:staticcheck // writing to the nil map is the panic this test needs
		return nil, nil, nil
	})
	cs := mcpClient(t, s)

	stopOut := captureOSStdout(t)
	stopErr := captureOSFile(t, &os.Stderr)
	res := mcpCall(t, cs, "boom", nil)
	stderr := stopErr()
	if got := stopOut(); got != "" {
		t.Errorf("the panic wrote %q to stdout, the protocol's stream", got)
	}
	if !strings.HasPrefix(stderr, "tnctl mcp: boom panicked: assignment to entry in nil map\n") || !strings.Contains(stderr, "goroutine ") {
		t.Errorf("stderr %q: want the panic line, then its stack", stderr)
	}
	const want = "internal error in tnctl mcp (boom); see the agent's MCP server log"
	if !res.IsError || len(res.Content) != 1 || mcpText(t, res) != want {
		t.Fatalf("%+v %q", res, mcpText(t, res))
	}
	if strings.Contains(mcpText(t, res), "goroutine") || strings.Contains(mcpText(t, res), "panic:") {
		t.Errorf("the result holds the panic: %q", mcpText(t, res))
	}
	if res := mcpCall(t, cs, "list_robots", nil); res.IsError {
		t.Errorf("the server did not survive the panic: %s", mcpText(t, res))
	}
}

// ---------- (m) runMCP's ordinary ends ----------

func TestMCPRunReturnsNilWhenTheSessionEnds(t *testing.T) {
	noBotMode(t)
	run := func(t *testing.T, ctx context.Context, t2 mcp.Transport) chan error {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- runMCP(ctx, &cliEnv{}, t2) }()
		return done
	}
	wait := func(t *testing.T, done chan error) {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("runMCP: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("runMCP did not return")
		}
	}

	t.Run("context already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		st, _ := mcp.NewInMemoryTransports()
		wait(t, run(t, ctx, st))
	})

	t.Run("cancelled after the client connected", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		st, ct := mcp.NewInMemoryTransports()
		done := run(t, ctx, st)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		cancel()
		wait(t, done)
	})

	t.Run("cancelled during a call", func(t *testing.T) {
		p := newFakePlatform(t)
		entered, release := make(chan struct{}, 1), make(chan struct{})
		t.Cleanup(func() { close(release) }) // runs before the fake's srv.Close
		p.onFunc("GET", "/robots", func(rec) (int, string) {
			entered <- struct{}{}
			<-release
			return 200, `[]`
		})
		e, _ := testEnv(t, p, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		st, ct := mcp.NewInMemoryTransports()
		done := make(chan error, 1)
		go func() { done <- runMCP(ctx, e, st) }()
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cs.Close() })
		go func() {
			_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_robots", Arguments: map[string]any{}})
		}()
		<-entered
		cancel()
		wait(t, done)
	})

	t.Run("the client closes the session", func(t *testing.T) {
		st, ct := mcp.NewInMemoryTransports()
		done := run(t, context.Background(), st)
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := cs.Close(); err != nil {
			t.Fatal(err)
		}
		wait(t, done)
	})
}

// ---------- (n) bot mode allows it ----------

func TestMCPIsNotRefusedInBotMode(t *testing.T) {
	if botRefused["mcp"] {
		t.Error("botRefused refuses mcp: a TeknoirBot's agent needs it")
	}
}

// ---------- (o) nothing but the protocol on stdout ----------

// captureOSStdout swaps os.Stdout for a pipe that a goroutine drains, until stop or the end of the test.
func captureOSStdout(t *testing.T) (stop func() string) {
	t.Helper()
	return captureOSFile(t, &os.Stdout)
}

// captureOSFile swaps *f (os.Stdout or os.Stderr) for a pipe that a goroutine drains, until stop or
// the end of the test.
func captureOSFile(t *testing.T, f **os.File) (stop func() string) {
	t.Helper()
	old := *f
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	*f = w
	done := make(chan string, 1)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	var once sync.Once
	var got string
	stop = func() string {
		once.Do(func() { *f = old; _ = w.Close(); got = <-done })
		return got
	}
	t.Cleanup(func() { stop() })
	return stop
}

func TestMCPWritesNothingToStdout(t *testing.T) {
	noBotMode(t)
	useLogin(t, map[string]any{"email": "a@b.c"}, "teknoir-demo")
	initConfig()
	p := newFakePlatform(t)
	for _, c := range mcpCases() {
		mcpRegister(p, c)
	}
	p.on("GET", mcpAcmePath("/overview"), 500, `{"error":{"message":"boom"}}`)
	p.on("GET", "/mqtt/stream", 200, "event: message\ndata: {\"topic\":\"/devices/d1/state/x\",\"payload\":\"hi\",\"retain\":false,\"ts\":\"2026-11-08T10:00:00Z\"}\n\n")
	e, _ := testEnv(t, p, nil)

	ptermBuf := capturePterm(t)
	stop := captureOSStdout(t)

	cs := mcpClient(t, newMCPServer(e))
	for _, name := range mcpToolNames {
		var args map[string]any
		for _, c := range mcpCases() {
			if c.tool == name && args == nil {
				args = c.args
			}
		}
		if args == nil && name == "mqtt_sample" {
			args = map[string]any{"topics": []string{"/devices/+/state/#"}}
		}
		if res := mcpCall(t, cs, name, args); res == nil || res.IsError {
			t.Errorf("%s did not succeed: %+v", name, res)
		}
	}
	if res := mcpCall(t, cs, "get_overview", map[string]any{"namespace": "acme"}); !res.IsError {
		t.Errorf("the 500 was a success: %q", mcpText(t, res))
	}

	if got := stop(); got != "" {
		t.Errorf("tnctl mcp wrote %q to stdout", got)
	}
	if got := ptermBuf.String(); got != "" {
		t.Errorf("tnctl mcp wrote %q through pterm", got)
	}
}

// ---------- (p) mqtt_sample ----------

// mcpSampleFast makes "seconds" of mqtt_sample 100 ms for the test.
func mcpSampleFast(t *testing.T) {
	t.Helper()
	old := mcpSampleUnit
	mcpSampleUnit = 100 * time.Millisecond
	t.Cleanup(func() { mcpSampleUnit = old })
}

// mcpSampleOpenFast shortens the bound on opening the stream, so a test need not wait 20 s.
func mcpSampleOpenFast(t *testing.T, d time.Duration) {
	t.Helper()
	old := mcpSampleOpenMax
	mcpSampleOpenMax = d
	t.Cleanup(func() { mcpSampleOpenMax = old })
}

// mcpSSE serves the MQTT stream of the teamspace: write sends what arrives, and with hold the
// request stays open until the client ends it. ended receives one value per request that ended; the
// headers are flushed before write, so a test can cancel the call once the server holds it.
func mcpSSE(t *testing.T, write func(w http.ResponseWriter), hold bool) (*httptest.Server, chan struct{}) {
	t.Helper()
	return mcpSSEAfter(t, 0, write, hold)
}

// mcpSSEAfter is mcpSSE whose platform waits before its headers, as it does while it reads the
// broker login, connects and subscribes. A status over 0 is answered instead of the stream.
func mcpSSEAfter(t *testing.T, before time.Duration, write func(w http.ResponseWriter), hold bool) (*httptest.Server, chan struct{}) {
	t.Helper()
	ended := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fleet/teknoir-demo/mqtt/stream" {
			http.NotFound(w, r)
			return
		}
		select {
		case <-time.After(before):
		case <-r.Context().Done():
			ended <- struct{}{}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if write != nil {
			write(w)
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if hold {
			<-r.Context().Done()
		}
		ended <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return srv, ended
}

// mcpSSERefuse answers every stream request with status and body, after before.
func mcpSSERefuse(t *testing.T, before time.Duration, status int, body string) (*httptest.Server, chan struct{}) {
	t.Helper()
	ended := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(before):
		case <-r.Context().Done():
			ended <- struct{}{}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
		ended <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return srv, ended
}

// mcpSampleEnv points the fleet's teamspace at srv.
func mcpSampleEnv(t *testing.T, srv *httptest.Server) *cliEnv {
	t.Helper()
	noBotMode(t)
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: srv.URL + "/fleet/teknoir-demo",
			Token: func(context.Context) (string, error) { return "tok", nil }}, "teknoir.cloud", "teknoir-demo", nil
	}
	return e
}

// mcpSSEEvent is one server-sent event, its data the JSON of v.
func mcpSSEEvent(name string, v any) string {
	b, _ := json.Marshal(v)
	return "event: " + name + "\ndata: " + string(b) + "\n\n"
}

// mcpSSEMsg is a message event of the stream with a text payload.
func mcpSSEMsg(topic, payload string) string {
	return mcpSSEEvent("message", map[string]any{
		"topic": topic, "payload": payload, "retain": false, "ts": "2026-11-08T10:00:00Z"})
}

// mcpSampleOut is a decoded mqtt_sample result.
type mcpSampleOut struct {
	Messages  []json.RawMessage `json:"messages"`
	Dropped   int               `json:"dropped"`
	StoppedBy string            `json:"stoppedBy"`
}

// mcpSampleOf decodes a successful mqtt_sample result, with its text.
func mcpSampleOf(t *testing.T, res *mcp.CallToolResult) (mcpSampleOut, string) {
	t.Helper()
	if res.IsError {
		t.Fatalf("error result: %s", mcpText(t, res))
	}
	text := mcpText(t, res)
	var out mcpSampleOut
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("result %q: %v", text, err)
	}
	return out, text
}

// mcpSampleFields is one message of a sample, as its keys.
func mcpSampleFields(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("message %q: %v", raw, err)
	}
	return fields
}

// mcpWaitEnded waits at most 2 s for the server's request to end.
func mcpWaitEnded(t *testing.T, ended <-chan struct{}) {
	t.Helper()
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream's request did not end")
	}
}

// sampleArgs is the arguments of the calls below.
func sampleArgs(extra map[string]any) map[string]any {
	args := map[string]any{"topics": []string{"/devices/+/state/#"}}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

func TestMCPMqttSampleStopsAtMax(t *testing.T) {
	mcpSampleFast(t)
	srv, ended := mcpSSE(t, func(w http.ResponseWriter) {
		for i := 0; i < 5; i++ {
			_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/x%d", i), "m"))
		}
	}, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"max": 2})))
	if len(got.Messages) != 2 || got.StoppedBy != "max" {
		t.Errorf("%d messages, stopped by %q, want 2 and max", len(got.Messages), got.StoppedBy)
	}
	mcpWaitEnded(t, ended)
}

func TestMCPMqttSampleStopsAtSeconds(t *testing.T) {
	mcpSampleFast(t)
	srv, ended := mcpSSE(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", "one"))
	}, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	start := time.Now()
	got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 1})))
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("the sample took %s", d)
	}
	if len(got.Messages) != 1 || got.StoppedBy != "seconds" {
		t.Errorf("%d messages, stopped by %q, want 1 and seconds", len(got.Messages), got.StoppedBy)
	}
	mcpWaitEnded(t, ended)
}

func TestMCPMqttSampleSilenceEndsAtSeconds(t *testing.T) {
	mcpSampleFast(t)
	srv, ended := mcpSSE(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, ": keepalive\n\n")
	}, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	got, text := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 1})))
	if !strings.Contains(text, `"messages": []`) {
		t.Errorf("messages are %q, want []", text)
	}
	if got.StoppedBy != "seconds" {
		t.Errorf("stopped by %q, want seconds", got.StoppedBy)
	}
	mcpWaitEnded(t, ended)
}

func TestMCPMqttSampleEndsWithTheStream(t *testing.T) {
	srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", "one"))
	}, false)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
	if len(got.Messages) != 1 || got.StoppedBy != "end" {
		t.Errorf("%d messages, stopped by %q, want 1 and end", len(got.Messages), got.StoppedBy)
	}
}

func TestMCPMqttSampleCountsDropped(t *testing.T) {
	srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 2, "reason": "rate"}))
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 3, "reason": "slow"}))
		_, _ = io.WriteString(w, mcpSSEEvent("other", map[string]any{"count": 100}))
	}, false)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
	if got.Dropped != 5 {
		t.Errorf("dropped %d, want 5", got.Dropped)
	}
	if len(got.Messages) != 0 {
		t.Errorf("%d messages, want none: the event named other was not ignored", len(got.Messages))
	}
}

func TestMCPMqttSampleDefaults(t *testing.T) {
	t.Run("max 50", func(t *testing.T) {
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
			for i := 0; i < 60; i++ {
				_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/x%d", i), "m"))
			}
		}, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
		got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
		if len(got.Messages) != 50 || got.StoppedBy != "max" {
			t.Errorf("%d messages, stopped by %q, want 50 and max", len(got.Messages), got.StoppedBy)
		}
	})
	t.Run("seconds 5", func(t *testing.T) {
		mcpSampleFast(t) // 5 seconds are 500 ms
		srv, _ := mcpSSE(t, nil, true)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
		start := time.Now()
		got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 0})))
		if d := time.Since(start); d < 500*time.Millisecond || d > 2*time.Second || got.StoppedBy != "seconds" {
			t.Errorf("stopped by %q after %s, want seconds after 500 ms (5 x 100 ms)", got.StoppedBy, d)
		}
	})
}

func TestMCPMqttSampleStreamErrorIsAnErrorResult(t *testing.T) {
	t.Run("the platform's error", func(t *testing.T) {
		const msg = "The MQTT broker closed the connection."
		srv, ended := mcpSSE(t, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEEvent("error", map[string]any{"code": "broker_closed", "message": msg}))
		}, true)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

		res := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil))
		if !res.IsError || !strings.Contains(mcpText(t, res), msg) {
			t.Errorf("%+v %q", res, mcpText(t, res))
		}
		mcpWaitEnded(t, ended)

		// The sample that failed gave its slot back: the next call reaches the platform again.
		res2 := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil))
		if !res2.IsError || !strings.Contains(mcpText(t, res2), msg) {
			t.Errorf("after an error event the slot was not released: %q", mcpText(t, res2))
		}
		mcpWaitEnded(t, ended)
	})
}

// A damaged error or message event is an error result with the parser's text: no panic (addTool
// would hide it behind its own error result) and no message skipped in silence.
func TestMCPMqttSampleBadEvents(t *testing.T) {
	for _, c := range []struct{ event, want string }{
		{"event: error\ndata: not-json\n\n", "unexpected error event from the MQTT stream"},
		{"event: message\ndata: not-json\n\n", "unexpected message from the MQTT stream"},
	} {
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) { _, _ = io.WriteString(w, c.event) }, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
		if res := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)); !res.IsError || !strings.Contains(mcpText(t, res), c.want) {
			t.Errorf("%q: %v %q, want an error result with %q", c.event, res.IsError, mcpText(t, res), c.want)
		}
	}
}

func TestMCPMqttSampleKeepsTheServersFields(t *testing.T) {
	srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{
			"topic": "/devices/d1/state/x", "payload": "hi", "retain": false,
			"ts": "2026-11-08T10:00:00Z", "extra": 1}))
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{
			"topic": "/devices/d1/state/y", "payloadBase64": "AAEC", "retain": false, "ts": "t"}))
	}, false)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
	if len(got.Messages) != 2 {
		t.Fatalf("%d messages", len(got.Messages))
	}
	text := mcpSampleFields(t, got.Messages[0])
	if string(text["extra"]) != "1" {
		t.Errorf("extra %q: a field tnctl does not know was dropped", text["extra"])
	}
	if _, ok := text["payloadBase64"]; ok {
		t.Error("payloadBase64 was added to a text message")
	}
	if string(text["payload"]) != `"hi"` {
		t.Errorf("payload %q", text["payload"])
	}
	bin := mcpSampleFields(t, got.Messages[1])
	if _, ok := bin["payload"]; ok {
		t.Error("payload was added to a base64 message")
	}
	if string(bin["payloadBase64"]) != `"AAEC"` {
		t.Errorf("payloadBase64 %q", bin["payloadBase64"])
	}
}

func TestMCPMqttSampleCutsALongPayload(t *testing.T) {
	t.Run("text", func(t *testing.T) {
		// 102401 bytes; byte 4096 is the second byte of a "ü", so the cut must step back one byte.
		long := "a" + strings.Repeat("ü", (100<<10)/2)
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", long))
		}, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

		got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
		if len(got.Messages) != 1 {
			t.Fatalf("%d messages", len(got.Messages))
		}
		fields := mcpSampleFields(t, got.Messages[0])
		var payload string
		if err := json.Unmarshal(fields["payload"], &payload); err != nil {
			t.Fatalf("payload %q: %v", fields["payload"], err)
		}
		if len(payload) != 4095 || !utf8.ValidString(payload) || strings.ContainsRune(payload, utf8.RuneError) {
			t.Errorf("payload is %d bytes (valid %v, U+FFFD %v), want 4095: cut where a rune starts",
				len(payload), utf8.ValidString(payload), strings.ContainsRune(payload, utf8.RuneError))
		}
		if got, want := string(fields["payloadBytes"]), "102401"; got != want {
			t.Errorf("payloadBytes %s, want %s", got, want)
		}
		if got := string(fields["payloadTruncated"]); got != "true" {
			t.Errorf("payloadTruncated %s, want true", got)
		}
	})

	t.Run("base64", func(t *testing.T) {
		enc := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), 100<<10))
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{"topic": "/devices/d1/state/x", "payloadBase64": enc}))
		}, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

		got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
		if len(got.Messages) != 1 {
			t.Fatalf("%d messages", len(got.Messages))
		}
		fields := mcpSampleFields(t, got.Messages[0])
		var cut string
		if err := json.Unmarshal(fields["payloadBase64"], &cut); err != nil {
			t.Fatalf("payloadBase64 %q: %v", fields["payloadBase64"], err)
		}
		if len(cut) != 4096 {
			t.Errorf("%d base64 characters, want 4096", len(cut))
		}
		if _, err := base64.StdEncoding.DecodeString(cut); err != nil {
			t.Errorf("the cut base64 does not decode: %v", err)
		}
		if got, want := string(fields["payloadBytes"]), "102400"; got != want {
			t.Errorf("payloadBytes %s, want %s", got, want)
		}
	})
}

// TestMCPMqttSampleSizeCountsEscapes writes the data as the platform's JSON.stringify does: <, >, &
// and U+2028 as they are. json.Marshal escapes each to six bytes in the result, so the size stop must
// count the escaped form, or the result outgrows mcpMaxResult and is cut into JSON that does not parse.
func TestMCPMqttSampleSizeCountsEscapes(t *testing.T) {
	for _, c := range []struct{ name, payload string }{
		{"lt", strings.Repeat("<", 4096)},
		{"amp", strings.Repeat("&", 4000)},
		{"u2028", strings.Repeat("\u2028", 1365)},
		{"html", strings.Repeat("<a>&</a>", 512)},
	} {
		t.Run(c.name, func(t *testing.T) {
			mcpSampleFast(t)
			srv, ended := mcpSSE(t, func(w http.ResponseWriter) {
				for i := 0; i < 200; i++ {
					_, _ = io.WriteString(w, "event: message\ndata: {\"topic\":\"/devices/d1/state/x"+strconv.Itoa(i)+
						"\",\"payload\":\""+c.payload+"\",\"retain\":false,\"ts\":\"t\"}\n\n")
				}
			}, true)
			cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

			res := mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"max": 200, "seconds": 30}))
			text := mcpText(t, res)
			var got mcpSampleOut
			err := json.Unmarshal([]byte(text), &got)
			if res.IsError || err != nil || got.StoppedBy != "size" || strings.Contains(text, "truncated") {
				tail := text[max(0, len(text)-80):]
				t.Errorf("%d bytes (JSON error %v), %d messages, stopped by %q, ends %q; want size and no truncated line",
					len(text), err, len(got.Messages), got.StoppedBy, tail)
			}
			mcpWaitEnded(t, ended)
		})
	}
}

func TestMCPMqttSampleStopsAtTheSizeCap(t *testing.T) {
	t.Run("4 KiB messages", func(t *testing.T) {
		payload := strings.Repeat("m", 4096)
		srv, ended := mcpSSE(t, func(w http.ResponseWriter) {
			for i := 0; i < 100; i++ {
				_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/x%d", i), payload))
			}
		}, true)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

		got, text := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"max": 200})))
		if got.StoppedBy != "size" {
			t.Errorf("stopped by %q, want size", got.StoppedBy)
		}
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			t.Errorf("the result is not valid JSON: %v", err)
		}
		if strings.Contains(text, "truncated") {
			t.Errorf("the result was truncated by mcpResult: %d bytes", len(text))
		}
		mcpWaitEnded(t, ended)
	})

	t.Run("800 byte messages", func(t *testing.T) {
		payload := strings.Repeat("m", 800)
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
			for i := 0; i < 200; i++ {
				_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/x%d", i), payload))
			}
		}, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

		got, text := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"max": 200})))
		if got.StoppedBy != "max" && got.StoppedBy != "size" {
			t.Errorf("stopped by %q, want max or size", got.StoppedBy)
		}
		if strings.Contains(text, "truncated") {
			t.Errorf("the result was truncated by mcpResult: %d bytes", len(text))
		}
	})
}

func TestMCPMqttSampleEndsWhenTheClientCancelsTheCall(t *testing.T) {
	mcpSampleFast(t)
	started := make(chan struct{}, 1)
	srv, ended := mcpSSE(t, func(http.ResponseWriter) { started <- struct{}{} }, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "mqtt_sample", Arguments: sampleArgs(map[string]any{"seconds": 30})})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the sample did not start")
	}
	cancel()
	mcpWaitEnded(t, ended)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the call did not return")
	}
}

func TestMCPMqttSampleRunsOneAtATime(t *testing.T) {
	mcpSampleFast(t)
	var mu sync.Mutex
	requests := 0
	started := make(chan struct{}, 1)
	srv, _ := mcpSSE(t, func(http.ResponseWriter) {
		mu.Lock()
		requests++
		first := requests == 1
		mu.Unlock()
		if first {
			started <- struct{}{}
		}
	}, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	type firstRes struct {
		res *mcp.CallToolResult
		err error
	}
	first := make(chan firstRes, 1)
	go func() { // it only sends: the test goroutine reports
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mqtt_sample",
			Arguments: sampleArgs(map[string]any{"seconds": 30})})
		first <- firstRes{res, err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first sample did not start")
	}
	res := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil))
	if want := "another mqtt_sample is running; put all topics (up to 8) in one call, or try again when it ends"; !res.IsError || mcpText(t, res) != want {
		t.Errorf("%+v %q, want %q", res, mcpText(t, res), want)
	}
	select {
	case r := <-first:
		if r.err != nil {
			t.Fatalf("the first sample: %v", r.err)
		}
		if got, _ := mcpSampleOf(t, r.res); got.StoppedBy != "seconds" {
			t.Errorf("the first sample stopped by %q", got.StoppedBy)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first sample did not end")
	}
	if got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 1}))); got.StoppedBy != "seconds" {
		t.Errorf("after the first sample: stopped by %q", got.StoppedBy)
	}
	mu.Lock()
	n := requests
	mu.Unlock()
	if n != 2 {
		t.Errorf("%d requests, want 2: a refused sample sent one", n)
	}
}

func TestMCPMqttSampleRefusesBadInput(t *testing.T) {
	noBotMode(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))

	nine := make([]string, 9)
	for i := range nine {
		nine[i] = fmt.Sprintf("/devices/d%d/state/#", i)
	}
	const topicErr = `"/other/x" is not a topic a stream can follow: use /devices/<device|+>/state/#, ` +
		`/devices/<device|+>/events/#, or a concrete topic below /devices/<device|+>/state/ or /devices/<device|+>/events/`
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"topics": []string{}}, "a stream follows 1 to 8 topics, got 0"},
		{map[string]any{"topics": nine}, "a stream follows 1 to 8 topics, got 9"},
		{map[string]any{"topics": []string{"/other/x"}}, topicErr},
		{sampleArgs(map[string]any{"seconds": 31}), "seconds must be from 1 to 30"},
		{sampleArgs(map[string]any{"seconds": -1}), "seconds must be from 1 to 30"},
		{sampleArgs(map[string]any{"max": 201}), "max must be from 1 to 200"},
		{sampleArgs(map[string]any{"max": -1}), "max must be from 1 to 200"},
	} {
		res := mcpCall(t, cs, "mqtt_sample", c.args)
		if !res.IsError || mcpText(t, res) != c.want {
			t.Errorf("%v: %+v %q, want %q", c.args, res, mcpText(t, res), c.want)
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("a refused input sent %d request(s)", n)
	}
}

func TestMCPMqttSampleInBotMode(t *testing.T) {
	p, conns := sseBotAPI(t, func(int) string { return msgEvent("/devices/d1/state/x", "one") })
	cs := mcpClient(t, newMCPServer(botEnv(t, p)))
	got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
	if got.StoppedBy != "end" || len(got.Messages) != 1 {
		t.Errorf("%d messages, stopped by %q, want 1 and end", len(got.Messages), got.StoppedBy)
	}
	p.mu.Lock()
	n := *conns
	p.mu.Unlock()
	if n != 1 {
		t.Errorf("%d requests, want 1", n)
	}

	p2, conns2 := sseBotAPI(t, func(int) string { return msgEvent("/devices/d1/state/x", "one") })
	cs = mcpClient(t, newMCPServer(botEnv(t, p2)))
	res := mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"namespace": "other"}))
	if want := "a TeknoirBot works only in its own teamspace (teknoir-demo)"; !res.IsError || mcpText(t, res) != want {
		t.Errorf("%+v %q, want %q", res, mcpText(t, res), want)
	}
	p2.mu.Lock()
	n = *conns2
	p2.mu.Unlock()
	if n != 0 {
		t.Errorf("%d requests for another teamspace", n)
	}

	// The refusal gave the slot back: a sample of the bot's own teamspace runs.
	got, _ = mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(nil)))
	if got.StoppedBy != "end" || len(got.Messages) != 1 {
		t.Errorf("after the refusal: %d messages, stopped by %q, want 1 and end", len(got.Messages), got.StoppedBy)
	}
	p2.mu.Lock()
	n = *conns2
	p2.mu.Unlock()
	if n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

// The listening time starts when the stream opens: a platform that never opens it is an error after
// the open bound, not an empty success.
func TestMCPMqttSampleOpenBound(t *testing.T) {
	mcpSampleFast(t)
	mcpSampleOpenFast(t, 2*mcpSampleUnit)
	srv, _ := mcpSSEAfter(t, 3*mcpSampleUnit, nil, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	res := mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 30}))
	if !res.IsError || !strings.Contains(mcpText(t, res), "did not open the MQTT stream") {
		t.Errorf("%v %q, want an error result saying the stream did not open", res.IsError, mcpText(t, res))
	}
}

// A platform that answers 503 after its own waiting time is an error result with its message, even
// when the call's `seconds` would have run out before that: `seconds` counts from the open, not from
// the call.
func TestMCPMqttSamplePlatformRefusalIsNotAnEmptySuccess(t *testing.T) {
	mcpSampleFast(t)
	const msg = "The MQTT broker cannot be reached right now."
	srv, _ := mcpSSERefuse(t, 3*mcpSampleUnit, http.StatusServiceUnavailable,
		`{"error":{"name":"ServiceUnavailable","message":"`+msg+`"}}`)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	res := mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 1}))
	if !res.IsError || !strings.Contains(mcpText(t, res), msg) {
		t.Errorf("%v %q, want an error result with %q", res.IsError, mcpText(t, res), msg)
	}
}

// The platform's own waiting time before its headers is not listening time: a stream that opens
// after it still listens for all of `seconds`.
func TestMCPMqttSampleListensFromTheOpen(t *testing.T) {
	mcpSampleFast(t)
	srv, ended := mcpSSEAfter(t, 3*mcpSampleUnit, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", "one"))
	}, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	start := time.Now()
	got, _ := mcpSampleOf(t, mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 1})))
	if len(got.Messages) != 1 || got.StoppedBy != "seconds" {
		t.Errorf("%d messages, stopped by %q, want 1 and seconds", len(got.Messages), got.StoppedBy)
	}
	if d := time.Since(start); d < 3*mcpSampleUnit {
		t.Errorf("the sample ended after %s, before the stream opened", d)
	}
	mcpWaitEnded(t, ended)
}

// The stream's headers arrive within a millisecond of the open bound: every result is a sample or the
// open-bound error, never the bound's cancel cause read from the open stream. Statistical: without the
// fix about one call in 25 fails.
func TestMCPMqttSampleOpenBoundRace(t *testing.T) {
	mcpSampleFast(t)
	old := mcpSampleOpenMax
	t.Cleanup(func() { mcpSampleOpenMax = old })
	for i := 0; i < 60; i++ {
		mcpSampleOpenMax = 30*time.Millisecond + time.Duration(i%11)*150*time.Microsecond
		before := 30*time.Millisecond + time.Duration(i%7)*200*time.Microsecond
		srv, _ := mcpSSEAfter(t, before, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", "one"))
		}, true)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
		res := mcpCall(t, cs, "mqtt_sample", sampleArgs(map[string]any{"seconds": 1}))
		if txt := mcpText(t, res); res.IsError && !strings.Contains(txt, "did not open the MQTT stream within") {
			t.Fatalf("call %d (bound %s, headers after %s): %q", i, mcpSampleOpenMax, before, txt)
		}
		_ = cs.Close()
		srv.Close()
	}
}

// The sample lasts the real 30 seconds: only the receiving middleware can end it within the guard.
func TestMCPMqttSampleEndsWhenTheServerStops(t *testing.T) {
	started := make(chan struct{}, 1)
	srv, ended := mcpSSE(t, func(http.ResponseWriter) { started <- struct{}{} }, true)
	e := mcpSampleEnv(t, srv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runMCP(ctx, e, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	go func() {
		_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mqtt_sample",
			Arguments: sampleArgs(map[string]any{"seconds": 30})})
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the sample did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runMCP: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runMCP did not return")
	}
	mcpWaitEnded(t, ended)
}
