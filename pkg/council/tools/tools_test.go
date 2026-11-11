package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// calls counts the tool handler calls of a test server.
type calls struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *calls) add(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[name]++
}

func (c *calls) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[name]
}

// textTool is a tool that answers with fixed text.
func textTool(s *mcp.Server, c *calls, name, desc string, schema any, answer string) {
	s.AddTool(&mcp.Tool{Name: name, Description: desc, InputSchema: schema},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c.add(name)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: answer}}}, nil
		})
}

// testServer is an MCP server with the four tools of the test.
func testServer(t *testing.T, c *calls) *mcp.Server {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	obj := map[string]any{"type": "object", "properties": map[string]any{}}
	textTool(s, c, "whoami", "Who this tnctl works as.", obj, "the bot")
	textTool(s, c, "list_robots", "The robots of the teamspace.", obj, "robot-a, robot-b")
	textTool(s, c, "inspect_logs", "The log of a pod on a robot.", map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"name": map[string]any{"type": "string"}, "tail": map[string]any{"type": "integer"}},
		"required":             []any{"name"},
		"additionalProperties": false,
	}, "log line")
	textTool(s, c, "get_config", "The effective configuration.", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"target": map[string]any{
				"type":       "object",
				"properties": map[string]any{"kind": map[string]any{"type": "string"}},
			},
		},
		"required":             []any{"target"},
		"additionalProperties": false,
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
	}, "config")
	return s
}

func newSet(t *testing.T, s *mcp.Server) *Set {
	t.Helper()
	set, err := New(context.Background(), s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

func TestNames(t *testing.T) {
	var c calls
	set := newSet(t, testServer(t, &c))
	for _, tc := range []struct {
		level string
		want  []string
	}{
		{LevelNone, []string{"whoami"}},
		{LevelRead, []string{"get_config", "list_robots", "whoami"}},
		{LevelCreator, []string{"get_config", "inspect_logs", "list_robots", "whoami"}},
		{"unknown", []string{"whoami"}},
	} {
		if got := set.Names(tc.level); !equal(got, tc.want) {
			t.Errorf("Names(%q) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

func TestFor(t *testing.T) {
	var c calls
	set := newSet(t, testServer(t, &c))

	all := set.For(LevelCreator)
	if len(all) != 4 {
		t.Fatalf("For(creator) has %d tools, want 4", len(all))
	}
	var names []string
	for _, u := range all {
		names = append(names, u.OfTool.Name)
	}
	if !equal(names, []string{"get_config", "inspect_logs", "list_robots", "whoami"}) {
		t.Errorf("For(creator) order = %v, want sorted by name", names)
	}
	for i, u := range all {
		ephemeral := hasCacheControl(t, u.OfTool)
		if i == len(all)-1 && !ephemeral {
			t.Errorf("the last tool (%s) has no cache_control", u.OfTool.Name)
		}
		if i < len(all)-1 && ephemeral {
			t.Errorf("tool %d (%s) has cache_control; only the last one may", i, u.OfTool.Name)
		}
	}

	// Schema conversion: properties, required, and every other key kept as an extra field.
	var cfg *anthropic.ToolParam
	for _, u := range all {
		if u.OfTool.Name == "get_config" {
			cfg = u.OfTool
		}
	}
	if cfg == nil {
		t.Fatal("get_config is not among the tools")
	}
	props, ok := cfg.InputSchema.Properties.(map[string]any)
	if !ok || props["target"] == nil {
		t.Errorf("get_config properties = %#v, want target", cfg.InputSchema.Properties)
	}
	if !equal(cfg.InputSchema.Required, []string{"target"}) {
		t.Errorf("get_config required = %v, want [target]", cfg.InputSchema.Required)
	}
	if v, ok := cfg.InputSchema.ExtraFields["additionalProperties"]; !ok || v != false {
		t.Errorf("get_config extra fields = %v, want additionalProperties: false kept", cfg.InputSchema.ExtraFields)
	}
	if _, ok := cfg.InputSchema.ExtraFields["$schema"]; !ok {
		t.Errorf("get_config extra fields = %v, want $schema kept", cfg.InputSchema.ExtraFields)
	}
	if _, ok := cfg.InputSchema.ExtraFields["properties"]; ok {
		t.Errorf("properties is also an extra field: %v", cfg.InputSchema.ExtraFields)
	}
	if cfg.Description.Value != "The effective configuration." {
		t.Errorf("get_config description = %q", cfg.Description.Value)
	}

	if got := set.For(LevelNone); len(got) != 1 || got[0].OfTool.Name != "whoami" {
		t.Errorf("For(none) = %v, want only whoami", got)
	}
	// The one tool of the lowest level is also the last, so it carries the breakpoint.
	if got := set.For(LevelNone); !hasCacheControl(t, got[0].OfTool) {
		t.Error("For(none) has no cache breakpoint on its only tool")
	}
}

// hasCacheControl reports whether the tool marshals with an ephemeral cache breakpoint. The SDK
// writes the type from a default, so the marshaled JSON is what the API sees.
func hasCacheControl(t *testing.T, tool *anthropic.ToolParam) bool {
	t.Helper()
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal tool %s: %v", tool.Name, err)
	}
	var v struct {
		CacheControl struct {
			Type string `json:"type"`
		} `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal tool %s: %v", tool.Name, err)
	}
	return v.CacheControl.Type == "ephemeral"
}

func TestCallAllowed(t *testing.T) {
	var c calls
	set := newSet(t, testServer(t, &c))
	res, err := set.Call(context.Background(), LevelRead, "list_robots", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError || res.Text != "robot-a, robot-b" || res.Cut {
		t.Errorf("result = %+v", res)
	}
	if res.Digest != res.Text {
		t.Errorf("digest = %q, want the whole text", res.Digest)
	}
	if n := c.get("list_robots"); n != 1 {
		t.Errorf("the tool ran %d times, want 1", n)
	}
}

func TestCallRefusedAtLevel(t *testing.T) {
	var c calls
	set := newSet(t, testServer(t, &c))
	res, err := set.Call(context.Background(), LevelRead, "inspect_logs", json.RawMessage(`{"name":"x"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text, "inspect_logs") {
		t.Errorf("result = %+v, want an error naming the tool", res)
	}
	if n := c.get("inspect_logs"); n != 0 {
		t.Errorf("the refused tool ran %d times, want 0", n)
	}

	res, err = set.Call(context.Background(), LevelCreator, "no_such_tool", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text, "no_such_tool") {
		t.Errorf("result = %+v, want an error naming the unknown tool", res)
	}

	res, err = set.Call(context.Background(), LevelNone, "list_robots", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError {
		t.Errorf("result = %+v, want level none to refuse list_robots", res)
	}
}

func TestCallNonObjectInput(t *testing.T) {
	var c calls
	set := newSet(t, testServer(t, &c))
	for _, in := range []string{`"text"`, `[1,2]`, `not json`, `null`, ``} {
		res, err := set.Call(context.Background(), LevelRead, "list_robots", json.RawMessage(in))
		if err != nil {
			t.Fatalf("Call(%q): %v", in, err)
		}
		if !res.IsError || !strings.Contains(res.Text, "list_robots") {
			t.Errorf("Call(%q) = %+v, want an error", in, res)
		}
	}
	if n := c.get("list_robots"); n != 0 {
		t.Errorf("the tool ran %d times, want 0", n)
	}
}

func TestCallCutsLongResult(t *testing.T) {
	big := strings.Repeat("a", 20<<10)
	var c calls
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	textTool(s, &c, "whoami", "who", map[string]any{"type": "object"}, big)
	set := newSet(t, s)

	res, err := set.Call(context.Background(), LevelNone, "whoami", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.Cut {
		t.Error("Cut is false for a 20 KiB result")
	}
	if !strings.HasSuffix(res.Text, "\n[cut]") {
		t.Errorf("Text ends with %q, want [cut]", tail(res.Text, 12))
	}
	if len(res.Text) != 16384+len("\n[cut]") {
		t.Errorf("Text is %d bytes, want 16384 + marker", len(res.Text))
	}
	if !strings.HasSuffix(res.Digest, "…") {
		t.Errorf("Digest ends with %q, want …", tail(res.Digest, 3))
	}
	if len(res.Digest) > 2048+len("…") {
		t.Errorf("Digest is %d bytes, want 2 KiB + …", len(res.Digest))
	}
}

func TestCallCutKeepsUTF8(t *testing.T) {
	// A three-byte character straddling the 16 KiB cut must be dropped whole, not split.
	unit := "あ"
	big := strings.Repeat(unit, (16<<10)/len(unit)+10)
	var c calls
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	textTool(s, &c, "whoami", "who", map[string]any{"type": "object"}, big)
	set := newSet(t, s)

	res, err := set.Call(context.Background(), LevelNone, "whoami", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.Cut {
		t.Fatal("Cut is false")
	}
	body := strings.TrimSuffix(res.Text, "\n[cut]")
	if !utf8Valid(body) {
		t.Errorf("Text is not valid UTF-8 at the cut (%d bytes)", len(body))
	}
	if len(body) > 16384 {
		t.Errorf("Text body is %d bytes, want at most 16384", len(body))
	}
	digest := strings.TrimSuffix(res.Digest, "…")
	if !utf8Valid(digest) || len(digest) > 2048 {
		t.Errorf("Digest is not valid UTF-8 or too long (%d bytes)", len(digest))
	}

	// A text that ends exactly on a character boundary is cut at the limit.
	exact := strings.Repeat(unit, 16384/len(unit)) // 16383 bytes with a 3-byte unit
	s2 := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	textTool(s2, &c, "whoami", "who", map[string]any{"type": "object"}, exact+"x")
	set2 := newSet(t, s2)
	res2, err := set2.Call(context.Background(), LevelNone, "whoami", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if body := strings.TrimSuffix(res2.Text, "\n[cut]"); !utf8Valid(body) {
		t.Error("a text cut just before a multi-byte character is not valid UTF-8")
	}
}

func TestCallMCPErrorResult(t *testing.T) {
	var c calls
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	s.AddTool(&mcp.Tool{Name: "whoami", Description: "who", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c.add("whoami")
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "the teamspace is gone"}}}, nil
		})
	set := newSet(t, s)
	res, err := set.Call(context.Background(), LevelNone, "whoami", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !res.IsError || res.Text != "the teamspace is gone" {
		t.Errorf("result = %+v, want the tool's error text", res)
	}
}

func TestCallTransportErrorIsResult(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	s.AddTool(&mcp.Tool{Name: "whoami", Description: "who", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return nil, fmt.Errorf("the fleet API is unreachable")
		})
	set := newSet(t, s)
	res, err := set.Call(context.Background(), LevelNone, "whoami", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Call returned an error, want an IsError result: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Text, "unreachable") {
		t.Errorf("result = %+v, want the failure the model should see", res)
	}
}

func TestCallCancelledContext(t *testing.T) {
	var c calls
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	textTool(s, &c, "whoami", "who", map[string]any{"type": "object"}, "x")
	set := newSet(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := set.Call(ctx, LevelNone, "whoami", json.RawMessage(`{}`)); err == nil {
		t.Error("Call with an ended context returned no error")
	}
}

func TestCallConcurrent(t *testing.T) {
	var n atomic.Int64
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	s.AddTool(&mcp.Tool{Name: "whoami", Description: "who", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			i := n.Add(1)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint(i)}}}, nil
		})
	set := newSet(t, s)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := set.Call(context.Background(), LevelNone, "whoami", json.RawMessage(`{}`))
			if err != nil {
				errs <- err
				return
			}
			if res.IsError || res.Text == "" {
				errs <- fmt.Errorf("result = %+v", res)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := n.Load(); got != 8 {
		t.Errorf("%d calls reached the tool, want 8", got)
	}
}

// TestInputSchemaNeverEmpty: a tool always marshals with an object input schema, also when its
// schema is nil or is just {"type":"object"}, and properties of a real schema are kept.
func TestInputSchemaNeverEmpty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema any
		props  bool
	}{
		{"empty object", map[string]any{"type": "object"}, false},
		{"empty properties", map[string]any{"type": "object", "properties": map[string]any{}}, false},
		{"nil", nil, false},
		{"null properties", map[string]any{"type": "object", "properties": nil}, false},
		{"with properties", map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got anthropic.ToolInputSchemaParam
			switch tc.schema == nil {
			case true:
				got = inputSchema(&mcp.Tool{Name: "x"})
			case false:
				got = inputSchema(&mcp.Tool{Name: "x", InputSchema: tc.schema})
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var v struct {
				Type       string         `json:"type"`
				Properties map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if v.Type != "object" {
				t.Errorf("type = %q, want object: %s", v.Type, raw)
			}
			if v.Properties == nil {
				t.Errorf("properties is missing, want {} at least: %s", raw)
			}
			if tc.props != (len(v.Properties) > 0) {
				t.Errorf("properties = %v, want %v: %s", v.Properties, tc.props, raw)
			}
		})
	}

	// The same through a tool of a real server.
	var c calls
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	textTool(s, &c, "whoami", "who", map[string]any{"type": "object"}, "x")
	set := newSet(t, s)
	tool := set.For(LevelNone)[0].OfTool
	raw, err := json.Marshal(tool)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var v struct {
		InputSchema struct {
			Type       string         `json:"type"`
			Properties map[string]any `json:"properties"`
		} `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.InputSchema.Type != "object" || v.InputSchema.Properties == nil {
		t.Errorf("the tool marshals with input_schema %s, want an object with empty properties", raw)
	}
}

func TestNewNilServer(t *testing.T) {
	if _, err := New(context.Background(), nil); err == nil {
		t.Error("New(nil) succeeded")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// utf8Valid reports whether s is valid UTF-8.
func utf8Valid(s string) bool { return utf8.ValidString(s) }
