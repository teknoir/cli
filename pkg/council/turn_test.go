package council

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
)

// ---------- helpers ----------

// reportJSON is a valid report answer.
func reportJSON(summary string, evidence ...string) string {
	ev := make([]any, 0, len(evidence))
	for _, id := range evidence {
		ev = append(ev, id)
	}
	b, _ := json.Marshal(map[string]any{
		"summary":        summary,
		"findings":       []any{map[string]any{"claim": "a claim from " + summary, "evidence": ev}},
		"proposals":      []any{},
		"confidence":     "high",
		"open_questions": []any{},
	})
	return string(b)
}

// critiqueAnswer is a valid critique answer.
func critiqueAnswer() string {
	return `{"issues":[{"member":"fleet","severity":"note","claim":"a claim","why":"because"}]}`
}

// mixedStep is an answer with text and tool calls.
func mixedStep(stop, text string, calls ...apiCall) *scriptStep {
	toolStep := toolStep(stop, calls...)
	var v struct {
		Content []map[string]any `json:"content"`
	}
	_ = json.Unmarshal([]byte(toolStep.msg), &v)
	content := []map[string]any{{"type": "text", "text": text}}
	content = append(content, v.Content...)
	toolStep.msg = apiMessage(content, stop)
	return toolStep
}

// conveneCall is a convene_council tool call.
func conveneCall(id string, tasks ...map[string]any) apiCall {
	list := make([]any, 0, len(tasks))
	for _, t := range tasks {
		list = append(list, t)
	}
	return apiCall{id: id, name: toolConvene, input: map[string]any{"tasks": list}}
}

// taskOf is one task of a convene input.
func taskOf(member, task, why string) map[string]any {
	return map[string]any{"member": member, "task": task, "why": why}
}

// skillCall is a read_skill tool call.
func skillCall(id, name string) apiCall {
	return apiCall{id: id, name: toolReadSkill, input: map[string]any{"name": name}}
}

// toolNames are the names of a request's tools.
func toolNames(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.OfTool != nil {
			out = append(out, t.OfTool.Name)
		}
	}
	return out
}

// paramText is a message param's text.
func paramText(p anthropic.MessageParam) string {
	var b strings.Builder
	for _, c := range p.Content {
		if c.OfText != nil {
			b.WriteString(c.OfText.Text)
		}
	}
	return b.String()
}

// toolResults are the tool result texts of an answer's user message.
func toolResults(p anthropic.MessageParam) []string {
	var out []string
	for _, c := range p.Content {
		if c.OfToolResult != nil {
			for _, inner := range c.OfToolResult.Content {
				if inner.OfText != nil {
					out = append(out, inner.OfText.Text)
				}
			}
		}
	}
	return out
}

// hasText reports whether any of the strings contains want.
func hasText(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

// joinedDeltas is the text of a poster's text_delta events.
func joinedDeltas(t *testing.T, p *fakePoster) string {
	t.Helper()
	var b strings.Builder
	for _, e := range p.ofType(hub.EventTextDelta) {
		if s, ok := dataOf(t, e)["text"].(string); ok {
			b.WriteString(s)
		}
	}
	return b.String()
}

// ---------- 1. a quick turn ----------

func TestQuickTurn(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	h.model.push(roleRoute, textStep("a robot is an edge computer", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	types := p.types()
	order := eventOrder(types, hub.EventTurnStart, hub.EventTextDelta, hub.EventPlan, hub.EventUsage)
	if order[0] != 0 {
		t.Fatalf("the first event is not turn_start: %v", types)
	}
	if order[1] < 0 || order[1] >= order[2] || order[2] >= order[3] || order[3] != len(types)-1 {
		t.Fatalf("event order: %v", types)
	}
	if got := joinedDeltas(t, p); got != "a robot is an edge computer" {
		t.Errorf("text deltas %q", got)
	}
	plan := dataOf(t, p.ofType(hub.EventPlan)[0])
	if plan["mode"] != modeQuick {
		t.Errorf("plan mode %v", plan["mode"])
	}
	if tasks, ok := plan["tasks"].([]any); !ok || len(tasks) != 0 {
		t.Errorf("plan tasks %v", plan["tasks"])
	}
	res := p.result()
	if res.State != stateDone || res.Final != "a robot is an edge computer" {
		t.Errorf("finish %q %q %q", res.State, res.Final, res.ErrCode)
	}
	if !p.finishBeforeRunEnded() {
		t.Error("Run's context ended before Finish returned")
	}
	if res.Usage.Calls != 1 {
		t.Errorf("usage calls %d", res.Usage.Calls)
	}
	if start := dataOf(t, p.ofType(hub.EventTurnStart)[0]); start["mode"] != modeAuto {
		t.Errorf("turn_start mode %v", start["mode"])
	}
}

// ---------- 2. mode quick offers no convene ----------

func TestQuickModeHasNoConvene(t *testing.T) {
	work := newWork(kindAsk, "", "what is a robot?")
	work.Options.Mode = modeQuick
	h := newHarness(t, work)
	h.model.push(roleRoute, textStep("an edge computer", "end_turn"))
	h.start(t)
	h.waitFinish(t)
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) == 0 {
		t.Fatal("no route request")
	}
	for _, name := range toolNames(route[0]) {
		if name == toolConvene {
			t.Fatalf("a quick turn offers %s", toolConvene)
		}
	}
}

// ---------- 3. the council ----------

func TestCouncilTurn(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is vision-station-01 degraded?"))
	barrier := make(chan struct{})
	e1, e2 := make(chan struct{}), make(chan struct{})
	go func() {
		<-e1
		<-e2
		close(barrier)
	}()
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1",
		taskOf(memberFleet, "the robot's problems", "find the facts"),
		taskOf(memberFleet, "the device's state", "find the facts"),
		taskOf(memberCoder, "a small dashboard", "the person asked for one"),
	)))
	h.model.push(roleFleet,
		&scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": reportJSON("fleet one")}}, "end_turn"), block: barrier, entered: e1},
		&scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": reportJSON("fleet two")}}, "end_turn"), block: barrier, entered: e2},
	)
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final answer", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if got := h.model.maxConcurrent(); got < 2 {
		t.Errorf("the Fleet tasks did not run at once: %d calls in flight", got)
	}
	res := p.result()
	if res.State != stateDone || res.Final != "the final answer" {
		t.Fatalf("finish %q %q %q %q", res.State, res.Final, res.ErrCode, res.ErrMsg)
	}
	if got := joinedDeltas(t, p); got != "the final answer" {
		t.Errorf("text deltas %q", got)
	}
	plan := dataOf(t, p.ofType(hub.EventPlan)[0])
	tasks, ok := plan["tasks"].([]any)
	if plan["mode"] != modeCouncil || !ok || len(tasks) != 3 {
		t.Fatalf("plan %v", plan)
	}
	if m := tasks[2].(map[string]any)["member"]; m != memberCoder {
		t.Errorf("third task %v", m)
	}

	// Every member started and finished, in index order.
	startOrder := make([]int, 0, 3)
	for _, e := range p.ofType(hub.EventMemberStart) {
		startOrder = append(startOrder, int(dataOf(t, e)["task"].(float64)))
	}
	if len(startOrder) != 3 {
		t.Errorf("member_start events: %v", startOrder)
	}
	doneOrder := make([]int, 0, 3)
	for _, e := range p.ofType(hub.EventMemberDone) {
		doneOrder = append(doneOrder, int(dataOf(t, e)["task"].(float64)))
	}
	if len(doneOrder) != 3 {
		t.Errorf("member_done events: %v", doneOrder)
	}

	// The synthesis got the reports and the critique.
	answers := h.model.requests(roleAnswer)
	if len(answers) != 1 {
		t.Fatalf("%d answer requests", len(answers))
	}
	last := paramText(answers[0].Messages[len(answers[0].Messages)-1])
	for _, want := range []string{"fleet one", "fleet two", critiqueAnswer()} {
		if !strings.Contains(last, want) {
			t.Errorf("the synthesis is missing %q: %s", want, last)
		}
	}
	// The Critic got the digests of the members' tool calls.
	critic := h.model.requests(roleCritic)
	if len(critic) != 1 {
		t.Fatalf("%d critic requests", len(critic))
	}
	if p.ofType(hub.EventCritique) == nil {
		t.Error("no critique event")
	}
}

// With one model slot, the second Fleet call does not start while the first holds it.
func TestCouncilTurnModelSlots(t *testing.T) {
	work := newWork(kindAsk, "", "why is the robot degraded?")
	h := newHarness(t, work)
	h.cfg.ModelSlots = 1
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1",
		taskOf(memberFleet, "a", "x"), taskOf(memberFleet, "b", "y"))))
	// The first Fleet call waits for the test; the second must not get a slot while it does.
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	h.model.push(roleFleet,
		&scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": reportJSON("one")}}, "end_turn"), block: release, entered: entered},
		&scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": reportJSON("two")}}, "end_turn"), entered: entered})
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no Fleet call started")
	}
	select {
	case <-entered:
		t.Fatal("the second Fleet call started while the first held the only slot")
	case <-time.After(50 * time.Millisecond):
	}
	if got := h.model.maxConcurrent(); got != 1 {
		t.Errorf("%d model calls overlapped with one slot", got)
	}
	close(release)
	p := h.waitFinish(t)
	h.stop(t)
	if got := h.model.maxConcurrent(); got != 1 {
		t.Errorf("%d model calls overlapped with one slot", got)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the final" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// ---------- 4. mode council with no convene ----------

func TestCouncilModeWithoutConvene(t *testing.T) {
	work := newWork(kindAsk, "", "what is the state of the fleet?")
	work.Options.Mode = modeCouncil
	h := newHarness(t, work)
	h.model.push(roleRoute, textStep("I will look", "end_turn"))
	h.model.push(roleFleet, textStep(reportJSON("fleet's report"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	plan := dataOf(t, p.ofType(hub.EventPlan)[0])
	tasks := plan["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["member"] != memberFleet {
		t.Fatalf("plan %v", plan)
	}
	// The Fleet task is the prompt itself.
	fleet := h.model.requests(roleFleet)
	if len(fleet) != 1 || !strings.Contains(paramText(fleet[0].Messages[0]), "what is the state of the fleet?") {
		t.Errorf("fleet request: %v", fleet)
	}
	if p.result().Final != "the final" {
		t.Errorf("final %q", p.result().Final)
	}
}

func TestCouncilModeCodeAsked(t *testing.T) {
	work := newWork(kindAsk, "", "write a script that samples the temperature")
	work.Options.Mode = modeCouncil
	h := newHarness(t, work)
	h.model.push(roleRoute, textStep("ok", "end_turn"))
	h.model.push(roleFleet, textStep(reportJSON("fleet's report"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	plan := dataOf(t, p.ofType(hub.EventPlan)[0])
	tasks := plan["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("plan %v", plan)
	}
	if tasks[1].(map[string]any)["member"] != memberCoder {
		t.Errorf("second task %v", tasks[1])
	}
	if len(h.coder.started()) != 1 {
		t.Errorf("the Coder ran %d times", len(h.coder.started()))
	}
}

func TestCouncilModeAtLevelNone(t *testing.T) {
	work := newWork(kindAsk, "", "what is the state of the fleet?")
	work.Options.Mode = modeCouncil
	h := newHarness(t, work)
	h.whoami = func(context.Context) (string, json.RawMessage, error) {
		return "none", json.RawMessage(harnessFacts), nil
	}
	h.model.push(roleRoute, textStep("I answer myself", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	// Nothing can be convened: the turn is quick.
	plan := dataOf(t, p.ofType(hub.EventPlan)[0])
	if plan["mode"] != modeQuick {
		t.Fatalf("plan %v", plan)
	}
	if p.result().Final != "I answer myself" {
		t.Errorf("final %q", p.result().Final)
	}
	if len(h.model.requests(roleFleet)) != 0 {
		t.Error("Fleet was called at level none")
	}
}

// ---------- 5. an invalid convene ----------

func TestInvalidConvene(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	invalid := toolStep("tool_use", conveneCall("tu_1",
		taskOf(memberCoder, "one", "x"), taskOf(memberCoder, "two", "y")))
	valid := toolStep("tool_use", conveneCall("tu_2", taskOf(memberFleet, "the facts", "x")))
	h.model.push(roleRoute, invalid, valid)
	h.model.push(roleFleet, textStep(reportJSON("the report"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) != 2 {
		t.Fatalf("%d route requests", len(route))
	}
	last := route[1].Messages[len(route[1].Messages)-1]
	results := toolResults(last)
	if !hasText(results, "at most one coder task") {
		t.Errorf("the convene error is not in the tool results: %v", results)
	}
	plan := dataOf(t, p.ofType(hub.EventPlan)[0])
	tasks := plan["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["member"] != memberFleet {
		t.Fatalf("the plan is not the valid convene: %v", plan)
	}
}

// ---------- 6. Fleet's report parsing ----------

func TestFleetInvalidReport(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	h.model.push(roleFleet,
		textStep("this is not JSON", "end_turn"),
		textStep("still not JSON", "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	fleet := h.model.requests(roleFleet)
	if len(fleet) != 2 {
		t.Fatalf("%d fleet requests", len(fleet))
	}
	if !fleet[1].ToolChoiceNone {
		t.Error("the retry does not force an answer")
	}
	retry := paramText(fleet[1].Messages[len(fleet[1].Messages)-1])
	if !strings.Contains(retry, "That was not a valid report:") {
		t.Errorf("the retry prompt %q", retry)
	}
	var done map[string]any
	for _, e := range p.ofType(hub.EventMemberDone) {
		done = dataOf(t, e)
	}
	rep := done["report"].(map[string]any)
	if rep["summary"] != "still not JSON" || rep["confidence"] != "low" {
		t.Errorf("the raw report: %v", rep)
	}
}

func TestFleetEvidenceKept(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	h.model.push(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_call_1", name: "list_robots"}),
		textStep(reportJSON("the report", "tu_call_1", "tu_other"), "end_turn"))
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
	findings := rep["findings"].([]any)
	ev := findings[0].(map[string]any)["evidence"].([]any)
	if len(ev) != 1 || ev[0] != "tu_call_1" {
		t.Errorf("evidence %v", ev)
	}
}

// ---------- 7. Fleet's round limit ----------

func TestFleetRoundLimit(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	rounds := make([]*scriptStep, 0, maxFleetRounds)
	for i := 0; i < maxFleetRounds; i++ {
		rounds = append(rounds, toolStep("tool_use", apiCall{id: "tu_r" + string(rune('a'+i)), name: "list_robots"}))
	}
	rounds = append(rounds, textStep(reportJSON("the report"), "end_turn"))
	h.model.push(roleFleet, rounds...)
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	h.waitFinish(t)
	h.stop(t)

	fleet := h.model.requests(roleFleet)
	if len(fleet) != maxFleetRounds+1 {
		t.Fatalf("%d fleet requests", len(fleet))
	}
	if fleet[maxFleetRounds-1].ToolChoiceNone {
		t.Error("the 10th call already forces an answer")
	}
	if !fleet[maxFleetRounds].ToolChoiceNone {
		t.Error("the 11th call does not force an answer")
	}
}

// ---------- 8. the turn caps ----------

// councilWithRepeatingFleet sets up the route convening Fleet tasks that keep calling tools.
func councilWithRepeatingFleet(t *testing.T, tasks int, routeUsage model.Usage) *testHarness {
	t.Helper()
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	list := make([]map[string]any, 0, tasks)
	for i := 0; i < tasks; i++ {
		list = append(list, taskOf(memberFleet, "a", "x"))
	}
	convene := toolStep("tool_use", conveneCall("tu_1", list...))
	convene.usage = routeUsage
	h.model.push(roleRoute, convene)
	h.model.push(roleFleet, &scriptStep{
		msg:    apiMessage([]map[string]any{{"type": "tool_use", "id": "tu_loop", "name": "list_robots", "input": map[string]any{}}}, "tool_use"),
		repeat: true,
	})
	return h
}

func TestCallCap(t *testing.T) {
	// Three Fleet tasks of up to eleven calls each spend the council's thirty calls.
	h := councilWithRepeatingFleet(t, 3, model.Usage{})
	h.model.push(roleAnswer, textStep("the answer after the cap", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 {
		t.Fatalf("%d budget events: %v", len(budgets), budgets)
	}
	if what := dataOf(t, budgets[0])["what"]; what != "calls" {
		t.Errorf("budget what %v", what)
	}
	if len(p.ofType(hub.EventCritique)) != 0 {
		t.Error("the critique ran after the budget refused it")
	}
	answers := h.model.requests(roleAnswer)
	if len(answers) != 1 || !answers[0].ToolChoiceNone {
		t.Fatalf("the answer call: %v", answers)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the answer after the cap" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

func TestTokenCap(t *testing.T) {
	h := councilWithRepeatingFleet(t, 2, model.Usage{Input: 480_000})
	h.model.push(roleAnswer, textStep("the answer after the cap", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 {
		t.Fatalf("%d budget events: %v", len(budgets), budgets)
	}
	d := dataOf(t, budgets[0])
	if d["what"] != "tokens" {
		t.Errorf("budget what %v", d["what"])
	}
	if d["limit"].(float64) != float64(capsCouncil.tokens) {
		t.Errorf("budget limit %v", d["limit"])
	}
	if len(p.ofType(hub.EventCritique)) != 0 {
		t.Error("the critique ran after the budget refused it")
	}
	answers := h.model.requests(roleAnswer)
	if len(answers) != 1 || !answers[0].ToolChoiceNone {
		t.Fatalf("the answer call: %v", answers)
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("state %q", res.State)
	}
}

// ---------- 9. time ----------

func TestTimeFleet(t *testing.T) {
	work := newWork(kindAsk, "", "why is the robot degraded?")
	work.Deadline = time.Now().Add(1500 * time.Millisecond).UnixMilli()
	h := newHarness(t, work)
	h.cfg.WorkReserve = 1000 * time.Millisecond
	h.cfg.AnswerGrace = 2 * time.Second
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	// The call waits on its own context, never on a sleep.
	h.model.push(roleFleet, &scriptStep{
		msg:   apiMessage([]map[string]any{{"type": "text", "text": reportJSON("too late")}}, "end_turn"),
		block: make(chan struct{}),
	})
	h.model.push(roleAnswer, textStep("the answer anyway", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 || dataOf(t, budgets[0])["what"] != "time" {
		t.Fatalf("budget events: %v", budgets)
	}
	var done map[string]any
	for _, e := range p.ofType(hub.EventMemberDone) {
		done = dataOf(t, e)
	}
	rep := done["report"].(map[string]any)
	if !strings.Contains(rep["summary"].(string), "Fleet stopped: time.") {
		t.Errorf("the stopped report: %v", rep)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the answer anyway" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

func TestTimeRoute(t *testing.T) {
	work := newWork(kindAsk, "", "what is a robot?")
	work.Deadline = time.Now().Add(1500 * time.Millisecond).UnixMilli()
	h := newHarness(t, work)
	h.cfg.WorkReserve = 1000 * time.Millisecond
	h.cfg.AnswerGrace = 2 * time.Second
	// The call waits on its own context, never on a sleep.
	h.model.push(roleRoute,
		&scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": "too late"}}, "end_turn"), block: make(chan struct{})},
		textStep("the answer on what we have", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 || dataOf(t, budgets[0])["what"] != "time" {
		t.Fatalf("budget events: %v", budgets)
	}
	route := h.model.requests(roleRoute)
	if len(route) != 2 || !route[1].ToolChoiceNone {
		t.Fatalf("the answer call: %v", route)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the answer on what we have" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// ---------- 10. the turn closed ----------

func TestTurnClosedWhileFleet(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	block := make(chan struct{})
	defer close(block)
	entered := make(chan struct{})
	h.model.push(roleFleet, &scriptStep{
		msg:     apiMessage([]map[string]any{{"type": "text", "text": reportJSON("late")}}, "end_turn"),
		block:   block,
		entered: entered,
	})
	h.start(t)
	p := h.poster(t)
	<-entered
	p.onClosed(hub.ClosedTurnClosed)
	h.waitFinish(t)
	h.stop(t)

	res := p.result()
	if res.State != stateFailed || res.ErrCode != codeTurnClosed {
		t.Fatalf("finish %q %q %q", res.State, res.ErrCode, res.Final)
	}
	if len(p.ofType(hub.EventUsage)) != 0 {
		t.Error("a closed turn emitted usage")
	}
	if len(h.model.requests(roleCritic)) != 0 || len(h.model.requests(roleAnswer)) != 0 {
		t.Error("a closed turn made further model calls")
	}
	if !p.finishBeforeRunEnded() {
		t.Error("Run's context ended before Finish returned")
	}
	if n := p.finishCount(); n != 1 {
		t.Errorf("Finish was called %d times", n)
	}
}

func TestTurnLostWhileRoute(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	block := make(chan struct{})
	defer close(block)
	entered := make(chan struct{})
	h.model.push(roleRoute, &scriptStep{
		msg:     apiMessage([]map[string]any{{"type": "text", "text": "late"}}, "end_turn"),
		block:   block,
		entered: entered,
	})
	h.start(t)
	p := h.poster(t)
	<-entered
	p.onClosed(hub.ClosedLost)
	h.waitFinish(t)
	h.stop(t)

	if res := p.result(); res.State != stateFailed || res.ErrCode != codeEventsLost {
		t.Fatalf("finish %q %q", res.State, res.ErrCode)
	}
	if !strings.Contains(p.result().ErrMsg, "could not deliver") {
		t.Errorf("message %q", p.result().ErrMsg)
	}
}

// ---------- 11. member turns ----------

func TestMemberTurnFleet(t *testing.T) {
	h := newHarness(t, newWork(kindMember, memberFleet, "which robots are degraded?"))
	h.model.push(roleFleet, textStep(reportJSON("the fleet's facts"), "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	var want report
	want, err := parseReport(reportJSON("the fleet's facts"))
	if err != nil {
		t.Fatal(err)
	}
	res := p.result()
	if res.State != stateDone || res.Final != want.markdown() {
		t.Fatalf("finish %q %q", res.State, res.Final)
	}
	if len(h.model.requests(roleCritic)) != 0 || len(h.model.requests(roleAnswer)) != 0 {
		t.Error("a member turn called the Critic or the Captain")
	}
	if start := dataOf(t, p.ofType(hub.EventTurnStart)[0]); start["mode"] != modeMember {
		t.Errorf("turn_start mode %v", start["mode"])
	}
}

func TestMemberTurnCoder(t *testing.T) {
	h := newHarness(t, newWork(kindMember, memberCoder, "write a script that prints hello"))
	steps := 0
	h.coder.run = func(ctx context.Context, task coder.Task) coder.Report {
		for i := 0; i < 3; i++ {
			task.OnUpdate(coder.Update{Kind: coder.KindMessage, Text: "step"})
			if task.OnStep(model.Usage{Input: 70_000}) {
				steps++
			}
		}
		return coder.Report{State: coder.StateDone, Text: "I wrote it", Changed: []string{"M hello.py"}}
	}
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if steps != 2 {
		t.Errorf("%d steps allowed, want 2", steps)
	}
	if got := len(p.ofType(hub.EventCoderUpdate)); got != 3 {
		t.Errorf("%d coder_update events", got)
	}
	if res := p.result(); res.State != stateDone || !strings.Contains(res.Final, "I wrote it") {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
	if len(h.coder.started()) != 1 {
		t.Errorf("the Coder ran %d times", len(h.coder.started()))
	}
}

func TestMemberTurnFleetNoAccess(t *testing.T) {
	h := newHarness(t, newWork(kindMember, memberFleet, "which robots are degraded?"))
	h.whoami = func(context.Context) (string, json.RawMessage, error) {
		return "none", json.RawMessage(harnessFacts), nil
	}
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if res := p.result(); res.State != stateFailed || res.ErrCode != codeNoAccess {
		t.Fatalf("finish %q %q", res.State, res.ErrCode)
	}
	if len(h.model.requests(roleFleet)) != 0 {
		t.Error("Fleet was called at level none")
	}
}

// ---------- 12. model errors ----------

func TestRouteModelError(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	h.model.push(roleRoute, &scriptStep{err: &model.StatusError{Status: 302, URL: "https://example.test"}})
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	res := p.result()
	if res.State != stateFailed || res.ErrCode != codeModelError {
		t.Fatalf("finish %q %q", res.State, res.ErrCode)
	}
	if res.ErrMsg != "The model could not be reached (status 302)." {
		t.Errorf("message %q", res.ErrMsg)
	}
}

func TestSynthesisModelError(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	h.model.push(roleFleet, textStep(reportJSON("the fleet's report"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, &scriptStep{err: errors.New("connection reset")})
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	res := p.result()
	if res.State != stateDone {
		t.Fatalf("state %q", res.State)
	}
	if !strings.Contains(res.Final, "(model error)") || !strings.Contains(res.Final, "the fleet's report") {
		t.Errorf("the composed answer: %q", res.Final)
	}
}

func TestQuickRefusal(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	h.model.push(roleRoute, textStep("I cannot help with that", "refusal"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if res := p.result(); res.State != stateFailed || res.ErrCode != codeModelRefused {
		t.Fatalf("finish %q %q", res.State, res.ErrCode)
	}
}

// ---------- 13. the Captain's streamed text ----------

func TestCaptainTextOrder(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	h.model.push(roleRoute,
		mixedStep("tool_use", "let me read", skillCall("tu_1", "teknoir-platform")),
		textStep("a robot is an edge computer", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	types := p.types()
	order := eventOrder(types, hub.EventTextDelta, hub.EventToolCall, hub.EventPlan)
	if order[0] < 0 || order[1] <= order[0] || order[2] <= order[1] {
		t.Fatalf("event order: %v", types)
	}
	if got := joinedDeltas(t, p); got != "let me reada robot is an edge computer" {
		t.Errorf("text deltas %q", got)
	}
	if res := p.result(); res.Final != "a robot is an edge computer" {
		t.Errorf("final %q", res.Final)
	}
}

// ---------- 16. the log ----------

func TestLogHasNoPromptOrText(t *testing.T) {
	const prompt = "the-secret-prompt-42"
	h := newHarness(t, newWork(kindAsk, "", prompt))
	h.model.push(roleRoute, textStep("the-secret-answer-42", "end_turn"))
	h.start(t)
	h.waitFinish(t)
	h.stop(t)

	lines := h.logs.all()
	if len(lines) == 0 {
		t.Fatal("no log lines")
	}
	for _, line := range lines {
		if strings.Contains(line, prompt) || strings.Contains(line, "the-secret-answer-42") {
			t.Errorf("a log line carries the turn's text: %q", line)
		}
	}
}

// ---------- 17. the history ----------

func TestHistoryMessages(t *testing.T) {
	work := newWork(kindAsk, "", "and now?")
	work.History = []hub.History{
		{Prompt: "q1", Final: "a1"},
		{Prompt: "q2", Final: ""},
	}
	h := newHarness(t, work)
	h.model.push(roleRoute, textStep("an answer", "end_turn"))
	h.start(t)
	h.waitFinish(t)
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) != 1 {
		t.Fatalf("%d route requests", len(route))
	}
	msgs := route[0].Messages
	if len(msgs) != 3 {
		t.Fatalf("%d messages: %v", len(msgs), msgs)
	}
	if paramText(msgs[0]) != "q1" || paramText(msgs[1]) != "a1" || paramText(msgs[2]) != "and now?" {
		t.Errorf("messages %q %q %q", paramText(msgs[0]), paramText(msgs[1]), paramText(msgs[2]))
	}
	if msgs[1].Role != anthropic.MessageParamRoleAssistant {
		t.Errorf("the second message is %q", msgs[1].Role)
	}
}

// ---------- 18. effort ----------

func TestEffort(t *testing.T) {
	for _, tc := range []struct {
		turn                  string
		captain, fleet, other string
	}{
		{"xhigh", "high", "medium", "medium"},
		{"low", "low", "low", "low"},
	} {
		t.Run(tc.turn, func(t *testing.T) {
			work := newWork(kindAsk, "", "why is the robot degraded?")
			work.Options.Effort = tc.turn
			h := newHarness(t, work)
			h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
			h.model.push(roleFleet, textStep(reportJSON("the report"), "end_turn"))
			h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
			h.model.push(roleAnswer, textStep("the final", "end_turn"))
			h.start(t)
			h.waitFinish(t)
			h.stop(t)

			for _, r := range h.model.requests(roleRoute) {
				if r.Effort != tc.captain {
					t.Errorf("the route's effort %q", r.Effort)
				}
			}
			for _, r := range h.model.requests(roleAnswer) {
				if r.Effort != tc.captain {
					t.Errorf("the answer's effort %q", r.Effort)
				}
			}
			for _, r := range h.model.requests(roleFleet) {
				if r.Effort != tc.fleet {
					t.Errorf("Fleet's effort %q", r.Effort)
				}
			}
			for _, r := range h.model.requests(roleCritic) {
				if r.Effort != tc.other {
					t.Errorf("the Critic's effort %q", r.Effort)
				}
			}
		})
	}
}

// ---------- 19. a cut-off tool call is not run ----------

func TestCutOffToolCall(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	h.model.push(roleRoute, mixedStep("max_tokens", "a robot is", apiCall{id: "tu_1", name: "list_robots"}))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if calls := h.tools.called(); len(calls) != 0 {
		t.Errorf("a cut-off tool call ran: %v", calls)
	}
	res := p.result()
	if res.State != stateDone || !strings.Contains(res.Final, cutAtLength) {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// ---------- a bad turn ----------

func TestBadTurn(t *testing.T) {
	for _, work := range []*hub.WorkTurn{
		newWork("nonsense", "", "hi"),
		newWork(kindMember, "captain", "hi"),
	} {
		h := newHarness(t, work)
		h.start(t)
		p := h.waitFinish(t)
		h.stop(t)
		if res := p.result(); res.State != stateFailed || res.ErrCode != codeBadTurn {
			t.Errorf("%s/%s: finish %q %q", work.Kind, work.Member, res.State, res.ErrCode)
		}
		if h.model.count() != 0 {
			t.Error("a bad turn made a model call")
		}
	}
}

// ---------- the platform is down ----------

func TestWhoamiRetry(t *testing.T) {
	calls := 0
	h := newHarness(t, newWork(kindAsk, "", "hi"))
	h.whoami = func(context.Context) (string, json.RawMessage, error) {
		calls++
		return "", nil, errors.New("503")
	}
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if res := p.result(); res.State != stateFailed || res.ErrCode != codePlatformDown {
		t.Fatalf("finish %q %q", res.State, res.ErrCode)
	}
	if calls != 2 {
		t.Errorf("%d whoami calls, want 2", calls)
	}
}

// At level none a council turn convenes only the Coder, and only when the prompt asks for code.
func TestCouncilModeAtLevelNoneWithCode(t *testing.T) {
	work := newWork(kindAsk, "", "write a script that samples the temperature")
	work.Options.Mode = modeCouncil
	h := newHarness(t, work)
	h.whoami = func(context.Context) (string, json.RawMessage, error) {
		return "none", json.RawMessage(harnessFacts), nil
	}
	h.model.push(roleRoute, textStep("ok", "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	plan := dataOf(t, p.ofType(hub.EventPlan)[0])
	tasks := plan["tasks"].([]any)
	if len(tasks) != 1 || tasks[0].(map[string]any)["member"] != memberCoder {
		t.Fatalf("plan %v", plan)
	}
	if len(h.model.requests(roleFleet)) != 0 {
		t.Error("Fleet was called at level none")
	}
	if res := p.result(); res.State != stateDone {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// Fleet's own token limit ends its loop with a forced answer and no budget event.
func TestFleetOwnTokenLimit(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	big := func(call string) *scriptStep {
		st := toolStep("tool_use", apiCall{id: call, name: "list_robots"})
		st.usage = model.Usage{Input: 70_000}
		return st
	}
	answer := textStep(reportJSON("the report"), "end_turn")
	answer.usage = model.Usage{Input: 10}
	h.model.push(roleFleet, big("tu_a"), big("tu_b"), answer)
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	fleet := h.model.requests(roleFleet)
	if len(fleet) != 3 {
		t.Fatalf("%d fleet requests", len(fleet))
	}
	if !fleet[2].ToolChoiceNone {
		t.Error("Fleet's answer after its own token limit is not forced")
	}
	for _, e := range p.ofType(hub.EventBudget) {
		if d := dataOf(t, e); d["what"] != "time" {
			t.Errorf("Fleet emitted a budget event: %v", d)
		}
	}
	if res := p.result(); res.State != stateDone || res.Final != "the final" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// An answer call the budget refuses ends the turn in the composed answer, done.
func TestAnswerCallRefused(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	work := newWork(kindAsk, "", "why is the robot degraded?")
	turn := d.newTurn(work)
	p := newFakePoster(work)
	turn.poster = p
	turn.merge = newMerger(p, memberCaptain, time.Millisecond, 1<<20)
	// A turn whose budget has no call left: even the answer call is refused.
	turn.budget = newBudget(caps{calls: 0})
	reports := []memberResult{{member: memberFleet, task: 0, fleet: &report{Summary: "the facts", Confidence: confidenceHigh}}}

	o := turn.synthesize("read", json.RawMessage(harnessFacts), reports, critique{})
	if o.state != stateDone {
		t.Fatalf("state %q", o.state)
	}
	if !strings.Contains(o.final, "(budget)") || !strings.Contains(o.final, "the facts") {
		t.Errorf("the composed answer: %q", o.final)
	}
	if n := len(h.model.requests(roleAnswer)); n != 0 {
		t.Errorf("%d synthesis calls were made", n)
	}
	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 || dataOf(t, budgets[0])["what"] != "calls" {
		t.Errorf("budget events: %v", budgets)
	}
}

// A turn the platform rejects finishes as events_rejected, and the answer still goes out.
func TestTurnRejected(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	block := make(chan struct{})
	defer close(block)
	entered := make(chan struct{}, 1)
	h.model.push(roleRoute, &scriptStep{
		msg:     apiMessage([]map[string]any{{"type": "text", "text": "late"}}, "end_turn"),
		block:   block,
		entered: entered,
	})
	h.start(t)
	p := h.poster(t)
	<-entered
	p.onClosed(hub.ClosedRejected)
	h.waitFinish(t)
	h.stop(t)

	if res := p.result(); res.State != stateFailed || res.ErrCode != codeEventsRejected {
		t.Fatalf("finish %q %q", res.State, res.ErrCode)
	}
	if !strings.Contains(p.result().ErrMsg, "refused this turn's progress") {
		t.Errorf("message %q", p.result().ErrMsg)
	}
}

// A member turn's Fleet call keeps the tool events of the task it belongs to.
func TestMemberEventsCarryTheTask(t *testing.T) {
	h := newHarness(t, newWork(kindMember, memberFleet, "which robots are degraded?"))
	h.model.push(roleFleet,
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots"}),
		textStep(reportJSON("the facts"), "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	for _, e := range append(p.ofType(hub.EventMemberStart), p.ofType(hub.EventToolCall)...) {
		d := dataOf(t, e)
		if _, ok := d["task"]; !ok {
			t.Errorf("%s has no task: %v", e.Type, d)
		}
		if d["member"] != memberFleet {
			t.Errorf("%s member %v", e.Type, d["member"])
		}
	}
	if got := h.tools.called(); len(got) != 1 || got[0] != "list_robots" {
		t.Errorf("tool calls %v", got)
	}
}

// ---------- the synthesis runs its tools on a live context ----------

// A synthesis that calls a tool and then answers: the tool runs, its result goes back, and the
// answer is the synthesis' text.
func TestSynthesisTool(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	h.model.push(roleFleet, textStep(reportJSON("the fleet's report"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer,
		toolStep("tool_use", apiCall{id: "tu_s1", name: "list_robots", input: map[string]any{}}),
		textStep("the verified answer", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if got := h.tools.called(); len(got) != 1 || got[0] != "list_robots" {
		t.Errorf("the synthesis' tool calls: %v", got)
	}
	results := p.ofType(hub.EventToolResult)
	if len(results) != 1 {
		t.Fatalf("%d tool_result events", len(results))
	}
	if d := dataOf(t, results[0]); d["member"] != memberCaptain || d["id"] != "tu_s1" {
		t.Errorf("the synthesis' tool_result %v", d)
	}
	res := p.result()
	if res.State != stateDone || res.Final != "the verified answer" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// A synthesis that keeps calling tools until the budget refuses one: one budget event, then the
// answer call with ToolChoiceNone, and the final is that call's text.
func TestSynthesisCallCap(t *testing.T) {
	work := newWork(kindAsk, "", "why is the robot degraded?")
	h := newHarness(t, work)
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turn := d.newTurn(work)
	p := newFakePoster(work)
	turn.poster = p
	turn.merge = newMerger(p, memberCaptain, time.Millisecond, 1<<20)
	// Two phase calls are left, and the answer call after them; the tokens are not the limit.
	turn.budget = newBudget(caps{calls: 3, tokens: 1_000_000})
	h.model.push(roleAnswer,
		toolStep("tool_use", apiCall{id: "tu_s1", name: "list_robots", input: map[string]any{}}),
		toolStep("tool_use", apiCall{id: "tu_s2", name: "list_robots", input: map[string]any{}}),
		textStep("the answer after the cap", "end_turn"))
	reports := []memberResult{{member: memberFleet, task: 0, fleet: &report{Summary: "the facts", Confidence: confidenceHigh}}}

	o := turn.synthesize("read", json.RawMessage(harnessFacts), reports, critique{})
	if o.state != stateDone || o.final != "the answer after the cap" {
		t.Fatalf("outcome %q %q %q", o.state, o.final, o.errCode)
	}
	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 {
		t.Fatalf("%d budget events: %v", len(budgets), budgets)
	}
	if what := dataOf(t, budgets[0])["what"]; what != "calls" {
		t.Errorf("budget what %v", what)
	}
	answers := h.model.requests(roleAnswer)
	if len(answers) != 3 {
		t.Fatalf("%d synthesis calls: two tool rounds and the answer call", len(answers))
	}
	for _, r := range answers[:2] {
		if r.ToolChoiceNone {
			t.Error("a tool round forced an answer")
		}
	}
	if !answers[2].ToolChoiceNone {
		t.Error("the answer call is not forced")
	}
	if got := h.tools.called(); len(got) != 2 {
		t.Errorf("the synthesis' tool calls: %v", got)
	}
}

// ---------- a tool call cut by the work deadline ----------

// blockingTools blocks one tool until its context ends.
type blockingTools struct {
	*fakeTools
	name string
}

func (b blockingTools) Call(ctx context.Context, level, name string, input json.RawMessage) (tools.Result, error) {
	if name == b.name {
		<-ctx.Done()
		return tools.Result{}, context.Cause(ctx)
	}
	return b.fakeTools.Call(ctx, level, name, input)
}

// A route tool call cut by D - WorkReserve: the calls that did not run get their results, and the
// Captain answers on what it has.
func TestRouteToolDeadline(t *testing.T) {
	work := newWork(kindAsk, "", "what is a robot?")
	work.Deadline = time.Now().Add(1500 * time.Millisecond).UnixMilli()
	h := newHarness(t, work)
	h.cfg.Tools = blockingTools{fakeTools: h.tools, name: "list_robots"}
	h.cfg.WorkReserve = 1000 * time.Millisecond
	h.cfg.AnswerGrace = 2 * time.Second
	h.model.push(roleRoute,
		toolStep("tool_use", apiCall{id: "tu_r", name: "list_robots", input: map[string]any{}}),
		textStep("the answer on what we have", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	budgets := p.ofType(hub.EventBudget)
	if len(budgets) != 1 || dataOf(t, budgets[0])["what"] != "time" {
		t.Fatalf("budget events: %v", budgets)
	}
	route := h.model.requests(roleRoute)
	if len(route) != 2 || !route[1].ToolChoiceNone {
		t.Fatalf("the answer call: %v", route)
	}
	last := route[1].Messages[len(route[1].Messages)-1]
	if got := toolResults(last); len(got) != 1 || !strings.Contains(got[0], "the time for tools is up") {
		t.Errorf("the not-run tool result: %v", got)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the answer on what we have" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// ---------- no model call starts after its deadline ----------

// A critique whose phase is over makes no model call: 20 runs of a turn that reaches the critique
// just as D - WorkReserve passes.
func TestNoCallAfterTheWorkDeadline(t *testing.T) {
	for i := 0; i < 20; i++ {
		work := newWork(kindAsk, "", "write a script")
		work.Deadline = time.Now().Add(120 * time.Millisecond).UnixMilli()
		h := newHarness(t, work)
		h.cfg.WorkReserve = 60 * time.Millisecond
		h.cfg.AnswerGrace = 2 * time.Second
		h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberCoder, "a script", "x"))))
		h.coder.run = func(ctx context.Context, task coder.Task) coder.Report {
			<-ctx.Done()
			return coder.Report{State: coder.StateFailed, Reason: coder.ReasonCancelled}
		}
		h.model.push(roleAnswer, textStep("the answer", "end_turn"))
		h.start(t)
		p := h.waitFinish(t)
		h.stop(t)

		if n := len(h.model.requests(roleCritic)); n != 0 {
			t.Fatalf("run %d: %d critic requests after the work deadline", i, n)
		}
		if res := p.result(); res.State != stateDone {
			t.Errorf("run %d: state %q %q", i, res.State, res.ErrCode)
		}
	}
}

// A lost turn whose budget is used up finishes as events_lost, not as an answer.
func TestLostTurnWithNoBudget(t *testing.T) {
	work := newWork(kindAsk, "", "why is the robot degraded?")
	h := newHarness(t, work)
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	turn := d.newTurn(work)
	p := newFakePoster(work)
	turn.poster = p
	turn.merge = newMerger(p, memberCaptain, time.Millisecond, 1<<20)
	turn.budget = newBudget(caps{calls: 0})
	turn.cancel(&closedError{reason: hub.ClosedLost})
	reports := []memberResult{{member: memberFleet, task: 0, fleet: &report{Summary: "the facts", Confidence: confidenceHigh}}}

	o := turn.synthesize("read", json.RawMessage(harnessFacts), reports, critique{})
	if o.state != stateFailed || o.errCode != codeEventsLost {
		t.Fatalf("outcome %q %q %q", o.state, o.errCode, o.final)
	}
}

// ---------- a skipped critique says so ----------

// A critique skipped for the time leaves a note in the synthesis' prompt, and posts no event.
func TestSkippedCritiqueIsNoted(t *testing.T) {
	work := newWork(kindAsk, "", "why is the robot degraded?")
	work.Deadline = time.Now().Add(1500 * time.Millisecond).UnixMilli()
	h := newHarness(t, work)
	h.cfg.WorkReserve = 1000 * time.Millisecond
	h.cfg.AnswerGrace = 2 * time.Second
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	never := make(chan struct{})
	h.model.push(roleFleet, &scriptStep{
		msg:   apiMessage([]map[string]any{{"type": "text", "text": reportJSON("too late")}}, "end_turn"),
		block: never,
	})
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	answers := h.model.requests(roleAnswer)
	if len(answers) != 1 {
		t.Fatalf("%d synthesis requests", len(answers))
	}
	last := paramText(answers[0].Messages[len(answers[0].Messages)-1])
	if !strings.Contains(last, "The critique was not run (time).") {
		t.Errorf("the synthesis is not told the critique did not run: %s", last)
	}
	if len(p.ofType(hub.EventCritique)) != 0 {
		t.Error("a skipped critique posted an event")
	}
}

// ---------- the Critic's input is bounded ----------

// A coder report with 200 changed lines of 300 bytes and a 20 KiB question still fit 60 KiB.
func TestCritiqueInputBounded(t *testing.T) {
	question := strings.Repeat("q", 20<<10)
	h := newHarness(t, newWork(kindAsk, "", question))
	changed := make([]string, 200)
	for i := range changed {
		line := fmt.Sprintf("M f%03d ", i)
		changed[i] = line + strings.Repeat("x", 300-len(line))
	}
	h.coder.report = coder.Report{State: coder.StateDone, Text: "the coder's report", Changed: changed}
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberCoder, "a script", "x"))))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	h.waitFinish(t)
	h.stop(t)

	critic := h.model.requests(roleCritic)
	if len(critic) != 1 {
		t.Fatalf("%d critic requests", len(critic))
	}
	payload := paramText(critic[0].Messages[0])
	if n := len(payload); n > maxCritiqueInput {
		t.Errorf("the Critic's input is %d bytes, more than %d", n, maxCritiqueInput)
	}
	if strings.TrimSpace(payload) == "" {
		t.Error("the Critic's input is empty")
	}
}

// ---------- an empty tool output ----------

// A tool that answers with nothing gets "(no output)" as its tool_result: the API refuses an
// empty text block.
func TestEmptyToolOutput(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "what is a robot?"))
	h.tools.results["list_robots"] = tools.Result{Text: "", Digest: "nothing"}
	h.model.push(roleRoute,
		toolStep("tool_use", apiCall{id: "tu_1", name: "list_robots", input: map[string]any{}}),
		textStep("the answer", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	route := h.model.requests(roleRoute)
	if len(route) != 2 {
		t.Fatalf("%d route requests", len(route))
	}
	results := toolResults(route[1].Messages[len(route[1].Messages)-1])
	if len(results) != 1 || results[0] != "(no output)" {
		t.Errorf("the tool result %q", results)
	}
	if res := p.result(); res.State != stateDone || res.Final != "the answer" {
		t.Errorf("finish %q %q", res.State, res.Final)
	}
}

// ---------- whoami has a deadline ----------

// A whoami that never answers ends the turn as platform_unavailable, and Finish is called before
// D + FinishGrace.
func TestWhoamiDeadline(t *testing.T) {
	work := newWork(kindAsk, "", "hi")
	work.Deadline = time.Now().Add(100 * time.Millisecond).UnixMilli()
	h := newHarness(t, work)
	h.cfg.AnswerGrace = 50 * time.Millisecond
	h.whoami = func(ctx context.Context) (string, json.RawMessage, error) {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return "", nil, errors.New("too slow")
		}
	}
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	if res := p.result(); res.State != stateFailed || res.ErrCode != codePlatformDown {
		t.Fatalf("finish %q %q", res.State, res.ErrCode)
	}
	finishDeadline := time.UnixMilli(work.Deadline).Add(h.cfg.FinishGrace)
	at := p.finishTime()
	if at.IsZero() {
		t.Fatal("Finish was not called")
	}
	if at.After(finishDeadline) {
		t.Errorf("Finish at %s, after D + FinishGrace %s", at.Format(time.RFC3339Nano), finishDeadline.Format(time.RFC3339Nano))
	}
}

// ---------- Fleet's retry after a cut-off tool call ----------

// A Fleet answer cut at max_tokens inside a tool call: the retry request carries a tool_result for
// that call, and Fleet reports rather than reporting a model error.
func TestFleetMaxTokensRetry(t *testing.T) {
	h := newHarness(t, newWork(kindAsk, "", "why is the robot degraded?"))
	h.model.push(roleRoute, toolStep("tool_use", conveneCall("tu_1", taskOf(memberFleet, "the facts", "x"))))
	h.model.push(roleFleet,
		mixedStep("max_tokens", `{"summary": "cut`, apiCall{id: "tu_cut", name: "list_robots", input: map[string]any{}}),
		textStep(reportJSON("retry"), "end_turn"))
	h.model.push(roleCritic, textStep(critiqueAnswer(), "end_turn"))
	h.model.push(roleAnswer, textStep("the final", "end_turn"))
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)

	fleet := h.model.requests(roleFleet)
	if len(fleet) != 2 {
		t.Fatalf("%d fleet requests", len(fleet))
	}
	msgs := fleet[1].Messages
	asst, user := msgs[len(msgs)-2], msgs[len(msgs)-1]
	if asst.Role != anthropic.MessageParamRoleAssistant || user.Role != anthropic.MessageParamRoleUser {
		t.Fatalf("the retry's last messages: %q %q", asst.Role, user.Role)
	}
	toolUses := 0
	for _, c := range asst.Content {
		if c.OfToolUse != nil {
			toolUses++
		}
	}
	var resultIDs []string
	seenText := false
	for _, c := range user.Content {
		switch {
		case c.OfToolResult != nil:
			if seenText {
				t.Error("a tool_result comes after the retry text")
			}
			resultIDs = append(resultIDs, c.OfToolResult.ToolUseID)
		case c.OfText != nil:
			seenText = true
			if !strings.Contains(c.OfText.Text, "That was not a valid report") {
				t.Errorf("the retry text %q", c.OfText.Text)
			}
		}
	}
	if toolUses != 1 || len(resultIDs) != 1 || resultIDs[0] != "tu_cut" {
		t.Fatalf("the retry carries %d tool_use and %v tool_result", toolUses, resultIDs)
	}
	var done map[string]any
	for _, e := range p.ofType(hub.EventMemberDone) {
		done = dataOf(t, e)
	}
	rep := done["report"].(map[string]any)
	if rep["summary"] != "retry" {
		t.Errorf("Fleet's report is %v", rep["summary"])
	}
	if _, stopped := rep["reason"]; stopped {
		t.Errorf("Fleet stopped instead of reporting: %v", rep)
	}
	if len(p.ofType(hub.EventToolCall)) != 0 {
		t.Errorf("a cut-off tool call ran: %v", h.tools.called())
	}
}
