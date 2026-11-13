package council

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/pkg/council/tools"
)

// maxTurnResults is the most whole tool results one turn keeps: past it the model is told the rest
// is gone rather than being given a reference that reads nothing.
const maxTurnResults = 16 << 20

// maxPage is the most of a result read_result gives in one page.
const maxPage = 12288

// toolResultPage is read_result's page of one stored result.
type toolResultPage struct {
	// Tool is the tool whose result it is.
	Tool string
	// Full is the whole text the tool returned.
	Full string
}

// resultStore is the turn's whole tool results, by reference: a result the model only saw a cut of
// is kept here so it can page the rest. Fleet's tasks run in parallel, so every access holds the
// mutex, and the store dies with the turn.
type resultStore struct {
	mu    sync.Mutex
	byRef map[string]toolResultPage
	count int
	bytes int
}

// newResultStore is an empty store.
func newResultStore() *resultStore {
	return &resultStore{byRef: map[string]toolResultPage{}}
}

// put keeps full for tool and returns its reference. ok is false when the store is full: the model
// then gets no reference, and its header says the rest is not kept.
func (s *resultStore) put(tool, full string) (ref string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bytes+len(full) > maxTurnResults {
		return "", false
	}
	s.count++
	s.bytes += len(full)
	ref = "r" + strconv.Itoa(s.count)
	s.byRef[ref] = toolResultPage{Tool: tool, Full: full}
	return ref, true
}

// get is the result ref names.
func (s *resultStore) get(ref string) (toolResultPage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.byRef[ref]
	return p, ok
}

// readResultTool is the read_result tool: the rest of a tool result the model saw a cut of.
func readResultTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:        toolReadResult,
		Description: anthropic.String("Read more of a tool result that came back RESULT PARTIAL in this turn: up to 12288 bytes of the whole result from offset."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"ref":    map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
			},
			Required: []string{"ref", "offset"},
			ExtraFields: map[string]any{
				"additionalProperties": false,
			},
		},
		Strict: anthropic.Bool(true),
	}}
}

// readResult answers one read_result call: a page of a stored result, in the untrusted block, after
// a line that says which bytes it is and where the next page starts. A bad reference or offset is
// an error result, so the model can ask for another page.
func (t *turn) readResult(input json.RawMessage) tools.Result {
	var in struct {
		Ref    string `json:"ref"`
		Offset *int   `json:"offset"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.Ref == "" || in.Offset == nil {
		return readResultError(`give {"ref": "r1", "offset": 0}`)
	}
	page, ok := t.results.get(in.Ref)
	if !ok {
		return readResultError("no result " + cutUTF8(in.Ref, 64, "…") + " in this turn")
	}
	offset := *in.Offset
	full := page.Full
	switch {
	case offset < 0 || offset > len(full):
		return readResultError(fmt.Sprintf("offset %d is outside the result: its bytes are 0..%d", offset, len(full)))
	case offset < len(full) && !utf8.RuneStart(full[offset]):
		return readResultError(fmt.Sprintf("offset %d is inside a character: use a rune boundary, or the nextOffset a page gave", offset))
	}
	end := offset + maxPage
	if end > len(full) {
		end = len(full)
	}
	for end < len(full) && !utf8.RuneStart(full[end]) {
		end--
	}
	var head string
	if end == len(full) {
		head = fmt.Sprintf("PAGE: bytes %d..%d of %d (ref %s); end", offset, end, len(full), in.Ref)
	} else {
		head = fmt.Sprintf("PAGE: bytes %d..%d of %d (ref %s); nextOffset %d", offset, end, len(full), in.Ref, end)
	}
	body := tools.Untrusted("tool:"+page.Tool+" ref:"+in.Ref, []byte(full), full[offset:end])
	return tools.Result{
		Text:   head + "\n" + body,
		Digest: head + "\n" + cutUTF8(full[offset:end], 1024, "…"),
	}
}

// readResultError is a read_result call that gives no page.
func readResultError(msg string) tools.Result {
	return tools.Result{Text: msg, Digest: msg, IsError: true}
}

// countTruncated is how many of the evidence entries rest on a result the model saw only part of.
func countTruncated(ev []evidence) int {
	n := 0
	for _, e := range ev {
		if e.Truncated {
			n++
		}
	}
	return n
}
