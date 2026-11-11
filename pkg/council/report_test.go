package council

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/model"
)

const goodReport = `{"summary":"The robot is offline.","findings":[{"claim":"the device is not connected","evidence":["c1","c2"]}],"proposals":[{"what":"tnctl robot restart r1","why":"it is stuck"}],"confidence":"medium","open_questions":["why did it stop?"]}`

const goodCritique = `{"issues":[{"member":"fleet","severity":"blocking","claim":"no evidence for the uptime","why":"the answer is wrong without it"},{"member":"coder","severity":"note","claim":"the script was not run","why":"it may fail"}]}`

func TestParseReport(t *testing.T) {
	r, err := parseReport(goodReport)
	if err != nil {
		t.Fatalf("parseReport: %v", err)
	}
	if r.Summary != "The robot is offline." {
		t.Errorf("summary = %q", r.Summary)
	}
	if len(r.Findings) != 1 || r.Findings[0].Claim != "the device is not connected" || len(r.Findings[0].Evidence) != 2 {
		t.Errorf("findings = %+v", r.Findings)
	}
	if len(r.Proposals) != 1 || r.Proposals[0].What != "tnctl robot restart r1" {
		t.Errorf("proposals = %+v", r.Proposals)
	}
	if r.Confidence != "medium" {
		t.Errorf("confidence = %q", r.Confidence)
	}
	if len(r.OpenQuestions) != 1 {
		t.Errorf("open questions = %+v", r.OpenQuestions)
	}
	// Empty lists are not nil.
	empty, err := parseReport(`{"summary":"s","findings":[],"proposals":[],"confidence":"low","open_questions":[]}`)
	if err != nil {
		t.Fatalf("parseReport empty: %v", err)
	}
	if empty.Findings == nil || empty.Proposals == nil || empty.OpenQuestions == nil {
		t.Errorf("empty lists = %+v", empty)
	}
}

func TestParseReportFenced(t *testing.T) {
	for _, fenced := range []string{
		"```json\n" + goodReport + "\n```",
		"```\n" + goodReport + "\n```",
		"\n\n```json\n" + goodReport + "\n```\n\n",
	} {
		r, err := parseReport(fenced)
		if err != nil {
			t.Fatalf("parseReport of a fenced report: %v", err)
		}
		if r.Confidence != "medium" {
			t.Errorf("fenced report confidence = %q", r.Confidence)
		}
	}
}

func TestParseCritique(t *testing.T) {
	c, err := parseCritique(goodCritique)
	if err != nil {
		t.Fatalf("parseCritique: %v", err)
	}
	if len(c.Issues) != 2 || c.Issues[0].Severity != "blocking" || c.Issues[1].Severity != "note" {
		t.Errorf("issues = %+v", c.Issues)
	}
	if c.Issues[0].Member != "fleet" {
		t.Errorf("member = %q", c.Issues[0].Member)
	}
	fenced, err := parseCritique("```json\n" + goodCritique + "\n```")
	if err != nil || len(fenced.Issues) != 2 {
		t.Fatalf("a fenced critique = %+v, %v", fenced, err)
	}
	none, err := parseCritique(`{"issues":[]}`)
	if err != nil || none.Issues == nil || len(none.Issues) != 0 {
		t.Fatalf("an empty critique = %+v, %v", none, err)
	}
}

func TestParseReportErrors(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
	}{
		{"not JSON", `hello`, "not JSON"},
		{"an array", `[]`, "not JSON"},
		{"null", `null`, "not JSON"},
		{"an unknown key", `{"summary":"s","findings":[],"proposals":[],"confidence":"low","open_questions":[],"extra":1}`, "unknown key"},
		{"a missing field", `{"summary":"s","findings":[],"proposals":[],"confidence":"low"}`, "missing open_questions"},
		{"a wrong summary type", `{"summary":1,"findings":[],"proposals":[],"confidence":"low","open_questions":[]}`, "summary must be a string"},
		{"findings not a list", `{"summary":"s","findings":{},"proposals":[],"confidence":"low","open_questions":[]}`, "findings must be a list"},
		{"a finding without a claim", `{"summary":"s","findings":[{"evidence":[]}],"proposals":[],"confidence":"low","open_questions":[]}`, "findings item is missing claim"},
		{"a bad enum", `{"summary":"s","findings":[],"proposals":[],"confidence":"high-ish","open_questions":[]}`, "confidence must be low, medium or high"},
		{"a proposal without why", `{"summary":"s","findings":[],"proposals":[{"what":"w"}],"confidence":"low","open_questions":[]}`, "proposals item is missing why"},
		{"open questions not a list", `{"summary":"s","findings":[],"proposals":[],"confidence":"low","open_questions":"q"}`, "open_questions must be a list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseReport(tc.text)
			if err == nil {
				t.Fatalf("parseReport accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "zzsecretzz") || strings.Contains(err.Error(), strings.TrimSuffix(tc.text, `"}`)) && len(tc.text) > 40 {
				t.Errorf("the error carries the text: %v", err)
			}
		})
	}
}

func TestParseCritiqueErrors(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
	}{
		{"not JSON", `nope`, "not JSON"},
		{"an unknown key", `{"issues":[],"other":1}`, "unknown key"},
		{"a missing field", `{}`, "missing issues"},
		{"issues not a list", `{"issues":{}}`, "issues must be a list"},
		{"a bad severity", `{"issues":[{"member":"fleet","severity":"fatal","claim":"c","why":"w"}]}`, "severity must be blocking or note"},
		{"an issue without why", `{"issues":[{"member":"fleet","severity":"note","claim":"c"}]}`, "issues item is missing why"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCritique(tc.text)
			if err == nil {
				t.Fatalf("parseCritique accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %q", err, tc.want)
			}
		})
	}
}

func TestKeepEvidence(t *testing.T) {
	r, err := parseReport(goodReport)
	if err != nil {
		t.Fatal(err)
	}
	r.keepEvidence(map[string]bool{"c2": true})
	if len(r.Findings[0].Evidence) != 1 || r.Findings[0].Evidence[0] != "c2" {
		t.Errorf("evidence = %+v, want [c2]", r.Findings[0].Evidence)
	}
	r.keepEvidence(nil)
	if r.Findings[0].Evidence == nil || len(r.Findings[0].Evidence) != 0 {
		t.Errorf("evidence = %+v, want an empty list", r.Findings[0].Evidence)
	}
}

func TestReportMarkdown(t *testing.T) {
	r, err := parseReport(goodReport)
	if err != nil {
		t.Fatal(err)
	}
	got := r.markdown()
	for _, want := range []string{
		"**Summary.** The robot is offline.",
		"**Findings**\n- the device is not connected (evidence: c1, c2)",
		"**Proposals**\n- tnctl robot restart r1: it is stuck",
		"**Open questions**\n- why did it stop?",
		"Confidence: medium.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown lacks %q:\n%s", want, got)
		}
	}
	// A finding without evidence is a plain bullet, and empty sections are left out.
	bare := report{Summary: "s", Findings: []finding{{Claim: "c"}}, Confidence: "low"}
	got = bare.markdown()
	if strings.Contains(got, "evidence:") || strings.Contains(got, "**Proposals**") || strings.Contains(got, "**Open questions**") {
		t.Errorf("bare markdown = %q", got)
	}
	if got != "**Summary.** s\n\n**Findings**\n- c\n\nConfidence: low." {
		t.Errorf("bare markdown = %q", got)
	}
}

func TestCoderMarkdown(t *testing.T) {
	done := coder.Report{State: coder.StateDone, Text: "It is done."}
	if got := coderMarkdown(done); got != "It is done." {
		t.Errorf("done markdown = %q", got)
	}
	done.Changed = []string{" M index.html", "?? run.sh"}
	got := coderMarkdown(done)
	if !strings.Contains(got, "**Changed files**") || !strings.Contains(got, "```\n M index.html\n?? run.sh\n```") {
		t.Errorf("done markdown = %q", got)
	}
	failed := coder.Report{State: coder.StateFailed, Reason: coder.ReasonTimeout, Error: "it ran too long"}
	got = coderMarkdown(failed)
	if !strings.HasPrefix(got, "The Coder wrote no report.") {
		t.Errorf("failed markdown = %q", got)
	}
	if !strings.Contains(got, "The Coder stopped: timeout. it ran too long") {
		t.Errorf("failed markdown = %q", got)
	}
	bare := coderMarkdown(coder.Report{State: coder.StateFailed, Reason: coder.ReasonStart})
	if !strings.Contains(bare, "The Coder stopped: start.") || strings.Contains(bare, "The Coder stopped: start. ") {
		t.Errorf("a failure without an error = %q", bare)
	}
}

func TestReportForEvent(t *testing.T) {
	small, err := parseReport(goodReport)
	if err != nil {
		t.Fatal(err)
	}
	if got := small.forEvent(); got.Cut || got.Summary != small.Summary || len(got.Findings) != 1 {
		t.Errorf("a small report was cut: %+v", got)
	}
	big := small
	big.Findings = make([]finding, 200)
	for i := range big.Findings {
		big.Findings[i] = finding{Claim: strings.Repeat("c", 500), Evidence: []string{strings.Repeat("e", 500)}}
	}
	cut := big.forEvent()
	raw, _ := json.Marshal(cut)
	if len(raw) > maxReportForEvent {
		t.Errorf("the cut report is %d bytes, want at most %d", len(raw), maxReportForEvent)
	}
	if !cut.Cut {
		t.Error("the cut report has no cut flag")
	}
	if len(cut.Findings) != 0 || cut.Confidence != confidenceLow || cut.OpenQuestions == nil {
		t.Errorf("the cut report = %+v", cut)
	}
}

func TestCoderForEvent(t *testing.T) {
	small := coder.Report{State: coder.StateDone, Text: "ok", Changed: []string{"a"}}
	if got, ok := coderForEvent(small).(coder.Report); !ok || got.Text != "ok" {
		t.Errorf("a small coder report = %#v, want itself", coderForEvent(small))
	}
	big := coder.Report{State: coder.StateDone, Text: strings.Repeat("t", 40<<10)}
	raw, _ := json.Marshal(big)
	if len(raw) <= maxReportForEvent {
		t.Fatalf("the test report is only %d bytes", len(raw))
	}
	for i := 0; i < 40; i++ {
		big.Changed = append(big.Changed, " M file"+string(rune('a'+i%26))+strings.Repeat("x", 100))
	}
	got, ok := coderForEvent(big).(map[string]any)
	if !ok {
		t.Fatalf("a big coder report = %#v, want a map", coderForEvent(big))
	}
	if got["cut"] != true || got["state"] != coder.StateDone {
		t.Errorf("the cut coder report = %+v", got)
	}
	if text, _ := got["text"].(string); len(text) > maxReportText+len("…") {
		t.Errorf("the cut text is %d bytes", len(text))
	}
	if changed, _ := got["changed"].([]string); len(changed) != maxCoderChanged {
		t.Errorf("changed = %d lines, want %d", len(changed), maxCoderChanged)
	}
	raw, _ = json.Marshal(got)
	if len(raw) > maxReportForEvent {
		t.Errorf("the cut coder report is %d bytes, want at most %d", len(raw), maxReportForEvent)
	}
}

func TestCritiqueForEvent(t *testing.T) {
	small := critique{Issues: []issue{{Member: "fleet", Severity: "note", Claim: "c", Why: "w"}}}
	if got := small.forEvent(); got.Cut {
		t.Errorf("a small critique was cut: %+v", got)
	}
	big := critique{Issues: make([]issue, 200)}
	for i := range big.Issues {
		big.Issues[i] = issue{Member: "fleet", Severity: "note", Claim: strings.Repeat("c", 2000), Why: strings.Repeat("w", 2000)}
	}
	cut := big.forEvent()
	raw, _ := json.Marshal(cut)
	if len(raw) > maxReportForEvent {
		t.Errorf("the cut critique is %d bytes, want at most %d", len(raw), maxReportForEvent)
	}
	if !cut.Cut {
		t.Error("the cut critique has no cut flag")
	}
	if len(cut.Issues) == len(big.Issues) || len(cut.Issues) == 0 {
		t.Errorf("the cut critique kept %d of %d issues", len(cut.Issues), len(big.Issues))
	}
	for _, it := range cut.Issues {
		if len(it.Claim) > maxIssueField+len("…") || len(it.Why) > maxIssueField+len("…") {
			t.Errorf("an issue is %d/%d bytes, want at most %d", len(it.Claim), len(it.Why), maxIssueField)
		}
	}
	// A huge note alone is cut, then dropped.
	huge := critique{Note: strings.Repeat("n", 40<<10)}
	got := huge.forEvent()
	raw, _ = json.Marshal(got)
	if len(raw) > maxReportForEvent || !got.Cut {
		t.Errorf("a huge note = %d bytes, cut %v", len(raw), got.Cut)
	}
}

func TestUsageJSON(t *testing.T) {
	got := usageJSON(model.Usage{Calls: 2, Input: 1, Output: 3, CacheRead: 4, CacheWrite: 5})
	want := map[string]int64{"calls": 2, "input": 1, "output": 3, "cacheRead": 4, "cacheWrite": 5}
	if len(got) != len(want) {
		t.Fatalf("usageJSON has %d keys: %+v", len(got), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("usageJSON[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func TestRawAndStoppedReports(t *testing.T) {
	r := rawReport(strings.Repeat("x", 10<<10))
	raw, _ := json.Marshal(r)
	if len(raw) > 2*maxReportText {
		t.Errorf("the raw report is %d bytes", len(raw))
	}
	if r.Confidence != confidenceLow || r.Findings == nil || r.Proposals == nil || r.OpenQuestions == nil {
		t.Errorf("the raw report = %+v", r)
	}
	s := stoppedReport("timeout")
	if s.Summary != "Fleet stopped: timeout." || s.Confidence != confidenceLow || s.Findings == nil {
		t.Errorf("the stopped report = %+v", s)
	}
}

// TestReportSchema: every object takes no other keys and requires all of them.
func TestReportSchema(t *testing.T) {
	for _, schema := range []map[string]any{reportSchema, critiqueSchema} {
		raw, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(raw), `"additionalProperties":false`) < 2 {
			t.Errorf("schema = %s, want additionalProperties false on every object", raw)
		}
	}
}

// TestCoderDurationIsMillis guards the coder report's own JSON, which coderForEvent measures.
func TestCoderDurationIsMillis(t *testing.T) {
	raw, err := json.Marshal(coder.Report{State: coder.StateDone, Duration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"durationMs":1000`) {
		t.Errorf("coder report = %s", raw)
	}
}
