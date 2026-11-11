package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// keyFile writes a key file for a test and returns its path.
func keyFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bot-key")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// recorded is one request the test proxy saw.
type recorded struct {
	method string
	path   string
	header http.Header
	body   map[string]any
}

// proxy is a bot API model proxy for a test: it records every request and answers with the
// function given.
type proxy struct {
	t      *testing.T
	server *httptest.Server

	mu   sync.Mutex
	reqs []recorded

	handle func(w http.ResponseWriter, r *http.Request, n int)
}

func newProxy(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, n int)) *proxy {
	t.Helper()
	p := &proxy{t: t, handle: handle}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		p.reqs = append(p.reqs, recorded{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		n := len(p.reqs)
		p.mu.Unlock()
		if p.handle != nil {
			p.handle(w, r, n)
			return
		}
		writeSSE(t, w, []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":11,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		})
	}))
	t.Cleanup(p.server.Close)
	return p
}

func (p *proxy) base() string { return p.server.URL }

func (p *proxy) requests() []recorded {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recorded(nil), p.reqs...)
}

func (p *proxy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// writeSSE answers with server-sent events.
func writeSSE(t *testing.T, w http.ResponseWriter, events []string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for i, ev := range events {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType(ev), ev)
		_ = i
	}
}

// eventType reads the "type" of an event's JSON, which the SDK does not need from the
// "event:" line but a real proxy sends.
func eventType(ev string) string {
	var v struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(ev), &v)
	return v.Type
}

// simple is the base request of the tests below.
func simple() Request {
	return Request{
		Turn:      "trn_abc-1",
		Model:     ModelSonnet,
		Effort:    "medium",
		System:    "you are the council",
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
		MaxTokens: 1024,
	}
}

func newClient(t *testing.T, apiBase, keyPath string) *Client {
	t.Helper()
	c, err := New(Config{APIBase: apiBase, KeyFile: keyPath, Version: "1.2.3"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestStreamRequestPathAndHeaders(t *testing.T) {
	p := newProxy(t, nil)
	c := newClient(t, p.base(), keyFile(t, "s3cret-key\n"))

	var got []string
	msg, usage, err := c.Stream(context.Background(), simple(), func(s string) { got = append(got, s) })
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if msg == nil || msg.ID != "msg_1" {
		t.Fatalf("message = %+v", msg)
	}
	reqs := p.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	r := reqs[0]
	if r.path != "/model/v1/messages" {
		t.Errorf("path = %q, want /model/v1/messages", r.path)
	}
	if got := r.header.Get("X-Api-Key"); got != "s3cret-key" {
		t.Errorf("X-Api-Key = %q, want the key file's content, trimmed", got)
	}
	if got := r.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
	if ua := r.header.Get("User-Agent"); !strings.HasPrefix(ua, "tnctl/") || !strings.Contains(ua, "council") {
		t.Errorf("User-Agent = %q", ua)
	}
	if got := r.header.Get("x-teknoir-turn"); got != "trn_abc-1" {
		t.Errorf("x-teknoir-turn = %q, want trn_abc-1", got)
	}
	// The streamed answer.
	if want := []string{"Hel", "lo"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("text deltas = %q, want %q in order", got, want)
	}
	var text strings.Builder
	for _, b := range msg.Content {
		if b.Type == "text" {
			text.WriteString(b.Text)
		}
	}
	if text.String() != "Hello" {
		t.Errorf("message text = %q, want Hello", text.String())
	}
	if usage.Calls != 1 || usage.Input != 11 || usage.Output != 9 || usage.CacheRead != 3 || usage.CacheWrite != 4 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestStreamTurnHeaderOmittedForBadValue(t *testing.T) {
	p := newProxy(t, nil)
	c := newClient(t, p.base(), keyFile(t, "k"))
	req := simple()
	req.Turn = "bad value!"
	if _, _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := p.requests()[0].header.Get("x-teknoir-turn"); got != "" {
		t.Errorf("x-teknoir-turn = %q, want it absent for a value that is not a header token", got)
	}
}

func TestStreamIgnoresAnthropicEnvironment(t *testing.T) {
	p := newProxy(t, nil)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a request reached the ANTHROPIC_BASE_URL server: %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer other.Close()

	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	t.Setenv("ANTHROPIC_BASE_URL", other.URL)

	c := newClient(t, p.base(), keyFile(t, "file-key"))
	if _, _, err := c.Stream(context.Background(), simple(), nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	reqs := p.requests()
	if len(reqs) != 1 {
		t.Fatalf("the bot API's proxy got %d requests, want 1 (no environment may redirect or authenticate)", len(reqs))
	}
	r := reqs[0]
	if got := r.header.Get("X-Api-Key"); got != "file-key" {
		t.Errorf("X-Api-Key = %q, want the key file's content", got)
	}
	if got := r.header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestStreamReadsKeyFileEveryCall(t *testing.T) {
	p := newProxy(t, nil)
	kf := keyFile(t, "key-one")
	c := newClient(t, p.base(), kf)
	if _, _, err := c.Stream(context.Background(), simple(), nil); err != nil {
		t.Fatalf("first Stream: %v", err)
	}
	if err := os.WriteFile(kf, []byte("key-two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Stream(context.Background(), simple(), nil); err != nil {
		t.Fatalf("second Stream: %v", err)
	}
	reqs := p.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	if got := reqs[0].header.Get("X-Api-Key"); got != "key-one" {
		t.Errorf("first X-Api-Key = %q", got)
	}
	if got := reqs[1].header.Get("X-Api-Key"); got != "key-two" {
		t.Errorf("second X-Api-Key = %q, want the replaced key", got)
	}
}

func TestStreamKeyFileFailures(t *testing.T) {
	p := newProxy(t, nil)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-key") }},
		{"empty", func(t *testing.T) string { return keyFile(t, "  \n\t ") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClient(t, p.base(), tc.setup(t))
			_, _, err := c.Stream(context.Background(), simple(), nil)
			if err == nil {
				t.Fatal("Stream succeeded, want an error for a missing or empty key file")
			}
			if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "key-one") {
				t.Errorf("error shows key content: %v", err)
			}
			if strings.Contains(err.Error(), "no-such-key") == false && tc.name == "missing" {
				t.Errorf("error does not name the file: %v", err)
			}
		})
	}
	if n := p.count(); n != 0 {
		t.Errorf("the proxy got %d requests, want none without a key", n)
	}
}

func TestStreamRequestBody(t *testing.T) {
	p := newProxy(t, nil)
	c := newClient(t, p.base(), keyFile(t, "k"))

	tools := []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
		Name:        "whoami",
		Description: anthropic.String("who"),
		InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
	}}}
	req := simple()
	req.Effort = "high"
	req.Tools = tools
	req.Format = map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}}
	if _, _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body := p.requests()[0].body

	thinking, _ := body["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking = %v, want type adaptive", body["thinking"])
	}
	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "high" {
		t.Errorf("output_config.effort = %v, want high", oc["effort"])
	}
	format, _ := oc["format"].(map[string]any)
	if format["type"] != "json_schema" {
		t.Errorf("output_config.format = %v, want a json_schema", oc["format"])
	}
	if _, ok := format["schema"].(map[string]any); !ok {
		t.Errorf("output_config.format.schema = %v", format["schema"])
	}
	tc, _ := body["tool_choice"].(map[string]any)
	if tc["type"] != "auto" {
		t.Errorf("tool_choice = %v, want {type: auto}", body["tool_choice"])
	}
	sys, _ := body["system"].([]any)
	if len(sys) != 1 {
		t.Fatalf("system = %v, want one block", body["system"])
	}
	sysBlock := sys[0].(map[string]any)
	if cc, _ := sysBlock["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("system cache_control = %v, want ephemeral", sysBlock["cache_control"])
	}
	gotTools, _ := body["tools"].([]any)
	if len(gotTools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	tool := gotTools[0].(map[string]any)
	if tool["name"] != "whoami" {
		t.Errorf("tools[0].name = %v, want whoami", tool["name"])
	}
	if cc, _ := tool["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("the last tool's cache_control = %v, want ephemeral", tool["cache_control"])
	}
}

func TestStreamRequestBodyWithoutToolsOrFormat(t *testing.T) {
	p := newProxy(t, nil)
	c := newClient(t, p.base(), keyFile(t, "k"))
	if _, _, err := c.Stream(context.Background(), simple(), nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	body := p.requests()[0].body
	if _, ok := body["tool_choice"]; ok {
		t.Errorf("tool_choice = %v, want it absent without tools", body["tool_choice"])
	}
	if _, ok := body["tools"]; ok {
		t.Errorf("tools = %v, want it absent", body["tools"])
	}
	oc, _ := body["output_config"].(map[string]any)
	if _, ok := oc["format"]; ok {
		t.Errorf("output_config.format = %v, want it absent without Format", oc["format"])
	}
	if oc["effort"] != "medium" {
		t.Errorf("output_config.effort = %v, want medium", oc["effort"])
	}
	// Only one cache breakpoint besides the system block: nothing else may carry it.
	if raw, _ := json.Marshal(body); strings.Count(string(raw), `"cache_control"`) != 1 {
		t.Errorf("body has %d cache_control breakpoints, want 1 (the system block): %s", strings.Count(string(raw), `"cache_control"`), raw)
	}
}

func TestStreamValidation(t *testing.T) {
	p := newProxy(t, nil)
	c := newClient(t, p.base(), keyFile(t, "k"))
	for _, tc := range []struct {
		name string
		edit func(*Request)
	}{
		{"unknown model", func(r *Request) { r.Model = "claude-opus-5-5" }},
		{"unknown effort", func(r *Request) { r.Effort = "xhigh" }},
		{"max tokens 0", func(r *Request) { r.MaxTokens = 0 }},
		{"max tokens 32001", func(r *Request) { r.MaxTokens = 32001 }},
		{"no messages", func(r *Request) { r.Messages = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := simple()
			tc.edit(&req)
			if _, _, err := c.Stream(context.Background(), req, nil); err == nil {
				t.Fatalf("Stream accepted %s", tc.name)
			}
		})
	}
	if n := p.count(); n != 0 {
		t.Errorf("the proxy got %d requests, want none for a refused request", n)
	}
}

func TestStreamRetriesOn429(t *testing.T) {
	p := newProxy(t, func(w http.ResponseWriter, r *http.Request, n int) {
		if n == 1 {
			w.Header().Set("retry-after", "0")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
			return
		}
		writeSSE(t, w, []string{
			`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		})
	})
	c := newClient(t, p.base(), keyFile(t, "k"))
	msg, _, err := c.Stream(context.Background(), simple(), nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if msg.ID != "msg_2" {
		t.Errorf("message id = %q, want the retried answer", msg.ID)
	}
	if n := p.count(); n != 2 {
		t.Errorf("requests = %d, want 2 (one 429, one success)", n)
	}
}

func TestNewBaseRules(t *testing.T) {
	kf := keyFile(t, "k")
	for _, base := range []string{"http://example.com", "ftp://example.com", "example.com", ""} {
		if _, err := New(Config{APIBase: base, KeyFile: kf, Version: "v"}); err == nil {
			t.Errorf("New accepted the base %q", base)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	if _, err := New(Config{APIBase: srv.URL, KeyFile: kf, Version: "v"}); err != nil {
		t.Errorf("New refused a loopback http base: %v", err)
	}
	if _, err := New(Config{APIBase: "https://tb-api.teknoir.cloud:8085/api/device/bot/v1", KeyFile: kf, Version: "v"}); err != nil {
		t.Errorf("New refused https: %v", err)
	}
	if _, err := New(Config{APIBase: srv.URL, KeyFile: "", Version: "v"}); err == nil {
		t.Error("New accepted an empty key file")
	}
}

// TestNewDoesNotFollowRedirects: the bot's key must not travel past the base, so a 3xx is an
// answer (and an error), not a redirect to follow.
func TestNewDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		writeSSE(t, w, []string{`{"type":"message_stop"}`})
	}))
	defer other.Close()
	var baseHits atomic.Int32
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		baseHits.Add(1)
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer base.Close()

	caller := &http.Client{Timeout: 5 * time.Second}
	c, err := New(Config{APIBase: base.URL, KeyFile: keyFile(t, "k"), Version: "v", HTTPClient: caller})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, _, err = c.Stream(context.Background(), simple(), nil)
	if err == nil {
		t.Fatal("Stream reported no error for a 307")
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusTemporaryRedirect {
		t.Errorf("Stream = %v, want a StatusError with the 307", err)
	}
	if n := baseHits.Load(); n != 1 {
		t.Errorf("the base got %d requests, want 1 (a redirect is not retried)", n)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect target got %d requests, want none", n)
	}
	if caller.CheckRedirect != nil {
		t.Error("New changed the caller's client")
	}
}

func TestUsageAdd(t *testing.T) {
	var u Usage
	u.Add(Usage{Calls: 1, Input: 10, Output: 5, CacheRead: 2, CacheWrite: 1})
	u.Add(Usage{Calls: 1, Input: 1})
	if u != (Usage{Calls: 2, Input: 11, Output: 5, CacheRead: 2, CacheWrite: 1}) {
		t.Errorf("usage = %+v", u)
	}
}
