package council

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/pkg/council/model"
)

// newTestProxy is the bot API's model proxy for a test: it records the last request body and
// answers a short stream.
func newTestProxy(t *testing.T) (base string, body func() string) {
	t.Helper()
	var mu sync.Mutex
	var last string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = string(raw)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL, func() string {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// proxyError sends one streaming request to a proxy that answers status with body, and returns the
// error the model client reports. The answer says not to retry it, as the platform's proxy does.
func proxyError(t *testing.T, status int, body string) error {
	t.Helper()
	return proxyErrorWith(t, status, body, nil)
}

// proxyErrorWith is proxyError with the given headers on the answer. A nil map sends
// x-should-retry: false. A case without that header gets Retry-After-Ms: 0 instead, so the SDK's
// own retries come back at once.
func proxyErrorWith(t *testing.T, status int, body string, headers map[string]string) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if headers == nil {
			w.Header().Set("x-should-retry", "false")
		}
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	c, err := model.New(model.Config{APIBase: server.URL, KeyFile: testKeyFile(t), Version: "test"})
	if err != nil {
		t.Fatalf("model.New: %v", err)
	}
	req := model.Request{
		Model:     model.ModelSonnet,
		Effort:    "medium",
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
		MaxTokens: 16,
	}
	_, _, serr := c.Stream(context.Background(), req, nil)
	if serr == nil {
		t.Fatalf("Stream reported no error for the %d", status)
	}
	return serr
}

// TestModelErrorOutcomeProxy: the proxy's own refusals are told apart from a model that could not
// be reached, and its message reaches the person.
func TestModelErrorOutcomeProxy(t *testing.T) {
	const today = "This bot used its 250000 tokens for today (UTC). It can continue tomorrow."
	const noKey = "The teamspace's model key was refused by the provider. Ask a teamspace admin."
	// retried is an answer that does not say whether to retry it: the gateway's own 503, or a
	// proxied model that failed.
	retried := map[string]string{"Retry-After-Ms": "0"}
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		code    string
		msg     string
	}{
		{
			name: "no model", status: http.StatusServiceUnavailable,
			body: `{"type":"error","error":{"type":"api_error"}}`,
			code: codeModelUnavailable, msg: "The model is not available.",
		},
		{
			name: "no model with a message", status: http.StatusServiceUnavailable,
			body: `{"type":"error","error":{"type":"api_error","message":"This bot has no model yet."}}`,
			code: codeModelUnavailable, msg: "This bot has no model yet.",
		},
		{
			name: "the gateway is down", status: http.StatusServiceUnavailable,
			body:    "no healthy upstream",
			headers: retried,
			code:    codeModelError, msg: "The model could not be reached (status 503).",
		},
		{
			name: "the model key was refused", status: http.StatusBadGateway,
			body: `{"type":"error","error":{"type":"api_error","message":"` + noKey + `"}}`,
			code: codeModelUnavailable, msg: noKey,
		},
		{
			name: "tokens for today", status: http.StatusTooManyRequests,
			body: `{"type":"error","error":{"type":"rate_limit_error","message":"` + today + `"}}`,
			code: codeModelBudget, msg: today,
		},
		{
			name: "busy", status: http.StatusTooManyRequests,
			body: `{"type":"error","error":{"type":"rate_limit_error","message":"Too many model calls at once."}}`,
			code: codeModelBusy, msg: "Too many model calls at once.",
		},
		{
			name: "busy without a message", status: http.StatusTooManyRequests,
			body: `{"type":"error","error":{"type":"rate_limit_error"}}`,
			code: codeModelBusy, msg: "The model is busy: try again in a minute.",
		},
		{
			name: "rejected", status: http.StatusBadRequest,
			body: `{"type":"error","error":{"type":"invalid_request_error","message":"Only claude-sonnet-5-5 may\u0007 be asked for.\nUse it."}}`,
			code: codeModelRejected, msg: "The platform refused the model request: Only claude-sonnet-5-5 may be asked for. Use it.",
		},
		{
			name: "rejected without a message", status: http.StatusBadRequest,
			body: `{"type":"error","error":{"type":"invalid_request_error"}}`,
			code: codeModelRejected, msg: "The platform refused the model request.",
		},
		{
			name: "a failure that said not to retry", status: http.StatusInternalServerError,
			body: `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
			code: codeModelUnavailable, msg: "boom",
		},
		{
			name: "a failure with a message", status: http.StatusInternalServerError,
			body:    `{"type":"error","error":{"type":"api_error","message":"boom"}}`,
			headers: retried,
			code:    codeModelError, msg: "The model could not be reached (status 500): boom",
		},
		{
			name: "a failure without a message", status: http.StatusInternalServerError,
			body:    `{"type":"error","error":{"type":"api_error"}}`,
			headers: retried,
			code:    codeModelError, msg: "The model could not be reached (status 500).",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := modelErrorOutcome(proxyErrorWith(t, tc.status, tc.body, tc.headers))
			if o.state != stateFailed || o.errCode != tc.code || o.errMsg != tc.msg {
				t.Errorf("outcome %q %q %q, want %q %q", o.state, o.errCode, o.errMsg, tc.code, tc.msg)
			}
		})
	}
}

// TestModelErrorOutcomeProxyMessageBounded: the proxy's message is cut to 300 runes.
func TestModelErrorOutcomeProxyMessageBounded(t *testing.T) {
	long := strings.Repeat("é", 400)
	o := modelErrorOutcome(proxyError(t, http.StatusBadRequest,
		`{"type":"error","error":{"type":"invalid_request_error","message":"`+long+`"}}`))
	const head = "The platform refused the model request: "
	got := strings.TrimPrefix(o.errMsg, head)
	if got == o.errMsg {
		t.Fatalf("message %q", o.errMsg)
	}
	if n := len([]rune(got)); n != maxProxyMessage {
		t.Errorf("the message is %d runes, want %d", n, maxProxyMessage)
	}
}

// testKeyFile writes a bot key file for a test.
func testKeyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bot-key")
	if err := os.WriteFile(p, []byte("test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// streamOnce sends one streaming request through the proxy with the system prompt and tools given.
func streamOnce(t *testing.T, base, system string, tools []anthropic.ToolUnionParam) {
	t.Helper()
	c, err := model.New(model.Config{APIBase: base, KeyFile: testKeyFile(t), Version: "test"})
	if err != nil {
		t.Fatalf("model.New: %v", err)
	}
	req := model.Request{
		Model:     model.ModelSonnet,
		Effort:    "medium",
		System:    system,
		Tools:     tools,
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
		MaxTokens: 1024,
	}
	if _, _, err := c.Stream(context.Background(), req, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
}
