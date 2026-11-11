package council

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/council/hub"
)

// recorder is an emitter that remembers every event, in order.
type recorder struct {
	mu     sync.Mutex
	events []recordedEvent
}

type recordedEvent struct {
	typ  string
	data any
}

func (r *recorder) Emit(eventType string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedEvent{typ: eventType, data: data})
}

func (r *recorder) all() []recordedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recordedEvent(nil), r.events...)
}

func (r *recorder) text() []string {
	var out []string
	for _, e := range r.all() {
		if e.typ != hub.EventTextDelta {
			continue
		}
		m, _ := e.data.(map[string]any)
		s, _ := m["text"].(string)
		out = append(out, s)
	}
	return out
}

func TestMergerMaxBytesSendsAtOnce(t *testing.T) {
	rec := &recorder{}
	m := newMerger(rec, "fleet", time.Hour, 2048)
	var want strings.Builder
	for i := 0; i < 500; i++ {
		chunk := strings.Repeat("a", 10)
		want.WriteString(chunk)
		m.write(chunk)
	}
	m.flush()
	texts := rec.text()
	if len(texts) > 5000/2048+2 {
		t.Errorf("%d events for 5000 bytes, want at most %d", len(texts), 5000/2048+2)
	}
	if got := strings.Join(texts, ""); got != want.String() {
		t.Errorf("the joined text is %d bytes, want the %d written in order", len(got), want.Len())
	}
	for _, e := range rec.all() {
		m, _ := e.data.(map[string]any)
		if m["member"] != "fleet" {
			t.Errorf("member = %v, want fleet", m["member"])
		}
	}
}

func TestMergerTimerFlushes(t *testing.T) {
	rec := &recorder{}
	m := newMerger(rec, "captain", 20*time.Millisecond, 1<<20)
	m.write("hello")
	m.close()
	if got := rec.text(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("text events = %q, want one hello", got)
	}

	rec = &recorder{}
	m = newMerger(rec, "captain", 20*time.Millisecond, 1<<20)
	m.write("hello")
	time.Sleep(60 * time.Millisecond)
	if got := rec.text(); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("text events = %q, want the timer's one hello", got)
	}
	m.close()
	if got := rec.text(); len(got) != 1 {
		t.Errorf("text events = %q after close, want no empty event", got)
	}
}

func TestMergerFlushOrdersTheTimer(t *testing.T) {
	for i := 0; i < 200; i++ {
		rec := &recorder{}
		m := newMerger(rec, "fleet", time.Millisecond, 1<<20)
		m.write("before")
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.flush()
			rec.Emit("marker", nil)
		}()
		wg.Wait()
		var joined strings.Builder
		marker := false
		for _, e := range rec.all() {
			if e.typ == "marker" {
				marker = true
				continue
			}
			if marker {
				t.Fatalf("a text event came after the marker: %q", e.data)
			}
			joined.WriteString(e.data.(map[string]any)["text"].(string))
		}
		if joined.String() != "before" {
			t.Fatalf("joined text = %q, want before", joined.String())
		}
	}
}

func TestMergerWriteAfterClose(t *testing.T) {
	rec := &recorder{}
	m := newMerger(rec, "fleet", time.Hour, 16)
	m.close()
	m.write("dropped")
	m.flush()
	if got := rec.text(); len(got) != 0 {
		t.Errorf("text events = %q, want none after close", got)
	}
}
