package cmd

// Tests of the third fix round of stage 0: the council's hint header, pages and inline answers that
// stay under its 16 KiB cut whatever the topic, a JSON text cut at a line, and the council's wiring.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teknoir/cli/pkg/api"
	ctools "github.com/teknoir/cli/pkg/council/tools"
)

// councilSet is the council's tool set over a server with the council's options.
func councilSet(t *testing.T, reply func(w http.ResponseWriter)) *ctools.Set {
	t.Helper()
	srv, _ := mcpSSE(t, reply, false)
	set, err := ctools.New(context.Background(), newMCPServerOpts(mcpSampleEnv(t, srv), mcpCouncilOptions(councilStore(t))))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

// councilCall calls a tool of set at the read level and fails the test on a transport error.
func councilCall(t *testing.T, set *ctools.Set, name string, in map[string]any) ctools.Result {
	t.Helper()
	raw, _ := json.Marshal(in)
	r, err := set.Call(context.Background(), ctools.LevelRead, name, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

// The council's index of 200 messages with long topics leaves messages out; its header names them,
// first, in whole words, and the hint part is at most 200 bytes.
func TestCouncilIndexHeaderNamesTheOmitted(t *testing.T) {
	set := councilSet(t, func(w http.ResponseWriter) {
		for i := 0; i < 200; i++ {
			topic := fmt.Sprintf("/devices/d1/state/%03d", i)
			topic += strings.Repeat("t", 200-len(topic))
			_, _ = io.WriteString(w, mcpSSEMsg(topic, strings.Repeat("p", 40)))
		}
	})
	r := councilCall(t, set, "mqtt_get_sample", sampleArgs(map[string]any{"max": 200}))
	if r.IsError {
		t.Fatalf("mqtt_get_sample: %q", r.Text)
	}
	var idx mcpGetSampleIndex
	if err := json.Unmarshal([]byte(r.Text), &idx); err != nil {
		t.Fatalf("index: %v", err)
	}
	if idx.Omitted == 0 || !idx.Truncated || !r.Upstream || r.Cut.Truncated {
		t.Fatalf("omitted %d, truncated %v, upstream %v, council cut %v", idx.Omitted, idx.Truncated, r.Upstream, r.Cut.Truncated)
	}
	n := len(idx.Messages)
	want := fmt.Sprintf("the other %d (i %d to %d)", idx.Omitted, n, idx.Count-1)
	text, _ := ctools.Render("mqtt_get_sample", r, "r1")
	header := strings.SplitN(text, "\n", 2)[0]
	if !strings.Contains(header, want) || !strings.Contains(header, fmt.Sprintf("lists messages 0 to %d of %d", n-1, idx.Count)) {
		t.Errorf("header = %q, want %q", header, want)
	}
	if len(r.Hint) > 200 {
		t.Errorf("the hint is %d bytes", len(r.Hint))
	}
	if !strings.HasSuffix(r.Hint, ".") || !strings.HasPrefix(idx.Hint, r.Hint) {
		t.Errorf("the hint %q is not a whole-sentence head of %q", r.Hint, idx.Hint)
	}
	if !strings.Contains(header, r.Hint+" Do not infer") {
		t.Errorf("header = %q", header)
	}
}

// pageAll reads message 0 of a capture through the council's set, page after page: every page fits
// the council's cut, parses, and the pages join to the payload.
func pageAll(t *testing.T, set *ctools.Set, id, topic, payload string) {
	t.Helper()
	var joined strings.Builder
	offset := 0
	for pages := 0; ; pages++ {
		if pages > 200 {
			t.Fatal("the pages do not end")
		}
		r := councilCall(t, set, "mqtt_read", map[string]any{"captureId": id, "i": 0, "offset": offset})
		if r.IsError || r.Cut.Truncated || len(r.Text) > 16384 {
			t.Fatalf("page at %d: error %v, cut %v, %d bytes", offset, r.IsError, r.Cut.Truncated, len(r.Text))
		}
		var p struct {
			mcpReadPage
			TopicBytes int `json:"topicBytes"`
		}
		if err := json.Unmarshal([]byte(r.Text), &p); err != nil {
			t.Fatalf("page at %d does not parse: %v", offset, err)
		}
		if p.TopicBytes != len(topic) {
			t.Errorf("topicBytes %d, want %d", p.TopicBytes, len(topic))
		}
		joined.WriteString(p.Data)
		if p.NextOffset == nil {
			if p.SHA256 != sha256Hex([]byte(payload)) {
				t.Errorf("sha256 %s", p.SHA256)
			}
			break
		}
		offset = *p.NextOffset
	}
	if sha256Hex([]byte(joined.String())) != sha256Hex([]byte(payload)) {
		t.Errorf("the pages join to %d bytes, want the %d of the payload", joined.Len(), len(payload))
	}
}

// A topic of quotes or control characters is capped by its escaped size: every page of a 60 KiB
// payload of quotes, and the index, stay under the council's 16 KiB.
func TestCouncilPagesWithEscapingTopics(t *testing.T) {
	payload := strings.Repeat(`"`, 60<<10)
	for _, topic := range []string{
		"/devices/d1/state/" + strings.Repeat(`"`, 1006),
		"/devices/d1/state/" + strings.Repeat("\x01", 1100),
	} {
		set := councilSet(t, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEMsg(topic, payload))
		})
		r := councilCall(t, set, "mqtt_get_sample", sampleArgs(nil))
		if r.IsError || r.Cut.Truncated || len(r.Text) > 16384 {
			t.Fatalf("index: error %v, cut %v, %d bytes", r.IsError, r.Cut.Truncated, len(r.Text))
		}
		var idx mcpGetSampleIndex
		if err := json.Unmarshal([]byte(r.Text), &idx); err != nil || idx.CaptureID == "" {
			t.Fatalf("index: %v %.200q", err, r.Text)
		}
		if got := idx.Messages[0].TopicBytes; got != len(topic) {
			t.Errorf("index topicBytes %d, want %d", got, len(topic))
		}
		pageAll(t, set, idx.CaptureID, topic, payload)
	}
}

// mcpTopicPage caps the escaped size, not the raw one, and never splits a rune.
func TestMCPTopicPageEscapedCap(t *testing.T) {
	for _, topic := range []string{
		strings.Repeat("a", 2000), strings.Repeat(`"`, 2000), strings.Repeat("\x01", 2000),
		strings.Repeat("é", 2000), strings.Repeat(" ", 2000),
	} {
		got, whole := mcpTopicPage(topic)
		q, _ := mcpMarshal(got)
		if len(q)-2 > mcpTopicMax || whole != len(topic) || !strings.HasPrefix(topic, got) {
			t.Errorf("%.8q: %d escaped bytes, whole %d", topic, len(q)-2, whole)
		}
	}
	if got, whole := mcpTopicPage("/devices/d1/state/x"); got != "/devices/d1/state/x" || whole != 0 {
		t.Errorf("a short topic: %q %d", got, whole)
	}
}

// A path longer than 512 escaped bytes is refused as a usage error that says so.
func TestCouncilReadRefusesALongPath(t *testing.T) {
	set := councilSet(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", `{"a":"`+strings.Repeat("v", 13000)+`"}`))
	})
	r := councilCall(t, set, "mqtt_get_sample", sampleArgs(nil))
	var idx mcpGetSampleIndex
	if err := json.Unmarshal([]byte(r.Text), &idx); err != nil || idx.CaptureID == "" {
		t.Fatalf("index: %v %.200q", err, r.Text)
	}
	r = councilCall(t, set, "mqtt_read", map[string]any{"captureId": idx.CaptureID, "i": 0, "path": strings.Repeat("a", 600)})
	if !r.IsError || !strings.Contains(r.Text, "path is 600 bytes") || !strings.Contains(r.Text, "512") {
		t.Errorf("a 600-byte path: error %v, %q", r.IsError, r.Text)
	}
	r = councilCall(t, set, "mqtt_read", map[string]any{"captureId": idx.CaptureID, "i": 0, "path": "a"})
	if r.IsError {
		t.Errorf("a short path: %q", r.Text)
	}
}

// inspect_logs over 1 MiB: the server cuts the JSON at a line, and neither header line counts the
// wrapper's lines; both say bytes of JSON text.
func TestCouncilInspectLogsAtTheLimit(t *testing.T) {
	var log strings.Builder
	for i := 0; log.Len() < 1<<20; i++ {
		fmt.Fprintf(&log, `{"ts":"2026-11-13T10:00:00Z","level":"info","n":%d,"msg":"%s"}`+"\n", i, strings.Repeat("m", 500))
	}
	raw, _ := json.Marshal(api.InspectLogsResponse{Robot: "r", Namespace: "default", Pod: "p", Tail: 5000,
		Text: log.String(), Lines: 2000, Bytes: log.Len(), Truncated: api.InspectTruncated{Bytes: true}})
	text := mcpCutMax(mcpIndent(raw), mcpCouncilMaxResult)
	if !strings.HasSuffix(text, "bytes of JSON text; narrow the query") {
		t.Fatalf("the server's cut ends %q", text[len(text)-120:])
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	s.AddTool(&mcp.Tool{Name: "inspect_logs", Description: "logs", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
		})
	set, err := ctools.New(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	r, err := set.Call(context.Background(), ctools.LevelCreator, "inspect_logs", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Upstream || !r.Cut.Truncated {
		t.Fatalf("upstream %v, council cut %v, error %v: %.300q", r.Upstream, r.Cut.Truncated, r.IsError, r.Text)
	}
	out, digest := ctools.Render("inspect_logs", r, "r1")
	for _, got := range []string{out, digest} {
		lines := strings.SplitN(got, "\n", 3)
		for _, h := range lines[:2] {
			if !strings.HasPrefix(h, "RESULT PARTIAL") || strings.Contains(h, " lines") || !strings.Contains(h, "bytes of JSON text") {
				t.Errorf("header line %q", h)
			}
		}
	}
}

// With the council's options the inline answer of mqtt_get_sample is held to 16 KiB: a sample that
// would come back larger is spilled, and its index is not cut. The stdio server keeps 48 KiB.
func TestCouncilInlineFits(t *testing.T) {
	for name, reply := range map[string]func(w http.ResponseWriter){
		"130 messages": func(w http.ResponseWriter) {
			for i := 0; i < 130; i++ {
				_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/t%03d", i), strings.Repeat("t", 91)))
			}
		},
		"12000 quotes": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/q", strings.Repeat(`"`, 12000)))
		},
	} {
		set := councilSet(t, reply)
		r := councilCall(t, set, "mqtt_get_sample", sampleArgs(map[string]any{"max": 200}))
		if r.IsError || r.Cut.Truncated || len(r.Text) > 16384 || strings.Contains(r.Text, `"shownItems"`) {
			t.Errorf("%s: error %v, cut %v, %d bytes", name, r.IsError, r.Cut.Truncated, len(r.Text))
			continue
		}
		var idx mcpGetSampleIndex
		if err := json.Unmarshal([]byte(r.Text), &idx); err != nil || idx.CaptureID == "" {
			t.Errorf("%s: not spilled: %v %.200q", name, err, r.Text)
		}
	}

	mcpCaptureFast(t)
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", strings.Repeat("x", 12000)))
	})
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	if got.CaptureID != "" || len(got.Messages) != 1 {
		t.Errorf("stdio: spilled %q with %d messages, want inline", got.CaptureID, len(got.Messages))
	}
}

// The description of mqtt_get_sample names the inline bound of its server.
func TestMCPGetSampleDescriptionBound(t *testing.T) {
	if d := mcpGetSampleDescriptionFor(mcpSizes{}); !strings.Contains(d, "fits under 48 KiB") {
		t.Errorf("stdio: %q", d)
	}
	o := mcpCouncilOptions(nil)
	d := mcpGetSampleDescriptionFor(mcpSizes{pageEscaped: o.PageEscapedMax, indexMax: o.IndexMax})
	if !strings.Contains(d, "fits under 16 KiB") || strings.Contains(d, "48 KiB") {
		t.Errorf("council: %q", d)
	}

	// The servers register the description of their own sizes.
	for _, c := range []struct {
		name, want, not string
		cs              *mcp.ClientSession
	}{
		{"stdio", "fits under 48 KiB", "16 KiB", mcpClient(t, newMCPServer(&cliEnv{}))},
		{"council", "fits under 16 KiB", "48 KiB", councilSized(t, func(http.ResponseWriter) {})},
	} {
		tools, err := c.cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tl := range tools.Tools {
			if tl.Name != "mqtt_get_sample" {
				continue
			}
			found = true
			if !strings.Contains(tl.Description, c.want) || strings.Contains(tl.Description, c.not) {
				t.Errorf("%s: the registered description does not name its bound: %q", c.name, tl.Description)
			}
		}
		if !found {
			t.Errorf("%s: mqtt_get_sample is not listed", c.name)
		}
	}
}

// The council's store sweeps its dir when it starts: a fresh capture of an earlier process goes, a
// file that is no capture stays.
func TestNewCouncilCaptureStoreSweeps(t *testing.T) {
	dir := t.TempDir()
	fresh := filepath.Join(dir, "cap-0123456789ab.jsonl")
	other := filepath.Join(dir, "notes.txt")
	for _, f := range []string{fresh, other} {
		if err := os.WriteFile(f, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st := newCouncilCaptureStore(dir)
	t.Cleanup(st.removeAll)
	if _, err := os.Stat(fresh); !os.IsNotExist(err) {
		t.Errorf("the fresh capture stayed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file that is no capture was removed: %v", err)
	}
	o := mcpCouncilOptions(st)
	if o.Captures != st || o.MaxResult != mcpCouncilMaxResult || o.PageEscapedMax != 14<<10 || o.IndexMax != 14<<10 {
		t.Errorf("options = %+v", o)
	}
}

// An $XDG_RUNTIME_DIR that cannot hold the captures is passed over for the cache dir.
func TestStdioCaptureDirUnusableRuntime(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache dir here")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, x := range []string{filepath.Join(t.TempDir(), "gone"), file} {
		t.Setenv("XDG_RUNTIME_DIR", x)
		if got := defaultMCPStdioCaptureDir(); got != filepath.Join(cache, "tnctl", "captures") {
			t.Errorf("XDG_RUNTIME_DIR=%s: %q, want the cache dir's", x, got)
		}
	}
}
