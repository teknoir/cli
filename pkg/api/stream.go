package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Go mirror of the MQTT stream of the fleet API: GET <Base>/mqtt/stream?topic=... answers a
// text/event-stream of the MQTT messages under those topics, plus how many it had to drop and why it
// ends.

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

// maxStreamEvent caps one line and one event's data of the stream.
const maxStreamEvent = 1 << 20

// streamIdleTimeout is how long the stream may stay silent before the connection is dropped: the
// platform sends a keepalive every 15 s, so 90 s without a byte means a half-open connection. It is
// a variable so a test can shorten it.
var streamIdleTimeout = 90 * time.Second

// errStreamTooLarge is what a line or an event past maxStreamEvent returns.
var errStreamTooLarge = errors.New("stream event larger than 1 MiB")

// streamTransport is the transport of a streaming request: connect and headers must arrive within
// 30 s (a half-open connection must not hang forever), and after them there is no timeout at all.
var streamTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	return t
}()

// streamClient is the client of a streaming request. It has no overall timeout (the stream is open
// until either side ends it) and never follows redirects, like httpClient: behind the gateway a
// refused request is a 302 to the login page.
var streamClient = &http.Client{
	Transport: streamTransport,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// splitSSE is the stream's line splitter: a line ends at \r\n, \n or a bare \r. A \r at the end of
// the buffer waits for the next read, since it may be the first half of a \r\n.
func splitSSE(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '\n':
			return i + 1, data[:i], nil
		case '\r':
			if i+1 < len(data) {
				if data[i+1] == '\n' {
					return i + 2, data[:i], nil
				}
				return i + 1, data[:i], nil
			}
			if atEOF {
				return i + 1, data[:i], nil
			}
			return 0, nil, nil // the next read may bring the \n of a \r\n
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// MqttSubscription is an open MQTT stream. Close it when done.
type MqttSubscription struct {
	body io.ReadCloser
	sc   *bufio.Scanner
	// idle fires when the stream has sent nothing for streamIdleTimeout; it closes the body so a
	// pending read returns.
	idleMu  sync.Mutex
	idle    *time.Timer
	idleHit bool
}

// stopIdle stops the idle timer and reports whether it had fired.
func (s *MqttSubscription) stopIdle() bool {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if s.idle != nil {
		s.idle.Stop()
	}
	return s.idleHit
}

// kick restarts the idle timer after a line arrived.
func (s *MqttSubscription) kick() {
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if s.idle != nil && !s.idleHit {
		s.idle.Reset(streamIdleTimeout)
	}
}

// idleError is the error of a stream that went silent for streamIdleTimeout.
func idleError() error {
	d := streamIdleTimeout
	text := d.String()
	if d%time.Second == 0 {
		text = fmt.Sprintf("%ds", int(d/time.Second))
	}
	return fmt.Errorf("no data from the stream for %s", text)
}

// MqttStream opens the stream of topics: GET <Base>/mqtt/stream. A non-2xx answer is an *Error and
// no stream is returned.
func (f *Fleet) MqttStream(ctx context.Context, topics []string) (*MqttSubscription, error) {
	if err := ValidateStreamTopics(topics); err != nil {
		return nil, err
	}
	token, err := f.Token(ctx)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	for _, t := range topics {
		q.Add("topic", t)
	}
	u := f.Base + "/mqtt/stream?" + q.Encode()
	debugf("Stream: GET %s", u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgent())
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, errorFromResponse(http.MethodGet, u, resp, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		return nil, errorFromResponse(http.MethodGet, u, resp, body)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Split(splitSSE)
	sc.Buffer(make([]byte, 0, 64<<10), maxStreamEvent)
	s := &MqttSubscription{body: resp.Body, sc: sc}
	s.idle = time.AfterFunc(streamIdleTimeout, func() {
		s.idleMu.Lock()
		s.idleHit = true
		s.idleMu.Unlock()
		_ = s.body.Close()
	})
	return s, nil
}

// Close ends the stream.
func (s *MqttSubscription) Close() error {
	if s.idle != nil {
		s.idle.Stop()
	}
	return s.body.Close()
}

// Next returns the next event of the stream: a blank line ends one, ":" lines (keepalives) are
// ignored, an event without an "event:" line is a message and an event the stream ends in the middle
// of is dropped. io.EOF means the server ended the stream; any other error is the read error.
func (s *MqttSubscription) Next() (StreamEvent, error) {
	ev := StreamEvent{}
	var data []string
	size := 0
	for {
		if !s.sc.Scan() {
			err := s.sc.Err()
			if s.stopIdle() {
				return StreamEvent{}, idleError()
			}
			switch {
			case err == nil:
				return StreamEvent{}, io.EOF // a clean end of the stream
			case errors.Is(err, bufio.ErrTooLong):
				return StreamEvent{}, errStreamTooLarge
			}
			return StreamEvent{}, err
		}
		s.kick()
		line := s.sc.Text()
		if line == "" {
			if len(data) == 0 {
				ev, data, size = StreamEvent{}, nil, 0 // not an event
				continue
			}
			if ev.Name == "" {
				ev.Name = StreamEventMessage
			}
			ev.Data = strings.Join(data, "\n")
			return ev, nil
		}
		if strings.HasPrefix(line, ":") {
			continue // keepalive
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			ev.Name = value
		case "data":
			size += len(value) + 1
			if size > maxStreamEvent {
				return StreamEvent{}, errStreamTooLarge
			}
			data = append(data, value)
		}
	}
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
