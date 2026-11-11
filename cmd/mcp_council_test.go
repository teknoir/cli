package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	if member != "" {
		m = `"` + member + `"`
	}
	return fmt.Sprintf(`{"id":%q,"title":%q,"origin":"mcp","member":%s,"author":{"ref":"r"},"createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null}`, id, title, m)
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

// ---------- (a) the tool list ----------

func TestMCPCouncilToolsAreListed(t *testing.T) {
	noBotMode(t)
	// Without the flag the server is the read-only one it always was.
	plain, err := mcpClient(t, newMCPServer(&cliEnv{})).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Tools) != 17 {
		t.Errorf("%d tools without --council, want the 17 read tools", len(plain.Tools))
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
	if len(tools.Tools) != 21 {
		t.Fatalf("%d tools, want 21", len(tools.Tools))
	}
	want := map[string][]string{
		"council_info":   {"bot"},
		"council_ask":    {"bot", "member", "task"},
		"council_run":    {"bot", "question"},
		"council_result": {"bot", "turn_id"},
	}
	seen := 0
	for _, tool := range tools.Tools {
		if tool.OutputSchema != nil {
			t.Errorf("%s: an output schema would send the result again as structured content", tool.Name)
		}
		if !strings.HasSuffix(tool.Description, mcpCouncilUntrustedNote) {
			if strings.HasPrefix(tool.Name, "council_") {
				t.Errorf("%s: description does not end with the untrusted note: %q", tool.Name, tool.Description)
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
		if tool.Name == "council_info" {
			// it only reads: the annotations of the read tools
			if !a.ReadOnlyHint || !a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
				t.Errorf("council_info: annotations %+v, want read-only", a)
			}
		} else if a.ReadOnlyHint || a.IdempotentHint || a.OpenWorldHint == nil || !*a.OpenWorldHint {
			t.Errorf("%s: annotations %+v: it starts work on the bot", tool.Name, a)
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
	if seen != 4 {
		t.Errorf("%d of the 4 council tools are listed", seen)
	}
	if got := cs.InitializeResult().Instructions; got != mcpCouncilInstructions {
		t.Errorf("instructions %q, want %q", got, mcpCouncilInstructions)
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
		{"council_ask", map[string]any{"bot": "scout", "member": "captain", "task": "x"}, `member must be fleet or coder, not "captain"`},
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
			frameOf("trn_run", 5, "member_done", `"member":"coder","task":1,"report":{"summary":"all green","findings":[],"proposals":[],"confidence":"high","open_questions":[]}`),
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
	if want := "Fleet is still working on a turn in this conversation: wait for it, then ask again."; msg != want &&
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
		frameOf("trn_run", 3, "member_done", `"member":"coder","task":1,"report":{"summary":"coder says"}`),
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
