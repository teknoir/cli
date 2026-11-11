package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Go mirror of the MQTT stream of the fleet API: GET <Base>/mqtt/stream?topic=... answers a
// text/event-stream of the MQTT messages under those topics, plus how many it had to drop and why it
// ends. The reader of the stream itself lives in sse.go, with the chat's.

// MaxStreamTopics is how many topics one MQTT stream may follow.
const MaxStreamTopics = 8

// The event names of the stream.
const (
	StreamEventMessage = "message"
	StreamEventDropped = "dropped"
	StreamEventError   = "error"
)

// ValidateStreamTopics checks how many topics a stream follows and each of them, with the rule the
// platform applies.
func ValidateStreamTopics(topics []string) error {
	if len(topics) < 1 || len(topics) > MaxStreamTopics {
		return fmt.Errorf("a stream follows 1 to %d topics, got %d", MaxStreamTopics, len(topics))
	}
	for _, t := range topics {
		if err := ValidateStreamTopic(t); err != nil {
			return err
		}
	}
	return nil
}

// ValidateStreamTopic accepts what the platform accepts: /devices/<device|+>/state/#,
// /devices/<device|+>/events/#, or a concrete topic below /devices/<device|+>/state/ or
// /devices/<device|+>/events/. Anything else (other topics, a shared subscription, wildcards in the
// middle of a topic, a topic over 256 characters) is refused.
func ValidateStreamTopic(topic string) error {
	bad := fmt.Errorf("%q is not a topic a stream can follow: use /devices/<device|+>/state/#, "+
		"/devices/<device|+>/events/#, or a concrete topic below /devices/<device|+>/state/ or /devices/<device|+>/events/", topic)
	if utf8.RuneCountInString(topic) > maxStreamTopicLen {
		return bad
	}
	// A topic always starts with "/": the first segment is empty.
	parts := strings.Split(topic, "/")
	if len(parts) < 5 || parts[0] != "" || parts[1] != "devices" {
		return bad
	}
	device := parts[2]
	if device != "+" && (len(device) > 63 || !streamDeviceLabel.MatchString(device)) {
		return bad
	}
	if parts[3] != "state" && parts[3] != "events" {
		return bad
	}
	if len(parts) == 5 && parts[4] == "#" {
		return nil // the whole subtree of a device, or of every device with +
	}
	for _, seg := range parts[4:] {
		// A concrete segment: + and # are only the whole device level and the last one.
		if seg == "" || strings.ContainsAny(seg, "+#\x00") || strings.ContainsFunc(seg, unicode.IsSpace) {
			return bad
		}
	}
	return nil
}

// streamDeviceLabel is a device name in a topic: the platform's DNS label rule.
var streamDeviceLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// maxStreamTopicLen caps one topic, as the platform does.
const maxStreamTopicLen = 256

// StreamEvent is one server-sent event of the MQTT stream. Data is the event's data lines, joined
// with newlines.
type StreamEvent struct {
	// Name is message, dropped or error.
	Name string
	Data string
}

// msgNoStreamOnOlderPlatform is what a 404 without a JSON error body of the stream route says: the
// platform was built before the route, not the device gone.
const msgNoStreamOnOlderPlatform = "this platform has no live MQTT stream yet: it is older than this tnctl (ask for a platform update)"

// MqttSubscription is an open MQTT stream. Close it when done.
type MqttSubscription struct {
	r *sseReader
}

// MqttStream opens the stream of topics: GET <Base>/mqtt/stream. A non-2xx answer is an *Error and
// no stream is returned.
func (f *Fleet) MqttStream(ctx context.Context, topics []string) (*MqttSubscription, error) {
	if err := ValidateStreamTopics(topics); err != nil {
		return nil, err
	}
	q := url.Values{}
	for _, t := range topics {
		q.Add("topic", t)
	}
	u := f.Base + "/mqtt/stream?" + q.Encode()
	resp, err := f.openSSE(ctx, u, nil)
	if err != nil {
		// Express's answer to a route the platform does not have is a 404 with an empty body (or an
		// HTML page); a missing device inside the route is a JSON error. The first is an older
		// platform, not a missing device.
		var ae *Error
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			if trimmed := strings.TrimSpace(string(ae.Body)); trimmed == "" || !strings.HasPrefix(trimmed, "{") {
				ae.Message = msgNoStreamOnOlderPlatform
			}
		}
		return nil, err
	}
	return &MqttSubscription{r: newSSEReader(resp.Body, streamIdleTimeout)}, nil
}

// Close ends the stream.
func (s *MqttSubscription) Close() error {
	return s.r.Close()
}

// Next returns the next event of the stream. An event without an "event:" line is a message; one
// without data is not an event at all. io.EOF means the server ended the stream; any other error is
// the read error.
func (s *MqttSubscription) Next() (StreamEvent, error) {
	ev, err := s.r.Next()
	if err != nil {
		return StreamEvent{}, err
	}
	name := ev.Name
	if name == "" {
		name = StreamEventMessage
	}
	return StreamEvent{Name: name, Data: ev.Data}, nil
}

// StreamMessage is the data of a message event.
type StreamMessage struct {
	Topic string `json:"topic"`
	// Payload is the message as text; PayloadBase64 is set instead when it is not UTF-8.
	Payload       string `json:"payload"`
	PayloadBase64 string `json:"payloadBase64"`
	Retain        bool   `json:"retain"`
	TS            string `json:"ts"`
}

// StreamDropped is the data of a dropped event: how many messages the server could not send, and why
// ("rate" or "size").
type StreamDropped struct {
	Count  int    `json:"count"`
	Reason string `json:"reason"`
}

// StreamError is the data of an error event: the stream then ends.
type StreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ParseStreamMessage reads the data of a message event.
func ParseStreamMessage(data string) (*StreamMessage, error) {
	var m StreamMessage
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		return nil, fmt.Errorf("unexpected message from the MQTT stream: %w", err)
	}
	return &m, nil
}

// ParseStreamDropped reads the data of a dropped event.
func ParseStreamDropped(data string) (*StreamDropped, error) {
	var d StreamDropped
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return nil, fmt.Errorf("unexpected dropped event from the MQTT stream: %w", err)
	}
	return &d, nil
}

// ParseStreamError reads the data of an error event.
func ParseStreamError(data string) (*StreamError, error) {
	var e StreamError
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		return nil, fmt.Errorf("unexpected error event from the MQTT stream: %w", err)
	}
	return &e, nil
}
