package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/api"
)

// ---------- the fake chat API ----------

// chatAPI is a fleet API with the chat routes of a bot, including the event stream.
type chatAPI struct {
	t  *testing.T
	mu sync.Mutex
	// routes are "METHOD <path after the teamspace base>".
	routes map[string]func(r rec) (int, string)
	// streams serve the event route: conn counts the connections to that path from 1.
	streams map[string]func(w http.ResponseWriter, r *http.Request, conn int)
	reqs    []rec
	conns   map[string]int
	srv     *httptest.Server
}

func newChatAPI(t *testing.T) *chatAPI {
	t.Helper()
	p := &chatAPI{t: t, routes: map[string]func(rec) (int, string){}, streams: map[string]func(http.ResponseWriter, *http.Request, int){}, conns: map[string]int{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path := strings.TrimPrefix(r.URL.EscapedPath(), "/api/device/fleet/teknoir-demo")
		rc := rec{Method: r.Method, Path: path, Query: r.URL.RawQuery, Body: string(b), Full: r.URL.EscapedPath(), UA: r.Header.Get("User-Agent")}
		p.mu.Lock()
		p.reqs = append(p.reqs, rc)
		h := p.routes[r.Method+" "+path]
		st := p.streams[path]
		p.conns[path]++
		conn := p.conns[path]
		p.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Redirect(w, r, "https://teknoir.cloud/oauth2/start?rd=x", http.StatusFound)
			return
		}
		if st != nil && r.Method == http.MethodGet {
			st(w, r, conn)
			return
		}
		if h == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":"not_found","message":"no route `+r.Method+" "+path+`"}`)
			return
		}
		status, body := h(rc)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *chatAPI) on(method, path string, status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[method+" "+path] = func(rec) (int, string) { return status, body }
}

// onStream serves the event route of one thread.
func (p *chatAPI) onStream(path string, f func(w http.ResponseWriter, r *http.Request, conn int)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streams[path] = f
}

func (p *chatAPI) seen(method, path string) []rec {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []rec
	for _, r := range p.reqs {
		if r.Method == method && r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func (p *chatAPI) env(t *testing.T) *cliEnv {
	t.Helper()
	e, _ := testEnv(t, newFakePlatform(t), nil)
	e.fleet = func() (*api.Fleet, string, string, error) {
		return &api.Fleet{Base: p.srv.URL + "/api/device/fleet/teknoir-demo", Root: p.srv.URL + "/api/device/fleet",
			Token: func(context.Context) (string, error) { return "tok", nil }}, "teknoir.cloud", "teknoir-demo", nil
	}
	return e
}

// setTTY says whether stdin and stdout are terminals for this test, and makes the reconnects fast.
func setTTY(t *testing.T, in, out bool) {
	t.Helper()
	oldIn, oldOut := stdinIsTerminal, stdoutIsTerminal
	stdinIsTerminal, stdoutIsTerminal = func() bool { return in }, func() bool { return out }
	oldDelay, oldMax, oldUnit := chatReconnectDelay, chatReconnectMax, chatRetryAfterUnit
	chatReconnectDelay, chatReconnectMax, chatRetryAfterUnit = time.Millisecond, 4*time.Millisecond, time.Millisecond
	oldGrace := chatCancelGrace
	chatCancelGrace = 2 * time.Second
	t.Cleanup(func() {
		stdinIsTerminal, stdoutIsTerminal = oldIn, oldOut
		chatReconnectDelay, chatReconnectMax, chatRetryAfterUnit = oldDelay, oldMax, oldUnit
		chatCancelGrace = oldGrace
	})
}

const (
	chatInfoBody = `{"bot":{"namespace":"teknoir-demo","name":"scout","uid":"u1","access":"creator","phase":"Running","online":true,"lastSeen":1,"council":"tnctl/v0.3.0"},
"canChat":true,"isCreator":true,
"members":[{"id":"captain","name":"Captain","model":"claude-sonnet-5-5","effort":"high","job":"leads"},
{"id":"fleet","name":"Fleet","model":"claude-sonnet-5-5","effort":"medium","job":"the fleet"},
{"id":"coder","name":"Coder","model":"claude-sonnet-5-5","effort":"medium","job":"code"}],
"budget":{"tokensToday":123456,"tokensPerDay":5000000}}`
	chatThreadNew = `{"id":"thr_new","title":"hello bot","origin":"cli","member":null,"author":{"ref":"r"},"createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null}`
	chatTurnBody  = `{"turn":{"id":"trn_1","threadId":"thr_new","index":1,"kind":"ask","member":null,"prompt":"hello bot","options":{},` +
		`"state":"queued","final":null,"error":null,"usage":null,"author":{"ref":"r"},"createdAt":1,"startedAt":null,"finishedAt":null}}`
)

// frame is one event of turn trn_1's stream, with a sequence number (0: none, as the stream's own
// errors have).
func frame(id int64, typ, data string) string {
	return frameOf("trn_1", id, typ, data)
}

// frameOf is frame for another turn: turnID "" leaves the turnId out. Every frame carries the time
// it happened, as the platform's do.
func frameOf(turnID string, id int64, typ, data string) string {
	const at = `"at":1700000000000,`
	head := at
	if turnID != "" && !strings.Contains(data, `"turnId"`) {
		head = `"turnId":"` + turnID + `",` + at
	}
	if id > 0 {
		return fmt.Sprintf("id: %d\nevent: %s\ndata: {%s%s}\n\n", id, typ, head, data)
	}
	return fmt.Sprintf("event: %s\ndata: {%s%s}\n\n", typ, head, data)
}

// writeFrames writes the frames of one turn.
func writeFrames(w http.ResponseWriter, frames []string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, f := range frames {
		_, _ = io.WriteString(w, f)
	}
	w.(http.Flusher).Flush()
}

// finalContent is the answer of the quick turn below.
const (
	quickFinal   = "Hello there."
	quickResumed = "Hello there, and more."
)

// quickFrames is a quick turn's events for turn trn_1: the Captain's text, the final, the usage, the
// end.
func quickFrames(final string) []string {
	return []string{
		frame(1, "turn_queued", `"index":1,"kind":"ask","member":null`),
		frame(2, "turn_start", `"mode":"quick"`),
		frame(3, "text_delta", `"member":"captain","text":"`+final[:len(final)/2]+`"`),
		frame(4, "text_delta", `"member":"captain","text":"`+final[len(final)/2:]+`"`),
		frame(5, "final", `"text":"`+final+`"`),
		frame(6, "usage", `"calls":2,"input":100,"output":40,"cacheRead":0,"cacheWrite":0,"ms":1500`),
		frame(7, "turn_end", `"state":"done"`),
	}
}

// A one-shot turn on a stream that is not a terminal: stdout is the final text alone, stderr has the
// thread and what the turn cost.
func TestChatOneShotNotATerminal(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, quickFrames(quickFinal))
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if want := quickFinal + "\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if !strings.Contains(errs, "thread thr_new\n") {
		t.Errorf("stderr %q: the thread is not named", errs)
	}
	if !strings.Contains(errs, "done in 1.5s, 2 calls, 140 tokens") {
		t.Errorf("stderr %q", errs)
	}
	// the thread is created for the CLI, with the prompt's first line as its title
	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 1 {
		t.Fatalf("requests %+v", p.reqs)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(created[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["origin"] != "cli" || body["title"] != "hello bot" {
		t.Errorf("body %s", created[0].Body)
	}
	// the turn carries the prompt
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
	if len(turns) != 1 || !strings.Contains(turns[0].Body, `"prompt":"hello bot"`) {
		t.Errorf("turn body %+v", turns)
	}
}

// On a terminal the answer is printed as it is written, and the final is not printed a second time.
func TestChatOneShotOnATerminalPrintsDeltas(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, quickFrames(quickFinal))
	})
	out, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatal(err)
	}
	if want := quickFinal + "\n"; out != want {
		t.Errorf("stdout %q, want %q (the final printed once)", out, want)
	}
}

// A final that is not what the Captain's stream said is printed after it, on its own line.
func TestChatFinalDiffersFromTheCaptainStream(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "text_delta", `"member":"captain","text":"a draft"`),
			frame(2, "final", `"text":"the answer"`),
			frame(3, "turn_end", `"state":"done"`),
		})
	})
	out, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatal(err)
	}
	if want := "a draft\n\nthe answer\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

// A council turn's progress is one line per event on stderr, in the order the events came.
func TestChatCouncilProgress(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "plan", `"mode":"council","tasks":[{"member":"fleet","task":"look"},{"member":"coder","task":"read"}]`),
			frame(2, "member_start", `"member":"fleet","task":"look","model":"m","effort":"high","text":"checking the robot"`),
			frame(3, "tool_call", `"member":"fleet","id":"c1","tool":"robots.list","input":{}`),
			frame(4, "tool_result", `"member":"fleet","id":"c1","ok":true,"summary":"2 robots"`),
			frame(5, "member_done", `"member":"fleet","report":"ok","usage":{"calls":1,"input":1,"output":1,"cacheRead":0,"cacheWrite":0,"ms":1}`),
			frame(6, "critique", `"issues":[{"member":"fleet","severity":"blocking","claim":"x","why":"y"},{"member":"fleet","severity":"minor","claim":"z","why":"w"}]`),
			frame(7, "budget", `"what":"tokens","limit":5000000`),
			frame(8, "final", `"text":"done"`),
			frame(9, "turn_end", `"state":"done"`),
		})
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--mode", "council", "hello bot")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != "done\n" {
		t.Errorf("stdout %q", out)
	}
	want := []string{
		"· council: fleet, coder\n",
		"· fleet: checking the robot\n",
		"· fleet → robots.list\n",
		"· fleet done\n",
		"· critic: 2 issues (1 blocking)\n",
		"· budget: tokens limit 5000000\n",
	}
	lines := strings.Split(strings.TrimSuffix(errs, "\n"), "\n")
	var got []string
	for _, l := range lines {
		if strings.HasPrefix(l, "· ") {
			got = append(got, l+"\n")
		}
	}
	if strings.Join(got, "") != strings.Join(want, "") {
		t.Errorf("progress\n%q\nwant\n%q", got, want)
	}
	// nothing of what is not progress: no tool_result, no final, no turn_start
	for _, un := range []string{"tool_result", "2 robots", "done in", "turn_start"} {
		if strings.Contains(errs, un) {
			t.Errorf("stderr carries %q: %q", un, errs)
		}
	}
}

// --json prints every event of this turn, as the platform sent it, and nothing on stderr.
func TestChatJSON(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, true) // a terminal too: --json turns the deltas off
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "turn_queued", `"index":1,"kind":"ask","member":null`),
			frame(2, "text_delta", `"member":"captain","text":"hi"`),
			// another turn's event: left out
			frameOf("trn_other", 3, "text_delta", `"member":"captain","text":"not mine"`),
			frame(4, "turn_end", `"state":"done"`),
		})
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--json", "hello bot")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines %q", lines)
	}
	var ev struct {
		Seq    int64  `json:"seq"`
		Type   string `json:"type"`
		TurnID string `json:"turnId"`
		Text   string `json:"text"`
		State  string `json:"state"`
	}
	want := []struct {
		seq   int64
		typ   string
		text  string
		state string
	}{
		{1, "turn_queued", "", ""},
		{2, "text_delta", "hi", ""},
		{4, "turn_end", "", "done"},
	}
	for i, w := range want {
		ev = struct {
			Seq    int64  `json:"seq"`
			Type   string `json:"type"`
			TurnID string `json:"turnId"`
			Text   string `json:"text"`
			State  string `json:"state"`
		}{}
		if err := json.Unmarshal([]byte(lines[i]), &ev); err != nil {
			t.Fatalf("line %d %q: %v", i, lines[i], err)
		}
		if ev.Seq != w.seq || ev.Type != w.typ || ev.Text != w.text || ev.State != w.state || ev.TurnID != "trn_1" {
			t.Errorf("line %d %+v, want %+v", i, ev, w)
		}
	}
	if strings.Contains(out, "not mine") {
		t.Errorf("another turn's event was printed: %q", out)
	}
}

// --thread continues a thread from its last sequence number.
func TestChatContinuesAThread(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("GET", "/bots/scout/chat/threads/thr_old", 200,
		`{"thread":{"id":"thr_old","title":"old","origin":"cli","member":null,"createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":null},"turns":[],"lastSeq":42}`)
	p.on("POST", "/bots/scout/chat/threads/thr_old/turns", 202, chatTurnBody)
	var lastEventID string
	var mu sync.Mutex
	p.onStream("/bots/scout/chat/threads/thr_old/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		lastEventID = r.Header.Get("Last-Event-ID")
		mu.Unlock()
		writeFrames(w, quickFrames(quickFinal))
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_old", "hello bot")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != quickFinal+"\n" {
		t.Errorf("stdout %q", out)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 0 {
		t.Errorf("%d threads were created for --thread", n)
	}
	mu.Lock()
	got := lastEventID
	mu.Unlock()
	if got != "42" {
		t.Errorf("Last-Event-ID %q, want the thread's lastSeq", got)
	}
}

// A stream that ends early is connected again from the last sequence number seen.
func TestChatReconnects(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	var mu sync.Mutex
	var ids []string
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		all := quickFrames(quickFinal)
		if conn == 1 {
			writeFrames(w, all[:2]) // the stream ends after two frames
			return
		}
		writeFrames(w, all[2:])
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if want := quickFinal + "\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	mu.Lock()
	got := append([]string{}, ids...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("%d connections", len(got))
	}
	if got[0] != "" || got[1] != "2" {
		t.Errorf("Last-Event-ID per connection %q", got)
	}
}

// stream_expired connects again at once, from where the stream stopped.
func TestChatStreamExpiredReconnectsAtOnce(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	// a pause far longer than the test: only the immediate reconnect makes it pass
	chatReconnectDelay = time.Hour
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	var mu sync.Mutex
	var ids []string
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		ids = append(ids, r.Header.Get("Last-Event-ID"))
		mu.Unlock()
		if conn == 1 {
			writeFrames(w, []string{
				frame(1, "text_delta", `"member":"captain","text":"half"`),
				frameOf("", 0, "error", `"code":"stream_expired","message":"the stream ran its hour"`),
			})
			return
		}
		writeFrames(w, []string{frame(2, "final", `"text":"half answer"`), frame(3, "turn_end", `"state":"done"`)})
	})
	start := time.Now()
	if _, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot"); err != nil {
		t.Fatalf("%v", err)
	}
	if d := time.Since(start); d > time.Minute {
		t.Errorf("took %s: stream_expired waited for the backoff", d)
	}
	mu.Lock()
	got := append([]string{}, ids...)
	mu.Unlock()
	if len(got) != 2 || got[1] != "1" {
		t.Errorf("Last-Event-ID per connection %q", got)
	}
}

// A 429 on the event stream with Retry-After is waited out for that long, not the backoff.
func TestChatWaitsOutA429OfTheStream(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	oldUnit, oldMax := chatRetryAfterUnit, chatRetryAfterMax
	chatRetryAfterUnit, chatRetryAfterMax = 5*time.Millisecond, 60*time.Second
	chatReconnectDelay = time.Hour // only the Retry-After makes this pass
	t.Cleanup(func() { chatRetryAfterUnit, chatRetryAfterMax = oldUnit, oldMax })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		if conn == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"code":"too_many_streams","message":"Too many streams for one person."}`)
			return
		}
		writeFrames(w, quickFrames(quickFinal))
	})
	start := time.Now()
	out, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if out != quickFinal+"\n" {
		t.Errorf("stdout %q", out)
	}
	if d := time.Since(start); d < 10*time.Millisecond || d > 30*time.Second {
		t.Errorf("waited %s, want the Retry-After of 2 units", d)
	}
}

// Six failed reconnects in a row end the command with exit 4 and the hint that the turn goes on.
func TestChatGivesUpAfterSixFailures(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("GET", "/bots/scout/chat/threads/thr_new/events", 503, `{"code":"overloaded","message":"The chat is busy."}`)
	var mu sync.Mutex
	n := 0
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		n++
		mu.Unlock()
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"overloaded","message":"The chat is busy."}`)
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeTimeout {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if want := "lost the chat stream; the turn goes on: tnctl chat scout --thread thr_new"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q, want %q", err, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != chatMaxAttempts {
		t.Errorf("%d connections, want %d", n, chatMaxAttempts)
	}
}

// access_lost ends the command with exit 6 and the platform's words.
func TestChatAccessLostExits6(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{frameOf("", 0, "error", `"code":"access_lost","message":"Your access to scout was removed."`)})
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "Your access to scout was removed.") {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
}

// canChat false is a refusal with the platform's reason.
func TestChatRefusedByThePlatform(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, `{"bot":{"name":"scout","access":"read","phase":null,"online":true},"canChat":false,
"reason":"the bot has no computer yet","members":[],"budget":{"tokensToday":0,"tokensPerDay":0}}`)
	_, _, err := runCLI(t, p.env(t), "chat", "scout", "hello")
	if err == nil || exitCodeOf(err) != codeAuth || !strings.Contains(err.Error(), "you cannot chat with scout: the bot has no computer yet") {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if n := len(p.seen("GET", "/bots/scout/chat")); n != 1 {
		t.Errorf("%d chat info requests", n)
	}
}

// council_off is the chat being off on the platform: exit 1.
func TestChatCouncilOff(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 404, `{"code":"council_off","message":"The chat is off."}`)
	_, _, err := runCLI(t, p.env(t), "chat", "scout", "hello")
	if err == nil || exitCodeOf(err) != codeError || !strings.Contains(err.Error(), "the TeknoirBot chat is turned off on this platform") {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
}

// A turn that failed exits 1 with the turn's own error.
func TestChatTurnFailed(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "error", `"code":"member_failed","message":"The Coder ran out of time."`),
			frame(2, "turn_end", `"state":"failed"`),
		})
	})
	_, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeError {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if want := "the turn failed: member_failed: The Coder ran out of time."; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
}

// A 409 turn_open names the turn that is open and how to cancel it: exit 8.
func TestChatTurnOpen(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 409, `{"code":"turn_open","message":"This thread has a turn in progress."}`)
	_, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeConflict {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if want := "this chat already has a turn in progress (tnctl chat scout --thread thr_new, then /cancel)"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q, want %q", err, want)
	}
}

// A bot's text never drives the terminal: escapes and control characters are dropped, newlines and
// tabs kept.
func TestChatCleansTheBotsText(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	text := "\x1b[31mred\x1b[0m and \x1b]0;title\x07one\x07two\nline\ttab"
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		b, _ := json.Marshal(text)
		writeFrames(w, []string{
			frame(1, "final", `"text":`+string(b)),
			frame(2, "turn_end", `"state":"done"`),
		})
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "red and onetwo\nline\ttab\n"
	if out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Error("an escape or a BEL reached stdout")
	}
}

// The REPL: two prompts in one thread, @coder asks the member alone, /new starts a new thread, and
// /quit ends with 0.
func TestChatREPL(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.onSeqChat("POST", "/bots/scout/chat/threads", []string{
		`{"id":"thr_a","title":"first","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null}`,
		`{"id":"thr_b","title":"third","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null}`,
	})
	threadBody := func(id string) string {
		return `{"turn":{"id":"trn_1","threadId":"` + id + `","index":1,"kind":"ask","member":null,"prompt":"p","options":{},"state":"queued","final":null,"error":null,"usage":null,"author":{"ref":"r"},"createdAt":1}}`
	}
	p.onFunc("POST", "/bots/scout/chat/threads/thr_a/turns", func(rec) (int, string) { return 202, threadBody("thr_a") })
	p.onFunc("POST", "/bots/scout/chat/threads/thr_b/turns", func(rec) (int, string) { return 202, threadBody("thr_b") })
	// The first thread has events from before this run (the same ids as this run's frames, since the
	// platform numbers events per thread and a resumed stream may repeat them): /new must not carry
	// its position into the second thread.
	lastSeq := map[string]int{"thr_a": 9, "thr_b": 0}
	for _, th := range []string{"thr_a", "thr_b"} {
		p.on("GET", "/bots/scout/chat/threads/"+th, 200,
			fmt.Sprintf(`{"thread":{"id":%q,"title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null},"turns":[],"lastSeq":%d}`, th, lastSeq[th]))
	}
	var mu sync.Mutex
	resumeAt := map[string][]string{}
	for _, th := range []string{"thr_a", "thr_b"} {
		p.onStream("/bots/scout/chat/threads/"+th+"/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			mu.Lock()
			resumeAt[th] = append(resumeAt[th], r.Header.Get("Last-Event-ID"))
			mu.Unlock()
			writeFrames(w, []string{frame(1, "final", `"text":"ok"`), frame(2, "turn_end", `"state":"done"`)})
		})
	}
	old := cliStdin
	cliStdin = strings.NewReader("first question\nsecond\n@coder fix it\n/new\nthird\n/quit\n")
	t.Cleanup(func() { cliStdin = old })

	out, errs, err := runCLI(t, p.env(t), "chat", "scout")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	// the bot's line, its members and the budget come first
	if !strings.Contains(out, "scout (creator, online) — council tnctl/v0.3.0") {
		t.Errorf("stdout %q", out)
	}
	if !strings.Contains(out, "tokens today: 123456 of 5000000") {
		t.Errorf("stdout %q", out)
	}
	if !strings.Contains(out, "Captain") || !strings.Contains(out, "claude-sonnet-5-5") {
		t.Errorf("stdout lacks the members: %q", out)
	}
	if n := strings.Count(out, "> "); n < 6 {
		t.Errorf("the prompt was printed %d times: %q", n, out)
	}
	turnsA := p.seen("POST", "/bots/scout/chat/threads/thr_a/turns")
	turnsB := p.seen("POST", "/bots/scout/chat/threads/thr_b/turns")
	if len(turnsA) != 3 {
		t.Fatalf("%d turns in the first thread", len(turnsA))
	}
	if !strings.Contains(turnsA[0].Body, `"prompt":"first question"`) || !strings.Contains(turnsA[1].Body, `"prompt":"second"`) {
		t.Errorf("bodies %+v", turnsA)
	}
	if !strings.Contains(turnsA[2].Body, `"prompt":"fix it"`) || !strings.Contains(turnsA[2].Body, `"member":"coder"`) {
		t.Errorf("@coder body %s", turnsA[2].Body)
	}
	if len(p.seen("POST", "/bots/scout/chat/threads")) != 2 {
		t.Errorf("%d threads were created, want 2 (/new)", len(p.seen("POST", "/bots/scout/chat/threads")))
	}
	if len(turnsB) != 1 || !strings.Contains(turnsB[0].Body, `"prompt":"third"`) {
		t.Errorf("the new thread's turn %+v", turnsB)
	}
	// the second turn of the same thread reloads it for lastSeq
	if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_a")); n != 2 {
		t.Errorf("%d reloads of thr_a, want 2", n)
	}
	// The first thread continues from its last event; the thread /new started is a new thread whose
	// events start over, so its stream begins at 0 and nothing is missed.
	mu.Lock()
	got := map[string][]string{}
	for th, ids := range resumeAt {
		got[th] = append([]string{}, ids...)
	}
	mu.Unlock()
	if len(got["thr_a"]) != 3 || got["thr_a"][0] != "" || got["thr_a"][1] != "9" || got["thr_a"][2] != "9" {
		t.Errorf("Last-Event-ID per connection of thr_a %q, want [\"\", \"9\", \"9\"]", got["thr_a"])
	}
	if len(got["thr_b"]) != 1 || got["thr_b"][0] != "" {
		t.Errorf("Last-Event-ID per connection of thr_b %q, want one with none (/new keeps no position)", got["thr_b"])
	}
}

// onSeqChat answers the first requests with these bodies and repeats the last.
func (p *chatAPI) onSeqChat(method, path string, bodies []string) {
	var mu sync.Mutex
	i := 0
	p.onFunc(method, path, func(rec) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		b := bodies[min(i, len(bodies)-1)]
		i++
		return 201, b
	})
}

func (p *chatAPI) onFunc(method, path string, f func(rec) (int, string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.routes[method+" "+path] = f
}

// The REPL's /cancel cancels the thread's open turn.
func TestChatREPLCancel(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("GET", "/bots/scout/chat/threads/thr_a", 200,
		`{"thread":{"id":"thr_a","title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":null},
"turns":[{"id":"trn_open","state":"running"}],"lastSeq":3}`)
	p.on("POST", "/bots/scout/chat/threads/thr_a/turns/trn_open/cancel", 202,
		`{"turn":{"id":"trn_open","threadId":"thr_a","index":1,"kind":"ask","prompt":"p","options":{},"state":"cancelled","createdAt":1}}`)
	old := cliStdin
	cliStdin = strings.NewReader("/cancel\n/quit\n")
	t.Cleanup(func() { cliStdin = old })
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_a")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_a/turns/trn_open/cancel")); n != 1 {
		t.Errorf("%d cancels\n%s", n, errs)
	}
	if !strings.Contains(errs, "cancelling turn trn_open") {
		t.Errorf("stderr %q", errs)
	}
}

// Ctrl-C at the prompt ends the conversation with 0, as /quit does.
func TestChatREPLCtrlCAtThePromptExits0(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	sigs := make(chan os.Signal, 1)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	oldIn := cliStdin
	// the reader blocks on a pipe that never sends: Ctrl-C is what ends the conversation
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close(); _ = r.Close() })
	cliStdin = r
	t.Cleanup(func() { cliStdin = oldIn })

	go func() {
		time.Sleep(20 * time.Millisecond)
		sigs <- os.Interrupt
	}()
	_, errs, err := runCLI(t, p.env(t), "chat", "scout")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
}

// Ctrl-C during a turn sends the cancel and keeps reading to the turn's end; a second one leaves at
// once.
func TestChatCtrlCCancelsTheTurn(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	sigs := make(chan os.Signal, 2)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 202,
		`{"turn":{"id":"trn_1","threadId":"thr_new","index":1,"kind":"ask","prompt":"p","options":{},"state":"cancelling","createdAt":1}}`)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, frame(1, "text_delta", `"member":"captain","text":"start"`))
		w.(http.Flusher).Flush()
		sigs <- os.Interrupt // the person presses Ctrl-C
		// the turn ends only once the platform has the cancel
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel")); n > 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		_, _ = io.WriteString(w, frame(2, "turn_end", `"state":"cancelled"`))
		w.(http.Flusher).Flush()
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeError {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if want := "the turn was cancelled"; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if !strings.Contains(errs, "cancelling…") {
		t.Errorf("stderr %q", errs)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel")); n != 1 {
		t.Errorf("%d cancels", n)
	}
}

// A second Ctrl-C leaves at once, without waiting for the turn.
func TestChatSecondCtrlCLeavesAtOnce(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	chatCancelGrace = time.Minute // the grace must not be what ends this
	sigs := make(chan os.Signal, 2)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": open\n\n")
		w.(http.Flusher).Flush()
		sigs <- os.Interrupt
		sigs <- os.Interrupt
		<-r.Context().Done() // the turn never ends
	})
	start := time.Now()
	_, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || err.Error() != "interrupted" {
		t.Fatalf("%v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("took %s: the second Ctrl-C did not leave", d)
	}
}

// A Ctrl-C is seen while the turn waits to reconnect: two of them end the command at once, exit 1,
// instead of waiting out a pause of 10 s.
func TestChatCtrlCDuringABackoffSleep(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	chatReconnectDelay, chatReconnectMax = 10*time.Second, 10*time.Second
	sigs := make(chan os.Signal, 2)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 202, chatTurnBody)
	var once sync.Once
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		once.Do(func() {
			sigs <- os.Interrupt
			sigs <- os.Interrupt
		})
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"overloaded","message":"The chat is busy."}`)
	})
	start := time.Now()
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeError || err.Error() != "interrupted" {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s: Ctrl-C waited for the pause to reconnect", d)
	}
}

// Two Ctrl-Cs during a turn of the conversation leave it too: exit 1, and no new prompt.
func TestChatREPLSecondCtrlCLeaves(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	chatCancelGrace = time.Minute // the grace must not be what ends this
	sigs := make(chan os.Signal, 2)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, ": open\n\n")
		w.(http.Flusher).Flush()
		sigs <- os.Interrupt
		sigs <- os.Interrupt
		<-r.Context().Done() // the turn never ends
	})
	oldIn := cliStdin
	cliStdin = strings.NewReader("hello bot\n/quit\n")
	t.Cleanup(func() { cliStdin = oldIn })
	out, errs, err := runCLI(t, p.env(t), "chat", "scout")
	if err == nil || exitCodeOf(err) != codeError || err.Error() != "interrupted" {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if n := strings.Count(out, "> "); n != 1 {
		t.Errorf("the prompt was printed %d times, want 1: %q", n, out)
	}
}

// One Ctrl-C during a turn of the conversation cancels that turn only: the prompt comes back, and
// the next line runs a new turn.
func TestChatREPLCtrlCCancelsTheTurnAndKeepsGoing(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	sigs := make(chan os.Signal, 1)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("GET", "/bots/scout/chat/threads/thr_new", 200,
		`{"thread":{"id":"thr_new","title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null},"turns":[],"lastSeq":2}`)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		if conn > 1 {
			writeFrames(w, []string{frame(1, "final", `"text":"ok"`), frame(2, "turn_end", `"state":"done"`)})
			return
		}
		// the first turn: it ends only once the platform has the cancel
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, frame(1, "text_delta", `"member":"captain","text":"start"`))
		w.(http.Flusher).Flush()
		sigs <- os.Interrupt // the person presses Ctrl-C
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if len(p.seen("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel")) > 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		_, _ = io.WriteString(w, frame(2, "turn_end", `"state":"cancelled"`))
		w.(http.Flusher).Flush()
	})
	oldIn := cliStdin
	cliStdin = strings.NewReader("first\nsecond\n/quit\n")
	t.Cleanup(func() { cliStdin = oldIn })
	out, errs, err := runCLI(t, p.env(t), "chat", "scout")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if !strings.Contains(errs, "the turn was cancelled") {
		t.Errorf("stderr %q", errs)
	}
	if n := strings.Count(out, "> "); n < 3 {
		t.Errorf("the prompt came back %d times, want at least 3 (the turn was cancelled): %q", n, out)
	}
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
	if len(turns) != 2 || !strings.Contains(turns[1].Body, `"prompt":"second"`) {
		t.Errorf("turns %+v", turns)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel")); n != 1 {
		t.Errorf("%d cancels", n)
	}
}

// A turn that does not end within the cancel grace of a Ctrl-C ends that turn with its own error,
// and the conversation goes on: the person asked to stop the turn, not to leave.
func TestChatREPLCancelGraceKeepsTheConversation(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	sigs := make(chan os.Signal, 1)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })
	oldGrace := chatCancelGrace
	chatCancelGrace = 200 * time.Millisecond
	t.Cleanup(func() { chatCancelGrace = oldGrace })

	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("GET", "/bots/scout/chat/threads/thr_new", 200,
		`{"thread":{"id":"thr_new","title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null},"turns":[],"lastSeq":1}`)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		if conn > 1 {
			writeFrames(w, []string{frame(2, "final", `"text":"ok"`), frame(3, "turn_end", `"state":"done"`)})
			return
		}
		// the first turn never ends: the bot does not take the cancel
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, frame(1, "text_delta", `"member":"captain","text":"start"`))
		w.(http.Flusher).Flush()
		sigs <- os.Interrupt
		<-r.Context().Done()
	})
	oldIn := cliStdin
	cliStdin = strings.NewReader("first\nsecond\n/quit\n")
	t.Cleanup(func() { cliStdin = oldIn })
	start := time.Now()
	_, errs, err := runCLI(t, p.env(t), "chat", "scout")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if !strings.Contains(errs, "did not end within") {
		t.Errorf("stderr %q", errs)
	}
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
	if len(turns) != 2 || !strings.Contains(turns[1].Body, `"prompt":"second"`) {
		t.Errorf("turns %+v", turns)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
}

// The stream's own error frames never reset the failure count: a server that answers every connect
// with one makes six failed attempts and stops with exit 4, instead of retrying forever.
func TestChatStreamErrorsAreFailures(t *testing.T) {
	for _, code := range []string{"stream_expired", "error"} {
		t.Run(code, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
			p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
			var mu sync.Mutex
			n := 0
			p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
				mu.Lock()
				n++
				over := n > chatMaxAttempts
				mu.Unlock()
				if over {
					// the error never stops: end the turn, so a count that never grows fails the
					// test instead of hanging it
					writeFrames(w, []string{frame(1, "turn_end", `"state":"done"`)})
					return
				}
				writeFrames(w, []string{frameOf("", 0, "error", `"code":"`+code+`","message":"no"`)})
			})
			_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
			if err == nil || exitCodeOf(err) != codeTimeout {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			if want := "lost the chat stream; the turn goes on: tnctl chat scout --thread thr_new"; !strings.Contains(err.Error(), want) {
				t.Errorf("error %q, want %q", err, want)
			}
			mu.Lock()
			defer mu.Unlock()
			if n != chatMaxAttempts {
				t.Errorf("%d connections, want %d", n, chatMaxAttempts)
			}
		})
	}
}

// A stream that carried an event and then dropped is reconnected without counting as a failure:
// six failed attempts follow it before the command stops.
func TestChatDroppedWorkingStreamIsNoFailure(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	var mu sync.Mutex
	n := 0
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		n++
		first := conn == 1
		mu.Unlock()
		if first {
			// the stream works, then ends
			writeFrames(w, []string{frame(1, "text_delta", `"member":"captain","text":"half"`)})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"overloaded","message":"The chat is busy."}`)
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeTimeout {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := 1 + chatMaxAttempts; n != want {
		t.Errorf("%d connections, want %d (one that worked, then %d failures)", n, want, chatMaxAttempts)
	}
}

// A turn's error code is the bot's text too: it carries no escape to the terminal.
func TestChatTurnErrorCodeIsCleaned(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	code, err := json.Marshal("\x1b]0;pwned\x07")
	if err != nil {
		t.Fatal(err)
	}
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "error", `"code":`+string(code)+`,"message":"boom"`),
			frame(2, "turn_end", `"state":"failed"`),
		})
	})
	_, _, err = runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err == nil || exitCodeOf(err) != codeError {
		t.Fatalf("exit %d: %v", exitCodeOf(err), err)
	}
	if strings.ContainsAny(err.Error(), "\x1b\x07") {
		t.Errorf("error %q: an escape or a BEL reached the terminal", err.Error())
	}
}

// The platform's own strings never reach the terminal raw either: the bot's access and council, and
// the id of the thread it made.
func TestChatInfoAndThreadIDAreCleaned(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true) // the conversation prints the bot's line
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, `{"bot":{"name":"scout","access":"creator\u001b[2J","online":true,"council":"v1\u001b[2J"},"canChat":true,
"members":[],"budget":{"tokensToday":0,"tokensPerDay":0}}`)
	id := "thr_\x1b[2J"
	idJSON, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	p.on("POST", "/bots/scout/chat/threads", 201,
		`{"id":`+string(idJSON)+`,"title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null}`)
	path := "/bots/scout/chat/threads/" + url.PathEscape(id)
	p.on("POST", path+"/turns", 202, chatTurnBody)
	p.onStream(path+"/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{frame(1, "final", `"text":"ok"`), frame(2, "turn_end", `"state":"done"`)})
	})
	oldIn := cliStdin
	cliStdin = strings.NewReader("hi\n/quit\n")
	t.Cleanup(func() { cliStdin = oldIn })
	out, errs, err := runCLI(t, p.env(t), "chat", "scout")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if strings.ContainsAny(out, "\x1b") || strings.ContainsAny(errs, "\x1b") {
		t.Errorf("an escape reached the terminal: stdout %q, stderr %q", out, errs)
	}
	if !strings.Contains(out, "scout (creator, online) — council v1") {
		t.Errorf("stdout %q", out)
	}
	if !strings.Contains(errs, "thread thr_") {
		t.Errorf("stderr %q", errs)
	}
}

// The conversation keeps --member on every turn, and an @member line keeps --effort while it drops
// the mode: a member answers alone.
func TestChatREPLKeepsMemberAndEffort(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		lines   string
		want    []string
		notWant []string
	}{
		{"--member on every turn", []string{"--member", "coder"}, "one\ntwo\n/quit\n",
			[]string{`"member":"coder"`}, []string{`"mode"`}},
		{"@fleet drops the mode and keeps the effort", []string{"--mode", "council", "--effort", "low"}, "@fleet x\n/quit\n",
			[]string{`"member":"fleet"`, `"effort":"low"`}, []string{`"mode"`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
			p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
			p.on("GET", "/bots/scout/chat/threads/thr_new", 200,
				`{"thread":{"id":"thr_new","title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null},"turns":[],"lastSeq":2}`)
			p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
				writeFrames(w, []string{frame(1, "final", `"text":"ok"`), frame(2, "turn_end", `"state":"done"`)})
			})
			oldIn := cliStdin
			cliStdin = strings.NewReader(c.lines)
			t.Cleanup(func() { cliStdin = oldIn })
			if _, errs, err := runCLI(t, p.env(t), append([]string{"chat", "scout"}, c.args...)...); err != nil {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
			if len(turns) == 0 {
				t.Fatalf("no turns: %+v", p.reqs)
			}
			for _, b := range turns {
				for _, w := range c.want {
					if !strings.Contains(b.Body, w) {
						t.Errorf("turn body %s lacks %s", b.Body, w)
					}
				}
				for _, nw := range c.notWant {
					if strings.Contains(b.Body, nw) {
						t.Errorf("turn body %s carries %s", b.Body, nw)
					}
				}
			}
		})
	}
}

// The final starts a paragraph of its own when Captain text was printed before a reset, so it does
// not run into the last sentence of a plan.
func TestChatFinalStartsAParagraphAfterCaptainText(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "text_delta", `"member":"captain","text":"Let me check"`),
			frame(2, "plan", `"mode":"quick","tasks":[]`),
			frame(3, "final", `"text":"The answer."`),
			frame(4, "turn_end", `"state":"done"`),
		})
	})
	out, _, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Let me check\n\nThe answer.\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
}

// A malformed frame is skipped where it is, and the turn goes on: it is not a reconnect.
func TestChatSkipsAMalformedFrame(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	var mu sync.Mutex
	n := 0
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		mu.Lock()
		n++
		mu.Unlock()
		writeFrames(w, []string{
			frame(1, "text_delta", `"member":"captain","text":"half "`),
			"id: 2\nevent: text_delta\ndata: [1]\n\n",
			frame(3, "final", `"text":"half answer"`),
			frame(4, "turn_end", `"state":"done"`),
		})
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if want := "half answer\n"; out != want {
		t.Errorf("stdout %q, want %q", out, want)
	}
	if !strings.Contains(errs, "· skipped a malformed event (seq 2)") {
		t.Errorf("stderr %q", errs)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Errorf("%d connections, want 1: a malformed frame is not a reconnect", n)
	}
}

// The usage errors are refused before anything is sent.
func TestChatUsageErrors(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true) // both terminals: the conversation would be available
	p := newChatAPI(t)
	e := p.env(t)
	long := strings.Repeat("x", chatMaxPrompt+1)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"bad mode", []string{"chat", "scout", "--mode", "fast", "hi"}, "--mode must be auto, quick or council"},
		{"bad member", []string{"chat", "scout", "--member", "captain", "hi"}, "--member must be fleet or coder"},
		{"bad effort", []string{"chat", "scout", "--effort", "max", "hi"}, "--effort must be low, medium or high"},
		{"member with mode", []string{"chat", "scout", "--member", "fleet", "--mode", "council", "hi"}, "--member and --mode cannot be used together"},
		{"new with thread", []string{"chat", "scout", "--new", "--thread", "thr_1", "hi"}, "--new and --thread cannot be used together"},
		{"a prompt too long", []string{"chat", "scout", long}, "the prompt is longer than 16000 characters"},
		{"no bot", []string{"chat"}, "chat needs a bot"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := runCLI(t, e, c.args...)
			if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), c.want) {
				t.Errorf("exit %d: %v", exitCodeOf(err), err)
			}
		})
	}
	// --json needs a prompt: the conversation has none
	_, _, err := runCLI(t, e, "chat", "scout", "--json")
	if err == nil || exitCodeOf(err) != codeUsage || !strings.Contains(err.Error(), "--json needs a prompt") {
		t.Errorf("--json without a prompt: exit %d: %v", exitCodeOf(err), err)
	}
	if n := len(p.seen("GET", "/bots/scout/chat")); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// The prompt comes from stdin when there is no argument and stdin is not a terminal.
func TestChatPromptFromStdin(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, quickFrames(quickFinal))
	})
	old := cliStdin
	cliStdin = strings.NewReader("what runs here?\n")
	t.Cleanup(func() { cliStdin = old })
	out, _, err := runCLI(t, p.env(t), "chat", "scout")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if out != quickFinal+"\n" {
		t.Errorf("stdout %q", out)
	}
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
	if len(turns) != 1 || !strings.Contains(turns[0].Body, `"prompt":"what runs here?"`) {
		t.Errorf("turn body %+v", turns)
	}
}

// chat threads: a table, and --json for the platform's answer.
func TestChatThreadsTableAndJSON(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200, `{"items":[
{"id":"thr_1","title":"what is running on my robot and why is it so slow here","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":3,"lastTurn":{"id":"trn_9","state":"done"}},
{"id":"thr_2","title":"pods","origin":"web","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null}]}`)
	out, _, err := runCLI(t, p.env(t), "chat", "threads", "scout")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "TITLE", "TURNS", "LAST", "UPDATED", "thr_1", "thr_2", "done", "pods", "3"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table lacks %q:\n%s", want, out)
		}
	}
	// the title is cut to 50 characters
	if strings.Contains(out, "so slow here") {
		t.Errorf("the title is not cut:\n%s", out)
	}
	out, _, err = runCLI(t, p.env(t), "chat", "threads", "scout", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Items []struct{ ID string } }
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Items) != 2 || v.Items[0].ID != "thr_1" {
		t.Errorf("%v: %s", err, out)
	}
	// an empty list says so on stderr and prints no table
	p.on("GET", "/bots/scout/chat/threads", 200, `{"items":[]}`)
	out, errs, err := runCLI(t, p.env(t), "chat", "threads", "scout")
	if err != nil || out != "" || !strings.Contains(errs, "no chat threads with scout") {
		t.Errorf("stdout %q, stderr %q, %v", out, errs, err)
	}
}

// The chat's tables never carry a terminal escape either.
func TestChatThreadsCleanTitles(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200, `{"items":[{"id":"thr_1","title":"\u001b[31mred\u001b[0m","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":{"id":"t","state":"done"}}]}`)
	out, _, err := runCLI(t, p.env(t), "chat", "threads", "scout")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out, "\x1b") {
		t.Errorf("an escape reached stdout: %q", out)
	}
	if !strings.Contains(out, "red") {
		t.Errorf("stdout %q", out)
	}
}

// escapeLine keeps a progress line one line of the terminal.
func TestChatProgressIsOneLine(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "member_start", `"member":"fleet","text":"first\nsecond"`),
			frame(2, "turn_end", `"state":"done"`),
		})
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errs, "· fleet: first second\n") {
		t.Errorf("stderr %q", errs)
	}
}

// The Captain's text reaches stdout as it comes, one write per delta: it does not wait for the
// final.
func TestChatPrinterWritesDeltasAsTheyCome(t *testing.T) {
	var w writeRecorder
	p := &chatTurnPrinter{out: &w, errw: io.Discard, tty: true}
	p.delta("captain", "Let me ")
	if got := w.String(); got != "Let me " || w.writes != 1 {
		t.Fatalf("after the first delta: %q in %d writes", got, w.writes)
	}
	p.delta("captain", "check")
	if got := w.String(); got != "Let me check" {
		t.Errorf("after the second delta %q", got)
	}
	p.final("Let me check")
	if got, want := w.String(), "Let me check\n"; got != want {
		t.Errorf("after the final %q, want %q", got, want)
	}
	if w.writes < 3 {
		t.Errorf("%d writes: the text was not printed as it came", w.writes)
	}
}

// writeRecorder counts the writes it is given.
type writeRecorder struct {
	buf    strings.Builder
	writes int
}

func (r *writeRecorder) Write(b []byte) (int, error) {
	r.writes++
	return r.buf.Write(b)
}

func (r *writeRecorder) String() string { return r.buf.String() }

// --json writes one line of valid JSON whatever the data holds: an empty object, an object over
// several lines, keys of its own.
func TestChatJSONWriterKeepsOneLine(t *testing.T) {
	cases := []struct {
		name string
		ev   api.ChatEvent
		want string
	}{
		{"an empty object", api.ChatEvent{Seq: 1, Type: "x", Data: json.RawMessage(`{ }`)},
			`{"seq":1,"type":"x"}` + "\n"},
		{"an object over several lines", api.ChatEvent{Seq: 2, Type: "y", Data: json.RawMessage("{\"a\": {\n \"b\": 1\n}}")},
			`{"seq":2,"type":"y","a":{"b":1}}` + "\n"},
		{"keys of its own", api.ChatEvent{Seq: 3, Type: "z", Data: json.RawMessage(`{"seq": 99, "type": "other", "text": "hi"}`)},
			`{"seq":3,"type":"z","text":"hi"}` + "\n"},
		{"keys in order", api.ChatEvent{Seq: 4, Type: "w", Data: json.RawMessage(`{"b":1,"a":2}`)},
			`{"seq":4,"type":"w","a":2,"b":1}` + "\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var b bytes.Buffer
			p := &chatTurnPrinter{out: &b, json: true}
			if err := p.writeEvent(c.ev); err != nil {
				t.Fatal(err)
			}
			if got := b.String(); got != c.want {
				t.Errorf("line %q, want %q", got, c.want)
			}
			if strings.Count(strings.TrimSuffix(b.String(), "\n"), "\n") != 0 {
				t.Errorf("more than one line: %q", b.String())
			}
		})
	}
}

// cleanBotText drops CSI and OSC escapes and control characters, and keeps newlines and tabs.
func TestCleanBotText(t *testing.T) {
	cases := map[string]string{
		"\x1b[31mred\x1b[0m":          "red",
		"\x1b]0;title\x07after":       "after",
		"\x1b]8;;http://x\x1b\\link":  "link",
		"a\x07b\x00c":                 "abc",
		"keep\nthis\tand this":        "keep\nthis\tand this",
		"\x1b[1;38;2;1;2;3mmix\x1b[m": "mix",
		"plain \u009btext":            "plain text",
		"a\x1b[31":                    "a", // a CSI the text ends in
		"a\x1b]0;t":                   "a", // an OSC the text ends in
	}
	for in, want := range cases {
		if got := cleanBotText(in); got != want {
			t.Errorf("cleanBotText(%q) = %q, want %q", in, got, want)
		}
	}
}
