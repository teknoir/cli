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

// The brief's worst preview case: 200 messages of 512 bytes and a real 24-character timestamp drop
// previews from the last message backwards until the index fits, and the hint says what the index
// could not show.
func TestMCPGetSampleOmitsPreviews(t *testing.T) {
	mcpCaptureFast(t)
	store := newCaptureStore(t.TempDir())
	c := &mcpCapturedSample{}
	for i := 0; i < 200; i++ {
		line, data := sampleMsgOf(i, &api.StreamMessage{
			Topic: fmt.Sprintf("/devices/d1/state/x%d", i), Payload: strings.Repeat("p", 512),
			TS: "2026-11-08T10:00:00.000Z"})
		if len(line.TS) != 24 {
			t.Fatalf("the timestamp is %d characters", len(line.TS))
		}
		c.messages = append(c.messages, mcpCapturedMessage{line: line, data: data, kind: "text"})
	}
	res, err := c.spill(store)
	if err != nil {
		t.Fatal(err)
	}
	if res.PreviewsOmitted == 0 {
		t.Errorf("%d previews omitted: the index did not fit", res.PreviewsOmitted)
	}
	// The previews paid for the room first: every message still listed shows none.
	for i, m := range res.Messages {
		if m.Preview != "" {
			t.Fatalf("message %d still shows a preview while %d were dropped", i, res.PreviewsOmitted)
		}
	}
	if b, _ := mcpMarshal(res); len(b) > mcpIndexMax {
		t.Errorf("the index is %d bytes", len(b))
	}
	if !mcpIndexFits(res) {
		t.Errorf("the index does not survive mcpResult whole: %d listed of %d", len(res.Messages), res.Count)
	}
	// The hint says what was dropped, both ways round: previews, and messages the list leaves out.
	if !strings.Contains(res.Hint, "Previews were dropped to fit; use mqtt_read with path or a short length.") {
		t.Errorf("the hint does not name the dropped previews: %q", res.Hint)
	}
	want := fmt.Sprintf("The index lists messages 0 to %d of %d", len(res.Messages)-1, res.Count)
	if res.Omitted > 0 {
		want += fmt.Sprintf("; the other %d (i %d to %d) are whole in the capture too: read them with mqtt_read by i.",
			res.Omitted, len(res.Messages), res.Count-1)
		if len(res.Messages)+res.Omitted != res.Count {
			t.Errorf("%d listed and %d omitted, want %d together", len(res.Messages), res.Omitted, res.Count)
		}
	}
	if !strings.Contains(res.Hint, want) {
		t.Errorf("the hint %q does not say %q", res.Hint, want)
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

	// Base64 reads in groups of 4 characters: an offset between two groups is an error that says
	// so and names nextOffset, never a page that decodes to garbage.
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0, "offset": 2})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, "multiple of 4") || !strings.Contains(text, "nextOffset") {
		t.Errorf("offset 2: %+v %q", res.IsError, text)
	}
	// A length that is not a multiple of 4 reads whole groups: 10 gives 8 characters, and 1 gives one
	// group of 4.
	if p := mcpReadOf(t, mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0, "length": 10})); p.Length != 8 || p.NextOffset == nil || *p.NextOffset != 8 {
		t.Errorf("length 10: %d characters, next %v; want 8", p.Length, p.NextOffset)
	}
	if p := mcpReadOf(t, mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0, "length": 1})); p.Length != 4 {
		t.Errorf("length 1: %d characters, want 4", p.Length)
	}
	// Every page at length 10 joins back into the same bytes.
	var joined strings.Builder
	for offset := 0; ; {
		p := mcpReadOf(t, mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0, "offset": offset, "length": 10}))
		joined.WriteString(p.Data)
		if p.NextOffset == nil {
			break
		}
		offset = *p.NextOffset
	}
	all, err := base64.StdEncoding.DecodeString(joined.String())
	if err != nil || !bytes.Equal(all, raw) {
		t.Errorf("the 10-character pages decode to %d bytes (%v), want %d", len(all), err, len(raw))
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
	// The index the agent sees, with mcpResult's indentation, still fits the result cap whole.
	mcpAssertWhole(t, mcpText(t, mcpResult(marshalRaw(res))), "captureId", "messages", "hint")
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
			mcpAssertWhole(t, text, "count", "totalBytes")
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

// The store counts the files it writes, not the payload bytes they carry: a base64 payload is a
// third larger than its bytes, and every line has its index fields either side of it. Two captures
// whose decoded payloads fit the cap still pass it on disk, and the oldest goes.
func TestCaptureStoreEvictsTheOldestPastTheCap(t *testing.T) {
	old := mcpCaptureStoreMax
	mcpCaptureStoreMax = 3000
	t.Cleanup(func() { mcpCaptureStoreMax = old })
	dir := t.TempDir()
	store := newCaptureStore(dir)
	// 1600 base64 characters are 1200 decoded bytes: two of them are 2400 bytes of payload, under
	// the cap, and about 2 × 1622 bytes of file, over it.
	base64Lines := func(w *bufio.Writer) (int, int, error) {
		text := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42, 0x00, 0xff}, 533))[:1599] + "A"
		if _, err := fmt.Fprintf(w, "{\"i\":0,\"encoding\":\"base64\",\"payload\":%q}\n", text); err != nil {
			return 0, 0, err
		}
		return 1, len(text) / 4 * 3, nil
	}
	var ids []string
	for i := 0; i < 2; i++ {
		id, _, err := store.writeCapture(base64Lines)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond) // created order
	}
	store.mu.Lock()
	n, files := len(store.caps), 0
	var live *mcpCapture
	for _, c := range store.caps {
		files += c.fileBytes
		live = c
	}
	store.mu.Unlock()
	// The two payloads fit the cap on their own; their files cannot. One capture, alone, does.
	if n != 1 {
		t.Fatalf("%d captures, want 1: the files passed the cap, not the payloads", n)
	}
	if got := live.total * 2; got > mcpCaptureStoreMax {
		t.Fatalf("two payloads are %d bytes, over the %d cap: the fixture has to pass it on its files", got, mcpCaptureStoreMax)
	}
	if got := live.fileBytes * 2; got <= mcpCaptureStoreMax {
		t.Fatalf("two files are %d bytes, under the %d cap: nothing to evict", got, mcpCaptureStoreMax)
	}
	if files > mcpCaptureStoreMax {
		t.Errorf("%d file bytes left, over the %d cap", files, mcpCaptureStoreMax)
	}
	if _, ok := store.get(ids[0]); ok {
		t.Error("the oldest capture survived past the cap")
	}
	if _, ok := store.get(ids[1]); !ok {
		t.Error("the new capture was evicted")
	}
	// Its file went too, and what is left fits the cap.
	if _, err := os.Stat(filepath.Join(dir, ids[0]+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("the evicted capture's file is still there: %v", err)
	}
	store.mu.Lock()
	total := 0
	for _, c := range store.caps {
		total += c.fileBytes
	}
	store.mu.Unlock()
	if total > mcpCaptureStoreMax {
		t.Errorf("%d file bytes in the store, want at most %d", total, mcpCaptureStoreMax)
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

	// The flush at the end fails too: the file is closed under the writer, and what lines buffered
	// is written nowhere. The capture is an error, and no file stays behind.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldOpen := mcpCaptureOpenFile
	mcpCaptureOpenFile = func(name string) (*os.File, error) {
		f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		_ = f.Close() // the writer holds a file whose descriptor is already shut
		return f, nil
	}
	t.Cleanup(func() { mcpCaptureOpenFile = oldOpen })
	_, _, err = store.writeCapture(func(w *bufio.Writer) (int, int, error) {
		// Small enough that the write itself buffers: the failure is the flush.
		if _, werr := w.WriteString("{\"i\":0}\n"); werr != nil {
			return 0, 0, werr
		}
		return 1, 8, nil
	})
	if err == nil || !strings.Contains(err.Error(), "could not keep the capture") {
		t.Fatalf("%v, want the failed flush", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("%d files left behind by the failed flush: %v", len(entries), entries)
	}
	mcpCaptureOpenFile = oldOpen

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

// ---------- the capture directory ----------

// A capture directory the world can read is set back to 0700 before anything is written in it.
func TestCaptureStorePrivateDirMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "captures")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { // MkdirAll is under the umask
		t.Fatal(err)
	}
	store := newCaptureStore(dir)
	if _, _, err := store.writeCapture(captureStoreLines(1, 10)); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode %o, want 0700", perm)
	}
}

// A capture directory that is a symlink is refused: the target is not tnctl's to write captures
// into, whoever put the link there.
func TestCaptureStoreRefusesSymlinkDir(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "captures")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	store := newCaptureStore(link)
	_, _, err := store.writeCapture(captureStoreLines(1, 10))
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("%v, want a refusal of the link", err)
	}
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		t.Errorf("%d files were written through the link: %v", len(entries), entries)
	}
}

// A capture directory that holds a file instead of being one is refused too.
func TestCaptureStoreRefusesNonDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "captures")
	if err := os.WriteFile(path, []byte("not a dir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := newCaptureStore(path)
	if _, _, err := store.writeCapture(captureStoreLines(1, 10)); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("%v, want a refusal", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "not a dir\n" {
		t.Errorf("the file changed: %q", b)
	}
}

// A dir that cannot be had is the dir's error, not the sample's: no "use a smaller max", which
// would send the caller after the wrong thing.
func TestCaptureStoreDirErrorHasNoSizeSuffix(t *testing.T) {
	store := newCaptureStore(filepath.Join(t.TempDir(), "captures"))
	if err := os.MkdirAll(filepath.Join(store.dir, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A file where the directory should be is refused before any write.
	if err := os.RemoveAll(store.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := store.writeCapture(captureStoreLines(1, 10))
	if err == nil || strings.Contains(err.Error(), "use a smaller max") {
		t.Fatalf("%v, want the dir's own error", err)
	}
}

// ---------- the store's life ----------

// removeAll deletes every capture with its file: the session that owned them has ended.
func TestCaptureStoreRemoveAll(t *testing.T) {
	dir := t.TempDir()
	store := newCaptureStore(dir)
	var ids []string
	for i := 0; i < 3; i++ {
		id, _, err := store.writeCapture(captureStoreLines(1, 10))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	store.removeAll()
	for _, id := range ids {
		if _, ok := store.get(id); ok {
			t.Errorf("capture %s survived removeAll", id)
		}
		if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); !os.IsNotExist(err) {
			t.Errorf("capture %s's file is still there: %v", id, err)
		}
	}
}

// forget drops one capture: a file that went from under it will not be read again.
func TestCaptureStoreForget(t *testing.T) {
	dir := t.TempDir()
	store := newCaptureStore(dir)
	id, _, err := store.writeCapture(captureStoreLines(1, 10))
	if err != nil {
		t.Fatal(err)
	}
	store.forget(id)
	if _, ok := store.get(id); ok {
		t.Error("the forgotten capture answered")
	}
	if _, err := os.Stat(filepath.Join(dir, id+".jsonl")); !os.IsNotExist(err) {
		t.Errorf("the forgotten capture's file is still there: %v", err)
	}
}

// A capture whose file went away from outside answers as an unknown one, and is forgotten: the next
// call does not read the file again.
func TestMCPReadCaptureFileRemoved(t *testing.T) {
	mcpCaptureFast(t)
	cs, id := mcpSpilledJSON(t, strings.Repeat(`{"a":"`+strings.Repeat("v", 200)+`"}`, 100))
	matches, err := filepath.Glob(filepath.Join(mcpStdioCaptureDir(), id+".jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("the capture file: %v %v", matches, err)
	}
	if err := os.Remove(matches[0]); err != nil {
		t.Fatal(err)
	}
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, "unknown or expired") {
		t.Errorf("%+v %q, want the unknown-or-expired error", res.IsError, text)
	}
	// And the same answer the second time: the capture is gone from the store, not only its file.
	if res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0}); !res.IsError || !strings.Contains(mcpText(t, res), "unknown or expired") {
		t.Errorf("the second read: %+v %q", res.IsError, mcpText(t, res))
	}
}

// The server deletes its captures when the session ends.
func TestMCPEndsItsCaptures(t *testing.T) {
	dir := t.TempDir()
	old := mcpStdioCaptureDir
	mcpStdioCaptureDir = func() string { return dir }
	t.Cleanup(func() { mcpStdioCaptureDir = old })
	noBotMode(t)
	srv, _ := mcpSSE(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEMsg("/devices/d1/state/x", strings.Repeat("m", 20<<10)))
	}, false)
	e := mcpSampleEnv(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runMCPWith(ctx, e, st, false) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil))
	got, text := mcpGetSampleOf(t, res)
	if got.CaptureID == "" {
		t.Fatalf("not spilled: %q", text)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(matches) != 1 {
		t.Fatalf("the capture's files: %v", matches)
	}
	cancel()
	_ = cs.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")); len(matches) != 0 {
		t.Errorf("the session left %d captures behind: %v", len(matches), matches)
	}
}

// ---------- escaping and page bounds ----------

// mcpSpillEnv is a server whose stream writes payload as the platform does: raw in the SSE data.
func mcpSpillEnv(t *testing.T, events func(w http.ResponseWriter)) *mcp.ClientSession {
	t.Helper()
	mcpCaptureFast(t)
	cs := mcpGetSampleEnv(t, events)
	return cs
}

// A sample of payloads that JSON escapes is measured as it is sent: mcpMarshal keeps "<", ">" and
// "&" one byte each, so a result that fits when it is checked is not cut when it goes out.
func TestMCPGetSampleEscapesAreCountedAsSent(t *testing.T) {
	cs := mcpSpillEnv(t, func(w http.ResponseWriter) {
		for i := 0; i < 100; i++ {
			_, _ = io.WriteString(w, mcpSSEMsg(fmt.Sprintf("/devices/d1/state/x%d", i), "a&b <c>"+strings.Repeat("m", 500)))
		}
	})
	res := mcpCall(t, cs, "mqtt_get_sample", sampleArgs(map[string]any{"max": 100}))
	text := mcpText(t, res)
	var got mcpGetSampleOut
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("the result is not JSON: %v in %.200q", err, text)
	}
	if res.IsError {
		t.Fatalf("%s", text)
	}
	mcpAssertWhole(t, text, "count", "totalBytes")
	// The payload itself comes back as the stream sent it, one "<" a byte, and reads whole too.
	payload := "a&b <c>" + strings.Repeat("m", 500)
	page := mcpReadOf(t, mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0}))
	if page.Data != payload {
		t.Errorf("the payload reads back as %q, want %q", page.Data, payload)
	}

	// The worst of it: a sample of about 12000 "&" fits the inline limit when it is measured the way
	// it is sent, and would be 72000 bytes — a cut result that does not parse — escaped the other way.
	cs = mcpSpillEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{
			"topic": "/devices/d1/state/x", "payload": strings.Repeat("&", 12000)}))
	})
	res = mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil))
	text = mcpText(t, res)
	var inline struct {
		CaptureID string            `json:"captureId"`
		Messages  []json.RawMessage `json:"messages"`
		Truncated bool              `json:"truncated"`
		Count     int               `json:"count"`
	}
	if err := json.Unmarshal([]byte(text), &inline); err != nil || res.IsError {
		t.Fatalf("the result is not JSON (%v): %.200q", err, text)
	}
	if inline.CaptureID != "" || len(inline.Messages) != 1 || inline.Count != 1 {
		t.Errorf("a 12000-byte sample did not come back inline: %.200q", text)
	}
	mcpAssertWhole(t, text, "messages", "count")
}

// A text page is bounded by what it takes once escaped, so control characters and U+2028 cannot grow
// a page past the cap; the pages still join back into the payload byte for byte.
func TestMCPReadPagesBoundEscapedSize(t *testing.T) {
	for _, c := range []struct{ name, unit string }{
		{"control characters", "\x01"},
		{"HTML characters", "<&>"},
	} {
		t.Run(c.name, func(t *testing.T) {
			payload := strings.Repeat(c.unit, 30000/len(c.unit))
			cs := mcpSpillEnv(t, func(w http.ResponseWriter) {
				_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{
					"topic": "/devices/d1/state/x", "payload": payload}))
			})
			got, _ := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
			if got.CaptureID == "" {
				t.Fatalf("a %d-byte payload was not spilled", len(payload))
			}
			var joined strings.Builder
			offset := 0
			for pages := 0; ; pages++ {
				if pages > 50 {
					t.Fatal("the pages do not end")
				}
				res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0, "offset": offset})
				if res.IsError {
					t.Fatalf("page at %d: %s", offset, mcpText(t, res))
				}
				text := mcpText(t, res)
				var page mcpReadPage
				if err := json.Unmarshal([]byte(text), &page); err != nil {
					t.Fatalf("page at %d is not JSON: %v", offset, err)
				}
				mcpAssertWhole(t, text, "data", "nextOffset")
				if page.Data == "" && page.NextOffset != nil {
					t.Fatalf("page at %d is empty and has a nextOffset %d", offset, *page.NextOffset)
				}
				joined.WriteString(page.Data)
				if page.NextOffset == nil {
					break
				}
				offset = *page.NextOffset
			}
			if sum := sha256Hex([]byte(joined.String())); sum != sha256Hex([]byte(payload)) {
				t.Errorf("the pages do not reassemble to the payload: %d of %d bytes", joined.Len(), len(payload))
			}
		})
	}
}

// A byte that is not UTF-8 escapes to six characters, so a 12288-byte page of them would be 72 KiB:
// the page is smaller, and the result of it whole. (It cannot come through the stream — JSON turns
// each such byte into the replacement character before tnctl sees it — so it is paged directly.)
func TestMCPReadPageBoundsInvalidUTF8(t *testing.T) {
	m := sampleMsg{Topic: "/devices/d1/state/x", Encoding: "utf8", Payload: strings.Repeat("\xff", 30000)}
	offset, pages := 0, 0
	for {
		if pages++; pages > 50 {
			t.Fatal("the pages do not end")
		}
		p, err := mcpPage(m, mcpReadIn{Offset: offset})
		if err != nil {
			t.Fatalf("page at %d: %v", offset, err)
		}
		if p.Length == 0 || p.Length > 36<<10/6 {
			t.Fatalf("page at %d is %d bytes: six escaped characters each pass the 36 KiB budget", offset, p.Length)
		}
		raw, err := mcpMarshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := mcpText(t, mcpResult(raw)); got != string(raw) && !strings.HasPrefix(got, "{") {
			t.Fatalf("page at %d does not survive mcpResult: %.200q", offset, got)
		}
		if len(mcpIndented(t, string(raw))) > mcpMaxResult {
			t.Fatalf("page at %d is over the %d cap once indented", offset, mcpMaxResult)
		}
		if p.NextOffset == nil {
			if end := offset + p.Length; end != len(m.Payload) {
				t.Errorf("the last page ends at %d, want %d", end, len(m.Payload))
			}
			break
		}
		offset = *p.NextOffset
	}
}

// A page always holds at least the rune at its offset: a length under the rune's size is that rune,
// not an empty page whose nextOffset is its own offset.
func TestMCPReadNeverReturnsAnEmptyPage(t *testing.T) {
	payload := "€abc"
	mcpCaptureFast(t)
	store := newCaptureStore(t.TempDir())
	line, data := sampleMsgOf(0, &api.StreamMessage{Topic: "/devices/d1/state/x", Payload: payload})
	if _, _, err := store.writeCapture(func(w *bufio.Writer) (int, int, error) {
		b, err := mcpMarshal(line)
		if err != nil {
			return 0, 0, err
		}
		_, err = w.Write(append(b, '\n'))
		return 1, len(data), err
	}); err != nil {
		t.Fatal(err)
	}
	page, err := mcpPage(line, mcpReadIn{I: 0, Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Data != "€" || page.Length != 3 || page.NextOffset == nil || *page.NextOffset != 3 {
		t.Errorf("page = %q, length %d, next %v; want the euro sign and offset 3", page.Data, page.Length, page.NextOffset)
	}
}

// A topic over 1024 bytes is cut back to a rune start in both tools, with its whole length given,
// and neither result is cut: a topic is a device's own string, and 50 KiB of one is not a result.
func TestMCPLongTopicIsBounded(t *testing.T) {
	topic := "/devices/d1/state/" + strings.Repeat("é", 25<<10)
	cs := mcpSpillEnv(t, func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, mcpSSEEvent("message", map[string]any{"topic": topic, "payload": strings.Repeat("m", 20<<10)}))
	})
	got, text := mcpGetSampleOf(t, mcpCall(t, cs, "mqtt_get_sample", sampleArgs(nil)))
	mcpAssertWhole(t, text, "captureId", "messages")
	var m mcpGetSampleMessage
	if err := json.Unmarshal(got.Messages[0], &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Topic) > 1024 || !utf8.ValidString(m.Topic) || m.TopicBytes != len(topic) {
		t.Errorf("the index's topic is %d bytes (valid %v), topicBytes %d; the whole is %d",
			len(m.Topic), utf8.ValidString(m.Topic), m.TopicBytes, len(topic))
	}
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": got.CaptureID, "i": 0})
	pageText := mcpText(t, res)
	mcpAssertWhole(t, pageText, "topic", "data")
	var page mcpReadPage
	if err := json.Unmarshal([]byte(pageText), &page); err != nil {
		t.Fatal(err)
	}
	var withBytes struct {
		Topic      string `json:"topic"`
		TopicBytes int    `json:"topicBytes"`
	}
	_ = json.Unmarshal([]byte(pageText), &withBytes)
	if len(withBytes.Topic) > 1024 || !utf8.ValidString(withBytes.Topic) || withBytes.TopicBytes != len(topic) {
		t.Errorf("the page's topic is %d bytes, topicBytes %d; the whole is %d", len(withBytes.Topic), withBytes.TopicBytes, len(topic))
	}
}

// ---------- paths ----------

// A key under a list index is named with its dot, so a refusal names a path the caller can fix.
func TestMCPPathWhereNamesDottedKeysAfterAnIndex(t *testing.T) {
	payload := string(jsonOfBytes(t, map[string]any{
		"a":    []any{map[string]any{"b": 1}},
		"note": strings.Repeat("x", 20<<10),
	}))
	cs, id := mcpSpilledJSON(t, payload)
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": "a[0].nope"})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, "a[0].nope") || !strings.Contains(text, `"nope" at a[0]`) {
		t.Errorf("%+v %q, want the dot after the index", res.IsError, text)
	}
}

// The "cannot be addressed" hint is for a key the path names and ends at, not for every key that
// starts with the path.
func TestMCPPathUnaddressableKeyOnlyWhenItIsMeant(t *testing.T) {
	payload := string(jsonOfBytes(t, map[string]any{
		"temperature.c": map[string]any{"v": 1},
		"a.b":           float64(2),
		"note":          strings.Repeat("x", 20<<10),
	}))
	cs, id := mcpSpilledJSON(t, payload)
	// A key that merely starts with the path's text: it is missing, and the keys are listed.
	res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": "temp"})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, `there is no key "temp"`) {
		t.Errorf("%+v %q, want a missing key", res.IsError, text)
	}
	// A key the path names whole: it cannot be addressed, and the error says why.
	res = mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": "a.b"})
	if text := mcpText(t, res); !res.IsError || !strings.Contains(text, `the key "a.b"`) || !strings.Contains(text, "cannot be addressed") {
		t.Errorf("%+v %q, want the unaddressable key", res.IsError, text)
	}
}

// A list index that is not a whole number from 0 is a mistake to correct, not an unaddressable key.
func TestMCPPathBadListIndex(t *testing.T) {
	payload := string(jsonOfBytes(t, map[string]any{"a": []any{1, 2}, "note": strings.Repeat("x", 20<<10)}))
	cs, id := mcpSpilledJSON(t, payload)
	for _, path := range []string{"a[-1]", "a[]", "a[x]", "a[99999999999999999999]"} {
		res := mcpCall(t, cs, "mqtt_read", map[string]any{"captureId": id, "i": 0, "path": path})
		text := mcpText(t, res)
		if !res.IsError || !strings.Contains(text, "is not a list index") || !strings.Contains(text, "use a whole number from 0, like items[0]") {
			t.Errorf("%s: %+v %q", path, res.IsError, text)
		}
	}
}

// A base64 payload the standard decoder refuses is tried unpadded, and one neither reads is
// counted by its text and carries no hash: never a zero-filled buffer described as its bytes.
func TestMqttSampleUnpaddedBase64(t *testing.T) {
	line, data := sampleMsgOf(0, &api.StreamMessage{Topic: "/devices/d1/state/x", PayloadBase64: "AAE"})
	if line.Encoding != "base64" || line.Bytes != 2 || !bytes.Equal(data, []byte{0x00, 0x01}) {
		t.Errorf("line %+v, data %x; want two bytes and 0001", line, data)
	}
	if got := sha256Hex(data); got != sha256Hex([]byte{0x00, 0x01}) {
		t.Errorf("the hash is %s", got)
	}
	// Neither decoder reads it: no bytes, the text's length, and no hash.
	line, data = sampleMsgOf(0, &api.StreamMessage{Topic: "/devices/d1/state/x", PayloadBase64: "!!!"})
	if data != nil || line.Bytes != 3 || line.SHA256 != "" {
		t.Errorf("line %+v, data %x; want no bytes and 3 characters", line, data)
	}
}
