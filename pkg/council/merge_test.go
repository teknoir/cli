package council

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/internal/textsafe"
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

// The parts of a bot key: tbk1, two ids and 43 characters.
const (
	keyPrefix = "tbk1."
	keyTail   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq"
)

// testKey is a bot key.
var testKey = keyPrefix + "abc123.def456." + keyTail

// TestMergerHoldsAKeyAcrossWrites: a key that two text deltas split is never emitted in a piece a
// redaction cannot see, whatever the timer does between them.
func TestMergerHoldsAKeyAcrossWrites(t *testing.T) {
	if len(testKey) != len(keyPrefix)+6+1+6+1+43 {
		t.Fatalf("the test key is %d bytes", len(testKey))
	}
	for _, cut := range []int{len(keyPrefix), len(keyPrefix) + 3, len(testKey) - 42, len(testKey) - 1} {
		rec := &recorder{}
		m := newMerger(rec, "captain", time.Hour, 1<<20)
		m.write("the key is " + testKey[:cut])
		m.onTimer()
		m.write(testKey[cut:] + " and that is all")
		m.close()

		texts := rec.text()
		if joined := strings.Join(texts, ""); !strings.Contains(joined, "[redacted]") || strings.Contains(joined, testKey) {
			t.Errorf("cut %d: joined %q", cut, joined)
		}
		for _, s := range texts {
			if strings.Contains(s, keyTail) {
				t.Errorf("cut %d: a delta carries the key's last 43 characters: %q", cut, s)
			}
		}
	}
}

// TestMergerHoldsAKeyStartFarBack: the end of the buffer that could start a key is held even when
// the only "tbk1." before it is further back than a key can reach. The hold must not be dropped
// with it: the rest of a key written next would otherwise carry its last 43 characters.
func TestMergerHoldsAKeyStartFarBack(t *testing.T) {
	for _, prefix := range []string{"t", "tb", "tbk", "tbk1", "tbk1."} {
		rec := &recorder{}
		m := newMerger(rec, "captain", time.Hour, 1<<20)
		m.write("keys look like tbk1.<bot>.<key>.<secret> " + strings.Repeat("z", 250) + " here it is: " + prefix)
		m.onTimer()
		m.write(testKey[len(prefix):] + " done")
		m.close()

		texts := rec.text()
		if joined := strings.Join(texts, ""); !strings.Contains(joined, "[redacted]") || strings.Contains(joined, testKey) {
			t.Errorf("prefix %q: joined %q", prefix, joined)
		}
		for _, s := range texts {
			if strings.Contains(s, keyTail) {
				t.Errorf("prefix %q: a delta carries the key's last 43 characters: %q", prefix, s)
			}
		}
	}
}

// TestMergerHoldsAKeyWithATbk1ID: a key whose own id is "tbk1" holds the cut inside itself: no
// delta carries its second half.
func TestMergerHoldsAKeyWithATbk1ID(t *testing.T) {
	key := "tbk1.tbk1.abc." + keyTail
	rec := &recorder{}
	m := newMerger(rec, "captain", time.Hour, 1<<20)
	m.write("the key is " + key)
	m.onTimer()
	for _, s := range rec.text() {
		if strings.Contains(s, keyTail) {
			t.Fatalf("a delta carries the key's last 43 characters: %q", s)
		}
	}
	m.write(" done")
	m.onTimer()
	m.close()

	if joined := strings.Join(rec.text(), ""); !strings.Contains(joined, "[redacted]") || strings.Contains(joined, key) {
		t.Errorf("joined %q", joined)
	}
	for _, s := range rec.text() {
		if strings.Contains(s, keyTail) {
			t.Errorf("a delta carries the key's last 43 characters: %q", s)
		}
	}
}

// TestMergerHoldsAKeyWhoseIDsHoldTbk1: a bot id may be "tbk1" or hold it, and the key then starts
// before the last "tbk1." of the buffer. A split there must not hand a delta the key's last 43
// characters.
func TestMergerHoldsAKeyWhoseIDsHoldTbk1(t *testing.T) {
	for _, start := range []string{"tbk1.tbk1.abc.", "tbk1.mytbk1.abc.", "tbk1.abc.tbk1."} {
		key := start + keyTail
		// The inner "tbk1." and the cut points a few characters after it, and inside the tail.
		inner := strings.Index(key[len(keyPrefix):], keyPrefix) + 2*len(keyPrefix)
		cuts := []int{inner + 1, inner + 3, inner + 6, len(key) - 20}
		for _, cut := range cuts {
			rec := &recorder{}
			m := newMerger(rec, "captain", time.Hour, 1<<20)
			m.write("the key is " + key[:cut])
			m.onTimer()
			m.write(key[cut:] + " done")
			m.close()

			texts := rec.text()
			if joined := strings.Join(texts, ""); !strings.Contains(joined, "[redacted]") || strings.Contains(joined, key) {
				t.Errorf("%s cut %d: joined %q", start, cut, joined)
			}
			for _, s := range texts {
				if strings.Contains(s, keyTail) {
					t.Errorf("%s cut %d: a delta carries the key's last 43 characters: %q", start, cut, s)
				}
			}
		}
	}
}

// TestMergerEmitsARedactedKeyTail: a stream that ends inside a key leaves a piece that is neither a
// key nor a prefix of one; flush and close redact it, since no later write completes it.
func TestMergerEmitsARedactedKeyTail(t *testing.T) {
	partial := "tbk1.ns.bot." + keyTail[:42]
	for _, end := range []string{"flush", "close"} {
		rec := &recorder{}
		m := newMerger(rec, "captain", time.Hour, 1<<20)
		m.write("the key is " + partial)
		if end == "flush" {
			m.flush()
		} else {
			m.close()
		}
		texts := rec.text()
		if len(texts) != 1 {
			t.Fatalf("%s: text events %q, want one", end, texts)
		}
		if strings.Contains(texts[0], keyTail[:42]) {
			t.Errorf("%s: the delta carries the key's piece: %q", end, texts[0])
		}
		if !strings.HasSuffix(texts[0], textsafe.Redacted) {
			t.Errorf("%s: the delta %q does not end with the redaction", end, texts[0])
		}
	}
}

// TestMergerHoldsAPrefix: a buffer that ends with the start of a key is held at a timed emit until
// the next write completes it or not.
func TestMergerHoldsAPrefix(t *testing.T) {
	rec := &recorder{}
	m := newMerger(rec, "captain", time.Hour, 1<<20)
	m.write("and then tb")
	m.onTimer()
	if got := strings.Join(rec.text(), ""); got != "and then " {
		t.Fatalf("text events %q, want the key's start held", got)
	}
	m.write("k1.rest")
	m.close()
	if joined := strings.Join(rec.text(), ""); joined != "and then tbk1.rest" {
		t.Errorf("joined %q", joined)
	}
}

// TestMergerCloseEmitsAHeldTail: close emits what the timer held back, redacted.
func TestMergerCloseEmitsAHeldTail(t *testing.T) {
	rec := &recorder{}
	m := newMerger(rec, "captain", time.Hour, 1<<20)
	m.write("and then tbk1.")
	m.onTimer()
	if got := strings.Join(rec.text(), ""); got != "and then " {
		t.Fatalf("text events %q, want the key's start held", got)
	}
	m.close()
	if joined := strings.Join(rec.text(), ""); joined != "and then tbk1." {
		t.Errorf("after close: joined %q", joined)
	}
}

// TestMergerHoldsOnlyAKeyStart: text that starts like a key and is not one is emitted once the hold
// would exceed what a key can be.
func TestMergerHoldsOnlyAKeyStart(t *testing.T) {
	rec := &recorder{}
	m := newMerger(rec, "captain", time.Hour, 1<<20)
	m.write("tbk1." + strings.Repeat("z", 300))
	m.onTimer()
	if got := strings.Join(rec.text(), ""); got != "tbk1."+strings.Repeat("z", 300) {
		t.Errorf("the held text is %d bytes, want it emitted whole", len(got))
	}
	if len(rec.text()) != 1 {
		t.Errorf("%d events, want one", len(rec.text()))
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
