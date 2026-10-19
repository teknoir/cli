// Package valuepath implements the value-path syntax and the value coercion rules of
// `tnctl config set`. They are the same rules as the backend's (valuePaths.ts):
//
//   - a path is dot separated keys; "\." is a dot in a key, "\\" a backslash, "\=" an equals
//     sign (`nodeSelector.teknoir\.org/node-role`). There are no list indexes: lists are
//     set whole.
//   - `k.path=value` splits on the first unescaped "=". The text is read like a YAML 1.1
//     scalar (what Helm does): on/yes -> true, 010 -> 8, 1.10 -> 1.1, ~ and null -> null,
//     with a warning whenever the text did not survive; "k=" is the empty string; quotes,
//     [..] and {..} are YAML flow.
//   - keys that name text (tag, repository, registry, digest, version, name, image, host,
//     url, namespace) stay strings, and so does a value whose current value is a string.
package valuepath

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ParsePath splits a path into keys. "a.b\.c.d" is [a, "b.c", d].
func ParsePath(p string) ([]string, error) {
	if p == "" {
		return nil, errors.New("the path is empty")
	}
	var segs []string
	var cur strings.Builder
	rs := []rune(p)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case '\\':
			if i+1 >= len(rs) {
				return nil, fmt.Errorf("path %q ends with a lone backslash; write \\\\ for a backslash and \\. for a dot", p)
			}
			i++
			switch n := rs[i]; n {
			case '.', '\\', '=':
				cur.WriteRune(n)
			default:
				return nil, fmt.Errorf("path %q: \\%c is not an escape; only \\. \\\\ and \\= are", p, n)
			}
		case '.':
			if cur.Len() == 0 {
				return nil, fmt.Errorf("path %q has an empty key; write \\. for a dot inside a key", p)
			}
			segs = append(segs, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	if cur.Len() == 0 {
		return nil, fmt.Errorf("path %q has an empty key; write \\. for a dot inside a key", p)
	}
	segs = append(segs, cur.String())
	for _, s := range segs {
		if listIndex.MatchString(s) {
			return nil, fmt.Errorf("path %q: lists are replaced as a whole, there is no %q index; set the list with k.path='[...]' or with -f values.yaml", p, s[strings.LastIndex(s, "["):])
		}
	}
	return segs, nil
}

// A key ending in [n] is a Helm-style list index.
var listIndex = regexp.MustCompile(`\[[0-9]+\]$`)

// EscapePath is the inverse of ParsePath: the canonical text of a path.
func EscapePath(segs []string) string {
	out := make([]string, len(segs))
	for i, s := range segs {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `.`, `\.`)
		out[i] = s
	}
	return strings.Join(out, ".")
}

// SetIn sets path in obj, creating maps as needed. It fails instead of replacing a scalar
// or a list on the way: "image is a string, not a map".
func SetIn(obj map[string]any, path []string, value any) error {
	cur := obj
	for i, k := range path[:len(path)-1] {
		next, ok := cur[k]
		if !ok || next == nil {
			m := map[string]any{}
			cur[k] = m
			cur = m
			continue
		}
		m, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("%s is %s, not a map; cannot set %s inside it", EscapePath(path[:i+1]), typeName(next), EscapePath(path))
		}
		cur = m
	}
	cur[path[len(path)-1]] = value
	return nil
}

// GetIn returns the value at path.
func GetIn(obj map[string]any, path []string) (any, bool) {
	var cur any = obj
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[k]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// UnsetIn removes path from obj and prunes the maps it leaves empty. It reports whether
// the key was there.
func UnsetIn(obj map[string]any, path []string) bool {
	if len(path) == 0 {
		return false
	}
	if len(path) == 1 {
		_, ok := obj[path[0]]
		delete(obj, path[0])
		return ok
	}
	child, ok := obj[path[0]].(map[string]any)
	if !ok {
		return false
	}
	found := UnsetIn(child, path[1:])
	if len(child) == 0 {
		delete(obj, path[0])
	}
	return found
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a boolean"
	case int, int64, float64:
		return "a number"
	case []any:
		return "a list"
	case map[string]any:
		return "a map"
	}
	return fmt.Sprintf("%T", v)
}

// Assignment is one parsed `k.path=value`.
type Assignment struct {
	// Path is the canonical escaped path.
	Path     string
	Segments []string
	Value    any
	// Type is string, number, boolean, null, list or map.
	Type string
	// Warning is set when the text was read as something else than text.
	Warning string
}

// Options of ParseAssignment.
type Options struct {
	// String keeps every value as text (--string).
	String bool
	// Existing looks up the current effective value of a path; used to keep a string a
	// string. May be nil.
	Existing func(segs []string) (any, bool)
}

// SplitAssignment splits "k.path=value" at the first unescaped "=".
func SplitAssignment(arg string) (key, value string, err error) {
	rs := []rune(arg)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\\':
			i++ // the escaped rune is part of the key
		case '=':
			return string(rs[:i]), string(rs[i+1:]), nil
		}
	}
	return "", "", fmt.Errorf("%q is not k.path=value", arg)
}

// ParseAssignment parses `k.path=value`.
func ParseAssignment(arg string, opt Options) (Assignment, error) {
	key, text, err := SplitAssignment(arg)
	if err != nil {
		return Assignment{}, err
	}
	segs, err := ParsePath(key)
	if err != nil {
		return Assignment{}, err
	}
	a := Assignment{Path: EscapePath(segs), Segments: segs}
	var existing any
	var hasExisting bool
	if opt.Existing != nil {
		existing, hasExisting = opt.Existing(segs)
	}
	v, typ, warn, err := ParseValue(text, segs, opt.String, existing, hasExisting)
	if err != nil {
		return Assignment{}, fmt.Errorf("%s: %w", a.Path, err)
	}
	a.Value, a.Type = v, typ
	if warn != "" {
		a.Warning = fmt.Sprintf("%s=%s: %s", a.Path, text, warn)
	}
	return a, nil
}

// stringKeys are the key endings that always hold text.
var stringKeys = []string{"tag", "repository", "registry", "digest", "version", "name", "image", "host", "url", "namespace"}

// StringKey reports whether the last key names text, so numbers and booleans stay strings.
func StringKey(segs []string) bool {
	if len(segs) == 0 {
		return false
	}
	last := strings.ToLower(segs[len(segs)-1])
	for _, s := range stringKeys {
		if strings.HasSuffix(last, s) {
			return true
		}
	}
	return false
}

// ParseValue reads the text of a value. existing/hasExisting is the current value of the
// path, used to keep strings strings.
func ParseValue(text string, segs []string, forceString bool, existing any, hasExisting bool) (value any, typ, warning string, err error) {
	if forceString {
		return text, "string", "", nil
	}
	if text == "" {
		return "", "string", "", nil
	}
	switch text[0] {
	case '"', '\'':
		return parseQuoted(text)
	case '[', '{':
		return parseFlow(text)
	}
	if isNull(text) {
		return nil, "null", "", nil
	}
	v, kind := resolveScalar(text)
	switch kind {
	case "reject":
		return nil, "", "", fmt.Errorf("%q is not a usable number (NaN and Inf are not supported); use --string to keep the text", text)
	case "string":
		return text, "string", "", nil
	}
	// A number or a boolean. Keys that hold text, and strings that are already there, win.
	if StringKey(segs) {
		return text, "string", "", nil
	}
	if hasExisting {
		if _, ok := existing.(string); ok {
			return text, "string", "", nil
		}
	}
	if canonical(v) != text {
		warning = fmt.Sprintf("read as %s %s (YAML 1.1, like Helm); use --string to keep the text", kind, canonical(v))
	}
	return v, kind, warning, nil
}

func isNull(s string) bool {
	switch s {
	case "~", "null", "Null", "NULL":
		return true
	}
	return false
}

var (
	boolTrue  = map[string]bool{"y": true, "Y": true, "yes": true, "Yes": true, "YES": true, "true": true, "True": true, "TRUE": true, "on": true, "On": true, "ON": true}
	boolFalse = map[string]bool{"n": true, "N": true, "no": true, "No": true, "NO": true, "false": true, "False": true, "FALSE": true, "off": true, "Off": true, "OFF": true}

	reDec    = regexp.MustCompile(`^[-+]?(0|[1-9][0-9_]*)$`)
	reOct    = regexp.MustCompile(`^[-+]?0o?[0-7_]+$`)
	reHex    = regexp.MustCompile(`^[-+]?0x[0-9a-fA-F_]+$`)
	reBin    = regexp.MustCompile(`^[-+]?0b[01_]+$`)
	reFloat  = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9][0-9_]*(\.[0-9_]*)?)([eE][-+]?[0-9]+)?$`)
	reInfNaN = regexp.MustCompile(`^[-+]?\.(inf|Inf|INF)$|^\.(nan|NaN|NAN)$`)
)

// resolveScalar reads a plain (unquoted) scalar like YAML 1.1 / go-yaml v2 (Helm). kind is
// boolean, number, string or reject (NaN/Inf).
func resolveScalar(s string) (value any, kind string) {
	if boolTrue[s] {
		return true, "boolean"
	}
	if boolFalse[s] {
		return false, "boolean"
	}
	if reInfNaN.MatchString(s) {
		return nil, "reject"
	}
	clean := strings.ReplaceAll(s, "_", "")
	switch {
	case reDec.MatchString(s):
		if n, err := strconv.ParseInt(clean, 10, 64); err == nil {
			return n, "number"
		}
	case reHex.MatchString(s):
		if n, err := strconv.ParseInt(stripSign(clean, "0x"), 16, 64); err == nil {
			return applySign(clean, n), "number"
		}
	case reBin.MatchString(s):
		if n, err := strconv.ParseInt(stripSign(clean, "0b"), 2, 64); err == nil {
			return applySign(clean, n), "number"
		}
	case reOct.MatchString(s):
		body := stripSign(clean, "0o")
		if strings.HasPrefix(body, "0") {
			body = strings.TrimPrefix(body, "0")
		}
		if body == "" {
			body = "0"
		}
		if n, err := strconv.ParseInt(body, 8, 64); err == nil {
			return applySign(clean, n), "number"
		}
	}
	if !reDec.MatchString(s) && reFloat.MatchString(s) {
		if f, err := strconv.ParseFloat(clean, 64); err == nil && !math.IsInf(f, 0) {
			return f, "number"
		}
	}
	return s, "string"
}

// stripSign removes a leading sign and the radix prefix (0x, 0b, 0o) of a cleaned number.
func stripSign(s, prefix string) string {
	s = strings.TrimLeft(s, "+-")
	return strings.TrimPrefix(s, prefix)
}

func applySign(orig string, n int64) int64 {
	if strings.HasPrefix(orig, "-") {
		return -n
	}
	return n
}

// canonical is how a coerced value reads back, to detect text that changed.
func canonical(v any) string {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func parseQuoted(text string) (any, string, string, error) {
	var s string
	if err := yaml.Unmarshal([]byte(text), &s); err != nil {
		return nil, "", "", fmt.Errorf("cannot read the quoted text %s: %s", text, firstLine(err))
	}
	return s, "string", "", nil
}

func parseFlow(text string) (any, string, string, error) {
	var v any
	dec := yaml.NewDecoder(bytes.NewReader([]byte(text)))
	if err := dec.Decode(&v); err != nil {
		return nil, "", "", fmt.Errorf("cannot read %s as a YAML list or map: %s", shorten(text), firstLine(err))
	}
	v = Normalize(v)
	switch v.(type) {
	case []any:
		return v, "list", "", nil
	case map[string]any:
		return v, "map", "", nil
	}
	return nil, "", "", fmt.Errorf("%s is not a YAML list or map", shorten(text))
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func shorten(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}
	return s
}

// Normalize makes a decoded YAML value JSON friendly: map keys become strings.
func Normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = Normalize(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = Normalize(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = Normalize(e)
		}
		return x
	}
	return v
}
