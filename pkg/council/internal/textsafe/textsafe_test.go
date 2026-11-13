package textsafe

import "testing"

// tail is a bot key's last 43 characters.
const tail = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq"

// TestRedactKeyTail: only a key the text ends inside is replaced; a whole key is RedactBotKeys'
// job, and text with no key's tail is left as it is.
func TestRedactKeyTail(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"no key", "a robot is an edge computer", "a robot is an edge computer"},
		{"a whole key", "the key is tbk1.ns.bot." + tail, "the key is tbk1.ns.bot." + tail},
		{"a key cut short", "the key is tbk1.ns.bot." + tail[:42], "the key is " + Redacted},
		{"a key cut at its first characters", "tbk1.ns.bot.A", Redacted},
		{"a key's start only", "tbk1.ns.bot.", "tbk1.ns.bot."},
		{"a complete key and a cut one", "tbk1.ns.bot." + tail + " then tbk1.x.y.Z", "tbk1.ns.bot." + tail + " then " + Redacted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactKeyTail(tc.in); got != tc.want {
				t.Errorf("RedactKeyTail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestKeyPrefixPattern: a text that is a key's start matches, and a text past the key's shape does
// not.
func TestKeyPrefixPattern(t *testing.T) {
	for _, s := range []string{"tbk1.", "tbk1.ns", "tbk1.ns.", "tbk1.ns.bot", "tbk1.ns.bot.", "tbk1.ns.bot.A", "tbk1.ns.bot." + tail[:42]} {
		if !KeyPrefixPattern.MatchString(s) {
			t.Errorf("%q does not match, and it is a key's start", s)
		}
	}
	for _, s := range []string{"", "tbk", "tbk1", "x tbk1.ns.bot.A", "tbk1.ns.bot." + tail, "tbk1.Ns.bot.A", "tbk1.ns.bot.A!"} {
		if KeyPrefixPattern.MatchString(s) {
			t.Errorf("%q matches, and it is not a key's start", s)
		}
	}
}
