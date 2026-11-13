package council

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/internal/botapi"
	"github.com/teknoir/cli/pkg/council/model"
)

// sleepRec is a Sleep that records its waits and returns at once.
type sleepRec struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (s *sleepRec) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	s.waits = append(s.waits, d)
	s.mu.Unlock()
	return ctx.Err()
}

func (s *sleepRec) all() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration{}, s.waits...)
}

// serveDaemon builds and serves a daemon of the harness's fakes.
func (h *testHarness) serveDaemon(t *testing.T) (context.Context, context.CancelFunc, chan error) {
	t.Helper()
	d, err := New(h.cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx) }()
	return ctx, cancel, done
}

// waitServe waits for Serve to return.
func waitServe(t *testing.T, done chan error, within time.Duration) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(within):
		t.Fatalf("Serve did not return within %s", within)
	}
}

// TestTwoTurnsAtOnce runs two queued turns side by side, and checks the free counts the daemon
// polls with: 2, 1, 0, then 1 again as one of them ends.
func TestTwoTurnsAtOnce(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, nil)
	h.hub = &fakeHub{}
	h.cfg.Hub = h.hub
	sleeps := &sleepRec{}
	h.cfg.Sleep = sleeps.sleep
	for i := 0; i < 2; i++ {
		work := newWork(kindAsk, "", fmt.Sprintf("question %d", i))
		work.ID = fmt.Sprintf("trn_0123456789abcdefghijklm%d", i)
		h.hub.queueTurn(work)
	}
	entered := make(chan struct{}, 2)
	h.model.push(roleRoute,
		&scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": "one"}}, "end_turn"), block: release, entered: entered},
		&scriptStep{msg: apiMessage([]map[string]any{{"type": "text", "text": "two"}}, "end_turn"), block: release, entered: entered},
		textStep("one", "end_turn"),
	)
	_, cancel, done := h.serveDaemon(t)
	<-entered
	<-entered
	waitUntil(t, "a free-0 poll", func() bool {
		for _, f := range h.hub.seenFrees() {
			if f == 0 {
				return true
			}
		}
		return false
	})
	frees := h.hub.seenFrees()
	if frees[0] != 2 || frees[1] != 1 {
		t.Fatalf("the first free counts: %v", frees)
	}
	close(release)
	// One turn ends, its free-0 poll is cancelled, and the freed slot is offered at once.
	waitUntil(t, "the freed slot", func() bool {
		frees := h.hub.seenFrees()
		sawZero := false
		for _, f := range frees {
			if f == 0 {
				sawZero = true
			}
			if sawZero && f > 0 {
				return true
			}
		}
		return false
	})
	if waits := sleeps.all(); len(waits) != 0 {
		t.Errorf("a cancelled poll slept: %v", waits)
	}
	cancel()
	waitServe(t, done, 5*time.Second)
}

// TestFreeZeroPollIsCancelled checks that a poll with no slot is cancelled when a turn ends, and
// that the freed slot is offered at once.
func TestFreeZeroPollIsCancelled(t *testing.T) {
	h := newHarness(t, nil)
	h.hub = &fakeHub{}
	h.cfg.Hub = h.hub
	h.cfg.MaxTurns = 1
	release := make(chan struct{})
	work := newWork(kindAsk, "", "question")
	h.hub.queueTurn(work)
	h.model.push(roleRoute, &scriptStep{
		msg:   apiMessage([]map[string]any{{"type": "text", "text": "one"}}, "end_turn"),
		block: release,
	})
	_, cancel, done := h.serveDaemon(t)
	waitUntil(t, "the blocked turn", func() bool {
		return h.model.count() == 1
	})
	// The daemon now polls with no slot.
	waitUntil(t, "a free-0 poll", func() bool {
		for _, f := range h.hub.seenFrees() {
			if f == 0 {
				return true
			}
		}
		return false
	})
	close(release)
	waitUntil(t, "the freed slot's poll", func() bool {
		frees := h.hub.seenFrees()
		sawZero := false
		for _, f := range frees {
			if f == 0 {
				sawZero = true
			}
			if sawZero && f == 1 {
				return true
			}
		}
		return false
	})
	cancel()
	waitServe(t, done, 5*time.Second)
}

// A poll error waits as hub.Backoff says, and the waits are recorded.
func TestPollBackoff(t *testing.T) {
	h := newHarness(t, nil)
	h.hub = &fakeHub{err: &hub.Error{Method: "GET", URL: "u", Status: http.StatusInternalServerError}}
	h.cfg.Hub = h.hub
	sleeps := &sleepRec{}
	h.cfg.Sleep = sleeps.sleep
	// A fixed source, so the jitter is the same as the one the test computes below.
	h.cfg.Rand = rand.New(rand.NewSource(42))
	_, cancel, done := h.serveDaemon(t)
	waitUntil(t, "three failed polls", func() bool {
		return len(sleeps.all()) >= 3
	})
	cancel()
	waitServe(t, done, 5*time.Second)

	waits := sleeps.all()
	// A second source with the same seed gives the same sequence of jitters.
	want := rand.New(rand.NewSource(42))
	for i, w := range waits[:3] {
		attempt := i + 1
		if got := hub.Backoff(&hub.Error{Status: http.StatusInternalServerError}, attempt, want); w != got {
			t.Errorf("wait %d is %s, want %s", i, w, got)
		}
	}
}

func TestPollBackoffStatuses(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *hub.Error
		want time.Duration
	}{
		{"unauthorized", &hub.Error{Status: http.StatusUnauthorized}, 60 * time.Second},
		{"off", &hub.Error{Status: http.StatusNotFound, Code: "council_off"}, 60 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.hub = &fakeHub{err: tc.err}
			h.cfg.Hub = h.hub
			sleeps := &sleepRec{}
			h.cfg.Sleep = sleeps.sleep
			_, cancel, done := h.serveDaemon(t)
			waitUntil(t, "one failed poll", func() bool { return len(sleeps.all()) >= 1 })
			cancel()
			waitServe(t, done, 5*time.Second)
			if got := sleeps.all()[0]; got != tc.want {
				t.Errorf("wait %s, want %s", got, tc.want)
			}
		})
	}
}

// A bot whose key file is gone needs a person: the poll names the file, says to re-create the bot,
// and waits 60 s.
func TestPollKeyError(t *testing.T) {
	h := newHarness(t, nil)
	h.hub = &fakeHub{err: fmt.Errorf("hub: %w", botapi.ErrKey)}
	h.cfg.Hub = h.hub
	h.cfg.KeyFile = "/run/teknoirbot/key"
	sleeps := &sleepRec{}
	h.cfg.Sleep = sleeps.sleep
	_, cancel, done := h.serveDaemon(t)
	waitUntil(t, "one failed poll", func() bool { return len(sleeps.all()) >= 1 })
	cancel()
	waitServe(t, done, 5*time.Second)

	if got := sleeps.all()[0]; got != 60*time.Second {
		t.Errorf("wait %s, want 60 s for a missing key", got)
	}
	lines := h.logs.all()
	named := false
	for _, line := range lines {
		if strings.Contains(line, "/run/teknoirbot/key") && strings.Contains(line, "re-create the bot") {
			named = true
		}
	}
	if !named {
		t.Errorf("no log line names the key file and re-creating the bot: %v", lines)
	}
}

// A 404 on the work queue is the council being off, whatever its code: 60 s between polls, and one
// line per streak.
func TestPollNoCouncilAPI(t *testing.T) {
	h := newHarness(t, nil)
	h.hub = &fakeHub{err: &hub.Error{Method: "GET", URL: "u", Status: http.StatusNotFound, Code: "NotFoundError"}}
	h.cfg.Hub = h.hub
	var mu sync.Mutex
	var waits []time.Duration
	h.cfg.Sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		n := len(waits)
		mu.Unlock()
		if n >= 10 {
			// Hold the tenth poll until the test cancels, so the count of failures is exact.
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	_, cancel, done := h.serveDaemon(t)
	waitUntil(t, "ten failed polls", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(waits) >= 10
	})
	cancel()
	waitServe(t, done, 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	for i, w := range waits {
		if w != 60*time.Second {
			t.Errorf("wait %d is %s, want 60 s for a platform with no council API", i, w)
		}
	}
	lines := 0
	for _, line := range h.logs.all() {
		if strings.Contains(line, "no council API") {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("lines about the missing council API = %d, want 2 (the first failure and the tenth): %v", lines, h.logs.all())
	}
}

// countLog is how many of the captured lines hold substr.
func countLog(lines []string, substr string) int {
	n := 0
	for _, line := range lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

// scriptHub is a work queue that answers with a script of errors, then with one error for good.
type scriptHub struct {
	mu   sync.Mutex
	errs []error
	then error
}

func (h *scriptHub) Next(ctx context.Context, free int) (*hub.WorkTurn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.errs) > 0 {
		err := h.errs[0]
		h.errs = h.errs[1:]
		return nil, err
	}
	return nil, h.then
}

func (h *scriptHub) NewPoster(turn *hub.WorkTurn, deadline time.Time, onClosed func(reason string)) Poster {
	p := newFakePoster(turn)
	p.onClosed = onClosed
	p.deadline = deadline
	return p
}

// A restart's 503s do not hide the 404s that follow them: the 404 line comes at the first 404 after
// them, not ten failures later, and then at every tenth 404.
func TestPollNoCouncilAPICountsTheFourOhFours(t *testing.T) {
	busy := &hub.Error{Method: "GET", URL: "u", Status: http.StatusServiceUnavailable}
	off := &hub.Error{Method: "GET", URL: "u", Status: http.StatusNotFound, Code: "NotFoundError"}

	h := newHarness(t, nil)
	h.cfg.Hub = &scriptHub{errs: []error{busy, busy}, then: off}
	var mu sync.Mutex
	var waits []time.Duration
	afterThird := -1
	h.cfg.Sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		n := len(waits)
		if n == 3 {
			afterThird = countLog(h.logs.all(), "no council API")
		}
		mu.Unlock()
		if n >= 12 {
			// Hold the twelfth poll's wait until the test cancels, so the count is exact.
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	_, cancel, done := h.serveDaemon(t)
	waitUntil(t, "twelve failed polls", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(waits) >= 12
	})
	cancel()
	waitServe(t, done, 5*time.Second)

	if afterThird != 1 {
		t.Errorf("lines about the missing council API after the 3rd poll = %d, want 1: the 404s are counted from the first 404: %v", afterThird, h.logs.all())
	}
	if got := countLog(h.logs.all(), "no council API"); got != 2 {
		t.Errorf("lines after twelve polls = %d, want 2 (the first 404 and the tenth): %v", got, h.logs.all())
	}
}

// The poster HubOf makes carries the turn's finish deadline: a failure at or past it gives up at
// once, with one K2 and no resend.
func TestHubOfPosterCarriesTheDeadline(t *testing.T) {
	var k2s atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		k2s.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	keyPath := filepath.Join(t.TempDir(), "bot-key")
	if err := os.WriteFile(keyPath, []byte("bot-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := hub.New(srv.URL, keyPath, "test", nil)
	if err != nil {
		t.Fatalf("hub.New: %v", err)
	}
	closed := make(chan string, 1)
	p := HubOf(c).NewPoster(&hub.WorkTurn{ID: "trn_1", ClaimID: "clm_1"},
		time.Now().Add(-time.Second), func(reason string) { closed <- reason })
	p.Emit(hub.EventTurnStart, map[string]any{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	select {
	case reason := <-closed:
		if reason != hub.ClosedLost {
			t.Errorf("onClosed = %q, want %q", reason, hub.ClosedLost)
		}
	case <-time.After(3 * time.Second):
		// The default flush holds the event for a second. With no deadline carried the poster
		// would keep resending, through its three-minute window.
		t.Fatal("no onClosed: the poster HubOf made did not carry the past deadline")
	}
	<-done
	if got := k2s.Load(); got != 1 {
		t.Errorf("K2 requests = %d, want 1", got)
	}
}

// The poster of a turn is made with the turn's finish deadline: D plus FinishGrace.
func TestPosterDeadline(t *testing.T) {
	work := newWork(kindAsk, "", "what is a robot?")
	h := newHarness(t, work)
	h.cfg.FinishGrace = 300 * time.Millisecond
	h.model.push(roleRoute, textStep("an edge computer", "end_turn"))
	h.start(t)
	p := h.poster(t)
	p.mu.Lock()
	got := p.deadline
	p.mu.Unlock()
	want := time.UnixMilli(work.Deadline).Add(h.cfg.FinishGrace)
	if !got.Equal(want) {
		t.Errorf("the poster's deadline = %v, want D + FinishGrace = %v", got, want)
	}
	h.waitFinish(t)
	h.stop(t)
}

// Two fast nil answers make a one-second sleep.
func TestFastNilsSleep(t *testing.T) {
	h := newHarness(t, nil)
	h.hub = &fakeHub{nils: 2}
	h.cfg.Hub = h.hub
	sleeps := &sleepRec{}
	h.cfg.Sleep = sleeps.sleep
	_, cancel, done := h.serveDaemon(t)
	waitUntil(t, "the one-second sleep", func() bool {
		for _, w := range sleeps.all() {
			if w == time.Second {
				return true
			}
		}
		return false
	})
	cancel()
	waitServe(t, done, 5*time.Second)
}

func TestNewRefusals(t *testing.T) {
	work := newWork(kindAsk, "", "q")
	h := newHarness(t, work)
	for _, cfg := range []Config{
		func() Config { c := h.cfg; c.MaxTurns = 3; return c }(),
		func() Config { c := h.cfg; c.ModelSlots = -1; return c }(),
		func() Config { c := h.cfg; c.Hub = nil; return c }(),
		func() Config { c := h.cfg; c.Model = nil; return c }(),
		func() Config { c := h.cfg; c.Tools = nil; return c }(),
		func() Config { c := h.cfg; c.Coder = nil; return c }(),
		func() Config { c := h.cfg; c.Whoami = nil; return c }(),
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New accepted MaxTurns %d, ModelSlots %d with a nil client", cfg.MaxTurns, cfg.ModelSlots)
		}
	}
	c := h.cfg
	c.MaxTurns, c.ModelSlots = 2, 1
	if _, err := New(c); err != nil {
		t.Errorf("New refused a good config: %v", err)
	}
}

// A stop with a Coder that ignores its context finishes the turn and returns within StopGrace.
func TestShutdownWithStuckCoder(t *testing.T) {
	work := newWork(kindMember, memberCoder, "write a script")
	h := newHarness(t, work)
	h.cfg.StopGrace = 300 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	var once sync.Once
	h.coder.run = func(ctx context.Context, task coder.Task) coder.Report {
		once.Do(func() { close(entered) })
		// A Coder that ignores its context: the process is being killed.
		time.Sleep(3 * time.Second)
		return coder.Report{State: coder.StateFailed, Reason: coder.ReasonCancelled}
	}
	_, cancel, done := h.serveDaemon(t)
	p := h.poster(t)
	<-entered

	start := time.Now()
	cancel()
	waitServe(t, done, h.cfg.StopGrace+(1*time.Second))
	if elapsed := time.Since(start); elapsed > h.cfg.StopGrace+time.Second {
		t.Errorf("Serve took %s", elapsed)
	}
	select {
	case <-p.finished:
	case <-time.After(time.Second):
		t.Fatal("the turn was not finished")
	}
	if res := p.result(); res.State != stateFailed || res.ErrCode != codeRestarting {
		t.Errorf("finish %q %q", res.State, res.ErrCode)
	}
}

// A turn that Next returns just as the bot stops still starts, and finishes as bot_restarting.
func TestTurnAtStop(t *testing.T) {
	h := newHarness(t, nil)
	h.hub = &fakeHub{holdUntilDone: true}
	h.cfg.Hub = h.hub
	work := newWork(kindAsk, "", "hi")
	h.hub.queueTurn(work)
	h.cfg.StopGrace = 500 * time.Millisecond
	_, cancel, done := h.serveDaemon(t)
	// The poll is in flight: the cancel makes it end, and the turn it returns still starts.
	waitUntil(t, "the poll", func() bool { return len(h.hub.seenFrees()) > 0 })
	cancel()
	waitServe(t, done, 5*time.Second)
	posters := h.hub.posterList()
	if len(posters) != 1 {
		t.Fatalf("%d posters: the claimed turn was not started", len(posters))
	}
	p := posters[0]
	select {
	case <-p.finished:
	case <-time.After(time.Second):
		t.Fatal("the turn was not finished")
	}
	if res := p.result(); res.State != stateFailed || res.ErrCode != codeRestarting {
		t.Errorf("finish %q %q", res.State, res.ErrCode)
	}
	if res := p.result(); res.ErrMsg != msgRestarting {
		t.Errorf("message %q", res.ErrMsg)
	}
}

// A turn whose model keeps failing still ends; the wall clock is what the daemon bounds.
func TestServeReturnsNilOnStop(t *testing.T) {
	h := newHarness(t, nil)
	h.hub = &fakeHub{nils: 1000}
	h.cfg.Hub = h.hub
	sleeps := &sleepRec{}
	h.cfg.Sleep = sleeps.sleep
	_, cancel, done := h.serveDaemon(t)
	time.Sleep(20 * time.Millisecond)
	cancel()
	waitServe(t, done, 2*time.Second)
}

// ---------- usage accounting ----------

// Coder usage enters the turn through OnStep only, once.
func TestUsageCountsCoderStepsOnce(t *testing.T) {
	work := newWork(kindMember, memberCoder, "write a script")
	h := newHarness(t, work)
	h.coder.run = func(ctx context.Context, task coder.Task) coder.Report {
		task.OnStep(model.Usage{Input: 100, Output: 10})
		return coder.Report{
			State: coder.StateDone, Text: "done",
			Usage: model.Usage{Input: 100, Output: 10, Calls: 1},
		}
	}
	h.start(t)
	p := h.waitFinish(t)
	h.stop(t)
	res := p.result()
	if res.Usage.Input != 100 || res.Usage.Output != 10 || res.Usage.Calls != 0 {
		t.Errorf("usage %+v", res.Usage)
	}
	usageEvents := p.ofType(hub.EventUsage)
	if len(usageEvents) != 1 {
		t.Fatalf("%d usage events", len(usageEvents))
	}
	if d := dataOf(t, usageEvents[0]); d["input"].(float64) != 100 {
		t.Errorf("usage event %v", d)
	}
}

// A turn that never got a report still finishes with a done state and a final.
func TestErrorOutcomeIsNotHidden(t *testing.T) {
	if o := modelErrorOutcome(errors.New("x")); o.errMsg != "The model could not be reached." {
		t.Errorf("message %q", o.errMsg)
	}
	if o := modelErrorOutcome(&model.StatusError{Status: 500}); o.errMsg != "The model could not be reached (status 500)." {
		t.Errorf("message %q", o.errMsg)
	}
}

// A poster whose Finish ignores its context and blocks: Serve still returns at stopAt + StopGrace.
func TestShutdownWithStuckFinish(t *testing.T) {
	work := newWork(kindAsk, "", "hi")
	h := newHarness(t, work)
	h.cfg.StopGrace = 300 * time.Millisecond
	h.hub = &fakeHub{queue: []*hub.WorkTurn{work}}
	h.cfg.Hub = h.hub
	release := make(chan struct{})
	defer close(release)
	h.hub.finishWait = release
	entered := make(chan struct{})
	h.model.push(roleRoute, &scriptStep{
		msg:     apiMessage([]map[string]any{{"type": "text", "text": "late"}}, "end_turn"),
		block:   make(chan struct{}),
		entered: entered,
	})
	_, cancel, done := h.serveDaemon(t)
	<-entered

	start := time.Now()
	cancel()
	waitServe(t, done, h.cfg.StopGrace+time.Second)
	if elapsed := time.Since(start); elapsed > h.cfg.StopGrace+time.Second {
		t.Errorf("Serve took %s, more than StopGrace + 1 s", elapsed)
	}
}

// New gives the daemon a jitter source when the config has none, and keeps the one it is given.
func TestNewRandDefault(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cfg
	c.Rand = nil
	d, err := New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.cfg.Rand == nil {
		t.Fatal("a daemon without a Rand got no jitter source")
	}
	// The waits it gives are jittered: a fixed source gives different waits for the same attempt.
	jittered := false
	for i := 0; i < 10; i++ {
		if hub.Backoff(&hub.Error{Status: http.StatusInternalServerError}, 1, d.cfg.Rand) != time.Second {
			jittered = true
			break
		}
	}
	if !jittered {
		t.Error("the daemon's Rand gave no jitter")
	}

	fixed := rand.New(rand.NewSource(7))
	c.Rand = fixed
	d, err = New(c)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.cfg.Rand != fixed {
		t.Error("New replaced the Rand it was given")
	}
}
