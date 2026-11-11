package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// councilServer answers with answer and records the request it saw.
type councilCall struct {
	Method, Path, Query, Body, LastEventID string
}

func councilServer(t *testing.T, status int, answer string, calls *[]councilCall) *Fleet {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*calls = append(*calls, councilCall{
			Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.RawQuery,
			Body: string(b), LastEventID: r.Header.Get("Last-Event-ID"),
		})
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization %q", r.Header.Get("Authorization"))
		}
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(ts.Close)
	return &Fleet{Base: ts.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
}

func TestChatInfoRoute(t *testing.T) {
	var calls []councilCall
	const body = `{"bot":{"namespace":"teknoir-demo","name":"scout","uid":"u1","access":"read","phase":"Running","online":true,
"lastSeen":1794391881385,"council":"tnctl/v0.3.0"},"canChat":true,"isCreator":false,
"members":[{"id":"captain","name":"Captain","model":"claude-sonnet-5-5","effort":"high","job":"runs the council","runtime":"OpenCode"}],
"budget":{"tokensToday":123,"tokensPerDay":5000000}}`
	f := councilServer(t, 200, body, &calls)
	info, raw, err := f.ChatInfo(context.Background(), "scout")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "GET" || calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat" {
		t.Fatalf("requests %+v", calls)
	}
	if info.Bot.Name != "scout" || !info.CanChat || info.IsCreator || *info.Bot.Council != "tnctl/v0.3.0" ||
		len(info.Members) != 1 || info.Members[0].Runtime != "OpenCode" || info.Budget.TokensPerDay != 5000000 {
		t.Errorf("%+v", info)
	}
	if !json.Valid(raw) {
		t.Errorf("raw %s", raw)
	}
}

// A bot without a computer has a null phase, and one never seen has a null lastSeen.
func TestChatInfoNullables(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"bot":{"name":"scout","phase":null,"lastSeen":null,"council":null},"canChat":false,"reason":"the bot has no computer yet"}`, &calls)
	info, _, err := f.ChatInfo(context.Background(), "scout")
	if err != nil {
		t.Fatal(err)
	}
	if info.Bot.Phase != nil || info.Bot.LastSeen != nil || info.Bot.Council != nil || info.CanChat {
		t.Errorf("%+v", info)
	}
	if info.Reason == "" {
		t.Error("the reason is lost")
	}
}

func TestChatThreadsRoute(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"items":[{"id":"thr_1","title":"hi","origin":"cli","member":null,"author":{"ref":"user:default/a","email":"a@b.c"},
"createdAt":1,"updatedAt":2,"turnCount":3,"lastTurn":{"id":"trn_9","state":"done"}}]}`, &calls)
	threads, raw, err := f.ChatThreads(context.Background(), "scout")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat/threads" || calls[0].Method != "GET" {
		t.Fatalf("%+v", calls)
	}
	if len(threads) != 1 || threads[0].ID != "thr_1" || threads[0].TurnCount != 3 || threads[0].LastTurn == nil || threads[0].LastTurn.State != "done" {
		t.Errorf("%+v", threads)
	}
	if len(raw) == 0 {
		t.Error("no raw answer")
	}
	// an empty list is an empty list, not an error
	var empty []councilCall
	f2 := councilServer(t, 200, `{"items":[]}`, &empty)
	none, _, err := f2.ChatThreads(context.Background(), "scout")
	if err != nil || len(none) != 0 {
		t.Errorf("%v %+v", err, none)
	}
}

func TestCreateChatThreadRoutes(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 201, `{"id":"thr_2","title":"t","origin":"cli","member":null,"createdAt":0,"updatedAt":0,"turnCount":0,"lastTurn":null}`, &calls)
	th, _, err := f.CreateChatThread(context.Background(), "scout", NewChatThread{Title: "what runs?", Origin: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if th.ID != "thr_2" {
		t.Errorf("%+v", th)
	}
	if len(calls) != 1 || calls[0].Method != "POST" || calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat/threads" {
		t.Fatalf("%+v", calls)
	}
	if calls[0].Body != `{"title":"what runs?","origin":"cli"}` {
		t.Errorf("body %s (the empty member must be left out)", calls[0].Body)
	}
	// every field empty: an empty object
	calls = nil
	if _, _, err := f.CreateChatThread(context.Background(), "scout", NewChatThread{}); err != nil {
		t.Fatal(err)
	}
	if calls[0].Body != `{}` {
		t.Errorf("body %s", calls[0].Body)
	}
}

func TestChatThreadRoute(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"thread":{"id":"thr_1","title":"t","origin":"cli","member":null,"createdAt":0,"updatedAt":0,"turnCount":1,"lastTurn":null},
"turns":[{"id":"trn_1","threadId":"thr_1","index":1,"kind":"ask","member":null,"prompt":"p","options":{"mode":"quick"},
"state":"done","final":"the answer","error":null,"usage":{"calls":2,"input":10,"output":20,"cacheRead":0,"cacheWrite":0,"ms":1500},
"author":{"ref":"user:default/a"},"createdAt":0,"startedAt":1,"finishedAt":2}],"lastSeq":42}`, &calls)
	d, _, err := f.ChatThread(context.Background(), "scout", "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat/threads/thr_1" || calls[0].Method != "GET" {
		t.Fatalf("%+v", calls)
	}
	if d.LastSeq != 42 || len(d.Turns) != 1 || *d.Turns[0].Final != "the answer" || d.Turns[0].Usage == nil || d.Turns[0].Usage.MS != 1500 {
		t.Errorf("%+v", d)
	}
}

// A turn's member and error are nullable too.
func TestChatTurnNullables(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{"thread":{},"turns":[{"id":"trn_1","member":null,"final":null,"error":null,"usage":null,"startedAt":null,"finishedAt":null}],"lastSeq":0}`, &calls)
	d, _, err := f.ChatThread(context.Background(), "scout", "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	tu := d.Turns[0]
	if tu.Member != nil || tu.Final != nil || tu.Error != nil || tu.Usage != nil || tu.StartedAt != nil {
		t.Errorf("%+v", tu)
	}
}

// Path segments are escaped: a bot name is one segment, whatever it holds.
func TestChatPathsAreEscaped(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 200, `{}`, &calls)
	ctx := context.Background()
	_, _, _ = f.ChatInfo(ctx, "a/b")
	_, _, _ = f.ChatThread(ctx, "scout", "x y")
	_, _, _ = f.PostChatTurn(ctx, "scout", "t/1", NewChatTurn{Prompt: "p"})
	_, _, _ = f.CancelChatTurn(ctx, "scout", "t1", "turn/1")
	_, _, _ = f.CreateChatThread(ctx, "scout", NewChatThread{})
	_, _, _ = f.ChatThreads(ctx, "scout")
	_ = f.DeleteChatThread(ctx, "scout", "thr_1")
	var paths []string
	for _, c := range calls {
		paths = append(paths, c.Method+" "+c.Path)
	}
	want := []string{
		"GET /api/device/fleet/teknoir-demo/bots/a%2Fb/chat",
		"GET /api/device/fleet/teknoir-demo/bots/scout/chat/threads/x%20y",
		"POST /api/device/fleet/teknoir-demo/bots/scout/chat/threads/t%2F1/turns",
		"POST /api/device/fleet/teknoir-demo/bots/scout/chat/threads/t1/turns/turn%2F1/cancel",
		"POST /api/device/fleet/teknoir-demo/bots/scout/chat/threads",
		"GET /api/device/fleet/teknoir-demo/bots/scout/chat/threads",
		"DELETE /api/device/fleet/teknoir-demo/bots/scout/chat/threads/thr_1",
	}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Errorf("paths\n%s\nwant\n%s", strings.Join(paths, "\n"), strings.Join(want, "\n"))
	}
}

func TestPostAndCancelChatTurn(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 202, `{"turn":{"id":"trn_5","threadId":"thr_1","index":2,"kind":"ask","member":null,"prompt":"p","options":{"mode":"council","effort":"high"},"state":"queued","final":null,"error":null,"usage":null,"author":{"ref":"r"},"createdAt":0,"startedAt":null,"finishedAt":null}}`, &calls)
	ctx := context.Background()
	tu, _, err := f.PostChatTurn(ctx, "scout", "thr_1", NewChatTurn{Prompt: "p", Mode: "council", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if tu.ID != "trn_5" || tu.State != "queued" {
		t.Errorf("%+v", tu)
	}
	if calls[0].Method != "POST" || calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat/threads/thr_1/turns" ||
		calls[0].Body != `{"prompt":"p","mode":"council","effort":"high"}` {
		t.Errorf("%+v", calls[0])
	}
	// a member turn leaves mode out
	if _, _, err := f.PostChatTurn(ctx, "scout", "thr_1", NewChatTurn{Prompt: "p", Member: "coder"}); err != nil {
		t.Fatal(err)
	}
	if calls[1].Body != `{"prompt":"p","member":"coder"}` {
		t.Errorf("body %s", calls[1].Body)
	}
	tu, _, err = f.CancelChatTurn(ctx, "scout", "thr_1", "trn_5")
	if err != nil {
		t.Fatal(err)
	}
	if calls[2].Method != "POST" || calls[2].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat/threads/thr_1/turns/trn_5/cancel" {
		t.Errorf("%+v", calls[2])
	}
	if tu.ID != "trn_5" {
		t.Errorf("%+v", tu)
	}
}

func TestDeleteChatThread(t *testing.T) {
	var calls []councilCall
	f := councilServer(t, 204, "", &calls)
	if err := f.DeleteChatThread(context.Background(), "scout", "thr_1"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Method != "DELETE" || calls[0].Path != "/api/device/fleet/teknoir-demo/bots/scout/chat/threads/thr_1" {
		t.Errorf("%+v", calls)
	}
}

func TestChatEventsFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept %q", r.Header.Get("Accept"))
		}
		if r.Header.Get("Last-Event-ID") != "" {
			t.Errorf("Last-Event-ID %q on a stream from 0", r.Header.Get("Last-Event-ID"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": open\n\n")
		_, _ = io.WriteString(w, "id: 11\nevent: turn_start\ndata: {\"turnId\":\"trn_1\",\"mode\":\"council\",\"at\":\"2026-11-11T10:00:00Z\"}\n\n")
		_, _ = io.WriteString(w, "id: 12\nevent: text_delta\ndata: {\"turnId\":\"trn_1\",\"member\":\"captain\",\"text\":\"Hel\"}\n\n")
		// the stream's own error: no id, no turnId
		_, _ = io.WriteString(w, "event: error\ndata: {\"code\":\"stream_expired\",\"message\":\"expired\"}\n\n")
	}))
	defer srv.Close()
	f := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	s, err := f.ChatEvents(context.Background(), "scout", "thr_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var got []ChatEvent
	for {
		ev, err := s.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Next: %v", err)
			}
			break
		}
		got = append(got, ev)
	}
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].Seq != 11 || got[0].Type != "turn_start" || got[0].TurnID != "trn_1" || !strings.Contains(string(got[0].Data), `"mode":"council"`) {
		t.Errorf("%+v", got[0])
	}
	if got[1].Seq != 12 || got[1].Type != "text_delta" || got[1].TurnID != "trn_1" {
		t.Errorf("%+v", got[1])
	}
	if got[2].Seq != 0 || got[2].Type != "error" || got[2].TurnID != "" {
		t.Errorf("%+v", got[2])
	}
}

// After a sequence number the stream resumes from it with Last-Event-ID.
func TestChatEventsResumesWithLastEventID(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 13\nevent: turn_end\ndata: {\"turnId\":\"trn_1\",\"state\":\"done\"}\n\n")
	}))
	defer srv.Close()
	f := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	s, err := f.ChatEvents(context.Background(), "scout", "thr_1", 12)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if ev, err := s.Next(); err != nil || ev.Seq != 13 || ev.Type != "turn_end" {
		t.Fatalf("%+v %v", ev, err)
	}
	if got != "12" {
		t.Errorf("Last-Event-ID %q", got)
	}
}

// A data line that is not a JSON object is an error from Next, never a panic; so is a broken object.
func TestChatEventsMalformedData(t *testing.T) {
	for _, body := range []string{"event: x\ndata: not-json\n\n", "event: x\ndata: [1,2]\n\n", "event: x\ndata: {\"a\":\n\n"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, body)
		}))
		f := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
		s, err := f.ChatEvents(context.Background(), "scout", "thr_1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Next(); err == nil || !strings.Contains(err.Error(), "malformed chat event") {
			t.Errorf("%q: %v", body, err)
		}
		_ = s.Close()
		srv.Close()
	}
}

// A malformed frame comes back with its sequence number and type, so a reader can move past it
// instead of replaying it on every reconnect.
func TestChatEventsMalformedFrameKeepsItsPosition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 7\nevent: text_delta\ndata: [1]\n\n")
		_, _ = io.WriteString(w, "id: 8\nevent: final\ndata: {\"turnId\":\n\n")
	}))
	defer srv.Close()
	f := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	s, err := f.ChatEvents(context.Background(), "scout", "thr_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, want := range []struct {
		seq int64
		typ string
	}{
		{7, "text_delta"},
		{8, "final"},
	} {
		ev, err := s.Next()
		if !errors.Is(err, ErrMalformedChatEvent) || !strings.Contains(err.Error(), "malformed chat event") {
			t.Fatalf("%q: %v", want.typ, err)
		}
		if ev.Seq != want.seq || ev.Type != want.typ {
			t.Errorf("%q: %+v", want.typ, ev)
		}
	}
}

// A refused stream is an *Error with the platform's code.
func TestChatEventsErrorAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"chat_not_allowed","message":"Ask a teamspace admin."}`)
	}))
	defer srv.Close()
	f := &Fleet{Base: srv.URL + "/api/device/fleet/teknoir-demo", Token: func(context.Context) (string, error) { return "tok", nil }}
	_, err := f.ChatEvents(context.Background(), "scout", "thr_1", 0)
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != 403 || ae.Code != "chat_not_allowed" {
		t.Fatalf("%T %v", err, err)
	}
}

// The chat's own routes answer *Error too: council_off is a 404 whose code names it.
func TestChatInfoErrors(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
	}{
		{404, `{"code":"council_off","message":"the chat is off"}`},
		{403, `{"code":"chat_not_allowed","message":"not for you"}`},
		{429, `{"code":"too_many_turns","message":"slow down"}`},
	} {
		var calls []councilCall
		f := councilServer(t, c.status, c.body, &calls)
		_, _, err := f.ChatInfo(context.Background(), "scout")
		var ae *Error
		if !errors.As(err, &ae) || ae.Status != c.status {
			t.Errorf("%d: %T %v", c.status, err, err)
		}
	}
}
