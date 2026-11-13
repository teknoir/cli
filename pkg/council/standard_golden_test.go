package council

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
	"github.com/teknoir/cli/pkg/resultcut"
)

// The standard baseline: what the council does today, request for request and event for event.
//
// The roster steps change the council's fixed members (Captain, Fleet, Coder, Critic) into members
// that come from a roster snapshot. The default "Standard" roster must reproduce today's
// behaviour byte for byte, so this file records today's behaviour as golden files under
// testdata/standard/, and every later step must keep them passing unchanged.
//
// A scenario records every model request, every coder task the council started, every tool call
// (with the access level it ran at), every event, and how the turn finished. That covers what the
// refactor rewrites: the prompts, the parsed answers, the forced answers and the tool plumbing.
//
// One scenario is expected to change on purpose: 9b (a Fleet report with no proposals, where the
// Critic runs today) is re-recorded by the step that makes the Critic conditional.
//
// Run with -update to rewrite the files:
//
//	go test ./pkg/council -run TestStandardGolden -update

// init registers -update unless another test file in the package already did: two registrations of
// one name panic the test binary. It runs when the package loads, which is when the test binary
// parses its flags.
func init() {
	if flag.Lookup("update") == nil {
		flag.Bool("update", false, "rewrite testdata/standard/*.json")
	}
}

// updating reports whether -update is on, read when a file is written or compared. The flag is
// looked up then, not cached when the package loads, so that a flag another test file registered
// first is the one that counts.
func updating() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// goldenUsage is what every scripted answer costs: nonzero and the same in every scenario, so the
// usage and budget arithmetic shows in the events and the files stay readable.
var goldenUsage = model.Usage{Calls: 1, Input: 100, Output: 10, CacheRead: 5}

// pushUsed is push with the golden usage on every step.
func (m *fakeModel) pushUsed(role string, steps ...*scriptStep) {
	for _, s := range steps {
		s.usage = goldenUsage
	}
	m.push(role, steps...)
}

// goldenRequest is one model.Request as the golden file records it. Turn is left out: every
// request of a scenario carries the same turn.
type goldenRequest struct {
	Role           string `json:"role"`
	Model          string `json:"model"`
	Effort         string `json:"effort"`
	System         string `json:"system"`
	MaxTokens      int64  `json:"maxTokens"`
	ToolChoiceNone bool   `json:"toolChoiceNone"`
	Format         any    `json:"format"`
	Tools          any    `json:"tools"`
	Messages       any    `json:"messages"`
}

// goldenCoderTask is one coder.Task the council started, without the turn ids and the callbacks.
type goldenCoderTask struct {
	Prompt string `json:"prompt"`
	Effort string `json:"effort"`
}

// goldenToolCall is one tool call the council made, with the access level it ran at and its parsed
// input.
type goldenToolCall struct {
	Level string `json:"level"`
	Name  string `json:"name"`
	Input any    `json:"input"`
}

// goldenEvent is one event the turn's poster saw, with its data as it travels.
type goldenEvent struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// goldenFinish is how the turn finished: the state and final the poster was given, and the error.
type goldenFinish struct {
	State   string `json:"state"`
	Final   string `json:"final"`
	ErrCode string `json:"errCode"`
	ErrMsg  string `json:"errMsg"`
}

// goldenDoc is one scenario: every request the model saw, in the order it saw them, the coder
// tasks, the tool calls, every event the poster got, and the finish.
type goldenDoc struct {
	Requests   []goldenRequest   `json:"requests"`
	CoderTasks []goldenCoderTask `json:"coderTasks"`
	ToolCalls  []goldenToolCall  `json:"toolCalls"`
	Events     []goldenEvent     `json:"events"`
	Finish     goldenFinish      `json:"finish"`
}

// TestStandardGolden records and checks the standard council.
func TestStandardGolden(t *testing.T) {
	scenarios := []struct {
		name  string
		setup func(t *testing.T) *testHarness
	}{
		{"1", scenarioQuickRead},
		{"2", scenarioAutoToolRound},
		{"3", scenarioCouncilRead},
		{"4", scenarioCouncilCreator},
		{"5", scenarioCouncilNone},
		{"6", scenarioMemberFleetHistory},
		{"6n", scenarioMemberFleetNoAccess},
		{"7", scenarioMemberCoder},
		{"8", scenarioAutoRefusedCoderTruncatedFleet},
		{"8c", scenarioAutoCoderAfterPlatformData},
		{"9a", scenarioAutoOneProposal},
		{"9b", scenarioAutoNoProposal},
		{"10", scenarioForcedAnswerAfterRounds},
		{"11", scenarioRefusedConveneMembers},
		{"12", scenarioConveneFleetAtNone},
		{"13", scenarioAutoOneProposalLowEffort},
		{"14", scenarioAutoAnswerAtNone},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			h := sc.setup(t)
			h.start(t)
			p := h.waitFinish(t)
			h.stop(t)

			// The requests are recorded in the order the fake saw them, which is only
			// deterministic while one model call runs at a time. No scenario has two members that
			// call the model (the Coder does not: it runs opencode), so this holds; a step that
			// makes two model calls overlap must sort the requests instead.
			if got := h.model.maxConcurrent(); got > 1 {
				t.Fatalf("%d model calls were in flight at once: the recorded request order is racy", got)
			}
			// The coder tasks are recorded in the order the fake started them, which is only
			// deterministic while one coder task runs at a time. No scenario has two; a step that
			// starts two at once must sort them instead.
			if got := h.coder.maxConcurrent(); got > 1 {
				t.Fatalf("%d coder tasks were in flight at once: the recorded coder task order is racy", got)
			}
			// A scripted answer the fake never gave means the scenario did not run as written: a
			// comparison would pass on a golden of a flow that no longer happens, and a re-record
			// would write that flow as the baseline. Both modes check it.
			if left := h.model.leftover(); len(left) > 0 {
				t.Fatalf("the fake model was not asked for every scripted answer: %v left over", left)
			}
			checkGolden(t, sc.name, goldenDoc{
				Requests:   recordRequests(t, h.model),
				CoderTasks: recordCoderTasks(h.coder),
				ToolCalls:  recordToolCalls(t, h.tools),
				Events:     recordEvents(t, p),
				Finish:     recordFinish(p),
			})
		})
	}
}

// goldenHarness is the harness of a golden scenario. MergeEvery is far beyond the test's life, so
// the Captain's streamed text reaches the poster in one text_delta per flush instead of one per
// timer tick: the number of those events is then a property of the flow, not of the timing.
func goldenHarness(t *testing.T, work *hub.WorkTurn) *testHarness {
	t.Helper()
	h := newHarness(t, work)
	h.cfg.MergeEvery = time.Hour
	return h
}

// factsAt is the platform's answer about the bot at access level level: the same facts as the
// default, with its access the level the turn runs at, so the Captain's prompt does not contradict
// the level it is given.
func factsAt(level string) json.RawMessage {
	return json.RawMessage(strings.Replace(harnessFacts, `"access":"read"`, `"access":"`+level+`"`, 1))
}

// whoamiAt makes the harness answer with the level and the facts at that level.
func (h *testHarness) whoamiAt(level string) {
	h.whoami = func(context.Context) (string, json.RawMessage, error) {
		return level, factsAt(level), nil
	}
}

// ---------- the scenarios ----------

// scenarioQuickRead is a quick turn at read: an answer without tools.
func scenarioQuickRead(t *testing.T) *testHarness {
	work := newWork(kindAsk, "", "what is a robot?")
	work.Options.Mode = modeQuick
	h := goldenHarness(t, work)
	h.model.pushUsed(roleRoute, textStep("a robot is an edge computer", "end_turn"))
	return h
}

// scenarioAutoToolRound is an auto turn at read: one list_robots round, then an answer.
func scenarioAutoToolRound(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindAsk, "", "how many robots are there?"))
	h.model.pushUsed(roleRoute,
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots", input: map[string]any{}}),
		textStep("there are two robots", "end_turn"))
	return h
}

// councilReadHolds is what scenarios 3 and 4 run: a council turn at read or at creator.
func councilReadHolds(t *testing.T, level, fleetTool string) *testHarness {
	work := newWork(kindAsk, "", "why is vision-station-01 degraded?")
	work.Options.Mode = modeCouncil
	h := goldenHarness(t, work)
	h.whoamiAt(level)
	h.model.pushUsed(roleRoute, toolStep("tool_use", conveneCall("tu_1",
		taskOf(memberFleet, "the robot's problems", "find the facts"),
		taskOf(memberCoder, "a small dashboard", "the person asked for one"),
	)))
	h.model.pushUsed(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_call_1", name: fleetTool, input: map[string]any{}}),
		textStep(reportJSON("the vision app is degraded", "tu_call_1"), "end_turn"))
	h.model.pushUsed(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.pushUsed(roleAnswer, textStep("the vision app on vision-station-01 is degraded", "end_turn"))
	return h
}

// scenarioCouncilRead is the council at read: Fleet and the Coder are convened, Fleet takes one
// tool round, then the Critic and the synthesis. Fleet and the Coder run at once.
func scenarioCouncilRead(t *testing.T) *testHarness {
	return councilReadHolds(t, "read", "list_robots")
}

// scenarioCouncilCreator is the same at creator, where Fleet may look inside a pod.
func scenarioCouncilCreator(t *testing.T) *testHarness {
	return councilReadHolds(t, "creator", "inspect_pod")
}

// scenarioCouncilNone is the council at none: the Captain convenes only the Coder, and its prompt
// says so.
func scenarioCouncilNone(t *testing.T) *testHarness {
	work := newWork(kindAsk, "", "write a script that samples the temperature")
	work.Options.Mode = modeCouncil
	h := goldenHarness(t, work)
	h.whoamiAt("none")
	h.model.pushUsed(roleRoute, textStep("the Coder takes this one", "end_turn"))
	h.model.pushUsed(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.pushUsed(roleAnswer, textStep("the script is on the bot", "end_turn"))
	return h
}

// scenarioMemberFleetHistory is a member turn for Fleet at read, with two earlier exchanges.
func scenarioMemberFleetHistory(t *testing.T) *testHarness {
	work := newWork(kindMember, memberFleet, "which robots are degraded?")
	work.History = []hub.History{
		{Prompt: "what is a robot?", Final: "an edge computer", Author: &hub.Author{Email: "a@b.c", DisplayName: "Anders"}},
		{Prompt: "and the fleet?", Final: "two robots", Author: &hub.Author{Email: "c@d.e", DisplayName: "Chris"}},
	}
	h := goldenHarness(t, work)
	h.model.pushUsed(roleFleet, textStep(reportJSON("one robot is degraded"), "end_turn"))
	return h
}

// scenarioMemberFleetNoAccess is a member turn for Fleet at none: Fleet has no tools at that
// level, so the turn fails without a model call.
func scenarioMemberFleetNoAccess(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindMember, memberFleet, "which robots are degraded?"))
	h.whoamiAt("none")
	return h
}

// scenarioMemberCoder is a member turn for the Coder at read: no model call of the council's own.
func scenarioMemberCoder(t *testing.T) *testHarness {
	work := newWork(kindMember, memberCoder, "write a script that prints hello")
	h := goldenHarness(t, work)
	h.coder.report = coder.Report{State: coder.StateDone, Text: "I wrote hello.sh", Changed: []string{"A hello.sh"}}
	return h
}

// scenarioAutoRefusedCoderTruncatedFleet is an auto turn at read: the Captain reads platform data,
// convenes the Coder for a question that asks for no change (refused), then convenes Fleet, whose
// tool result is truncated, so the Critic runs on a report that rests on part of a result.
func scenarioAutoRefusedCoderTruncatedFleet(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindAsk, "", "why is r2 degraded?"))
	h.tools.results["list_robots"] = tools.Result{
		Text: "r2: the newest 20 events only", Full: "r2: the newest 20 events only", Digest: "r2's newest events", Truncated: true,
	}
	h.model.pushUsed(roleRoute,
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots", input: map[string]any{}}),
		toolStep("tool_use", conveneCall("tu_2", taskOf(memberCoder, "set the edge limit to 0", "the log says so"))),
		toolStep("tool_use", conveneCall("tu_3", taskOf(memberFleet, "the robot's problems", "find the facts"))),
	)
	h.model.pushUsed(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_call_1", name: "list_robots", input: map[string]any{}}),
		textStep(reportJSON("the edge is overloaded", "tu_call_1"), "end_turn"))
	h.model.pushUsed(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.pushUsed(roleAnswer, textStep("r2's edge is overloaded", "end_turn"))
	return h
}

// scenarioAutoCoderAfterPlatformData is an auto turn at read that asks for a change: the Captain
// reads platform data, then convenes the Coder, whose prompt leaves the Captain's notes out (the
// notes would be that data again). The fake coder reports progress and a step, so the turn's
// coder_update events and its usage arithmetic are recorded too.
func scenarioAutoCoderAfterPlatformData(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindAsk, "", "change the edge limit on r2 to 5"))
	h.model.pushUsed(roleRoute,
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots", input: map[string]any{}}),
		toolStep("tool_use", conveneCall("tu_2", taskOf(memberCoder, "set the edge limit on r2 to 5", "the person asked for it"))),
	)
	h.coder.run = func(_ context.Context, task coder.Task) coder.Report {
		task.OnUpdate(coder.Update{Kind: coder.KindMessage, Text: "the limit is set"})
		task.OnStep(goldenUsage)
		return coder.Report{State: coder.StateDone, Text: "I set the edge limit to 5", Changed: []string{"M robots/r2.yaml"}}
	}
	h.model.pushUsed(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.pushUsed(roleAnswer, textStep("r2's edge limit is 5", "end_turn"))
	return h
}

// proposalsReport is a valid report with n proposals, citing evidence.
func proposalsReport(summary string, proposals int, evidence ...string) string {
	ev := make([]any, 0, len(evidence))
	for _, id := range evidence {
		ev = append(ev, id)
	}
	list := make([]any, 0, proposals)
	for i := 0; i < proposals; i++ {
		list = append(list, map[string]any{"what": "change " + summary, "why": "the report"})
	}
	b, _ := json.Marshal(map[string]any{
		"summary":        summary,
		"findings":       []any{map[string]any{"claim": "a claim from " + summary, "evidence": ev}},
		"proposals":      list,
		"confidence":     "high",
		"open_questions": []any{},
	})
	return string(b)
}

// autoOneFleetTaskEffort is an auto turn at read whose Fleet task reports the given report, with
// the turn's effort set.
func autoOneFleetTaskEffort(t *testing.T, effort, summary, fleetReport string) *testHarness {
	work := newWork(kindAsk, "", "how many robots are degraded?")
	work.Options.Effort = effort
	h := goldenHarness(t, work)
	h.model.pushUsed(roleRoute, toolStep("tool_use", conveneCall("tu_1",
		taskOf(memberFleet, "the degraded robots", "find the facts"))))
	h.model.pushUsed(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_call_1", name: "list_robots", input: map[string]any{}}),
		textStep(fleetReport, "end_turn"))
	h.model.pushUsed(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.pushUsed(roleAnswer, textStep(summary, "end_turn"))
	return h
}

// autoOneFleetTask is autoOneFleetTaskEffort at the harness' effort.
func autoOneFleetTask(t *testing.T, summary, fleetReport string) *testHarness {
	return autoOneFleetTaskEffort(t, "", summary, fleetReport)
}

// scenarioAutoOneProposal is an auto turn at read: an untruncated Fleet result and a report with
// one proposal; the Critic runs.
func scenarioAutoOneProposal(t *testing.T) *testHarness {
	return autoOneFleetTask(t, "one robot is degraded", proposalsReport("one robot is degraded", 1, "tu_call_1"))
}

// scenarioAutoNoProposal is the same with no proposals. Today the Critic runs all the same; the
// step that makes the Critic conditional re-records this file on purpose.
func scenarioAutoNoProposal(t *testing.T) *testHarness {
	return autoOneFleetTask(t, "one robot is degraded", proposalsReport("one robot is degraded", 0, "tu_call_1"))
}

// scenarioAutoOneProposalLowEffort is scenario 9a with the turn's effort at low: the override shows
// in every request the turn makes.
func scenarioAutoOneProposalLowEffort(t *testing.T) *testHarness {
	return autoOneFleetTaskEffort(t, "low", "one robot is degraded", proposalsReport("one robot is degraded", 1, "tu_call_1"))
}

// scenarioForcedAnswerAfterRounds is an auto turn at read where Fleet spends every tool round it
// has, so the council forces the report with tool_choice none; Fleet's first round reads a guide
// and a result the tool cut, and its second pages the cut with read_result. Fleet's forced answer
// is a tool call the round limit does not run: the retry tells it so, with the not-run rounds
// result, and the report comes on that retry.
func scenarioForcedAnswerAfterRounds(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindAsk, "", "why is r2 degraded?"))
	h.model.pushUsed(roleRoute, toolStep("tool_use", conveneCall("tu_1",
		taskOf(memberFleet, "the robot's problems", "find the facts"))))

	// One long list_robots result, cut by the tool itself: the model sees part, and the turn keeps
	// the whole for read_result under r1.
	full := strings.Repeat("r2: an event\n", 100)
	cut := resultcut.Cut(full, 256)
	h.tools.queueResults("list_robots", tools.Result{
		Text: cut.Shown, Full: full, Cut: cut, Truncated: true, Digest: "r2's newest events",
	})

	steps := []*scriptStep{
		toolStep("tool_use",
			apiCall{id: "tu_s1", name: toolReadSkill, input: map[string]any{"name": "teknoir-platform", "offset": 0}},
			apiCall{id: "tu_call_1", name: "list_robots", input: map[string]any{}}),
		toolStep("tool_use",
			apiCall{id: "tu_r1", name: toolReadResult, input: map[string]any{"ref": "r1", "offset": cut.ContinueAt}},
			apiCall{id: "tu_call_2", name: "whoami", input: map[string]any{}}),
	}
	// The rest of Fleet's tool rounds: the first two rounds above are the first two of the limit.
	for i := 3; i <= maxFleetRounds; i++ {
		steps = append(steps, toolStep("tool_use",
			apiCall{id: fmt.Sprintf("tu_call_%d", i), name: "whoami", input: map[string]any{}}))
	}
	// The forced answer calls a tool, which is not run, and the retry answers with the report.
	steps = append(steps,
		toolStep("tool_use", apiCall{id: "tu_call_11", name: "whoami", input: map[string]any{}}),
		textStep(reportJSON("the edge is overloaded", "tu_call_1"), "end_turn"))
	h.model.pushUsed(roleFleet, steps...)

	h.model.pushUsed(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.pushUsed(roleAnswer, textStep("r2's edge is overloaded", "end_turn"))
	return h
}

// scenarioRefusedConveneMembers is an auto turn at read where the Captain convenes a member that
// does not exist, then a task without a member: both are refused, and the Captain answers itself.
func scenarioRefusedConveneMembers(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindAsk, "", "how many robots are degraded?"))
	h.model.pushUsed(roleRoute,
		toolStep("tool_use", conveneCall("tu_1", taskOf("pilot", "x", "the refusal's test"))),
		toolStep("tool_use", conveneCall("tu_2", taskOf("", "x", "the refusal's test"))),
		textStep("there are two robots", "end_turn"))
	return h
}

// scenarioConveneFleetAtNone is an auto turn at none where the Captain convenes Fleet: at that
// level no Fleet task may be convened, so the convene is refused and the Captain answers itself.
func scenarioConveneFleetAtNone(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindAsk, "", "how many robots are degraded?"))
	h.whoamiAt("none")
	h.model.pushUsed(roleRoute,
		toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the degraded robots", "find the facts"))),
		textStep("I cannot see the robots at this access level", "end_turn"))
	return h
}

// scenarioAutoAnswerAtNone is an auto turn at none: the Captain answers in text, as the level-none
// sentence of its prompt says.
func scenarioAutoAnswerAtNone(t *testing.T) *testHarness {
	h := goldenHarness(t, newWork(kindAsk, "", "how many robots are degraded?"))
	h.whoamiAt("none")
	h.model.pushUsed(roleRoute, textStep("I cannot see the robots at this access level", "end_turn"))
	return h
}

// ---------- recording ----------

// recordRequests is every request the fake model saw, in the order it saw them.
func recordRequests(t *testing.T, m *fakeModel) []goldenRequest {
	t.Helper()
	m.mu.Lock()
	reqs := append([]model.Request{}, m.reqs...)
	m.mu.Unlock()
	out := make([]goldenRequest, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, goldenRequest{
			Role:           roleOf(r),
			Model:          r.Model,
			Effort:         r.Effort,
			System:         r.System,
			MaxTokens:      r.MaxTokens,
			ToolChoiceNone: r.ToolChoiceNone,
			Format:         plainJSON(t, r.Format),
			Tools:          plainJSON(t, r.Tools),
			Messages:       plainJSON(t, r.Messages),
		})
	}
	return out
}

// recordCoderTasks is every coder task the fake started, in the order it started them.
func recordCoderTasks(c *fakeCoder) []goldenCoderTask {
	tasks := c.started()
	out := make([]goldenCoderTask, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, goldenCoderTask{Prompt: task.Prompt, Effort: task.Effort})
	}
	return out
}

// recordToolCalls is every tool call the fake tools saw, in call order, with the level it ran at
// and its input as plain JSON.
func recordToolCalls(t *testing.T, f *fakeTools) []goldenToolCall {
	t.Helper()
	f.mu.Lock()
	calls := append([]toolCallRec{}, f.calls...)
	f.mu.Unlock()
	out := make([]goldenToolCall, 0, len(calls))
	for _, c := range calls {
		var input any
		if err := json.Unmarshal([]byte(c.Input), &input); err != nil {
			t.Fatalf("tool call %s: input %q is not JSON: %v", c.Name, c.Input, err)
		}
		out = append(out, goldenToolCall{Level: c.Level, Name: c.Name, Input: input})
	}
	return out
}

// recordFinish is how the turn finished.
func recordFinish(p *fakePoster) goldenFinish {
	r := p.result()
	return goldenFinish{State: r.State, Final: r.Final, ErrCode: r.ErrCode, ErrMsg: r.ErrMsg}
}

// plainJSON is v as plain JSON values, the way it travels.
func plainJSON(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal %T: %v", v, err)
	}
	return out
}

// recordEvents is every event the poster got, with its timings dropped and the members' parallel
// work put in a stable order.
func recordEvents(t *testing.T, p *fakePoster) []goldenEvent {
	t.Helper()
	events := p.snapshot()
	out := make([]goldenEvent, 0, len(events))
	for _, e := range events {
		out = append(out, goldenEvent{Type: e.Type, Data: dropTimings(t, e)})
	}
	return orderMemberRuns(out)
}

// dropTimings is an event's data without the keys that vary between runs: every "ms" (a timing),
// at any depth. Nothing else in an event varies: the ids are scripted, and the reports, digests
// and usage counts all come from the scripted answers.
func dropTimings(t *testing.T, e fakeEvent) any {
	t.Helper()
	return dropMs(plainJSON(t, e.Data))
}

// dropMs removes every "ms" key from v, at any depth.
func dropMs(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "ms")
		for k, m := range x {
			x[k] = dropMs(m)
		}
		return x
	case []any:
		for i, m := range x {
			x[i] = dropMs(m)
		}
		return x
	}
	return v
}

// eventTask is the member task an event belongs to, or -1: the member events and coder updates
// carry it, and the orchestrator's own events do not.
func eventTask(e goldenEvent) int {
	d, ok := e.Data.(map[string]any)
	if !ok {
		return -1
	}
	v, ok := d["task"]
	if !ok {
		return -1
	}
	f, ok := v.(float64)
	if !ok {
		return -1
	}
	return int(f)
}

// orderMemberRuns orders the members' parallel work: workPhase runs the plan's members at once, so
// the events of two tasks are recorded in either order. Each task's own events keep their order,
// and the tasks are put in plan order. Their events are one contiguous run, because the
// orchestrator does nothing else inside workPhase (and goldenHarness keeps the merger's timer from
// flushing into it).
func orderMemberRuns(events []goldenEvent) []goldenEvent {
	out := append([]goldenEvent{}, events...)
	for i := 0; i < len(out); {
		if eventTask(out[i]) < 0 {
			i++
			continue
		}
		j := i
		for j < len(out) && eventTask(out[j]) >= 0 {
			j++
		}
		run := out[i:j]
		sort.SliceStable(run, func(a, b int) bool { return eventTask(run[a]) < eventTask(run[b]) })
		i = j
	}
	return out
}

// ---------- the files ----------

// checkGolden writes the scenario's file with -update, and otherwise compares it byte for byte.
func checkGolden(t *testing.T, name string, doc goldenDoc) {
	t.Helper()
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal the golden document: %v", err)
	}
	raw = append(raw, '\n')
	path := filepath.Join("testdata", "standard", name+".json")
	if updating() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("make %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		return
	}
	wantRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update to write it)", path, err)
	}
	if bytes.Equal(wantRaw, raw) {
		return
	}
	var want goldenDoc
	if err := json.Unmarshal(wantRaw, &want); err != nil {
		t.Fatalf("%s is not a golden document: %v", path, err)
	}
	t.Fatalf("%s does not match the standard baseline:\n%s", path, goldenDiff(want, doc, string(wantRaw), string(raw)))
}

// goldenDiff says what differs: the first request, coder task, tool call, event or finish that
// does, and the first line.
func goldenDiff(want, got goldenDoc, wantRaw, gotRaw string) string {
	var b strings.Builder
	if n := len(want.Requests); n != len(got.Requests) {
		fmt.Fprintf(&b, "the file has %d requests, this run %d\n", n, len(got.Requests))
	}
	for i := 0; i < len(want.Requests) && i < len(got.Requests); i++ {
		if !reflect.DeepEqual(want.Requests[i], got.Requests[i]) {
			fmt.Fprintf(&b, "requests[%d] (role %q) differs\n", i, got.Requests[i].Role)
			break
		}
	}
	if n := len(want.CoderTasks); n != len(got.CoderTasks) {
		fmt.Fprintf(&b, "the file has %d coder tasks, this run %d\n", n, len(got.CoderTasks))
	}
	for i := 0; i < len(want.CoderTasks) && i < len(got.CoderTasks); i++ {
		if !reflect.DeepEqual(want.CoderTasks[i], got.CoderTasks[i]) {
			fmt.Fprintf(&b, "coderTasks[%d] differs\n", i)
			break
		}
	}
	if n := len(want.ToolCalls); n != len(got.ToolCalls) {
		fmt.Fprintf(&b, "the file has %d tool calls, this run %d\n", n, len(got.ToolCalls))
	}
	for i := 0; i < len(want.ToolCalls) && i < len(got.ToolCalls); i++ {
		if !reflect.DeepEqual(want.ToolCalls[i], got.ToolCalls[i]) {
			fmt.Fprintf(&b, "toolCalls[%d] (%q at %s) differs\n", i, got.ToolCalls[i].Name, got.ToolCalls[i].Level)
			break
		}
	}
	if n := len(want.Events); n != len(got.Events) {
		fmt.Fprintf(&b, "the file has %d events, this run %d\n", n, len(got.Events))
	}
	for i := 0; i < len(want.Events) && i < len(got.Events); i++ {
		if !reflect.DeepEqual(want.Events[i], got.Events[i]) {
			fmt.Fprintf(&b, "events[%d] (%q) differs\n", i, got.Events[i].Type)
			break
		}
	}
	if !reflect.DeepEqual(want.Finish, got.Finish) {
		fmt.Fprintf(&b, "the finish differs: state %q, errCode %q\n", got.Finish.State, got.Finish.ErrCode)
	}
	b.WriteString(firstLineDiff(wantRaw, gotRaw))
	return b.String()
}

// firstLineDiff is the first line the two documents differ at.
func firstLineDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w == g {
			continue
		}
		return fmt.Sprintf("line %d:\n  file:     %q\n  recorded: %q\n", i+1, w, g)
	}
	return ""
}
