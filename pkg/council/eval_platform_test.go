package council

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// evalPlatform is the fixture platform of the council's eval: an in-process MCP server whose tools
// answer fixed bodies, registered under the names the council's tools.Set sees in production, so a
// scenario runs a whole turn through the real Set (the cut, the header, the untrusted block and the
// level refusal) without a cluster, a network or the model proxy.
type evalPlatform struct {
	server *mcp.Server
	mu     sync.Mutex
	calls  map[string]int

	// The bodies the tools answer. A scenario sets the ones it needs; an unset tool answers "".
	devices string // list_devices
	sample  string // mqtt_get_sample
	preview string // mqtt_sample
	logs    string // inspect_logs
	pod     string // inspect_pod
}

// newEvalPlatform registers the fixture tools on a server that has not run yet: the harness
// connects the real tools.Set to it.
func newEvalPlatform() *evalPlatform {
	p := &evalPlatform{
		server: mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "eval"}, nil),
		calls:  map[string]int{},
	}
	object := map[string]any{"type": "object", "properties": map[string]any{}}
	pod := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
			"tail": map[string]any{"type": "integer"},
		},
		"required":             []any{"name"},
		"additionalProperties": false,
	}
	p.add("whoami", "Who this tnctl works as.", object, func() string {
		return harnessFacts
	})
	p.add("list_devices", "The devices of the teamspace.", object, func() string {
		return p.devices
	})
	p.add("mqtt_get_sample", "A sample of the teamspace's MQTT messages.", object, func() string {
		return p.sample
	})
	p.add("mqtt_sample", "A short preview of the teamspace's MQTT messages.", object, func() string {
		return p.preview
	})
	p.add("inspect_pod", "The newest events of a pod on a device.", pod, func() string {
		return p.pod
	})
	p.add("inspect_logs", "The log of a pod on a device.", pod, func() string {
		return p.logs
	})
	return p
}

// add registers one fixture tool: it counts its calls and answers with what answer returns, indented
// as the council's server indents every result (evalIndent).
func (p *evalPlatform) add(name, desc string, schema any, answer func() string) {
	p.server.AddTool(&mcp.Tool{Name: name, Description: desc, InputSchema: schema},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			p.mu.Lock()
			p.calls[name]++
			p.mu.Unlock()
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: evalIndent(answer())}}}, nil
		})
}

// count is how many times the tool's handler ran.
func (p *evalPlatform) count(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[name]
}

// evalIndent is text as cmd/mcp.go's mcpIndent gives it to the council: JSON indented two spaces,
// anything that does not parse as it is, and an empty answer as the empty object. json.Indent keeps
// string literals byte for byte, so a payload's escapes reach the model as the encoder wrote them.
func evalIndent(text string) string {
	if len(bytes.TrimSpace([]byte(text))) == 0 {
		return "{}"
	}
	var b bytes.Buffer
	if json.Indent(&b, []byte(text), "", "  ") != nil {
		return text
	}
	return b.String()
}
