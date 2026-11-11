package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// readerOf reads one stream body with an idle time of 0 (never).
func readerOf(body string) *sseReader {
	return newSSEReader(io.NopCloser(strings.NewReader(body)), 0)
}

func readAll(t *testing.T, r *sseReader) []SSEEvent {
	t.Helper()
	var out []SSEEvent
	for {
		ev, err := r.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatalf("Next: %v", err)
			}
			return out
		}
		out = append(out, ev)
	}
}

// The event's id is its last "id:" line; an id with a NUL is dropped and the previous one kept, as
// the SSE spec says.
func TestSSEEventID(t *testing.T) {
	cases := []struct {
		name, body string
		want       []string
	}{
		{"none", "event: a\ndata: 1\n\n", []string{""}},
		{"one", "id: 7\nevent: a\ndata: 1\n\n", []string{"7"}},
		{"the last one wins", "id: 7\nid: 9\nevent: a\ndata: 1\n\n", []string{"9"}},
		{"a NUL is ignored", "id: 7\nid: 9\x00\nevent: a\ndata: 1\n\n", []string{"7"}},
		{"the id belongs to its event", "id: 7\nevent: a\ndata: 1\n\nevent: b\ndata: 2\n\n", []string{"7", ""}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, ev := range readAll(t, readerOf(c.body)) {
				got = append(got, ev.ID)
			}
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Errorf("ids %q, want %q", got, c.want)
			}
		})
	}
}

// A line ends at \n, \r\n or a bare \r; keepalive comments are not events; an event the stream ends in
// the middle of is dropped.
func TestSSEReaderLineEndingsAndKeepalives(t *testing.T) {
	cases := []struct {
		name, body string
		want       []SSEEvent
	}{
		{"LF", "event: a\ndata: 1\n\n", []SSEEvent{{Name: "a", Data: "1"}}},
		{"CRLF", "event: a\r\ndata: 1\r\n\r\n", []SSEEvent{{Name: "a", Data: "1"}}},
		{"CR", "event: a\rdata: 1\r\r", []SSEEvent{{Name: "a", Data: "1"}}},
		{"keepalives", ": open\n\n: keepalive\n\nevent: a\ndata: 1\n\n", []SSEEvent{{Name: "a", Data: "1"}}},
		{"joined data", "event: a\ndata: one\ndata: two\n\n", []SSEEvent{{Name: "a", Data: "one\ntwo"}}},
		{"a cut event is dropped", "event: a\ndata: 1\n\nevent: b\ndata: 2\n", []SSEEvent{{Name: "a", Data: "1"}}},
		{"no name", "data: 1\n\n", []SSEEvent{{Name: "", Data: "1"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := readAll(t, readerOf(c.body))
			if len(got) != len(c.want) {
				t.Fatalf("events %+v, want %+v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("event %d %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// A line or an event's data past 1 MiB is an error, not a memory hole.
func TestSSEReaderRefusesOversizedEvents(t *testing.T) {
	r := readerOf("data: " + strings.Repeat("y", maxStreamEvent) + "z\n\n")
	if _, err := r.Next(); !errors.Is(err, errStreamTooLarge) {
		t.Errorf("a long line: %v", err)
	}
	r = readerOf("data: " + strings.Repeat("y", maxStreamEvent/2) + "\ndata: " + strings.Repeat("y", maxStreamEvent/2) + "\n\n")
	if _, err := r.Next(); !errors.Is(err, errStreamTooLarge) {
		t.Errorf("a long event: %v", err)
	}
}

// A stream that stays silent for the idle time is closed and says why; a clean end is io.EOF.
func TestSSEReaderIdleTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": open\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer ts.Close()
	resp, err := streamClient.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	r := newSSEReader(resp.Body, 150*time.Millisecond)
	defer func() { _ = r.Close() }()
	_, err = r.Next()
	if err == nil || !strings.Contains(err.Error(), "no data from the stream for") {
		t.Fatalf("err %v", err)
	}
	if errors.Is(err, io.EOF) {
		t.Error("a silent stream ended as a clean end of stream")
	}

	// A body that ends is a clean end, whatever the idle time.
	r2 := newSSEReader(io.NopCloser(strings.NewReader("event: a\ndata: 1\n\n")), time.Second)
	defer func() { _ = r2.Close() }()
	if _, err := r2.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("second Next: %v", err)
	}
}
