package council

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
)

// cleanReport is a report that needs no reviewer: high confidence, no proposal, whole.
func cleanReport() *report {
	return &report{
		Summary:       "the facts",
		Findings:      []finding{{Claim: "a claim", Evidence: []string{"tu_1"}}},
		Proposals:     []proposal{},
		Confidence:    confidenceHigh,
		OpenQuestions: []string{},
	}
}

// proposalReport is a report that proposes a change.
func proposalReport() *report {
	r := cleanReport()
	r.Proposals = []proposal{{What: "change it", Why: "because"}}
	return r
}

func TestNeedCritique(t *testing.T) {
	stopped := stoppedReport("Fleet", "time")
	raw := rawReport("not json")
	forced := cleanReport()
	forced.partial = true
	low := cleanReport()
	low.Confidence = confidenceLow
	openQuestions := cleanReport()
	openQuestions.OpenQuestions = []string{"is it the edge?"}
	openQuestions.Findings = []finding{{Claim: "a hunch", Evidence: []string{}}}
	coderEmpty := &coder.Report{State: coder.StateDone, Changed: []string{}}
	coderChanged := &coder.Report{State: coder.StateDone, Changed: []string{"M x"}}
	coderNotStarted := &coder.Report{State: coder.StateFailed, Reason: coder.ReasonBadTask, Changed: []string{}}

	for _, tc := range []struct {
		name       string
		thorough   bool
		unattended bool
		truncated  bool
		reports    []memberResult
		want       bool
		why        string
	}{
		{"thorough", true, false, false, nil, true, "thorough"},
		{"unattended", false, true, false, nil, true, "unattended"},
		{"truncated", false, false, true, nil, true, "truncated"},
		{"a stopped report", false, false, false,
			[]memberResult{{report: &stopped}}, true, "incomplete"},
		{"a raw report", false, false, false,
			[]memberResult{{report: &raw}}, true, "incomplete"},
		{"a forced report", false, false, false,
			[]memberResult{{report: forced}}, true, "incomplete"},
		{"a low-confidence report", false, false, false,
			[]memberResult{{report: low}}, true, "low confidence"},
		{"a coder with no changed file", false, false, false,
			[]memberResult{{coder: coderEmpty}}, true, "coder"},
		{"a coder with a changed file", false, false, false,
			[]memberResult{{coder: coderChanged}}, true, "coder"},
		{"a coder that never started", false, false, false,
			[]memberResult{{coder: coderNotStarted}}, true, "coder"},
		{"a proposal", false, false, false,
			[]memberResult{{report: proposalReport()}}, true, "proposal"},
		{"a clean report", false, false, false,
			[]memberResult{{report: cleanReport()}}, false, "not needed"},
		{"open questions and a finding without evidence", false, false, false,
			[]memberResult{{report: openQuestions}}, false, "not needed"},
		{"no reports", false, false, false, nil, false, "not needed"},
		// The order of the reasons: each one beats every reason after it.
		{"thorough beats the rest", true, true, true,
			[]memberResult{{report: &stopped}}, true, "thorough"},
		{"unattended beats truncated and an incomplete report", false, true, true,
			[]memberResult{{report: &stopped}}, true, "unattended"},
		{"truncated beats an incomplete report", false, false, true,
			[]memberResult{{report: &stopped}}, true, "truncated"},
		{"an incomplete report beats low confidence", false, false, false,
			[]memberResult{{report: &stopped}, {report: low}}, true, "incomplete"},
		{"an incomplete report beats low confidence, low first", false, false, false,
			[]memberResult{{report: low}, {report: forced}}, true, "incomplete"},
		{"low confidence beats a coder", false, false, false,
			[]memberResult{{report: low}, {coder: coderChanged}}, true, "low confidence"},
		{"low confidence beats a coder, coder first", false, false, false,
			[]memberResult{{coder: coderChanged}, {report: low}}, true, "low confidence"},
		{"low confidence beats a proposal", false, false, false,
			[]memberResult{{report: proposalReport()}, {report: low}}, true, "low confidence"},
		{"a coder beats a proposal, fleet first", false, false, false,
			[]memberResult{{report: proposalReport()}, {coder: coderChanged}}, true, "coder"},
		{"a coder beats a proposal, coder first", false, false, false,
			[]memberResult{{coder: coderChanged}, {report: proposalReport()}}, true, "coder"},
		// A reason held by a later member counts as much as the first member's.
		{"an incomplete report after a clean one", false, false, false,
			[]memberResult{{report: cleanReport()}, {report: forced}}, true, "incomplete"},
		{"a proposal after a clean report", false, false, false,
			[]memberResult{{report: cleanReport()}, {report: proposalReport()}}, true, "proposal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, why := needCritique(tc.thorough, tc.unattended, tc.truncated, tc.reports)
			if got != tc.want || why != tc.why {
				t.Errorf("needCritique(%v, %v, %v, %d reports) = %v, %q, want %v, %q",
					tc.thorough, tc.unattended, tc.truncated, len(tc.reports), got, why, tc.want, tc.why)
			}
		})
	}
}

// lowConfidenceReport is reportJSON with its confidence at low.
func lowConfidenceReport(summary string, evidence ...string) string {
	ev := make([]any, 0, len(evidence))
	for _, id := range evidence {
		ev = append(ev, id)
	}
	b, _ := json.Marshal(map[string]any{
		"summary":        summary,
		"findings":       []any{map[string]any{"claim": "a claim from " + summary, "evidence": ev}},
		"proposals":      []any{},
		"confidence":     confidenceLow,
		"open_questions": []any{},
	})
	return string(b)
}

// criticRuns is the one Critic request and the one critique event a policy-passing turn makes.
func criticRuns(t *testing.T, h *testHarness, p *fakePoster) {
	t.Helper()
	if n := len(h.model.requests(roleCritic)); n != 1 {
		t.Fatalf("%d critic requests, want 1", n)
	}
	if n := len(p.ofType(hub.EventCritique)); n != 1 {
		t.Fatalf("%d critique events, want 1", n)
	}
}

// noLeftovers fails when a scripted answer was never given.
func noLeftovers(t *testing.T, h *testHarness) {
	t.Helper()
	if left := h.model.leftover(); len(left) > 0 {
		t.Errorf("the fake model was not asked for every scripted answer: %v", left)
	}
}

// TestCriticRunsWhenTheLeadSawPartOfAResult: the Captain's own cut result makes the turn need the
// reviewer, even though Fleet's own result is whole.
func TestCriticRunsWhenTheLeadSawPartOfAResult(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is r2 degraded?"))
	h.tools.queueResults("list_robots", cutResult("the whole log", "the shown part"))
	// The Captain and then Fleet see whole results after the cut one: the flag is set once and never cleared.
	h.model.push(roleRoute,
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots", input: map[string]any{}}),
		toolStep("tool_use", apiCall{id: "tu_2", name: "whoami", input: map[string]any{}}),
		toolStep("tool_use", conveneCall("tu_3", taskOf(slugFleet, "the facts", "x"))))
	h.model.push(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_f1", name: "list_robots", input: map[string]any{}}),
		textStep(reportJSON("one robot is degraded", "tu_f1"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	criticRuns(t, h, p)
	noLeftovers(t, h)
	var fleet []map[string]any
	for _, e := range p.ofType(hub.EventToolResult) {
		if d := dataOf(t, e); d["id"] == "tu_f1" {
			fleet = append(fleet, d)
		}
	}
	if len(fleet) != 1 {
		t.Fatalf("%d tool_result events for Fleet's call, want 1", len(fleet))
	}
	if s, _ := fleet[0]["summary"].(string); strings.HasPrefix(s, "RESULT PARTIAL") {
		t.Errorf("Fleet's result was cut too: %q", s)
	}
}

// TestCriticRunsWhenAnErrorResultWasCut: a failed call whose text passed the limit was cut too
// (tools.Set.Call sets Truncated on an error result), so the turn needs the reviewer.
func TestCriticRunsWhenAnErrorResultWasCut(t *testing.T) {
	full := "[\n" + strings.Repeat("  {\"name\":\"x\"},\n", 9) + "  {\"name\":\"x\"}\n]"
	shown := full[:20] + "\n]"
	cut := cutResult(full, shown)
	cut.IsError = true
	h := newHarness(t, newWork(kindAsk, "", "which devices are there?"))
	h.tools.queueResults("list_robots", cut)
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(slugFleet, "the devices", "x"))))
	h.model.push(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_f1", name: "list_robots", input: map[string]any{}}),
		textStep(reportJSON("the call failed"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	criticRuns(t, h, p)
	noLeftovers(t, h)
}

// TestCriticRunsWhenAMemberSawPartOfAResult: only Fleet's result is cut, and the Captain's is not:
// the turn still needs the reviewer.
func TestCriticRunsWhenAMemberSawPartOfAResult(t *testing.T) {
	full := "[\n" + strings.Repeat("  {\"name\":\"x\"},\n", 9) + "  {\"name\":\"x\"}\n]"
	shown := full[:20] + "\n]"
	cut := cutResult(full, shown)

	h := newHarness(t, newWork(kindAsk, "", "which devices are there?"))
	h.tools.queueResults("list_robots", cut)
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(slugFleet, "the devices", "x"))))
	h.model.push(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_f1", name: "list_robots", input: map[string]any{}}),
		toolStep("tool_use", apiCall{id: "tu_r1", name: toolReadResult,
			input: map[string]any{"ref": "r1", "offset": cut.Cut.ContinueAt}}),
		textStep(reportJSON("nine of them, probably"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	criticRuns(t, h, p)
	noLeftovers(t, h)
}

// TestCriticSkippedWhenTheTurnNeedsItNot: an auto turn whose members all finished with complete
// results, high confidence and no proposal posts no critique at all, and the synthesis is told so.
func TestCriticSkippedWhenTheTurnNeedsItNot(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "how many robots are degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(slugFleet, "the facts", "x"))))
	h.model.push(roleFleet, textStep(reportJSON("one robot is degraded"), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	noLeftovers(t, h)
	if n := len(h.model.requests(roleCritic)); n != 0 {
		t.Errorf("%d critic requests, want none", n)
	}
	if n := len(p.ofType(hub.EventCritique)); n != 0 {
		t.Errorf("%d critique events, want none", n)
	}
	if n := len(p.ofType(hub.EventBudget)); n != 0 {
		t.Errorf("%d budget events, want none", n)
	}
	answers := h.model.requests(roleAnswer)
	if len(answers) != 1 {
		t.Fatalf("%d answer requests", len(answers))
	}
	last := paramText(answers[0].Messages[len(answers[0].Messages)-1])
	want := "The critique:\n" + `{"issues":[],"note":"The critique was not run (not needed)."}`
	if !strings.HasSuffix(last, want) {
		t.Errorf("the synthesis does not end with %q: %s", want, last)
	}
	if !hasText(h.logs.all(), "critique: skipped (not needed)") {
		t.Errorf("no skip line in the log: %v", h.logs.all())
	}
}

// TestCriticRunsOnLowConfidence: the same turn, with the report's confidence at low.
func TestCriticRunsOnLowConfidence(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "how many robots are degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(slugFleet, "the facts", "x"))))
	h.model.push(roleFleet, textStep(lowConfidenceReport("one robot is degraded"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	criticRuns(t, h, p)
	noLeftovers(t, h)
}

// TestCriticRunsOnAReportTheMemberDidNotFinish: Fleet's answers are not reports, so its report is a
// raw one: incomplete, and the reviewer runs.
func TestCriticRunsOnAReportTheMemberDidNotFinish(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "how many robots are degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(slugFleet, "the facts", "x"))))
	h.model.push(roleFleet,
		textStep("not json", "end_turn"),
		textStep("still not json", "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	var done map[string]any
	for _, e := range p.ofType(hub.EventMemberDone) {
		done = dataOf(t, e)
	}
	rep := done["report"].(map[string]any)
	if rep["summary"] != "still not json" {
		t.Errorf("the report's summary %v", rep["summary"])
	}
	criticRuns(t, h, p)
	noLeftovers(t, h)
}

// TestCriticRunsAfterTheCoder: an auto turn that convenes only the Coder, whose report has no
// changed file. It runs with the bot key, so the reviewer is needed all the same (D20).
func TestCriticRunsAfterTheCoder(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "write a script that samples the temperature"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberCoder, "a sampler", "the person asked"))))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if got := len(h.coder.started()); got != 1 {
		t.Fatalf("the Coder ran %d times", got)
	}
	criticRuns(t, h, p)
	noLeftovers(t, h)
}

// TestNoReviewerAutoTurnSkipsTheCritic: an auto turn of a council with no reviewer says so. The
// turn is clean, so the policy says no; the note is the reviewer check's, because it comes first.
func TestNoReviewerAutoTurnSkipsTheCritic(t *testing.T) {
	work := newWork(kindAsk, "", "which robots are degraded?")
	work.Council = customSnapshot(t, customRoster)
	h := goldenHarness(t, work)
	h.tools.catalog = customTools()
	h.whoamiAt("creator")
	h.model.push("lead-route:Chief",
		toolStep("tool_use", conveneCall("tu_1", taskOf("reader", "the robots", "facts"))))
	h.model.push("member:Reader", textStep(reportJSON("two robots are degraded"), "end_turn"))
	h.model.push("lead-answer:Chief", textStep("final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	noLeftovers(t, h)
	if n := len(h.model.requests(roleCritic)); n != 0 {
		t.Errorf("%d reviewer requests, want none", n)
	}
	if n := len(p.ofType(hub.EventCritique)); n != 0 {
		t.Errorf("%d critique events, want none", n)
	}
	answers := h.model.requests("lead-answer:Chief")
	if len(answers) != 1 {
		t.Fatalf("%d synthesis requests", len(answers))
	}
	last := paramText(answers[0].Messages[len(answers[0].Messages)-1])
	if !strings.HasSuffix(last, `{"issues":[],"note":"The critique was not run (no reviewer in this council)."}`) {
		t.Errorf("the synthesis has no note that the council has no reviewer: %s", last)
	}
	if !hasText(h.logs.all(), "critique: skipped (no reviewer)") {
		t.Errorf("no skip line in the log: %v", h.logs.all())
	}
}

// TestUnattendedTurnRunsTheCritic: the clean auto turn of TestCriticSkippedWhenTheTurnNeedsItNot,
// on a council that is unattended.
func TestUnattendedTurnRunsTheCritic(t *testing.T) {
	work := newWork(kindAsk, "", "how many robots are degraded?")
	work.Council = customSnapshot(t, func(s *hub.Snapshot) { s.Unattended = true })
	h := goldenHarness(t, work)
	h.whoamiAt("creator")
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(slugFleet, "the facts", "x"))))
	h.model.push(roleFleet, textStep(reportJSON("one robot is degraded"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	criticRuns(t, h, p)
	noLeftovers(t, h)
}
