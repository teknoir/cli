// Package hub is the council's work queue client: the bot asks for a turn, posts the events of the
// turn it got, and finishes it.
//
// The contract is the bot API's /council routes (all bodies JSON, the bot's key as a bearer token):
//
//	K1 GET  {base}/council/work?wait=25&free=<n>   200 {"turn": WorkTurn}, 204 no work
//	K2 POST {base}/council/turns/{turnId}/events   {"claimId","batch","events"} → 204
//	K3 POST {base}/council/turns/{turnId}/finish   {"claimId","state",…,"usage"} → 204
//
// K2 and K3 answer 409 {"code":"turn_closed"|"claim_mismatch"} when the turn must be dropped, and
// 404 when it is unknown.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teknoir/cli/pkg/api"
	"github.com/teknoir/cli/pkg/council/internal/botapi"
)

// The poster's defaults.
const (
	// DefaultFlush is how long events may wait for a batch.
	DefaultFlush = 1 * time.Second
	// DefaultHeartbeat is how long a turn may go without a request before an empty batch extends
	// its lease.
	DefaultHeartbeat = 10 * time.Second
	// DefaultBatchBytes is when a full buffered batch goes out at once.
	DefaultBatchBytes = 64 << 10
	// DefaultRequestTimeout is the deadline of one K2 or K3 attempt.
	DefaultRequestTimeout = 15 * time.Second
	// DefaultRetryBase is the wait before the first resend of a failed K2 or K3; the waits after it
	// double.
	DefaultRetryBase = 1 * time.Second
	// DefaultMaxBuffered is the most event JSON a poster buffers; events beyond it are dropped.
	DefaultMaxBuffered = 4 << 20

	// MaxEvents is the most events one K2 request may carry.
	MaxEvents = 100
	// MaxBatchBytes is the most JSON one K2 request may carry.
	MaxBatchBytes = 256 << 10
	// MaxEventBytes is the most one event's JSON may be: MaxBatchBytes less the room the rest of
	// the request body needs.
	MaxEventBytes = MaxBatchBytes - 1024

	// maxAttempts is how many times one K2 batch or one K3 is tried: the first attempt and three
	// resends, 1, 2 and 4 retry bases apart.
	maxAttempts = 3
	// maxRetryAfter caps one resend's wait, also when the server's Retry-After asks for longer.
	maxRetryAfter = 30 * time.Second

	// maxResult caps what is read from one response.
	maxResult = 1 << 20
	// workTimeout is the timeout of K1: the server holds the request up to its wait (25 s).
	workTimeout = 45 * time.Second
)

// WorkTurn is one turn the bot was given.
type WorkTurn struct {
	ID       string    `json:"id"`
	ClaimID  string    `json:"claimId"`
	ThreadID string    `json:"threadId"`
	Index    int       `json:"index"`
	Kind     string    `json:"kind"`   // ask | member
	Member   string    `json:"member"` // "", fleet, coder
	Prompt   string    `json:"prompt"`
	Options  Options   `json:"options"`
	Origin   string    `json:"origin"`
	Author   Author    `json:"author"`
	History  []History `json:"history"`
	// LeaseSeconds is how long the turn stays claimed without a request.
	LeaseSeconds int `json:"leaseSeconds"`
	// Deadline is when the turn must be finished, in epoch milliseconds.
	Deadline int64 `json:"deadline"`
}

// Options are the settings of a turn.
type Options struct {
	Mode   string `json:"mode"`
	Effort string `json:"effort"`
}

// Author is who asked.
type Author struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}

// History is one earlier exchange of the thread.
type History struct {
	Prompt string `json:"prompt"`
	Final  string `json:"final"`
}

// Usage is what a turn cost, as K3 reports it.
type Usage struct {
	Calls      int64 `json:"calls"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Ms         int64 `json:"ms"`
}

// The event types a council member may emit. Anything else is dropped by Emit: the server's set is
// closed, so an unknown type would only make the whole batch fail.
const (
	EventTurnStart   = "turn_start"
	EventPlan        = "plan"
	EventMemberStart = "member_start"
	EventTextDelta   = "text_delta"
	EventToolCall    = "tool_call"
	EventToolResult  = "tool_result"
	EventCoderUpdate = "coder_update"
	EventMemberDone  = "member_done"
	EventCritique    = "critique"
	EventBudget      = "budget"
	EventUsage       = "usage"
	EventError       = "error"
)

var eventTypes = map[string]bool{
	EventTurnStart:   true,
	EventPlan:        true,
	EventMemberStart: true,
	EventTextDelta:   true,
	EventToolCall:    true,
	EventToolResult:  true,
	EventCoderUpdate: true,
	EventMemberDone:  true,
	EventCritique:    true,
	EventBudget:      true,
	EventUsage:       true,
	EventError:       true,
}

// The reasons a Poster reports a turn dropped, through onClosed.
const (
	// ClosedTurnClosed is a 409 turn_closed: the turn was cancelled or ended elsewhere.
	ClosedTurnClosed = "turn_closed"
	// ClosedClaimMismatch is a 409 claim_mismatch: another claim holds the turn.
	ClosedClaimMismatch = "claim_mismatch"
	// ClosedNotFound is a 404: the server does not know the turn.
	ClosedNotFound = "not_found"
	// ClosedLost is the network failing for good: a batch could not be delivered.
	ClosedLost = "lost"
	// ClosedRejected is the server refusing a batch (a 4xx other than 404, 409 and 429): the turn's
	// events may not be posted at all.
	ClosedRejected = "rejected"
)

// Error is a failed hub request: the HTTP status and the server's error code, so a caller can tell
// a refused key (401, 403) from a closed turn, and Backoff can tell 401 and 403 apart.
type Error struct {
	Method  string
	URL     string // without query string
	Status  int
	Code    string
	Message string
	// RetryAfter is the server's Retry-After (whole seconds), or 0.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	msg := fmt.Sprintf("%s %s: HTTP %d", e.Method, e.URL, e.Status)
	if e.Code != "" {
		msg += " " + e.Code
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// StatusOf is the HTTP status err carries, or 0 when it carries none.
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// CodeOf is the server's error code of err, or "" when it has none.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Client is the council work queue of one bot API.
type Client struct {
	base     string
	keyFile  string
	version  string
	http     *http.Client
	councilH string
}

// New returns the hub client of a TeknoirBot. It refuses a base that may not carry the bot's key,
// and an empty key file, before any request leaves.
func New(apiBase, keyFile, version string, httpClient *http.Client) (*Client, error) {
	base := botapi.TrimBase(apiBase)
	if base == "" {
		return nil, fmt.Errorf("no bot API base: %w", botapi.ErrInsecure)
	}
	if !botapi.Allowed(base) {
		return nil, fmt.Errorf("%s: %w", base, botapi.ErrInsecure)
	}
	if strings.TrimSpace(keyFile) == "" {
		return nil, errors.New("no bot key file: $TNCTL_BOT_KEY_FILE is empty")
	}
	return &Client{
		base:     base,
		keyFile:  strings.TrimSpace(keyFile),
		version:  version,
		http:     noRedirect(httpClient),
		councilH: "tnctl/" + version,
	}, nil
}

// noRedirect is a copy of httpClient (or a new one when it is nil) that does not follow redirects:
// the bot's key rides on every request, so a 3xx must never carry it to another host or to plain
// http. A 3xx is then just a non-2xx answer. The caller's client is left as it is.
func noRedirect(httpClient *http.Client) *http.Client {
	var c http.Client
	if httpClient != nil {
		c = *httpClient
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

// do sends one request and reads its answer. A non-2xx answer is returned as an *Error, with its
// body; a 2xx one is left to the caller.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (int, []byte, error) {
	key, err := botapi.Key(c.keyFile)
	if err != nil {
		return 0, nil, err
	}
	u := c.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to marshal the request body: %w", err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("X-Teknoir-Council", c.councilH)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResult))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to read the response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		e := errorOf(method, stripQuery(u), resp.StatusCode, raw)
		e.RetryAfter = retryAfterOf(resp.Header.Get("Retry-After"))
		return resp.StatusCode, raw, e
	}
	return resp.StatusCode, raw, nil
}

// retryAfterOf reads a Retry-After header in whole seconds (the hub never sends a date), capped at
// maxRetryAfter; anything else is 0.
func retryAfterOf(v string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	d := time.Duration(n) * time.Second
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

// errorOf is the Error of a non-2xx answer, with the server's own code when it sent one.
func errorOf(method, u string, status int, body []byte) *Error {
	e := &Error{Method: method, URL: u, Status: status}
	var v struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   *struct {
			Name    string `json:"name"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) == nil {
		e.Code, e.Message = v.Code, v.Message
		if v.Error != nil {
			if e.Message == "" {
				e.Message = v.Error.Message
			}
			if e.Code == "" {
				e.Code = v.Error.Name
			}
		}
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

// Next is K1: the next turn of this bot, or nil when there is none. free is how many turn slots the
// bot has left (0-2); the server may hold the request up to 25 s, so this call allows longer.
func (c *Client) Next(ctx context.Context, free int) (*WorkTurn, error) {
	if free < 0 {
		free = 0
	}
	if free > 2 {
		free = 2
	}
	q := url.Values{"wait": {"25"}, "free": {strconv.Itoa(free)}}
	ctx, cancel := context.WithTimeout(ctx, workTimeout)
	defer cancel()
	status, raw, err := c.do(ctx, http.MethodGet, "/council/work", q, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent || len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var v struct {
		Turn *WorkTurn `json:"turn"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("unexpected answer from the council work queue: %w", err)
	}
	return v.Turn, nil
}

// Event is one event of a turn.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// PosterOptions overrides the poster's intervals, for tests. A zero field keeps the default.
type PosterOptions struct {
	// Flush is how long events may wait for a batch (DefaultFlush).
	Flush time.Duration
	// Heartbeat is how long a turn may go without a request (DefaultHeartbeat).
	Heartbeat time.Duration
	// BatchSize is the buffered size that flushes at once (DefaultBatchBytes).
	BatchSize int
	// RequestTimeout is the deadline of one K2 or K3 attempt (DefaultRequestTimeout).
	RequestTimeout time.Duration
	// RetryBase is the wait before the first resend of a failed request; the waits after it double
	// (DefaultRetryBase).
	RetryBase time.Duration
	// MaxBuffered is the most event JSON the poster buffers; events beyond it are dropped
	// (DefaultMaxBuffered).
	MaxBuffered int
}

// The poster's states.
const (
	// stateOpen takes events and sends them.
	stateOpen = iota
	// stateFinishing takes no more events: the rest of the buffer goes out, then K3.
	stateFinishing
	// stateFinished is K3 done, or given up on.
	stateFinished
	// stateClosed is a turn the server or the network ended: nothing more may be sent.
	stateClosed
)

// batch is one K2 request: its number, its whole events as JSON, and its resend state. The number
// is given once and kept for every resend, so the server stores a repeated batch once.
type batch struct {
	num      int
	events   []json.RawMessage
	attempts int
	retryAt  time.Time
}

// finishReq is the K3 request, and its resend state.
type finishReq struct {
	state   string
	final   string
	errCode string
	errMsg  string
	usage   Usage

	attempts int
	retryAt  time.Time
}

// Poster posts the events of one turn, strictly in order, and finishes it.
//
// One driver goroutine (Run or Finish) sends at a time, so batches never interleave and nothing
// goes out after K3. Every request is bounded by RequestTimeout, and a request cut short by the
// driver's ctx leaves its batch pending for the next driver, with the same number.
type Poster struct {
	c    *Client
	turn *WorkTurn

	// The settings, fixed at creation.
	flush      time.Duration
	heartbeat  time.Duration
	batchSize  int
	reqTimeout time.Duration
	retryBase  time.Duration
	maxBuf     int
	onClose    func(reason string)

	// wake tells the driver there is work, without ever blocking. It carries at most one token.
	wake chan struct{}

	// mu guards everything below.
	mu sync.Mutex

	// What is still to be sent.
	buf      []json.RawMessage // whole events: the JSON of {"type":…,"data":…}
	bufLen   int               // the sum of the lengths of buf
	oldest   time.Time         // when the oldest buffered event was added
	pending  *batch            // the batch with a number and no 2xx answer yet; at most one
	lastNum  int               // the last batch number given
	lastSent time.Time         // when the last K2 or K3 got a 2xx

	// Where the turn is.
	state   int
	reason  string        // why state is stateClosed
	fin     *finishReq    // the K3 body, once Finish was called
	finErr  error         // Finish's result, once state is stateFinished
	finDone chan struct{} // closed when state becomes stateFinished

	// Who drives.
	driving bool
	idle    chan struct{} // closed when the current driver stops

	// What was lost.
	dropped      int  // events Emit dropped
	notified     bool // onClosed was called
	finishCalled bool // Finish was called
}

// Poster returns the poster of one turn.
//
// onClosed is called when the turn must be dropped, with ClosedTurnClosed, ClosedClaimMismatch,
// ClosedNotFound, ClosedLost or ClosedRejected. It is called at most once, only from Run, after
// the sender has stopped and no lock is held; it may call Finish or Emit. It is never called once
// Finish has been called.
func (c *Client) Poster(turn *WorkTurn, onClosed func(reason string)) *Poster {
	return c.PosterWith(turn, onClosed, PosterOptions{})
}

// PosterWith is Poster with the intervals given.
func (c *Client) PosterWith(turn *WorkTurn, onClosed func(reason string), opts PosterOptions) *Poster {
	p := &Poster{
		c:          c,
		turn:       turn,
		flush:      opts.Flush,
		heartbeat:  opts.Heartbeat,
		batchSize:  opts.BatchSize,
		reqTimeout: opts.RequestTimeout,
		retryBase:  opts.RetryBase,
		maxBuf:     opts.MaxBuffered,
		onClose:    onClosed,
		wake:       make(chan struct{}, 1),
		finDone:    make(chan struct{}),
		lastSent:   time.Now(),
	}
	if p.flush <= 0 {
		p.flush = DefaultFlush
	}
	if p.heartbeat <= 0 {
		p.heartbeat = DefaultHeartbeat
	}
	if p.batchSize <= 0 || p.batchSize > MaxBatchBytes {
		p.batchSize = DefaultBatchBytes
	}
	if p.reqTimeout <= 0 {
		p.reqTimeout = DefaultRequestTimeout
	}
	if p.retryBase <= 0 {
		p.retryBase = DefaultRetryBase
	}
	if p.maxBuf <= 0 {
		p.maxBuf = DefaultMaxBuffered
	}
	if turn == nil {
		p.turn = &WorkTurn{}
	}
	return p
}

// Emit buffers one event. It never blocks on the network: a full buffer only makes the next flush
// go out sooner.
//
// data is marshalled here, on the caller's goroutine, so the caller may change or reuse it as soon
// as Emit returns. nil (or anything that marshals to null) is sent as {}; a string, number, array
// or bool, data that cannot be marshalled, and an event over MaxEventBytes are dropped, as is an
// event type the bot may not emit, an event once Finish was called, and an event the buffer has no
// room for.
func (p *Poster) Emit(eventType string, data any) {
	if !eventTypes[eventType] {
		p.dropOne()
		return
	}
	payload, ok := payloadOf(data)
	if !ok {
		p.dropOne()
		return
	}
	raw, err := json.Marshal(Event{Type: eventType, Data: payload})
	if err != nil || len(raw) > MaxEventBytes {
		p.dropOne()
		return
	}
	p.mu.Lock()
	if p.state != stateOpen || p.bufLen+len(raw) > p.maxBuf {
		p.dropped++
		p.mu.Unlock()
		return
	}
	first := len(p.buf) == 0
	if first {
		p.oldest = time.Now()
	}
	p.buf = append(p.buf, raw)
	p.bufLen += len(raw)
	// The first event wakes an idle driver too: it may be sleeping until the next heartbeat, and the
	// event must go out after Flush, not then.
	wake := first || p.bufLen >= p.batchSize || len(p.buf) >= MaxEvents
	p.mu.Unlock()
	if wake {
		p.signal()
	}
}

// payloadOf is the JSON of one event's data: {} for nil and null, and not at all for anything that
// is not a JSON object, since the server's events are objects.
func payloadOf(data any) (json.RawMessage, bool) {
	if data == nil {
		return json.RawMessage("{}"), true
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return json.RawMessage("{}"), true
	}
	if raw[0] != '{' {
		return nil, false
	}
	return raw, true
}

// dropOne counts one event Emit dropped.
func (p *Poster) dropOne() {
	p.mu.Lock()
	p.dropped++
	p.mu.Unlock()
}

// Dropped is how many events Emit dropped: an unknown type, data that is not an object, an event
// that is too large, one emitted after Finish, or one the buffer had no room for.
func (p *Poster) Dropped() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dropped
}

// signal wakes the driver without ever blocking.
func (p *Poster) signal() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run sends the turn's events until ctx ends or the turn is dropped or finished. It returns at
// once when another driver runs, or when the turn is already closed or finished.
//
// It calls onClosed at most once, when the turn was dropped by the server or the network and Finish
// had not been called when the driver stopped; it runs after the driver has stopped, so onClosed may
// call Finish or Emit. A Finish called from another goroutine at that moment may return before
// onClosed runs: handle both idempotently.
//
// End a turn by calling Finish while Run still runs, and cancel Run's ctx only after Finish
// returned. Cancelling Run first abandons a K2 in flight that Finish then resends; the server
// stores a resent batch once, but the two requests may overlap.
func (p *Poster) Run(ctx context.Context) {
	p.mu.Lock()
	if p.driving || p.state == stateClosed || p.state == stateFinished {
		p.mu.Unlock()
		return
	}
	p.driving = true
	p.idle = make(chan struct{})
	p.mu.Unlock()

	p.drive(ctx)

	p.mu.Lock()
	notify := p.state == stateClosed && !p.finishCalled && !p.notified
	reason := p.reason
	if notify {
		p.notified = true
	}
	p.mu.Unlock()
	if notify && p.onClose != nil {
		p.onClose(reason)
	}
}

// drive sends what is to be sent until the turn is closed or finished, or ctx ends. Only one driver
// runs at a time, and neither mu nor any other lock is held during a request or a wait.
func (p *Poster) drive(ctx context.Context) {
	defer p.stopDriver()
	for {
		p.mu.Lock()
		if p.state == stateClosed || p.state == stateFinished || ctx.Err() != nil {
			p.mu.Unlock()
			return
		}
		now := time.Now()
		switch {
		case p.pending != nil:
			// A batch that has a number waits out its retry, then goes again as it is.
			b := p.pending
			if wait := b.retryAt.Sub(now); wait > 0 {
				p.mu.Unlock()
				if !p.await(ctx, wait) {
					return
				}
				continue
			}
			p.mu.Unlock()
			p.sendBatch(ctx, b)

		case len(p.buf) > 0 && (p.state == stateFinishing || p.bufLen >= p.batchSize ||
			len(p.buf) >= MaxEvents || now.Sub(p.oldest) >= p.flush):
			b := p.takeBatchLocked()
			p.pending = b
			p.mu.Unlock()
			p.sendBatch(ctx, b)

		case p.state == stateFinishing:
			if p.fin == nil {
				p.finishLocked(nil)
				p.mu.Unlock()
				continue
			}
			fr := p.fin
			if wait := fr.retryAt.Sub(now); wait > 0 {
				p.mu.Unlock()
				if !p.await(ctx, wait) {
					return
				}
				continue
			}
			p.mu.Unlock()
			p.sendFinish(ctx, fr)

		case now.Sub(p.lastSent) >= p.heartbeat:
			// Nothing may go unsaid for the lease's sake: an empty batch keeps the turn ours.
			b := &batch{num: p.nextNumLocked(), events: []json.RawMessage{}}
			p.pending = b
			p.mu.Unlock()
			p.sendBatch(ctx, b)

		default:
			wait := p.nextWaitLocked(now)
			p.mu.Unlock()
			if !p.await(ctx, wait) {
				return
			}
		}
	}
}

// stopDriver hands the poster back, waking a Finish that waits for it.
func (p *Poster) stopDriver() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.driving = false
	if p.idle != nil {
		close(p.idle)
		p.idle = nil
	}
}

// await waits for d, for a wake from Emit or Finish, or for ctx to end. It reports whether the
// driver is to go on.
func (p *Poster) await(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-p.wake:
		return true
	case <-t.C:
		return true
	}
}

// nextWaitLocked is how long the driver may wait: until the oldest event must flush, or until the
// next heartbeat is due.
func (p *Poster) nextWaitLocked(now time.Time) time.Duration {
	next := p.lastSent.Add(p.heartbeat)
	if len(p.buf) > 0 {
		if flush := p.oldest.Add(p.flush); flush.Before(next) {
			next = flush
		}
	}
	if d := next.Sub(now); d > 0 {
		return d
	}
	return 0
}

// nextNumLocked gives a batch its number.
func (p *Poster) nextNumLocked() int {
	p.lastNum++
	return p.lastNum
}

// takeBatchLocked moves the first part of the buffer into a new batch: at most MaxEvents events,
// at most MaxBatchBytes of JSON and at most the batch size, but always at least one event.
func (p *Poster) takeBatchLocked() *batch {
	limit := p.batchSize
	if limit > MaxBatchBytes {
		limit = MaxBatchBytes
	}
	n, size := 0, 0
	for _, raw := range p.buf {
		// The server measures the events as one JSON array: the events, n commas and two brackets.
		if n > 0 && (n >= MaxEvents || size+len(raw)+n+2 > limit) {
			break
		}
		size += len(raw)
		n++
	}
	events := make([]json.RawMessage, n)
	copy(events, p.buf[:n])
	for i := 0; i < n; i++ {
		p.buf[i] = nil // the batch holds them now
	}
	p.buf = p.buf[n:]
	p.bufLen -= size
	if len(p.buf) == 0 {
		p.buf, p.oldest = nil, time.Time{}
	}
	// What is left keeps the oldest event's time: it has waited at least as long as the batch taken.
	return &batch{num: p.nextNumLocked(), events: events}
}

// sendBatch is one K2 attempt for b, outside mu. It is the only code that sends K2 and the only
// code that clears pending.
func (p *Poster) sendBatch(ctx context.Context, b *batch) {
	events := b.events
	if events == nil {
		events = []json.RawMessage{}
	}
	body := struct {
		ClaimID string            `json:"claimId"`
		Batch   int               `json:"batch"`
		Events  []json.RawMessage `json:"events"`
	}{ClaimID: p.turn.ClaimID, Batch: b.num, Events: events}
	path := "/council/turns/" + url.PathEscape(p.turn.ID) + "/events"

	attemptCtx, cancel := context.WithTimeout(ctx, p.reqTimeout)
	_, _, err := p.c.do(attemptCtx, http.MethodPost, path, nil, body)
	cancel()

	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		p.pending = nil
		p.lastSent = time.Now()
		return
	}
	if ctx.Err() != nil {
		// The caller's ctx ended the attempt: the batch stays pending, with its number, its events
		// and its attempts, for the next driver to resend.
		return
	}
	var he *Error
	if errors.As(err, &he) {
		switch {
		case he.Status == http.StatusConflict && he.Code == "claim_mismatch":
			p.endedLocked(ClosedClaimMismatch)
		case he.Status == http.StatusConflict:
			p.endedLocked(ClosedTurnClosed)
		case he.Status == http.StatusNotFound:
			p.endedLocked(ClosedNotFound)
		case refused(he.Status):
			if p.state == stateFinishing {
				// One refused batch does not hold up the turn's answer.
				p.pending = nil
				return
			}
			p.endedLocked(ClosedRejected)
		default:
			p.retryBatchLocked(b, he.RetryAfter)
		}
		return
	}
	// A network error, or an attempt that timed out while ctx is still live.
	p.retryBatchLocked(b, 0)
}

// refused is a status a resend cannot change: a 3xx (redirects are never followed) or a 4xx other
// than 429. 404 and 409 are handled before it.
func refused(status int) bool {
	return status >= 300 && status < 500 && status != http.StatusTooManyRequests
}

// retryWait is the wait before the attempts-th resend: 1, 2 or 4 retry bases, or the server's
// Retry-After when it asks for longer, but never more than maxRetryAfter.
func (p *Poster) retryWait(attempts int, retryAfter time.Duration) time.Duration {
	d := p.retryBase << uint(attempts-1)
	if retryAfter > d {
		d = retryAfter
	}
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

// retryBatchLocked resends b after 1, 2 and 4 retry bases (or the server's Retry-After), then gives
// it up.
func (p *Poster) retryBatchLocked(b *batch, retryAfter time.Duration) {
	b.attempts++
	if b.attempts > maxAttempts {
		if p.state == stateFinishing {
			// The events are lost, but the turn's answer still goes out.
			p.pending = nil
			p.dropBufferLocked()
			return
		}
		p.closeLocked(ClosedLost)
		return
	}
	b.retryAt = time.Now().Add(p.retryWait(b.attempts, retryAfter))
}

// sendFinish is one K3 attempt for fr, outside mu.
func (p *Poster) sendFinish(ctx context.Context, fr *finishReq) {
	body := map[string]any{"claimId": p.turn.ClaimID, "state": fr.state, "usage": fr.usage}
	if fr.final != "" {
		body["final"] = fr.final
	}
	if fr.errCode != "" {
		msg := fr.errMsg
		if msg == "" {
			msg = fr.errCode
		}
		body["error"] = map[string]string{"code": fr.errCode, "message": msg}
	}
	path := "/council/turns/" + url.PathEscape(p.turn.ID) + "/finish"

	attemptCtx, cancel := context.WithTimeout(ctx, p.reqTimeout)
	_, _, err := p.c.do(attemptCtx, http.MethodPost, path, nil, body)
	cancel()

	p.mu.Lock()
	defer p.mu.Unlock()
	if err == nil {
		p.finishLocked(nil)
		return
	}
	if ctx.Err() != nil {
		// The caller's ctx ended the attempt: the finish stays, for the next driver.
		return
	}
	var retryAfter time.Duration
	var he *Error
	if errors.As(err, &he) {
		if he.Status == http.StatusConflict && he.Code == "turn_closed" {
			// The turn ended otherwise: there is nothing left to report.
			p.finishLocked(nil)
			return
		}
		if refused(he.Status) {
			p.finishLocked(err)
			return
		}
		retryAfter = he.RetryAfter
	}
	fr.attempts++
	if fr.attempts > maxAttempts {
		p.finishLocked(err)
		return
	}
	fr.retryAt = time.Now().Add(p.retryWait(fr.attempts, retryAfter))
}

// Finish sends what is left, then K3, and returns K3's result.
//
// A turn the server ended (ClosedTurnClosed, ClosedClaimMismatch, ClosedNotFound) returns nil
// without K3; a lost or rejected one still sends K3, since the answer is all that is left. Finish
// never waits longer than ctx allows: when ctx ends it returns ctx.Err(), leaving the rest pending
// for a later call.
func (p *Poster) Finish(ctx context.Context, state, final, errCode, errMsg string, usage Usage) error {
	p.mu.Lock()
	if p.state == stateFinished {
		err := p.finErr
		p.mu.Unlock()
		return err
	}
	if p.fin == nil {
		p.fin = &finishReq{state: state, final: final, errCode: errCode, errMsg: errMsg, usage: usage}
	}
	p.finishCalled = true
	take := p.canFinishLocked()
	p.mu.Unlock()
	if !take {
		return nil
	}

	for {
		p.mu.Lock()
		if p.state == stateFinished {
			err := p.finErr
			p.mu.Unlock()
			return err
		}
		if p.state == stateClosed && !p.canFinishLocked() {
			p.mu.Unlock()
			return nil
		}
		if ctx.Err() != nil {
			p.mu.Unlock()
			return ctx.Err()
		}
		if !p.driving {
			// Nobody is sending: this Finish is the driver.
			p.driving = true
			p.idle = make(chan struct{})
			p.mu.Unlock()
			p.drive(ctx)
			continue
		}
		idle := p.idle
		p.signal()
		p.mu.Unlock()
		select {
		case <-p.finDone:
			p.mu.Lock()
			err := p.finErr
			p.mu.Unlock()
			return err
		case <-idle:
			// The driver stopped, for example because its ctx ended: take over.
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// canFinishLocked moves the poster to finishing, and reports whether a K3 may still go out. A turn
// the server ended has no finish; a lost or rejected one keeps only its answer.
func (p *Poster) canFinishLocked() bool {
	if p.state != stateClosed {
		p.state = stateFinishing
		return true
	}
	switch p.reason {
	case ClosedTurnClosed, ClosedClaimMismatch, ClosedNotFound:
		return false
	}
	// ClosedLost or ClosedRejected: the events are lost, but the answer still goes out.
	p.dropBufferLocked()
	p.pending = nil
	p.state = stateFinishing
	return true
}

// endedLocked records that the server ended the turn: the poster closes, or, while finishing, the
// finish is done without a K3.
func (p *Poster) endedLocked(reason string) {
	if p.state == stateFinishing {
		p.finishLocked(nil)
		return
	}
	p.closeLocked(reason)
}

// closeLocked marks the turn dropped: nothing more is sent.
func (p *Poster) closeLocked(reason string) {
	if p.state == stateClosed || p.state == stateFinished {
		return
	}
	p.state = stateClosed
	p.reason = reason
	p.dropBufferLocked()
	p.pending = nil
}

// finishLocked records K3's result.
func (p *Poster) finishLocked(err error) {
	if p.state == stateFinished {
		return
	}
	p.state = stateFinished
	p.finErr = err
	close(p.finDone)
}

// dropBufferLocked throws the buffered events away.
func (p *Poster) dropBufferLocked() {
	p.buf, p.bufLen, p.oldest = nil, 0, time.Time{}
}

// Backoff is how long to wait after the attempt-th consecutive failed Next (attempt from 1). A
// refused key (401 or 403) needs a person, so it is 60 s; anything else doubles from 1 s to at most
// 30 s, with ±20 % jitter from rnd.
func Backoff(err error, attempt int, rnd *rand.Rand) time.Duration {
	if s := StatusOf(err); s == http.StatusUnauthorized || s == http.StatusForbidden {
		return 60 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	const maxWait = 30 * time.Second
	base := time.Second
	for i := 1; i < attempt && base < maxWait; i++ {
		base *= 2
	}
	if base > maxWait {
		base = maxWait
	}
	return jitter(base, rnd)
}

// jitter spreads d by ±20 %, using rnd (nil: no jitter).
func jitter(d time.Duration, rnd *rand.Rand) time.Duration {
	if rnd == nil {
		return d
	}
	spread := float64(d) * 0.2
	f := float64(d) - spread + rnd.Float64()*2*spread
	if f < 0 {
		return 0
	}
	return time.Duration(f)
}

// userAgent is the User-Agent of every hub request: pkg/api's, the same one tnctl sends elsewhere.
func userAgent() string { return api.UserAgent() }

// stripQuery drops the query string of a URL, so an error does not carry waits and free counts.
func stripQuery(u string) string {
	if p, err := url.Parse(u); err == nil {
		p.RawQuery, p.Fragment = "", ""
		return p.String()
	}
	return u
}
