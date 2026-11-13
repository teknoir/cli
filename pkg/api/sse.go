package api

// The generic reader of a text/event-stream answer. The MQTT stream (stream.go) and a TeknoirBot's
// chat (council.go) are both one, and both read it through sseReader.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSEEvent is one server-sent event. Data is the event's data lines, joined with newlines.
type SSEEvent struct {
	// ID is the value of the event's last "id:" line, "" when it had none. An id that holds a NUL is
	// ignored, as the SSE spec says.
	ID string
	// Name is the event's "event:" line, "" when it had none.
	Name string
	// Data is the event's data lines, joined with newlines.
	Data string
}

// maxStreamEvent caps one line and one event's data of a stream.
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

// sseReader reads the events of a text/event-stream body. Close it when done.
type sseReader struct {
	body io.ReadCloser
	sc   *bufio.Scanner
	// idleFor is how long the stream may stay silent (0: never). idle fires when it has sent nothing
	// for that long and closes the body so a pending read returns.
	idleFor time.Duration
	idleMu  sync.Mutex
	idle    *time.Timer
	idleHit bool
}

// newSSEReader reads the events of body, dropping the connection when it stays silent for idle.
func newSSEReader(body io.ReadCloser, idle time.Duration) *sseReader {
	sc := bufio.NewScanner(body)
	sc.Split(splitSSE)
	sc.Buffer(make([]byte, 0, 64<<10), maxStreamEvent)
	r := &sseReader{body: body, sc: sc, idleFor: idle}
	if idle > 0 {
		r.idle = time.AfterFunc(idle, func() {
			r.idleMu.Lock()
			r.idleHit = true
			r.idleMu.Unlock()
			_ = r.body.Close()
		})
	}
	return r
}

// stopIdle stops the idle timer and reports whether it had fired.
func (r *sseReader) stopIdle() bool {
	r.idleMu.Lock()
	defer r.idleMu.Unlock()
	if r.idle != nil {
		r.idle.Stop()
	}
	return r.idleHit
}

// kick restarts the idle timer after a line arrived.
func (r *sseReader) kick() {
	r.idleMu.Lock()
	defer r.idleMu.Unlock()
	if r.idle != nil && !r.idleHit {
		r.idle.Reset(r.idleFor)
	}
}

// idleError is the error of a stream that went silent for the idle time.
func (r *sseReader) idleError() error {
	d := r.idleFor
	text := d.String()
	if d%time.Second == 0 {
		text = fmt.Sprintf("%ds", int(d/time.Second))
	}
	return fmt.Errorf("no data from the stream for %s", text)
}

// Close ends the stream.
func (r *sseReader) Close() error {
	if r.idle != nil {
		r.idle.Stop()
	}
	return r.body.Close()
}

// Next returns the next event of the stream: a blank line ends one, ":" lines (keepalives) are
// ignored, and an event the stream ends in the middle of is dropped. io.EOF means the server ended
// the stream; any other error is the read error.
func (r *sseReader) Next() (SSEEvent, error) {
	ev := SSEEvent{}
	var data []string
	size := 0
	for {
		if !r.sc.Scan() {
			err := r.sc.Err()
			if r.stopIdle() {
				return SSEEvent{}, r.idleError()
			}
			switch {
			case err == nil:
				return SSEEvent{}, io.EOF // a clean end of the stream
			case errors.Is(err, bufio.ErrTooLong):
				return SSEEvent{}, errStreamTooLarge
			}
			return SSEEvent{}, err
		}
		r.kick()
		line := r.sc.Text()
		if line == "" {
			if len(data) == 0 {
				ev, data, size = SSEEvent{}, nil, 0 // not an event
				continue
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
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				ev.ID = value
			}
		case "data":
			size += len(value) + 1
			if size > maxStreamEvent {
				return SSEEvent{}, errStreamTooLarge
			}
			data = append(data, value)
		}
	}
}

// openSSE does the GET of a stream with the bearer token, Accept: text/event-stream, the
// User-Agent and any extra headers. A 3xx or non-2xx answer is an *Error and no body is returned.
func (f *Fleet) openSSE(ctx context.Context, rawURL string, header http.Header) (*http.Response, error) {
	token, err := f.Token(ctx)
	if err != nil {
		return nil, err
	}
	debugf("Stream: GET %s", rawURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", UserAgent())
	for name, values := range header {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, errorFromResponse(http.MethodGet, rawURL, resp, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		return nil, errorFromResponse(http.MethodGet, rawURL, resp, body)
	}
	return resp, nil
}
