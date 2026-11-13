// Package tools is the council's view of tnctl's own MCP server: which tools a council member may
// call at its level, the tool parameters sent to the model, and the calls themselves.
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teknoir/cli/pkg/resultcut"
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
	// Text is the tool's text content joined with "\n"; for a refusal it is the refusal's sentence.
	Text string
	// Digest overrides the transcript's digest when the result brings a shorter one of its own: a
	// guide's first 200 bytes. Empty means Render cuts the text it shows.
	Digest string
	// IsError reports that the call failed, or was refused.
	IsError bool
	// Truncated reports that the model does not see the whole result: either Set cut it, or the
	// tool said it cut it itself.
	Truncated bool
	// Upstream reports that the tool said its result is truncated.
	Upstream bool
	// Full is the whole text, for the turn's result store; the model never gets it as it is. A
	// result that did not come from an MCP tool (a refusal, a guide, an override) leaves it empty,
	// and Render then shows Text without the untrusted wrapper.
	Full string
	// Cut is the cut of Full when it is over the limit, and zero otherwise.
	Cut resultcut.Result
	// Hint is what an upstream cut says to do about it.
	Hint string
	// UpstreamTotal is how many items the whole result had, when the tool said so: the counts inside
	// a cut wrapper are the tool's own, and the header names this one instead.
	UpstreamTotal int
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
	text := strings.Join(parts, "\n")
	out := Result{Text: text, Full: text, IsError: res.IsError}
	if hint, total, cut := upstreamCut(text); cut {
		out.Truncated, out.Upstream, out.Hint, out.UpstreamTotal = true, true, hint, total
	}
	if len(text) > maxText {
		// 1 KiB less than the limit leaves room for the header and the wrapper.
		out.Cut = resultcut.Cut(text, maxText-1024)
		out.Truncated = true
	}
	return out, nil
}

// The hints an upstream cut gets when the tool says nothing of its own.
const (
	hintInspectLogs = "the platform cut the log; narrow tail or since"
	hintInspectPod  = "the platform listed only the newest events"
)

// upstreamCut reports whether text is a result its tool already cut, and what it said about it: its
// hint (the tool's own, or one of the defaults above) and, when it named one, how many items the
// whole result had. A tool says it cut its result in one of four ways:
//
//   - a top-level "truncated": true, the server's own wrapper (mcpCut) and mcp_sample's truncated;
//   - a top-level "truncated" object with a member that is true, which is inspect_logs
//     (api.InspectLogsResponse: "truncated": {"bytes": …, "lines": …});
//   - a top-level "eventsTruncated": true, which is inspect_pod;
//   - a final line that resultcut.LineCutTrailer wrote, whose totals are in its own header.
func upstreamCut(text string) (hint string, total int, ok bool) {
	if r, cut := resultcut.ParseLineCutTrailer(text); cut {
		// The line is its own header: what the tool left out is already counted there.
		return strings.TrimPrefix(resultcut.LineCutTrailer(r), "\n"), 0, true
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &v); err != nil || v == nil {
		return "", 0, false
	}
	switch {
	case rawJSONTrue(v["truncated"]):
		return sanitizeHint(rawString(v["hint"])), rawInt(v["totalItems"]), true
	case jsonObjectHasTrue(v["truncated"]):
		return sanitizeHint(firstHint(rawString(v["hint"]), hintInspectLogs)), rawInt(v["totalItems"]), true
	case rawJSONTrue(v["eventsTruncated"]):
		return sanitizeHint(firstHint(rawString(v["hint"]), hintInspectPod)), rawInt(v["totalItems"]), true
	}
	return "", 0, false
}

// rawJSONTrue reports whether raw is the JSON literal true.
func rawJSONTrue(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "true" }

// jsonObjectHasTrue reports whether raw is a JSON object with any member that is true.
func jsonObjectHasTrue(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return false
	}
	for _, v := range m {
		if rawJSONTrue(v) {
			return true
		}
	}
	return false
}

// rawString is a JSON string value, or "" when raw is not one.
func rawString(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// rawInt is a JSON integer value, or 0 when raw is not one.
func rawInt(raw json.RawMessage) int {
	var n int
	_ = json.Unmarshal(raw, &n)
	return n
}

// firstHint is the tool's own hint, or the default when it gave none.
func firstHint(hint, def string) string {
	if strings.TrimSpace(hint) == "" {
		return def
	}
	return hint
}

// The limits of a hint and a field name in the header: the header is outside the untrusted block, so
// what a device wrote reaches it only as a short, single line.
const (
	maxHintHeader  = 200
	maxFieldHeader = 64
)

// sanitizeHint makes a tool's hint safe to put in the header: control characters (a newline would
// start a line of the council's own) and format characters (which can reverse text, or hide a tag's
// letters) become spaces, the untrusted block's tags are escaped as they are inside the block, and
// the result is at most maxHintHeader bytes, cut at a rune start.
func sanitizeHint(hint string) string {
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, hint)
	clean = untrustedTag.ReplaceAllStringFunc(clean, func(m string) string {
		return "&lt;" + m[1:]
	})
	return cutUTF8(clean, maxHintHeader)
}

// cutUTF8 is s cut to at most max bytes, at a rune start.
func cutUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := max
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// errorResult is a failed or refused call, as the model sees it.
func errorResult(text string) Result {
	return Result{Text: text, Digest: text, IsError: true}
}

// Render is what the model and the transcript get for one tool result: a header that says the
// result is partial and how to read the rest, then the result inside the untrusted block. ref is
// the turn's store reference for the whole text, or "" when the store could not keep it. digest is
// the transcript's summary: the header and the head of what is shown.
func Render(name string, r Result, ref string) (text, digest string) {
	header := renderHeader(r, ref)
	shown := r.Text
	if r.Cut.Truncated {
		shown = r.Cut.Shown
	}
	if shown == "" {
		// The API refuses an empty text block in a tool_result.
		shown = "(no output)"
	}
	body := shown
	if r.Full != "" {
		body = Untrusted("tool:"+name, []byte(r.Full), shown)
	}
	if header != "" {
		text = header + "\n" + body
	} else {
		text = body
	}
	switch {
	case r.Digest != "":
		digest = r.Digest
	default:
		d := shown
		if len(d) > maxDigest {
			d = d[:utf8Cut(d, maxDigest)] + "…"
		}
		if header != "" {
			d = header + "\n" + d
		}
		digest = d
	}
	return text, digest
}

// renderHeader is the first line of a partial result: what the tool said about its own cut, then
// what Set cut and how to read the rest.
func renderHeader(r Result, ref string) string {
	var lines []string
	if r.Upstream {
		line := "RESULT PARTIAL: the tool says its result is truncated"
		if r.UpstreamTotal > 0 && r.Cut.Truncated {
			// Both cuts: the tool's numbers inside the wrapper are its own, and the header's are the
			// council's; a total the tool named is the only one that counts whole items.
			line += fmt.Sprintf(" (%d items in all; the shownItems and shownBytes inside are the tool's own cut, not what you see here)", r.UpstreamTotal)
		}
		line += "."
		if r.Hint != "" {
			line += " " + r.Hint
		}
		line += " Do not infer missing items, counts or absences."
		lines = append(lines, line)
	}
	if r.Cut.Truncated {
		c := r.Cut
		counts := fmt.Sprintf("shown %d of %d bytes, %d of %d %s", c.ShownBytes, c.TotalBytes, c.ShownItems, c.TotalItems, c.Unit)
		var line string
		switch {
		case ref == "":
			line = "RESULT PARTIAL: " + counts + "; the rest is not kept." +
				" Do not infer missing items, counts or absences; narrow the query."
		case c.ContinueAt >= 0:
			line = "RESULT PARTIAL: " + counts + fieldOf(c) + " (ref " + ref + ")." +
				" Do not infer missing items, counts or absences; read_result with ref " + ref +
				" and offset " + strconv.Itoa(c.ContinueAt) + " gives the rest, or narrow the query."
		default:
			line = "RESULT PARTIAL: " + counts + fieldOf(c) + " (ref " + ref + ")." +
				" Do not infer missing items, counts or absences; read_result with ref " + ref +
				" pages the whole result from offset 0, or narrow the query."
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// fieldOf names the object key whose array was cut, cut to maxFieldHeader bytes: the header it goes
// into is outside the untrusted block.
func fieldOf(c resultcut.Result) string {
	if c.Field == "" {
		return ""
	}
	return fmt.Sprintf(" of %q", cutUTF8(c.Field, maxFieldHeader))
}

// untrustedTag matches the block's own tags, in any case, and the ways a device can space, case or
// split one to slip past: whitespace, a NUL or a backslash between the "<" and the name, and a "/"
// before it anywhere. Only the "<" is escaped, so the text stays readable.
var untrustedTag = regexp.MustCompile(`(?i)<[\s\x00/\\]*untrusted`)

// Untrusted is text inside the block that says where it came from: source, and the sha256 of the
// whole text the shown part belongs to. An <untrusted or </untrusted inside the text is escaped, in
// any case and with a "/", a backslash, a NUL or white space between the bracket and the name, so
// that a payload cannot close the block and pass for the platform's own words.
func Untrusted(source string, whole []byte, shown string) string {
	sum := sha256.Sum256(whole)
	var b strings.Builder
	b.WriteString(`<untrusted source="`)
	b.WriteString(strings.NewReplacer(`"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(source))
	b.WriteString(`" sha256="`)
	b.WriteString(hex.EncodeToString(sum[:]))
	b.WriteString(`">` + "\n")
	b.WriteString(untrustedTag.ReplaceAllStringFunc(shown, func(m string) string {
		return "&lt;" + m[1:]
	}))
	b.WriteString("\n</untrusted>")
	return b.String()
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
