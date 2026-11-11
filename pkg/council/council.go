package council

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/teknoir/cli/pkg/council/coder"
	"github.com/teknoir/cli/pkg/council/hub"
	"github.com/teknoir/cli/pkg/council/model"
	"github.com/teknoir/cli/pkg/council/tools"
)

// The council's members.
const (
	memberCaptain = "captain"
	memberCritic  = "critic"
)

// The states a turn is finished with, as K3 takes them.
const (
	stateDone   = "done"
	stateFailed = "failed"
)

// The error codes a turn is finished with.
const (
	codeBadTurn        = "bad_turn"
	codeTurnClosed     = "turn_closed"
	codeEventsLost     = "events_lost"
	codeEventsRejected = "events_rejected"
	codeRestarting     = "bot_restarting"
	codePlatformDown   = "platform_unavailable"
	codeNoAccess       = "no_access"
	codeModelError     = "model_error"
	codeModelRefused   = "model_refused"
	// The platform proxy's own refusals: no model was reached.
	codeModelUnavailable = "model_unavailable"
	codeModelBudget      = "model_budget"
	codeModelBusy        = "model_busy"
	codeModelRejected    = "model_rejected"
)

// The messages of the outcomes that are not spelled out at their site.
const (
	msgBadTurn        = "The bot could not read this turn."
	msgTurnClosed     = "The turn was closed."
	msgEventsLost     = "The bot could not deliver this turn's progress."
	msgEventsRejected = "The platform refused this turn's progress."
	msgRestarting     = "The bot restarted during this turn."
	msgPlatformDown   = "The bot could not read who it is on the platform."
	msgNoAccess       = "At access level none, Fleet has no tools to use."
	msgNoAnswer       = "The Captain gave no answer."
	msgNoBudget       = "The Captain ran out of budget before answering."
)

// The limits of the daemon.
const (
	maxTurnsLimit     = 2
	defaultModelSlots = 4
)

// Model is the council's client of the bot API's Messages proxy: one streaming request.
type Model interface {
	Stream(ctx context.Context, req model.Request, onText func(string)) (*anthropic.Message, model.Usage, error)
}

// Tools are the tools of tnctl's own MCP server, per access level.
type Tools interface {
	For(level string) []anthropic.ToolUnionParam
	Call(ctx context.Context, level, name string, input json.RawMessage) (tools.Result, error)
}

// Coder runs one coder task on the bot's computer.
type Coder interface {
	Run(ctx context.Context, task coder.Task) coder.Report
}

// Poster is the events and the finish of one turn: hub.Poster is one.
type Poster interface {
	Emit(eventType string, data any)
	Run(ctx context.Context)
	Finish(ctx context.Context, state, final, errCode, errMsg string, usage hub.Usage) error
}

// Hub is the platform's work queue: hub.Client is one, through HubOf.
type Hub interface {
	Next(ctx context.Context, free int) (*hub.WorkTurn, error)
	NewPoster(turn *hub.WorkTurn, onClosed func(reason string)) Poster
}

// Whoami says who the bot is now: its access level (none, read or creator; "" counts as none) and
// the platform's JSON about it, for the prompts.
type Whoami func(ctx context.Context) (level string, facts json.RawMessage, err error)

// hubClient adapts a hub.Client to Hub.
type hubClient struct{ c *hub.Client }

// HubOf adapts a *hub.Client: NewPoster calls Client.Poster.
func HubOf(c *hub.Client) Hub { return hubClient{c: c} }

func (h hubClient) Next(ctx context.Context, free int) (*hub.WorkTurn, error) {
	return h.c.Next(ctx, free)
}

func (h hubClient) NewPoster(turn *hub.WorkTurn, onClosed func(reason string)) Poster {
	return h.c.Poster(turn, onClosed)
}

// Config is what a daemon is built from.
type Config struct {
	Hub    Hub
	Model  Model
	Tools  Tools
	Coder  Coder
	Whoami Whoami
	// MaxTurns is how many turns run at once, 1..2; 0 means 2.
	MaxTurns int
	// ModelSlots is how many orchestrator model calls run at once across all turns; 0 means 4.
	ModelSlots int
	// Logf is the operator log: ids, counts, codes and durations only. nil logs nothing.
	Logf func(format string, args ...any)

	// The rest is for tests; zero values take the defaults.

	// Now is time.Now.
	Now func() time.Time
	// Sleep waits d or until ctx ends; its error ends Serve.
	Sleep func(ctx context.Context, d time.Duration) error
	// Rand is the jitter of hub.Backoff; nil gets a seeded source. Only Serve's goroutine uses it,
	// so it needs no lock.
	Rand *rand.Rand
	// MergeEvery and MergeBytes bound the Captain's streamed text.
	MergeEvery time.Duration
	MergeBytes int
	// StopGrace is how long a stop waits for the turns to finish.
	StopGrace time.Duration
	// WorkReserve is what the work phases leave before the turn's deadline.
	WorkReserve time.Duration
	// AnswerGrace is how far past its deadline the answer calls may run.
	AnswerGrace time.Duration
	// FinishGrace is how far past its deadline the finish may run.
	FinishGrace time.Duration
}

// defaults fills a Config's zero fields in place.
func (c *Config) defaults() {
	if c.MaxTurns == 0 {
		c.MaxTurns = 2
	}
	if c.ModelSlots == 0 {
		c.ModelSlots = defaultModelSlots
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = sleepContext
	}
	if c.Rand == nil {
		// Only Serve's goroutine reads Rand, through hub.Backoff, so one source is enough.
		c.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if c.MergeEvery <= 0 {
		c.MergeEvery = 250 * time.Millisecond
	}
	if c.MergeBytes <= 0 {
		c.MergeBytes = 2048
	}
	if c.StopGrace <= 0 {
		c.StopGrace = 5 * time.Second
	}
	if c.WorkReserve <= 0 {
		c.WorkReserve = 75 * time.Second
	}
	if c.AnswerGrace <= 0 {
		c.AnswerGrace = 20 * time.Second
	}
	if c.FinishGrace <= 0 {
		c.FinishGrace = 50 * time.Second
	}
}

// sleepContext waits d, or until ctx ends.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Daemon serves the council of one TeknoirBot: it takes turns from the platform's work queue and
// answers them.
type Daemon struct {
	cfg Config
	// slots bounds the model calls in flight; it is a channel so waiting can end with a context.
	slots chan struct{}
	// stopping is closed when Serve stops; the turns select on it.
	stopping chan struct{}
	stopOnce sync.Once

	// mu guards everything below.
	mu         sync.Mutex
	running    int
	pollCancel context.CancelFunc
	stopAt     time.Time
	turns      map[*turn]struct{}

	// wg tracks the turn goroutines, for the stop's wait.
	wg sync.WaitGroup
}

// New returns the daemon of one bot. Every client is required.
func New(cfg Config) (*Daemon, error) {
	if cfg.Hub == nil {
		return nil, errors.New("council: no hub")
	}
	if cfg.Model == nil {
		return nil, errors.New("council: no model client")
	}
	if cfg.Tools == nil {
		return nil, errors.New("council: no tools")
	}
	if cfg.Coder == nil {
		return nil, errors.New("council: no coder")
	}
	if cfg.Whoami == nil {
		return nil, errors.New("council: no whoami")
	}
	if cfg.MaxTurns < 0 || cfg.MaxTurns > maxTurnsLimit {
		return nil, fmt.Errorf("council: %d turns at once is not 1 or 2", cfg.MaxTurns)
	}
	if cfg.ModelSlots < 0 {
		return nil, fmt.Errorf("council: %d model slots", cfg.ModelSlots)
	}
	cfg.defaults()
	return &Daemon{
		cfg:      cfg,
		slots:    make(chan struct{}, cfg.ModelSlots),
		stopping: make(chan struct{}),
		turns:    make(map[*turn]struct{}),
	}, nil
}

// Serve takes turns from the work queue until ctx ends, then stops: it cancels every running turn,
// each of which finishes as bot_restarting, and returns nil when they are done or at StopGrace.
func (d *Daemon) Serve(ctx context.Context) error {
	attempt := 0
	fastNils := 0
	ended := false
	for !ended && ctx.Err() == nil {
		d.mu.Lock()
		free := d.cfg.MaxTurns - d.running
		pollCtx := ctx
		var cancel context.CancelFunc
		if free <= 0 {
			// A poll with no slot keeps the bot online on the platform; ending a turn cancels it, so
			// the free slot is offered at once.
			pollCtx, cancel = context.WithCancel(ctx)
			d.pollCancel = cancel
		}
		d.mu.Unlock()

		started := d.cfg.Now()
		work, err := d.cfg.Hub.Next(pollCtx, free)

		// Whether an ending turn (or the stop) cancelled this poll, before it is cancelled here.
		pollCancelled := pollCtx.Err() != nil
		d.mu.Lock()
		d.pollCancel = nil
		d.mu.Unlock()
		if cancel != nil {
			cancel()
		}

		switch {
		case err == nil && work != nil:
			attempt = 0
			fastNils = 0
			if ctx.Err() != nil {
				// The turn was claimed as the bot stopped: it still starts, and finishes at once.
				d.beginStop()
			}
			d.startTurn(work)
		case err != nil:
			if pollCancelled || ctx.Err() != nil {
				// The poll was cancelled, or the bot is stopping: not a failure.
				continue
			}
			attempt++
			d.cfg.Logf("poll: failed (status %d, code %q), attempt %d", hub.StatusOf(err), hub.CodeOf(err), attempt)
			wait := hub.Backoff(err, attempt, d.cfg.Rand)
			if hub.StatusOf(err) == 404 && hub.CodeOf(err) == "council_off" {
				wait = 60 * time.Second
			}
			if d.cfg.Sleep(ctx, wait) != nil {
				ended = true
			}
		default:
			attempt = 0
			if d.cfg.Now().Sub(started) < time.Second {
				fastNils++
			} else {
				fastNils = 0
			}
			if fastNils >= 2 {
				// A server answering at once must not make a busy loop.
				fastNils = 0
				if d.cfg.Sleep(ctx, time.Second) != nil {
					ended = true
				}
			}
		}
	}
	stopAt := d.beginStop()
	return d.waitTurns(stopAt)
}

// beginStop starts the stop once: it records when it began, closes stopping and cancels every
// running turn with errShutdown. It returns when the stop began.
func (d *Daemon) beginStop() time.Time {
	d.stopOnce.Do(func() {
		now := d.cfg.Now()
		d.mu.Lock()
		d.stopAt = now
		close(d.stopping)
		for t := range d.turns {
			t.cancel(errShutdown)
		}
		d.mu.Unlock()
	})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopAt
}

// waitTurns waits for every turn's Finish to return, or for StopGrace from the stop.
func (d *Daemon) waitTurns(stopAt time.Time) error {
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	remaining := stopAt.Add(d.cfg.StopGrace).Sub(d.cfg.Now())
	if remaining < 0 {
		remaining = 0
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	return nil
}

// startTurn runs one turn in its own goroutine.
func (d *Daemon) startTurn(work *hub.WorkTurn) {
	t := d.newTurn(work)
	d.mu.Lock()
	d.running++
	d.turns[t] = struct{}{}
	d.mu.Unlock()
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		t.run()
	}()
}

// endTurn takes a finished turn out of the daemon, and offers the slot its end freed.
func (d *Daemon) endTurn(t *turn) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.turns[t]; ok {
		delete(d.turns, t)
		d.running--
	}
	if d.pollCancel != nil {
		d.pollCancel()
	}
}
