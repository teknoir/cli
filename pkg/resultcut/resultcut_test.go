package resultcut

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// validJSON fails the test unless s parses as JSON.
func validJSON(t *testing.T, s string) {
	t.Helper()
	if !json.Valid([]byte(s)) {
		t.Fatalf("Shown is not valid JSON: %q", s)
	}
}

// decodeArray fails the test unless s is a JSON array, and returns its elements.
func decodeArray(t *testing.T, s string) []json.RawMessage {
	t.Helper()
	var out []json.RawMessage
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("Shown is not a JSON array: %v in %q", err, s)
	}
	return out
}

func TestCutFitsWhole(t *testing.T) {
	for _, tc := range []struct {
		text string
		unit string
		n    int
	}{
		{`[1,2,3]`, "items", 3},
		{"[\n  1,\n  2\n]", "items", 2},
		{"one\ntwo\nthree", "lines", 3},
		{"one\ntwo\n", "lines", 2},
		{`{"a":1}`, "lines", 1},
	} {
		r := Cut(tc.text, 4096)
		if r.Truncated || r.Shown != tc.text || r.TotalBytes != len(tc.text) || r.ShownBytes != len(tc.text) {
			t.Errorf("Cut(%q) = %+v, want the whole text", tc.text, r)
		}
		if r.Unit != tc.unit || r.TotalItems != tc.n || r.ShownItems != tc.n || r.Field != "" {
			t.Errorf("Cut(%q) = %+v, want %d %s", tc.text, r, tc.n, tc.unit)
		}
		if r.ContinueAt != len(tc.text) {
			t.Errorf("Cut(%q).ContinueAt = %d, want the end", tc.text, r.ContinueAt)
		}
	}
}

func TestCutSmallMaxIsRaised(t *testing.T) {
	text := strings.Repeat("x", 500)
	r := Cut(text, 4)
	if len(r.Shown) > minMax || len(r.Shown) == 0 {
		t.Errorf("Shown is %d bytes, want at least one and at most %d", len(r.Shown), minMax)
	}
	if r.Truncated != (len(text) > minMax) || r.TotalBytes != len(text) {
		t.Errorf("Cut = %+v", r)
	}
}

func TestCutIndentedArray(t *testing.T) {
	var items []string
	for i := 0; i < 40; i++ {
		items = append(items, fmt.Sprintf(`{"name":"device-%02d","state":"online"}`, i))
	}
	text := "[\n  " + strings.Join(items, ",\n  ") + "\n]"
	if len(text) <= 500 {
		t.Fatalf("the fixture is only %d bytes", len(text))
	}
	const max = 500
	r := Cut(text, max)
	if !r.Truncated || r.Unit != "items" || r.TotalItems != 40 || r.Field != "" {
		t.Fatalf("Cut = %+v", r)
	}
	if len(r.Shown) > max {
		t.Errorf("Shown is %d bytes, want at most %d", len(r.Shown), max)
	}
	if !strings.HasSuffix(r.Shown, "\n]") {
		t.Errorf("Shown does not end with its closing bracket: %q", tail(r.Shown))
	}
	validJSON(t, r.Shown)
	elems := decodeArray(t, r.Shown)
	if len(elems) != r.ShownItems || r.ShownItems == 0 || r.ShownItems >= 40 {
		t.Errorf("Shown holds %d elements, ShownItems = %d", len(elems), r.ShownItems)
	}
	// The unseen rest starts where the shown prefix ends, at the next element.
	if r.ContinueAt <= 0 || r.ContinueAt >= len(text) {
		t.Fatalf("ContinueAt = %d", r.ContinueAt)
	}
	if !strings.HasPrefix(text, strings.TrimSuffix(r.Shown, "\n]")) {
		t.Error("Shown without its closing bracket is not a prefix of the text")
	}
}

func TestCutCompactArray(t *testing.T) {
	var items []string
	for i := 0; i < 100; i++ {
		items = append(items, fmt.Sprintf(`{"n":%d}`, i))
	}
	text := "[" + strings.Join(items, ",") + "]"
	const max = 200
	r := Cut(text, max)
	if !r.Truncated || r.Unit != "items" || r.TotalItems != 100 {
		t.Fatalf("Cut = %+v", r)
	}
	if len(r.Shown) > max || !strings.HasSuffix(r.Shown, "]") || strings.Contains(r.Shown, "\n") {
		t.Errorf("Shown = %q", r.Shown)
	}
	validJSON(t, r.Shown)
	if got := len(decodeArray(t, r.Shown)); got != r.ShownItems {
		t.Errorf("Shown holds %d elements, ShownItems = %d", got, r.ShownItems)
	}
}

// A forty-element array of 1.2 KiB objects cut to 16 KiB: the shown part is valid JSON of whole
// elements, and the text from ContinueAt starts with the next element after the comma.
func TestCutArrayAtWholeElements(t *testing.T) {
	arr, ok := parseArray(bigArray(40, 1200))
	if !ok {
		t.Fatal("the fixture is not an array")
	}
	text := bigArray(40, 1200)
	r := Cut(text, 16<<10)
	if len(r.Shown) > 16<<10 {
		t.Fatalf("Shown is %d bytes", len(r.Shown))
	}
	validJSON(t, r.Shown)
	elems := decodeArray(t, r.Shown)
	if len(elems) != r.ShownItems || r.TotalItems != 40 || r.ShownItems == 0 || r.ShownItems >= 40 {
		t.Fatalf("Shown holds %d elements, ShownItems = %d of %d", len(elems), r.ShownItems, r.TotalItems)
	}
	// The shown prefix is the text up to the end of element k, and the rest starts there.
	if !strings.HasPrefix(text, strings.TrimSuffix(r.Shown, "\n]")) {
		t.Fatal("Shown without its closing bracket is not a prefix of the text")
	}
	rest := text[r.ContinueAt:]
	if !strings.HasPrefix(rest, ",") {
		t.Fatalf("the rest starts with %q, want the comma after the last shown element", first(rest, 20))
	}
	rest = strings.TrimLeft(rest[1:], " \n")
	next := strings.TrimLeft(string(arr.elements[r.ShownItems]), " \n")
	if !strings.HasPrefix(rest, next) {
		t.Errorf("the rest starts with %q, want element %d (%q)", first(rest, 40), r.ShownItems, first(next, 40))
	}
	// The elements shown are the first ShownItems of the original.
	for i, got := range elems {
		var a, b any
		if err := json.Unmarshal(got, &a); err != nil {
			t.Fatalf("element %d: %v", i, err)
		}
		if err := json.Unmarshal(arr.elements[i], &b); err != nil {
			t.Fatalf("element %d of the text: %v", i, err)
		}
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("element %d = %v, want %v", i, a, b)
		}
	}
}

// bigArray is count objects of about pad bytes, as an indented JSON array.
func bigArray(count, pad int) string {
	var items []string
	for i := 0; i < count; i++ {
		items = append(items, fmt.Sprintf(`{"name":"device-%02d","pad":"%s"}`, i, strings.Repeat("x", pad)))
	}
	return "[\n  " + strings.Join(items, ",\n  ") + "\n]"
}

func TestCutArrayNoElementFits(t *testing.T) {
	text := `[{"pad":"` + strings.Repeat("x", 500) + `"},{"pad":"y"}]`
	r := Cut(text, 100)
	if !r.Truncated || r.Unit != "items" || r.TotalItems != 2 || r.ShownItems != 0 {
		t.Fatalf("Cut = %+v", r)
	}
	if len(r.Shown) > 100 || !strings.HasPrefix(text, r.Shown) {
		t.Errorf("Shown is %d bytes and not a prefix: %q", len(r.Shown), r.Shown)
	}
	if r.ContinueAt != len(r.Shown) {
		t.Errorf("ContinueAt = %d, want %d", r.ContinueAt, len(r.Shown))
	}

	// An indented array whose first element alone is over the cap: not one whole element is shown,
	// and the counts say 0 items rather than counting the lines of the cut.
	keys := make([]string, 0, 400)
	for i := 0; i < 400; i++ {
		keys = append(keys, fmt.Sprintf(`"key%03d":%d`, i, i))
	}
	var ind bytes.Buffer
	if err := json.Indent(&ind, []byte(`[{`+strings.Join(keys, ",")+`},{"a":1}]`), "", "  "); err != nil {
		t.Fatal(err)
	}
	if ind.Len() <= 4096 {
		t.Fatalf("the indented fixture is only %d bytes", ind.Len())
	}
	r = Cut(ind.String(), 4096)
	if !r.Truncated || r.Unit != "items" {
		t.Fatalf("Cut = %+v", r)
	}
	if r.ShownItems != 0 || r.TotalItems != 2 {
		t.Errorf("ShownItems = %d of %d, want 0 of 2", r.ShownItems, r.TotalItems)
	}
}

func TestCutObjectTwoArrays(t *testing.T) {
	small := "[" + strings.Repeat(`{"a":1},`, 3) + `{"a":1}]`
	big := bigArray(20, 200)
	compact := `{"name":"<list>","items":` + small + `,"devices":` + big + `,"count":2}`
	// The platform's results arrive indented, which is what mcpResult does to them.
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(compact), "", "  "); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	const max = 700
	if len(text) <= max {
		t.Fatalf("the fixture is only %d bytes", len(text))
	}
	r := Cut(text, max)
	if !r.Truncated || r.Unit != "items" || r.Field != "devices" || r.ContinueAt != -1 {
		t.Fatalf("Cut = %+v", r)
	}
	if r.TotalItems != 20 || r.ShownItems >= 20 || r.ShownItems < 0 {
		t.Errorf("ShownItems = %d of %d", r.ShownItems, r.TotalItems)
	}
	if len(r.Shown) > max {
		t.Errorf("Shown is %d bytes, want at most %d", len(r.Shown), max)
	}
	validJSON(t, r.Shown)
	if !strings.Contains(r.Shown, "\n  \"name\": \"<list>\"") {
		t.Errorf("the cut object is not indented two spaces without HTML escaping: %q", first(r.Shown, 120))
	}
	var got struct {
		Name    string            `json:"name"`
		Items   []json.RawMessage `json:"items"`
		Devices []json.RawMessage `json:"devices"`
		Count   int               `json:"count"`
	}
	if err := json.Unmarshal([]byte(r.Shown), &got); err != nil {
		t.Fatalf("Shown does not parse: %v in %q", err, r.Shown)
	}
	if got.Name != "<list>" || got.Count != 2 || len(got.Items) != 4 || len(got.Devices) != r.ShownItems {
		t.Errorf("Shown = %+v", got)
	}
	// The key order of the original is kept.
	iName := strings.Index(r.Shown, `"name"`)
	iItems := strings.Index(r.Shown, `"items"`)
	iDevices := strings.Index(r.Shown, `"devices"`)
	iCount := strings.Index(r.Shown, `"count"`)
	if iName >= iItems || iItems >= iDevices || iDevices >= iCount {
		t.Errorf("the key order changed: %d %d %d %d", iName, iItems, iDevices, iCount)
	}
}

func TestCutObjectLargestIsNotAnArray(t *testing.T) {
	text := `{"text":"` + strings.Repeat("z", 5000) + `","list":[1,2,3]}`
	r := Cut(text, 200)
	if r.Unit != "lines" || r.Field != "" || r.Truncated != true {
		t.Fatalf("Cut = %+v, want the line cut", r)
	}
	if len(r.Shown) > 200 || !strings.HasPrefix(text, r.Shown) {
		t.Errorf("Shown is %d bytes and not a prefix", len(r.Shown))
	}
}

func TestCutLineText(t *testing.T) {
	var b strings.Builder
	for i := 0; b.Len() < 4000; i++ {
		fmt.Fprintf(&b, "line %03d of the log\n", i)
	}
	text := b.String()
	const max = 1000
	r := Cut(text, max)
	if !r.Truncated || r.Unit != "lines" || r.Field != "" {
		t.Fatalf("Cut = %+v", r)
	}
	if !strings.HasSuffix(r.Shown, "\n") || len(r.Shown) > max {
		t.Errorf("the line cut does not end at a newline within the cap: %d bytes", len(r.Shown))
	}
	if r.ShownItems != strings.Count(r.Shown, "\n") || r.TotalItems != strings.Count(text, "\n") {
		t.Errorf("ShownItems = %d, TotalItems = %d", r.ShownItems, r.TotalItems)
	}
	if r.ContinueAt != len(r.Shown) {
		t.Errorf("ContinueAt = %d, want %d", r.ContinueAt, len(r.Shown))
	}
}

// A text with no newline near the cap, and a trailing newline that does not start a line.
func TestCutLineTextAtRune(t *testing.T) {
	text := `{"log":"` + strings.Repeat("é", 600) + `"}` + "\n"
	r := Cut(text, 101)
	if !r.Truncated || r.Unit != "lines" {
		t.Fatalf("Cut = %+v", r)
	}
	if len(r.Shown) > 101 || !utf8.ValidString(r.Shown) || !strings.HasPrefix(text, r.Shown) {
		t.Errorf("Shown = %d bytes, valid %v", len(r.Shown), utf8.ValidString(r.Shown))
	}
	if lineCount("a\nb\n") != 2 || lineCount("a\nb") != 2 || lineCount("") != 0 {
		t.Errorf("lineCount %d %d %d", lineCount("a\nb\n"), lineCount("a\nb"), lineCount(""))
	}
}

func TestCutUTF8AtTheCap(t *testing.T) {
	// A three-byte character straddling the cap must be dropped whole.
	text := strings.Repeat("あ", 500)
	r := Cut(text, 100)
	if !utf8.ValidString(r.Shown) || len(r.Shown) > 100 {
		t.Errorf("Shown is %d bytes and valid %v", len(r.Shown), utf8.ValidString(r.Shown))
	}
	if !strings.HasPrefix(text, r.Shown) {
		t.Error("Shown is not a prefix of the text")
	}
	// The same at a newline the rune cut cannot use.
	r = Cut("é\n"+text, 65)
	if !utf8.ValidString(r.Shown) || len(r.Shown) > 65 {
		t.Errorf("Shown is %d bytes and valid %v", len(r.Shown), utf8.ValidString(r.Shown))
	}
}

// A property over random arrays: Shown is valid JSON within the cap, and holds exactly the first
// ShownItems elements of the text.
func TestCutRandomArrays(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for round := 0; round < 200; round++ {
		n := 1 + rnd.Intn(30)
		elems := make([]json.RawMessage, n)
		var b strings.Builder
		b.WriteByte('[')
		for i := 0; i < n; i++ {
			raw := fmt.Sprintf(`{"i":%d,"s":%q}`, i, strings.Repeat("x", rnd.Intn(40)))
			elems[i] = json.RawMessage(raw)
			if i > 0 {
				b.WriteByte(',')
			}
			if rnd.Intn(2) == 0 {
				b.WriteString("\n  ")
			}
			b.WriteString(raw)
		}
		b.WriteByte(']')
		text := b.String()
		if len(text) <= 200 {
			continue
		}
		// Large enough that at least one element fits, so the cut is at whole elements, and
		// smaller than the text, so something is cut.
		max := 200 + rnd.Intn(len(text))
		if max >= len(text) {
			max = len(text) - 1
		}
		r := Cut(text, max)
		if !r.Truncated || len(r.Shown) > max {
			t.Fatalf("round %d: Cut(%d bytes, %d) = %d bytes, truncated %v", round, len(text), max, len(r.Shown), r.Truncated)
		}
		if r.Unit != "items" || r.ContinueAt < 0 || r.ShownItems < 1 || r.ShownItems > n {
			t.Fatalf("round %d: Cut = %+v", round, r)
		}
		validJSON(t, r.Shown)
		got := decodeArray(t, r.Shown)
		if len(got) != r.ShownItems {
			t.Fatalf("round %d: Shown holds %d elements, ShownItems = %d", round, len(got), r.ShownItems)
		}
		for i := range got {
			var a, b any
			if err := json.Unmarshal(got[i], &a); err != nil {
				t.Fatalf("round %d element %d: %v", round, i, err)
			}
			if err := json.Unmarshal(elems[i], &b); err != nil {
				t.Fatalf("round %d element %d of the text: %v", round, i, err)
			}
			if fmt.Sprint(a) != fmt.Sprint(b) {
				t.Fatalf("round %d: element %d = %v, want %v", round, i, a, b)
			}
		}
	}
}

// The trailer of a line cut reads back as the numbers the cut wrote.
func TestLineCutTrailerRoundTrip(t *testing.T) {
	for _, r := range []Result{
		{Truncated: true, ShownBytes: 14798, TotalBytes: 48123, ShownItems: 12, TotalItems: 40, Unit: "items"},
		{Truncated: true, ShownBytes: 1000, TotalBytes: 4000, ShownItems: 0, TotalItems: 2, Unit: "items"},
		{Truncated: true, ShownBytes: 900, TotalBytes: 3000, ShownItems: 9, TotalItems: 30, Unit: "lines"},
	} {
		text := "the head of the text" + LineCutTrailer(r)
		got, ok := ParseLineCutTrailer(text)
		if !ok {
			t.Fatalf("the trailer %q is not recognised", LineCutTrailer(r))
		}
		if got.ShownBytes != r.ShownBytes || got.TotalBytes != r.TotalBytes ||
			got.ShownItems != r.ShownItems || got.TotalItems != r.TotalItems || got.Unit != r.Unit || !got.Truncated {
			t.Errorf("parsed %+v, want %+v", got, r)
		}
	}
	// A text that does not end with the trailer is not a cut.
	for _, text := range []string{"", "shown 1 of 2 bytes", "… truncated: shown 1 of 2 bytes, 1 of 2 lines", LineCutTrailer(Result{Unit: "lines"}) + "\n"} {
		if _, ok := ParseLineCutTrailer(text); ok {
			t.Errorf("%q was read as a cut", text)
		}
	}
}

// tail is the last bytes of s, for a failure message.
func tail(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[len(s)-40:]
}

// first is the first n bytes of s.
func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
