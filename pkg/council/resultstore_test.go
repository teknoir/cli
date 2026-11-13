package council

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teknoir/cli/pkg/council/tools"
	"github.com/teknoir/cli/pkg/resultcut"
)

// deviceArray is a JSON array of 40 indented device objects of about 1.2 KiB: about 48 KiB, which
// is what a list_devices result of a busy teamspace looks like.
func deviceArray() string {
	items := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		items = append(items, fmt.Sprintf(`{"name":"device-%02d","state":"online","labels":{"team":"demo","site":"%s"}}`,
			i, strings.Repeat("x", 1150)))
	}
	return "[\n  " + strings.Join(items, ",\n  ") + "\n]"
}

// deviceServer is a real tools.Set whose list_devices answers deviceArray: the cut, the header and
// the store are then tested as the council meets them.
func deviceServer(t *testing.T, answer string) *tools.Set {
	t.Helper()
	s := mcp.NewServer(&mcp.Implementation{Name: "tnctl", Version: "test"}, nil)
	s.AddTool(&mcp.Tool{Name: "list_devices", Description: "The devices of the teamspace.", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: answer}}}, nil
		})
	set, err := tools.New(context.Background(), s)
	if err != nil {
		t.Fatalf("tools.New: %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

// memberHeading is the header of a partial result: its first line.
func memberHeading(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

// offsetOfHeader is the offset a header names in its read_result sentence.
var offsetOfHeader = regexp.MustCompile(`offset (\d+) gives the rest`)

// pageBody is the text of a read_result page: its first line says which bytes it is, and the rest
// is inside the untrusted block.
func pageBody(t *testing.T, text string) (head, body string) {
	t.Helper()
	i := strings.IndexByte(text, '\n')
	if i < 0 {
		t.Fatalf("the page has no line: %q", text)
	}
	head, rest := text[:i], text[i+1:]
	open := strings.Index(rest, ">\n")
	if !strings.HasPrefix(rest, `<untrusted source="tool:list_devices ref:`) || open < 0 {
		t.Fatalf("the page is not in the untrusted block: %q", text)
	}
	inner := rest[open+2:]
	if !strings.HasSuffix(inner, "\n</untrusted>") {
		t.Fatalf("the page does not end with the block's close: %q", text)
	}
	return head, strings.TrimSuffix(inner, "\n</untrusted>")
}

// A Fleet turn whose list_devices result is cut: Fleet sees a header that counts the items and
// names a reference, and read_result answers a page of the rest.
func TestFleetSeesAPartialResult(t *testing.T) {
	answer := deviceArray()
	work := newWork(kindMember, memberFleet, "which devices are online?")
	h := newHarness(t, work)
	h.cfg.Tools = deviceServer(t, answer)

	h.model.push(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_devices", input: map[string]any{}}),
		toolStep("tool_use", apiCall{id: "tu_2", name: toolReadResult, input: map[string]any{"ref": "r1", "offset": 0}}),
		textStep(reportJSON("the devices are online"), "end_turn"),
	)
	h.start(t)
	h.waitFinish(t)
	h.stop(t)

	fleet := h.model.requests(roleFleet)
	if len(fleet) != 3 {
		t.Fatalf("%d Fleet requests, want 3", len(fleet))
	}
	first := toolResults(fleet[1].Messages[len(fleet[1].Messages)-1])
	if len(first) != 1 {
		t.Fatalf("%d tool results in the first answer", len(first))
	}
	head := memberHeading(first[0])
	if !strings.Contains(head, "RESULT PARTIAL") || !strings.Contains(head, "of 40 items") || !strings.Contains(head, "ref r1") {
		t.Fatalf("the header %q does not count the items or name the reference", head)
	}
	if !strings.Contains(first[0], "<untrusted source=\"tool:list_devices\"") {
		t.Error("the result is not in the untrusted block")
	}

	// The page read_result gives back.
	paged := toolResults(fleet[2].Messages[len(fleet[2].Messages)-1])
	if len(paged) != 1 || !strings.HasPrefix(paged[0], "PAGE: bytes 0..") || !strings.Contains(paged[0], "(ref r1)") {
		t.Fatalf("the page is %q", paged)
	}
	if !strings.Contains(paged[0], "<untrusted source=\"tool:list_devices ref:r1\"") {
		t.Error("the page is not in the untrusted block of the result it pages")
	}

	// The shown part of the first result is valid JSON of whole elements, and a prefix of the answer.
	open := strings.Index(first[0], ">\n")
	shown := strings.TrimSuffix(first[0][open+2:], "\n</untrusted>")
	var elems []json.RawMessage
	if err := json.Unmarshal([]byte(shown), &elems); err != nil {
		t.Fatalf("the shown part is not a JSON array: %v in %q", err, head)
	}
	if len(elems) == 0 || len(elems) >= 40 {
		t.Fatalf("the shown part holds %d of 40 elements", len(elems))
	}
	if !strings.HasPrefix(answer, strings.TrimSuffix(shown, "\n]")) {
		t.Fatal("the shown part without its closing bracket is not a prefix of the answer")
	}
}

// The whole result is kept for the turn, and its pages join with the shown head into the text the
// tool returned, byte for byte.
func TestReadResultPagesReassemble(t *testing.T) {
	answer := deviceArray()
	turn := &turn{results: newResultStore(), d: &Daemon{cfg: Config{Tools: deviceServer(t, answer)}}}
	input, _ := json.Marshal(map[string]any{})
	res, err := turn.callTool(context.Background(), "read", toolUse{ID: "tu_1", Name: "list_devices", Input: input})
	if err != nil {
		t.Fatalf("callTool: %v", err)
	}
	text, _ := tools.Render("list_devices", res, turn.storeResult("list_devices", res))
	head := memberHeading(text)
	m := offsetOfHeader.FindStringSubmatch(head)
	if m == nil {
		t.Fatalf("the header %q names no offset", head)
	}
	offset, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	open := strings.Index(text, ">\n")
	shown := strings.TrimSuffix(text[open+2:], "\n</untrusted>")

	var pages []string
	for i := 0; i < 10; i++ {
		page := turn.readResult(json.RawMessage(fmt.Sprintf(`{"ref":"r1","offset":%d}`, offset)))
		if page.IsError {
			t.Fatalf("read_result at %d: %s", offset, page.Text)
		}
		hdr, pageText := pageBody(t, page.Text)
		if i == 0 {
			sum := sha256.Sum256([]byte(answer))
			if !strings.Contains(page.Text, `sha256="`+hex.EncodeToString(sum[:])+`"`) {
				t.Errorf("a page does not carry the sha256 of the whole answer: %q", page.Text[:min(len(page.Text), 200)])
			}
		}
		var from, to, total, nextOffset int
		line, err := fmt.Sscanf(hdr, "PAGE: bytes %d..%d of %d (ref r1); nextOffset %d", &from, &to, &total, &nextOffset)
		if line == 3 {
			// The last page ends with "; end" instead of a next offset.
			if _, err := fmt.Sscanf(hdr, "PAGE: bytes %d..%d of %d (ref r1); end", &from, &to, &total); err != nil || !strings.HasSuffix(hdr, "; end") {
				t.Fatalf("the last page's first line %q: %v", hdr, err)
			}
		} else if err != nil {
			t.Fatalf("a page's first line %q: %v", hdr, err)
		}
		if from != offset || to <= from || total != len(answer) {
			t.Fatalf("page %d says bytes %d..%d of %d, want %d.. of %d", i, from, to, total, offset, len(answer))
		}
		if pageText != answer[from:to] {
			t.Fatalf("page %d is not bytes %d..%d of the answer", i, from, to)
		}
		pages = append(pages, pageText)
		if strings.HasSuffix(hdr, "; end") {
			joined := strings.TrimSuffix(shown, "\n]") + strings.Join(pages, "")
			if joined != answer {
				t.Errorf("the shown head and the pages join into %d bytes, want the %d-byte answer", len(joined), len(answer))
			}
			return
		}
		offset = nextOffset
	}
	t.Fatal("the pages do not end")
}

// ---------- the store ----------

// The store keeps 16 MiB of cut results per turn; past it the model gets no reference, and its
// header says the rest is not kept.
func TestResultStoreCap(t *testing.T) {
	turn := &turn{results: newResultStore()}
	big := strings.Repeat("x", 1<<20)
	cut := tools.Result{Text: big, Full: big, Cut: resultcut.Result{Truncated: true, Shown: big[:100]}}
	for i := 0; i < maxTurnResults>>20; i++ {
		if ref := turn.storeResult("list_devices", cut); ref != fmt.Sprintf("r%d", i+1) {
			t.Fatalf("put %d gave the reference %q", i, ref)
		}
	}
	if ref := turn.storeResult("list_devices", cut); ref != "" {
		t.Errorf("the store kept a result past its %d-byte cap: %q", maxTurnResults, ref)
	}
	// What the model gets then says so.
	_, _ = turn, cut
	body := strings.Repeat("y", 200)
	r := tools.Result{Text: body, Full: body, Cut: resultcut.Result{}}
	r.Cut.Shown = body[:100]
	r.Cut.Truncated = true
	r.Truncated = true
	r.Cut.ShownBytes, r.Cut.TotalBytes = 100, len(body)
	r.Cut.Unit, r.Cut.TotalItems, r.Cut.ShownItems = "lines", 2, 1
	text, _ := tools.Render("list_devices", r, turn.storeResult("list_devices", r))
	if !strings.Contains(memberHeading(text), "the rest is not kept") {
		t.Errorf("the header %q does not say the rest is not kept", memberHeading(text))
	}
	if strings.Contains(text, "read_result") {
		t.Error("a result the store refused names read_result")
	}
}

// Fleet's tasks run in parallel goroutines of one turn: the store is safe to use from both, and
// each result gets its own reference. Run with -race.
func TestResultStoreConcurrent(t *testing.T) {
	turn := &turn{results: newResultStore()}
	full := strings.Repeat("z", 4000)
	if len(full) > maxTurnResults {
		t.Fatalf("the fixture is %d bytes, over the %d store", len(full), maxTurnResults)
	}
	var wg sync.WaitGroup
	refs := make(chan string, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := tools.Result{Text: full, Full: full, Cut: resultcut.Result{Truncated: true, Shown: full}}
			ref := turn.storeResult("list_devices", r)
			if ref == "" {
				t.Errorf("the store refused a %d-byte result", len(full))
			} else if _, ok := turn.results.get(ref); !ok {
				t.Errorf("the store lost %q", ref)
			}
			refs <- ref
		}()
	}
	wg.Wait()
	close(refs)
	seen := map[string]bool{}
	for ref := range refs {
		if seen[ref] {
			t.Errorf("the reference %q was given twice", ref)
		}
		seen[ref] = true
	}
	if len(seen) != 8 {
		t.Errorf("%d references, want 8", len(seen))
	}
}

// ---------- read_result's errors ----------

func TestReadResultErrors(t *testing.T) {
	turn := &turn{results: newResultStore()}
	full := "héllo wörld"
	ref, ok := turn.results.put("list_devices", full)
	if !ok {
		t.Fatal("put refused")
	}
	for _, tc := range []struct {
		name   string
		input  string
		expect string
	}{
		{"unknown ref", `{"ref":"r404","offset":0}`, "no result r404 in this turn"},
		{"below zero", `{"ref":"r1","offset":-1}`, "outside the result"},
		{"past the end", `{"ref":"r1","offset":999}`, "outside the result"},
		{"inside a character", `{"ref":"r1","offset":2}`, "nextOffset"},
		{"no offset", `{"ref":"r1"}`, `"ref"`},
		{"not JSON", `nope`, `"ref"`},
	} {
		res := turn.readResult(json.RawMessage(tc.input))
		if !res.IsError || !strings.Contains(res.Text, tc.expect) {
			t.Errorf("%s: %+v, want an error with %q", tc.name, res, tc.expect)
		}
	}
	// The offset the end of the result is at gives an empty page, not an error.
	res := turn.readResult(json.RawMessage(fmt.Sprintf(`{"ref":%q,"offset":%d}`, ref, len(full))))
	if res.IsError || !strings.Contains(res.Text, "PAGE: bytes") {
		t.Errorf("the end offset: %+v", res)
	}
}

// A page is at most maxPage bytes, ends on a rune boundary, and carries the sha256 of the whole
// text rather than of the page.
func TestReadResultPage(t *testing.T) {
	turn := &turn{results: newResultStore()}
	full := strings.Repeat("é", 10000)
	ref, _ := turn.results.put("list_devices", full)
	res := turn.readResult(json.RawMessage(fmt.Sprintf(`{"ref":%q,"offset":0}`, ref)))
	_, body := pageBody(t, res.Text)
	if len(body) > maxPage {
		t.Errorf("the page is %d bytes, want at most %d", len(body), maxPage)
	}
	if !strings.HasPrefix(full, body) {
		t.Error("the page is not the head of the result")
	}
	hdr := memberHeading(res.Text)
	if !strings.HasPrefix(hdr, "PAGE: bytes 0..") || !strings.Contains(hdr, "of 20000 (ref r1); nextOffset 12288") {
		t.Fatalf("the page's first line %q", hdr)
	}
	if n := strings.Count(res.Text, "sha256=\""); n != 1 {
		t.Errorf("the page carries %d sha256 attributes", n)
	}
	// The attribute is the hash of the whole text, not of the page: that is how a page is recognised
	// as part of the result it came from.
	sum := sha256.Sum256([]byte(full))
	if !strings.Contains(res.Text, `sha256="`+hex.EncodeToString(sum[:])+`"`) {
		t.Errorf("the page does not carry the sha256 of the whole result: %q", res.Text[:min(len(res.Text), 200)])
	}
}
