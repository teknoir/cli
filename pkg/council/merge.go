package council

import (
	"sync"
	"time"

	"github.com/teknoir/cli/pkg/council/hub"
)

// emitter posts one event of a turn. hub.Poster is one; so is a test's recorder.
type emitter interface {
	Emit(eventType string, data any)
}

// merger buffers one member's streamed text and emits it as text_delta{member, text}.
//
// Text is never split or reordered: every emit carries the whole buffer. Emit runs while the mutex
// is held, so a timer flush can never land after a later flush, or after an event the member emits
// once its flush returned.
type merger struct {
	e        emitter
	member   string
	every    time.Duration
	maxBytes int

	mu     sync.Mutex
	buf    []byte
	timer  *time.Timer
	closed bool
}

// newMerger returns the merger of one member: it emits at most every, and at once when the buffer
// reaches maxBytes.
func newMerger(e emitter, member string, every time.Duration, maxBytes int) *merger {
	return &merger{e: e, member: member, every: every, maxBytes: maxBytes}
}

// write is the onText callback of model.Client.Stream: it appends text to the buffer, and emits the
// buffer when it is full or starts the timer that emits it otherwise. A write after close is
// dropped.
func (m *merger) write(text string) {
	if text == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.buf = append(m.buf, text...)
	if len(m.buf) >= m.maxBytes {
		m.emitLocked()
		return
	}
	if m.timer == nil {
		m.timer = time.AfterFunc(m.every, m.onTimer)
	}
}

// flush emits what is buffered now.
func (m *merger) flush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopTimerLocked()
	m.emitLocked()
}

// close flushes, then ignores every later write and timer.
func (m *merger) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopTimerLocked()
	m.emitLocked()
	m.closed = true
}

// onTimer is the timer callback: it emits what is buffered, unless the merger is closed or the
// buffer is empty.
func (m *merger) onTimer() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timer = nil
	if m.closed {
		return
	}
	m.emitLocked()
}

// emitLocked emits and clears the buffer, and stops the timer. Emit is called with mu held: the
// event it posts is ordered against every other event of the member.
func (m *merger) emitLocked() {
	m.stopTimerLocked()
	if len(m.buf) == 0 {
		return
	}
	text := string(m.buf)
	m.buf = m.buf[:0]
	m.e.Emit(hub.EventTextDelta, map[string]any{"member": m.member, "text": text})
}

// stopTimerLocked stops the timer, if one runs.
func (m *merger) stopTimerLocked() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}
