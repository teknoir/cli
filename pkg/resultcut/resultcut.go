// Package resultcut cuts a long tool result into what fits, and says what it left out: how much of
// the text and how many of its items the model sees, and where the rest starts. A model that is
// told a result is partial can read the rest or narrow its query; one that is handed a silent cut
// infers the missing items instead.
package resultcut

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// minMax is the smallest cut allowed: below it the numbers in a header would be longer than the
// text shown.
const minMax = 64

// Result is a cut of one text.
type Result struct {
	// Shown is what fits in max bytes: valid JSON when Unit is "items" and the cut is at whole
	// elements, otherwise the head of the text.
	Shown string
	// Truncated reports that Shown is shorter than the text.
	Truncated bool
	// ShownBytes and TotalBytes are the sizes of Shown and of the whole text.
	ShownBytes int
	TotalBytes int
	// ShownItems and TotalItems count the array's elements, or the text's lines.
	ShownItems int
	TotalItems int
	// Unit is "items" for a JSON array cut and "lines" for a line cut.
	Unit string
	// Field is the object key whose array was cut; empty for a top-level array or a line cut.
	Field string
	// ContinueAt is the byte offset in the text where the unseen rest starts when Shown, without
	// its closing bracket, is a prefix of the text; -1 when Shown is a re-marshalled copy.
	ContinueAt int
}

// cutResult is what makes a Result: the parts every cut path sets, so that the paths only decide
// the text and the counts.
func cutResult(shown string, total int) Result {
	return Result{Shown: shown, Truncated: true, ShownBytes: len(shown), TotalBytes: total, ContinueAt: len(shown)}
}

// Cut cuts text to at most max bytes.
//
// A top-level JSON array is cut at whole elements and gets its closing bracket back, so Shown is
// valid JSON. A JSON object whose largest member is an array is re-marshalled with that array cut
// to whole elements, keeping the key order. Anything else is cut after its last newline when that
// keeps at least half of max, or at a rune boundary otherwise.
func Cut(text string, max int) Result {
	if max < minMax {
		max = minMax
	}
	if len(text) <= max {
		return whole(text)
	}
	if arr, ok := parseArray(text); ok {
		if r, ok := cutArray(text, arr, max); ok {
			return r
		}
		// Not even one element fits: the head of the text, but the counts still say how many
		// elements the whole text has.
		r := cutLines(text, max)
		r.TotalItems, r.Unit = len(arr.elements), "items"
		return r
	}
	if obj, ok := parseObject(text); ok {
		if r, ok := cutObject(text, obj, max); ok {
			return r
		}
	}
	return cutLines(text, max)
}

// whole is the result of a text that fits: nothing was cut, and every line and element is shown.
func whole(text string) Result {
	r := Result{Shown: text, ShownBytes: len(text), TotalBytes: len(text), ContinueAt: len(text)}
	if arr, ok := parseArray(text); ok {
		r.Unit, r.ShownItems, r.TotalItems = "items", len(arr.elements), len(arr.elements)
		return r
	}
	r.Unit = "lines"
	r.ShownItems = lineCount(text)
	r.TotalItems = r.ShownItems
	return r
}

// array is a top-level JSON array walked once: its elements, and where each one ends in the text.
type array struct {
	elements []json.RawMessage
	ends     []int
}

// parseArray walks text as one JSON array. It is false when text is not exactly one array.
func parseArray(text string) (array, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('[') {
		return array{}, false
	}
	var out array
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return array{}, false
		}
		out.elements = append(out.elements, raw)
		out.ends = append(out.ends, int(dec.InputOffset()))
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim(']') {
		return array{}, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return array{}, false
	}
	return out, true
}

// cutArray is the cut of a top-level array at the largest number of whole elements that fits, with
// the closing bracket the cut removed put back.
func cutArray(text string, arr array, max int) (Result, bool) {
	closing := "]"
	if strings.Contains(text, "\n") {
		// An indented array puts each element on its own line, and the bracket belongs there too.
		closing = "\n]"
	}
	k := 0
	for i, end := range arr.ends {
		if end+len(closing) > max {
			break
		}
		k = i + 1
	}
	if k == 0 {
		return Result{}, false
	}
	end := arr.ends[k-1]
	r := cutResult(text[:end]+closing, len(text))
	r.ShownItems, r.TotalItems, r.Unit, r.ContinueAt = k, len(arr.elements), "items", end
	return r, true
}

// object is a top-level JSON object walked in its key order, with the array to cut.
type object struct {
	keys []string
	vals map[string]json.RawMessage
	// key is the largest member, when that member is an array, and elements are its elements.
	key      string
	elements []json.RawMessage
}

// parseObject walks text as one JSON object whose largest member by raw bytes is an array.
func parseObject(text string) (object, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return object{}, false
	}
	out := object{vals: map[string]json.RawMessage{}}
	largest, largestLen := "", -1
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return object{}, false
		}
		key, ok := tok.(string)
		if !ok {
			return object{}, false
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return object{}, false
		}
		if _, dup := out.vals[key]; dup {
			// A repeated key has no place in a re-marshalled object.
			return object{}, false
		}
		out.keys = append(out.keys, key)
		out.vals[key] = raw
		if len(raw) > largestLen {
			largest, largestLen = key, len(raw)
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return object{}, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return object{}, false
	}
	arr, ok := parseArray(string(out.vals[largest]))
	if !ok {
		return object{}, false
	}
	out.key, out.elements = largest, arr.elements
	return out, true
}

// cutObject is the cut of an object at the largest number of whole elements of its array that
// fits, with the object re-marshalled around them.
func cutObject(text string, obj object, max int) (Result, bool) {
	indented := strings.Contains(text, "\n")
	lo, hi, best := 0, len(obj.elements), -1
	// The size grows with k, so the largest k that fits is found by bisection.
	for lo <= hi {
		mid := (lo + hi) / 2
		if len(build(obj, mid, indented)) <= max {
			best, lo = mid, mid+1
		} else {
			hi = mid - 1
		}
	}
	if best < 0 {
		return Result{}, false
	}
	shown := build(obj, best, indented)
	r := cutResult(shown, len(text))
	r.ShownItems, r.TotalItems, r.Unit, r.Field, r.ContinueAt = best, len(obj.elements), "items", obj.key, -1
	return r, true
}

// build is the object with its array cut to k elements: the key order of the original, two-space
// indentation when the original was indented, and no HTML escaping.
func build(obj object, k int, indented bool) string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, key := range obj.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		if indented {
			b.WriteString("\n  ")
		}
		b.Write(marshalKey(key))
		b.WriteByte(':')
		if indented {
			b.WriteByte(' ')
		}
		raw := obj.vals[key]
		if key == obj.key {
			raw = compactArray(obj.elements, k)
		}
		b.Write(indentValue(raw, indented))
	}
	if indented && len(obj.keys) > 0 {
		b.WriteByte('\n')
	}
	b.WriteByte('}')
	return b.String()
}

// marshalKey is a JSON key without HTML escaping: the escaped form would be longer than the key the
// original held, and a cut is not a place to add bytes.
func marshalKey(key string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(key); err != nil {
		return []byte(`""`)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// compactArray is elems[:k] as compact JSON.
func compactArray(elems []json.RawMessage, k int) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i := 0; i < k; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		var elem bytes.Buffer
		if err := json.Compact(&elem, elems[i]); err != nil {
			continue
		}
		b.Write(elem.Bytes())
	}
	b.WriteByte(']')
	return b.Bytes()
}

// indentValue is raw as it goes inside the object: compact, then indented two spaces below the
// object's own indentation. A value the decoder already read is valid JSON, so Compact cannot fail.
func indentValue(raw json.RawMessage, indented bool) []byte {
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return []byte("null")
	}
	if !indented {
		return compact.Bytes()
	}
	var out bytes.Buffer
	if err := json.Indent(&out, compact.Bytes(), "  ", "  "); err != nil {
		return compact.Bytes()
	}
	return out.Bytes()
}

// cutLines is the line cut: after the last newline at or before max when that keeps at least half
// of max, or at the last rune start at or before max otherwise.
func cutLines(text string, max int) Result {
	cut := max
	if i := strings.LastIndex(text[:max], "\n"); i+1 >= max/2 {
		cut = i + 1
	} else {
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
	}
	r := cutResult(text[:cut], len(text))
	r.ShownItems, r.TotalItems, r.Unit = strings.Count(text[:cut], "\n"), lineCount(text), "lines"
	return r
}

// lineCount is how many lines a text has: a trailing newline ends a line rather than starting one.
func lineCount(text string) int {
	if text == "" {
		return 0
	}
	n := strings.Count(text, "\n")
	if !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}
