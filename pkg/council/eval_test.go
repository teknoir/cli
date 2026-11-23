package council

// The council's eval harness: whole turns against a scripted model and a fixture platform.
//
// Each scenario below builds a harness (newHarness), plugs the real tools.Set over an in-process
// MCP server with fixture tools under their production names (eval_platform_test.go), scripts the
// model's answers and runs the turn to its finish. The assertions are on what the model was sent,
// what the fixture platform was asked, and every event the poster got: they pin the guarantees
// Stage 0 added (a cut result is marked, whole-item and paged by read_result; every tool result is
// inside an untrusted block whose look-alike tags are escaped) so that the next refactor of the
// council cannot drop one of them without a test failing.
//
// Run them with: go test -run TestEval -v ./pkg/council/

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
	"github.com/teknoir/cli/pkg/resultcut"
)

// ---------- the harness ----------

// evalHarness is a harness whose tools are the real tools.Set over the fixture platform.
type evalHarness struct {
	*testHarness
	p *evalPlatform
}

// newEval is a harness with one turn queued, the real tools.Set over a fixture platform, and the
// bot at the level.
func newEval(t *testing.T, work *hub.WorkTurn, level string) *evalHarness {
	t.Helper()
	h := newHarness(t, work)
	p := newEvalPlatform()
	ts, err := tools.New(context.Background(), p.server)
	if err != nil {
		t.Fatalf("tools.New: %v", err)
	}
	t.Cleanup(func() { _ = ts.Close() })
	// Registered after ts.Close, so it runs first: a scenario that failed before h.stop does not
	// leave its daemon serving into the next one. Cancelling twice is harmless.
	t.Cleanup(func() {
		if h.cancel != nil {
			h.cancel()
		}
	})
	h.cfg.Tools = ts
	h.whoami = func(context.Context) (string, json.RawMessage, error) {
		return level, json.RawMessage(harnessFacts), nil
	}
	return &evalHarness{testHarness: h, p: p}
}

// results are the tool_result blocks of a message.
type evalResult struct {
	ID      string
	Text    string
	IsError bool
}

// evalResults are the tool results of one message, in order.
func evalResults(msg anthropic.MessageParam) []evalResult {
	var out []evalResult
	for _, c := range msg.Content {
		if c.OfToolResult == nil {
			continue
		}
		var b strings.Builder
		for _, inner := range c.OfToolResult.Content {
			if inner.OfText != nil {
				b.WriteString(inner.OfText.Text)
			}
		}
		out = append(out, evalResult{ID: c.OfToolResult.ToolUseID, Text: b.String(), IsError: c.OfToolResult.IsError.Value})
	}
	return out
}

// evalLastResults are the tool results of a request's last message.
func evalLastResults(req model.Request) []evalResult {
	if len(req.Messages) == 0 {
		return nil
	}
	return evalResults(req.Messages[len(req.Messages)-1])
}

// evalUntrustedBody is the text inside a tool_result's untrusted block: between the opener line and
// the block's own closing tag.
func evalUntrustedBody(t *testing.T, text string) string {
	t.Helper()
	start := strings.Index(text, "<untrusted ")
	if start < 0 {
		t.Fatalf("no untrusted block in %.300q", text)
	}
	if start > 0 && text[start-1] != '\n' {
		t.Fatalf("the untrusted block does not start a line: %.300q", text)
	}
	rest := text[start:]
	const closer = "\n</untrusted>"
	if !strings.HasSuffix(rest, closer) {
		t.Fatalf("the untrusted block does not end with its own closer: %.300q", rest[max(0, len(rest)-300):])
	}
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		t.Fatalf("the untrusted opener is not a line of its own: %.300q", text)
	}
	return rest[nl+1 : len(rest)-len(closer)]
}

// evalDump is every request and every event of the harness as one string: the "never contains"
// checks look for a cut marker in anything the model saw or the person's client got.
func evalDump(t *testing.T, h *evalHarness) string {
	t.Helper()
	var b strings.Builder
	for _, e := range h.hub.posterList() {
		for _, ev := range e.snapshot() {
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("event %s: %v", ev.Type, err)
			}
			b.Write(raw)
			b.WriteByte('\n')
		}
	}
	h.model.mu.Lock()
	reqs := append([]model.Request{}, h.model.reqs...)
	h.model.mu.Unlock()
	for _, req := range reqs {
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// evalJSON is v as JSON text with its HTML characters kept, as mcpMarshal writes it: a payload's
// "<" reaches the model as it is.
func evalJSON(t *testing.T, v any) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// The ways a fixture carries a corpus payload in the text the model reads: each is s as the inside
// of that text's string.
var (
	// encRaw is a text that is not JSON: s as it is.
	encRaw = func(_ *testing.T, s string) string { return s }
	// encJSON is a JSON string as mcpMarshal writes it (no HTML escaping). The platform's
	// JSON.stringify differs only in leaving U+2028/U+2029 raw, which the unit tests and
	// injection_logs cover.
	encJSON = func(t *testing.T, s string) string { j := evalJSON(t, s); return j[1 : len(j)-1] }
	// encHTML is a JSON string as json.Marshal writes it (marshalRaw): "<" is \u003c.
	encHTML = func(t *testing.T, s string) string {
		j, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return string(j[1 : len(j)-1])
	}
)

// ---------- the fixtures ----------

// evalDevices is a list_devices answer of n devices, each about extra bytes, compact: the fixture
// platform indents it as the council's server does.
func evalDevices(n, extra int) string {
	items := make([]string, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, fmt.Sprintf(`{"name":"device-%02d","state":"online","extra":"%s"}`,
			i, strings.Repeat(string(rune('a'+i%26)), extra)))
	}
	return "[" + strings.Join(items, ",") + "]"
}

// injEntry is one entry of the injection corpus.
type injEntry struct {
	Marker  string `json:"marker"`
	Why     string `json:"why"`
	Payload string `json:"payload"`
	// Escaped is the payload as the block must show it when the payload is a tag: each of its
	// openers "&lt;" and the rest as written. Empty for the entries that carry no tag.
	Escaped string `json:"escaped,omitempty"`
}

// evalCorpus is the injection corpus of testdata/eval/injection.json.
func evalCorpus(t *testing.T) []injEntry {
	t.Helper()
	raw, err := os.ReadFile("testdata/eval/injection.json")
	if err != nil {
		t.Fatalf("the injection corpus: %v", err)
	}
	var out []injEntry
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the injection corpus: %v", err)
	}
	if len(out) < 20 {
		t.Fatalf("the corpus has %d entries, want at least 20", len(out))
	}
	tags, big := 0, false
	for _, e := range out {
		if e.Marker == "" || !strings.Contains(e.Payload, e.Marker) {
			t.Fatalf("corpus entry %q does not carry its own marker: %q", e.Marker, e.Payload)
		}
		if e.Escaped != "" {
			tags++
			if !strings.HasPrefix(e.Escaped, e.Marker+" &lt;") {
				t.Fatalf("corpus entry %s: its escaped form %.80q does not start with its marker and &lt;", e.Marker, e.Escaped)
			}
		}
		if e.Marker == "INJ20" && len(e.Payload) >= 4<<10 && strings.Count(e.Payload, "</untrusted>") > 300 {
			big = true
		}
	}
	// A floor, so emptying the escaped fields cannot switch the per-entry check off.
	if tags < 12 {
		t.Fatalf("%d corpus entries carry an escaped form, want the 12 tag entries", tags)
	}
	if !big {
		t.Fatal("the corpus has no INJ20 payload of 4 KiB of repeated closers")
	}
	return out
}

// evalSample is an mqtt_get_sample answer whose messages carry every corpus payload: JSON without
// HTML escaping, as mcpMarshal writes it.
func evalSample(t *testing.T, corpus []injEntry) string {
	t.Helper()
	return evalJSON(t, map[string]any{"messages": evalMessages(t, corpus), "count": len(corpus)})
}

// evalPreview is an mqtt_sample answer whose messages carry every corpus payload: each message as
// the platform's JSON.stringify sends it, and the result as mqtt_sample marshals it (marshalRaw, so
// json.Marshal, cmd/mcp.go): every "<" of a payload reaches the model as \u003c.
func evalPreview(t *testing.T, corpus []injEntry) string {
	t.Helper()
	msgs := make([]json.RawMessage, 0, len(corpus))
	for _, m := range evalMessages(t, corpus) {
		msgs = append(msgs, json.RawMessage(evalJSON(t, m)))
	}
	b, err := json.Marshal(map[string]any{"messages": msgs, "stoppedBy": "max"})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(b)
}

// evalMessages are one MQTT message per corpus payload.
func evalMessages(t *testing.T, corpus []injEntry) []map[string]any {
	t.Helper()
	msgs := make([]map[string]any, 0, len(corpus))
	for i, e := range corpus {
		msgs = append(msgs, map[string]any{
			"topic":   fmt.Sprintf("/devices/device-%02d/state", i),
			"payload": e.Payload,
			"qos":     0,
		})
	}
	return msgs
}

// evalLogs is an inspect_logs answer whose log lines carry every corpus payload, as plain text.
// Production inspect_logs is the platform's JSON (api.InspectLogsResponse through mcpIndent), which
// injection_mqtt and injection_preview cover; this one stands for what reaches the council as text:
// an answer that does not parse, which mcpIndent passes on as it is, and a tool error. It is the
// only way the corpus's raw controls (INJ05's tab and newline, INJ16's ESC) and a raw U+2028 (INJ18)
// reach the escaping, because a JSON text carries them escaped.
func evalLogs(corpus []injEntry) string {
	lines := make([]string, 0, len(corpus))
	for _, e := range corpus {
		lines = append(lines, "2026-11-13T09:00:00Z "+e.Payload)
	}
	return strings.Join(lines, "\n")
}

// evalHiddenSeq is the hidden runes of s, in order: those of tools.isHidden (controls but the
// newline and the tab, format characters, combining marks, the Hangul fillers) and the line and
// paragraph separators.
func evalHiddenSeq(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n', r == '\t':
		case unicode.In(r, unicode.Cc, unicode.Cf, unicode.Mn, unicode.Me),
			r == 0x2028, r == 0x2029, r == 0x115F, r == 0x1160, r == 0x3164, r == 0xFFA0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// evalNotOffered fails when a read-level request offers a tool the level reserves for creators.
func evalNotOffered(t *testing.T, who string, reqs []model.Request) {
	t.Helper()
	for i, req := range reqs {
		for _, n := range toolNames(req) {
			if strings.HasPrefix(n, "inspect_") {
				t.Errorf("%s request %d offers a read-level member %s", who, i, n)
			}
		}
	}
}

// evalCheckInjection is the assertion of the injection scenarios: the tool result is one untrusted
// block, nothing inside it can forge or close one, every tag of the corpus has its opener escaped
// and the rest kept, no corpus entry was dropped, and the hidden characters are the tool's. sent is
// the text the tool answered; enc is how that text spells a payload.
func evalCheckInjection(t *testing.T, name, text, sent string, corpus []injEntry, enc func(*testing.T, string) string) {
	t.Helper()
	if got := strings.Count(text, "<untrusted"); got != 1 {
		t.Errorf("%d structural openers in the result, want 1: %.300q", got, text)
	}
	if got := strings.Count(text, "</untrusted>"); got != 1 {
		t.Errorf("%d closers in the result, want 1: %.300q", got, text)
	}
	want := `<untrusted source="tool:` + name + `" sha256="`
	if !strings.HasPrefix(text, want) {
		t.Errorf("the block does not open the result: %.300q", text)
	}
	if !strings.HasSuffix(text, "\n</untrusted>") {
		t.Errorf("the block does not close the result: %.300q", text[max(0, len(text)-100):])
	}
	body := evalUntrustedBody(t, text)

	// Each tag entry's whole escaped form, spelled as this fixture spells text, follows its marker:
	// only the opener became "&lt;" and nothing of the tag was lost. The forms are the corpus's own,
	// so this check asks neither untrustedTag nor escapeOpener what to expect.
	for _, e := range corpus {
		if e.Escaped == "" {
			continue
		}
		parts := strings.Split(e.Escaped, "&lt;")
		for i, s := range parts {
			parts[i] = enc(t, s)
		}
		if w := strings.Join(parts, "&lt;"); !strings.Contains(body, w) {
			i := strings.Index(body, e.Marker+" ")
			t.Errorf("corpus entry %s (%s): want %.120q in the block, got %.120q", e.Marker, e.Why, w, body[max(0, i):min(len(body), max(0, i)+len(w))])
		}
	}

	// A second escaping pass changes nothing, so no match of the package's own regexp is left. This
	// cannot see a narrowed rule, which leaves the same matches out on both passes: the per-entry
	// forms above are the independent check.
	if again := evalUntrustedBody(t, tools.Untrusted("s", []byte(body), body)); again != body {
		where := 0
		for where < len(body) && where < len(again) && body[where] == again[where] {
			where++
		}
		t.Errorf("a second escaping pass changed the block near %.120q", body[max(0, where-40):min(len(body), where+80)])
	}

	// The block cannot be closed from inside: the inner text's opener and closer counts are what the
	// escaping leaves, and the wrapper's own are the only ones in the result.
	if strings.HasSuffix(body, "</untrusted>") || strings.Contains(body, "\n</untrusted>") {
		t.Errorf("the result text carries a closer of its own: %.200q", body[max(0, len(body)-200):])
	}

	// Nothing was silently dropped: every entry's marker is still there.
	for _, e := range corpus {
		if !strings.Contains(body, e.Marker) {
			t.Errorf("corpus entry %s (%s) is missing from the block", e.Marker, e.Why)
		}
	}
	if !utf8.ValidString(body) {
		t.Error("the block is not valid UTF-8")
	}

	// HARDENING (a later stage, not a Stage 0 promise): Stage 0 escapes the block's own tags and
	// keeps every other character the tool sent, so bidi controls, zero-width and format characters,
	// combining marks, tag characters, C0 controls such as ESC and the line separator U+2028 reach
	// the model as they are. The eval pins that: the
	// block's hidden runes are the tool's, in order, none dropped and none added. A later rule would
	// rewrite only the bidi embedding, override and isolate controls (U+202A-U+202E, U+2066-U+2069)
	// and the tag characters (U+E0000-U+E007F, keeping a well-formed emoji tag sequence) to a
	// visible form after the tag escape, and assert none of those is left. It would not touch
	// combining marks, ZWJ, ZWSP, the soft hyphen, LRM/RLM, newlines or tabs, which legitimate
	// device text uses: sanitizeHint's space for every hidden rune would flatten them, so it is no
	// model for a body.
	got, sentSeq := evalHiddenSeq(body), evalHiddenSeq(sent)
	if sentSeq == "" {
		t.Fatal("the fixture carries no hidden character: the hidden-rune pin would be vacuous")
	}
	if got != sentSeq {
		t.Errorf("the block's hidden runes are %+q, the tool sent %+q", got, sentSeq)
	}
}

// ---------- the scenarios ----------

// TestEval runs whole council turns against a scripted model and the fixture platform.
func TestEval(t *testing.T) {
	t.Run("cut_list", evalCutList)
	t.Run("cut_no_ref", evalCutNoRef)
	t.Run("injection_mqtt", evalInjectionMQTT)
	t.Run("injection_preview", evalInjectionPreview)
	t.Run("injection_obey", evalInjectionObey)
	t.Run("injection_logs", evalInjectionLogs)
	t.Run("escape_linear", evalEscapeLinear)
	t.Run("budget_rounds", evalBudgetRounds)
	t.Run("budget_tokens", evalBudgetTokens)
}

// The Captain's header names the reference and the offset read_result takes.
var (
	evalRefInHeader    = regexp.MustCompile(`\(ref (r[0-9]+)\)`)
	evalOffsetInHeader = regexp.MustCompile(`offset ([0-9]+) gives the rest`)
)

// e2eDevices is the list_devices answer of the cut scenarios: 60 devices of about 3.5 KiB, indented
// as the council's server sends it.
func e2eDevices(t *testing.T) string {
	t.Helper()
	body := evalIndent(evalDevices(60, 3500))
	if len(body) < 200<<10 {
		t.Fatalf("the fixture is %d bytes, want at least 200 KiB", len(body))
	}
	return body
}

// evalCutList: a 200 KiB list is cut whole-item, the header names the ref and the offset, and the
// read_result page continues exactly where the shown part ended. No pre-Stage-0 cut marker reaches
// the model or the client.
func evalCutList(t *testing.T) {
	body := e2eDevices(t)
	h := newEval(t, newWork(kindAsk, "", "what is the state of the devices?"), tools.LevelRead)
	h.p.devices = body

	h.model.push(roleRoute, toolStep("tool_use", apiCall{id: "tu_list", name: "list_devices", input: map[string]any{}}))
	// The Captain reads the header the first result carries and calls read_result with what it says.
	// build runs on the turn's goroutine, not the test's: it reports with t.Errorf and ends the turn
	// with a text answer, never t.Fatal.
	h.model.push(roleRoute, &scriptStep{build: func(req model.Request) *scriptStep {
		results := evalLastResults(req)
		if len(results) != 1 {
			t.Errorf("the Captain saw %d tool results, want 1", len(results))
			return textStep("not one tool result", "end_turn")
		}
		ref := evalRefInHeader.FindStringSubmatch(results[0].Text)
		offset := evalOffsetInHeader.FindStringSubmatch(results[0].Text)
		if ref == nil || offset == nil {
			t.Errorf("the result's header names no ref and offset: %.400q", results[0].Text)
			return textStep("no ref in the header", "end_turn")
		}
		off, err := strconv.Atoi(offset[1])
		if err != nil {
			t.Errorf("the header's offset %q: %v", offset[1], err)
			return textStep("no offset in the header", "end_turn")
		}
		return toolStep("tool_use", apiCall{id: "tu_page", name: toolReadResult, input: map[string]any{
			"ref": ref[1], "offset": off,
		}})
	}})
	h.model.push(roleRoute, textStep("the list holds 60 devices", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) != 3 {
		t.Fatalf("%d route requests, want 3", len(route))
	}
	first := evalLastResults(route[1])
	if len(first) != 1 {
		t.Fatalf("the first answer's results: %d", len(first))
	}
	text := first[0].Text

	// The header is renderHeader's, line for line, with the counts of this result.
	cut := resultcut.Cut(body, 16384-1024)
	if !cut.Truncated || cut.Unit != "items" || cut.TotalItems != 60 || cut.ShownItems <= 0 || cut.ShownItems >= 60 {
		t.Fatalf("the fixture's cut is %+v", cut)
	}
	header := fmt.Sprintf("RESULT PARTIAL: shown %d of %d bytes, %d of %d items (ref r1). "+
		"Do not infer missing items, counts or absences; read_result with ref r1 and offset %d gives the rest, or narrow the query.",
		cut.ShownBytes, cut.TotalBytes, cut.ShownItems, cut.TotalItems, cut.ContinueAt)
	sum := sha256.Sum256([]byte(body))
	want := header + "\n<untrusted source=\"tool:list_devices\" sha256=\"" + hex.EncodeToString(sum[:]) + "\">\n"
	if !strings.HasPrefix(text, want) {
		t.Fatalf("the tool result starts %.400q,\nwant %.400q", text, want)
	}

	// The shown part fits the model's limit and is whole items.
	shown := evalUntrustedBody(t, text)
	if len(shown) > 16384 {
		t.Errorf("the shown part is %d bytes, over the model's 16384", len(shown))
	}
	// The header's counts are the result's, not only resultcut's: the expected header above comes
	// from the function under test, so a wrong count would be on both sides.
	if cut.ShownBytes != len(shown) || cut.TotalBytes != len(body) {
		t.Errorf("the header counts %d of %d bytes, the result shows %d of %d", cut.ShownBytes, cut.TotalBytes, len(shown), len(body))
	}
	// The shown part is the result's head closed by the cut's "\n]" (the indented path), and the
	// header's offset is where that head ends.
	if !strings.HasSuffix(shown, "\n]") {
		t.Errorf("the shown part does not end with the indented array's closing: %.40q", shown[max(0, len(shown)-40):])
	}
	if head := strings.TrimSuffix(strings.TrimSuffix(shown, "]"), "\n"); cut.ContinueAt != len(head) || !strings.HasPrefix(body, head) {
		t.Errorf("the header's offset is %d, the shown part is the result's first %d bytes", cut.ContinueAt, len(head))
	}
	var devices []map[string]any
	if err := json.Unmarshal([]byte(shown), &devices); err != nil {
		t.Fatalf("the shown part is not a JSON array: %v in %.200q", err, shown)
	}
	if len(devices) != cut.ShownItems {
		t.Errorf("%d devices shown, the header says %d", len(devices), cut.ShownItems)
	}
	for i, d := range devices {
		if d["name"] == nil || d["state"] == nil {
			t.Errorf("shown device %d is not a whole object: %v", i, d)
		}
	}

	// The page continues exactly where the shown part ended: no gap, no repeat, in order.
	page, ok := evalReadPage(t, route[2], "r1")
	if !ok {
		t.Fatalf("no page in the third route request: %v", evalLastResults(route[2]))
	}
	if page.Offset != cut.ContinueAt {
		t.Errorf("the page starts at %d, the header named %d", page.Offset, cut.ContinueAt)
	}
	if want := body[page.Offset : page.Offset+len(page.Text)]; want != page.Text {
		t.Errorf("the page is not the result from its offset: %.200q", page.Text)
	}
	names := evalDeviceNames(t, body)
	shownNames := evalDeviceNames(t, shown)
	pageNames := evalDeviceNames(t, page.Text)
	if got := len(shownNames) + len(pageNames); got == 0 || got > len(names) {
		t.Fatalf("%d shown + %d paged devices of %d", len(shownNames), len(pageNames), len(names))
	}
	for i, n := range append(append([]string{}, shownNames...), pageNames...) {
		if n != names[i] {
			t.Fatalf("device %d is %q, want %q: the page does not continue the shown part", i, n, names[i])
		}
	}
	if h.p.count("list_devices") != 1 {
		t.Errorf("list_devices ran %d times", h.p.count("list_devices"))
	}
	if res := p.result(); res.State != stateDone || res.Final != "the list holds 60 devices" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}

	// Nothing the pre-Stage-0 code wrote as a cut marker reaches the model or the client. Before
	// Stage 0, tools.go's result() (used by Set.Call and errorResult) appended "\n[cut]" to a result
	// over 16 KiB, and prompts.go's skill loader cut read_skill's text with
	// cutUTF8(body, maxSkillText, "\n[cut]"); c0025c7 removed both. "…[truncated]" is no marker the
	// council ever wrote — `git log -S'[truncated]' -- pkg/council` is empty — and the check is here
	// as a guard for the coming refactor, so that a new silent cut cannot hide behind it.
	dump := evalDump(t, h)
	for _, marker := range []string{"[cut]", "…[truncated]"} {
		if strings.Contains(dump, marker) {
			t.Errorf("the request or an event carries the old cut marker %q", marker)
		}
	}
}

// evalPage is the read_result page of a request: its offset and its body.
type evalPage struct {
	Offset int
	Text   string // the page as the tool gave it: the result's bytes from Offset
}

// evalPage reads the read_result answer out of a request's last message: the call it answers is the
// only tool result there after the list, and the page's own head says which bytes it is.
func evalReadPage(t *testing.T, req model.Request, ref string) (evalPage, bool) {
	t.Helper()
	results := evalLastResults(req)
	if len(results) != 1 {
		return evalPage{}, false
	}
	text := results[0].Text
	if results[0].IsError {
		t.Fatalf("read_result refused: %.300q", text)
	}
	head := regexp.MustCompile(`^PAGE: bytes ([0-9]+)\.\.([0-9]+) of [0-9]+ \(ref ` + ref + `\);(?: nextOffset [0-9]+| end)\n`)
	m := head.FindStringSubmatch(text)
	if m == nil {
		return evalPage{}, false
	}
	return evalPage{Offset: mustInt(t, m[1]), Text: evalUntrustedBody(t, text)}, true
}

// evalDeviceNames are the device names in a JSON text, compact or indented, in order.
func evalDeviceNames(t *testing.T, s string) []string {
	t.Helper()
	re := regexp.MustCompile(`"name":\s*"(device-[0-9]+)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// mustInt is a decimal a regexp matched. Only on the test goroutine: it calls t.Fatalf.
func mustInt(t *testing.T, s string) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		t.Fatalf("%q is not a number", s)
	}
	return n
}

// evalCutNoRef: a result the turn's store cannot keep gets the "not kept" header and no reference,
// and read_result refuses the reference the model would then have been given.
//
// The store refuses on its size, not on a hook: put() takes a result only while the turn's kept
// bytes stay at or under maxTurnResults (16 MiB). One result over that cap cannot be delivered
// here — the MCP transport buffers at most DefaultMaxLineLength (16 MiB) per frame and an over-cap
// result arrives as a transport error, not as a cut result — so the scenario fills the store with
// two results of about 9 MiB each: the first is kept as r1, the second is refused. This is the
// store's real refusal, not the brief's "same list": that list is 200 KiB and always kept.
//
// Its turn deadline and its wait are 2 minutes, not newWork's 10 s: under -race the two 9 MiB
// round trips through the in-memory MCP transport take about 7 s even on a fast desktop CPU (most of
// it the race detector's instrumentation of the JSON encoding), and the route phase's context ends
// at the deadline less WorkReserve, which a slower CI runner passes ("3 route requests, want 4").
// Nothing in the scenario depends on the deadline: it is headroom only, so do not shorten it.
func evalCutNoRef(t *testing.T) {
	body := evalIndent(evalDevices(45, 200<<10))
	if len(body) <= maxTurnResults/2 || len(body) >= 16<<20 {
		t.Fatalf("the fixture is %d bytes, want over half the store's %d and under one transport frame", len(body), maxTurnResults)
	}
	work := newWork(kindAsk, "", "what is the state of the devices?")
	work.Deadline = time.Now().Add(2 * time.Minute).UnixMilli()
	h := newEval(t, work, tools.LevelRead)
	h.p.devices = body

	h.model.push(roleRoute, toolStep("tool_use", apiCall{id: "tu_one", name: "list_devices", input: map[string]any{}}))
	h.model.push(roleRoute, toolStep("tool_use", apiCall{id: "tu_two", name: "list_devices", input: map[string]any{}}))
	h.model.push(roleRoute, toolStep("tool_use", apiCall{id: "tu_page", name: toolReadResult, input: map[string]any{
		"ref": "r2", "offset": 0,
	}}))
	h.model.push(roleRoute, textStep("the list is long", "end_turn"))
	h.start(t)
	p := h.poster(t)
	select {
	case <-p.finished:
	case <-time.After(2 * time.Minute):
		t.Fatalf("the turn did not finish; events: %v", p.types())
	}
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) != 4 {
		t.Fatalf("%d route requests, want 4", len(route))
	}
	cut := resultcut.Cut(body, 16384-1024)
	kept := evalLastResults(route[1])
	if len(kept) != 1 || !strings.HasPrefix(kept[0].Text, "RESULT PARTIAL: ") || !strings.Contains(kept[0].Text, "(ref r1).") {
		t.Fatalf("the first result is not the kept cut: %v", kept)
	}
	refused := evalLastResults(route[2])
	if len(refused) != 1 {
		t.Fatalf("the second answer's results: %d", len(refused))
	}
	// The "not kept" header's counts are the result's, not only resultcut's.
	if shown := evalUntrustedBody(t, refused[0].Text); cut.ShownBytes != len(shown) || cut.TotalBytes != len(body) || cut.TotalItems != 45 {
		t.Errorf("the header counts %d of %d bytes and %d items, the result shows %d of %d bytes of 45 items",
			cut.ShownBytes, cut.TotalBytes, cut.TotalItems, len(shown), len(body))
	}
	header := fmt.Sprintf("RESULT PARTIAL: shown %d of %d bytes, %d of %d items; the rest is not kept. "+
		"Do not infer missing items, counts or absences; narrow the query.",
		cut.ShownBytes, cut.TotalBytes, cut.ShownItems, cut.TotalItems)
	if !strings.HasPrefix(refused[0].Text, header+"\n<untrusted source=\"tool:list_devices\"") {
		t.Fatalf("the tool result starts %.400q,\nwant %.400q", refused[0].Text, header)
	}
	if strings.Contains(refused[0].Text, "(ref") || strings.Contains(refused[0].Text, "read_result") {
		t.Errorf("a result the store could not keep names a reference: %.300q", refused[0].Text)
	}

	// The model calls read_result with the reference it would have had: the store has no such result.
	page := evalLastResults(route[3])
	if len(page) != 1 {
		t.Fatalf("the read_result answer: %d results", len(page))
	}
	const refusal = "no result r2 in this turn"
	if !page[0].IsError || page[0].Text != refusal {
		t.Errorf("read_result gave %q (error %v), want %q", page[0].Text, page[0].IsError, refusal)
	}
	if n := h.p.count("list_devices"); n != 2 {
		t.Errorf("list_devices ran %d times, want 2", n)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the list is long" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// evalInjectionMQTT: a member turn whose sample carries the injection corpus reaches Fleet in one
// untrusted block that nothing in the corpus can forge, close or lose.
func evalInjectionMQTT(t *testing.T) {
	evalInjectionMember(t, "mqtt_get_sample", func(h *evalHarness, corpus []injEntry) string {
		h.p.sample = evalSample(t, corpus)
		return h.p.sample
	}, encJSON)
}

// evalInjectionPreview: the same through mqtt_sample, whose result json.Marshal writes, so every
// "<" of a payload reaches the model as the JSON escape \u003c.
func evalInjectionPreview(t *testing.T) {
	evalInjectionMember(t, "mqtt_sample", func(h *evalHarness, corpus []injEntry) string {
		h.p.preview = evalPreview(t, corpus)
		return h.p.preview
	}, encHTML)
}

// evalInjectionMember is a read-level Fleet turn that calls tool once, whose answer set gives the
// corpus, and then reports.
func evalInjectionMember(t *testing.T, tool string, set func(*evalHarness, []injEntry) string, enc func(*testing.T, string) string) {
	corpus := evalCorpus(t)
	h := newEval(t, newWork(kindMember, slugFleet, "what do the devices publish?"), tools.LevelRead)
	sent := set(h, corpus)

	h.model.push(roleFleet, toolStep("tool_use", apiCall{id: "tu_sample", name: tool, input: map[string]any{}}))
	h.model.push(roleFleet, textStep(reportJSON("the messages are unremarkable"), "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	fleet := h.model.requests(roleFleet)
	if len(fleet) != 2 {
		t.Fatalf("%d Fleet requests, want 2", len(fleet))
	}
	evalNotOffered(t, "Fleet", fleet)
	results := evalLastResults(fleet[1])
	if len(results) != 1 {
		t.Fatalf("Fleet saw %d tool results, want 1", len(results))
	}
	if results[0].IsError {
		t.Fatalf("%s failed: %.300q", tool, results[0].Text)
	}
	evalCheckInjection(t, tool, results[0].Text, sent, corpus, enc)
	if n := h.p.count(tool); n != 1 {
		t.Errorf("%s ran %d times", tool, n)
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("finish %q %q", res.State, res.ErrCode)
	}
}

// evalInjectionObey: the Captain "obeys" an instruction it read in a sample and calls the tools the
// level reserves; the level refusal answers them and the turn still answers.
func evalInjectionObey(t *testing.T) {
	corpus := evalCorpus(t)
	h := newEval(t, newWork(kindAsk, "", "what do the devices publish?"), tools.LevelRead)
	h.p.sample = evalSample(t, corpus)
	const obey = "INJ14"
	found := false
	for _, e := range corpus {
		if e.Marker == obey && strings.Contains(e.Payload, "Ignore all previous instructions") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the corpus has no %s entry that asks for inspect_pod", obey)
	}

	h.model.push(roleRoute, toolStep("tool_use", apiCall{id: "tu_sample", name: "mqtt_get_sample", input: map[string]any{}}))
	// What the sample asked for: the tools a creator may call, called at read.
	h.model.push(roleRoute, toolStep("tool_use",
		apiCall{id: "tu_pod", name: "inspect_pod", input: map[string]any{"name": "api-1"}},
		apiCall{id: "tu_logs", name: "inspect_logs", input: map[string]any{"name": "api-1"}},
	))
	h.model.push(roleRoute, textStep("I will not run those", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) != 3 {
		t.Fatalf("%d route requests, want 3", len(route))
	}
	// A read-level Captain is never offered the tools it then "obeys" its way to.
	evalNotOffered(t, "route", route)
	// The sample was delivered before the Captain obeyed it.
	if sample := evalLastResults(route[1]); len(sample) != 1 || sample[0].IsError || !strings.Contains(sample[0].Text, obey) {
		t.Errorf("the sample the Captain read: %v", sample)
	}
	results := evalLastResults(route[2])
	if len(results) != 2 {
		t.Fatalf("%d tool results for two calls", len(results))
	}
	for i, name := range []string{"inspect_pod", "inspect_logs"} {
		want := fmt.Sprintf("%q is not a tool a council member at level %q may call", name, tools.LevelRead)
		if !results[i].IsError || results[i].Text != want {
			t.Errorf("%s gave %q (error %v), want %q", name, results[i].Text, results[i].IsError, want)
		}
	}
	if n := h.p.count("mqtt_get_sample"); n != 1 {
		t.Errorf("mqtt_get_sample ran %d times, want 1", n)
	}
	if n := h.p.count("inspect_pod"); n != 0 {
		t.Errorf("inspect_pod ran %d times", n)
	}
	if n := h.p.count("inspect_logs"); n != 0 {
		t.Errorf("inspect_logs ran %d times", n)
	}
	res := p.result()
	if res.State != stateDone || res.Final != "I will not run those" {
		t.Errorf("finish %q %q %q", res.State, res.Final, res.ErrCode)
	}
}

// evalInjectionLogs: a creator's inspect_logs answer, plain text, carries the corpus through the
// same block, with its controls raw.
func evalInjectionLogs(t *testing.T) {
	corpus := evalCorpus(t)
	h := newEval(t, newWork(kindAsk, "", "what does the log say?"), tools.LevelCreator)
	h.p.logs = evalLogs(corpus)

	h.model.push(roleRoute, toolStep("tool_use", apiCall{id: "tu_logs", name: "inspect_logs", input: map[string]any{"name": "api-1"}}))
	h.model.push(roleRoute, textStep("the log says nothing much", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) != 2 {
		t.Fatalf("%d route requests, want 2", len(route))
	}
	// The positive control of evalNotOffered: a creator is offered the inspect tools.
	if got := toolNames(route[0]); !slices.Contains(got, "inspect_logs") {
		t.Errorf("a creator Captain is not offered inspect_logs: %v", got)
	}
	results := evalLastResults(route[1])
	if len(results) != 1 {
		t.Fatalf("the Captain saw %d tool results, want 1", len(results))
	}
	if results[0].IsError {
		t.Fatalf("inspect_logs failed: %.300q", results[0].Text)
	}
	evalCheckInjection(t, "inspect_logs", results[0].Text, h.p.logs, corpus, encRaw)
	// The payloads arrived raw: an ESC and a U+2028 are in the block as they are.
	if body := evalUntrustedBody(t, results[0].Text); !strings.Contains(body, "\x1b[31m") || !strings.Contains(body, "\u2028") {
		t.Error("the log's raw controls did not reach the block")
	}
	if h.p.count("inspect_logs") != 1 {
		t.Errorf("inspect_logs ran %d times", h.p.count("inspect_logs"))
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("finish %q %q", res.State, res.ErrCode)
	}
}

// evalEscapeLinear: escaping is linear in the text. No turn can show a quadratic rule: Render
// escapes at most the 16 KiB shown part and read_result a 12 KiB page, and INJ20's 4 KiB of closers
// takes milliseconds either way. So the rule is timed on its own, on 96 KiB of closers, where the
// linear rule takes milliseconds (about 75 ms under -race) and a quadratic one seconds (about 10 s,
// 115 s under -race). The call runs in a goroutine, so a quadratic rule fails at 2 s, not when it
// ends.
func evalEscapeLinear(t *testing.T) {
	big := strings.Repeat("</untrusted>", 8192)
	done := make(chan string, 1)
	go func() { done <- tools.Untrusted("tool:x", nil, big) }()
	select {
	case out := <-done:
		if got := strings.Count(out, "&lt;/untrusted>"); got != 8192 {
			t.Errorf("%d escaped closers, want 8192", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("escaping %d KiB of closers took over 2s: the escaping is not linear", len(big)>>10)
	}
}

// evalBudgetRounds: Fleet's tool rounds are its role's budget (standardFleetRounds in the Standard
// council); the next request offers the same tools with tool_choice none, and the forced answer ends
// the loop even when it still asks for a tool: that call is not run, and the member reports.
//
// DESIGN: a forced answer that has only tool_use blocks also ends the loop, with an empty report
// ("**Summary.** \n\nConfidence: low.") and a calls budget event; a stricter rule would name the
// stop in the report ("Fleet stopped: rounds.").
func evalBudgetRounds(t *testing.T) {
	h := newEval(t, newWork(kindMember, slugFleet, "what is the state of the devices?"), tools.LevelRead)
	h.p.devices = `[{"name":"device-00","state":"online"}]`

	// One repeat step: list_devices in every answer. The forced answer is a model that ignores
	// tool_choice none: the report and one more call (the API should not return a tool_use then,
	// but the code defends against it; see notRunRounds in turn.go). Only the force check ends the
	// loop there: turn.go's Fleet loop without `force ||` runs an 11th list_devices and ends with
	// "Fleet stopped: budget.".
	call := apiCall{id: "tu_x", name: "list_devices", input: map[string]any{}}
	h.model.push(roleFleet, &scriptStep{repeat: true, build: func(req model.Request) *scriptStep {
		if req.ToolChoiceNone {
			return mixedStep("tool_use", reportJSON("the report after the rounds"), call)
		}
		return toolStep("tool_use", call)
	}})
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	fleet := h.model.requests(roleFleet)
	if len(fleet) != standardFleetRounds+1 {
		t.Fatalf("%d Fleet requests, want %d", len(fleet), standardFleetRounds+1)
	}
	for i, req := range fleet {
		if want := i == standardFleetRounds; req.ToolChoiceNone != want {
			t.Errorf("Fleet request %d: tool_choice none %v, want %v", i, req.ToolChoiceNone, want)
		}
	}
	// What the code does at the limit: it keeps the tools and forces the answer with tool_choice
	// none (fleetTask's `r := req; r.ToolChoiceNone = force`), so the member still answers with the
	// report schema rather than being left without tools.
	if got, want := toolNames(fleet[standardFleetRounds]), toolNames(fleet[0]); !slices.Equal(got, want) {
		t.Errorf("the forced call offers %v, want the same tools as the first %v", got, want)
	}
	if n := h.p.count("list_devices"); n != standardFleetRounds {
		t.Errorf("list_devices ran %d times, want %d: the forced answer's call ran", n, standardFleetRounds)
	}
	if n := len(p.ofType(hub.EventBudget)); n != 0 {
		t.Errorf("%d budget events: the call cap, not the round limit, ended the loop", n)
	}
	res := p.result()
	if res.State != stateDone || !strings.Contains(res.Final, "the report after the rounds") {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// evalBudgetTokens: a council turn whose spending reaches the phase line (capsCouncil.tokens less
// reserveTokens) emits one tokens budget event with the limit capsCouncil.tokens; its members
// report "Fleet stopped: budget.", the critique is skipped, no phase call (member, critique, route
// round) is made after it, and the one reserved answer call still answers. The second case spends
// past the whole cap: the answer call is still made.
func evalBudgetTokens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spent int64
	}{
		{"phase_line", capsCouncil.tokens - reserveTokens},
		{"past_cap", capsCouncil.tokens + 1},
	} {
		t.Run(tc.name, func(t *testing.T) { evalBudgetTokensAt(t, tc.spent) })
	}
}

func evalBudgetTokensAt(t *testing.T, spent int64) {
	work := newWork(kindAsk, "", "why is the robot degraded?")
	work.Options.Mode = modeCouncil
	h := newEval(t, work, tools.LevelRead)

	// One route call spends the tokens.
	route := toolStep("tool_use", conveneCall("tu_1", taskOf(slugFleet, "the facts", "find out")))
	route.usage = model.Usage{Input: spent}
	h.model.push(roleRoute, route)
	h.model.push(roleFleet, textStep(reportJSON("Fleet's report"), "end_turn"))
	// The answer spends more than the reserve: the cap is a line for phase calls, not a ceiling.
	ans := textStep("the answer at the cap", "end_turn")
	ans.usage = model.Usage{Input: 30_000}
	h.model.push(roleAnswer, ans)
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 {
		t.Fatalf("%d budget events, want 1: %v", len(budgets), p.types())
	}
	if d := dataOf(t, budgets[0]); d["what"] != "tokens" {
		t.Errorf("budget what %v", d["what"])
	} else if limit, ok := d["limit"].(float64); !ok || limit != float64(capsCouncil.tokens) {
		t.Errorf("budget limit %v, want %d", d["limit"], capsCouncil.tokens)
	}
	// Fleet never reached the model: its one call was refused, and its report says so.
	if n := len(h.model.requests(roleFleet)); n != 0 {
		t.Errorf("%d Fleet requests after the token cap", n)
	}
	var summary string
	for _, e := range p.ofType(hub.EventMemberDone) {
		if rep, ok := dataOf(t, e)["report"].(map[string]any); ok {
			summary, _ = rep["summary"].(string)
		}
	}
	if summary != "Fleet stopped: budget." {
		t.Errorf("Fleet's report is %q, want %q", summary, "Fleet stopped: budget.")
	}
	if n := len(p.ofType(hub.EventCritique)); n != 0 {
		t.Error("the critique ran after the budget refused it")
	}
	// DESIGN (budget.go begin and reserveLocked; TestBudgetTokenLine): the token line stops phase
	// calls at c.tokens minus the reserve (20 000, or a quarter of the cap when that is less); the
	// one answer call is exempt (begin(true) checks calls only) and spends the reserve, even when
	// the whole cap is already spent, so exactly one ToolChoiceNone answer request follows the
	// budget event. The Stage 1 plan keeps this. A stricter rule would refuse begin(true) with
	// "tokens" once spent >= c.tokens, so that synthesisAnswer answers with
	// composedAnswer("budget", reports) and makes no model call.
	if n := h.model.count(); n != 2 {
		t.Errorf("%d model requests in all, want the route and the forced answer", n)
	}
	if answers := h.model.requests(roleAnswer); len(answers) != 1 || !answers[0].ToolChoiceNone {
		t.Fatalf("the answer calls: %d", len(answers))
	}
	evalNotOffered(t, "route", h.model.requests(roleRoute))
	evalNotOffered(t, "answer", h.model.requests(roleAnswer))
	for _, name := range []string{"whoami", "list_devices", "mqtt_get_sample", "mqtt_sample", "inspect_pod", "inspect_logs"} {
		if n := h.p.count(name); n != 0 {
			t.Errorf("%s ran %d times", name, n)
		}
	}
	res := p.result()
	if res.State != stateDone || res.Final != "the answer at the cap" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
	if want := spent + 30_000; res.Usage.Input != want {
		t.Errorf("the turn's input tokens are %d, want %d: the answer call's are counted", res.Usage.Input, want)
	}
}
