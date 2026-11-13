package cmd

// Tests of mqtt_get_sample, mqtt_read and the capture store behind them.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"unicode/utf8"

	"github.com/teknoir/cli/pkg/api"
)

// mcpCaptureFast puts the server's captures in the test's own directory.
func mcpCaptureFast(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	old := mcpStdioCaptureDir
	mcpStdioCaptureDir = func() string { return dir }
	t.Cleanup(func() { mcpStdioCaptureDir = old })
}

// mcpGetSampleOut is a decoded mqtt_get_sample result: the inline shape, and the spilled one.
type mcpGetSampleOut struct {
	Messages        []json.RawMessage `json:"messages"`
	Count           int               `json:"count"`
	TotalBytes      int               `json:"totalBytes"`
	StoppedBy       string            `json:"stoppedBy"`
	ServerDropped   streamDropped     `json:"serverDropped"`
	Truncated       bool              `json:"truncated"`
	CaptureID       string            `json:"captureId"`
	File            string            `json:"file"`
	ExpiresAt       string            `json:"expiresAt"`
	PreviewsOmitted int               `json:"previewsOmitted"`
	Omitted         int               `json:"omitted"`
	Hint            string            `json:"hint"`
}

// mcpGetSampleOf decodes a successful mqtt_get_sample result, with its text.
func mcpGetSampleOf(t *testing.T, res *mcp.CallToolResult) (mcpGetSampleOut, string) {
	t.Helper()
	if res.IsError {
		t.Fatalf("error result: %s", mcpText(t, res))
	}
	text := mcpText(t, res)
	var out mcpGetSampleOut
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("result %q: %v", text, err)
	}
	return out, text
}

// mcpReadPage is a decoded mqtt_read result.
type mcpReadPage struct {
	CaptureID  string `json:"captureId"`
	I          int    `json:"i"`
	Topic      string `json:"topic"`
	Path       string `json:"path"`
	Offset     int    `json:"offset"`
	Length     int    `json:"length"`
	TotalBytes int    `json:"totalBytes"`
	NextOffset *int   `json:"nextOffset"`
	Encoding   string `json:"encoding"`
	SHA256     string `json:"sha256"`
	Data       string `json:"data"`
}

// mcpReadOf decodes a successful mqtt_read result.
func mcpReadOf(t *testing.T, res *mcp.CallToolResult) mcpReadPage {
	t.Helper()
	if res.IsError {
		t.Fatalf("error result: %s", mcpText(t, res))
	}
	var p mcpReadPage
	if err := json.Unmarshal([]byte(mcpText(t, res)), &p); err != nil {
		t.Fatalf("result %q: %v", mcpText(t, res), err)
	}
	return p
}

// mcpReadAll pages message i of a capture from start to end and joins the pages.
func mcpReadAll(t *testing.T, cs *mcp.ClientSession, id string, i, length int) string {
	t.Helper()
	var b strings.Builder
	offset := 0
	for {
		res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": i, "offset": offset, "length": length})
		p := mcpReadOf(t, res)
		b.WriteString(p.Data)
		if p.NextOffset == nil {
			return b.String()
		}
		if *p.NextOffset <= offset {
			t.Fatalf("nextOffset %d does not advance from %d", *p.NextOffset, offset)
		}
		offset = *p.NextOffset
	}
}

// mcpGetSampleEnv is a server over a stream that writes what reply does.
func mcpGetSampleEnv(t *testing.T, reply func(w http.ResponseWriter)) *mcp.ClientSession {
	t.Helper()
	srv, _ := mcpSSE(t, reply, false)
	return mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
}

func TestMCPGetSampleInline(t *testing.T) {
	mcpCaptureFast(t)
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/a", "hello"))
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{"topic": "/devices/d1/state/b", "payloadBase64": "AAEC"}))
	})
	got, text := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	if got.CaptureID != "" {
		t.Errorf("a small sample spilled: %q", text)
	}
	if len(got.Messages) != 2 || got.Count != 2 || got.TotalBytes != 8 || got.StoppedBy != "end" {
		t.Errorf("%d messages, count %d, %d bytes, stopped by %q", len(got.Messages), got.Count, got.TotalBytes, got.StoppedBy)
	}
	var first mcpInlineMessage
	if err := json.Unmarshal(got.Messages[0], &first); err != nil {
		t.Fatal(err)
	}
	if first.Payload != "hello" || first.Encoding != "utf8" || first.I != 0 || first.Bytes != 5 {
		t.Errorf("first %+v", first)
	}
	var second mcpInlineMessage
	if err := json.Unmarshal(got.Messages[1], &second); err != nil {
		t.Fatal(err)
	}
	if second.Payload != "AAEC" || second.Encoding != "base64" || second.Bytes != 3 {
		t.Errorf("second %+v", second)
	}
}

// The inline/spill boundary is exactly 12288 payload bytes.
func TestMCPGetSampleInlineBoundary(t *testing.T) {
	for _, c := range []struct {
		bytes  int
		spills bool
	}{{12288, false}, {12289, true}} {
		mcpCaptureFast(t)
		payload := strings.Repeat("m", c.bytes)
		cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", payload))
		})
		got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
		if spilled := got.CaptureID != ""; spilled != c.spills {
			t.Errorf("%d payload bytes: spilled %v, want %v", c.bytes, spilled, c.spills)
		}
	}
}

// The brief's worst preview case: 200 messages of 512 bytes drop previews from the last message
// backwards until the index fits, and say how many.
func TestMCPGetSampleOmitsPreviews(t *testing.T) {
	mcpCaptureFast(t)
	store := newCaptureStore(t.TempDir())
	c := &mcpCapturedSample{}
	for i := 0; i < 200; i++ {
		line, data := sampleMsgOf(i, &api.StreamMessage{
			Topic: fmt.Sprintf("/devices/d1/state/x%d", i), Payload: strings.Repeat("p", 512), TS: "t"})
		c.messages = append(c.messages, mcpCapturedMessage{line: line, data: data, kind: "text"})
	}
	res, err := c.spill(store)
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviewsOmitted == 0 || res.Omitted != 0 {
		t.Errorf("%d previews omitted, %d messages omitted: the previews should have paid first", res.PreviewsOmitted, res.Omitted)
	}
	if b, _ := mcpMarshal(res); len(b) > mcpIndexMax {
		t.Errorf("the index is %d bytes", len(b))
	}
	if res.Messages[0].Preview == "" || res.Messages[199].Preview != "" {
		t.Errorf("first preview %q, last %q", res.Messages[0].Preview, res.Messages[199].Preview)
	}
}

func TestMCPGetSampleSpillsAboveInline(t *testing.T) {
	mcpCaptureFast(t)
	payload := strings.Repeat("m", 13<<10)
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", payload))
	})
	res := mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil))
	got, text := mcpGetSampleOf(t, res)
	if got.CaptureID == "" || !validCaptureID(got.CaptureID) {
		t.Fatalf("not spilled: %q", text)
	}
	if got.Count != 1 || got.TotalBytes != len(payload) || got.StoppedBy != "end" {
		t.Errorf("count %d, %d bytes, stopped by %q", got.Count, got.TotalBytes, got.StoppedBy)
	}
	if !strings.Contains(text, `"hint"`) || !strings.Contains(text, "mqtt_read") {
		t.Errorf("no hint: %q", text)
	}
	var m mcpGetSampleMessage
	if err := json.Unmarshal(got.Messages[0], &m); err != nil {
		t.Fatal(err)
	}
	if m.Kind != "text" || m.JSONValid || m.SHA256 != sha256Hex([]byte(payload)) || m.Bytes != len(payload) {
		t.Errorf("index entry %+v", m)
	}
	if !strings.HasPrefix(m.Preview, "mmm") || len(m.Preview) > mcpPreviewMax {
		t.Errorf("preview %q", m.Preview)
	}
	if _, err := os.Stat(got.File); err != nil {
		t.Errorf("the capture file: %v", err)
	}

	// The payload reads back whole, byte for byte, and its hash matches.
	sum := sha256.Sum256([]byte(mcpReadAll(t, cs, got.CaptureID, 0, mcpPageMax)))
	if want := sha256Hex([]byte(payload)); hex.EncodeToString(sum[:]) != want {
		t.Errorf("the pages do not reassemble to the payload")
	}
}

// jsonOfBytes is json.Marshal for a test fixture.
func jsonOfBytes(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sha256Hex is the hex SHA-256 of b.
func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// A 200 KiB JSON payload pages back byte for byte, and one field of it reads by path.
func TestMCPGetSamplePagesAJSONPayload(t *testing.T) {
	mcpCaptureFast(t)
	detections := make([]any, 200)
	for i := range detections {
		detections[i] = map[string]any{"label": fmt.Sprintf("person-%03d", i), "score": 0.5,
			"box": map[string]any{"x": i, "y": i * 2}}
	}
	payload := string(jsonOfBytes(t, map[string]any{"kind": "Alert", "detections": detections,
		"note": strings.Repeat("ü", 100<<10)}))
	if len(payload) < 200<<10 {
		t.Fatalf("the fixture is %d bytes, want at least 200 KiB", len(payload))
	}
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/events/alerts", payload))
	})
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	if got.CaptureID == "" {
		t.Fatal("a 200 KiB payload was not spilled")
	}
	back := mcpReadAll(t, cs, got.CaptureID, 0, mcpPageMax)
	if back != payload {
		t.Fatalf("the pages came back as %d bytes, want %d", len(back), len(payload))
	}
	if n := (len(payload) + mcpPageMax - 1) / mcpPageMax; n < 15 {
		t.Errorf("the payload is %d pages", n)
	}
	p := mcpReadOf(t, mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0,
		"path": "detections[3].box.y"}))
	if p.Data != "6" || p.Path != "detections[3].box.y" {
		t.Errorf("path read %q (path %q), want 6", p.Data, p.Path)
	}
	if p.SHA256 != sha256Hex([]byte(payload)) {
		t.Errorf("sha256 %q", p.SHA256)
	}
	var m mcpGetSampleMessage
	if err := json.Unmarshal(got.Messages[0], &m); err != nil {
		t.Fatal(err)
	}
	if m.Kind != "json" || !m.JSONValid {
		t.Errorf("kind %q, jsonValid %v", m.Kind, m.JSONValid)
	}
}

// A binary payload comes back as base64 that decodes to the same bytes.
func TestMCPGetSamplePagesABinaryPayload(t *testing.T) {
	mcpCaptureFast(t)
	raw := append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, bytes.Repeat([]byte{0x42, 0x00, 0xff}, 20<<10)...)
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{
			"topic": "/devices/d1/events/x", "payloadBase64": base64.StdEncoding.EncodeToString(raw)}))
	})
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	if got.CaptureID == "" {
		t.Fatal("not spilled")
	}
	var m mcpGetSampleMessage
	if err := json.Unmarshal(got.Messages[0], &m); err != nil {
		t.Fatal(err)
	}
	if m.Kind != "png" || m.Encoding != "base64" || m.SHA256 != sha256Hex(raw) {
		t.Errorf("index entry %+v", m)
	}
	if !strings.HasPrefix(m.Preview, "hex:89504e47") {
		t.Errorf("preview %q", m.Preview)
	}
	text := mcpReadAll(t, cs, got.CaptureID, 0, mcpPageMax)
	decoded, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		t.Fatalf("the pages are not base64: %v", err)
	}
	if !bytes.Equal(decoded, raw) || len(text) != base64.StdEncoding.EncodedLen(len(raw)) {
		t.Errorf("the pages decode to %d bytes, want %d", len(decoded), len(raw))
	}
}

func TestMCPGetSampleKinds(t *testing.T) {
	for _, c := range []struct {
		name     string
		encoding string
		payload  []byte
		want     string
	}{
		{name: "json", payload: []byte(`{"a":1}`), want: "json"},
		{name: "text", payload: []byte("hello"), want: "text"},
		{name: "jpeg", payload: []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2}, want: "jpeg"},
		{name: "png", payload: []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1}, want: "png"},
		{name: "gzip", payload: []byte{0x1f, 0x8b, 0x08, 0}, want: "gzip"},
		// The stream sends a payload it cannot decode as UTF-8 base64; those bytes that would be
		// valid UTF-8 are not a text payload on this path.
		{name: "binary base64", encoding: "base64", payload: []byte{0x00, 0x01, 0x02}, want: "binary"},
		{name: "json with a whitespace", payload: []byte(" {\"a\" : [1]} \n"), want: "json"},
		{name: "prose that starts like json", payload: []byte("{not json"), want: "text"},
	} {
		enc := c.encoding
		if enc == "" {
			enc = "utf8"
		}
		if got := mcpPayloadKind(enc, c.payload); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// A base64 payload is never JSON, whatever its bytes look like.
	if got := mcpPayloadKind("base64", []byte(`{"a":1}`)); got != "binary" {
		t.Errorf("base64 of JSON: %q, want binary", got)
	}
}

// 200 messages with 8 maximum-length topics and 512-byte payloads keep the index under 40960 bytes,
// dropping previews from the last message backwards.
func TestMCPGetSampleIndexStaysBounded(t *testing.T) {
	mcpCaptureFast(t)
	c := &mcpCapturedSample{}
	topic := "/devices/" + strings.Repeat("a", 63) + "/state/" + strings.Repeat("b", 63) + "/" + strings.Repeat("c", 63) + "/" + strings.Repeat("d", 49)
	if len(topic) > 256 {
		t.Fatalf("the topic is %d characters", len(topic))
	}
	for i := 0; i < 200; i++ {
		line, data := sampleMsgOf(i, &api.StreamMessage{Topic: topic, Payload: strings.Repeat("p", 512), TS: "t"})
		c.messages = append(c.messages, mcpCapturedMessage{line: line, data: data, kind: "text"})
	}
	store := newCaptureStore(t.TempDir())
	res, err := c.spill(store)
	if err != nil {
		t.Fatal(err)
	}
	b, err := mcpMarshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > mcpIndexMax {
		t.Errorf("the index is %d bytes, want at most %d", len(b), mcpIndexMax)
	}
	if res.Count != 200 {
		t.Errorf("count %d, want 200", res.Count)
	}
	if len(res.Messages)+res.Omitted != 200 {
		t.Errorf("%d listed and %d omitted, want 200 together", len(res.Messages), res.Omitted)
	}
	// The index the agent sees, with mcpResult's indentation, still fits the result cap without the
	// truncation marker.
	if text := mcpText(t, mcpResult(marshalRaw(res))); strings.Contains(text, mcpCutMarker) {
		t.Errorf("the index was cut by mcpResult: %d bytes", len(text))
	}
}

// mcpSpilledJSON is a server whose one message is payload, spilled to a capture: its id and client.
func mcpSpilledJSON(t *testing.T, payload string) (*mcp.ClientSession, string) {
	t.Helper()
	mcpCaptureFast(t)
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/events/alerts", payload))
	})
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	if got.CaptureID == "" {
		t.Fatalf("a %d-byte payload was not spilled", len(payload))
	}
	return cs, got.CaptureID
}

func TestMCPReadPath(t *testing.T) {
	payload := string(jsonOfBytes(t, map[string]any{
		"note": strings.Repeat("x", 20<<10),
		"data": map[string]any{"label": "person", "score": 0.9,
			"items": []any{map[string]any{"id": 1}, map[string]any{"id": 2}}},
		"dotted.key": 1,
	}))
	cs, id := mcpSpilledJSON(t, payload)

	read := func(path string) (*mcp.CallToolResult, mcpReadPage) {
		res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": path})
		return res, mcpReadPage{}
	}
	for _, c := range []struct{ path, want string }{
		{"data.label", `"person"`},
		{"data.items[1].id", "2"},
		{"data.items[0]", `{"id":1}`},
		{"data", `{"items":[{"id":1},{"id":2}],"label":"person","score":0.9}`},
	} {
		res, _ := read(c.path)
		if res.IsError {
			t.Errorf("%s: %s", c.path, mcpText(t, res))
			continue
		}
		if p := mcpReadOf(t, res); p.Data != c.want || p.Path != c.path {
			t.Errorf("%s: %q (path %q), want %q", c.path, p.Data, p.Path, c.want)
		}
	}

	// A missing key names the first step that failed and the keys available there.
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": "data.nope"})
	text := mcpText(t, res)
	if !res.IsError || !strings.Contains(text, `there is no key "nope" at data; it has:`) ||
		!strings.Contains(text, "items") || !strings.Contains(text, "label") {
		t.Errorf("%+v %q", res.IsError, text)
	}
	// A missing index names the list and its length.
	res = mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": "data.items[5]"})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, "there is no [5]") || !strings.Contains(text, "has 2") {
		t.Errorf("%+v %q", res.IsError, text)
	}
	// A key that holds a dot cannot be addressed, and the error says so.
	res = mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": "dotted.key"})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, "cannot be addressed") {
		t.Errorf("%+v %q", res.IsError, text)
	}
	// path on a payload that is not JSON.
	res = mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": "["})
	if text := mcpText(t, res); !res.IsError {
		t.Errorf("a broken path succeeded: %q", text)
	}
}

func TestMCPReadPathNeedsJSON(t *testing.T) {
	mcpCaptureFast(t)
	text := strings.Repeat("just text, not JSON. ", 1000)
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", text))
	})
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0, "path": "a.b"})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, "is not JSON") {
		t.Errorf("%+v %q", res.IsError, text)
	}
}

// Text pages end where a rune starts, and a page that would cut one asks for nextOffset instead.
func TestMCPReadRuneBoundaries(t *testing.T) {
	mcpCaptureFast(t)
	// "ü" is two bytes: byte 12287 is the first byte of one, so a full page must end at 12286.
	payload := "a" + strings.Repeat("ü", 8000)
	cs := mcpGetSampleEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", payload))
	})
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	p := mcpReadOf(t, mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0}))
	if p.Length > mcpPageMax || !utf8.ValidString(p.Data) || strings.ContainsRune(p.Data, utf8.RuneError) {
		t.Errorf("page of %d bytes (valid %v)", p.Length, utf8.ValidString(p.Data))
	}
	if p.NextOffset == nil {
		t.Fatal("no nextOffset on a 16 KiB payload")
	}
	// An offset inside a character is an error that names nextOffset.
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0, "offset": 2})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, "nextOffset") {
		t.Errorf("%+v %q", res.IsError, text)
	}
	if back := mcpReadAll(t, cs, got.CaptureID, 0, mcpPageMax); back != payload {
		t.Errorf("the pages came back as %d bytes, want %d", len(back), len(payload))
	}
}

// A capture id that is unknown, expired or malformed is one answer: take a new sample.
func TestMCPReadRefusesBadIDs(t *testing.T) {
	mcpCaptureFast(t)
	cs, id := mcpSpilledJSON(t, strings.Repeat(`{"a":"`+strings.Repeat("v", 200)+`"}`, 100))
	const want = "is unknown or expired"
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"unknown", map[string]any{"captureId": "cap-0123456789ab", "i": 0}},
		{"malformed", map[string]any{"captureId": "cap-xyz", "i": 0}},
		{"a path", map[string]any{"captureId": "../../etc/passwd", "i": 0}},
		{"no prefix", map[string]any{"captureId": "0123456789ab", "i": 0}},
	} {
		res := mcpCall(t, cs, "mqtt_read", c.args)
		if text := mcpText(t, res); !res.IsError || !strings.Contains(text, want) || !strings.Contains(text, "mqtt_get_sample") {
			t.Errorf("%s: %+v %q", c.name, res.IsError, text)
		}
	}
	// The real one reads, to prove the refusals above were about the ids.
	if res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "length": 10}); res.IsError {
		t.Fatalf("%s", mcpText(t, res))
	}
	// An id that expires answers the same way.
	old := mcpCaptureTTL
	mcpCaptureTTL = time.Millisecond
	t.Cleanup(func() { mcpCaptureTTL = old })
	cs2, id2 := mcpSpilledJSON(t, strings.Repeat(`{"a":"`+strings.Repeat("v", 200)+`"}`, 100))
	time.Sleep(5 * time.Millisecond)
	res := mcpCall(t, cs2, "mqtt_read", map[string]any{"captureId": id2, "i": 0})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, want) {
		t.Errorf("expired: %+v %q", res.IsError, text)
	}
}

func TestMCPReadRefusesBadArguments(t *testing.T) {
	cs, id := mcpSpilledJSON(t, strings.Repeat(`{"a":"`+strings.Repeat("v", 200)+`"}`, 100))
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"captureId": id, "i": 1}, "i must be from 0 to 0: the capture has 1 messages"},
		{map[string]any{"captureId": id, "i": -1}, "i must be from 0 to 0"},
		{map[string]any{"captureId": id, "i": 0, "length": 12289}, "length must be from 1 to 12288"},
		{map[string]any{"captureId": id, "i": 0, "length": -1}, "length must be from 1 to 12288"},
		{map[string]any{"captureId": id, "i": 0, "offset": -1}, "offset must be 0 or more"},
		{map[string]any{"captureId": id, "i": 0, "offset": 1 << 30}, "past the end"},
	} {
		res := mcpCall(t, cs, "mqtt_read", c.args)
		if text := mcpText(t, res); !res.IsError || !strings.Contains(text, c.want) {
			t.Errorf("%v: %+v %q, want %q", c.args, res.IsError, text, c.want)
		}
	}
}

// stoppedBy size carries the size of the message it refused.
func TestMCPGetSampleStopsAtMaxBytes(t *testing.T) {
	mcpCaptureFast(t)
	srv, ended := mcpSSE(t, func(w http.ResponseWriter) {
		for i := 0; i < 5; i++ {
			_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/x%d", i), strings.Repeat("m", 600)))
		}
	}, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(map[string]any{"maxBytes": 2048})))
	if got.StoppedBy != "size" || got.Count != 3 {
		t.Errorf("stopped by %q with %d messages, want size and 3", got.StoppedBy, got.Count)
	}
	var idx struct {
		NextMessageBytes int `json:"nextMessageBytes"`
	}
	res := mcpCall(t, cs, "mqtt_get_sample", sampleArgs(map[string]any{"maxBytes": 1024}))
	if err := json.Unmarshal([]byte(mcpText(t, res)), &idx); err != nil {
		t.Fatalf("%s", mcpText(t, res))
	}
	if idx.NextMessageBytes != 600 {
		t.Errorf("nextMessageBytes %d, want 600", idx.NextMessageBytes)
	}
	mcpWaitEnded(t, ended)
}

// serverDropped is surfaced by mqtt_get_sample too, with the reason it did not know as other.
func TestMCPGetSampleServerDropped(t *testing.T) {
	mcpCaptureFast(t)
	srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 2, "reason": "size"}))
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 1, "reason": "rate"}))
		_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 4, "reason": "who"}))
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", "hi"))
	}, false)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
	got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	if got.ServerDropped != (streamDropped{Size: 2, Rate: 1, Other: 4}) {
		t.Errorf("serverDropped %+v", got.ServerDropped)
	}
}

// mqtt_sample and mqtt_get_sample share the one-sample slot, and the refusal names the running one.
func TestMCPGetSampleSharesTheSlot(t *testing.T) {
	mcpCaptureFast(t)
	mcpSampleFast(t)
	started := make(chan struct{}, 1)
	srv, _ := mcpSSE(t, func(http.ResponseWriter) { started <- struct{}{} }, true)
	cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))

	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "mqtt_get_sample",
			Arguments: sampleArgs(map[string]any{"seconds": 30})})
		if err != nil {
			t.Errorf("the first sample: %v", err)
		}
		done <- res
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the first sample did not start")
	}
	res := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil))
	if want := "another mqtt_get_sample is running; put all topics (up to 8) in one call, or try again when it ends"; !res.IsError || mcpText(t, res) != want {
		t.Errorf("%+v %q, want %q", res.IsError, mcpText(t, res), want)
	}
	select {
	case r := <-done:
		if r.IsError {
			t.Errorf("the first sample: %s", mcpText(t, r))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first sample did not end")
	}
	// The slot is free again, and the refusal names the other side.
	started2 := make(chan struct{}, 1)
	srv2, _ := mcpSSE(t, func(http.ResponseWriter) { started2 <- struct{}{} }, true)
	cs2 := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv2)))
	go func() {
		_, _ = cs2.CallTool(context.Background(), &mcp.CallToolParams{Name: "mqtt_sample",
			Arguments: sampleArgs(map[string]any{"seconds": 30})})
	}()
	select {
	case <-started2:
	case <-time.After(2 * time.Second):
		t.Fatal("the mqtt_sample did not start")
	}
	res = mcpCall(t, cs2, "mqtt_get_sample", sampleArgs(nil))
	if want := "another mqtt_sample is running; put all topics (up to 8) in one call, or try again when it ends"; !res.IsError || mcpText(t, res) != want {
		t.Errorf("%+v %q, want %q", res.IsError, mcpText(t, res), want)
	}
}

func TestMCPGetSampleRefusesBadInput(t *testing.T) {
	mcpCaptureFast(t)
	noBotMode(t)
	p := newFakePlatform(t)
	e, _ := testEnv(t, p, nil)
	cs := mcpClient(t, newMCPServer(e))
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = fmt.Sprintf("/devices/d%d/state/#", i)
	}
	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"topics": []string{}}, "a stream follows 1 to 8 topics, got 0"},
		{map[string]any{"topics": nine}, "a stream follows 1 to 8 topics, got 9"},
		{sampleArgs(map[string]any{"seconds": 31}), "seconds must be from 1 to 30"},
		{sampleArgs(map[string]any{"seconds": -1}), "seconds must be from 1 to 30"},
		{sampleArgs(map[string]any{"max": 201}), "max must be from 1 to 200"},
		{sampleArgs(map[string]any{"maxBytes": 1023}), "maxBytes must be from 1KiB to 8MiB"},
		{sampleArgs(map[string]any{"maxBytes": 8<<20 + 1}), "maxBytes must be from 1KiB to 8MiB"},
	} {
		res := mcpCall(t, cs, "mqtt_get_sample", c.args)
		if !res.IsError || mcpText(t, res) != c.want {
			t.Errorf("%v: %+v %q, want %q", c.args, res, mcpText(t, res), c.want)
		}
	}
	if n := p.countAll(); n != 0 {
		t.Errorf("a refused input sent %d request(s)", n)
	}
}

// Every mqtt_get_sample result stays under the result cap without mcpResult's cut.
func TestMCPGetSampleResultsFitTheCap(t *testing.T) {
	for _, c := range []struct {
		name    string
		payload string
		max     int
	}{
		{"inline, just under the spill", strings.Repeat("m", 12000), 50},
		{"spilled, one message", strings.Repeat("m", 20<<10), 50},
		{"spilled, 200 messages", strings.Repeat("m", 600), 200},
		{"200 messages with 256-character topics, inline by payloads", "m", 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			mcpCaptureFast(t)
			topic := "/devices/" + strings.Repeat("d", 63) + "/state/" + strings.Repeat("s", 177)
			if len(topic) != 256 {
				t.Fatalf("the topic is %d characters", len(topic))
			}
			srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
				for i := 0; i < c.max; i++ {
					_, _ = io.WriteString(w, mcpSSEMsg(topic, c.payload))
				}
			}, false)
			cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
			res := mcpCall(t, cs, "mqtt_get_sample", sampleArgs(map[string]any{"max": c.max, "maxBytes": 8 << 20}))
			if res.IsError {
				t.Fatalf("%s", mcpText(t, res))
			}
			text := mcpText(t, res)
			var v any
			err := json.Unmarshal([]byte(text), &v)
			if strings.Contains(text, mcpCutMarker) || err != nil {
				t.Errorf("%d bytes, JSON error %v: the result was cut", len(text), err)
			}
		})
	}
}

// ---------- the capture store ----------

// captureStoreLines is a writeCapture body: n messages of size bytes each.
func captureStoreLines(n, size int) func(w *bufio.Writer) (int, int, error) {
	return func(w *bufio.Writer) (int, int, error) {
		line := strings.Repeat("x", size)
		for i := 0; i < n; i++ {
			if _, err := fmt.Fprintf(w, "{\"i\":%d,\"payload\":%q}\n", i, line); err != nil {
				return 0, 0, err
			}
		}
		return n, n * size, nil
	}
}

func TestCaptureStoreModes(t *testing.T) {
	dir := t.TempDir()
	store := newCaptureStore(filepath.Join(dir, "captures"))
	id, _, err := store.writeCapture(captureStoreLines(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	if !validCaptureID(id) {
		t.Errorf("id %q", id)
	}
	st, err := os.Stat(filepath.Join(dir, "captures"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode %o, want 0700", perm)
	}
	f, err := os.Stat(filepath.Join(dir, "captures", id+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := f.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode %o, want 0600", perm)
	}
}

func TestCaptureStoreEvictsTheOldestPastTheCap(t *testing.T) {
	old := mcpCaptureStoreMax
	mcpCaptureStoreMax = 3000
	t.Cleanup(func() { mcpCaptureStoreMax = old })
	dir := t.TempDir()
	store := newCaptureStore(dir)
	var ids []string
	for i := 0; i < 3; i++ {
		id, _, err := store.writeCapture(captureStoreLines(1, 1000))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond) // created order
	}
	store.mu.Lock()
	n := len(store.caps)
	store.mu.Unlock()
	if n != 3 {
		t.Fatalf("%d captures, want 3: 3 KiB fits the cap", n)
	}
	id, _, err := store.writeCapture(captureStoreLines(1, 1000))
	if err != nil {
		t.Fatal(err)
	}
	ids = append(ids, id)
	if _, ok := store.get(ids[0]); ok {
		t.Error("the oldest capture survived past the cap")
	}
	if _, ok := store.get(ids[3]); !ok {
		t.Error("the new capture was evicted")
	}
	// Its file went too, and the store's files fit the cap.
	if _, err := os.Stat(filepath.Join(dir, ids[0]+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("the evicted capture's file is still there: %v", err)
	}
	store.mu.Lock()
	total := 0
	for _, c := range store.caps {
		total += c.total
	}
	store.mu.Unlock()
	if total > mcpCaptureStoreMax {
		t.Errorf("%d bytes in the store, want at most %d", total, mcpCaptureStoreMax)
	}
}

// The first use removes what an earlier process left: old cap-*.jsonl files, and nothing else.
func TestCaptureStoreSweepsStaleFiles(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, "cap-0123456789ab.jsonl")
	fresh := filepath.Join(dir, "cap-ba9876543210.jsonl")
	other := filepath.Join(dir, "notes.jsonl")
	notAnID := filepath.Join(dir, "cap-nothex.jsonl")
	for _, p := range []string{stale, fresh, other, notAnID} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	long := time.Now().Add(-2 * mcpCaptureTTL)
	for _, p := range []string{stale, other, notAnID} {
		if err := os.Chtimes(p, long, long); err != nil {
			t.Fatal(err)
		}
	}
	store := newCaptureStore(dir)
	if _, _, err := store.writeCapture(captureStoreLines(1, 10)); err != nil {
		t.Fatal(err)
	}
	for p, gone := range map[string]bool{stale: true, fresh: false, other: false, notAnID: false} {
		_, err := os.Stat(p)
		if gone && !os.IsNotExist(err) {
			t.Errorf("%s should have been swept", filepath.Base(p))
		}
		if !gone && err != nil {
			t.Errorf("%s should have stayed: %v", filepath.Base(p), err)
		}
	}
}

// A write that fails removes its partial file and is an error, never a silent success.
func TestCaptureStoreFailedWrite(t *testing.T) {
	dir := t.TempDir()
	store := newCaptureStore(dir)
	boom := errors.New("disk full")
	_, _, err := store.writeCapture(func(w *bufio.Writer) (int, int, error) {
		_, _ = w.WriteString("{\"i\":0}\n")
		return 0, 0, boom
	})
	if err == nil || !strings.Contains(err.Error(), "could not keep the capture: disk full") ||
		!strings.Contains(err.Error(), "use a smaller max or maxBytes") {
		t.Fatalf("%v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("%d files left behind: %v", len(entries), entries)
	}

	// A write error inside lines counts too.
	_, _, err = store.writeCapture(func(w *bufio.Writer) (int, int, error) {
		_, werr := w.WriteString(strings.Repeat("x", 1<<16))
		return 0, 0, werr
	})
	if err != nil {
		t.Fatalf("%v: a buffered write that fits should not fail here", err)
	}
	store.mu.Lock()
	n := len(store.caps)
	store.mu.Unlock()
	if n != 1 {
		t.Errorf("%d captures", n)
	}
}

func TestCaptureStoreGetDropsExpired(t *testing.T) {
	old := mcpCaptureTTL
	t.Cleanup(func() { mcpCaptureTTL = old })
	dir := t.TempDir()
	store := newCaptureStore(dir)
	id, _, err := store.writeCapture(captureStoreLines(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.get(id); !ok {
		t.Fatal("the fresh capture is gone")
	}
	mcpCaptureTTL = time.Millisecond
	id2, _, err := store.writeCapture(captureStoreLines(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, ok := store.get(id2); ok {
		t.Error("an expired capture answered")
	}
	if _, err := os.Stat(filepath.Join(dir, id2+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("the expired capture's file is still there: %v", err)
	}
}

// mcp_sample's new top-level fields: truncated, truncatedMessages, totalBytes and serverDropped.
func TestMCPMqttSampleSaysWhenItCut(t *testing.T) {
	t.Run("uncut", func(t *testing.T) {
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", "hello"))
			_, _ = io.WriteString(w, mcpSSEEvent("dropped", map[string]any{"count": 2, "reason": "size"}))
		}, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
		res := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil))
		if res.IsError {
			t.Fatal(mcpText(t, res))
		}
		text := mcpText(t, res)
		var got struct {
			mcpSampleOut
			Truncated         bool          `json:"truncated"`
			TruncatedMessages int           `json:"truncatedMessages"`
			TotalBytes        int           `json:"totalBytes"`
			ServerDropped     streamDropped `json:"serverDropped"`
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatal(err)
		}
		if got.Truncated || got.TruncatedMessages != 0 || got.TotalBytes != 5 {
			t.Errorf("truncated %v, %d cut, %d bytes", got.Truncated, got.TruncatedMessages, got.TotalBytes)
		}
		if got.ServerDropped != (streamDropped{Size: 2}) || got.Dropped != 2 {
			t.Errorf("serverDropped %+v, dropped %d", got.ServerDropped, got.Dropped)
		}
		if strings.Contains(text, "hint") {
			t.Errorf("an uncut sample carries a hint: %q", text)
		}
	})
	t.Run("cut payload", func(t *testing.T) {
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", strings.Repeat("m", 5000)))
		}, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
		res := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil))
		text := mcpText(t, res)
		var got struct {
			Truncated         bool   `json:"truncated"`
			TruncatedMessages int    `json:"truncatedMessages"`
			TotalBytes        int    `json:"totalBytes"`
			Hint              string `json:"hint"`
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Truncated || got.TruncatedMessages != 1 || got.TotalBytes != 5000 {
			t.Errorf("truncated %v, %d cut, %d bytes", got.Truncated, got.TruncatedMessages, got.TotalBytes)
		}
		if got.Hint != mcpSampleCutHint {
			t.Errorf("hint %q, want %q", got.Hint, mcpSampleCutHint)
		}
	})
	t.Run("size stop", func(t *testing.T) {
		srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
			for i := 0; i < 20; i++ {
				_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/x%d", i), strings.Repeat("m", 4000)))
			}
		}, false)
		cs := mcpClient(t, newMCPServer(mcpSampleEnv(t, srv)))
		res := mcpCall(t, cs, "mqtt_sample", sampleArgs(nil))
		text := mcpText(t, res)
		var got struct {
			Messages   []json.RawMessage `json:"messages"`
			StoppedBy  string            `json:"stoppedBy"`
			Truncated  bool              `json:"truncated"`
			TotalBytes int               `json:"totalBytes"`
			Hint       string            `json:"hint"`
		}
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatal(err)
		}
		if got.StoppedBy != "size" || !got.Truncated || got.Hint != mcpSampleCutHint {
			t.Errorf("stopped by %q, truncated %v, hint %q", got.StoppedBy, got.Truncated, got.Hint)
		}
		if want := len(got.Messages) * 4000; got.TotalBytes != want {
			t.Errorf("totalBytes %d, want the %d kept messages' %d", got.TotalBytes, len(got.Messages), want)
		}
	})
}
