package council

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/model"
)

// finding is one claim of a member's report and the tool calls that show it.
type finding struct {
	Claim    string   `json:"claim"`
	Evidence []string `json:"evidence"`
}

// proposal is one change a member suggests.
type proposal struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

// report is what a member reports.
type report struct {
	Summary       string     `json:"summary"`
	Findings      []finding  `json:"findings"`
	Proposals     []proposal `json:"proposals"`
	Confidence    string     `json:"confidence"`
	OpenQuestions []string   `json:"open_questions"`
	Cut           bool       `json:"cut,omitempty"`
	// partial: the member did not finish (a stopped report, a text that was not a report, an answer
	// forced by its round or token limit or cut with tool calls it never ran, or a plan task for a
	// member the council has not); never in the JSON.
	partial bool
}

// issue is one problem the Critic found.
type issue struct {
	Member   string `json:"member"`
	Severity string `json:"severity"`
	Claim    string `json:"claim"`
	Why      string `json:"why"`
}

// critique is what the Critic reports.
type critique struct {
	Issues []issue `json:"issues"`
	Note   string  `json:"note,omitempty"`
	Cut    bool    `json:"cut,omitempty"`
}

// The limits of a report, a critique and what an event carries.
const (
	maxReportText     = 4096
	maxReportForEvent = 16 << 10
	maxIssueField     = 1024
	maxCoderChanged   = 20

	confidenceLow    = "low"
	confidenceMedium = "medium"
	confidenceHigh   = "high"

	severityBlocking = "blocking"
	severityNote     = "note"
)

// reportSchema is the JSON schema a member's answer must follow, and critiqueSchema the Critic's.
var (
	reportSchema = objectSchema(map[string]any{
		"summary": stringSchema(),
		"findings": arraySchema(objectSchema(map[string]any{
			"claim":    stringSchema(),
			"evidence": arraySchema(stringSchema()),
		})),
		"proposals": arraySchema(objectSchema(map[string]any{
			"what": stringSchema(),
			"why":  stringSchema(),
		})),
		"confidence":     enumSchema(confidenceLow, confidenceMedium, confidenceHigh),
		"open_questions": arraySchema(stringSchema()),
	})
	critiqueSchema = objectSchema(map[string]any{
		"issues": arraySchema(objectSchema(map[string]any{
			"member":   stringSchema(),
			"severity": enumSchema(severityBlocking, severityNote),
			"claim":    stringSchema(),
			"why":      stringSchema(),
		})),
	})
)

// objectSchema is a JSON object schema that takes no other keys and requires all of them.
func objectSchema(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for name := range properties {
		required = append(required, name)
	}
	sort.Strings(required)
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

// stringSchema is a JSON string schema.
func stringSchema() map[string]any { return map[string]any{"type": "string"} }

// arraySchema is a JSON array schema of items.
func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

// enumSchema is a JSON string schema with a closed set of values.
func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

// parseReport parses a member's answer: the JSON object the report schema describes, with a
// surrounding code fence removed. The error names the first problem and never carries the text.
func parseReport(text string) (report, error) {
	fields, err := answerFields(text, "summary", "findings", "proposals", "confidence", "open_questions")
	if err != nil {
		return report{}, err
	}
	var r report
	if r.Summary, err = stringField(fields, "summary"); err != nil {
		return report{}, err
	}
	if r.Findings, err = structList[finding](fields, "findings", "claim", "evidence"); err != nil {
		return report{}, err
	}
	if r.Proposals, err = structList[proposal](fields, "proposals", "what", "why"); err != nil {
		return report{}, err
	}
	if r.Confidence, err = stringField(fields, "confidence"); err != nil {
		return report{}, err
	}
	switch r.Confidence {
	case confidenceLow, confidenceMedium, confidenceHigh:
	default:
		return report{}, errors.New("confidence must be low, medium or high")
	}
	if r.OpenQuestions, err = stringList(fields, "open_questions"); err != nil {
		return report{}, err
	}
	for i := range r.Findings {
		if r.Findings[i].Evidence == nil {
			r.Findings[i].Evidence = []string{}
		}
	}
	return r, nil
}

// parseCritique parses the Critic's answer: the JSON object the critique schema describes, with a
// surrounding code fence removed. The error names the first problem and never carries the text.
func parseCritique(text string) (critique, error) {
	fields, err := answerFields(text, "issues")
	if err != nil {
		return critique{}, err
	}
	out := critique{}
	if out.Issues, err = structList[issue](fields, "issues", "member", "severity", "claim", "why"); err != nil {
		return critique{}, err
	}
	for _, it := range out.Issues {
		switch it.Severity {
		case severityBlocking, severityNote:
		default:
			return critique{}, errors.New("severity must be blocking or note")
		}
	}
	return out, nil
}

// answerFields is an answer as a JSON object with a surrounding code fence removed: every key the
// answer may have is present in allowed, and every one of them is there.
func answerFields(text string, allowed ...string) (map[string]json.RawMessage, error) {
	s := unfence(strings.TrimSpace(text))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &fields); err != nil || fields == nil {
		return nil, errors.New("not JSON")
	}
	known := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		known[name] = true
	}
	for name := range fields {
		if !known[name] {
			return nil, errors.New("an unknown key")
		}
	}
	for _, name := range allowed {
		if _, ok := fields[name]; !ok {
			return nil, fmt.Errorf("missing %s", name)
		}
	}
	return fields, nil
}

// unfence removes one surrounding ```json or ``` fence from a trimmed answer.
func unfence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := strings.TrimPrefix(s, "```")
	i := strings.IndexByte(rest, '\n')
	if i < 0 {
		return s
	}
	rest = strings.TrimSuffix(rest[i+1:], "```")
	return strings.TrimSpace(rest)
}

// stringField is one required string field.
func stringField(fields map[string]json.RawMessage, name string) (string, error) {
	var s string
	if err := json.Unmarshal(fields[name], &s); err != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return s, nil
}

// stringList is one required list of strings.
func stringList(fields map[string]json.RawMessage, name string) ([]string, error) {
	var v []string
	if err := json.Unmarshal(fields[name], &v); err != nil || v == nil {
		return nil, fmt.Errorf("%s must be a list", name)
	}
	return v, nil
}

// structList is one required list of objects, each with every key of required present, no unknown
// key, and the types T gives its fields.
func structList[T any](fields map[string]json.RawMessage, name string, required ...string) ([]T, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(fields[name], &items); err != nil || items == nil {
		return nil, fmt.Errorf("%s must be a list", name)
	}
	out := make([]T, 0, len(items))
	for _, item := range items {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(item, &keys); err != nil || keys == nil {
			return nil, fmt.Errorf("%s must be a list of objects", name)
		}
		for _, key := range required {
			if _, ok := keys[key]; !ok {
				return nil, fmt.Errorf("%s item is missing %s", name, key)
			}
		}
		var v T
		dec := json.NewDecoder(bytes.NewReader(item))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s must be a list of objects", name)
		}
		out = append(out, v)
	}
	return out, nil
}

// rawReport is a member's text that is not the report JSON, as a low-confidence report.
func rawReport(text string) report {
	return report{
		Summary:       cutUTF8(text, maxReportText, "…"),
		Findings:      []finding{},
		Proposals:     []proposal{},
		Confidence:    confidenceLow,
		OpenQuestions: []string{},
		partial:       true,
	}
}

// stoppedReport is a member that did not report, as a low-confidence report.
func stoppedReport(name, why string) report {
	return report{
		Summary:       name + " stopped: " + why + ".",
		Findings:      []finding{},
		Proposals:     []proposal{},
		Confidence:    confidenceLow,
		OpenQuestions: []string{},
		partial:       true,
	}
}

// keepEvidence removes evidence ids not in ids (the member's own tool call ids).
func (r *report) keepEvidence(ids map[string]bool) {
	for i := range r.Findings {
		kept := make([]string, 0, len(r.Findings[i].Evidence))
		for _, id := range r.Findings[i].Evidence {
			if ids[id] {
				kept = append(kept, id)
			}
		}
		r.Findings[i].Evidence = kept
	}
}

// markdown is the report as the answer and the transcript show it.
func (r report) markdown() string {
	var b strings.Builder
	b.WriteString("**Summary.** " + r.Summary)
	if len(r.Findings) > 0 {
		b.WriteString("\n\n**Findings**")
		for _, f := range r.Findings {
			b.WriteString("\n- " + f.Claim)
			if len(f.Evidence) > 0 {
				b.WriteString(" (evidence: " + strings.Join(f.Evidence, ", ") + ")")
			}
		}
	}
	if len(r.Proposals) > 0 {
		b.WriteString("\n\n**Proposals**")
		for _, p := range r.Proposals {
			b.WriteString("\n- " + p.What + ": " + p.Why)
		}
	}
	if len(r.OpenQuestions) > 0 {
		b.WriteString("\n\n**Open questions**")
		for _, q := range r.OpenQuestions {
			b.WriteString("\n- " + q)
		}
	}
	b.WriteString("\n\nConfidence: " + r.Confidence + ".")
	return b.String()
}

// coderMarkdown is the Coder's report as the answer and the transcript show it.
func coderMarkdown(r coder.Report) string {
	var b strings.Builder
	if r.Text != "" {
		b.WriteString(r.Text)
	} else {
		b.WriteString("The Coder wrote no report.")
	}
	if len(r.Changed) > 0 {
		b.WriteString("\n\n**Changed files**\n\n```\n")
		b.WriteString(strings.Join(r.Changed, "\n"))
		b.WriteString("\n```")
	}
	if r.State == coder.StateFailed {
		b.WriteString("\n\nThe Coder stopped: " + r.Reason + ".")
		if r.Error != "" {
			b.WriteString(" " + r.Error)
		}
	}
	return b.String()
}

// forEvent is the report as a member_done event carries it: r itself when its JSON is at most
// 16 KiB, else {summary: cut to 4 KiB, findings: [], proposals: [], confidence: "low",
// open_questions: [], cut: true}.
func (r report) forEvent() report {
	if raw, err := json.Marshal(r); err == nil && len(raw) <= maxReportForEvent {
		return r
	}
	return report{
		Summary:       cutUTF8(r.Summary, maxReportText, "…"),
		Findings:      []finding{},
		Proposals:     []proposal{},
		Confidence:    confidenceLow,
		OpenQuestions: []string{},
		Cut:           true,
	}
}

// coderForEvent is the coder report for member_done: r when its JSON is at most 16 KiB, else
// {state, reason, text: Text cut to 4 KiB, changed: its first 20 lines, cut: true}.
func coderForEvent(r coder.Report) any {
	if raw, err := json.Marshal(r); err == nil && len(raw) <= maxReportForEvent {
		return r
	}
	changed := r.Changed
	if changed == nil {
		changed = []string{}
	}
	if len(changed) > maxCoderChanged {
		changed = changed[:maxCoderChanged]
	}
	return map[string]any{
		"state":   r.State,
		"reason":  r.Reason,
		"text":    cutUTF8(r.Text, maxReportText, "…"),
		"changed": changed,
		"cut":     true,
	}
}

// forEvent keeps issues while the JSON is at most 16 KiB: first every claim/why cut to 1 KiB, then
// issues dropped from the end; Cut is set when anything changed.
func (c critique) forEvent() critique {
	if raw, err := json.Marshal(c); err == nil && len(raw) <= maxReportForEvent {
		return c
	}
	cut := critique{Note: c.Note, Issues: make([]issue, len(c.Issues))}
	changed := false
	for i, it := range c.Issues {
		entry := issue{
			Member:   it.Member,
			Severity: it.Severity,
			Claim:    cutUTF8(it.Claim, maxIssueField, "…"),
			Why:      cutUTF8(it.Why, maxIssueField, "…"),
		}
		if entry.Claim != it.Claim || entry.Why != it.Why {
			changed = true
		}
		cut.Issues[i] = entry
	}
	for len(cut.Issues) > 0 {
		if raw, err := json.Marshal(cut); err == nil && len(raw) <= maxReportForEvent {
			break
		}
		cut.Issues = cut.Issues[:len(cut.Issues)-1]
		changed = true
	}
	// A note alone can be too long: it is cut, and dropped when even 4 KiB of it is too much.
	if raw, err := json.Marshal(cut); err != nil || len(raw) > maxReportForEvent {
		if n := cutUTF8(cut.Note, maxReportText, "…"); n != cut.Note {
			cut.Note = n
			changed = true
		}
		if raw, err := json.Marshal(cut); err != nil || len(raw) > maxReportForEvent {
			cut.Note = ""
			changed = true
		}
	}
	cut.Cut = changed
	return cut
}

// usageJSON is u as events carry it: {calls, input, output, cacheRead, cacheWrite}.
func usageJSON(u model.Usage) map[string]int64 {
	return map[string]int64{
		"calls":      u.Calls,
		"input":      u.Input,
		"output":     u.Output,
		"cacheRead":  u.CacheRead,
		"cacheWrite": u.CacheWrite,
	}
}
