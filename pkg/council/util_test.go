package council

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEffortOf(t *testing.T) {
	for _, tc := range []struct {
		turn, def, want string
	}{
		{"low", "high", "low"},
		{"medium", "high", "medium"},
		{"high", "high", "high"},
		{"", "high", "high"},
		{"xhigh", "high", "high"},
		{"max", "high", "high"},
		{"bogus", "high", "high"},
		{"high", "low", "low"},
		{"medium", "low", "low"},
		{"low", "medium", "low"},
		{"", "medium", "medium"},
		{"xhigh", "medium", "medium"},
		{"high", "", "high"},
	} {
		if got := effortOf(tc.turn, tc.def); got != tc.want {
			t.Errorf("effortOf(%q, %q) = %q, want %q", tc.turn, tc.def, got, tc.want)
		}
	}
}

func TestCutUTF8(t *testing.T) {
	if got := cutUTF8("hello", 10, "…"); got != "hello" {
		t.Errorf("short string = %q, want it whole", got)
	}
	if got := cutUTF8("hello", 5, "…"); got != "hello" {
		t.Errorf("exact string = %q, want it whole", got)
	}
	if got := cutUTF8("hello", 3, "…"); got != "hel…" {
		t.Errorf("cut string = %q, want hel…", got)
	}
	// "é" is two bytes: a cut inside it drops it whole.
	if got := cutUTF8("aé", 2, "…"); got != "a…" {
		t.Errorf("cut on a UTF-8 boundary = %q, want a…", got)
	}
	if got := cutUTF8("abc", 0, "…"); got != "…" {
		t.Errorf("cut to nothing = %q, want …", got)
	}
	if got := cutUTF8("abc", -1, "…"); got != "…" {
		t.Errorf("negative cut = %q, want …", got)
	}
	if got := cutUTF8("hello", 3, ""); got != "hel" {
		t.Errorf("cut without a suffix = %q, want hel", got)
	}
}

func TestEventInput(t *testing.T) {
	// A small object is sent as the object itself.
	obj, ok := eventInput(json.RawMessage(`{"a":1}`)).(map[string]any)
	if !ok || obj["a"] != float64(1) {
		t.Errorf("small object = %#v", eventInput(json.RawMessage(`{"a":1}`)))
	}
	// A small non-object stays a string.
	if got, ok := eventInput(json.RawMessage(`[1,2]`)).(string); !ok || got != "[1,2]" {
		t.Errorf("array = %#v", eventInput(json.RawMessage(`[1,2]`)))
	}
	// A large object is a string of its first 2048 bytes.
	big := `{"a":"` + strings.Repeat("x", 4000) + `"}`
	s, ok := eventInput(json.RawMessage(big)).(string)
	if !ok {
		t.Fatalf("large object = %#v, want a string", eventInput(json.RawMessage(big)))
	}
	if len(s) != 2048+len("…") || !strings.HasSuffix(s, "…") {
		t.Errorf("large object = %d bytes, ending %q", len(s), s[len(s)-5:])
	}
	if !strings.HasPrefix(s, `{"a":"xxx`) {
		t.Errorf("large object does not start with the raw JSON: %q", s[:10])
	}
	// A cut inside a UTF-8 sequence drops the whole sequence.
	large := `{"a":"` + strings.Repeat("é", 2000) + `"}`
	cut, _ := eventInput(json.RawMessage(large)).(string)
	if !strings.HasPrefix(large, strings.TrimSuffix(cut, "…")) {
		t.Error("the cut string is not a prefix of the raw JSON")
	}
	if len(cut) > 2048+len("…") {
		t.Errorf("cut object = %d bytes, want at most %d", len(cut), 2048+len("…"))
	}
}
