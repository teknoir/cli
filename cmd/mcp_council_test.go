package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/auth"
)

// ---------- the fake chat API, as the council tools use it ----------

// countAll is how many requests the fake chat API saw.
func (p *chatAPI) countAll() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// councilFast makes the council waits and reconnects fast enough for a test.
func councilFast(t *testing.T) {
	t.Helper()
	oldBackoff, oldPoll := councilBackoff, councilPollInterval
	councilBackoff = []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	councilPollInterval = 5 * time.Millisecond
	t.Cleanup(func() { councilBackoff, councilPollInterval = oldBackoff, oldPoll })
}

// councilHarness is a fake chat API and a client of the server with the council tools.
func councilHarness(t *testing.T, p *chatAPI) *mcp.ClientSession {
	t.Helper()
	noBotMode(t)
	return mcpClient(t, newMCPServerWith(p.env(t), true))
}

// chatThreadBody is a thread as P3 answers it.
func chatThreadBody(id, title, member string) string {
	m := "null"
	role := "null"
	if member != "" {
		m = `"` + member + `"`
		role = `{"id":"tpl:` + member + `","scope":"template","name":"` + strings.ToUpper(member[:1]) + member[1:] + `"}`
	}
	return fmt.Sprintf(`{"id":%q,"title":%q,"origin":"mcp","member":%s,"author":{"ref":"r"},"createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null,"memberRole":%s}`, id, title, m, role)
}

// chatTurnIn is a turn as P6 answers it.
func chatTurnIn(id, thread, member string) string {
	m := "null"
	if member != "" {
		m = `"` + member + `"`
	}
	return fmt.Sprintf(`{"turn":{"id":%q,"threadId":%q,"index":1,"kind":"ask","member":%s,"prompt":"p","options":{},`+
		`"state":"queued","final":null,"error":null,"usage":null,"author":{"ref":"r"},"createdAt":1}}`, id, thread, m)
}

// chatTurnInState is chatTurnIn with another turn state.
func chatTurnInState(id, thread, member, state string) string {
	return strings.Replace(chatTurnIn(id, thread, member), `"state":"queued"`, `"state":"`+state+`"`, 1)
}

// chatDetailBody is P4's answer: one thread with its turns and the newest event's id.
func chatDetailBody(thread string, lastSeq int64, turns string) string {
	return fmt.Sprintf(`{"thread":{"id":%q,"title":"t","origin":"mcp","member":null,"author":{"ref":"r"},`+
		`"createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null},"turns":[%s],"lastSeq":%d}`, thread, turns, lastSeq)
}

// chatDetailRoster is chatDetailBody whose thread is set to the council roster ("" for none: the
// bot's default). The display name differs from the name, as the hub's do.
func chatDetailRoster(thread, roster string, lastSeq int64, turns string) string {
	d := chatDetailBody(thread, lastSeq, turns)
	if roster == "" {
		return d
	}
	return strings.Replace(d, `"lastTurn":null}`, `"lastTurn":null,"roster":{"id":"ros_1","name":`+q(roster)+
		`,"displayName":`+q(strings.ToUpper(roster[:1])+roster[1:]+" council")+`}}`, 1)
}

// chatDetailMember is chatDetailBody whose thread names its member role.
func chatDetailMember(thread, name string, lastSeq int64, turns string) string {
	return strings.Replace(chatDetailBody(thread, lastSeq, turns), `"lastTurn":null}`,
		`"lastTurn":null,"memberRole":{"id":"rol_sa","scope":"teamspace","name":`+q(name)+`}}`, 1)
}

// streamAnalystThread is P3's answer for teamspace/stream-analyst-test: a member thread whose member
// is called name ("" for an answer without memberRole).
func streamAnalystThread(id, name string) string {
	role := "null"
	if name != "" {
		role = `{"id":"rol_sa","scope":"teamspace","name":` + q(name) + `}`
	}
	return `{"id":` + q(id) + `,"title":"MCP: test teamspace/stream-analyst-test","origin":"mcp","member":"stream-analyst-test",` +
		`"author":{"ref":"r"},"createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null,"memberRole":` + role + `}`
}

// chatTurnRecord is one turn of a P4 answer with a final answer.
func chatTurnRecord(id, thread, state, final, member string) string {
	m := "null"
	if member != "" {
		m = `"` + member + `"`
	}
	f := "null"
	if final != "" {
		b, _ := json.Marshal(final)
		f = string(b)
	}
	return fmt.Sprintf(`{"id":%q,"threadId":%q,"index":1,"kind":"ask","member":%s,"prompt":"p","options":{},`+
		`"state":%q,"final":%s,"error":null,"usage":{"calls":1,"input":5,"output":2,"cacheRead":0,"cacheWrite":0,"ms":7},`+
		`"author":{"ref":"r"},"createdAt":1}`, id, thread, m, state, f)
}

// holdStream writes the frames and keeps the connection open until the client ends it.
func holdStream(w http.ResponseWriter, r *http.Request, frames []string) {
	writeFrames(w, frames)
	<-r.Context().Done()
}

// mcpMap is a successful result's JSON.
func mcpMap(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("error result: %s", mcpText(t, res))
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(mcpText(t, res)), &v); err != nil {
		t.Fatalf("result %q: %v", mcpText(t, res), err)
	}
	return v
}

// mcpErr is an error result's text, and fails if it was not one.
func mcpErr(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if !res.IsError {
		t.Fatalf("not an error result: %s", mcpText(t, res))
	}
	return mcpText(t, res)
}

// memberFrames is turn trn_1's events: it starts, the member reports, the Captain's final and the
// usage, then the end.
func memberFrames(report, final, state string) []string {
	return memberFramesOf("trn_1", "fleet", report, final, state)
}

// memberFramesOf is memberFrames for another turn and member.
func memberFramesOf(id, member, report, final, state string) []string {
	if state == "" {
		state = "done"
	}
	f := func(seq int64, typ, data string) string { return frameOf(id, seq, typ, data) }
	return []string{
		f(1, "turn_queued", `"index":1,"kind":"ask","member":"`+member+`"`),
		f(2, "turn_start", `"mode":"member"`),
		f(3, "member_start", `"member":"`+member+`","task":0,"model":"m","effort":"high","text":"looking"`),
		f(4, "member_done", `"member":"`+member+`","task":0,"report":`+report+`,"usage":{"calls":1,"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"ms":1}`),
		f(5, "final", `"text":`+jsonText(final)),
		f(6, "usage", `"calls":2,"input":100,"output":40,"cacheRead":0,"cacheWrite":0,"ms":1500`),
		f(7, "turn_end", `"state":"`+state+`"`),
	}
}

// jsonText is s as a JSON string.
func jsonText(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// bodyEq fails unless body is exactly want's JSON: both are decoded and compared, so an extra key
// fails and the key order does not count.
func bodyEq(t *testing.T, what, body, want string) {
	t.Helper()
	got, exp := chatBodyMap(t, body), chatBodyMap(t, want)
	if !reflect.DeepEqual(got, exp) {
		t.Errorf("%s: %s, want %s", what, body, want)
	}
}

// ---------- (a) the tool list ----------

func TestMCPCouncilToolsAreListed(t *testing.T) {
	noBotMode(t)
	// Without the flag the server is the read-only one it always was.
	plain, err := mcpClient(t, newMCPServer(&cliEnv{})).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Tools) != 19 {
		t.Errorf("%d tools without --council, want the 19 read tools", len(plain.Tools))
	}
	for _, tool := range plain.Tools {
		if strings.HasPrefix(tool.Name, "council_") {
			t.Errorf("%s is listed without --council", tool.Name)
		}
	}

	cs := mcpClient(t, newMCPServerWith(&cliEnv{}, true))
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 24 {
		t.Fatalf("%d tools, want 24", len(tools.Tools))
	}
	want := map[string][]string{
		"council_info":   {"bot"},
		"council_roles":  nil,
		"council_ask":    {"bot", "member", "task"},
		"council_run":    {"bot", "question"},
		"council_result": {"bot", "turn_id"},
	}
	seen := 0
	for _, tool := range tools.Tools {
		if tool.OutputSchema != nil {
			t.Errorf("%s: an output schema would send the result again as structured content", tool.Name)
		}
		note := mcpCouncilUntrustedNote
		if tool.Name == "council_info" || tool.Name == "council_roles" {
			note = mcpCouncilPeopleNote
		}
		if !strings.HasSuffix(tool.Description, note) {
			if strings.HasPrefix(tool.Name, "council_") {
				t.Errorf("%s: description does not end with its note: %q", tool.Name, tool.Description)
			}
		}
		req, ok := want[tool.Name]
		if !ok {
			continue
		}
		seen++
		if tool.Annotations == nil {
			t.Fatalf("%s: no annotations", tool.Name)
		}
		a := tool.Annotations
		if a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s: annotations %+v", tool.Name, a)
		}
		if tool.Name == "council_info" || tool.Name == "council_roles" {
			// they only read: the annotations of the read tools
			if !a.ReadOnlyHint || !a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
				t.Errorf("%s: annotations %+v, want read-only", tool.Name, a)
			}
		} else if a.ReadOnlyHint || a.IdempotentHint || a.OpenWorldHint == nil || !*a.OpenWorldHint {
			// council_ask and council_run start work on the bot; council_result only reads, but is
			// annotated like them so that clients run one agent's stream-holding waits one at a time
			t.Errorf("%s: annotations %+v, want those of the tools that start work", tool.Name, a)
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
		if strings.Join(got, ",") != strings.Join(req, ",") {
			t.Errorf("%s: required %v, want %v", tool.Name, got, req)
		}
	}
	if seen != 5 {
		t.Errorf("%d of the 5 council tools are listed", seen)
	}
	if got := cs.InitializeResult().Instructions; got != mcpCouncilInstructions {
		t.Errorf("instructions %q, want %q", got, mcpCouncilInstructions)
	}
}

// The council tools' texts are the brief's, word for word.
func TestMCPCouncilTexts(t *testing.T) {
	noBotMode(t)
	cs := mcpClient(t, newMCPServerWith(&cliEnv{}, true))
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	desc := map[string]string{
		"council_info":  "Who may chat with a TeknoirBot and its councils: the bot's state, whether you may chat (and why not), the default council's members with their models, today's token budget, and the bot's councils (council.roster is the default council with its members; council.rosters lists every council by name, for council_run's roster). Call it before council_ask or council_run. " + mcpCouncilPeopleNote,
		"council_roles": "The members a TeknoirBot's councils can use. With bot: available lists every member the bot may ask or put in a council (the bot's own first, then the teamspace's, then the premade ones; each with its slug, scope, kind, model, effort, tool groups and status), and catalog the tool groups, skills and limits. Without bot: the teamspace's catalog (the premade members and councils, and the teamspace's shared members). council_ask takes the slug of one whose kind is member (leads and reviewers only lead or check a council), or <scope>/<slug> when two members share a slug. " + mcpCouncilPeopleNote,
		"council_ask":   "Give one member of a TeknoirBot's council a task: fleet looks at the teamspace's robots and devices with the read tools, coder works on the bot's own computer (code, builds, its k3s cluster), and council_roles lists every member the bot may ask. You lead: split the work and judge the reports; to have several members work at once, ask each with wait_seconds 0 and collect their reports with council_result (clients may run council calls one at a time). Each member keeps one conversation per MCP session, so a follow-up task sees the earlier ones; name it the same way each time (fleet and template/fleet are two conversations). It waits up to wait_seconds (default 45) for the report; a turn still running is fetched with council_result. It starts work on the bot and uses its token budget. " + mcpCouncilUntrustedNote,
		"council_run":   "Ask one of a TeknoirBot's councils: its lead plans, convenes members when needed, and answers; the council's reviewer, when it has one, checks the members' reports when the turn needs it. roster picks the council by name (council_info lists them in council.rosters); without it the bot's default council answers. Each council keeps one conversation per MCP session. mode quick has the lead answer alone, auto (the default) lets the lead decide, and council (or thorough) always convenes members and has the reviewer, if the council has one, check their reports: slower, several times the tokens. It waits like council_ask; a turn still running is fetched with council_result. It starts work on the bot and uses its token budget. " + mcpCouncilUntrustedNote,
	}
	props := map[string]map[string]string{
		"council_ask": {"member": "which member: its slug, such as fleet or coder, or <scope>/<slug> (scope bot, teamspace or template) when two members share a slug; council_info lists the default council's members (council.roster.members), council_roles every member the bot may ask (those with kind member)"},
		"council_run": {
			"roster":   "the council to ask, by name (council_info: council.rosters); empty means the bot's default council",
			"mode":     "auto (default): the lead answers itself or convenes members; quick: the lead answers alone; council: the lead always convenes members, and the council's reviewer, when it has one, checks their reports (slower, several times the tokens)",
			"thorough": "true asks for the most careful answer, slower and several times the tokens: the same as mode council, and the platform records the question as thorough",
		},
		"council_roles": {"bot": "the TeknoirBot, by name; empty lists the teamspace's catalog instead"},
	}
	seen := 0
	for _, tool := range tools.Tools {
		if want, ok := desc[tool.Name]; ok {
			seen++
			if tool.Description != want {
				t.Errorf("%s: description\n%q\nwant\n%q", tool.Name, tool.Description, want)
			}
		}
		for name, want := range props[tool.Name] {
			schema, _ := tool.InputSchema.(map[string]any)
			ps, _ := schema["properties"].(map[string]any)
			prop, _ := ps[name].(map[string]any)
			if got := fmt.Sprint(prop["description"]); got != want {
				t.Errorf("%s.%s: %q, want %q", tool.Name, name, got, want)
			}
		}
	}
	if seen != len(desc) {
		t.Errorf("%d of the %d described tools are listed", seen, len(desc))
	}
	if got, want := cs.InitializeResult().Instructions, "A view of a Teknoir teamspace: read-only, except council_ask and council_run, which hand work to a TeknoirBot's council: they start turns on the bot and spend its token budget."; !strings.HasPrefix(got, want) {
		t.Errorf("instructions %q, want them to open with %q", got, want)
	}
	c := newMCPCmd(&cliEnv{})
	if got, want := c.Flags().Lookup("council").Usage, "also offer the five council tools: hand work to a TeknoirBot's council (a person's own agent only)"; got != want {
		t.Errorf("--council: %q, want %q", got, want)
	}
	long := "With --council it also offers council_info, council_roles, council_ask, council_run and\n" +
		"council_result: they hand work to a TeknoirBot's council, so that your own agent can lead while the\n" +
		"bot's members do the work, or ask one of the bot's councils to answer. council_ask and council_run\n" +
		"start work on the bot and spend its token budget; council_info, council_roles and council_result\n" +
		"only read. --council is for a person's own agent; a TeknoirBot cannot drive a council.\n"
	if !strings.Contains(c.Long, long) {
		t.Errorf("Long has no paragraph\n%s", long)
	}
}

// ---------- (b) council_ask ----------

func TestMCPCouncilAskFleet(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFrames(`{"summary":"two robots","findings":["r1 up"],"proposals":[],"confidence":"high","open_questions":[]}`,
			"## Report\nr1 is up.", ""))
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "what is running?", "effort": "high",
	}))
	if got["untrusted"] != mcpCouncilUntrustedNote {
		t.Errorf("untrusted %v", got["untrusted"])
	}
	if got["turnId"] != "trn_1" || got["threadId"] != "thr_fleet" || got["member"] != "fleet" || got["state"] != "done" {
		t.Errorf("result %v", got)
	}
	report, _ := got["report"].(map[string]any)
	if report["summary"] != "two robots" {
		t.Errorf("report %v", got["report"])
	}
	if got["final"] != "## Report\nr1 is up." {
		t.Errorf("final %v", got["final"])
	}
	usage, _ := got["usage"].(map[string]any)
	if usage["calls"] != float64(2) || usage["input"] != float64(100) {
		t.Errorf("usage %v", got["usage"])
	}
	if _, ok := usage["at"]; ok {
		t.Errorf("the usage carries the frame's at: %v", usage)
	}
	if _, ok := got["next"]; ok {
		t.Errorf("a turn that ended carries next: %v", got["next"])
	}

	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 1 {
		t.Fatalf("%d threads created", len(created))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(created[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["origin"] != "mcp" || body["member"] != "fleet" || body["title"] != "MCP: test fleet" {
		t.Errorf("thread body %s", created[0].Body)
	}
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_fleet/turns")
	if len(turns) != 1 {
		t.Fatalf("%d turns posted", len(turns))
	}
	if err := json.Unmarshal([]byte(turns[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["prompt"] != "what is running?" || body["member"] != "fleet" || body["effort"] != "high" {
		t.Errorf("turn body %s", turns[0].Body)
	}
	if _, ok := body["mode"]; ok {
		t.Errorf("a member turn carries a mode: %s", turns[0].Body)
	}
}

// ---------- (c) one conversation per member ----------

func TestMCPCouncilKeepsOneThreadPerMember(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.onFunc("POST", "/bots/scout/chat/threads", func(r rec) (int, string) {
		var body map[string]any
		_ = json.Unmarshal([]byte(r.Body), &body)
		member, _ := body["member"].(string)
		switch member {
		case "fleet":
			return 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet")
		case "coder":
			return 201, chatThreadBody("thr_coder", "MCP: test coder", "coder")
		}
		return 201, chatThreadBody("thr_council", "MCP: test council", "")
	})
	for _, th := range []struct{ id, member string }{
		{"thr_coder", "coder"},
		{"thr_council", ""},
	} {
		th := th
		p.on("POST", "/bots/scout/chat/threads/"+th.id+"/turns", 202, chatTurnIn("trn_"+th.id[4:], th.id, th.member))
		turn, member, final := "trn_"+th.id[4:], th.member, th.id[4:]+" says"
		if member == "" {
			member = "fleet" // the Captain answers; the member is only who a frame is about
		}
		p.onStream("/bots/scout/chat/threads/"+th.id+"/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, memberFramesOf(turn, member, `{"summary":"ok"}`, final, ""))
		})
	}
	// The fleet thread after its first turn: one turn that ended, seven events.
	p.onFunc("GET", "/bots/scout/chat/threads/thr_fleet", func(rec) (int, string) {
		return 200, chatDetailBody("thr_fleet", 7, chatTurnRecord("trn_1", "thr_fleet", "done", "first answer", "fleet"))
	})
	var mu sync.Mutex
	var fleetPosts int
	var after []string
	p.onFunc("POST", "/bots/scout/chat/threads/thr_fleet/turns", func(rec) (int, string) {
		mu.Lock()
		fleetPosts++
		n := fleetPosts
		mu.Unlock()
		return 202, chatTurnIn(fmt.Sprintf("trn_%d", n), "thr_fleet", "fleet")
	})
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		after = append(after, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		if conn == 1 {
			writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "first answer", ""))
			return
		}
		writeFrames(w, []string{
			frameOf("trn_2", 8, "final", `"text":"and again"`),
			frameOf("trn_2", 9, "turn_end", `"state":"done"`),
		})
	})
	cs := councilHarness(t, p)

	for i := 0; i < 2; i++ {
		got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "first"}))
		wantTurn, wantFinal := "trn_1", "first answer"
		if i == 1 {
			wantTurn, wantFinal = "trn_2", "and again"
		}
		if got["turnId"] != wantTurn || got["threadId"] != "thr_fleet" || got["final"] != wantFinal || got["state"] != "done" {
			t.Errorf("ask %d: %v, want turn %s on thr_fleet, final %q", i, got, wantTurn, wantFinal)
		}
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
		t.Errorf("%d threads created for the two fleet asks, want 1", n)
	}
	mu.Lock()
	got := append([]string{}, after...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "" || got[1] != "7" {
		t.Errorf("Last-Event-ID per fleet stream %q, want [\"\", \"7\"]: the second ask resumes the thread", got)
	}

	// coder keeps a conversation of its own
	coder := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "coder", "task": "build"}))
	if coder["turnId"] != "trn_coder" || coder["threadId"] != "thr_coder" || coder["final"] != "coder says" || coder["state"] != "done" {
		t.Errorf("coder: %v", coder)
	}
	// the council answers on a thread of its own, with no member
	run := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}))
	if run["turnId"] != "trn_council" || run["threadId"] != "thr_council" || run["answer"] != "council says" || run["state"] != "done" {
		t.Errorf("run: %v", run)
	}
	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 3 {
		t.Fatalf("%d threads created, want 3", len(created))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(created[2].Body), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["member"]; ok {
		t.Errorf("a council thread names a member: %s", created[2].Body)
	}
	if body["title"] != "MCP: test council" {
		t.Errorf("council thread title %v", body["title"])
	}
}

// ---------- (d) two members at once ----------

func TestMCPCouncilAsksTwoMembersAtOnce(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.onFunc("POST", "/bots/scout/chat/threads", func(r rec) (int, string) {
		var body map[string]any
		_ = json.Unmarshal([]byte(r.Body), &body)
		if body["member"] == "coder" {
			return 201, chatThreadBody("thr_coder", "MCP: test coder", "coder")
		}
		return 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet")
	})
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_fleet", "thr_fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_coder/turns", 202, chatTurnIn("trn_coder", "thr_coder", "coder"))
	started := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	open := func(member, turn string) func(w http.ResponseWriter, r *http.Request, conn int) {
		return func(w http.ResponseWriter, r *http.Request, conn int) {
			if conn == 1 {
				started <- member
			}
			<-release
			writeFrames(w, memberFramesOf(turn, member, `{"summary":"`+member+` done"}`, member+" answered", ""))
		}
	}
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", open("fleet", "trn_fleet"))
	p.onStream("/bots/scout/chat/threads/thr_coder/events", open("coder", "trn_coder"))
	cs := councilHarness(t, p)
	t.Cleanup(func() { once.Do(func() { close(release) }) }) // a failed test must not leave the streams held

	type answer struct {
		member string
		res    *mcp.CallToolResult
		err    error
	}
	done := make(chan answer, 2)
	for _, member := range []string{"fleet", "coder"} {
		member := member
		go func() {
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "council_ask",
				Arguments: map[string]any{"bot": "scout", "member": member, "task": "go"}})
			done <- answer{member, res, err}
		}()
	}
	// Both streams are open before either is answered: the two asks ran side by side.
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("the two streams did not open at once")
		}
	}
	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		a := <-done
		if a.err != nil {
			t.Fatalf("%s: %v", a.member, a.err)
		}
		got := mcpMap(t, a.res)
		if got["state"] != "done" || got["member"] != a.member {
			t.Errorf("%s: %v", a.member, got)
		}
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 2 {
		t.Errorf("%d threads, want 2", n)
	}
}

// ---------- (e) a wait that runs out, and council_result ----------

func TestMCPCouncilWaitThenResult(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	var mu sync.Mutex
	var ids []string
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		if conn == 1 {
			// the turn starts and then says nothing: the wait runs out
			holdStream(w, r, []string{frame(1, "turn_start", `"mode":"member"`)})
			return
		}
		// The turn ended but the final was not in a frame this run saw: P4 has it.
		writeFrames(w, []string{frame(3, "turn_end", `"state":"done"`)})
	})
	p.onFunc("GET", "/bots/scout/chat/threads/thr_fleet", func(rec) (int, string) {
		return 200, chatDetailBody("thr_fleet", 3, chatTurnRecord("trn_1", "thr_fleet", "done", "the answer", "fleet"))
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 1,
	}))
	if got["state"] != "running" {
		t.Fatalf("state %v, want running", got["state"])
	}
	if next, _ := got["next"].(string); next != "The turn is still running: call council_result with bot scout and turn_id trn_1 to wait for it." {
		t.Errorf("next %q", next)
	}

	got = mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_1", "wait_seconds": 5}))
	if got["state"] != "done" || got["final"] != "the answer" {
		t.Errorf("result %v", got)
	}
	if usage, _ := got["usage"].(map[string]any); usage["calls"] != float64(1) {
		t.Errorf("usage %v: the record's usage", got["usage"])
	}
	mu.Lock()
	resume := append([]string{}, ids...)
	mu.Unlock()
	if len(resume) != 2 || resume[1] != "1" {
		t.Errorf("Last-Event-ID per stream %q, want the resumption after seq 1", resume)
	}
}

// ---------- (f) a turn this process never saw ----------

func TestMCPCouncilResultFindsAnUnknownTurn(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200, `{"items":[{"id":"thr_old","title":"old","origin":"web","member":null,"author":{"ref":"r"},"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":{"id":"trn_old","state":"done"}}]}`)
	p.on("GET", "/bots/scout/chat/threads/thr_old", 200,
		chatDetailBody("thr_old", 9, chatTurnRecord("trn_old", "thr_old", "done", "an old answer", "coder")))
	// A server of its own: the process never started this turn.
	cs := mcpClient(t, newMCPServerWith(p.env(t), true))

	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_old"}))
	if got["state"] != "done" || got["final"] != "an old answer" || got["member"] != "coder" || got["threadId"] != "thr_old" {
		t.Errorf("result %v", got)
	}
	if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_old")); n != 1 {
		t.Errorf("%d reads of the thread, want 1", n)
	}

	res := mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_nope"})
	if want := "No turn trn_nope on bot scout that you can see."; mcpErr(t, res) != want {
		t.Errorf("%q, want %q", mcpText(t, res), want)
	}
}

// ---------- (g) a thread the platform forgot ----------

func TestMCPCouncilRecreatesAForgottenThread(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	var mu sync.Mutex
	created := 0
	p.onFunc("POST", "/bots/scout/chat/threads", func(rec) (int, string) {
		mu.Lock()
		created++
		n := created
		mu.Unlock()
		return 201, chatThreadBody(fmt.Sprintf("thr_%d", n), "MCP: test fleet", "fleet")
	})
	for _, th := range []string{"thr_1", "thr_2"} {
		th := th
		p.on("POST", "/bots/scout/chat/threads/"+th+"/turns", 202, chatTurnIn("trn_1", th, "fleet"))
		p.onStream("/bots/scout/chat/threads/"+th+"/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, memberFrames(`{"summary":"ok"}`, "done", ""))
		})
	}
	// The first thread is deleted between the two asks.
	var deleted bool
	p.onFunc("GET", "/bots/scout/chat/threads/thr_1", func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if deleted {
			return 404, `{"code":"not_found","message":"no thread"}`
		}
		return 200, chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "done", "fleet"))
	})
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	mu.Lock()
	deleted = true
	mu.Unlock()
	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two"}))
	if got["threadId"] != "thr_2" {
		t.Errorf("thread %v, want the new one", got["threadId"])
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 2 {
		t.Errorf("%d threads created, want 2", n)
	}
}

// ---------- (h) a conversation that is still working ----------

func TestMCPCouncilRefusesAConversationInUse(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFrames(`{"summary":"ok"}`, "done", ""))
	})
	var deleted bool
	var mu sync.Mutex
	p.onFunc("GET", "/bots/scout/chat/threads/thr_fleet", func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if deleted {
			return 200, chatDetailBody("thr_fleet", 3, chatTurnRecord("trn_late", "thr_fleet", "running", "", "fleet"))
		}
		return 200, chatDetailBody("thr_fleet", 7, chatTurnRecord("trn_1", "thr_fleet", "done", "done", "fleet"))
	})
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	mu.Lock()
	deleted = true
	mu.Unlock()
	before := len(p.seen("POST", "/bots/scout/chat/threads/thr_fleet/turns"))
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two"})
	want := "Fleet is still working on turn trn_late in this conversation: call council_result with turn_id trn_late."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if after := len(p.seen("POST", "/bots/scout/chat/threads/thr_fleet/turns")); after != before {
		t.Errorf("a turn was posted onto a conversation in use: %d, then %d", before, after)
	}
}

// ---------- (i) the stream of a wait ----------

func TestMCPCouncilReconnectsTheStream(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	var mu sync.Mutex
	var ids []string
	all := memberFrames(`{"summary":"ok"}`, "the answer", "")
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		if conn == 1 {
			writeFrames(w, all[:3]) // the stream ends in the middle of the turn
			return
		}
		writeFrames(w, all[3:])
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "go"}))
	if got["state"] != "done" || got["final"] != "the answer" {
		t.Errorf("result %v", got)
	}
	report, _ := got["report"].(map[string]any)
	if report["summary"] != "ok" {
		t.Errorf("report %v: the report of the resumed stream's member_done", got["report"])
	}
	mu.Lock()
	resume := append([]string{}, ids...)
	mu.Unlock()
	if len(resume) < 2 || resume[1] != "3" {
		t.Errorf("Last-Event-ID per stream %q, want the resumption after seq 3", resume)
	}
}

func TestMCPCouncilAccessLost(t *testing.T) {
	councilFast(t)
	const msg = "Your access to scout was removed."
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{frameOf("", 0, "error", `"code":"access_lost","message":"`+msg+`"`)})
	})
	cs := councilHarness(t, p)

	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "go"})
	if got := mcpErr(t, res); got != msg {
		t.Errorf("%q, want %q", got, msg)
	}
}

// ---------- (j) the stream's cap ----------

func TestMCPCouncilPollsWhenTheStreamIsRefused(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":"too_many_streams","message":"Too many streams for one person."}`)
	})
	var mu sync.Mutex
	polls := 0
	p.onFunc("GET", "/bots/scout/chat/threads/thr_fleet", func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		polls++
		if polls < 2 {
			return 200, chatDetailBody("thr_fleet", 3, chatTurnRecord("trn_1", "thr_fleet", "running", "", "fleet"))
		}
		return 200, chatDetailBody("thr_fleet", 4, chatTurnRecord("trn_1", "thr_fleet", "done", "from the record", "fleet"))
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 5,
	}))
	if got["state"] != "done" || got["final"] != "from the record" {
		t.Errorf("result %v", got)
	}
	mu.Lock()
	n := polls
	mu.Unlock()
	if n < 2 {
		t.Errorf("%d polls, want the thread read until the turn ended", n)
	}
}

// ---------- (k) the refusals ----------

func TestMCPCouncilRefusesBadInput(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	cs := councilHarness(t, p)
	seconds := func(n int) any { return n }
	long := strings.Repeat("a", chatMaxPrompt+1)
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"council_info", map[string]any{"bot": ".."}, `".." is not a valid bot`},
		{"council_ask", map[string]any{"bot": "scout", "member": "Captain", "task": "x"}, `member is a member's slug, such as fleet, or <scope>/<slug> with scope bot, teamspace or template, not "Captain"`},
		{"council_ask", map[string]any{"bot": "scout", "member": "crew/fleet", "task": "x"}, `member is a member's slug, such as fleet, or <scope>/<slug> with scope bot, teamspace or template, not "crew/fleet"`},
		{"council_ask", map[string]any{"bot": "scout", "member": "teamspace/a/b", "task": "x"}, `member is a member's slug, such as fleet, or <scope>/<slug> with scope bot, teamspace or template, not "teamspace/a/b"`},
		{"council_ask", map[string]any{"bot": "scout", "member": "", "task": "x"}, `member is a member's slug, such as fleet, or <scope>/<slug> with scope bot, teamspace or template, not ""`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "roster": "Ops"}, `roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "Ops"`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "roster": "council:x"}, `roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "council:x"`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "roster": "template/ops"}, `roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "template/ops"`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "thorough": true, "mode": "quick"}, `thorough is mode council: it cannot be used with mode quick`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "thorough": true, "mode": "auto"}, `thorough is mode council: it cannot be used with mode auto`},
		{"council_ask", map[string]any{"bot": "scout", "member": "Captain", "task": "x", "effort": "max"}, `member is a member's slug, such as fleet, or <scope>/<slug> with scope bot, teamspace or template, not "Captain"`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "thorough": true, "mode": "quick", "roster": "Ops"}, `thorough is mode council: it cannot be used with mode quick`},
		{"council_roles", map[string]any{"bot": ".."}, `".." is not a valid bot`},
		{"council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "  "}, "task must not be empty"},
		{"council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": long}, fmt.Sprintf("task is longer than %d characters", chatMaxPrompt)},
		{"council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "x", "effort": "max"}, `effort must be low, medium or high, not "max"`},
		{"council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "x", "wait_seconds": seconds(56)}, "wait_seconds must be from 0 to 55"},
		{"council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "x", "wait_seconds": seconds(-1)}, "wait_seconds must be from 0 to 55"},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "mode": "slow"}, `mode must be auto, quick or council, not "slow"`},
		{"council_run", map[string]any{"bot": "scout", "question": ""}, "question must not be empty"},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "effort": "x"}, `effort must be low, medium or high, not "x"`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "wait_seconds": seconds(56)}, "wait_seconds must be from 0 to 55"},
		{"council_result", map[string]any{"bot": "scout", "turn_id": "nope"}, `"nope" is not a turn id`},
		{"council_result", map[string]any{"bot": "scout", "turn_id": "trn_"}, `"trn_" is not a turn id`},
		{"council_result", map[string]any{"bot": "scout", "turn_id": "trn_1", "wait_seconds": seconds(-1)}, "wait_seconds must be from 0 to 55"},

		// the bot and the teamspace, before anything is sent
		{"council_ask", map[string]any{"bot": "..", "member": "fleet", "task": "x"}, `".." is not a valid bot`},
		{"council_ask", map[string]any{"bot": "", "member": "fleet", "task": "x"}, `"" is not a valid bot`},
		{"council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "x", "namespace": "Bad_NS"}, `namespace "Bad_NS" is not a teamspace name`},
		{"council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "x", "namespace": strings.Repeat("a", 64)}, fmt.Sprintf("namespace %q is not a teamspace name", strings.Repeat("a", 64))},
		{"council_run", map[string]any{"bot": "..", "question": "x"}, `".." is not a valid bot`},
		{"council_run", map[string]any{"bot": "scout", "question": "x", "namespace": "Bad_NS"}, `namespace "Bad_NS" is not a teamspace name`},
		{"council_result", map[string]any{"bot": "..", "turn_id": "trn_1"}, `".." is not a valid bot`},
		{"council_result", map[string]any{"bot": "scout", "turn_id": "trn_1", "namespace": "Bad_NS"}, `namespace "Bad_NS" is not a teamspace name`},
	} {
		res := mcpCall(t, cs, c.tool, c.args)
		if got := mcpErr(t, res); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.tool, c.args, got, c.want)
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("a refused input sent %d request(s)", n)
	}
}

// ---------- (l) wait_seconds 0 ----------

func TestMCPCouncilWaitZeroDoesNotWait(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 0,
	}))
	if got["state"] != "queued" {
		t.Errorf("state %v, want queued", got["state"])
	}
	if next, _ := got["next"].(string); !strings.Contains(next, "call council_result") {
		t.Errorf("next %q", next)
	}
	p.mu.Lock()
	conns := p.conns["/bots/scout/chat/threads/thr_fleet/events"]
	p.mu.Unlock()
	if conns != 0 {
		t.Errorf("%d stream connections, want none", conns)
	}
}

// ---------- (m) a call the agent cancelled ----------

func TestMCPCouncilReturnsWhenTheCallIsCancelled(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	started := make(chan struct{}, 1)
	streamEnded := make(chan struct{})
	var endedOnce sync.Once
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		started <- struct{}{}
		holdStream(w, r, []string{frame(1, "turn_start", `"mode":"member"`)})
		endedOnce.Do(func() { close(streamEnded) })
	})
	cs := councilHarness(t, p)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "council_ask",
			Arguments: map[string]any{"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 55}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the call did not return when it was cancelled")
	}
	// The wait really ended: the server's held stream saw its request context end.
	select {
	case <-streamEnded:
	case <-time.After(time.Second):
		t.Fatal("the server's stream is still open a second after the call was cancelled")
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_fleet/turns/trn_1/cancel")); n != 0 {
		t.Errorf("%d cancels: the tool must never cancel the turn on the bot", n)
	}
}

// ---------- (n) council_run's answer ----------

func TestMCPCouncilRun(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_council", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_council/turns", 202, chatTurnIn("trn_run", "thr_council", ""))
	p.onStream("/bots/scout/chat/threads/thr_council/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_run", 1, "turn_queued", `"index":1,"kind":"ask","member":null`),
			frameOf("trn_run", 2, "turn_start", `"mode":"council"`),
			frameOf("trn_run", 3, "plan", `"mode":"council","tasks":[{"member":"fleet","task":"look"},{"member":"coder","task":"check"}]`),
			frameOf("trn_run", 4, "member_done", `"member":"fleet","task":0,"report":{"summary":"two robots","findings":[],"proposals":[],"confidence":"high","open_questions":[]}`),
			frameOf("trn_run", 5, "member_done", `"member":"coder","task":1,"report":{"state":"done","text":"all green"}`),
			frameOf("trn_run", 6, "critique", `"issues":[{"member":"fleet","severity":"minor","claim":"x","why":"y"}],"note":"one issue"`),
			frameOf("trn_run", 7, "final", `"text":"## Answer\neverything is fine"`),
			frameOf("trn_run", 8, "usage", `"calls":3,"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"ms":900`),
			frameOf("trn_run", 9, "turn_end", `"state":"done"`),
		})
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{
		"bot": "scout", "question": "status?", "mode": "council", "effort": "high",
	}))
	if got["state"] != "done" || got["answer"] != "## Answer\neverything is fine" {
		t.Errorf("result %v", got)
	}
	if _, ok := got["member"]; ok {
		t.Errorf("a council turn names one member: %v", got["member"])
	}
	members, _ := got["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members %v", got["members"])
	}
	first, _ := members[0].(map[string]any)
	second, _ := members[1].(map[string]any)
	if first["member"] != "fleet" || first["task"] != float64(0) || first["summary"] != "two robots" ||
		second["member"] != "coder" || second["task"] != float64(1) || second["summary"] != "all green" {
		t.Errorf("members %v", members)
	}
	critique, _ := got["critique"].(map[string]any)
	if critique["note"] != "one issue" {
		t.Errorf("critique %v", got["critique"])
	}
	if _, ok := critique["turnId"]; ok {
		t.Error("the critique carries the frame's turnId")
	}
	if _, ok := critique["at"]; ok {
		t.Error("the critique carries the frame's at")
	}
	usage, _ := got["usage"].(map[string]any)
	if usage["calls"] != float64(3) {
		t.Errorf("usage %v", got["usage"])
	}
	if _, ok := usage["at"]; ok {
		t.Errorf("the usage carries the frame's at: %v", usage)
	}
	if next, ok := got["next"]; ok {
		t.Errorf("a turn that ended carries next: %v", next)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(p.seen("POST", "/bots/scout/chat/threads/thr_council/turns")[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["mode"] != "council" || body["effort"] != "high" || body["prompt"] != "status?" {
		t.Errorf("turn body %s", p.seen("POST", "/bots/scout/chat/threads/thr_council/turns")[0].Body)
	}
}

// A final the platform shortened is not the answer: the turn's own record has it whole, and
// councilFinish reads it from there.
func TestMCPCouncilRunCutFinalComesFromTheThread(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_council", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_council/turns", 202, chatTurnIn("trn_run", "thr_council", ""))
	p.onStream("/bots/scout/chat/threads/thr_council/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_run", 1, "turn_queued", `"index":1,"kind":"ask","member":null`),
			frameOf("trn_run", 2, "turn_start", `"mode":"council"`),
			frameOf("trn_run", 3, "final", `"text":"the whole…","cut":true,"type":"final","bytes":40960`),
			frameOf("trn_run", 4, "turn_end", `"state":"done"`),
		})
	})
	p.on("GET", "/bots/scout/chat/threads/thr_council", 200, chatDetailBody("thr_council", 4,
		chatTurnRecord("trn_run", "thr_council", "done", "the whole answer", "")))
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}))
	if got["state"] != "done" {
		t.Errorf("state %v", got["state"])
	}
	if want := "the whole answer"; got["answer"] != want {
		t.Errorf("answer %v, want %q (the record's whole answer)", got["answer"], want)
	}
}

// A shortened final of a turn that did not end done is not the answer either: councilFinish reads the
// thread's record, which has it whole.
func TestMCPCouncilRunCutFinalOnAFailedTurn(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_council", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_council/turns", 202, chatTurnIn("trn_run", "thr_council", ""))
	p.onStream("/bots/scout/chat/threads/thr_council/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_run", 1, "turn_start", `"mode":"council"`),
			frameOf("trn_run", 2, "final", `"text":"the whole…","cut":true,"type":"final","bytes":40960`),
			frameOf("trn_run", 3, "turn_end", `"state":"failed"`),
		})
	})
	p.on("GET", "/bots/scout/chat/threads/thr_council", 200, chatDetailBody("thr_council", 3,
		chatTurnRecord("trn_run", "thr_council", "failed", "the whole answer", "")))
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}))
	if got["state"] != "failed" {
		t.Errorf("state %v", got["state"])
	}
	if want := "the whole answer"; got["answer"] != want {
		t.Errorf("answer %v, want %q (the record's whole answer)", got["answer"], want)
	}
	if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_council")); n != 1 {
		t.Errorf("%d reads of the thread, want 1", n)
	}
}

// council_run's members carry the Coder's own report as a summary: the start of its answer, and how
// its task ended when it did not finish. Fleet's entries are unchanged.
func TestMCPCouncilRunCoderSummary(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_council", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_council/turns", 202, chatTurnIn("trn_run", "thr_council", ""))
	p.onStream("/bots/scout/chat/threads/thr_council/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_run", 1, "turn_start", `"mode":"council"`),
			frameOf("trn_run", 2, "member_done", `"member":"fleet","task":0,"report":{"summary":"two robots","findings":[],"proposals":[],"confidence":"high","open_questions":[]}`),
			frameOf("trn_run", 3, "member_done", `"member":"coder","task":1,"report":{"state":"done","reason":"","text":"did X","changed":[],"steps":2,"tools":1}`),
			frameOf("trn_run", 4, "member_done", `"member":"coder","task":2,"report":{"state":"failed","reason":"timeout","text":"still going"}`),
			frameOf("trn_run", 5, "member_done", `"member":"coder","task":3,"report":{"state":"done","reason":"","text":"cut\u001b[31m report","changed":["a"],"cut":true}`),
			frameOf("trn_run", 6, "final", `"text":"done"`),
			frameOf("trn_run", 7, "turn_end", `"state":"done"`),
		})
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}))
	members, _ := got["members"].([]any)
	want := []struct {
		member  string
		task    int
		summary string
	}{
		{"fleet", 0, "two robots"},
		{"coder", 1, "did X"},
		{"coder", 2, "still going (failed: timeout)"},
		{"coder", 3, "cut report"},
	}
	if len(members) != len(want) {
		t.Fatalf("members %v, want %d", got["members"], len(want))
	}
	for i, w := range want {
		m, _ := members[i].(map[string]any)
		if m["member"] != w.member || m["task"] != float64(w.task) || m["summary"] != w.summary {
			t.Errorf("member %d %v, want %s/%d/%q", i, m, w.member, w.task, w.summary)
		}
	}
}

// A Coder report's summary is the start of its answer: cleaned, one line, 1000 characters at most,
// and with its state when the task did not finish.
func TestCouncilCoderSummary(t *testing.T) {
	long := strings.Repeat("x", councilSummaryRunes+1)
	cases := []struct {
		name   string
		report string
		want   string
	}{
		{"done", `{"state":"done","reason":"","text":"did X"}`, "did X"},
		{"a control character", `{"state":"done","text":"did\u0007 X"}`, "did X"},
		{"one line", `{"state":"done","text":"did X\n\n"}`, "did X"},
		{"no newlines or tabs", `{"state":"done","text":"line one\n\tline two"}`, "line one line two"},
		{"failed with a reason", `{"state":"failed","reason":"timeout","text":"half"}`, "half (failed: timeout)"},
		{"failed without a reason", `{"state":"failed","text":"half"}`, "half (failed)"},
		{"failed without text", `{"state":"failed","reason":"timeout"}`, "(failed: timeout)"},
		{"failed with the report's error", `{"state":"failed","error":"disk full"}`, "disk full (failed)"},
		{"the 1000 characters", `{"state":"done","text":` + jsonText(strings.Repeat("x", councilSummaryRunes)) + `}`, strings.Repeat("x", councilSummaryRunes)},
		{"cut to 1000 characters", `{"state":"done","text":` + jsonText(long) + `}`, long[:councilSummaryRunes] + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := councilMemberSummary("coder", json.RawMessage(c.report))
			if got != c.want {
				t.Errorf("summary %q, want %q", got, c.want)
			}
			if strings.ContainsAny(got, "\n\t") {
				t.Errorf("summary %q: a newline or a tab reached the members list", got)
			}
		})
	}
	// Fleet's report keeps its own summary.
	if got := councilMemberSummary("fleet", json.RawMessage(`{"summary":"two robots","confidence":"high"}`)); got != "two robots" {
		t.Errorf("Fleet's summary %q", got)
	}
}

// ---------- (o) a turn that failed is a normal result ----------

func TestMCPCouncilFailedTurnIsAResult(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "error", `"code":"member_failed","message":"The Coder ran out of time."`),
			frame(2, "turn_end", `"state":"failed"`),
		})
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "go"}))
	if got["state"] != "failed" {
		t.Errorf("state %v", got["state"])
	}
	turnErr, _ := got["error"].(map[string]any)
	if turnErr["code"] != "member_failed" || turnErr["message"] != "The Coder ran out of time." {
		t.Errorf("error %v", got["error"])
	}
}

// A done turn carries no error: the mid-turn error event it emitted (too_many_events) is history.
func TestMCPCouncilDoneTurnDropsAnEarlierError(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_council", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_council/turns", 202, chatTurnIn("trn_run", "thr_council", ""))
	p.onStream("/bots/scout/chat/threads/thr_council/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_run", 1, "error", `"code":"too_many_events","message":"more than 3000 events: the turn goes on without them"`),
			frameOf("trn_run", 2, "final", `"text":"the answer"`),
			frameOf("trn_run", 3, "turn_end", `"state":"done"`),
		})
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}))
	if got["state"] != "done" || got["answer"] != "the answer" {
		t.Errorf("result %v", got)
	}
	if _, ok := got["error"]; ok {
		t.Errorf("a done turn carries an error: %v", got["error"])
	}
}

// A failed turn keeps the last error it reported, not the first.
func TestMCPCouncilFailedTurnKeepsTheLastError(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_council", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_council/turns", 202, chatTurnIn("trn_run", "thr_council", ""))
	p.onStream("/bots/scout/chat/threads/thr_council/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_run", 1, "error", `"code":"too_many_events","message":"more than 3000 events: the turn goes on without them"`),
			frameOf("trn_run", 2, "error", `"code":"timeout","message":"the turn ran past its deadline"`),
			frameOf("trn_run", 3, "turn_end", `"state":"failed"`),
		})
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}))
	if got["state"] != "failed" {
		t.Errorf("state %v", got["state"])
	}
	turnErr, _ := got["error"].(map[string]any)
	if turnErr["code"] != "timeout" {
		t.Errorf("error %v, want the last one (timeout)", got["error"])
	}
}

// ---------- (p) a person only ----------

func TestMCPCouncilRefusedInBotMode(t *testing.T) {
	p := newBotAPI(t)
	botEnv(t, p)
	useDefaultEnv(t, &cliEnv{
		fleet:       func() (*api.Fleet, string, string, error) { return nil, "", "", errors.New("the command ran") },
		domainFleet: func() (*api.Fleet, string, error) { return nil, "", errors.New("the command ran") },
	})
	_, _, err := runRealRoot(t, "mcp", "--council")
	if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "--council is for a person's own agent: a TeknoirBot cannot drive a council") {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if n := p.count(); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// ---------- (q) a stream the platform refuses ----------

func TestMCPCouncilRefusedStreamIsAnErrorAtOnce(t *testing.T) {
	for _, c := range []struct {
		name    string
		respond func(w http.ResponseWriter, r *http.Request)
		want    string
		exact   bool
	}{
		{"deleted", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","message":"no thread"}`)
		}, "The conversation was deleted.", true},
		{"not allowed", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"code":"chat_not_allowed","message":"You may not chat with this bot."}`)
		}, "You may not chat with this bot.", false},
		{"login redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://teknoir.cloud/oauth2/start?rd=x", http.StatusFound)
		}, "tnctl login", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			councilFast(t)
			p := newChatAPI(t)
			p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
			p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
			p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
				c.respond(w, r)
			})
			cs := councilHarness(t, p)

			res := mcpCall(t, cs, "council_ask", map[string]any{
				"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 2,
			})
			got := mcpErr(t, res)
			if c.exact && got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
			if !c.exact && !strings.Contains(got, c.want) {
				t.Errorf("%q, want it to say %q", got, c.want)
			}
			p.mu.Lock()
			conns := p.conns["/bots/scout/chat/threads/thr_fleet/events"]
			p.mu.Unlock()
			if conns != 1 {
				t.Errorf("%d stream connections, want 1: a refusal is not retried", conns)
			}
		})
	}
}

func TestMCPCouncilDeletedStreamIsAnError(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{frameOf("", 0, "error", `"code":"not_found","message":"no thread"`)})
	})
	cs := councilHarness(t, p)

	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 2})
	if got := mcpErr(t, res); got != "The conversation was deleted." {
		t.Errorf("%q, want the deleted conversation", got)
	}
	if n := p.countAll(); n != 3 {
		t.Errorf("%d requests, want 3 (thread, turn, stream): the stream's not_found is not retried", n)
	}
}

func TestMCPCouncilReconnectsAfterStreamErrors(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	var mu sync.Mutex
	var ids []string
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		switch conn {
		case 1:
			// The turn starts, then the stream says it is broken: connect again after the frame.
			writeFrames(w, []string{frame(1, "turn_start", `"mode":"member"`),
				frameOf("", 2, "error", `"code":"internal","message":"boom"`)})
		case 2:
			// The stream has run its hour: connect again after that frame too.
			writeFrames(w, []string{frameOf("", 3, "error", `"code":"stream_expired","message":"the stream has run its hour"`)})
		default:
			writeFrames(w, []string{frame(4, "final", `"text":"after two reconnects"`), frame(5, "turn_end", `"state":"done"`)})
		}
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "go"}))
	if got["state"] != "done" || got["final"] != "after two reconnects" {
		t.Errorf("result %v", got)
	}
	mu.Lock()
	resume := append([]string{}, ids...)
	mu.Unlock()
	if len(resume) != 3 || resume[0] != "" || resume[1] != "2" || resume[2] != "3" {
		t.Errorf("Last-Event-ID per stream %q, want [\"\", \"2\", \"3\"]", resume)
	}
}

func TestMCPCouncilExpiredSessionIsALoginError(t *testing.T) {
	councilFast(t)
	noBotMode(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	e := p.env(t)
	fl, domain, ns, _ := e.fleet()
	var mu sync.Mutex
	expired := false
	tok := fl.Token
	fl.Token = func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if expired {
			return "", fmt.Errorf("refreshing the login: %w", auth.ErrSessionExpired)
		}
		return tok(ctx)
	}
	e.fleet = func() (*api.Fleet, string, string, error) { return fl, domain, ns, nil }
	cs := mcpClient(t, newMCPServerWith(e, true))

	if res := mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 0,
	}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	mu.Lock()
	expired = true
	mu.Unlock()
	res := mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_1", "wait_seconds": 5})
	if got := mcpErr(t, res); got != "Your tnctl login has expired. Run: tnctl login" {
		t.Errorf("%q, want the login error", got)
	}
}

// ---------- (r) two calls for one member ----------

func TestMCPCouncilSerializesTwoAsksForAMember(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	release := make(chan struct{})
	p.onFunc("POST", "/bots/scout/chat/threads", func(rec) (int, string) {
		<-release // hold the first ask inside its start until the second has been answered
		return 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet")
	})
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFrames(`{"summary":"ok"}`, "done", ""))
	})
	cs := councilHarness(t, p)

	done := make(chan *mcp.CallToolResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "council_ask",
				Arguments: map[string]any{"bot": "scout", "member": "fleet", "task": "go"}})
			done <- res
		}()
	}
	var first *mcp.CallToolResult
	select {
	case first = <-done:
	case <-time.After(5 * time.Second):
	}
	close(release)
	if first == nil {
		t.Fatal("neither ask was refused while the other was starting its turn")
	}
	second := <-done

	busy, ok := first, true
	if !first.IsError {
		busy, ok = second, second.IsError
	}
	if !ok {
		t.Fatalf("both asks went through: %s and %s", mcpText(t, first), mcpText(t, second))
	}
	msg := mcpText(t, busy)
	if want := "The member fleet is still working on a turn in this conversation: wait for it, then ask again."; msg != want &&
		!strings.Contains(msg, "still working on turn trn_1") {
		t.Errorf("%q, want the busy error of the first ask", msg)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
		t.Errorf("%d threads created, want 1: the second ask must not post", n)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_fleet/turns")); n != 1 {
		t.Errorf("%d turns posted, want 1", n)
	}
}

// A start held in its read of the thread refuses a second start on the conversation with the member's
// name this process already knows (beginStart's in-flight branch).
func TestMCPCouncilInFlightBusyNamesTheMember(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.onSeq("POST", "/bots/scout/chat/threads/thr_fleet/turns",
		chatReply{202, chatTurnIn("trn_1", "thr_fleet", "fleet")},
		chatReply{202, chatTurnIn("trn_2", "thr_fleet", "fleet")})
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	held := make(chan struct{}, 1)
	release := make(chan struct{})
	p.onFunc("GET", "/bots/scout/chat/threads/thr_fleet", func(rec) (int, string) {
		select {
		case held <- struct{}{}:
		default:
		}
		select {
		case <-release: // hold the second ask in its read of the thread while the third arrives
		case <-time.After(5 * time.Second):
		}
		return 200, chatDetailBody("thr_fleet", 7, chatTurnRecord("trn_1", "thr_fleet", "done", "ok", "fleet"))
	})
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	second := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "council_ask",
			Arguments: map[string]any{"bot": "scout", "member": "fleet", "task": "two", "wait_seconds": 0}})
		second <- res
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the second ask never read its thread")
	}
	before := p.countAll()
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "three", "wait_seconds": 0})
	// Counted while the second ask is still held: once released, its P6 is a request of its own.
	after := p.countAll()
	close(release)
	if want := "Fleet is still working on a turn in this conversation: wait for it, then ask again."; mcpErr(t, res) != want {
		t.Errorf("third ask: %q, want %q", mcpText(t, res), want)
	}
	if after != before {
		t.Errorf("the refused ask sent %d requests, want none", after-before)
	}
	if r := <-second; r == nil || r.IsError {
		t.Errorf("the second ask failed: %v", r)
	}
}

func TestMCPCouncilRefusesASecondAskOnItsOwnTurn(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnInState("trn_1", "thr_fleet", "fleet", "running"))
	// What the platform would say, for a call that got as far as reading the thread.
	p.on("GET", "/bots/scout/chat/threads/thr_fleet", 200,
		chatDetailBody("thr_fleet", 3, chatTurnRecord("trn_1", "thr_fleet", "running", "", "fleet")))
	cs := councilHarness(t, p)

	first := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "one", "wait_seconds": 0,
	}))
	if first["state"] != "running" {
		t.Fatalf("first ask %v", first)
	}
	before := p.countAll()
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two"})
	want := "Fleet is still working on turn trn_1 in this conversation: call council_result with turn_id trn_1."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if after := p.countAll(); after != before {
		t.Errorf("%d requests after the refusal, want none", after-before)
	}
}

func TestMCPCouncilTurnOpenNamesTheTurn(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 409, `{"code":"turn_open","message":"a turn is in progress"}`)
	p.on("GET", "/bots/scout/chat/threads/thr_fleet", 200,
		chatDetailBody("thr_fleet", 4, chatTurnRecord("trn_9", "thr_fleet", "running", "", "fleet")))
	cs := councilHarness(t, p)

	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "go"})
	want := "Fleet is still working on turn trn_9 in this conversation: call council_result with turn_id trn_9."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// ---------- (s) two waiters on one turn ----------

func TestMCPCouncilTwoWaitersCountEachEventOnce(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_council", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_council/turns", 202, chatTurnIn("trn_run", "thr_council", ""))
	opened := make(chan struct{}, 2)
	release := make(chan struct{})
	events := []string{
		frameOf("trn_run", 2, "member_done", `"member":"fleet","task":0,"report":{"summary":"fleet says"}`),
		frameOf("trn_run", 3, "member_done", `"member":"coder","task":1,"report":{"state":"done","text":"coder says"}`),
	}
	p.onStream("/bots/scout/chat/threads/thr_council/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		// Both connections send the same two events: the second waiter must not count them again.
		writeFrames(w, events)
		opened <- struct{}{}
		<-release
		for _, f := range []string{
			frameOf("trn_run", 4, "final", `"text":"both said so"`),
			frameOf("trn_run", 5, "turn_end", `"state":"done"`),
		} {
			_, _ = io.WriteString(w, f)
		}
		w.(http.Flusher).Flush()
	})
	cs := councilHarness(t, p)

	type answer struct {
		tool string
		res  *mcp.CallToolResult
	}
	done := make(chan answer, 2)
	go func() {
		res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "council_run",
			Arguments: map[string]any{"bot": "scout", "question": "status?", "wait_seconds": 55}})
		done <- answer{"council_run", res}
	}()
	<-opened // the run's stream is open
	go func() {
		res, _ := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "council_result",
			Arguments: map[string]any{"bot": "scout", "turn_id": "trn_run", "wait_seconds": 55}})
		done <- answer{"council_result", res}
	}()
	<-opened // and the result's
	close(release)
	for i := 0; i < 2; i++ {
		a := <-done
		got := mcpMap(t, a.res)
		members, _ := got["members"].([]any)
		if len(members) != 2 {
			t.Errorf("%s: members %v, want the 2 member_done events once each", a.tool, got["members"])
		}
		if got["answer"] != "both said so" {
			t.Errorf("%s: answer %v", a.tool, got["answer"])
		}
	}
}

// ---------- (t) the polling fallback keeps the stream's place ----------

func TestMCPCouncilPollingDoesNotSkipEvents(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	var mu sync.Mutex
	var ids []string
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		if conn == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":"too_many_streams","message":"Too many streams for one person."}`)
			return
		}
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		writeFrames(w, []string{
			frame(2, "member_done", `"member":"fleet","task":0,"report":{"summary":"seen at two"}`),
			frame(3, "final", `"text":"the end"`),
			frame(4, "turn_end", `"state":"done"`),
		})
	})
	// The record is two events ahead, and the poll must not move the turn there: event 2 is a
	// member_done a later stream still has to deliver.
	p.on("GET", "/bots/scout/chat/threads/thr_fleet", 200,
		chatDetailBody("thr_fleet", 2, chatTurnRecord("trn_1", "thr_fleet", "running", "", "fleet")))
	cs := councilHarness(t, p)

	first := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 1,
	}))
	if first["state"] != "running" {
		t.Fatalf("first ask %v, want the turn still running", first)
	}
	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_1", "wait_seconds": 5}))
	if got["state"] != "done" || got["final"] != "the end" {
		t.Errorf("result %v", got)
	}
	report, _ := got["report"].(map[string]any)
	if report["summary"] != "seen at two" {
		t.Errorf("report %v: the stream resumed past event 2", got["report"])
	}
	mu.Lock()
	resume := append([]string{}, ids...)
	mu.Unlock()
	if len(resume) != 1 {
		t.Fatalf("Last-Event-ID per stream %q, want one resumption", resume)
	}
	if resume[0] != "" && resume[0] != "1" {
		t.Errorf("Last-Event-ID %q: the poll moved the turn past event 2", resume[0])
	}
}

// ---------- (u) the turns this process remembers ----------

func TestMCPCouncilTurnCache(t *testing.T) {
	// The same turn added twice is one entry, and the turn already known comes back.
	st := newCouncilState()
	a := &councilTurn{id: "trn_a", state: "running"}
	if got := st.addTurn(a); got != a {
		t.Fatalf("addTurn returned %v, want the turn it was given", got)
	}
	if got := st.addTurn(&councilTurn{id: "trn_a", state: "done"}); got != a {
		t.Errorf("addTurn returned %v, want the turn it already knew", got)
	}
	if len(st.turns) != 1 || len(st.order) != 1 {
		t.Errorf("%d turns and %d positions after two adds of one id", len(st.turns), len(st.order))
	}

	// Alternating ids do not grow the map.
	st = newCouncilState()
	for i := 0; i < 70; i++ {
		st.addTurn(&councilTurn{id: fmt.Sprintf("trn_%d", i%2), state: "running"})
	}
	if len(st.turns) > councilMaxTurns || len(st.order) != len(st.turns) {
		t.Errorf("%d turns, %d positions for 70 adds of 2 ids", len(st.turns), len(st.order))
	}

	// Distinct ids stop at the cap, without a duplicate position.
	st = newCouncilState()
	for i := 0; i < 70; i++ {
		st.addTurn(&councilTurn{id: fmt.Sprintf("trn_run_%d", i), state: "running"})
	}
	if len(st.turns) != councilMaxTurns || len(st.order) != councilMaxTurns {
		t.Errorf("%d turns, %d positions, want %d of each", len(st.turns), len(st.order), councilMaxTurns)
	}
	seen := map[string]bool{}
	for _, id := range st.order {
		if seen[id] {
			t.Errorf("%s is in the order twice", id)
		}
		seen[id] = true
	}

	// A full map drops a finished turn before a running one.
	st = newCouncilState()
	for i := 0; i < councilMaxTurns-1; i++ {
		st.addTurn(&councilTurn{id: fmt.Sprintf("trn_run_%d", i), state: "running"})
	}
	st.addTurn(&councilTurn{id: "trn_fin", state: "done"})
	st.addTurn(&councilTurn{id: "trn_new", state: "running"})
	if st.turns["trn_fin"] != nil {
		t.Error("the finished turn was kept and a running one dropped")
	}
	if st.turns["trn_run_0"] == nil || st.turns["trn_new"] == nil {
		t.Error("the eviction took a running turn")
	}
}

func TestMCPCouncilClientName(t *testing.T) {
	req := func(name string) *mcp.CallToolRequest {
		return &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{
			Meta: mcp.Meta{mcp.MetaKeyClientInfo: &mcp.Implementation{Name: name}},
		}}
	}
	for _, c := range []struct{ name, want string }{
		{"Claude Code", "Claude Code"},
		{"", "agent"},
		{" \n\r\t\x00\x07 ", "agent"},
		{"evil\x00\r\nname", "evilname"},
		{"padded  ", "padded"},
		{strings.Repeat("å", 70), strings.Repeat("å", 60)},
	} {
		if got := mcpCouncilClientName(req(c.name)); got != c.want {
			t.Errorf("name %q: %q, want %q", c.name, got, c.want)
		}
	}
	if got := mcpCouncilClientName(nil); got != "agent" {
		t.Errorf("no request: %q, want agent", got)
	}
}

// ---------- (v) the turn of another call ----------

func TestMCPCouncilResultSearchesKnownThreadsFirst(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFrames(`{"summary":"ok"}`, "done", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_fleet", 200, chatDetailBody("thr_fleet", 9,
		chatTurnRecord("trn_1", "thr_fleet", "done", "done", "fleet")+`,`+
			chatTurnRecord("trn_old", "thr_fleet", "done", "found in the known thread", "coder")))
	// The bot's own list would answer too, if it were asked for.
	p.on("GET", "/bots/scout/chat/threads", 200,
		`{"items":[{"id":"thr_other","title":"other","origin":"web","member":null,"author":{"ref":"r"},`+
			`"createdAt":1,"updatedAt":2,"turnCount":0,"lastTurn":null}]}`)
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "one", "wait_seconds": 0,
	}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_old"}))
	if got["state"] != "done" || got["final"] != "found in the known thread" || got["threadId"] != "thr_fleet" {
		t.Errorf("result %v", got)
	}
	if n := len(p.seen("GET", "/bots/scout/chat/threads")); n != 0 {
		t.Errorf("%d thread-list reads, want none: the thread this process knows has the turn", n)
	}
}

func TestMCPCouncilResultSearchesThreadsNewestFirst(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200,
		`{"items":[`+
			`{"id":"thr_new","title":"n","origin":"web","member":null,"author":{"ref":"r"},"createdAt":1,"updatedAt":9,"turnCount":0,"lastTurn":null},`+
			`{"id":"thr_mid","title":"m","origin":"web","member":null,"author":{"ref":"r"},"createdAt":1,"updatedAt":8,"turnCount":0,"lastTurn":null},`+
			`{"id":"thr_old","title":"o","origin":"web","member":null,"author":{"ref":"r"},"createdAt":1,"updatedAt":7,"turnCount":0,"lastTurn":null}]}`)
	p.on("GET", "/bots/scout/chat/threads/thr_new", 200,
		chatDetailBody("thr_new", 3, chatTurnRecord("trn_a", "thr_new", "done", "the newest answer", "fleet")))
	p.on("GET", "/bots/scout/chat/threads/thr_mid", 200,
		chatDetailBody("thr_mid", 5, chatTurnRecord("trn_b", "thr_mid", "done", "the middle answer", "coder")))
	p.on("GET", "/bots/scout/chat/threads/thr_old", 200,
		chatDetailBody("thr_old", 7, chatTurnRecord("trn_c", "thr_old", "done", "the oldest answer", "fleet")))
	cs := mcpClient(t, newMCPServerWith(p.env(t), true))

	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_b"}))
	if got["state"] != "done" || got["final"] != "the middle answer" || got["threadId"] != "thr_mid" {
		t.Errorf("result %v", got)
	}
	for _, th := range []struct {
		id, want string
	}{{"thr_new", "1"}, {"thr_mid", "1"}, {"thr_old", "0"}} {
		if n := fmt.Sprint(len(p.seen("GET", "/bots/scout/chat/threads/"+th.id))); n != th.want {
			t.Errorf("%s read %s times, want %s", th.id, n, th.want)
		}
	}
}

func TestMCPCouncilResultReadsAtMostTenThreads(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	var items []string
	for i := 1; i <= 12; i++ {
		items = append(items, fmt.Sprintf(`{"id":"thr_%02d","title":"t","origin":"web","member":null,"author":{"ref":"r"},`+
			`"createdAt":1,"updatedAt":%d,"turnCount":0,"lastTurn":null}`, i, 100-i))
	}
	p.on("GET", "/bots/scout/chat/threads", 200, `{"items":[`+strings.Join(items, ",")+`]}`)
	for i := 1; i <= 12; i++ {
		p.on("GET", fmt.Sprintf("/bots/scout/chat/threads/thr_%02d", i), 200,
			chatDetailBody(fmt.Sprintf("thr_%02d", i), 0, ""))
	}
	cs := mcpClient(t, newMCPServerWith(p.env(t), true))

	res := mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_gone"})
	if want := "No turn trn_gone on bot scout that you can see."; mcpErr(t, res) != want {
		t.Errorf("%q, want %q", mcpText(t, res), want)
	}
	if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_10")); n != 1 {
		t.Errorf("the 10th thread was read %d times, want 1", n)
	}
	for _, i := range []int{11, 12} {
		if n := len(p.seen("GET", fmt.Sprintf("/bots/scout/chat/threads/thr_%02d", i))); n != 0 {
			t.Errorf("thread %d was read %d times: the search stops at 10", i, n)
		}
	}
}

func TestMCPCouncilResultWaitsForAnUnknownRunningTurn(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200,
		`{"items":[{"id":"thr_live","title":"live","origin":"web","member":null,"author":{"ref":"r"},`+
			`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":{"id":"trn_live","state":"running"}}]}`)
	p.on("GET", "/bots/scout/chat/threads/thr_live", 200,
		chatDetailBody("thr_live", 5, chatTurnRecord("trn_live", "thr_live", "running", "", "fleet")))
	var mu sync.Mutex
	var ids []string
	p.onStream("/bots/scout/chat/threads/thr_live/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		writeFrames(w, []string{
			frameOf("trn_live", 6, "final", `"text":"late answer"`),
			frameOf("trn_live", 7, "turn_end", `"state":"done"`),
		})
	})
	cs := mcpClient(t, newMCPServerWith(p.env(t), true))

	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{
		"bot": "scout", "turn_id": "trn_live", "wait_seconds": 5,
	}))
	if got["state"] != "done" || got["final"] != "late answer" || got["threadId"] != "thr_live" {
		t.Errorf("result %v", got)
	}
	mu.Lock()
	resume := append([]string{}, ids...)
	mu.Unlock()
	if len(resume) != 1 || resume[0] != "5" {
		t.Errorf("Last-Event-ID per stream %q, want [\"5\"]: the unknown turn waits from the record", resume)
	}
}

// ---------- (w) a thread deleted between the read and the post ----------

// postGoneFake is a fake chat API whose thread thr_1 answers reads but refuses every post after the
// first: the platform deleted it between the read and the post. With secondToo, thr_2 refuses too.
type postGoneFake struct {
	p       *chatAPI
	mu      sync.Mutex
	created int
	first   int // posts onto thr_1
	second  int // posts onto thr_2
	gone    bool
}

func newPostGoneFake(t *testing.T, secondToo bool) *postGoneFake {
	t.Helper()
	f := &postGoneFake{p: newChatAPI(t)}
	p := f.p
	p.onFunc("POST", "/bots/scout/chat/threads", func(rec) (int, string) {
		f.mu.Lock()
		f.created++
		n := f.created
		f.mu.Unlock()
		return 201, chatThreadBody(fmt.Sprintf("thr_%d", n), "MCP: test fleet", "fleet")
	})
	p.onFunc("POST", "/bots/scout/chat/threads/thr_1/turns", func(rec) (int, string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.first++
		if f.gone {
			return 404, `{"code":"not_found","message":"no thread"}`
		}
		return 202, chatTurnIn("trn_1", "thr_1", "fleet")
	})
	p.onFunc("POST", "/bots/scout/chat/threads/thr_2/turns", func(rec) (int, string) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.second++
		if secondToo {
			return 404, `{"code":"not_found","message":"no thread"}`
		}
		return 202, chatTurnIn("trn_2", "thr_2", "fleet")
	})
	p.onFunc("GET", "/bots/scout/chat/threads/thr_1", func(rec) (int, string) {
		return 200, chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "first", "fleet"))
	})
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "first", ""))
	})
	p.onStream("/bots/scout/chat/threads/thr_2/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_2", "fleet", `{"summary":"ok"}`, "second", ""))
	})
	return f
}

func TestMCPCouncilPostNotFoundMakesAnotherThreadOnce(t *testing.T) {
	councilFast(t)
	f := newPostGoneFake(t, false)
	cs := councilHarness(t, f.p)

	first := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}))
	if first["threadId"] != "thr_1" || first["final"] != "first" {
		t.Fatalf("first ask %v", first)
	}
	f.mu.Lock()
	f.gone = true
	f.mu.Unlock()
	second := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two"}))
	if second["threadId"] != "thr_2" || second["turnId"] != "trn_2" || second["final"] != "second" {
		t.Errorf("second ask %v, want the new thread's turn", second)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != 2 {
		t.Errorf("%d threads created, want 2", f.created)
	}
	if f.first != 2 {
		t.Errorf("%d posts onto the deleted thread, want 2: the first and one retry", f.first)
	}
	if f.second != 1 {
		t.Errorf("%d posts onto the new thread, want 1", f.second)
	}
}

func TestMCPCouncilPostNotFoundTwiceIsAToolError(t *testing.T) {
	councilFast(t)
	f := newPostGoneFake(t, true)
	cs := councilHarness(t, f.p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	f.mu.Lock()
	f.gone = true
	f.mu.Unlock()
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two"})
	if got := mcpErr(t, res); !strings.Contains(got, "no thread") {
		t.Errorf("%q, want the platform's not_found", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created != 2 {
		t.Errorf("%d threads created, want 2: a second not_found makes no third thread", f.created)
	}
	if f.first != 2 || f.second != 1 {
		t.Errorf("%d posts onto the deleted thread and %d onto the new one, want 2 and 1", f.first, f.second)
	}
}

// ---------- (x) one failed read of the polling fallback ----------

func TestMCPCouncilPollKeepsReading(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":"too_many_streams","message":"Too many streams for one person."}`)
	})
	var mu sync.Mutex
	reads := 0
	p.onFunc("GET", "/bots/scout/chat/threads/thr_fleet", func(rec) (int, string) {
		mu.Lock()
		reads++
		n := reads
		mu.Unlock()
		switch n {
		case 1:
			// The connection died under the read: the body stops halfway, which is a transport
			// failure and not the platform's answer.
			return 200, `{"thread":{"id":"thr_fleet","title":"t`
		case 2:
			return 500, `{"code":"internal","message":"the platform is briefly down"}`
		}
		return 200, chatDetailBody("thr_fleet", 4, chatTurnRecord("trn_1", "thr_fleet", "done", "read at last", "fleet"))
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 5,
	}))
	if got["state"] != "done" || got["final"] != "read at last" {
		t.Errorf("result %v, want the turn as the third read found it", got)
	}
	mu.Lock()
	n := reads
	mu.Unlock()
	if n < 3 {
		t.Errorf("%d reads, want the poll to keep reading after the two failures", n)
	}
}

// ---------- (y) any member the bot may ask ----------

func TestMCPCouncilAskScopedMember(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.onSeq("POST", "/bots/scout/chat/threads",
		chatReply{201, streamAnalystThread("thr_1", "Stream analyst")},
		chatReply{201, chatThreadBody("thr_2", "MCP: test stream-analyst-test", "stream-analyst-test")})
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "stream-analyst-test"))
	p.on("POST", "/bots/scout/chat/threads/thr_2/turns", 202, chatTurnIn("trn_2", "thr_2", "stream-analyst-test"))
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "teamspace/stream-analyst-test", "task": "look", "wait_seconds": 0,
	}))
	if got["member"] != "stream-analyst-test" || got["turnId"] != "trn_1" || got["threadId"] != "thr_1" {
		t.Errorf("result %v", got)
	}
	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 1 {
		t.Fatalf("%d threads created", len(created))
	}
	bodyEq(t, "the scoped member's thread", created[0].Body,
		`{"title":"MCP: test teamspace/stream-analyst-test","origin":"mcp","member":"stream-analyst-test","memberScope":"teamspace"}`)
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")
	if len(turns) != 1 {
		t.Fatalf("%d turns posted", len(turns))
	}
	bodyEq(t, "the scoped member's turn", turns[0].Body, `{"prompt":"look","member":"stream-analyst-test"}`)

	// The same ask again: the conversation is busy, and names the member from P3's answer.
	before := p.countAll()
	res := mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "teamspace/stream-analyst-test", "task": "look", "wait_seconds": 0,
	})
	want := "Stream analyst is still working on turn trn_1 in this conversation: call council_result with turn_id trn_1."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if after := p.countAll(); after != before {
		t.Errorf("a refused ask sent %d request(s)", after-before)
	}

	// The bare slug is another conversation: no lookup is spent to tell it from the scoped one.
	got = mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
		"bot": "scout", "member": "stream-analyst-test", "task": "look", "wait_seconds": 0,
	}))
	if got["threadId"] != "thr_2" || got["turnId"] != "trn_2" {
		t.Errorf("the bare slug's ask %v, want the second thread", got)
	}
	created = p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 2 {
		t.Fatalf("%d threads created, want 2", len(created))
	}
	bodyEq(t, "the bare member's thread", created[1].Body,
		`{"title":"MCP: test stream-analyst-test","origin":"mcp","member":"stream-analyst-test"}`)
}

// Before this process knows a member's name, its busy error names the reference the agent gave.
func TestMCPCouncilBusyNamesTheRef(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, streamAnalystThread("thr_1", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "stream-analyst-test"))
	cs := councilHarness(t, p)

	args := map[string]any{"bot": "scout", "member": "teamspace/stream-analyst-test", "task": "look", "wait_seconds": 0}
	if res := mcpCall(t, cs, "council_ask", args); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	res := mcpCall(t, cs, "council_ask", args)
	want := "The member teamspace/stream-analyst-test is still working on turn trn_1 in this conversation: call council_result with turn_id trn_1."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// The name a thread's own read carries replaces the reference: the read happens before the
// open-turn check.
func TestMCPCouncilBusyNamesTheRefAfterTheReads(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, streamAnalystThread("thr_1", ""))
	p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
		chatReply{202, chatTurnIn("trn_1", "thr_1", "stream-analyst-test")},
		chatReply{409, `{"code":"turn_open","message":"a turn is in progress"}`})
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "stream-analyst-test", `{"summary":"ok"}`, "ok", ""))
	})
	p.onSeq("GET", "/bots/scout/chat/threads/thr_1",
		chatReply{200, chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "stream-analyst-test"))},
		chatReply{200, chatDetailBody("thr_1", 9, chatTurnRecord("trn_1", "thr_1", "done", "ok", "stream-analyst-test")+","+
			chatTurnRecord("trn_late", "thr_1", "running", "", "stream-analyst-test"))})
	cs := councilHarness(t, p)

	args := map[string]any{"bot": "scout", "member": "teamspace/stream-analyst-test", "task": "look"}
	got := mcpMap(t, mcpCall(t, cs, "council_ask", args))
	if got["state"] != "done" || got["threadId"] != "thr_1" {
		t.Fatalf("first ask %v", got)
	}
	want := "The member teamspace/stream-analyst-test is still working on turn trn_late in this conversation: call council_result with turn_id trn_late."
	args["wait_seconds"] = 0
	if got := mcpErr(t, mcpCall(t, cs, "council_ask", args)); got != want {
		t.Errorf("second ask: %q, want %q", got, want)
	}
	if got := mcpErr(t, mcpCall(t, cs, "council_ask", args)); got != want {
		t.Errorf("third ask: %q, want %q", got, want)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 2 {
		t.Errorf("%d turns posted, want 2: the turn_open refusals are not retried", n)
	}
}

func TestCouncilWho(t *testing.T) {
	for _, c := range []struct{ name, ref, want string }{
		{"", "bot/other", "The member bot/other"},
		{"Ops lead", "", "The council"},
		{"Fleet", "fleet", "Fleet"},
	} {
		if got := councilWho(c.name, c.ref); got != c.want {
			t.Errorf("councilWho(%q, %q) = %q, want %q", c.name, c.ref, got, c.want)
		}
	}
	if got, want := councilWho("A\x1b[31mB", "x"), hubText("A\x1b[31mB"); got != want {
		t.Errorf("councilWho: %q, want the hub's text %q", got, want)
	}
	if hubText("A\x1b[31mB") == "A\x1b[31mB" {
		t.Error("hubText left the control character in")
	}
}

// The thread's own read names its member, and that name is what the next refusal prints.
func TestMCPCouncilStartTakesTheNameOfTheThreadsRead(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_fleet", "MCP: test fleet", "fleet"))
	p.on("POST", "/bots/scout/chat/threads/thr_fleet/turns", 202, chatTurnIn("trn_1", "thr_fleet", "fleet"))
	p.onStream("/bots/scout/chat/threads/thr_fleet/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFrames(`{"summary":"ok"}`, "ok", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_fleet", 200,
		chatDetailMember("thr_fleet", "Fleet ops", 3, chatTurnRecord("trn_late", "thr_fleet", "running", "", "fleet")))
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two"})
	want := "Fleet ops is still working on turn trn_late in this conversation: call council_result with turn_id trn_late."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

// The hub's own refusals of a start reach the agent as the hub wrote them.
func TestMCPCouncilAskPassesTheHubsRefusals(t *testing.T) {
	for _, c := range []struct {
		name   string
		member string
		status int
		body   string
		want   string
	}{
		{"the lead's slug", "captain", 400,
			councilErrBody("unknown_member", "The Captain leads every turn: ask without @.", ""),
			"The Captain leads every turn: ask without @."},
		{"an unknown slug", "nobody", 400,
			councilErrBody("unknown_member", "'nobody' is not a member of this council. Members: fleet, coder.", ""),
			"'nobody' is not a member of this council. Members: fleet, coder."},
		{"an ambiguous slug", "triage", 400,
			councilErrBody("ambiguous", "More than one member is called 'triage' (teamspace, bot): name the scope.", ""),
			"More than one member is called 'triage' (teamspace, bot): name the scope."},
		{"no access", "fleet", 403,
			councilErrBody("chat_not_allowed", "You may not chat with scout.", ""),
			"You may not chat with scout."},
		{"the chat is off", "coder", 404,
			`{"code":"council_off","message":"The council is turned off on this platform."}`,
			"the TeknoirBot chat is turned off on this platform"},
	} {
		t.Run(c.name, func(t *testing.T) {
			councilFast(t)
			p := newChatAPI(t)
			p.on("POST", "/bots/scout/chat/threads", c.status, c.body)
			cs := councilHarness(t, p)

			res := mcpCall(t, cs, "council_ask", map[string]any{
				"bot": "scout", "member": c.member, "task": "x", "wait_seconds": 0,
			})
			if got := mcpErr(t, res); got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
			if n := p.countAll(); n != 1 {
				t.Errorf("%d requests, want 1: a refused thread posts no turn", n)
			}
		})
	}

	// A code this process does not map keeps classify's own wording.
	t.Run("the bot is not ready", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 409, councilErrBody("bot_not_ready", "The bot is starting.", ""))
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_ask", map[string]any{
			"bot": "scout", "member": "fleet", "task": "x", "wait_seconds": 0,
		})
		if got := mcpErr(t, res); !strings.Contains(got, "The bot is starting.") {
			t.Errorf("%q, want it to say %q", got, "The bot is starting.")
		}
		if n := p.countAll(); n != 2 {
			t.Errorf("%d requests, want 2", n)
		}
	})

	// A 404 is not a deleted thread when it says the chat is off.
	t.Run("the chat is off at the turn", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 404,
			`{"code":"council_off","message":"The council is turned off on this platform."}`)
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_ask", map[string]any{
			"bot": "scout", "member": "fleet", "task": "x", "wait_seconds": 0,
		})
		if got := mcpErr(t, res); got != "the TeknoirBot chat is turned off on this platform" {
			t.Errorf("%q, want the chat's own sentence", got)
		}
		if n := p.countAll(); n != 2 {
			t.Errorf("%d requests, want 2: a council_off 404 is not a deleted thread", n)
		}
	})
}

func TestMCPCouncilForgetsAGoneThread(t *testing.T) {
	t.Run("member", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.onSeq("POST", "/bots/scout/chat/threads",
			chatReply{201, chatThreadBody("thr_1", "MCP: test fleet", "fleet")},
			chatReply{201, chatThreadBody("thr_2", "MCP: test fleet", "fleet")})
		p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
			chatReply{202, chatTurnIn("trn_1", "thr_1", "fleet")},
			chatReply{409, councilErrBody("role_gone", "The member 'fleet' was archived or deleted; start a new chat with another member.", "")})
		p.on("POST", "/bots/scout/chat/threads/thr_2/turns", 202, chatTurnIn("trn_2", "thr_2", "fleet"))
		p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
		})
		p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
			chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "fleet")))
		cs := councilHarness(t, p)

		if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
			t.Fatal(mcpText(t, res))
		}
		res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two", "wait_seconds": 0})
		if got, want := mcpErr(t, res), "The member 'fleet' was archived or deleted; start a new chat with another member."; got != want {
			t.Errorf("%q, want %q", got, want)
		}
		got := mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "three", "wait_seconds": 0}))
		if got["threadId"] != "thr_2" || got["turnId"] != "trn_2" {
			t.Errorf("the third ask %v, want a turn on the new thread", got)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 2 {
			t.Errorf("%d threads created, want 2", n)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 2 {
			t.Errorf("%d turns posted on thr_1, want 2", n)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_2/turns")); n != 1 {
			t.Errorf("%d turns posted on thr_2, want 1", n)
		}
		if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_1")); n != 1 {
			t.Errorf("%d reads of thr_1, want 1: the forgotten thread is not read again", n)
		}
	})

	t.Run("council", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.onSeq("POST", "/bots/scout/chat/threads",
			chatReply{201, chatThreadBody("thr_1", "MCP: test council", "")},
			chatReply{201, chatThreadBody("thr_2", "MCP: test council", "")})
		p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
			chatReply{202, chatTurnIn("trn_1", "thr_1", "")},
			chatReply{409, councilErrBody("roster_gone", "The council for this chat was deleted; pick another council for this chat.", "")},
			chatReply{409, councilErrBody("role_gone", "The member 'triage' was archived or deleted; start a new chat with another member.", "")},
			chatReply{409, councilErrBody("pin_empty", "None of the pinned members is in this council any more; change the pin.", "")})
		p.on("POST", "/bots/scout/chat/threads/thr_2/turns", 202, chatTurnIn("trn_2", "thr_2", ""))
		p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
		})
		p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
			chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "")))
		cs := councilHarness(t, p)

		if res := mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}); res.IsError {
			t.Fatal(mcpText(t, res))
		}
		// roster_gone and role_gone come from the council itself: that thread is kept.
		for _, want := range []string{
			"The council for this chat was deleted; pick another council for this chat.",
			"The member 'triage' was archived or deleted; start a new chat with another member.",
			"None of the pinned members is in this council any more; change the pin.\nCall council_run again: it starts a new chat without the pin.",
		} {
			res := mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?", "wait_seconds": 0})
			if got := mcpErr(t, res); got != want {
				t.Errorf("%q, want %q", got, want)
			}
		}
		got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?", "wait_seconds": 0}))
		if got["threadId"] != "thr_2" || got["turnId"] != "trn_2" {
			t.Errorf("the fifth run %v, want a turn on the new thread", got)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 2 {
			t.Errorf("%d threads created, want 2", n)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 4 {
			t.Errorf("%d turns posted on thr_1, want 4", n)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_2/turns")); n != 1 {
			t.Errorf("%d turns posted on thr_2, want 1", n)
		}
		if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_1")); n != 3 {
			t.Errorf("%d reads of thr_1, want 3: pin_empty forgot the thread, the other refusals kept it", n)
		}
	})
}

// A conflict is never retried: the agent sends again, and the thread is kept.
func TestMCPCouncilConflictIsNotRetried(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test council", ""))
	p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
		chatReply{202, chatTurnIn("trn_1", "thr_1", "")},
		chatReply{409, councilErrBody("conflict", "This chat's council just changed. Send again.", "")},
		chatReply{202, chatTurnIn("trn_2", "thr_1", "")})
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "")))
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	res := mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?", "wait_seconds": 0})
	if got, want := mcpErr(t, res), "This chat's council just changed. Send again."; got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 2 {
		t.Errorf("%d turns posted at the conflict, want 2: it is not retried", n)
	}
	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?", "wait_seconds": 0}))
	if got["turnId"] != "trn_2" || got["threadId"] != "thr_1" {
		t.Errorf("the third run %v, want trn_2 on the kept thread", got)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
		t.Errorf("%d threads created, want 1", n)
	}
}

func TestMCPCouncilRunRoster(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.onSeq("POST", "/bots/scout/chat/threads",
		chatReply{201, chatThreadBody("thr_1", "MCP: test council ops", "")},
		chatReply{201, chatThreadBody("thr_2", "MCP: test council", "")})
	p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
		chatReply{202, chatTurnIn("trn_1", "thr_1", "")},
		chatReply{202, chatTurnIn("trn_2", "thr_1", "")})
	p.on("POST", "/bots/scout/chat/threads/thr_2/turns", 202, chatTurnIn("trn_3", "thr_2", ""))
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	// The thread's council is set to ops: its display name differs from its name.
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailRoster("thr_1", "ops", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "")))
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?", "roster": "ops"}))
	if got["state"] != "done" || got["turnId"] != "trn_1" || got["threadId"] != "thr_1" {
		t.Fatalf("first run %v", got)
	}
	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 1 {
		t.Fatalf("%d threads created", len(created))
	}
	bodyEq(t, "the roster's thread", created[0].Body, `{"title":"MCP: test council ops","origin":"mcp","roster":"ops"}`)
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")
	if len(turns) != 1 {
		t.Fatalf("%d turns posted", len(turns))
	}
	bodyEq(t, "the roster's turn", turns[0].Body, `{"prompt":"status?"}`)

	// The same roster again: the thread's council matches by name, so no new thread.
	got = mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?", "roster": "ops", "wait_seconds": 0}))
	if got["turnId"] != "trn_2" || got["threadId"] != "thr_1" {
		t.Errorf("the second run %v, want trn_2 on thr_1", got)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
		t.Errorf("%d threads created, want 1", n)
	}

	// The default council is another conversation.
	got = mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?", "wait_seconds": 0}))
	if got["threadId"] != "thr_2" || got["turnId"] != "trn_3" {
		t.Errorf("the default council's run %v, want the second thread", got)
	}
	created = p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 2 {
		t.Fatalf("%d threads created, want 2", len(created))
	}
	bodyEq(t, "the default council's thread", created[1].Body, `{"title":"MCP: test council","origin":"mcp"}`)
}

// A member and a council of one name are two conversations: the council: prefix keeps them apart.
func TestMCPCouncilCouncilAndMemberOfOneName(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.onSeq("POST", "/bots/scout/chat/threads",
		chatReply{201, chatThreadBody("thr_m", "MCP: test triage", "triage")},
		chatReply{201, chatThreadBody("thr_c", "MCP: test council triage", "")})
	p.on("POST", "/bots/scout/chat/threads/thr_m/turns", 202, chatTurnIn("trn_m", "thr_m", "triage"))
	p.on("POST", "/bots/scout/chat/threads/thr_c/turns", 202, chatTurnIn("trn_c", "thr_c", ""))
	cs := councilHarness(t, p)
	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "triage", "task": "x", "wait_seconds": 0}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	res := mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "x", "roster": "triage", "wait_seconds": 0})
	if res.IsError {
		t.Fatalf("council_run refused: %s", mcpText(t, res))
	}
	if got := mcpMap(t, res); got["threadId"] != "thr_c" || got["turnId"] != "trn_c" {
		t.Errorf("the council triage's run %v, want its own thread", got)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 2 {
		t.Errorf("%d P3s, want 2", n)
	}
}

// A council_run that names a roster is still the council's conversation: its busy text names the
// council, never the roster as if it were a member.
func TestMCPCouncilBusyRosterRunIsTheCouncil(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test council ops", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", ""))
	cs := councilHarness(t, p)
	args := map[string]any{"bot": "scout", "question": "x", "roster": "ops", "wait_seconds": 0}
	if res := mcpCall(t, cs, "council_run", args); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	before := p.countAll()
	want := "The council is still working on turn trn_1 in this conversation: call council_result with turn_id trn_1."
	if got := mcpErr(t, mcpCall(t, cs, "council_run", args)); got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if n := p.countAll(); n != before {
		t.Errorf("the busy run sent %d requests, want none", n-before)
	}
}

// A council run whose thread is still on its council refuses a turn open on it: at the thread's read
// (no P6), and from P6's turn_open; both texts name the council, never the roster.
func TestMCPCouncilRunBusyAtTheReadAndAtTheTurn(t *testing.T) {
	const want = "The council is still working on turn trn_web in this conversation: call council_result with turn_id trn_web."
	open := chatDetailRoster("thr_1", "ops", 9, chatTurnRecord("trn_1", "thr_1", "done", "ok", "")+","+
		chatTurnRecord("trn_web", "thr_1", "running", "", ""))
	setup := func(t *testing.T, p *chatAPI) {
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test council ops", ""))
		p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
		})
	}
	run := func(t *testing.T, p *chatAPI) string {
		cs := councilHarness(t, p)
		args := map[string]any{"bot": "scout", "question": "status?", "roster": "ops"}
		if res := mcpCall(t, cs, "council_run", args); res.IsError {
			t.Fatal(mcpText(t, res))
		}
		args["wait_seconds"] = 0
		return mcpErr(t, mcpCall(t, cs, "council_run", args))
	}
	t.Run("at the read", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		setup(t, p)
		p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
			chatReply{202, chatTurnIn("trn_1", "thr_1", "")}, chatReply{202, chatTurnIn("trn_2", "thr_1", "")})
		p.on("GET", "/bots/scout/chat/threads/thr_1", 200, open)
		if got := run(t, p); got != want {
			t.Errorf("%q, want %q", got, want)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 1 {
			t.Errorf("%d turns posted, want 1: the open turn is refused at the read", n)
		}
	})
	t.Run("at the turn", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		setup(t, p)
		p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
			chatReply{202, chatTurnIn("trn_1", "thr_1", "")},
			chatReply{409, `{"code":"turn_open","message":"This chat already has a turn in progress."}`})
		p.onSeq("GET", "/bots/scout/chat/threads/thr_1",
			chatReply{200, chatDetailRoster("thr_1", "ops", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", ""))},
			chatReply{200, open})
		if got := run(t, p); got != want {
			t.Errorf("%q, want %q", got, want)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 2 {
			t.Errorf("%d turns posted, want 2: turn_open is not retried", n)
		}
	})
}

func TestMCPCouncilRunStartsANewThreadWhenTheCouncilChanged(t *testing.T) {
	for _, c := range []struct {
		name, roster, title, detail string
	}{
		{"changed in the web", "", "MCP: test council",
			chatDetailRoster("thr_1", "night", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", ""))},
		{"changed to another council", "ops", "MCP: test council ops",
			chatDetailRoster("thr_1", "night", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", ""))},
		{"changed with a turn open", "ops", "MCP: test council ops",
			chatDetailRoster("thr_1", "night", 9, chatTurnRecord("trn_1", "thr_1", "done", "ok", "")+","+
				chatTurnRecord("trn_web", "thr_1", "running", "", ""))},
	} {
		t.Run(c.name, func(t *testing.T) {
			councilFast(t)
			p := newChatAPI(t)
			p.onSeq("POST", "/bots/scout/chat/threads",
				chatReply{201, chatThreadBody("thr_1", c.title, "")},
				chatReply{201, chatThreadBody("thr_2", c.title, "")})
			p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", ""))
			p.on("POST", "/bots/scout/chat/threads/thr_2/turns", 202, chatTurnIn("trn_2", "thr_2", ""))
			p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
				writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
			})
			var mu sync.Mutex
			var ids []string
			p.onStream("/bots/scout/chat/threads/thr_2/events", func(w http.ResponseWriter, r *http.Request, conn int) {
				mu.Lock()
				ids = append(ids, r.Header.Get("Last-Event-ID"))
				mu.Unlock()
				writeFrames(w, memberFramesOf("trn_2", "fleet", `{"summary":"ok"}`, "ok", ""))
			})
			p.on("GET", "/bots/scout/chat/threads/thr_1", 200, c.detail)
			cs := councilHarness(t, p)

			args := map[string]any{"bot": "scout", "question": "status?"}
			if c.roster != "" {
				args["roster"] = c.roster
			}
			if res := mcpCall(t, cs, "council_run", args); res.IsError {
				t.Fatal(mcpText(t, res))
			}
			args["wait_seconds"] = 5
			got := mcpMap(t, mcpCall(t, cs, "council_run", args))
			if got["threadId"] != "thr_2" || got["state"] != "done" {
				t.Errorf("result %v, want a new thread that ended done", got)
			}
			created := p.seen("POST", "/bots/scout/chat/threads")
			if len(created) != 2 {
				t.Fatalf("%d threads created, want 2", len(created))
			}
			if created[0].Body != created[1].Body {
				t.Errorf("the two thread bodies differ: %s and %s", created[0].Body, created[1].Body)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 1 {
				t.Errorf("%d turns posted on the old thread, want 1", n)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_2/turns")); n != 1 {
				t.Errorf("%d turns posted on the new thread, want 1", n)
			}
			mu.Lock()
			gotIDs := append([]string{}, ids...)
			mu.Unlock()
			if len(gotIDs) == 0 || gotIDs[0] != "" {
				t.Errorf("Last-Event-ID per connection of thr_2 %q, want the first one empty", gotIDs)
			}
		})
	}
}

func TestMCPCouncilRunUnknownRoster(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.onSeq("POST", "/bots/scout/chat/threads",
		chatReply{201, chatThreadBody("thr_1", "MCP: test council night-ops", "")},
		chatReply{404, councilErrBody("unknown_roster", "There is no council 'night-ops' for scout.", "")})
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", ""))
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	// The bot's own council night-ops was deleted: its threads fall back to the default.
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailRoster("thr_1", "", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "")))
	cs := councilHarness(t, p)

	args := map[string]any{"bot": "scout", "question": "status?", "roster": "night-ops"}
	if res := mcpCall(t, cs, "council_run", args); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	want := "There is no council 'night-ops' for scout."
	for i := 0; i < 2; i++ {
		args["wait_seconds"] = 0
		if got := mcpErr(t, mcpCall(t, cs, "council_run", args)); got != want {
			t.Errorf("run %d: %q, want %q", i+2, got, want)
		}
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 3 {
		t.Errorf("%d threads created, want 3: a 404 is not taken for a deleted thread", n)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")); n != 1 {
		t.Errorf("%d turns posted, want 1", n)
	}
	if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_1")); n != 1 {
		t.Errorf("%d reads of thr_1, want 1: the thread was forgotten", n)
	}
}

func TestMCPCouncilRunThorough(t *testing.T) {
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"thorough alone", map[string]any{"thorough": true},
			`{"prompt":"why?","mode":"council","thorough":true}`},
		{"mode council", map[string]any{"mode": "council"},
			`{"prompt":"why?","mode":"council"}`},
		{"thorough with mode council", map[string]any{"thorough": true, "mode": "council"},
			`{"prompt":"why?","mode":"council","thorough":true}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			councilFast(t)
			p := newChatAPI(t)
			p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test council", ""))
			p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", ""))
			cs := councilHarness(t, p)

			args := map[string]any{"bot": "scout", "question": "why?", "wait_seconds": 0}
			for k, v := range c.args {
				args[k] = v
			}
			if res := mcpCall(t, cs, "council_run", args); res.IsError {
				t.Fatal(mcpText(t, res))
			}
			turns := p.seen("POST", "/bots/scout/chat/threads/thr_1/turns")
			if len(turns) != 1 {
				t.Fatalf("%d turns posted", len(turns))
			}
			bodyEq(t, "the turn", turns[0].Body, c.want)
		})
	}
}

func TestMCPCouncilPostNotFoundKeepsTheBodies(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.onSeq("POST", "/bots/scout/chat/threads",
		chatReply{201, chatThreadBody("thr_1", "MCP: test council ops", "")},
		chatReply{201, chatThreadBody("thr_2", "MCP: test council ops", "")})
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 404,
		councilErrBody("not_found", "This chat or turn does not exist.", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_2/turns", 202, chatTurnIn("trn_2", "thr_2", ""))
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{
		"bot": "scout", "question": "why?", "roster": "ops", "thorough": true, "wait_seconds": 0,
	}))
	if got["threadId"] != "thr_2" || got["turnId"] != "trn_2" {
		t.Errorf("result %v, want the new thread's turn", got)
	}
	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 2 {
		t.Fatalf("%d threads created", len(created))
	}
	for i, r := range created {
		bodyEq(t, fmt.Sprintf("thread %d", i), r.Body, `{"title":"MCP: test council ops","origin":"mcp","roster":"ops"}`)
	}
	for _, thread := range []string{"thr_1", "thr_2"} {
		turns := p.seen("POST", "/bots/scout/chat/threads/"+thread+"/turns")
		if len(turns) != 1 {
			t.Fatalf("%s: %d turns posted", thread, len(turns))
		}
		bodyEq(t, thread+"'s turn", turns[0].Body, `{"prompt":"why?","mode":"council","thorough":true}`)
	}
}

// A council turn's members carry the member's display name when the bot sends one.
func TestMCPCouncilRunMemberNames(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", ""))
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frameOf("trn_1", 1, "turn_start", `"mode":"council"`),
			frameOf("trn_1", 2, "member_done", `"member":"fleet","name":"Fleet","task":1,"report":{"summary":"two robots down"}`),
			frameOf("trn_1", 3, "member_done", `"member":"stream-analyst-test","task":2,"report":{"summary":"quiet"}`),
			frameOf("trn_1", 4, "final", `"text":"done"`),
			frameOf("trn_1", 5, "turn_end", `"state":"done"`),
		})
	})
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{"bot": "scout", "question": "status?"}))
	members, _ := got["members"].([]any)
	var want []any
	if err := json.Unmarshal([]byte(`[{"member":"fleet","name":"Fleet","task":1,"summary":"two robots down"},`+
		`{"member":"stream-analyst-test","task":2,"summary":"quiet"}]`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(members, want) {
		t.Errorf("members %v, want %v", members, want)
	}
}

func TestMCPCouncilFoundTurnJoinsItsConversation(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, streamAnalystThread("thr_1", "Stream analyst"))
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "stream-analyst-test"))
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "stream-analyst-test", `{"summary":"ok"}`, "ok", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailMember("thr_1", "Stream analyst 2", 9, chatTurnRecord("trn_1", "thr_1", "done", "ok", "stream-analyst-test")+","+
			chatTurnRecord("trn_9", "thr_1", "running", "", "stream-analyst-test")))
	cs := councilHarness(t, p)

	args := map[string]any{"bot": "scout", "member": "teamspace/stream-analyst-test", "task": "look"}
	if res := mcpCall(t, cs, "council_ask", args); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{
		"bot": "scout", "turn_id": "trn_9", "wait_seconds": 0,
	}))
	if got["state"] != "running" || got["threadId"] != "thr_1" {
		t.Errorf("council_result %v, want the running turn of this process's thread", got)
	}
	before := p.countAll()
	res := mcpCall(t, cs, "council_ask", args)
	want := "Stream analyst 2 is still working on turn trn_9 in this conversation: call council_result with turn_id trn_9."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if after := p.countAll(); after != before {
		t.Errorf("the refused ask sent %d request(s)", after-before)
	}
}

func TestMCPCouncilFoundCouncilTurnJoinsItsConversation(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", ""))
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailBody("thr_1", 9, chatTurnRecord("trn_1", "thr_1", "done", "ok", "")+","+
			chatTurnRecord("trn_9", "thr_1", "running", "", "")))
	cs := councilHarness(t, p)

	args := map[string]any{"bot": "scout", "question": "status?"}
	if res := mcpCall(t, cs, "council_run", args); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{
		"bot": "scout", "turn_id": "trn_9", "wait_seconds": 0,
	}))
	if got["state"] != "running" || got["threadId"] != "thr_1" {
		t.Errorf("council_result %v, want the running turn of this process's thread", got)
	}
	before := p.countAll()
	res := mcpCall(t, cs, "council_run", args)
	want := "The council is still working on turn trn_9 in this conversation: call council_result with turn_id trn_9."
	if got := mcpErr(t, res); got != want {
		t.Errorf("%q, want %q", got, want)
	}
	if after := p.countAll(); after != before {
		t.Errorf("the refused run sent %d request(s)", after-before)
	}
}

// A turn of a thread this process does not keep belongs to none of its conversations: starting one
// is not refused.
func TestMCPCouncilFoundForeignTurnBlocksNothing(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200,
		`{"items":[{"id":"thr_web","title":"web","origin":"web","member":null,"author":{"ref":"r"},`+
			`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":{"id":"trn_web","state":"running"}}]}`)
	p.on("GET", "/bots/scout/chat/threads/thr_web", 200,
		chatDetailBody("thr_web", 4, chatTurnRecord("trn_web", "thr_web", "running", "", "")))
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_new", "MCP: test council", ""))
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnIn("trn_new", "thr_new", ""))
	cs := councilHarness(t, p)

	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{
		"bot": "scout", "turn_id": "trn_web", "wait_seconds": 0,
	}))
	if got["state"] != "running" {
		t.Errorf("council_result %v, want the web turn running", got)
	}
	got = mcpMap(t, mcpCall(t, cs, "council_run", map[string]any{
		"bot": "scout", "question": "status?", "wait_seconds": 0,
	}))
	if got["threadId"] != "thr_new" || got["turnId"] != "trn_new" {
		t.Errorf("the run %v, want its own new thread", got)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
		t.Errorf("%d threads created, want 1", n)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_web/turns")); n != 0 {
		t.Errorf("%d turns posted on the web's thread, want none", n)
	}

	t.Run("member", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat/threads", 200,
			`{"items":[{"id":"thr_web","title":"web","origin":"web","member":"fleet","author":{"ref":"r"},`+
				`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":{"id":"trn_web","state":"running"}}]}`)
		p.on("GET", "/bots/scout/chat/threads/thr_web", 200,
			chatDetailBody("thr_web", 4, chatTurnRecord("trn_web", "thr_web", "running", "", "fleet")))
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_new", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnIn("trn_new", "thr_new", "fleet"))
		cs := councilHarness(t, p)

		got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{
			"bot": "scout", "turn_id": "trn_web", "wait_seconds": 0,
		}))
		if got["state"] != "running" || got["member"] != "fleet" {
			t.Errorf("council_result %v, want the web member turn running", got)
		}
		got = mcpMap(t, mcpCall(t, cs, "council_ask", map[string]any{
			"bot": "scout", "member": "fleet", "task": "x", "wait_seconds": 0,
		}))
		if got["threadId"] != "thr_new" || got["turnId"] != "trn_new" {
			t.Errorf("the ask %v, want its own new thread", got)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
			t.Errorf("%d threads created, want 1", n)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_web/turns")); n != 0 {
			t.Errorf("%d turns posted on the web's thread, want none", n)
		}
	})
}

// A turn found on a thread this process does not keep does not join the conversation that keeps
// another thread of the same member: keyOfThread matches the thread id.
func TestMCPCouncilFoundForeignTurnNextToAKeptThread(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
	p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
		chatReply{202, chatTurnIn("trn_1", "thr_1", "fleet")},
		chatReply{202, chatTurnIn("trn_2", "thr_1", "fleet")})
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "fleet")))
	p.on("GET", "/bots/scout/chat/threads", 200,
		`{"items":[{"id":"thr_web","title":"web","origin":"web","member":"fleet","author":{"ref":"r"},`+
			`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":{"id":"trn_web","state":"running"}}]}`)
	p.on("GET", "/bots/scout/chat/threads/thr_web", 200,
		chatDetailBody("thr_web", 4, chatTurnRecord("trn_web", "thr_web", "running", "", "fleet")))
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_web", "wait_seconds": 0}))
	if got["state"] != "running" || got["threadId"] != "thr_web" {
		t.Fatalf("council_result %v, want the web's running turn on thr_web", got)
	}
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two", "wait_seconds": 0})
	if res.IsError {
		t.Fatalf("the ask on the kept thread was refused: %s", mcpText(t, res))
	}
	if got := mcpMap(t, res); got["threadId"] != "thr_1" || got["turnId"] != "trn_2" {
		t.Errorf("the ask on the kept thread %v, want trn_2 on thr_1", got)
	}
}

// A turn found on another bot's thread joins none of this bot's conversations, even when the two
// threads share an id: keyOfThread matches the bot as well as the thread (and the teamspace: the next
// test).
func TestMCPCouncilFoundTurnOfAnotherBotJoinsNothing(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
	p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
		chatReply{202, chatTurnIn("trn_1", "thr_1", "fleet")},
		chatReply{202, chatTurnIn("trn_2", "thr_1", "fleet")})
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "fleet")))
	p.on("GET", "/bots/other/chat/threads", 200,
		`{"items":[{"id":"thr_1","title":"web","origin":"web","member":"fleet","author":{"ref":"r"},`+
			`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":{"id":"trn_o","state":"running"}}]}`)
	p.on("GET", "/bots/other/chat/threads/thr_1", 200,
		chatDetailBody("thr_1", 4, chatTurnRecord("trn_o", "thr_1", "running", "", "fleet")))
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "other", "turn_id": "trn_o", "wait_seconds": 0}))
	if got["state"] != "running" || got["threadId"] != "thr_1" {
		t.Fatalf("council_result %v, want other's running turn", got)
	}
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two", "wait_seconds": 0})
	if res.IsError {
		t.Fatalf("scout's ask was refused for other's turn: %s", mcpText(t, res))
	}
	if got := mcpMap(t, res); got["threadId"] != "thr_1" || got["turnId"] != "trn_2" {
		t.Errorf("scout's ask %v, want trn_2 on thr_1", got)
	}
}

// A turn found on the same bot's thread in another teamspace joins none of this teamspace's
// conversations, even when the two threads share an id: keyOfThread matches the teamspace too.
func TestMCPCouncilFoundTurnInAnotherTeamspaceJoinsNothing(t *testing.T) {
	councilFast(t)
	p := newChatAPI(t)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
	p.onSeq("POST", "/bots/scout/chat/threads/thr_1/turns",
		chatReply{202, chatTurnIn("trn_1", "thr_1", "fleet")},
		chatReply{202, chatTurnIn("trn_2", "thr_1", "fleet")})
	p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
	})
	p.on("GET", "/bots/scout/chat/threads/thr_1", 200,
		chatDetailBody("thr_1", 7, chatTurnRecord("trn_1", "thr_1", "done", "ok", "fleet")))
	// The fake API strips only teknoir-demo's base: another teamspace's routes are full paths.
	const other = "/api/device/fleet/other-ns"
	p.on("GET", other+"/bots/scout/chat/threads", 200,
		`{"items":[{"id":"thr_1","title":"web","origin":"web","member":"fleet","author":{"ref":"r"},`+
			`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":{"id":"trn_o","state":"running"}}]}`)
	p.on("GET", other+"/bots/scout/chat/threads/thr_1", 200,
		chatDetailBody("thr_1", 4, chatTurnRecord("trn_o", "thr_1", "running", "", "fleet")))
	cs := councilHarness(t, p)

	if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
		t.Fatal(mcpText(t, res))
	}
	got := mcpMap(t, mcpCall(t, cs, "council_result", map[string]any{"namespace": "other-ns", "bot": "scout", "turn_id": "trn_o", "wait_seconds": 0}))
	if got["state"] != "running" || got["threadId"] != "thr_1" {
		t.Fatalf("council_result %v, want other-ns's running turn", got)
	}
	res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two", "wait_seconds": 0})
	if res.IsError {
		t.Fatalf("teknoir-demo's ask was refused for other-ns's turn: %s", mcpText(t, res))
	}
	if got := mcpMap(t, res); got["threadId"] != "thr_1" || got["turnId"] != "trn_2" {
		t.Errorf("teknoir-demo's ask %v, want trn_2 on thr_1", got)
	}
}

func TestMCPCouncilReadRefusalsSpeakTheHubsWords(t *testing.T) {
	const want = "You may not chat with scout."

	t.Run("start", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "fleet"))
		p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
		})
		p.onFunc("GET", "/bots/scout/chat/threads/thr_1", func(rec) (int, string) {
			return http.StatusForbidden, councilErrBody("chat_not_allowed", want, "")
		})
		cs := councilHarness(t, p)

		if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
			t.Fatal(mcpText(t, res))
		}
		res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two", "wait_seconds": 0})
		if got := mcpErr(t, res); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})

	t.Run("find", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.onFunc("GET", "/bots/scout/chat/threads", func(rec) (int, string) {
			return http.StatusForbidden, councilErrBody("chat_not_allowed", want, "")
		})
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_x", "wait_seconds": 0})
		if got := mcpErr(t, res); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})

	t.Run("known", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "fleet"))
		p.onFunc("GET", "/bots/scout/chat/threads/thr_1", func(rec) (int, string) {
			return http.StatusForbidden, councilErrBody("chat_not_allowed", want, "")
		})
		cs := councilHarness(t, p)

		if res := mcpCall(t, cs, "council_ask", map[string]any{
			"bot": "scout", "member": "fleet", "task": "one", "wait_seconds": 0,
		}); res.IsError {
			t.Fatal(mcpText(t, res))
		}
		res := mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_x", "wait_seconds": 0})
		if got := mcpErr(t, res); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})

	t.Run("listed", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat/threads", 200,
			`{"items":[{"id":"thr_web","title":"web","origin":"web","member":null,"author":{"ref":"r"},`+
				`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":null}]}`)
		p.onFunc("GET", "/bots/scout/chat/threads/thr_web", func(rec) (int, string) {
			return http.StatusForbidden, councilErrBody("chat_not_allowed", want, "")
		})
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_x", "wait_seconds": 0})
		if got := mcpErr(t, res); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})

	t.Run("stream", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "fleet"))
		p.onFunc("GET", "/bots/scout/chat/threads/thr_1/events", func(rec) (int, string) {
			return http.StatusForbidden, councilErrBody("chat_not_allowed", want, "")
		})
		cs := councilHarness(t, p)

		res := mcpCall(t, cs, "council_ask", map[string]any{
			"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 2,
		})
		if got := mcpErr(t, res); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})
}

// council_off on every read test 16 refuses with chat_not_allowed: chatInfoError's sentence, never the
// hub's message.
func TestMCPCouncilCouncilOffOnTheReads(t *testing.T) {
	const off = `{"code":"council_off","message":"The council is turned off on this platform."}`
	const want = "the TeknoirBot chat is turned off on this platform"
	refuse := func(rec) (int, string) { return http.StatusNotFound, off }
	t.Run("start", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "fleet"))
		p.onStream("/bots/scout/chat/threads/thr_1/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, memberFramesOf("trn_1", "fleet", `{"summary":"ok"}`, "ok", ""))
		})
		p.onFunc("GET", "/bots/scout/chat/threads/thr_1", refuse)
		cs := councilHarness(t, p)
		if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one"}); res.IsError {
			t.Fatal(mcpText(t, res))
		}
		if got := mcpErr(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "two", "wait_seconds": 0})); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})
	t.Run("find", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.onFunc("GET", "/bots/scout/chat/threads", refuse)
		cs := councilHarness(t, p)
		if got := mcpErr(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_x", "wait_seconds": 0})); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})
	t.Run("known", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "fleet"))
		p.onFunc("GET", "/bots/scout/chat/threads/thr_1", refuse)
		cs := councilHarness(t, p)
		if res := mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "one", "wait_seconds": 0}); res.IsError {
			t.Fatal(mcpText(t, res))
		}
		if got := mcpErr(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_x", "wait_seconds": 0})); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})
	t.Run("listed", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat/threads", 200,
			`{"items":[{"id":"thr_web","title":"web","origin":"web","member":null,"author":{"ref":"r"},`+
				`"createdAt":1,"updatedAt":2,"turnCount":1,"lastTurn":null}]}`)
		p.onFunc("GET", "/bots/scout/chat/threads/thr_web", refuse)
		cs := councilHarness(t, p)
		if got := mcpErr(t, mcpCall(t, cs, "council_result", map[string]any{"bot": "scout", "turn_id": "trn_x", "wait_seconds": 0})); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})
	t.Run("stream", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadBody("thr_1", "MCP: test fleet", "fleet"))
		p.on("POST", "/bots/scout/chat/threads/thr_1/turns", 202, chatTurnIn("trn_1", "thr_1", "fleet"))
		p.onFunc("GET", "/bots/scout/chat/threads/thr_1/events", refuse)
		cs := councilHarness(t, p)
		if got := mcpErr(t, mcpCall(t, cs, "council_ask", map[string]any{"bot": "scout", "member": "fleet", "task": "go", "wait_seconds": 2})); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	})
}

func TestMCPCouncilInfoErrors(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"the chat is off", 404, `{"code":"council_off","message":"The council is turned off on this platform."}`,
			"the TeknoirBot chat is turned off on this platform"},
		{"no such bot", 404, councilErrBody("not_found", "No bot named scout in teamspace teknoir-demo.", ""),
			"No bot named scout in teamspace teknoir-demo."},
	} {
		t.Run(c.name, func(t *testing.T) {
			councilFast(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", c.status, c.body)
			cs := councilHarness(t, p)

			res := mcpCall(t, cs, "council_info", map[string]any{"bot": "scout"})
			if got := mcpErr(t, res); got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
		})
	}
}

// The 48 KiB cut is tested at its exact edge: one byte more and it cuts.
func TestMCPCouncilInfoCutAtTheBoundary(t *testing.T) {
	padded := func(pad string) string {
		const tail = `this council."]`
		if n := strings.Count(chatInfoV2, tail); n != 1 {
			t.Fatalf("chatInfoV2 changed: %q occurs %d times", tail, n)
		}
		return strings.Replace(chatInfoV2, tail, `this council.",`+q(pad)+`]`, 1)
	}
	n := mcpMaxResult - len(mcpIndent(json.RawMessage(padded(""))))

	councilFast(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, padded(strings.Repeat("a", n)))
	cs := councilHarness(t, p)

	got := mcpText(t, mcpCall(t, cs, "council_info", map[string]any{"bot": "scout"}))
	want := mcpIndent(json.RawMessage(padded(strings.Repeat("a", n))))
	if got != want {
		t.Errorf("the answer at the cap is not whole: %d bytes, want %d", len(got), len(want))
	}
	if len(got) != mcpMaxResult {
		t.Errorf("%d bytes, want exactly %d", len(got), mcpMaxResult)
	}

	t.Run("one more byte", func(t *testing.T) {
		councilFast(t)
		p := newChatAPI(t)
		full := mcpIndent(json.RawMessage(padded(strings.Repeat("a", n+1))))
		p.on("GET", "/bots/scout/chat", 200, padded(strings.Repeat("a", n+1)))
		cs := councilHarness(t, p)

		got := mcpText(t, mcpCall(t, cs, "council_info", map[string]any{"bot": "scout"}))
		if !strings.HasSuffix(got, " bytes of JSON text; narrow the query") {
			tail := got
			if len(tail) > 80 {
				tail = tail[len(tail)-80:]
			}
			t.Errorf("%q, want a JSON text cut's trailer", tail)
		}
		head, _, ok := strings.Cut(got, "\n… truncated")
		if !ok {
			t.Fatalf("the answer is not cut: %d bytes", len(got))
		}
		if head != full[:mcpMaxResult-1024] {
			t.Errorf("the cut's text is not the answer's first %d bytes", mcpMaxResult-1024)
		}
		for _, s := range []string{`"tokensPerDay"`, `"defaultRoster": "standard"`, `"name": "night"`} {
			if !strings.Contains(head, s) {
				t.Errorf("the cut left out %s", s)
			}
		}
	})
}
