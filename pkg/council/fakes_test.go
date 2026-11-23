package council

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/agentkit"
	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
)

// ---------- the scripted model ----------

// The Standard council's member slugs, as a test names them. The daemon itself names only the built-in
// Coder (memberCoder): every other member comes from the turn's council.
const (
	slugFleet = "fleet"
	slugCoder = "coder"
)

// The roles a scripted answer is matched to, from the request's system prompt.
const (
	roleRoute   = "captain-route"
	roleAnswer  = "captain-answer"
	roleFleet   = "fleet"
	roleCritic  = "critic"
	roleUnknown = "unknown"
)

// standardFleetRounds is the Standard Fleet's budget.maxToolRounds in legacy_standard.json and
// testdata/standard_snapshot_*.json: the tests' own copy of it, so that no production default is
// what they count against (TestLegacyStandardBudget matches the two).
const (
	standardFleetRounds = 10
	standardFleetTokens = 120_000
)

// roleOf is the member a request is for. The Standard roles keep their names, because the golden
// files record them; every other request is named by the schema it answers and the name of the role
// its prompt starts with, so a council of the test's own is scripted by its own slugs.
func roleOf(req model.Request) string {
	switch {
	case strings.HasPrefix(req.System, "You are Fleet, "):
		return roleFleet
	case strings.HasPrefix(req.System, "You are the Critic of "):
		return roleCritic
	case strings.HasPrefix(req.System, "You are the Captain of "):
		if strings.Contains(req.System, "The council has reported") {
			return roleAnswer
		}
		return roleRoute
	case reflect.DeepEqual(req.Format, any(reportSchema)):
		// A member: "You are <Name>, <persona> of the council of the TeknoirBot …".
		return "member:" + between(req.System, "You are ", ", ")
	case reflect.DeepEqual(req.Format, any(critiqueSchema)):
		// A reviewer: "You are the <Name> of the council of a TeknoirBot."
		return "reviewer:" + between(req.System, "You are the ", " of the council")
	case strings.HasPrefix(req.System, "You are the "):
		// A lead of another council: "You are the <Name> of the council of the TeknoirBot …".
		if strings.Contains(req.System, "The council has reported") {
			return "lead-answer:" + between(req.System, "You are the ", " of the council")
		}
		return "lead-route:" + between(req.System, "You are the ", " of the council")
	}
	return roleUnknown
}

// between is the text of s between the first head and the first tail after it, or "" when either is
// missing. It is how a test reads a role's name out of its prompt.
func between(s, head, tail string) string {
	i := strings.Index(s, head)
	if i < 0 {
		return ""
	}
	rest := s[i+len(head):]
	j := strings.Index(rest, tail)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// scriptStep is one scripted answer.
type scriptStep struct {
	msg     string
	usage   model.Usage
	err     error
	block   chan struct{} // the call waits for it, or for its context to end
	entered chan struct{} // closed when the call starts
	repeat  bool          // the step answers every later call of its role
	// build, when set, makes the answer from the request it answers: a scripted model that reads
	// the previous answer's tool result (the eval's read_result offset). nil means the step
	// answers as it is. build runs on the turn's goroutine, not the test's: it reports with
	// t.Errorf and returns a terminal step, never t.Fatal or t.FailNow.
	build func(req model.Request) *scriptStep
}

// fakeModel is a scripted Model: a queue of answers per role.
type fakeModel struct {
	t           *testing.T
	mu          sync.Mutex
	steps       map[string][]*scriptStep
	reqs        []model.Request
	inFlight    int
	maxInFlight int
}

func newFakeModel(t *testing.T) *fakeModel {
	return &fakeModel{t: t, steps: map[string][]*scriptStep{}}
}

// push scripts answers for a role, in order.
func (m *fakeModel) push(role string, steps ...*scriptStep) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps[role] = append(m.steps[role], steps...)
}

// textStep is an answer of plain text.
func textStep(text, stop string) *scriptStep {
	return &scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": text}}, stop)}
}

// toolStep is an answer that calls tools.
type apiCall struct {
	id    string
	name  string
	input map[string]any
}

// toolStep is an answer with the tool calls, in order.
func toolStep(stop string, calls ...apiCall) *scriptStep {
	content := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		content = append(content, map[string]any{"type": "tool_use", "id": c.id, "name": c.name, "input": c.input})
	}
	return &scriptStep{msg: apiMessage(content, stop)}
}

// apiMessage is a Messages API answer as JSON.
func apiMessage(content []map[string]any, stop string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-sonnet-5-5",
		"content": content, "stop_reason": stop,
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 2},
	})
	return string(b)
}

func (m *fakeModel) Stream(ctx context.Context, req model.Request, onText func(string)) (*anthropic.Message, model.Usage, error) {
	role := roleOf(req)
	m.checkToolResults(role, req.Messages)
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.inFlight++
	if m.inFlight > m.maxInFlight {
		m.maxInFlight = m.inFlight
	}
	var st *scriptStep
	if q := m.steps[role]; len(q) > 0 {
		st = q[0]
		if !st.repeat {
			m.steps[role] = q[1:]
		}
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.inFlight--
		m.mu.Unlock()
	}()
	if st == nil {
		return nil, model.Usage{}, fmt.Errorf("fake model: no scripted %s answer", role)
	}
	if st.build != nil {
		// The answer comes from the request it answers: the queue held a placeholder.
		if built := st.build(req); built != nil {
			st = built
		}
	}
	if st.entered != nil {
		// A send, not a close: two steps may share one channel.
		select {
		case st.entered <- struct{}{}:
		case <-ctx.Done():
			return nil, model.Usage{}, context.Cause(ctx)
		}
	}
	if st.block != nil {
		select {
		case <-st.block:
		case <-ctx.Done():
			return nil, model.Usage{}, context.Cause(ctx)
		}
	}
	if st.err != nil {
		return nil, st.usage, st.err
	}
	msg := &anthropic.Message{}
	if err := json.Unmarshal([]byte(st.msg), msg); err != nil {
		return nil, model.Usage{}, fmt.Errorf("fake model: bad message JSON: %w", err)
	}
	if onText != nil {
		for _, c := range msg.Content {
			if c.Type != "text" {
				continue
			}
			for i := 0; i < len(c.Text); i += 4 {
				end := i + 4
				if end > len(c.Text) {
					end = len(c.Text)
				}
				onText(c.Text[i:end])
			}
		}
	}
	u := st.usage
	if u.Calls == 0 {
		u.Calls = 1
	}
	return msg, u, nil
}

// checkToolResults checks the API's rule on a request: every assistant message with tool_use
// blocks is followed by a user message with a tool_result for each of their ids.
func (m *fakeModel) checkToolResults(role string, msgs []anthropic.MessageParam) {
	if m.t == nil {
		return
	}
	for i, msg := range msgs {
		if msg.Role != anthropic.MessageParamRoleAssistant {
			continue
		}
		var ids []string
		for _, c := range msg.Content {
			if c.OfToolUse != nil {
				ids = append(ids, c.OfToolUse.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if i+1 == len(msgs) {
			m.t.Errorf("fake model: a %s request ends with an assistant answer whose tool_use %v has no tool_result", role, ids)
			continue
		}
		next := msgs[i+1]
		if next.Role != anthropic.MessageParamRoleUser {
			m.t.Errorf("fake model: a %s request has a %s message after tool_use %v, not a user message", role, next.Role, ids)
			continue
		}
		found := make(map[string]bool, len(next.Content))
		for _, c := range next.Content {
			if c.OfToolResult != nil {
				found[c.OfToolResult.ToolUseID] = true
			}
		}
		for _, id := range ids {
			if !found[id] {
				m.t.Errorf("fake model: a %s request has tool_use %s without a tool_result in the next message", role, id)
			}
		}
	}
}

// requests is the requests of one role, in order.
func (m *fakeModel) requests(role string) []model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.Request
	for _, r := range m.reqs {
		if roleOf(r) == role {
			out = append(out, r)
		}
	}
	return out
}

// maxConcurrent is the most calls that were in flight at once.
func (m *fakeModel) maxConcurrent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxInFlight
}

// count is how many requests the fake has answered.
func (m *fakeModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.reqs)
}

// leftover is how many scripted answers of each role the fake never gave: a script that does not
// match the flow the council ran.
func (m *fakeModel) leftover() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]int, len(m.steps))
	for role, q := range m.steps {
		if n := len(q); n > 0 {
			out[role] = n
		}
	}
	return out
}

// ---------- the tools ----------

// testToolNames are the tools each level may use.
var testToolNames = map[string][]string{
	"none":    {"whoami"},
	"read":    {"whoami", "list_robots"},
	"creator": {"whoami", "list_robots", "inspect_pod"},
}

// toolCallRec is one tool call the fake saw.
type toolCallRec struct {
	Level, Name string
	Input       string
}

// fakeTools is a scripted Tools.
type fakeTools struct {
	mu      sync.Mutex
	calls   []toolCallRec
	results map[string]tools.Result
	// queues are results for one tool, one per call in order: the first is given to the first
	// call, and once it is used up the tool answers from results as usual.
	queues map[string][]tools.Result
	// catalog is the tools each level may use, when the test has its own: a test that needs a tool
	// the Standard catalog has not sets it, and testToolNames stays the shared default.
	catalog map[string][]string
}

// namesAt is the catalog at level: the test's own when it set one, else testToolNames.
func (f *fakeTools) namesAt(level string) []string {
	if f.catalog != nil {
		return f.catalog[level]
	}
	return testToolNames[level]
}

func newFakeTools() *fakeTools {
	return &fakeTools{results: map[string]tools.Result{}, queues: map[string][]tools.Result{}}
}

// queueResults gives a tool one result per call, in order, before its default result.
func (f *fakeTools) queueResults(name string, results ...tools.Result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[name] = append(f.queues[name], results...)
}

// Names is a sorted copy of the fake catalog at level.
func (f *fakeTools) Names(level string) []string {
	out := append([]string{}, f.namesAt(level)...)
	slices.Sort(out)
	return out
}

// For keeps the fake catalog's own order, filtered by allow: the golden files record that order.
func (f *fakeTools) For(level string, allow []string) []anthropic.ToolUnionParam {
	names := f.namesAt(level)
	out := make([]anthropic.ToolUnionParam, 0, len(names))
	for _, n := range names {
		if !slices.Contains(allow, n) {
			continue
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{Name: n}})
	}
	if n := len(out); n > 0 {
		out[n-1].OfTool.CacheControl = anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL5m}
	}
	return out
}

func (f *fakeTools) Call(ctx context.Context, level string, allow []string, name string, input json.RawMessage) (tools.Result, error) {
	// The same order of checks, and the same refusals, as tools.Set.Call.
	if !slices.Contains(f.namesAt(level), name) {
		msg := fmt.Sprintf("%q is not a tool a council member at level %q may call", name, level)
		return tools.Result{Text: msg, Digest: msg, IsError: true}, nil
	}
	if !slices.Contains(allow, name) {
		msg := fmt.Sprintf("%q is not a tool this council member may call", name)
		return tools.Result{Text: msg, Digest: msg, IsError: true}, nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, toolCallRec{Level: level, Name: name, Input: string(input)})
	var res tools.Result
	var ok bool
	if q := f.queues[name]; len(q) > 0 {
		res, ok = q[0], true
		f.queues[name] = q[1:]
	} else {
		res, ok = f.results[name]
	}
	f.mu.Unlock()
	if ctx.Err() != nil {
		return tools.Result{}, context.Cause(ctx)
	}
	if ok {
		return res, nil
	}
	return tools.Result{Text: "result of " + name, Full: "result of " + name, Digest: "digest of " + name}, nil
}

// called is the names of the tools called, in order.
func (f *fakeTools) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Name)
	}
	return out
}

// ---------- the coder ----------

// fakeCoder is a scripted Coder.
type fakeCoder struct {
	mu          sync.Mutex
	tasks       []coder.Task
	report      coder.Report
	run         func(ctx context.Context, task coder.Task) coder.Report
	inFlight    int
	maxInFlight int
}

func newFakeCoder() *fakeCoder {
	return &fakeCoder{report: coder.Report{State: coder.StateDone, Text: "the coder's report", Changed: []string{}}}
}

func (c *fakeCoder) Run(ctx context.Context, task coder.Task) coder.Report {
	c.mu.Lock()
	c.tasks = append(c.tasks, task)
	c.inFlight++
	if c.inFlight > c.maxInFlight {
		c.maxInFlight = c.inFlight
	}
	run := c.run
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
	}()
	if run != nil {
		return run(ctx, task)
	}
	return c.report
}

func (c *fakeCoder) started() []coder.Task {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]coder.Task{}, c.tasks...)
}

// maxConcurrent is the most coder tasks that ran at once.
func (c *fakeCoder) maxConcurrent() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.maxInFlight
}

// ---------- the hub and the posters ----------

// fakeEvent is one event a fake poster saw.
type fakeEvent struct {
	Type string
	Data any
}

// fakePoster records a turn's events and finish.
type fakePoster struct {
	mu           sync.Mutex
	turn         *hub.WorkTurn
	events       []fakeEvent
	finishCalled bool
	finishes     int
	finishAt     time.Time
	// deadline is when the turn's finish must be posted, as the hub made the poster with it.
	deadline time.Time
	// finishWait makes Finish block on it before it records anything: a Finish that ignores its
	// ctx. The test sets it before the stop; the turn's goroutine reads it after, through the
	// channel the stop closes.
	finishWait chan struct{}
	state      string
	final      string
	errCode    string
	errMsg     string
	usage      hub.Usage
	seq        int
	finishSeq  int
	runEndSeq  int
	closed     bool
	runEnded   chan struct{}
	finished   chan struct{}
	onClosed   func(string)
}

func newFakePoster(turn *hub.WorkTurn) *fakePoster {
	return &fakePoster{turn: turn, runEnded: make(chan struct{}), finished: make(chan struct{})}
}

func (p *fakePoster) Emit(eventType string, data any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finishCalled {
		p.closed = true
	}
	p.events = append(p.events, fakeEvent{Type: eventType, Data: data})
}

func (p *fakePoster) Run(ctx context.Context) {
	<-ctx.Done()
	p.mu.Lock()
	p.runEndSeq = p.seq
	p.seq++
	close(p.runEnded)
	p.mu.Unlock()
}

func (p *fakePoster) Finish(ctx context.Context, state, final, errCode, errMsg string, usage hub.Usage) error {
	if p.finishWait != nil {
		// A poster that ignores its context: Serve must return at StopGrace anyway.
		<-p.finishWait
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finishCalled {
		return nil
	}
	p.finishCalled = true
	p.finishes++
	p.finishAt = time.Now()
	p.state, p.final, p.errCode, p.errMsg, p.usage = state, final, errCode, errMsg, usage
	p.finishSeq = p.seq
	p.seq++
	close(p.finished)
	return nil
}

// snapshot is the poster's events so far.
func (p *fakePoster) snapshot() []fakeEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]fakeEvent{}, p.events...)
}

// types is the types of the events so far.
func (p *fakePoster) types() []string {
	out := make([]string, 0, len(p.snapshot()))
	for _, e := range p.snapshot() {
		out = append(out, e.Type)
	}
	return out
}

// ofType is the events of one type.
func (p *fakePoster) ofType(typ string) []fakeEvent {
	var out []fakeEvent
	for _, e := range p.snapshot() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// dataOf is an event's data as a JSON object, decoded the way it travels.
func dataOf(t *testing.T, e fakeEvent) map[string]any {
	t.Helper()
	if e.Data == nil {
		return nil
	}
	raw, err := json.Marshal(e.Data)
	if err != nil {
		t.Fatalf("event %s: %v", e.Type, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("event %s: %v", e.Type, err)
	}
	return out
}

// finishCount is how many times Finish was called.
func (p *fakePoster) finishCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finishes
}

// result is the poster's finish.
type finishResult struct {
	State, Final, ErrCode, ErrMsg string
	Usage                         hub.Usage
}

func (p *fakePoster) result() finishResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return finishResult{State: p.state, Final: p.final, ErrCode: p.errCode, ErrMsg: p.errMsg, Usage: p.usage}
}

// finishTime is when Finish was called.
func (p *fakePoster) finishTime() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finishAt
}

// finishBeforeRunEnded reports whether Finish returned before Run's context ended.
func (p *fakePoster) finishBeforeRunEnded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finishSeq < p.runEndSeq
}

// fakeHub is a scripted work queue.
type fakeHub struct {
	mu      sync.Mutex
	queue   []*hub.WorkTurn
	frees   []int
	posters []*fakePoster
	err     error
	nils    int
	// finishWait is given to every poster the hub makes: a Finish that blocks, ignoring its ctx.
	finishWait chan struct{}
	// holdUntilDone makes Next wait for its context before it hands out a queued turn: a turn the
	// server claims as the bot stops.
	holdUntilDone bool
}

func (h *fakeHub) Next(ctx context.Context, free int) (*hub.WorkTurn, error) {
	h.mu.Lock()
	h.frees = append(h.frees, free)
	hold := h.holdUntilDone
	h.mu.Unlock()
	if hold {
		<-ctx.Done()
	}
	h.mu.Lock()
	if len(h.queue) > 0 {
		work := h.queue[0]
		h.queue = h.queue[1:]
		h.mu.Unlock()
		return work, nil
	}
	err := h.err
	nils := h.nils
	if nils > 0 {
		h.nils--
	}
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if nils > 0 {
		return nil, nil
	}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func (h *fakeHub) NewPoster(turn *hub.WorkTurn, deadline time.Time, onClosed func(reason string)) Poster {
	p := newFakePoster(turn)
	p.onClosed = onClosed
	p.deadline = deadline
	h.mu.Lock()
	p.finishWait = h.finishWait
	h.posters = append(h.posters, p)
	h.mu.Unlock()
	return p
}

func (h *fakeHub) posterList() []*fakePoster {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*fakePoster{}, h.posters...)
}

// seenFrees is the free counts Next was called with.
func (h *fakeHub) seenFrees() []int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int{}, h.frees...)
}

// queueTurn adds a turn behind the ones queued.
func (h *fakeHub) queueTurn(turn *hub.WorkTurn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.queue = append(h.queue, turn)
}

// ---------- the harness ----------

// logCapture records the daemon's log lines.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logCapture) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.lines...)
}

// harnessFacts is the platform's answer about the bot.
const harnessFacts = `{"mode":"bot","bot":{"namespace":"teknoir-demo","name":"scout","access":"read"}}`

// testHarness is a daemon served by its fakes.
type testHarness struct {
	model  *fakeModel
	tools  *fakeTools
	coder  *fakeCoder
	hub    *fakeHub
	logs   *logCapture
	whoami func(ctx context.Context) (string, json.RawMessage, error)
	cfg    Config
	d      *Daemon
	ctx    context.Context
	cancel context.CancelFunc
	done   chan error
}

// newTurn is a work turn with a deadline comfortably ahead.
func newWork(kind, member, prompt string) *hub.WorkTurn {
	return &hub.WorkTurn{
		ID:       "trn_0123456789abcdefghijklmn",
		ClaimID:  "clm_1",
		ThreadID: "thr_0123456789abcdefghijklmn",
		Kind:     kind,
		Member:   member,
		Prompt:   prompt,
		Author:   hub.Author{Email: "a@b.c", DisplayName: "Anders"},
		Deadline: time.Now().Add(10 * time.Second).UnixMilli(),
	}
}

// testBook is the guides of the council's tests: the kit embedded in tnctl, so what a prompt says
// about guides does not depend on the machine the test runs on.
func testBook() *skillBook { return newSkillBook(agentkit.FS) }

// newHarness is a harness with one turn queued and small durations.
func newHarness(t *testing.T, work *hub.WorkTurn) *testHarness {
	t.Helper()
	h := &testHarness{
		model: newFakeModel(t),
		tools: newFakeTools(),
		coder: newFakeCoder(),
		hub:   &fakeHub{queue: []*hub.WorkTurn{work}},
		logs:  &logCapture{},
	}
	h.whoami = func(context.Context) (string, json.RawMessage, error) {
		return "read", json.RawMessage(harnessFacts), nil
	}
	h.cfg = Config{
		Hub: h.hub, Model: h.model, Tools: h.tools, Coder: h.coder,
		Whoami: func(ctx context.Context) (string, json.RawMessage, error) { return h.whoami(ctx) },
		Logf:   h.logs.logf,
		Sleep:  func(context.Context, time.Duration) error { return nil },
		Now:    time.Now,
		Skills: agentkit.FS,

		MergeEvery:  2 * time.Millisecond,
		MergeBytes:  1 << 20,
		StopGrace:   500 * time.Millisecond,
		WorkReserve: 150 * time.Millisecond,
		AnswerGrace: 300 * time.Millisecond,
		FinishGrace: 300 * time.Millisecond,
	}
	return h
}

// start serves the daemon in the background.
func (h *testHarness) start(t *testing.T) {
	t.Helper()
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.d = d
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.done = make(chan error, 1)
	go func() { h.done <- d.Serve(h.ctx) }()
}

// poster is the turn's poster, once the turn made one.
func (h *testHarness) poster(t *testing.T) *fakePoster {
	t.Helper()
	var p *fakePoster
	waitUntil(t, "the turn's poster", func() bool {
		list := h.hub.posterList()
		if len(list) == 0 {
			return false
		}
		p = list[0]
		return true
	})
	return p
}

// waitFinish waits for the turn's Finish and returns the poster.
func (h *testHarness) waitFinish(t *testing.T) *fakePoster {
	t.Helper()
	p := h.poster(t)
	select {
	case <-p.finished:
	case <-time.After(10 * time.Second):
		t.Fatalf("the turn did not finish; events: %v", p.types())
	}
	return p
}

// stop cancels Serve and waits for it.
func (h *testHarness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

// waitUntil waits for f, or fails the test.
func waitUntil(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// eventOrder is the indexes of the first event of each type, in order; a type that is missing is
// -1.
func eventOrder(types []string, want ...string) []int {
	out := make([]int, len(want))
	for i, w := range want {
		out[i] = -1
		for j, typ := range types {
			if typ == w {
				out[i] = j
				break
			}
		}
	}
	return out
}
