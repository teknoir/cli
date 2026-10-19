package valuepath

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePath(t *testing.T) {
	ok := []struct {
		in   string
		want []string
	}{
		{"a", []string{"a"}},
		{"a.b.c", []string{"a", "b", "c"}},
		{`a.b\.c.d`, []string{"a", "b.c", "d"}},
		{`a\\.b`, []string{`a\`, "b"}},
		{`nodeSelector.teknoir\.org/node-role`, []string{"nodeSelector", "teknoir.org/node-role"}},
		{`a\=b`, []string{"a=b"}},
		{"-dash.key", []string{"-dash", "key"}},
		{"has[bracket", []string{"has[bracket"}},
		{"has]bracket", []string{"has]bracket"}},
	}
	for _, c := range ok {
		got, err := ParsePath(c.in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParsePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	bad := map[string]string{
		"":          "empty",
		"a..b":      "empty key",
		".a":        "empty key",
		"a.":        "empty key",
		`a\`:        "lone backslash",
		`a\x`:       "not an escape",
		"a[0].b":    "lists are replaced as a whole",
		"items[12]": "lists are replaced as a whole",
	}
	for in, frag := range bad {
		_, err := ParsePath(in)
		if err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("ParsePath(%q) error = %v, want one containing %q", in, err, frag)
		}
	}
}

func TestEscapeRoundTrip(t *testing.T) {
	for _, p := range []string{
		`a.b.c`, `nodeSelector.teknoir\.org/node-role`, `a\\.b`, `x.y\.z\\\.w`, `k8s\.io.name`,
	} {
		segs, err := ParsePath(p)
		if err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		if got := EscapePath(segs); got != p {
			t.Errorf("EscapePath(ParsePath(%q)) = %q", p, got)
		}
	}
}

func TestSplitAssignment(t *testing.T) {
	cases := []struct{ in, key, val string }{
		{"a=b", "a", "b"},
		{"a=x=y", "a", "x=y"},
		{"a=", "a", ""},
		{`a\=b=1`, `a\=b`, "1"},
		{`a\\=1`, `a\\`, "1"},
		{"a.b=--flag", "a.b", "--flag"},
	}
	for _, c := range cases {
		k, v, err := SplitAssignment(c.in)
		if err != nil || k != c.key || v != c.val {
			t.Errorf("SplitAssignment(%q) = %q, %q, %v; want %q, %q", c.in, k, v, err, c.key, c.val)
		}
	}
	if _, _, err := SplitAssignment("novalue"); err == nil {
		t.Error("expected an error without =")
	}
	if _, _, err := SplitAssignment(`a\=b`); err == nil {
		t.Error("an escaped = is not a separator")
	}
}

func TestParseAssignment(t *testing.T) {
	type want struct {
		path    string
		value   any
		typ     string
		warning string // fragment, "" = none
	}
	cases := []struct {
		in  string
		opt Options
		w   want
	}{
		{"a=true", Options{}, want{"a", true, "boolean", ""}},
		{"a=false", Options{}, want{"a", false, "boolean", ""}},
		{"a=on", Options{}, want{"a", true, "boolean", "read as boolean true"}},
		{"a=No", Options{}, want{"a", false, "boolean", "read as boolean false"}},
		{"a=y", Options{}, want{"a", true, "boolean", "read as boolean true"}},
		{"a=5", Options{}, want{"a", int64(5), "number", ""}},
		{"a=-5", Options{}, want{"a", int64(-5), "number", ""}},
		{"a=010", Options{}, want{"a", int64(8), "number", "read as number 8"}},
		{"a=0o17", Options{}, want{"a", int64(15), "number", "read as number 15"}},
		{"a=0x10", Options{}, want{"a", int64(16), "number", "read as number 16"}},
		{"a=0b101", Options{}, want{"a", int64(5), "number", "read as number 5"}},
		{"a=1_000", Options{}, want{"a", int64(1000), "number", "read as number 1000"}},
		{"a=1.10", Options{}, want{"a", 1.1, "number", "read as number 1.1"}},
		{"a=1.5", Options{}, want{"a", 1.5, "number", ""}},
		{"a=1e3", Options{}, want{"a", 1000.0, "number", "read as number 1000"}},
		{"a=09", Options{}, want{"a", 9.0, "number", "read as number 9"}},
		{"a=", Options{}, want{"a", "", "string", ""}},
		{"a=null", Options{}, want{"a", nil, "null", ""}},
		{"a=~", Options{}, want{"a", nil, "null", ""}},
		{"a=NULL", Options{}, want{"a", nil, "null", ""}},
		{`a="null"`, Options{}, want{"a", "null", "string", ""}},
		{`a='1.10'`, Options{}, want{"a", "1.10", "string", ""}},
		{`a="has spaces"`, Options{}, want{"a", "has spaces", "string", ""}},
		{"a=x=y", Options{}, want{"a", "x=y", "string", ""}},
		{`a\=b=1`, Options{}, want{"a=b", int64(1), "number", ""}},
		{"a=hello world", Options{}, want{"a", "hello world", "string", ""}},
		{"a=2026-10-19", Options{}, want{"a", "2026-10-19", "string", ""}},
		{"a=1:30", Options{}, want{"a", "1:30", "string", ""}},
		{"a=v1.2.3", Options{}, want{"a", "v1.2.3", "string", ""}},
		{"a=[1,2]", Options{}, want{"a", []any{1, 2}, "list", ""}},
		{"a={b: 1}", Options{}, want{"a", map[string]any{"b": 1}, "map", ""}},
		{"a=[]", Options{}, want{"a", []any{}, "list", ""}},
		{"a=[{key: a, operator: Exists}]", Options{}, want{"a", []any{map[string]any{"key": "a", "operator": "Exists"}}, "list", ""}},
		{`nodeSelector.teknoir\.org/node-role=vision`, Options{}, want{`nodeSelector.teknoir\.org/node-role`, "vision", "string", ""}},
		// --string
		{"a=1", Options{String: true}, want{"a", "1", "string", ""}},
		{"a=null", Options{String: true}, want{"a", "null", "string", ""}},
		{"a=[1,2]", Options{String: true}, want{"a", "[1,2]", "string", ""}},
		// keys that hold text
		{"image.tag=1.10", Options{}, want{"image.tag", "1.10", "string", ""}},
		{"image.tag=20261019", Options{}, want{"image.tag", "20261019", "string", ""}},
		{"image.tag=010", Options{}, want{"image.tag", "010", "string", ""}},
		{"image.repository=on", Options{}, want{"image.repository", "on", "string", ""}},
		{"app.version=2", Options{}, want{"app.version", "2", "string", ""}},
		{"imageTag=7", Options{}, want{"imageTag", "7", "string", ""}},
		{"db.host=1", Options{}, want{"db.host", "1", "string", ""}},
		{"image.tag=null", Options{}, want{"image.tag", nil, "null", ""}},
		// an existing string stays a string, an existing number does not matter
		{"port=5", Options{Existing: func([]string) (any, bool) { return "80", true }}, want{"port", "5", "string", ""}},
		{"port=5", Options{Existing: func([]string) (any, bool) { return 80.0, true }}, want{"port", int64(5), "number", ""}},
		{"port=5", Options{Existing: func([]string) (any, bool) { return nil, false }}, want{"port", int64(5), "number", ""}},
	}
	for _, c := range cases {
		got, err := ParseAssignment(c.in, c.opt)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Path != c.w.path || got.Type != c.w.typ || !sameValue(got.Value, c.w.value) {
			t.Errorf("%q = path %q value %#v type %s; want %q %#v %s", c.in, got.Path, got.Value, got.Type, c.w.path, c.w.value, c.w.typ)
		}
		if c.w.warning == "" && got.Warning != "" {
			t.Errorf("%q: unexpected warning %q", c.in, got.Warning)
		}
		if c.w.warning != "" && !strings.Contains(got.Warning, c.w.warning) {
			t.Errorf("%q: warning %q does not contain %q", c.in, got.Warning, c.w.warning)
		}
		if c.w.warning != "" && !strings.Contains(got.Warning, "--string") {
			t.Errorf("%q: the warning should name --string: %q", c.in, got.Warning)
		}
	}
}

// sameValue compares decoded values; YAML flow numbers are ints of any width.
func sameValue(a, b any) bool {
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !sameValue(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k := range x {
			if !sameValue(x[k], y[k]) {
				return false
			}
		}
		return true
	case int:
		switch y := b.(type) {
		case int:
			return x == y
		case int64:
			return int64(x) == y
		}
		return false
	}
	return reflect.DeepEqual(a, b)
}

func TestParseAssignmentErrors(t *testing.T) {
	cases := map[string]string{
		"novalue":     "not k.path=value",
		"a..b=1":      "empty key",
		"items[0]=x":  "lists are replaced as a whole",
		"a=.inf":      "NaN and Inf",
		"a=-.Inf":     "NaN and Inf",
		"a=.nan":      "NaN and Inf",
		`a="unclosed`: "cannot read the quoted text",
		"a=[1, 2":     "cannot read",
		`=value`:      "empty",
	}
	for in, frag := range cases {
		_, err := ParseAssignment(in, Options{})
		if err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%q: error = %v, want one containing %q", in, err, frag)
		}
	}
	// --string makes NaN fine
	if a, err := ParseAssignment("a=.nan", Options{String: true}); err != nil || a.Value != ".nan" {
		t.Errorf("--string .nan = %v, %v", a.Value, err)
	}
}

func TestSetUnsetGet(t *testing.T) {
	obj := map[string]any{}
	if err := SetIn(obj, []string{"image", "tag"}, "pr-1"); err != nil {
		t.Fatal(err)
	}
	if err := SetIn(obj, []string{"image", "pullPolicy"}, "Always"); err != nil {
		t.Fatal(err)
	}
	if err := SetIn(obj, []string{"a", "b", "c"}, int64(1)); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"image": map[string]any{"tag": "pr-1", "pullPolicy": "Always"},
		"a":     map[string]any{"b": map[string]any{"c": int64(1)}},
	}
	if !reflect.DeepEqual(obj, want) {
		t.Fatalf("got %#v", obj)
	}
	if v, ok := GetIn(obj, []string{"image", "tag"}); !ok || v != "pr-1" {
		t.Errorf("GetIn = %v %v", v, ok)
	}
	if _, ok := GetIn(obj, []string{"image", "tag", "x"}); ok {
		t.Error("GetIn through a scalar must fail")
	}

	// cannot traverse a scalar or a list
	err := SetIn(obj, []string{"image", "tag", "x"}, 1)
	if err == nil || !strings.Contains(err.Error(), "image.tag is a string, not a map") {
		t.Errorf("error = %v", err)
	}
	obj["l"] = []any{1}
	if err := SetIn(obj, []string{"l", "x"}, 1); err == nil || !strings.Contains(err.Error(), "a list, not a map") {
		t.Errorf("error = %v", err)
	}
	// null is replaced by a map
	obj["n"] = nil
	if err := SetIn(obj, []string{"n", "x"}, 1); err != nil {
		t.Errorf("null should become a map: %v", err)
	}

	// unset prunes empty parents
	if !UnsetIn(obj, []string{"a", "b", "c"}) {
		t.Error("UnsetIn found nothing")
	}
	if _, ok := obj["a"]; ok {
		t.Errorf("empty parents must be pruned: %#v", obj)
	}
	if UnsetIn(obj, []string{"a", "b", "c"}) {
		t.Error("second UnsetIn must report not found")
	}
	if !UnsetIn(obj, []string{"image", "tag"}) || obj["image"].(map[string]any)["pullPolicy"] != "Always" {
		t.Errorf("sibling kept: %#v", obj)
	}
}

func TestStringKey(t *testing.T) {
	for _, k := range []string{"tag", "image.tag", "Repository", "myRegistry", "digest", "appVersion", "name", "hostname", "image", "url", "namespace", "targetNamespace"} {
		segs := strings.Split(k, ".")
		if !StringKey(segs) {
			t.Errorf("%q should be a string key", k)
		}
	}
	for _, k := range []string{"replicas", "port", "enabled", "resources.limits.cpu", "timeout"} {
		if StringKey(strings.Split(k, ".")) {
			t.Errorf("%q should not be a string key", k)
		}
	}
	if StringKey(nil) {
		t.Error("empty path")
	}
}

func TestFlowValuesReadYAML11Booleans(t *testing.T) {
	// yaml.v3 reads yes/on as text; Helm (YAML 1.1) reads them as booleans, like a plain k=yes.
	a, err := ParseAssignment("a={enabled: yes, off: 'no', quoted: \"on\", list: [on, n, text], n: 3}", Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"enabled": true, "off": "no", "quoted": "on", "list": []any{true, false, "text"}, "n": 3}
	if !reflect.DeepEqual(a.Value, want) {
		t.Errorf("got %#v", a.Value)
	}
	b, err := ParseAssignment("a=yes", Options{})
	if err != nil || b.Value != true {
		t.Errorf("plain yes: %#v %v", b.Value, err)
	}
}

func TestApplyToYAMLKeepsWhatItDoesNotTouch(t *testing.T) {
	in := "# values for the notebook\nenabled: yes\nmode: off\nimage:\n  tag: main  # pinned\nlist: [on, off]\n"
	asg := func(args ...string) []Assignment {
		var out []Assignment
		for _, arg := range args {
			a, err := ParseAssignment(arg, Options{})
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
		return out
	}
	out, err := ApplyToYAML([]byte(in), asg("image.tag=pr-1", "replicas=2", "a.b.c=x", "gone=null", "debug=yes"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, frag := range []string{"# values for the notebook", "enabled: yes\n", "mode: off\n", "list: [on, off]", "tag: pr-1", "replicas: 2", "debug: true", "gone: null", "c: x"} {
		if !strings.Contains(got, frag) {
			t.Errorf("missing %q in\n%s", frag, got)
		}
	}
	if strings.Contains(got, `"yes"`) || strings.Contains(got, `"off"`) {
		t.Errorf("untouched scalars must not be quoted:\n%s", got)
	}
	// a scalar on the way is refused, like SetIn
	if _, err := ApplyToYAML([]byte(in), asg("mode.x=1")); err == nil || !strings.Contains(err.Error(), "mode is a boolean, not a map") {
		t.Errorf("error = %v", err)
	}
	if _, err := ApplyToYAML([]byte(in), asg("list.x=1")); err == nil || !strings.Contains(err.Error(), "a list") {
		t.Errorf("error = %v", err)
	}
	// empty and comment-only documents start as an empty map
	for _, doc := range []string{"", "# nothing yet\n", "null\n"} {
		out, err := ApplyToYAML([]byte(doc), asg("a.b=1"))
		if err != nil || !strings.Contains(string(out), "a:\n  b: 1") {
			t.Errorf("%q: %v %q", doc, err, out)
		}
	}
}
