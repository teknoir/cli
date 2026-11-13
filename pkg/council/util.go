// Package council is the TeknoirBot council: the members of one chat turn, the model calls they make,
// the tools they run and the events they post.
//
// The turn's orchestration lives in the daemon; this package holds the pieces it is built from: the
// turn budget, the merger of a member's streamed text, the prompts, the council's own tools, the
// members' reports and the small helpers they share.
package council

import "encoding/json"

// effortOf is min(def, turn) with low < medium < high. A turn effort that is empty or not low,
// medium or high ("xhigh", "max", anything else) counts as high.
func effortOf(turn, def string) string {
	if effortRank(turn) <= effortRank(def) {
		return effortName(effortRank(turn))
	}
	return effortName(effortRank(def))
}

// effortRank orders the efforts, and puts everything else at high's rank.
func effortRank(effort string) int {
	switch effort {
	case "low":
		return 0
	case "medium":
		return 1
	}
	return 2
}

// effortName is the effort at rank.
func effortName(rank int) string {
	switch rank {
	case 0:
		return "low"
	case 1:
		return "medium"
	}
	return "high"
}

// cutUTF8 cuts s to at most n bytes on a UTF-8 boundary; when it cut, it appends suffix (the suffix
// is not counted in n).
func cutUTF8(s string, n int, suffix string) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	return s[:i] + suffix
}

// eventInput is a tool input for a tool_call event: the decoded JSON object when the raw JSON is at
// most 2048 bytes and decodes to an object, else a string of its first 2048 bytes (UTF-8 safe) plus
// "…".
func eventInput(raw json.RawMessage) any {
	if len(raw) <= 2048 {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			if obj, ok := v.(map[string]any); ok {
				return obj
			}
		}
	}
	return cutUTF8(string(raw), 2048, "…")
}
