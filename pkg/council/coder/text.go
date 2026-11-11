package coder

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ellipsis marks text the package cut.
const ellipsis = "…"

// ansiPattern matches the escape sequences of terminal colour and of cursor control: OSC first, so
// its terminator is not read as a two-character escape.
var ansiPattern = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[@-_]`)

// The secrets that must never leave the package.
var (
	// botKeyPattern is a TeknoirBot key: tbk1.<id>.<id>.<43 characters>.
	botKeyPattern = regexp.MustCompile(`tbk1\.[a-z0-9-]{1,63}\.[a-z0-9-]{1,63}\.[A-Za-z0-9_-]{43}`)
	// anthropicKeyPattern is an Anthropic API key.
	anthropicKeyPattern = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{10,}`)
)

// redacted replaces a secret with this.
const redacted = "[redacted]"

// sanitize makes text safe to leave the package: terminal escapes are removed, and so are the
// control characters other than a newline and a tab, before the secrets are redacted. The control
// characters go first: one inside a key stops the patterns from matching, and stripping it
// afterwards would rebuild the key.
func sanitize(s string) string {
	s = ansiPattern.ReplaceAllString(s, "")
	if strings.ContainsFunc(s, func(r rune) bool { return isControl(r) }) {
		var b strings.Builder
		b.Grow(len(s))
		for _, r := range s {
			if isControl(r) {
				continue
			}
			b.WriteRune(r)
		}
		s = b.String()
	}
	s = botKeyPattern.ReplaceAllString(s, redacted)
	return anthropicKeyPattern.ReplaceAllString(s, redacted)
}

// isControl reports whether r is a control character that sanitize drops.
func isControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.IsControl(r)
}

// cutText cuts s to at most max bytes, on a UTF-8 boundary, and marks the cut. The ellipsis is in
// addition to max.
func cutText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	room := max - len(ellipsis)
	if room < 0 {
		room = 0
	}
	return cutBytes(s, room) + ellipsis
}

// cutTotal cuts s to at most max bytes in all: the ellipsis counts against max.
func cutTotal(s string, max int) string {
	if len(s) <= max {
		return s
	}
	room := max - len(ellipsis)
	if room < 0 {
		room = 0
	}
	return cutBytes(s, room) + ellipsis
}

// cutBytes cuts s to at most max bytes without splitting a character, and without marking the cut.
func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	max int
	buf []byte
	// wrapped is set when bytes were dropped: the kept bytes may start inside a secret, which no
	// pattern would then find.
	wrapped bool
}

// Write keeps the end of p.
func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.wrapped = true
	}
	return len(p), nil
}

// String is the kept stderr, sanitized. When the window starts inside a word, that word goes: it
// may be the tail of a secret whose prefix, the part the patterns match, was dropped.
func (t *tailBuffer) String() string {
	buf := t.buf
	if t.wrapped {
		// Secrets hold no whitespace, so the first whitespace after the cut is the end of the
		// word the cut went through.
		if i := bytes.IndexFunc(buf, unicode.IsSpace); i >= 0 {
			buf = buf[i:]
		} else {
			buf = nil
		}
	}
	return sanitize(string(buf))
}

// cutTail cuts s to its last max bytes, without splitting a character and without marking the cut.
func cutTail(s string, max int) string {
	if len(s) <= max {
		return s
	}
	start := len(s) - max
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}
