package cmd

// mqtt_get_sample and mqtt_read: a sample that returns every payload whole. A small one comes
// inline; a larger one is written to the server's capture store and read back with mqtt_read, a
// page (or one field, by path) at a time. mqtt_sample stays the cheap preview.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teknoir/cli/pkg/api"
)

// The limits of mqtt_get_sample and mqtt_read.
const (
	// mcpGetSampleMaxMessages, mcpGetSampleMaxSeconds, mcpGetSampleMaxBytes and
	// mcpGetSampleMinBytes bound what one sample takes.
	mcpGetSampleMaxMessages = 200
	mcpGetSampleMaxSeconds  = 30
	mcpGetSampleMaxBytes    = 8 << 20
	mcpGetSampleMinBytes    = 1 << 10

	// mcpPageMax is the longest page mqtt_read returns. A 256 KiB payload is about 22 pages.
	mcpPageMax = 12288

	// mcpInlineMax is how many payload bytes one result carries itself; a larger sample is spilled
	// to the capture store instead.
	mcpInlineMax = 12288

	// mcpIndexMax caps a spilled sample's answer. 200 messages with 8 maximum-length topics and a
	// 512-byte payload each fit below it; when the previews would pass it, they are dropped from the
	// last message backwards.
	mcpIndexMax = 40960

	// mcpPreviewMax and mcpPreviewBudget bound the previews of one index.
	mcpPreviewMax    = 512
	mcpPreviewBudget = 24576
)

// mcpGetSampleDescription is what the tool tells the agent.
const mcpGetSampleDescription = "Listen to the MQTT messages of the teamspace's devices for a few seconds and return them whole: each payload as the stream sent it, unlike mqtt_sample, which cuts a long one. A sample whose payloads add up to at most 12288 bytes comes back inline; a larger one is written to a capture that lasts 30 minutes and the result is an index (kind, sha256, a preview per message), to read with mqtt_read by index, offset and length, or one JSON field with path. payloads over 256 KiB are dropped by the platform and counted in serverDropped.size. mqtt_sample is the cheaper preview when whole payloads are not needed. It stops at max messages, after seconds, when the stream ends, or when the next message would pass maxBytes; stoppedBy says which. One sample runs at a time: put every topic (up to 8) in one call instead of calling it in parallel. " + mcpUntrustedNote

// mcpReadDescription is what mqtt_read tells the agent.
const mcpReadDescription = "Read one message of a capture mqtt_get_sample spilled: the payload's text, or its base64 text, from offset for at most length bytes (12288 at most, about 22 reads of a 256 KiB payload), with nextOffset for the next page; or, with path, one field of a JSON payload such as detections[0].label, marshalled compactly and paged the same way. A capture lasts 30 minutes; after that, take a new sample. " + mcpUntrustedNote

// mcpGetSampleHint is what a spilled sample tells the agent to do next.
const mcpGetSampleHint = "Each payload is whole in the capture. Read one with mqtt_read (captureId, i, offset, length up to 12288), or one field of a JSON payload with path, for example path=detections[0].label. The capture lasts 30 minutes."

// mcpGetSampleIn is mqtt_get_sample's input.
type mcpGetSampleIn struct {
	Namespace string   `json:"namespace,omitempty" jsonschema:"the teamspace; empty means the configured one"`
	Topics    []string `json:"topics" jsonschema:"1 to 8 MQTT topics: /devices/<device|+>/state/# or /devices/<device|+>/events/#, or a concrete topic below .../state/ or .../events/; for example /devices/+/state/#"`
	Seconds   int      `json:"seconds,omitempty" jsonschema:"how long to listen; default 5, max 30"`
	Max       int      `json:"max,omitempty" jsonschema:"stop after this many messages; default 50, max 200"`
	MaxBytes  int      `json:"maxBytes,omitempty" jsonschema:"stop before the payloads pass this many bytes; default 2 MiB, max 8 MiB"`
}

// mcpReadIn is mqtt_read's input.
type mcpReadIn struct {
	CaptureID string `json:"captureId" jsonschema:"the captureId of a spilled mqtt_get_sample"`
	I         int    `json:"i" jsonschema:"the message's index, from 0"`
	Offset    int    `json:"offset,omitempty" jsonschema:"start this many bytes into the text (default 0)"`
	Length    int    `json:"length,omitempty" jsonschema:"at most this many bytes; default and max 12288"`
	Path      string `json:"path,omitempty" jsonschema:"a field of a JSON payload, like detections[0].label; without it the whole payload's text"`
}

// mcpGetSampleMessage is one message of a spilled sample's index.
type mcpGetSampleMessage struct {
	I         int    `json:"i"`
	Topic     string `json:"topic"`
	TS        string `json:"ts"`
	Retain    bool   `json:"retain"`
	Bytes     int    `json:"bytes"`
	Encoding  string `json:"encoding"`
	SHA256    string `json:"sha256"`
	Kind      string `json:"kind"`
	JSONValid bool   `json:"jsonValid"`
	Preview   string `json:"preview,omitempty"`
}

// mcpGetSampleIndex is a spilled sample's result: where the capture is and what it holds.
type mcpGetSampleIndex struct {
	CaptureID        string                `json:"captureId"`
	File             string                `json:"file"`
	ExpiresAt        string                `json:"expiresAt"`
	Count            int                   `json:"count"`
	TotalBytes       int                   `json:"totalBytes"`
	StoppedBy        string                `json:"stoppedBy"`
	NextMessageBytes int                   `json:"nextMessageBytes,omitempty"`
	ServerDropped    streamDropped         `json:"serverDropped"`
	Truncated        bool                  `json:"truncated"`
	Messages         []mcpGetSampleMessage `json:"messages"`
	PreviewsOmitted  int                   `json:"previewsOmitted,omitempty"`
	Omitted          int                   `json:"omitted,omitempty"`
	Hint             string                `json:"hint"`
}

// mcpInlineMessage is one message of an inline sample: the payload itself, whole.
type mcpInlineMessage struct {
	I        int    `json:"i"`
	Topic    string `json:"topic"`
	TS       string `json:"ts"`
	Retain   bool   `json:"retain"`
	Bytes    int    `json:"bytes"`
	Encoding string `json:"encoding"`
	Payload  string `json:"payload"`
}

// mcpInlineResult is a small sample's result: every payload, whole.
type mcpInlineResult struct {
	Messages         []mcpInlineMessage `json:"messages"`
	Count            int                `json:"count"`
	TotalBytes       int                `json:"totalBytes"`
	StoppedBy        string             `json:"stoppedBy"`
	NextMessageBytes int                `json:"nextMessageBytes,omitempty"`
	ServerDropped    streamDropped      `json:"serverDropped"`
	Truncated        bool               `json:"truncated"`
}

// mcpGetSampleTool is the mqtt_get_sample handler. It shares the server's sample slot, and therefore
// its one open stream, with mqtt_sample.
func mcpGetSampleTool(e *cliEnv, slot *mcpSampleSlot, captures *mcpCaptureStore) func(context.Context, *mcp.CallToolRequest, mcpGetSampleIn) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in mcpGetSampleIn) (*mcp.CallToolResult, any, error) {
		if err := api.ValidateStreamTopics(in.Topics); err != nil {
			return nil, nil, err
		}
		seconds := in.Seconds
		if seconds == 0 {
			seconds = 5
		}
		if seconds < 0 || seconds > mcpGetSampleMaxSeconds {
			return nil, nil, usageErr("seconds must be from 1 to %d", mcpGetSampleMaxSeconds)
		}
		max := in.Max
		if max == 0 {
			max = 50
		}
		if max < 0 || max > mcpGetSampleMaxMessages {
			return nil, nil, usageErr("max must be from 1 to %d", mcpGetSampleMaxMessages)
		}
		maxBytes := in.MaxBytes
		if maxBytes == 0 {
			maxBytes = 2 << 20
		}
		if maxBytes < mcpGetSampleMinBytes || maxBytes > mcpGetSampleMaxBytes {
			return nil, nil, usageErr("maxBytes must be from 1KiB to 8MiB")
		}
		release, running, ok := slot.acquire("mqtt_get_sample")
		if !ok {
			return nil, nil, usageErr("another %s is running; put all topics (up to 8) in one call, or try again when it ends", running)
		}
		defer release()
		fl, err := mcpFleet(e, in.Namespace)
		if err != nil {
			return nil, nil, err
		}
		c, err := mcpCollectSample(ctx, fl, mcpSampleRequest{
			topics: in.Topics, seconds: seconds, max: max, maxBytes: maxBytes,
		})
		if err != nil {
			return nil, nil, err
		}
		if res := c.inline(); c.encoded() <= mcpInlineMax && mcpFitsResult(res) {
			return mcpResult(marshalRaw(res)), nil, nil
		}
		res, err := c.spill(captures)
		if err != nil {
			return nil, nil, err
		}
		return mcpResult(marshalRaw(res)), nil, nil
	}
}

// mcpSampleRequest is what a whole-message sample was asked for.
type mcpSampleRequest struct {
	topics       []string
	seconds, max int
	maxBytes     int
}

// mcpCapturedMessage is one message mqtt_get_sample kept, whole. line.Payload is what a page reads;
// data is the payload's decoded bytes, which its size, its hash and its kind are taken from.
type mcpCapturedMessage struct {
	line   sampleMsg
	data   []byte
	kind   string
	jsonOK bool
}

// mcpCapturedSample is what mqtt_get_sample saw: the messages and why it stopped.
type mcpCapturedSample struct {
	messages         []mcpCapturedMessage
	stoppedBy        string
	nextMessageBytes int
	dropped          streamDropped
}

// mcpCollectSample is one whole-message sample: it keeps every message as it arrives, up to max
// messages or maxBytes of payloads (the message that would pass the limit is not kept), and reports
// what the platform dropped. Memory is bounded by maxBytes plus the one message past it.
func mcpCollectSample(ctx context.Context, fl *api.Fleet, req mcpSampleRequest) (*mcpCapturedSample, error) {
	c := &mcpCapturedSample{messages: []mcpCapturedMessage{}}
	payloadBytes := 0
	out, err := mqttCollect(ctx, fl, mqttCollectOpts{
		topics: req.topics, seconds: req.seconds, unit: mcpSampleUnit, openMax: mcpSampleOpenMax, openCode: codeError,
	}, func(m *api.StreamMessage, _ string) (string, int, error) {
		line, data := sampleMsgOf(len(c.messages), m)
		sum := sha256.Sum256(data)
		line.SHA256 = hex.EncodeToString(sum[:])
		if payloadBytes+line.Bytes > req.maxBytes {
			return "size", line.Bytes, nil
		}
		c.messages = append(c.messages, mcpCapturedMessage{
			line: line, data: data, kind: mcpPayloadKind(line.Encoding, data), jsonOK: mcpJSONPayload(line.Encoding, data),
		})
		payloadBytes += line.Bytes
		if len(c.messages) >= req.max {
			return "max", 0, nil
		}
		return "", 0, nil
	})
	if err != nil {
		return nil, err
	}
	c.stoppedBy, c.nextMessageBytes, c.dropped = out.stoppedBy, out.nextMessageBytes, out.dropped
	return c, nil
}

// encoded is the payload bytes as text: what the inline result and a capture hold.
func (c *mcpCapturedSample) encoded() int {
	n := 0
	for _, m := range c.messages {
		n += len(m.line.Payload)
	}
	return n
}

// totalBytes is the payloads' byte count, as the results report it.
func (c *mcpCapturedSample) totalBytes() int {
	n := 0
	for _, m := range c.messages {
		n += m.line.Bytes
	}
	return n
}

// inline is the small sample's result: every payload, whole.
func (c *mcpCapturedSample) inline() mcpInlineResult {
	res := mcpInlineResult{
		Messages: make([]mcpInlineMessage, 0, len(c.messages)), Count: len(c.messages),
		TotalBytes: c.totalBytes(), StoppedBy: c.stoppedBy, NextMessageBytes: c.nextMessageBytes,
		ServerDropped: c.dropped,
	}
	for _, m := range c.messages {
		res.Messages = append(res.Messages, mcpInlineMessage{
			I: m.line.I, Topic: m.line.Topic, TS: m.line.TS, Retain: m.line.Retain,
			Bytes: m.line.Bytes, Encoding: m.line.Encoding, Payload: m.line.Payload,
		})
	}
	return res
}

// spill writes the sample to the capture store and returns its index. The previews are dropped from
// the last message backwards until the index fits mcpIndexMax.
func (c *mcpCapturedSample) spill(captures *mcpCaptureStore) (*mcpGetSampleIndex, error) {
	id, entry, err := captures.writeCapture(func(w *bufio.Writer) (int, int, error) {
		for _, m := range c.messages {
			b, err := mcpMarshal(m.line)
			if err != nil {
				return 0, 0, err
			}
			if _, err := w.Write(append(b, '\n')); err != nil {
				return 0, 0, err
			}
		}
		return len(c.messages), c.totalBytes(), nil
	})
	if err != nil {
		return nil, err
	}
	previewLen := min(mcpPreviewMax, max(64, mcpPreviewBudget/max(1, len(c.messages))))
	res := &mcpGetSampleIndex{
		CaptureID: id, File: entry.file, ExpiresAt: entry.expires.UTC().Format(time.RFC3339),
		Count: len(c.messages), TotalBytes: c.totalBytes(), StoppedBy: c.stoppedBy,
		NextMessageBytes: c.nextMessageBytes, ServerDropped: c.dropped, Hint: mcpGetSampleHint,
	}
	for _, m := range c.messages {
		res.Messages = append(res.Messages, mcpGetSampleMessage{
			I: m.line.I, Topic: m.line.Topic, TS: m.line.TS, Retain: m.line.Retain, Bytes: m.line.Bytes,
			Encoding: m.line.Encoding, SHA256: m.line.SHA256, Kind: m.kind, JSONValid: m.jsonOK,
			Preview: mcpPreview(m, previewLen),
		})
	}
	for i := len(res.Messages) - 1; i >= 0; i-- {
		if mcpIndexFits(res) {
			break
		}
		res.Messages[i].Preview = ""
		res.PreviewsOmitted++
	}
	// A capture of 200 messages with long topics fills the cap on its own, without any preview:
	// listing fewer of them keeps the result whole, and every message stays readable by its index i,
	// which mqtt_read takes.
	for !mcpIndexFits(res) && len(res.Messages) > 1 {
		res.Messages = res.Messages[:len(res.Messages)-1]
		res.Omitted++
	}
	return res, nil
}

// mcpIndexFits reports whether a spilled sample's answer fits: under mcpIndexMax as compact JSON,
// and under mcpMaxResult once mcpResult has indented it (a cut index would not parse).
func mcpIndexFits(v any) bool {
	b, err := mcpMarshal(v)
	if err != nil || len(b) > mcpIndexMax {
		return false
	}
	return mcpFitsResult(v)
}

// mcpFitsResult reports whether v survives mcpResult whole: marshalled, indented, and under
// mcpMaxResult. A larger one would be cut into JSON that does not parse.
func mcpFitsResult(v any) bool {
	b, err := mcpMarshal(v)
	if err != nil {
		return false
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return false
	}
	return buf.Len() <= mcpMaxResult
}

// mcpPayloadKind names what a payload is: JSON, text, JPEG, PNG, gzip, or binary.
func mcpPayloadKind(encoding string, data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		return "jpeg"
	case bytes.HasPrefix(data, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}):
		return "png"
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		return "gzip"
	case encoding != "utf8":
		return "binary"
	case json.Valid(data):
		return "json"
	}
	return "text"
}

// mcpJSONPayload reports whether a text payload is valid JSON.
func mcpJSONPayload(encoding string, data []byte) bool {
	return encoding == "utf8" && json.Valid(data)
}

// mcpPreview is the first n bytes of a text payload, cut back to a rune start, or hex: and the first
// 32 bytes in hex of a binary one.
func mcpPreview(m mcpCapturedMessage, n int) string {
	if m.line.Encoding != "utf8" {
		b := m.data
		if len(b) > 32 {
			b = b[:32]
		}
		return "hex:" + hex.EncodeToString(b)
	}
	text := m.line.Payload
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// mcpReadTool is the mqtt_read handler.
func mcpReadTool(captures *mcpCaptureStore) func(context.Context, *mcp.CallToolRequest, mcpReadIn) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in mcpReadIn) (*mcp.CallToolResult, any, error) {
		if in.Length < 0 || in.Length > mcpPageMax {
			return nil, nil, usageErr("length must be from 1 to %d", mcpPageMax)
		}
		if in.Offset < 0 {
			return nil, nil, usageErr("offset must be 0 or more")
		}
		c, ok := captures.get(in.CaptureID)
		if !ok {
			return nil, nil, mcpCaptureGone(in.CaptureID)
		}
		if in.I < 0 || in.I >= c.count {
			return nil, nil, usageErr("i must be from 0 to %d: the capture has %d messages", c.count-1, c.count)
		}
		m, err := c.line(in.I)
		if err != nil {
			return nil, nil, newErr(codeError, "the capture could not be read: %v", err)
		}
		page, err := mcpPage(m, in)
		if err != nil {
			return nil, nil, err
		}
		page.CaptureID, page.I, page.Topic = in.CaptureID, in.I, m.Topic
		page.Encoding, page.SHA256 = m.Encoding, m.SHA256
		return mcpResult(marshalRaw(page)), nil, nil
	}
}

// mcpCaptureGone is the error of an unknown, malformed or expired capture id.
func mcpCaptureGone(id string) error {
	return usageErr("capture %s is unknown or expired (captures last 30 minutes); take a new sample with mqtt_get_sample", id)
}

// mcpPageResult is one page of a message's text: its payload, or one field of a JSON payload.
type mcpPageResult struct {
	CaptureID  string `json:"captureId"`
	I          int    `json:"i"`
	Topic      string `json:"topic"`
	Path       string `json:"path,omitempty"`
	Offset     int    `json:"offset"`
	Length     int    `json:"length"`
	TotalBytes int    `json:"totalBytes"`
	NextOffset *int   `json:"nextOffset"`
	Encoding   string `json:"encoding"`
	SHA256     string `json:"sha256"`
	Data       string `json:"data"`
}

// mcpPage cuts the page the call asks for out of the message's text: the payload's own text (UTF-8,
// or base64), or, with path, one field of a JSON payload marshalled compactly.
func mcpPage(m sampleMsg, in mcpReadIn) (*mcpPageResult, error) {
	length := in.Length
	if length == 0 {
		length = mcpPageMax
	}
	text := m.Payload
	if in.Path != "" {
		if !mcpJSONPayload(m.Encoding, []byte(m.Payload)) {
			return nil, usageErr("the payload of message %d is not JSON: path needs a JSON payload", in.I)
		}
		v, err := mcpJSONPath([]byte(m.Payload), in.Path)
		if err != nil {
			return nil, err
		}
		b, err := mcpMarshal(v)
		if err != nil {
			return nil, usageErr("path %q: %v", in.Path, err)
		}
		text = string(b)
	}
	if in.Offset > len(text) {
		return nil, usageErr("offset %d is past the end: this is %d bytes", in.Offset, len(text))
	}
	res := &mcpPageResult{Offset: in.Offset, TotalBytes: len(text), Path: in.Path}
	base64Text := m.Encoding == "base64" && in.Path == ""
	end := in.Offset + length
	switch {
	case base64Text:
		// The base64 text decodes in whole 4-character groups: page on those, or the caller cannot
		// join the pages back into something that decodes. A length under 4 still reads one group.
		if in.Offset%4 != 0 {
			return nil, usageErr("offset %d is not a multiple of 4: base64 reads in groups of 4 characters; use nextOffset", in.Offset)
		}
		if end -= end % 4; end-in.Offset < 4 {
			end = in.Offset + 4
		}
	default:
		// Text pages are cut where a rune starts, or the caller gets half a character.
		if in.Offset < len(text) && !utf8.RuneStart(text[in.Offset]) {
			return nil, usageErr("offset %d is inside a UTF-8 character; use nextOffset from the previous page", in.Offset)
		}
		for end > in.Offset && end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
	}
	if end > len(text) {
		end = len(text)
	}
	res.Data, res.Length = text[in.Offset:end], end-in.Offset
	if end < len(text) {
		next := end
		res.NextOffset = &next
	}
	return res, nil
}

// mcpPathSeg is one step of a path: a key, a list index, or both (key[index]).
type mcpPathSeg struct {
	key     string
	index   int
	isIndex bool
}

// mcpParsePath splits a dotted path with list indexes (a.b[3].c, [0].x, items[2]) into its steps.
// A key that holds ".", "[" or "]" cannot be written on one, and the error says so.
func mcpParsePath(path string) ([]mcpPathSeg, error) {
	if path == "" {
		return nil, usageErr("path is empty: give a key such as data.type")
	}
	notAddressable := usageErr("path %q: a key that holds \".\", \"[\" or \"]\" cannot be addressed; there is no way to write it", path)
	var segs []mcpPathSeg
	rest := path
	for rest != "" {
		key := rest
		if i := strings.IndexAny(rest, ".["); i >= 0 {
			key, rest = rest[:i], rest[i:]
		} else {
			rest = ""
		}
		switch {
		case strings.HasPrefix(rest, "."):
			if key == "" {
				return nil, notAddressable // ".x", or "a..b"
			}
			segs = append(segs, mcpPathSeg{key: key})
			rest = rest[1:]
			if rest == "" {
				return nil, usageErr("path %q ends with a dot", path)
			}
		case strings.HasPrefix(rest, "["):
			if key != "" {
				segs = append(segs, mcpPathSeg{key: key})
			}
			for strings.HasPrefix(rest, "[") {
				end := strings.IndexByte(rest, ']')
				if end < 0 {
					return nil, usageErr("path %q has a [ without its ]", path)
				}
				n, err := strconv.Atoi(rest[1:end])
				if err != nil || n < 0 {
					return nil, notAddressable // "[x]" names no index tnctl can read
				}
				segs = append(segs, mcpPathSeg{index: n, isIndex: true})
				rest = rest[end+1:]
			}
			if rest != "" && !strings.HasPrefix(rest, ".") && !strings.HasPrefix(rest, "[") {
				return nil, notAddressable // "items[2]x"
			}
			if strings.HasPrefix(rest, ".") {
				rest = rest[1:]
				if rest == "" {
					return nil, usageErr("path %q ends with a dot", path)
				}
			}
		case key != "":
			segs = append(segs, mcpPathSeg{key: key})
		}
		if len(segs) == 0 {
			return nil, usageErr("path %q does not name a key", path)
		}
	}
	return segs, nil
}

// mcpJSONPath selects one value from a JSON payload. A missing key or index is an error naming the
// first step that failed and what is there.
func mcpJSONPath(payload []byte, path string) (any, error) {
	segs, err := mcpParsePath(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	var cur any
	if err := dec.Decode(&cur); err != nil {
		return nil, usageErr("the payload is not JSON: %v", err)
	}
	for i, seg := range segs {
		where := mcpWhere(segs[:i])
		if seg.isIndex {
			arr, ok := cur.([]any)
			if !ok {
				return nil, usageErr("path %q: [%d] needs a list, and %s is not one", path, seg.index, where)
			}
			if seg.index >= len(arr) {
				return nil, usageErr("path %q: there is no [%d] at %s, which has %d", path, seg.index, where, len(arr))
			}
			cur = arr[seg.index]
			continue
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, usageErr("path %q: %q needs an object, and %s is not one", path, seg.key, where)
		}
		v, ok := obj[seg.key]
		if !ok {
			// A key that holds ".", "[" or "]" reads as several steps; say why it cannot be selected
			// rather than that it is missing.
			for k := range obj {
				if strings.ContainsAny(k, ".[]") && strings.HasPrefix(k, seg.key) {
					return nil, usageErr("path %q: the key %q at %s holds \".\", \"[\" or \"]\" and cannot be addressed", path, k, where)
				}
			}
			return nil, usageErr("path %q: there is no key %q at %s; it has: %s", path, seg.key, where, mcpKeyList(obj))
		}
		cur = v
	}
	return cur, nil
}

// mcpWhere names the place a step looks in, for an error: the path to it.
func mcpWhere(segs []mcpPathSeg) string {
	if len(segs) == 0 {
		return "the payload"
	}
	var b strings.Builder
	for i, s := range segs {
		switch {
		case s.isIndex:
			fmt.Fprintf(&b, "[%d]", s.index)
		case i > 0 && !segs[i-1].isIndex:
			b.WriteString("." + s.key)
		default:
			b.WriteString(s.key)
		}
	}
	return b.String()
}

// mcpKeyList names the keys an object has, at most 20 of them.
func mcpKeyList(obj map[string]any) string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	more := ""
	if len(keys) > 20 {
		more = fmt.Sprintf(", and %d more", len(keys)-20)
		keys = keys[:20]
	}
	return strings.Join(keys, ", ") + more
}
