// Package tools is the council's view of tnctl's own MCP server: which tools a council member may
// call at its level, the tool parameters sent to the model, and the calls themselves.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tool levels. A council member is given one of them.
const (
	// LevelNone may only ask who it is.
	LevelNone = "none"
	// LevelRead may use every tool that does not change anything; the tools that look inside a
	// robot's pods are reserved for the level above.
	LevelRead = "read"
	// LevelCreator may use every tool.
	LevelCreator = "creator"
)

// inspectPrefix marks the tools that reach into a robot's pods. Only a creator may call them.
const inspectPrefix = "inspect_"

// Limits on what a tool result may put into the model's context.
const (
	maxText   = 16384 // bytes of a result the model gets
	maxDigest = 2048  // bytes of a result a transcript records
)

// Set is the tools of one MCP server, listed once.
type Set struct {
	session *mcp.ClientSession
	server  *mcp.ServerSession
	tools   map[string]*mcp.Tool
	names   []string // sorted
}

// New connects an MCP client to server over mcp.NewInMemoryTransports() and lists its tools once.
// The server must not have run yet.
func New(ctx context.Context, server *mcp.Server) (*Set, error) {
	if server == nil {
		return nil, errors.New("council tools: no MCP server")
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, fmt.Errorf("council tools: connect the MCP server: %w", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "tnctl-council", Version: "council"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = ss.Close()
		return nil, fmt.Errorf("council tools: connect the MCP client: %w", err)
	}
	s := &Set{session: cs, server: ss, tools: map[string]*mcp.Tool{}}
	cursor := ""
	for {
		page, err := cs.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("council tools: list the MCP tools: %w", err)
		}
		for _, t := range page.Tools {
			if t == nil || t.Name == "" {
				continue
			}
			if _, dup := s.tools[t.Name]; dup {
				continue
			}
			s.tools[t.Name] = t
			s.names = append(s.names, t.Name)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	sortStrings(s.names)
	return s, nil
}

// Close ends the connection to the MCP server.
func (s *Set) Close() error {
	var errs []error
	if s.session != nil {
		errs = append(errs, s.session.Close())
	}
	if s.server != nil {
		errs = append(errs, s.server.Close())
	}
	return errors.Join(errs...)
}

// allowed reports whether name may be called at level. An unknown level counts as LevelNone.
func (s *Set) allowed(level, name string) bool {
	if _, ok := s.tools[name]; !ok {
		return false
	}
	switch level {
	case LevelCreator:
		return true
	case LevelRead:
		return !strings.HasPrefix(name, inspectPrefix)
	default:
		return name == "whoami"
	}
}

// Names is the tools allowed at level, sorted. An unknown level counts as LevelNone.
func (s *Set) Names(level string) []string {
	out := make([]string, 0, len(s.names))
	for _, name := range s.names {
		if s.allowed(level, name) {
			out = append(out, name)
		}
	}
	return out
}

// For is the allowed tools at level as Anthropic tool params, sorted by name. The last tool carries
// the cache breakpoint: the list is the same on every call of a member.
func (s *Set) For(level string) []anthropic.ToolUnionParam {
	names := s.Names(level)
	out := make([]anthropic.ToolUnionParam, 0, len(names))
	for _, name := range names {
		tool := &anthropic.ToolParam{Name: name, InputSchema: inputSchema(s.tools[name])}
		if d := s.tools[name].Description; d != "" {
			tool.Description = anthropic.String(d)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: tool})
	}
	if n := len(out); n > 0 && out[n-1].OfTool != nil {
		out[n-1].OfTool.CacheControl = anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m}
	}
	return out
}

// inputSchema converts an MCP tool's input schema into Anthropic's: properties and required have
// fields of their own, and every other key is kept as an extra field. A schema that is not a JSON
// object becomes the empty object schema, and properties is always an object, so a tool always
// marshals with "input_schema":{"type":"object","properties":{}} at least.
func inputSchema(t *mcp.Tool) anthropic.ToolInputSchemaParam {
	out := anthropic.ToolInputSchemaParam{Properties: map[string]any{}}
	if t == nil || t.InputSchema == nil {
		return out
	}
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return out
	}
	for k, v := range m {
		switch k {
		case "type":
			// The param writes "object" itself; MCP input schemas are objects.
		case "properties":
			// Only an object replaces the empty default: "properties": null would drop the key.
			if props, ok := v.(map[string]any); ok {
				out.Properties = props
			}
		case "required":
			out.Required = stringsOf(v)
		default:
			if out.ExtraFields == nil {
				out.ExtraFields = map[string]any{}
			}
			out.ExtraFields[k] = v
		}
	}
	return out
}

// stringsOf is a schema's required list, which arrives as a JSON array.
func stringsOf(v any) []string {
	switch v := v.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Result is what one tool call gives the model.
type Result struct {
	// Text is the tool's text content joined with "\n", cut to 16 KiB on a UTF-8 boundary and
	// marked "[cut]" when it was longer.
	Text string
	// Digest is the first 2 KiB of Text for the transcript, with "…" when it was cut.
	Digest string
	// IsError reports that the call failed, or was refused.
	IsError bool
	// Cut reports that Text is shorter than what the tool returned.
	Cut bool
}

// Call runs one tool. A tool that is unknown, or not allowed at level, is refused without a call;
// so is an input that is not a JSON object. A failed call is a Result with IsError set, so the
// model sees it and can try something else; only an ended ctx is an error.
func (s *Set) Call(ctx context.Context, level, name string, input json.RawMessage) (Result, error) {
	if !s.allowed(level, name) {
		return errorResult(fmt.Sprintf("%q is not a tool a council member at level %q may call", name, level)), nil
	}
	args := map[string]any{}
	if trimmed := bytes.TrimSpace(input); len(trimmed) == 0 || json.Unmarshal(trimmed, &args) != nil || args == nil {
		return errorResult(fmt.Sprintf("the input of %q is not a JSON object", name)), nil
	}

	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, ctxErr
		}
		return errorResult(fmt.Sprintf("%s failed: %v", name, err)), nil
	}
	if res == nil {
		return Result{}, nil
	}
	var parts []string
	for _, c := range res.Content {
		if text, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	out := result(strings.Join(parts, "\n"))
	out.IsError = res.IsError
	return out, nil
}

// result cuts a tool's text for the model and its digest for the transcript.
func result(text string) Result {
	out := Result{Text: text}
	if len(out.Text) > maxText {
		out.Text = out.Text[:utf8Cut(out.Text, maxText)] + "\n[cut]"
		out.Cut = true
	}
	out.Digest = out.Text
	if len(out.Digest) > maxDigest {
		out.Digest = out.Digest[:utf8Cut(out.Digest, maxDigest)] + "…"
	}
	return out
}

// errorResult is a failed or refused call, as the model sees it.
func errorResult(text string) Result {
	out := result(text)
	out.IsError = true
	return out
}

// utf8Cut is the largest n <= max that does not split a UTF-8 sequence of s.
func utf8Cut(s string, max int) int {
	if len(s) <= max {
		return len(s)
	}
	n := max
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return n
}

// sortStrings sorts a string slice without pulling in sort for one call site.
func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
