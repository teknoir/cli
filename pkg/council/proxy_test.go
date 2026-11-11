package council

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
