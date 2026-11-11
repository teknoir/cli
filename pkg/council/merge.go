package council

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/internal/textsafe"
)

// keyStart is what a TeknoirBot key begins with.
const keyStart = "tbk1."

// maxKeyHold is the most of a buffer's end that is held back because it could be the start of a
// bot key another write completes. A key is at most 176 bytes, so nothing longer can be one.
const maxKeyHold = 200

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
		m.emitLocked(false)
		return
	}
	if m.timer == nil {
		m.timer = time.AfterFunc(m.every, m.onTimer)
	}
}

// flush emits everything buffered now, a key's start held back or not.
func (m *merger) flush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopTimerLocked()
	m.emitLocked(true)
}

// close flushes, then ignores every later write and timer.
func (m *merger) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopTimerLocked()
	m.emitLocked(true)
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
	m.emitLocked(false)
}

// emitLocked emits the buffer, redacted, and stops the timer. Unless all is set, the buffer's end
// that could be the start of a bot key is held back for a later write: the platform redacts a key
// per event, so a key cut in two would pass in both halves. Emit is called with mu held: the event
// it posts is ordered against every other event of the member.
func (m *merger) emitLocked(all bool) {
	m.stopTimerLocked()
	if len(m.buf) == 0 {
		return
	}
	text := string(m.buf)
	cut := len(text)
	if !all {
		if from := holdFrom(text); from < cut {
			cut = from
		}
		// A key whose own ids start with tbk1 is held from inside it (the last "tbk1." of the
		// buffer is then the key's second id): the cut must not fall inside a key that redacting
		// the piece would miss.
		for _, loc := range textsafe.BotKeyPattern.FindAllStringIndex(text, -1) {
			if loc[0] < cut && cut < loc[1] {
				cut = loc[0]
			}
		}
	}
	// Never split a character: a held-back part grows to the next boundary.
	for cut > 0 && cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut--
	}
	if cut == 0 {
		return
	}
	m.buf = append(m.buf[:0], m.buf[cut:]...)
	out := text[:cut]
	if all {
		// Nothing follows this emit: a key the text ends inside can no longer be completed, and
		// its pieces would pass a redaction that sees neither.
		out = textsafe.RedactKeyTail(out)
	}
	m.e.Emit(hub.EventTextDelta, map[string]any{"member": m.member, "text": textsafe.RedactBotKeys(out)})
}

// holdFrom is where the buffer's end may still become a bot key: the earliest "tbk1." whose rest
// could still grow into one, or the proper prefix of a key's start that the buffer ends with. It is
// len(s) when nothing at the end can be a key's start.
func holdFrom(s string) int {
	from := len(s)
	// Every "tbk1." counts, not only the last: an id may itself hold "tbk1", and the key then
	// starts before it. Walking from the earliest is what keeps such a key whole.
	for i := 0; i <= len(s)-len(keyStart); {
		j := strings.Index(s[i:], keyStart)
		if j < 0 {
			break
		}
		if p := i + j; len(s)-p <= maxKeyHold && textsafe.KeyPrefixPattern.MatchString(s[p:]) {
			from = p
			break
		}
		i += j + 1
	}
	// A prefix of the key's start has no length bound of its own: it is held however far back the
	// "tbk1." that looked like a key's start is.
	for n := len(keyStart) - 1; n >= 1; n-- {
		if strings.HasSuffix(s, keyStart[:n]) {
			if p := len(s) - n; p < from {
				from = p
			}
			break
		}
	}
	return from
}

// stopTimerLocked stops the timer, if one runs.
func (m *merger) stopTimerLocked() {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
}
