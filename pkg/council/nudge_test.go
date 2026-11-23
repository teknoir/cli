package council

import (
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
)

// The no-tool nudge (noToolNudge in turn.go): a data member that answers its first call without
// calling a tool read nothing of what its tools exist for, so it gets one extra call with the nudge
// text, and the answer it gives then is its report. One extra call at most.

// TestNudgeReadsWithATool: a data member whose first answer is a report with no tool call is told
// once to call its tools; it then reads with one, and the report cites that call's evidence.
func TestNudgeReadsWithATool(t *testing.T) {
	h := newHarness(t, newWork(kindMember, slugFleet, "which robots are degraded?"))
	first := reportJSON("no tool has been called yet")
	h.model.push(roleFleet,
		textStep(first, "end_turn"),
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots", input: map[string]any{}}),
		textStep(reportJSON("one robot is degraded", "tu_1"), "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	reqs := h.model.requests(roleFleet)
	if len(reqs) != 3 {
		t.Fatalf("%d Fleet requests, want the answer, the nudged answer and the report", len(reqs))
	}
	// The nudged request is the first one with the first answer and the nudge appended, unforced.
	msgs := reqs[1].Messages
	if len(msgs) != len(reqs[0].Messages)+2 {
		t.Fatalf("the nudged request has %d messages, want %d", len(msgs), len(reqs[0].Messages)+2)
	}
	answer := msgs[len(msgs)-2]
	if answer.Role != anthropic.MessageParamRoleAssistant || paramText(answer) != first {
		t.Errorf("the nudged request does not end with the first answer: %q %q", answer.Role, paramText(answer))
	}
	if got := paramText(msgs[len(msgs)-1]); got != noToolNudge {
		t.Errorf("the nudge text is %q", got)
	}
	if reqs[1].ToolChoiceNone {
		t.Error("the nudged request is forced, and it must not be")
	}

	if got := h.tools.called(); len(got) != 1 || got[0] != "list_robots" {
		t.Errorf("the tools called are %v, want list_robots once", got)
	}
	var done map[string]any
	for _, e := range p.ofType(hub.EventMemberDone) {
		done = dataOf(t, e)
	}
	rep, ok := done["report"].(map[string]any)
	if !ok {
		t.Fatalf("no member_done report: %v", done)
	}
	findings, _ := rep["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("%d findings, want 1", len(findings))
	}
	if ev, _ := findings[0].(map[string]any)["evidence"].([]any); len(ev) != 1 || ev[0] != "tu_1" {
		t.Errorf("the report's evidence is %v, want the tool call's tu_1", ev)
	}
}

// TestNudgeAnswerWithoutAToolIsTheReport: a data member that calls no tool even when nudged is not
// questioned again: that second answer is its report.
func TestNudgeAnswerWithoutAToolIsTheReport(t *testing.T) {
	h := newHarness(t, newWork(kindMember, slugFleet, "which robots are degraded?"))
	second := reportJSON("still no tool, so this is the report")
	h.model.push(roleFleet,
		textStep(reportJSON("answered from what I already knew"), "end_turn"),
		textStep(second, "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if got := h.model.count(); got != 2 {
		t.Errorf("%d model calls, want 2: the answer and its one nudge", got)
	}
	if got := h.tools.called(); len(got) != 0 {
		t.Errorf("the tools called are %v, and the member called none", got)
	}
	want, err := parseReport(second)
	if err != nil {
		t.Fatal(err)
	}
	if res := p.result(); res.State != stateDone || res.Final != want.markdown() {
		t.Errorf("finish %q %q, want the nudged answer as the report", res.State, res.Final)
	}
}

// TestNoNudgeWithoutPlatformData: a role that works on the reports, not on the platform's data, is
// not nudged when it answers without a tool: it has no data tools to call.
func TestNoNudgeWithoutPlatformData(t *testing.T) {
	work := newWork(kindMember, "planner", "which robots are degraded?")
	s := stdSnapshot(t, "read")
	s.Roster, s.Lead, s.Reviewer, s.Budget = nil, nil, nil, nil
	planner := s.Members[0] // the Standard Fleet is the role every test member is built from.
	planner.Slug, planner.Name = "planner", "Planner"
	planner.Groups, planner.Tools, planner.Skills, planner.Results = []string{"results"}, nil, nil, true
	s.Members = []hub.RoleSnapshot{planner}
	work.Council = s

	h := newHarness(t, work)
	h.model.push("member:Planner", textStep(reportJSON("no tool was called, and none was needed"), "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if got := h.model.count(); got != 1 {
		t.Errorf("%d model calls, want 1: the answer, with no nudge", got)
	}
	if res := p.result(); res.State != stateDone || !strings.Contains(res.Final, "no tool was called, and none was needed") {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// TestNudgeIsBoundedByTheTokenLimit: the nudge is one extra call, and the member's token limit still
// bounds it. A first answer that spends the member's whole budget makes the nudged call a forced
// one (tool_choice none): the nudge cannot buy it a tool round it could not afford, and a second
// tool-less answer is the report.
func TestNudgeIsBoundedByTheTokenLimit(t *testing.T) {
	work := newWork(kindMember, slugFleet, "which robots are degraded?")
	s := stdSnapshot(t, "read")
	s.Roster, s.Lead, s.Reviewer, s.Budget = nil, nil, nil, nil
	fleet := s.Members[0]
	fleet.Budget = &hub.RoleBudget{MaxToolRounds: 2, MaxTokens: 10_000}
	s.Members = []hub.RoleSnapshot{fleet}
	work.Council = s

	h := newHarness(t, work)
	h.model.push(roleFleet,
		// The first answer spends the member's 10,000 tokens: a nudged call would be forced.
		&scriptStep{usage: model.Usage{Calls: 1, Input: 10_000},
			msg: apiMessage([]map[string]any{{"type": "text", "text": reportJSON("read nothing, and spent the budget")}}, "end_turn")})
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	noLeftovers(t, h)
	if reqs := h.model.requests(roleFleet); len(reqs) != 1 {
		t.Fatalf("%d Fleet requests, want only the answer: no nudge once the token budget is spent", len(reqs))
	}
	if res := p.result(); res.State != stateDone || !strings.Contains(res.Final, "spent the budget") {
		t.Errorf("finish %q %q, want the first answer as the report", res.State, res.Final)
	}
}

// TestConvenedMemberIsNudged: the nudge is not the member turn's alone; a data member the lead
// convenes gets it too.
func TestConvenedMemberIsNudged(t *testing.T) {
	work := newWork(kindAsk, "", "how many robots are degraded?")
	work.Options.Mode = modeCouncil
	h := newHarness(t, work)
	h.model.push(roleRoute, textStep("I will look", "end_turn"))
	h.model.push(roleFleet,
		textStep(reportJSON("nothing has been read yet"), "end_turn"),
		textStep(reportJSON("one robot is degraded"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	noLeftovers(t, h)
	reqs := h.model.requests(roleFleet)
	if len(reqs) != 2 {
		t.Fatalf("%d Fleet requests, want the answer and its one nudge", len(reqs))
	}
	if got := paramText(reqs[1].Messages[len(reqs[1].Messages)-1]); got != noToolNudge {
		t.Errorf("the nudged request does not end with the nudge: %q", got)
	}
	// The synthesis reads the nudged answer, not the first one.
	answers := h.model.requests(roleAnswer)
	if len(answers) != 1 {
		t.Fatalf("%d answer requests", len(answers))
	}
	syn := paramText(answers[0].Messages[len(answers[0].Messages)-1])
	if !strings.Contains(syn, "one robot is degraded") || strings.Contains(syn, "nothing has been read yet") {
		t.Errorf("the synthesis does not carry the nudged report: %s", syn)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the final" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}
