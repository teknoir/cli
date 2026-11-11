package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teknoir/cli/pkg/council/internal/botapi"
)

// botAPI is a fake bot API for the hub tests: it records requests and lets each test answer them.
type botAPI struct {
	t      *testing.T
	server *httptest.Server
	handle func(w http.ResponseWriter, r *http.Request, n int, body []byte)
	key    string

	mu   sync.Mutex
	reqs []req
}

type req struct {
	method string
	path   string
	query  string
	header http.Header
	body   map[string]any
	raw    []byte
}

func newBotAPI(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, n int, body []byte)) *botAPI {
	t.Helper()
	b := &botAPI{t: t, handle: handle, key: "bot-key"}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		b.mu.Lock()
		b.reqs = append(b.reqs, req{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), body: body, raw: raw})
		n := len(b.reqs)
		b.mu.Unlock()
		b.handle(w, r, n, raw)
	}))
	t.Cleanup(b.server.Close)
	return b
}

func (b *botAPI) requests() []req {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]req(nil), b.reqs...)
}

func (b *botAPI) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.reqs)
}

func keyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bot-key")
	if err := os.WriteFile(p, []byte("bot-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newClient(t *testing.T, apiBase, keyPath string) *Client {
	t.Helper()
	c, err := New(apiBase, keyPath, "9.9.9", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNextDecodesTurn(t *testing.T) {
	want := `{"turn":{"id":"trn_1","claimId":"clm_1","threadId":"thr_1","index":3,"kind":"member","member":"fleet","prompt":"what is wrong?","options":{"mode":"normal","effort":"high"},"origin":"ui","author":{"email":"a@b.c","displayName":"Anders"},"history":[{"prompt":"p1","final":"f1"},{"prompt":"p2","final":"f2"}],"leaseSeconds":60,"deadline":1799999999999}}`
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, want)
	})
	c := newClient(t, b.server.URL, keyFile(t))

	turn, err := c.Next(context.Background(), 2)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if turn == nil {
		t.Fatal("Next returned nil for a 200")
	}
	if turn.ID != "trn_1" || turn.ClaimID != "clm_1" || turn.ThreadID != "thr_1" || turn.Index != 3 ||
		turn.Kind != "member" || turn.Member != "fleet" || turn.Prompt != "what is wrong?" ||
		turn.Options.Mode != "normal" || turn.Options.Effort != "high" || turn.Origin != "ui" ||
		turn.Author.Email != "a@b.c" || turn.Author.DisplayName != "Anders" ||
		turn.LeaseSeconds != 60 || turn.Deadline != 1799999999999 {
		t.Errorf("turn = %+v", turn)
	}
	if len(turn.History) != 2 || turn.History[0] != (History{Prompt: "p1", Final: "f1"}) || turn.History[1].Final != "f2" {
		t.Errorf("history = %+v", turn.History)
	}

	r := b.requests()[0]
	if r.method != http.MethodGet || r.path != "/council/work" {
		t.Errorf("request = %s %s", r.method, r.path)
	}
	if r.query != "free=2&wait=25" {
		t.Errorf("query = %q, want free=2&wait=25", r.query)
	}
	if got := r.header.Get("Authorization"); got != "Bearer bot-key" {
		t.Errorf("Authorization = %q, want the bot's key", got)
	}
	if got := r.header.Get("X-Teknoir-Council"); got != "tnctl/9.9.9" {
		t.Errorf("X-Teknoir-Council = %q", got)
	}
	if ua := r.header.Get("User-Agent"); !strings.HasPrefix(ua, "tnctl/") {
		t.Errorf("User-Agent = %q", ua)
	}
}

func TestNextNoWork(t *testing.T) {
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	turn, err := c.Next(context.Background(), 0)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if turn != nil {
		t.Errorf("turn = %+v, want nil for a 204", turn)
	}
}

func TestNextErrorCarriesStatus(t *testing.T) {
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	_, err := c.Next(context.Background(), 0)
	if err == nil {
		t.Fatal("Next succeeded on a 503")
	}
	if got := StatusOf(err); got != http.StatusServiceUnavailable {
		t.Errorf("StatusOf = %d, want 503", got)
	}
}

func TestNewBaseRules(t *testing.T) {
	kf := keyFile(t)
	for _, base := range []string{"http://example.com", "ftp://x", ""} {
		if _, err := New(base, kf, "v", nil); err == nil {
			t.Errorf("New accepted the base %q", base)
		}
	}
	if _, err := New("http://127.0.0.1:1", kf, "v", nil); err != nil {
		t.Errorf("New refused a loopback base: %v", err)
	}
	if _, err := New("https://tb-api.teknoir.cloud/api/device/bot/v1", "", "v", nil); err == nil {
		t.Error("New accepted an empty key file")
	}
}

// TestNewDoesNotFollowRedirects sends the bot's key nowhere but the base: a 3xx is an answer, not
// an invitation.
func TestNewDoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer base.Close()

	caller := &http.Client{Timeout: 5 * time.Second}
	c, err := New(base.URL, keyFile(t), "v", caller)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Next(context.Background(), 0); err == nil {
		t.Fatal("Next followed the redirect")
	} else if s := StatusOf(err); s != http.StatusTemporaryRedirect {
		t.Errorf("StatusOf = %d, want the 307 itself", s)
	}
	if n := reached.Load(); n != 0 {
		t.Errorf("the redirect target got %d requests, want none", n)
	}
	if caller.CheckRedirect != nil {
		t.Error("New changed the caller's client")
	}
}

// turn is the turn the poster tests post.
func turn() *WorkTurn {
	return &WorkTurn{ID: "trn_1", ClaimID: "clm_1"}
}

// posterOpts is a fast poster: the tests do not wait seconds for a retry.
func posterOpts() PosterOptions {
	return PosterOptions{
		Flush:          20 * time.Millisecond,
		Heartbeat:      200 * time.Millisecond,
		RetryBase:      10 * time.Millisecond,
		RequestTimeout: 200 * time.Millisecond,
	}
}

// recorder records what the poster sent, in the order the server saw it.
type recorder struct {
	mu   sync.Mutex
	reqs []sent
}

// sent is one K2 or K3 request of a poster.
type sent struct {
	finish bool
	batch  int
	events []Event
	body   map[string]any
	at     time.Time
}

// add records one request body.
func (r *recorder) add(path string, body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := sent{finish: strings.HasSuffix(path, "/finish"), body: body, at: time.Now()}
	if !s.finish {
		s.batch, s.events = decodeEvents(body)
	}
	r.reqs = append(r.reqs, s)
}

func (r *recorder) all() []sent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sent(nil), r.reqs...)
}

func (r *recorder) k2() []sent {
	var out []sent
	for _, s := range r.all() {
		if !s.finish {
			out = append(out, s)
		}
	}
	return out
}

func (r *recorder) k3() []sent {
	var out []sent
	for _, s := range r.all() {
		if s.finish {
			out = append(out, s)
		}
	}
	return out
}

// deliver is the number of buffered events a batch holds: the per-event types of one test.
func decodeEvents(body map[string]any) (int, []Event) {
	batch, _ := body["batch"].(float64)
	raw, _ := json.Marshal(body["events"])
	var evs []Event
	_ = json.Unmarshal(raw, &evs)
	return int(batch), evs
}

// okHandler records every request and answers 204.
func okHandler(r *recorder) func(w http.ResponseWriter, req *http.Request, n int, body []byte) {
	return func(w http.ResponseWriter, req *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		r.add(req.URL.Path, v)
		w.WriteHeader(http.StatusNoContent)
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// stored is the events the server keeps: for every batch number, the events of its last attempt,
// in the order the numbers were given.
func stored(rec *recorder) ([]int, [][]Event) {
	var nums []int
	byNum := map[int][]Event{}
	for _, s := range rec.k2() {
		if _, ok := byNum[s.batch]; !ok {
			nums = append(nums, s.batch)
		}
		byNum[s.batch] = s.events
	}
	out := make([][]Event, len(nums))
	for i, n := range nums {
		out[i] = byNum[n]
	}
	return nums, out
}

func TestPosterBatchesInOrder(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, posterOpts())
	p.Emit(EventTurnStart, map[string]any{"a": 1})
	p.Emit(EventTextDelta, map[string]any{"text": "hi"})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()

	waitFor(t, "the first batch", func() bool { return len(ev.k2()) >= 1 })
	batches, payloads := stored(ev)
	if len(batches) != 1 {
		cancel()
		<-done
		t.Fatalf("batches = %v, want one batch of the two events", batches)
	}
	if batches[0] != 1 {
		t.Errorf("first batch number = %d, want 1", batches[0])
	}
	if len(payloads[0]) != 2 || payloads[0][0].Type != EventTurnStart || payloads[0][1].Type != EventTextDelta {
		t.Errorf("events = %+v, want the two in order", payloads[0])
	}

	// The next flush is batch 2.
	p.Emit(EventMemberDone, map[string]any{})
	waitFor(t, "the second batch", func() bool { return len(ev.k2()) >= 2 })
	cancel()
	<-done
	batches, payloads = stored(ev)
	if len(batches) != 2 || batches[1] != 2 {
		t.Fatalf("batches = %v, want 1 then 2", batches)
	}
	if len(payloads[1]) != 1 || payloads[1][0].Type != EventMemberDone {
		t.Errorf("second batch = %+v", payloads[1])
	}

	// The claim travels with every batch.
	for _, r := range b.requests() {
		if r.path != "/council/turns/trn_1/events" {
			t.Errorf("path = %q", r.path)
		}
		if r.body["claimId"] != "clm_1" {
			t.Errorf("claimId = %v", r.body["claimId"])
		}
	}
}

func TestPosterManyEventsSplit(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Flush = 100 * time.Millisecond
	opts.Heartbeat = time.Hour
	p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, opts)
	for i := 0; i < 150; i++ {
		p.Emit(EventTextDelta, map[string]any{"i": i})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	waitFor(t, "both batches", func() bool { return len(ev.k2()) >= 2 })
	cancel()
	<-done
	batches, payloads := stored(ev)
	if len(batches) != 2 {
		t.Fatalf("batches = %v, want two (100 and 50)", batches)
	}
	if len(payloads[0]) != 100 || len(payloads[1]) != 50 {
		t.Errorf("batch sizes = %d and %d, want 100 and 50", len(payloads[0]), len(payloads[1]))
	}
	if batches[0] != 1 || batches[1] != 2 {
		t.Errorf("batch numbers = %v, want 1, 2", batches)
	}
}

func TestPosterBatchBytes(t *testing.T) {
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Flush = 50 * time.Millisecond
	p := c.PosterWith(turn(), nil, opts)
	chunk := strings.Repeat("x", 40<<10)
	for i := 0; i < 4; i++ { // 160 KiB buffered against a 64 KiB batch size
		p.Emit(EventTextDelta, map[string]any{"text": chunk})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	waitFor(t, "three requests", func() bool { return b.count() >= 3 })
	cancel()
	<-done
	n := b.count()
	if n < 3 {
		t.Fatalf("requests = %d, want at least 3 for 160 KiB of events", n)
	}
	for _, r := range b.requests() {
		if len(r.raw) > MaxBatchBytes {
			t.Errorf("one request carried %d bytes, over the %d-byte limit", len(r.raw), MaxBatchBytes)
		}
	}
}

func TestPosterRetriesSameBatchNumber(t *testing.T) {
	ev := &recorder{}
	var attempts atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		ev.add(r.URL.Path, v)
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	waitFor(t, "the resent batch", func() bool { return ev.k2() != nil && len(ev.k2()) >= 2 })
	cancel()
	<-done
	batches, _ := stored(ev)
	if len(batches) == 0 {
		t.Fatal("nothing was sent")
	}
	if got := len(ev.k2()); got < 2 {
		t.Fatalf("K2 requests = %d, want the batch resent after the 503", got)
	}
	for _, s := range ev.k2() {
		if s.batch != 1 {
			t.Errorf("batch number = %d, want the same number 1 resent", s.batch)
		}
	}
}

func TestPosterTurnClosed(t *testing.T) {
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"code":"turn_closed","state":"cancelled"}`)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	var mu sync.Mutex
	var reasons []string
	p := c.PosterWith(turn(), func(reason string) {
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
	}, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})

	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after a 409 turn_closed")
	}
	mu.Lock()
	got := append([]string(nil), reasons...)
	mu.Unlock()
	if len(got) != 1 || got[0] != ClosedTurnClosed {
		t.Errorf("onClosed was called with %v, want [%s]", got, ClosedTurnClosed)
	}
	if n := b.count(); n != 1 {
		t.Errorf("requests = %d, want 1: a closed turn is not retried", n)
	}
	// Finish after the server closed the turn: nil, and no K3.
	if err := p.Finish(context.Background(), "done", "answer", "", "", Usage{}); err != nil {
		t.Errorf("Finish after turn_closed = %v, want nil", err)
	}
	if n := b.count(); n != 1 {
		t.Errorf("requests = %d after Finish, want 1: no K3 for a closed turn", n)
	}
}

// An event emitted while the driver is idle (sleeping until the next heartbeat) goes out after
// Flush, not at the heartbeat.
func TestPosterFirstEventWakesIdleDriver(t *testing.T) {
	arrived := make(chan time.Time, 8)
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.Contains(string(body), EventTurnStart) {
			arrived <- time.Now()
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Heartbeat = 2 * time.Second
	p := c.PosterWith(turn(), nil, opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	p.Emit(EventTurnStart, map[string]any{"mode": "quick"})
	select {
	case at := <-arrived:
		if d := at.Sub(start); d > 500*time.Millisecond {
			t.Errorf("the event arrived after %v, want about Flush (20 ms)", d)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the event did not arrive before the heartbeat")
	}
}

// A 503 with Retry-After waits as long as the server asks (capped), not the 1, 2, 4 retry bases.
func TestPosterHonoursRetryAfter(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprint(w, `{"code":"unavailable"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), nil, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Finish(ctx, "done", "answer", "", "", Usage{}); err != nil {
		t.Fatalf("Finish = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(times) < 2 {
		t.Fatalf("requests = %d, want the batch resent", len(times))
	}
	if d := times[1].Sub(times[0]); d < 900*time.Millisecond {
		t.Errorf("resent after %v, want ≥ the server's Retry-After of 1 s", d)
	}
	reqs := b.requests()
	if reqs[0].body["batch"] != reqs[1].body["batch"] {
		t.Errorf("the resend has batch %v, want the same number %v", reqs[1].body["batch"], reqs[0].body["batch"])
	}
}

// A 3xx on K2 is refused at once (redirects are never followed), not retried like a 5xx.
func TestPosterRedirectIsRejected(t *testing.T) {
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		w.Header().Set("Location", "http://elsewhere.invalid/")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	reasons := make(chan string, 2)
	p := c.PosterWith(turn(), func(reason string) { reasons <- reason }, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})
	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after a 307")
	}
	if got := <-reasons; got != ClosedRejected {
		t.Errorf("onClosed(%q), want %q", got, ClosedRejected)
	}
	if n := b.count(); n != 1 {
		t.Errorf("requests = %d, want 1: a redirect is not retried", n)
	}
}

// With the batch size at the server's limit, a batch's JSON array (commas and brackets included)
// never goes over MaxBatchBytes.
func TestPosterBatchArrayFitsServerLimit(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v struct {
			Events json.RawMessage `json:"events"`
		}
		_ = json.Unmarshal(body, &v)
		mu.Lock()
		sizes = append(sizes, len(v.Events))
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.BatchSize = MaxBatchBytes
	p := c.PosterWith(turn(), nil, opts)
	// 100 events whose raw sizes add up to exactly MaxBatchBytes: without the separators they would
	// all fit in one batch.
	const n = 100
	overhead := len(`{"type":"text_delta","data":{"t":""}}`)
	per := MaxBatchBytes / n
	for i := 0; i < n; i++ {
		p.Emit(EventTextDelta, map[string]any{"t": strings.Repeat("a", per-overhead)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Finish(ctx, "done", "", "", "", Usage{}); err != nil {
		t.Fatalf("Finish = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, size := range sizes {
		if size > MaxBatchBytes {
			t.Errorf("request %d carries %d bytes of events, over %d", i+1, size, MaxBatchBytes)
		}
	}
}

func TestPosterClaimMismatchAndNotFound(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"claim_mismatch", http.StatusConflict, `{"code":"claim_mismatch"}`, ClosedClaimMismatch},
		{"not_found", http.StatusNotFound, `{}`, ClosedNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			c := newClient(t, b.server.URL, keyFile(t))
			reasons := make(chan string, 2)
			p := c.PosterWith(turn(), func(reason string) { reasons <- reason }, posterOpts())
			p.Emit(EventTurnStart, map[string]any{})
			done := make(chan struct{})
			go func() { defer close(done); p.Run(context.Background()) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Run did not return")
			}
			select {
			case got := <-reasons:
				if got != tc.want {
					t.Errorf("onClosed = %q, want %q", got, tc.want)
				}
			default:
				t.Fatal("onClosed was not called")
			}
		})
	}
}

// TestPosterOrderThenFinish: 250 events, and Finish while Run runs. Every batch goes out once, in
// order and numbered 1, 2, 3, then K3, and nothing after it.
func TestPosterOrderThenFinish(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))

	var closed atomic.Int32
	p := c.PosterWith(turn(), func(string) { closed.Add(1) }, posterOpts())
	for i := 0; i < 250; i++ {
		p.Emit(EventTextDelta, map[string]any{"i": i})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()

	if err := p.Finish(ctx, "done", "the answer", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	<-done

	nums, payloads := stored(ev)
	if len(nums) != 3 || nums[0] != 1 || nums[1] != 2 || nums[2] != 3 {
		t.Fatalf("batch numbers = %v, want 1, 2, 3", nums)
	}
	if len(payloads[0]) != 100 || len(payloads[1]) != 100 || len(payloads[2]) != 50 {
		t.Fatalf("batch sizes = %d, %d, %d, want 100, 100, 50", len(payloads[0]), len(payloads[1]), len(payloads[2]))
	}
	all := ev.all()
	if all[len(all)-1].finish != true {
		t.Fatalf("the last request is not K3: %+v", all[len(all)-1])
	}
	if n := len(ev.k3()); n != 1 {
		t.Errorf("K3 requests = %d, want exactly 1", n)
	}
	if n := closed.Load(); n != 0 {
		t.Errorf("onClosed was called %d times, want 0", n)
	}

	// An event emitted after Finish is dropped, and never sent.
	before := b.count()
	p.Emit(EventTextDelta, map[string]any{"i": "late"})
	time.Sleep(50 * time.Millisecond)
	if n := b.count(); n != before {
		t.Errorf("requests after Finish = %d, want %d: a finished turn sends nothing", n-before, 0)
	}
	if p.Dropped() == 0 {
		t.Error("Dropped() = 0 after an event emitted post-Finish")
	}
}

// TestFinishWithoutRun: Finish alone sends the buffered events and then K3.
func TestFinishWithoutRun(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), nil, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})
	p.Emit(EventMemberDone, map[string]any{"final": "yes"})

	if err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{Calls: 3, Input: 100, Output: 20, CacheRead: 5, CacheWrite: 1, Ms: 1200}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	nums, payloads := stored(ev)
	if len(nums) != 1 || len(payloads[0]) != 2 {
		t.Fatalf("batches = %v, want the two buffered events in one batch before K3", nums)
	}
	if k3 := ev.k3(); len(k3) != 1 {
		t.Fatalf("K3 requests = %d, want 1", len(k3))
	}
	all := ev.all()
	if !all[len(all)-1].finish {
		t.Error("K3 is not the last request")
	}
	body := ev.k3()[0].body
	if body["claimId"] != "clm_1" || body["state"] != "done" || body["final"] != "the answer" {
		t.Errorf("finish body = %v", body)
	}
	usage, _ := body["usage"].(map[string]any)
	if usage["calls"] != float64(3) || usage["input"] != float64(100) || usage["output"] != float64(20) ||
		usage["cacheRead"] != float64(5) || usage["cacheWrite"] != float64(1) || usage["ms"] != float64(1200) {
		t.Errorf("usage = %v", usage)
	}
	if _, ok := body["error"]; ok {
		t.Errorf("finish body has an error: %v", body)
	}
}

// TestFinishAfterRunCtxEnded: Run's ctx ends first, then Finish, with a live ctx, takes over and
// sends the rest and K3.
func TestFinishAfterRunCtxEnded(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Flush = 100 * time.Millisecond
	opts.Heartbeat = time.Hour
	p := c.PosterWith(turn(), nil, opts)
	for i := 0; i < 150; i++ {
		p.Emit(EventTextDelta, map[string]any{"i": i})
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(runCtx) }()
	waitFor(t, "the first batch", func() bool { return len(ev.k2()) >= 1 })
	runCancel()
	<-done

	if err := p.Finish(context.Background(), "done", "f", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	nums, payloads := stored(ev)
	total := 0
	for _, part := range payloads {
		total += len(part)
	}
	if total != 150 {
		t.Errorf("events sent = %d, want 150", total)
	}
	if len(nums) < 2 || nums[0] != 1 || nums[len(nums)-1] != len(nums) {
		t.Errorf("batch numbers = %v, want 1..n in order", nums)
	}
	all := ev.all()
	if !all[len(all)-1].finish {
		t.Error("K3 is not the last request")
	}
}

// TestFinishTakesOverPending: a batch in its retry wait when Run's ctx ends is resent by Finish with
// the same number, before the rest and K3.
func TestFinishTakesOverPending(t *testing.T) {
	ev := &recorder{}
	var attempts atomic.Int32
	seen := make(chan struct{}, 8)
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		ev.add(r.URL.Path, v)
		if strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		seen <- struct{}{}
		if attempts.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Flush = 100 * time.Millisecond
	opts.Heartbeat = time.Hour
	opts.RetryBase = 50 * time.Millisecond // the third attempt waits 100 ms
	p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, opts)
	for i := 0; i < 150; i++ {
		p.Emit(EventTextDelta, map[string]any{"i": i})
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(runCtx) }()
	<-seen
	<-seen // two 503s: the batch is now in its retry wait
	runCancel()
	<-done

	if err := p.Finish(context.Background(), "done", "f", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	var numbers []int
	for _, s := range ev.k2() {
		numbers = append(numbers, s.batch)
	}
	if len(numbers) < 3 || numbers[0] != 1 || numbers[1] != 1 || numbers[2] != 1 {
		t.Fatalf("batch numbers = %v, want the pending batch 1 resent by Finish", numbers)
	}
	if got := len(ev.k2()[2].events); got != 100 {
		t.Errorf("the resent batch carries %d events, want the original 100", got)
	}
	if len(numbers) != 4 || numbers[3] != 2 {
		t.Errorf("batch numbers = %v, want 1, 1, 1, 2", numbers)
	}
	total := 0
	nums, payloads := stored(ev)
	for _, part := range payloads {
		total += len(part)
	}
	if len(nums) != 2 || total != 150 {
		t.Errorf("stored batches = %v with %d events, want 2 batches of 150", nums, total)
	}
	if all := ev.all(); !all[len(all)-1].finish {
		t.Error("K3 is not the last request")
	}
}

func TestEmitData(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Flush = time.Hour
	opts.Heartbeat = time.Hour
	p := c.PosterWith(turn(), nil, opts)

	p.Emit(EventTurnStart, nil)                                                    // {}
	p.Emit(EventPlan, "a string")                                                  // dropped
	p.Emit(EventPlan, []int{1, 2})                                                 // dropped
	p.Emit(EventPlan, 42)                                                          // dropped
	p.Emit(EventError, map[string]any{"text": strings.Repeat("x", MaxEventBytes)}) // too large
	m := map[string]any{}
	for i := 0; i < 100; i++ {
		m["i"] = i
		p.Emit(EventTextDelta, m) // marshalled now; the map is reused afterwards
		m["i"] = -1
	}
	if got := p.Dropped(); got != 4 {
		t.Errorf("Dropped() = %d, want 4 (a string, an array, a number and an oversized event)", got)
	}
	if err := p.Finish(context.Background(), "done", "", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	_, payloads := stored(ev)
	if len(payloads) < 1 {
		t.Fatal("nothing was sent")
	}
	var events []Event
	for _, part := range payloads {
		events = append(events, part...)
	}
	if len(events) != 101 {
		t.Fatalf("events sent = %d, want 101 (the nil one and the 100 reused maps)", len(events))
	}
	if data, ok := events[0].Data.(map[string]any); !ok || len(data) != 0 {
		t.Errorf("the nil data was sent as %#v, want {}", events[0].Data)
	}
	for i, e := range events[1:] {
		data, _ := e.Data.(map[string]any)
		if data["i"] != float64(i) {
			t.Fatalf("event %d carries i = %v, want %d (the value at Emit time)", i, data["i"], i)
		}
	}
}

func TestPosterRejected(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		ev.add(r.URL.Path, v)
		if strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"code":"bad_event","message":"no such event"}`)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	var closed atomic.Int32
	var reason atomic.Value
	p := c.PosterWith(turn(), func(r string) {
		closed.Add(1)
		reason.Store(r)
	}, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})

	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return for a refused batch")
	}
	waitFor(t, "onClosed", func() bool { return closed.Load() == 1 })
	if got, _ := reason.Load().(string); got != ClosedRejected {
		t.Errorf("onClosed = %q, want %q", got, ClosedRejected)
	}
	time.Sleep(50 * time.Millisecond)
	if n := closed.Load(); n != 1 {
		t.Errorf("onClosed was called %d times, want 1", n)
	}

	// The turn is over, but its answer still goes out.
	if err := p.Finish(context.Background(), "failed", "", "rejected", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if k3 := ev.k3(); len(k3) != 1 {
		t.Errorf("K3 requests = %d, want 1 after a rejected batch", len(k3))
	}
}

func TestPosterLost(t *testing.T) {
	t.Run("while open", func(t *testing.T) {
		ev := &recorder{}
		b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			ev.add(r.URL.Path, v)
			if strings.HasSuffix(r.URL.Path, "/finish") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		c := newClient(t, b.server.URL, keyFile(t))
		opts := posterOpts()
		opts.MaxRetryWait = 20 * time.Millisecond
		opts.GiveUpAfter = 200 * time.Millisecond
		closed := make(chan string, 2)
		p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)
		p.Emit(EventTurnStart, map[string]any{})

		start := time.Now()
		done := make(chan struct{})
		go func() { defer close(done); p.Run(context.Background()) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Run did not return after a lost batch")
		}
		took := time.Since(start)
		select {
		case got := <-closed:
			if got != ClosedLost {
				t.Errorf("onClosed = %q, want %q", got, ClosedLost)
			}
		case <-time.After(time.Second):
			t.Fatal("onClosed was not called")
		}
		if got := len(ev.k2()); got < 5 {
			t.Errorf("K2 attempts = %d, want more than the old four, through the 200 ms window", got)
		}
		if took < 200*time.Millisecond {
			t.Errorf("the batch was given up after %v, want at least the 200 ms window", took)
		}

		if err := p.Finish(context.Background(), "failed", "", "", "", Usage{}); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if k3 := ev.k3(); len(k3) != 1 {
			t.Errorf("K3 requests = %d, want 1 after a lost turn", len(k3))
		}
	})

	t.Run("while finishing", func(t *testing.T) {
		ev := &recorder{}
		b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			ev.add(r.URL.Path, v)
			if strings.HasSuffix(r.URL.Path, "/finish") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		})
		c := newClient(t, b.server.URL, keyFile(t))
		opts := posterOpts()
		opts.MaxRetryWait = 20 * time.Millisecond
		opts.GiveUpAfter = 200 * time.Millisecond
		var closed atomic.Int32
		p := c.PosterWith(turn(), func(string) { closed.Add(1) }, opts)
		p.Emit(EventTurnStart, map[string]any{})

		start := time.Now()
		if err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{}); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		if got := len(ev.k2()); got < 5 {
			t.Errorf("K2 attempts = %d, want more than the old four, through the 200 ms window", got)
		}
		if took := time.Since(start); took < 200*time.Millisecond {
			t.Errorf("the batch was given up after %v, want at least the 200 ms window", took)
		}
		if k3 := ev.k3(); len(k3) != 1 {
			t.Fatalf("K3 requests = %d, want 1: the answer still goes out", len(k3))
		}
		if got := closed.Load(); got != 0 {
			t.Errorf("onClosed was called %d times, want 0: Finish was called", got)
		}
	})
}

func TestFinishTurnClosedQuiet(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		ev.add(r.URL.Path, v)
		if strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"code":"turn_closed","state":"done"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	var closed atomic.Int32
	p := c.PosterWith(turn(), func(string) { closed.Add(1) }, posterOpts())
	if err := p.Finish(context.Background(), "done", "f", "", "", Usage{}); err != nil {
		t.Errorf("Finish returned %v for a 409 turn_closed, want nil", err)
	}
	if got := closed.Load(); got != 0 {
		t.Errorf("onClosed was called %d times, want 0", got)
	}
}

// TestFinishTurnClosedWhileFinishing: a 409 turn_closed on a K2 sent while finishing ends the finish
// quietly, with no K3.
func TestFinishTurnClosedWhileFinishing(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		ev.add(r.URL.Path, v)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"code":"turn_closed","state":"cancelled"}`)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	var closed atomic.Int32
	p := c.PosterWith(turn(), func(string) { closed.Add(1) }, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})

	if err := p.Finish(context.Background(), "done", "f", "", "", Usage{}); err != nil {
		t.Fatalf("Finish = %v, want nil", err)
	}
	if k3 := ev.k3(); len(k3) != 0 {
		t.Errorf("K3 requests = %d, want none", len(k3))
	}
	if got := closed.Load(); got != 0 {
		t.Errorf("onClosed was called %d times, want 0", got)
	}
}

func TestFinishRetries5xx(t *testing.T) {
	var finishes atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			if finishes.Add(1) == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), nil, posterOpts())
	if err := p.Finish(context.Background(), "done", "f", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if got := finishes.Load(); got != 2 {
		t.Errorf("K3 was sent %d times, want 2 (one 500, one 204)", got)
	}
}

// TestFinishErrorCarriesMessage: a K3 error whose message is empty falls back to its code.
func TestFinishErrorCarriesMessage(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), nil, posterOpts())
	if err := p.Finish(context.Background(), "failed", "", "budget", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	k3 := ev.k3()
	if len(k3) != 1 {
		t.Fatalf("K3 requests = %d, want 1", len(k3))
	}
	errObj, _ := k3[0].body["error"].(map[string]any)
	if errObj["code"] != "budget" || errObj["message"] != "budget" {
		t.Errorf("error = %v, want {code: budget, message: budget}", k3[0].body["error"])
	}
	if k3[0].body["state"] != "failed" {
		t.Errorf("state = %v", k3[0].body["state"])
	}
}

func TestFinishWithError(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), nil, posterOpts())
	if err := p.Finish(context.Background(), "failed", "", "budget", "out of tokens", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	k3 := ev.k3()
	if len(k3) != 1 {
		t.Fatalf("K3 requests = %d, want 1", len(k3))
	}
	errObj, _ := k3[0].body["error"].(map[string]any)
	if errObj["code"] != "budget" || errObj["message"] != "out of tokens" {
		t.Errorf("error = %v", k3[0].body["error"])
	}
}

// TestFinishSanitizesError: K3 refuses a code it does not take, so Finish sends one it does, and
// cuts a long message.
func TestFinishSanitizesError(t *testing.T) {
	cases := []struct{ code, want string }{
		{"Budget", "budget"},
		{"model.rate_limit", "model.rate_limit"},
		{"bad code with spaces", "bot_error"},
		{"9lives", "bot_error"},
		{strings.Repeat("a", 65), "bot_error"},
		{strings.Repeat("a", 64), strings.Repeat("a", 64)},
	}
	for _, tc := range cases {
		ev := &recorder{}
		b := newBotAPI(t, okHandler(ev))
		c := newClient(t, b.server.URL, keyFile(t))
		p := c.PosterWith(turn(), nil, posterOpts())
		msg := strings.Repeat("é", MaxErrorMessage+10)
		if err := p.Finish(context.Background(), "failed", "", tc.code, msg, Usage{}); err != nil {
			t.Fatalf("Finish(%q): %v", tc.code, err)
		}
		k3 := ev.k3()
		if len(k3) != 1 {
			t.Fatalf("K3 requests = %d, want 1", len(k3))
		}
		errObj, _ := k3[0].body["error"].(map[string]any)
		if errObj["code"] != tc.want {
			t.Errorf("code %q sent as %v, want %q", tc.code, errObj["code"], tc.want)
		}
		got, _ := errObj["message"].(string)
		if n := utf8.RuneCountInString(got); n != MaxErrorMessage {
			t.Errorf("message has %d characters, want %d", n, MaxErrorMessage)
		}
	}
}

// TestOnClosedCallsFinish: onClosed runs on Run's goroutine with no lock held, so it may Finish.
func TestOnClosedCallsFinish(t *testing.T) {
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"code":"turn_closed"}`)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	finished := make(chan error, 1)
	var p *Poster
	p = c.PosterWith(turn(), func(string) {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		finished <- p.Finish(ctx, "failed", "", "turn_closed", "", Usage{})
	}, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})

	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return: onClosed calling Finish deadlocked")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("Finish from onClosed = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Finish from onClosed did not return")
	}
}

// TestFinishRespectsCtx: Finish returns when its ctx ends, even with the server hanging.
func TestFinishRespectsCtx(t *testing.T) {
	release := make(chan struct{})
	var hanging atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		hanging.Add(1)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { close(release) })
	c := newClient(t, b.server.URL, keyFile(t))

	p := c.PosterWith(turn(), nil, posterOpts())
	p.Emit(EventTurnStart, map[string]any{})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.Finish(ctx, "done", "f", "", "", Usage{})
	took := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Finish = %v, want context.DeadlineExceeded", err)
	}
	if took > 600*time.Millisecond {
		t.Errorf("Finish took %v, want no more than its 300 ms ctx", took)
	}
	if got := hanging.Load(); got == 0 {
		t.Error("no K2 attempt was made")
	}
}

// TestFinishGetsItsOwnWindow: a K2 whose window runs out while the turn finishes does not spend the
// window of the answer that follows it: K3 rides out a run of failures of its own.
func TestFinishGetsItsOwnWindow(t *testing.T) {
	var k2s, k3s atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			if k3s.Add(1) <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		k2s.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = 200 * time.Millisecond
	p := c.PosterWith(turn(), nil, opts)
	p.Emit(EventTurnStart, map[string]any{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Finish(ctx, "done", "the answer", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v, want nil: the answer gets a failure window of its own", err)
	}
	if got := k2s.Load(); got == 0 {
		t.Error("no K2 attempt was made")
	}
	if got := k3s.Load(); got != 3 {
		t.Errorf("K3 requests = %d, want 3: the two 503s resent, then taken", got)
	}
}

// TestFinishAfterLostGetsItsOwnWindow: a turn the network lost while it was open still posts its
// answer through a run of failures of its own.
func TestFinishAfterLostGetsItsOwnWindow(t *testing.T) {
	var k3s atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			if k3s.Add(1) <= 2 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = 200 * time.Millisecond
	closed := make(chan string, 2)
	p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)
	p.Emit(EventTurnStart, map[string]any{})

	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case got := <-closed:
		if got != ClosedLost {
			t.Fatalf("onClosed = %q, want %q", got, ClosedLost)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("onClosed was not called")
	}
	<-done

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Finish(ctx, "failed", "", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v, want nil: the answer after a lost batch gets a window of its own", err)
	}
	if got := k3s.Load(); got != 3 {
		t.Errorf("K3 requests = %d, want 3: the two 503s resent, then taken", got)
	}
}

// TestFinishRetriesHungAttempt: a long ctx lets each hung attempt end at RequestTimeout and be
// retried until the batch's window runs out.
func TestFinishRetriesHungAttempt(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var attempts atomic.Int32
	var finishes atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			finishes.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		attempts.Add(1)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.RequestTimeout = 50 * time.Millisecond
	opts.MaxRetryWait = 20 * time.Millisecond
	opts.GiveUpAfter = 300 * time.Millisecond
	p := c.PosterWith(turn(), nil, opts)
	p.Emit(EventTurnStart, map[string]any{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := p.Finish(ctx, "done", "f", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	// The window is a time, so the time is what is asserted: a loaded machine gives fewer attempts,
	// not a shorter window.
	if took := time.Since(start); took < 300*time.Millisecond || took > 3*time.Second {
		t.Errorf("Finish took %v, want the whole 300 ms window and no more than 3 s", took)
	}
	if got := attempts.Load(); got < 2 {
		t.Errorf("K2 attempts = %d, want the hung attempt retried", got)
	}
	if got := finishes.Load(); got != 1 {
		t.Errorf("K3 requests = %d, want 1 after the lost batch", got)
	}
}

// TestPosterHeartbeat: an idle Run sends empty batches with increasing numbers, about one
// Heartbeat apart, and a sent batch postpones the next heartbeat.
func TestPosterHeartbeat(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Heartbeat = 100 * time.Millisecond
	p := c.PosterWith(turn(), nil, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	// Nothing is emitted: the only requests are heartbeats.
	waitFor(t, "three heartbeats", func() bool { return len(ev.k2()) >= 3 })
	cancel()
	<-done

	beats := ev.k2()
	nums, payloads := stored(ev)
	for i := range payloads {
		if len(payloads[i]) != 0 {
			t.Errorf("request %d carried %d events, want an empty heartbeat", i, len(payloads[i]))
		}
	}
	for i := 1; i < len(nums); i++ {
		if nums[i] != nums[i-1]+1 {
			t.Errorf("batch numbers = %v, want increasing by one", nums)
		}
	}
	// About one heartbeat apart, and never sooner.
	for i := 1; i < len(beats); i++ {
		if gap := beats[i].at.Sub(beats[i-1].at); gap < 80*time.Millisecond {
			t.Errorf("heartbeat %d came %v after the last, want about 100 ms", i, gap)
		}
	}
	for _, r := range b.requests() {
		var v map[string]any
		_ = json.Unmarshal(r.raw, &v)
		if _, ok := v["events"].([]any); !ok {
			t.Errorf("the heartbeat has no events array: %s", r.raw)
		}
	}
}

// TestPosterHeartbeatPostponed: a batch that goes out restarts the heartbeat clock.
func TestPosterHeartbeatPostponed(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Heartbeat = 100 * time.Millisecond
	opts.Flush = 20 * time.Millisecond
	p := c.PosterWith(turn(), nil, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()

	time.Sleep(30 * time.Millisecond)
	p.Emit(EventTextDelta, map[string]any{"i": 1})
	waitFor(t, "the event batch", func() bool { return len(ev.k2()) >= 1 })
	sentAt := time.Now()
	waitFor(t, "a heartbeat after it", func() bool { return len(ev.k2()) >= 2 })
	beat := ev.k2()[1]
	cancel()
	<-done
	if gap := beat.at.Sub(sentAt); gap < 70*time.Millisecond {
		t.Errorf("the heartbeat came %v after the batch, want about 100 ms", gap)
	}
}

// TestPosterBufferCap: with MaxBuffered reached, further events are dropped and counted.
func TestPosterBufferCap(t *testing.T) {
	release := make(chan struct{})
	var hanging atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		hanging.Add(1)
		<-release
		w.WriteHeader(http.StatusNoContent)
	})
	t.Cleanup(func() { close(release) })
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.MaxBuffered = 10 << 10
	opts.RequestTimeout = time.Second
	p := c.PosterWith(turn(), nil, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	waitFor(t, "the first hung attempt", func() bool { return hanging.Load() >= 1 })

	// 1 KiB of data per event, well over the 10 KiB the buffer may hold.
	chunk := strings.Repeat("x", 1000)
	for i := 0; i < 20; i++ {
		p.Emit(EventTextDelta, map[string]any{"i": i, "text": chunk})
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	if p.Dropped() == 0 {
		t.Error("Dropped() = 0 with 20 KiB emitted into a 10 KiB buffer")
	}
	if p.dropped > 20 {
		t.Errorf("dropped = %d, want fewer than the 20 emitted", p.dropped)
	}
}

func TestEmitDropsUnknownType(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Flush = time.Hour
	p := c.PosterWith(turn(), nil, opts)
	p.Emit("made_up_event", map[string]any{"x": 1})
	p.Emit(EventError, map[string]any{"message": "real"})
	if err := p.Finish(context.Background(), "done", "f", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	_, payloads := stored(ev)
	if len(payloads) != 1 || len(payloads[0]) != 1 {
		t.Fatalf("batches = %v, want one K2 with the one allowed event", payloads)
	}
	raw, _ := json.Marshal(payloads[0])
	if strings.Contains(string(raw), "made_up_event") {
		t.Errorf("the dropped event was sent: %s", raw)
	}
	if !strings.Contains(string(raw), EventError) {
		t.Errorf("the real event is missing: %s", raw)
	}
	if got := p.Dropped(); got != 1 {
		t.Errorf("Dropped() = %d, want 1", got)
	}
	if len(b.requests()) != 2 {
		t.Errorf("requests = %d, want K2 then K3", len(b.requests()))
	}
}

// dropConnection closes the connection under a request: the client sees a transport error, with no
// status at all.
func dropConnection(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
			return
		}
	}
	w.WriteHeader(http.StatusServiceUnavailable)
}

// outageOpts is a poster that rides out a short outage quickly.
func outageOpts() PosterOptions {
	opts := posterOpts()
	opts.RetryBase = 5 * time.Millisecond
	opts.MaxRetryWait = 20 * time.Millisecond
	opts.Heartbeat = time.Hour
	return opts
}

// TestPosterSurvivesOutage: a batch that fails with dropped connections and 503s for longer than the
// old four attempts, then succeeds, keeps the turn open and is stored once, in order, with every
// event.
func TestPosterSurvivesOutage(t *testing.T) {
	var attempts atomic.Int32
	ev := &recorder{}
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		if strings.HasSuffix(r.URL.Path, "/finish") {
			ev.add(r.URL.Path, v)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if k := attempts.Add(1); k <= 12 {
			// Twelve failures: three times the old limit, and still far inside the window.
			if k%2 == 0 {
				dropConnection(w)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ev.add(r.URL.Path, v)
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = 2 * time.Second
	closed := make(chan string, 2)
	p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)
	p.Emit(EventTurnStart, map[string]any{"i": 0})
	p.Emit(EventTextDelta, map[string]any{"i": 1})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	waitFor(t, "the batch through the outage", func() bool { return len(ev.k2()) >= 1 })
	p.Emit(EventMemberDone, map[string]any{"i": 2})
	waitFor(t, "the second batch", func() bool { return len(ev.k2()) >= 2 })
	cancel()
	<-done

	if err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	select {
	case reason := <-closed:
		t.Fatalf("onClosed(%q) during a twelve-attempt outage", reason)
	default:
	}
	nums, payloads := stored(ev)
	if len(nums) != 2 || nums[0] != 1 || nums[1] != 2 {
		t.Fatalf("stored batches = %v, want 1 then 2", nums)
	}
	var events []Event
	for _, part := range payloads {
		events = append(events, part...)
	}
	if len(events) != 3 {
		t.Fatalf("events stored = %d, want the 3 emitted", len(events))
	}
	for i, e := range events {
		data, _ := e.Data.(map[string]any)
		if data["i"] != float64(i) {
			t.Errorf("event %d carries i = %v, want %d: in order, none lost", i, data["i"], i)
		}
	}
	if k3 := ev.k3(); len(k3) != 1 {
		t.Errorf("K3 requests = %d, want 1", len(k3))
	}
}

// TestPosterGivesUpAfterTheWindow: a run of failures that lasts GiveUpAfter loses the turn, at the
// window's end and not at a count of attempts.
func TestPosterGivesUpAfterTheWindow(t *testing.T) {
	var attempts atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = 300 * time.Millisecond
	closed := make(chan string, 2)
	p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)
	p.Emit(EventTurnStart, map[string]any{})

	start := time.Now()
	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return: the window never ran out")
	}
	if got := <-closed; got != ClosedLost {
		t.Errorf("onClosed = %q, want %q", got, ClosedLost)
	}
	if took := time.Since(start); took < 300*time.Millisecond {
		t.Errorf("the turn was lost after %v, want at least the 300 ms window", took)
	}
	if got := attempts.Load(); got <= 4 {
		t.Errorf("K2 attempts = %d, want more than the old four", got)
	}
}

// TestPosterNewStreakAfterSuccess: a 2xx ends the run of failures, so the next one gets a window of
// its own.
func TestPosterNewStreakAfterSuccess(t *testing.T) {
	var failing atomic.Bool
	ev := &recorder{}
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		if strings.HasSuffix(r.URL.Path, "/finish") {
			ev.add(r.URL.Path, v)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		ev.add(r.URL.Path, v)
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.MaxRetryWait = 10 * time.Millisecond
	// Each run below lasts two thirds of the window, so neither can run out of it by itself, and
	// the two together always outlast one window counted from the first run: load can only make the
	// runs longer, which still proves the second window is a new one.
	opts.GiveUpAfter = 600 * time.Millisecond
	closed := make(chan string, 2)
	p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()

	failing.Store(true)
	p.Emit(EventTurnStart, map[string]any{"i": 0})
	time.Sleep(400 * time.Millisecond)
	failing.Store(false)
	waitFor(t, "the first batch", func() bool { return len(ev.k2()) >= 1 })

	// A second run of the same length: counted from the first run's first failure, the two outlast
	// the window; only a window of its own keeps the second batch.
	failing.Store(true)
	p.Emit(EventTextDelta, map[string]any{"i": 1})
	time.Sleep(400 * time.Millisecond)
	failing.Store(false)
	waitFor(t, "the second batch", func() bool { return len(ev.k2()) >= 2 })
	cancel()
	<-done

	if err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	select {
	case reason := <-closed:
		t.Fatalf("onClosed(%q): the second run of failures did not get its own window", reason)
	default:
	}
	nums, payloads := stored(ev)
	if len(nums) != 2 || nums[0] != 1 || nums[1] != 2 {
		t.Fatalf("stored batches = %v, want 1 then 2", nums)
	}
	if len(payloads[0]) != 1 || len(payloads[1]) != 1 {
		t.Errorf("event counts per batch = %d and %d, want 1 and 1", len(payloads[0]), len(payloads[1]))
	}
}

// TestPosterDeadlineGivesUpAtOnce: a failure at or after the poster's Deadline is not resent.
func TestPosterDeadlineGivesUpAtOnce(t *testing.T) {
	var attempts atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		attempts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = time.Minute // the deadline is what gives up here
	opts.Deadline = time.Now().Add(-time.Second)
	closed := make(chan string, 2)
	p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)
	p.Emit(EventTurnStart, map[string]any{})
	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return: a deadline in the past did not give up")
	}
	if got := <-closed; got != ClosedLost {
		t.Errorf("onClosed = %q, want %q", got, ClosedLost)
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("K2 attempts = %d, want 1: a failure past the deadline is not resent", got)
	}
}

// TestPosterPastDeadlineStillDelivers: the deadline bounds the resends, not the requests: a batch
// and a finish that the server takes go out even with the deadline already past.
func TestPosterPastDeadlineStillDelivers(t *testing.T) {
	ev := &recorder{}
	b := newBotAPI(t, okHandler(ev))
	c := newClient(t, b.server.URL, keyFile(t))
	opts := posterOpts()
	opts.Deadline = time.Now().Add(-time.Second)
	closed := make(chan string, 2)
	p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)
	p.Emit(EventTurnStart, map[string]any{"i": 0})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	waitFor(t, "the first batch", func() bool { return len(ev.k2()) >= 1 })
	cancel()
	<-done

	if err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	select {
	case reason := <-closed:
		t.Fatalf("onClosed(%q): a past deadline must not stop a request the server takes", reason)
	default:
	}
	if k3 := ev.k3(); len(k3) != 1 {
		t.Errorf("K3 requests = %d, want 1", len(k3))
	}
	_, payloads := stored(ev)
	if len(payloads) != 1 || len(payloads[0]) != 1 {
		t.Fatalf("stored batches = %d, want one with the event: %v", len(payloads), payloads)
	}
}

// TestPosterResendNotPastDeadline: a failure whose resend would land at or after Deadline gives up
// at once, however long the server's Retry-After asks to wait, and nothing goes out past it.
func TestPosterResendNotPastDeadline(t *testing.T) {
	var mu sync.Mutex
	var at []time.Time
	deadline := time.Now().Add(50 * time.Millisecond)
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = time.Minute // the deadline is what gives up here
	opts.Deadline = deadline
	closed := make(chan string, 2)
	p := c.PosterWith(turn(), func(r string) { closed <- r }, opts)
	p.Emit(EventTurnStart, map[string]any{})

	start := time.Now()
	done := make(chan struct{})
	go func() { defer close(done); p.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return: a resend would have gone out past the deadline")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("the turn was lost after %v, want at once: the resend would pass the deadline", took)
	}
	if got := <-closed; got != ClosedLost {
		t.Errorf("onClosed = %q, want %q", got, ClosedLost)
	}
	k2 := 0
	for _, r := range b.requests() {
		if strings.HasSuffix(r.path, "/events") {
			k2++
		}
	}
	if k2 != 1 {
		t.Errorf("K2 requests = %d, want 1: no resend past the deadline", k2)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ts := range at {
		if !ts.Before(deadline) {
			t.Errorf("a request went out at %v, at or after the deadline %v", ts, deadline)
		}
	}
}

// TestFinishResendNotPastDeadline: the same for K3: a failure whose resend would land at or after
// Deadline ends the finish with that error, after one request.
func TestFinishResendNotPastDeadline(t *testing.T) {
	var finishes atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			finishes.Add(1)
		}
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = time.Minute // the deadline is what gives up here
	opts.Deadline = time.Now().Add(50 * time.Millisecond)
	p := c.PosterWith(turn(), nil, opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := p.Finish(ctx, "done", "the answer", "", "", Usage{})
	if got := StatusOf(err); got != http.StatusServiceUnavailable {
		t.Fatalf("Finish = %v, want the 503", err)
	}
	if got := finishes.Load(); got != 1 {
		t.Errorf("K3 requests = %d, want 1: no resend past the deadline", got)
	}
}

// TestRetryWait: the wait doubles from RetryBase to MaxRetryWait and is capped, not wrapped, however
// large the base or the attempt count.
func TestRetryWait(t *testing.T) {
	t.Run("a huge base is capped", func(t *testing.T) {
		// 1<<35+1 is past MaxRetryWait at every shift, and its 29th shift wraps to a small positive
		// value: the doubling must be capped, not wrapped.
		for _, base := range []time.Duration{time.Hour, (1 << 35) + 1} {
			p := &Poster{retryBase: base, maxRetry: DefaultMaxRetryWait}
			for attempts := 1; attempts <= 1000; attempts++ {
				if got := p.retryWait(attempts, 0); got != DefaultMaxRetryWait {
					t.Fatalf("base %s, attempt %d waits %s, want %s", base, attempts, got, DefaultMaxRetryWait)
				}
			}
		}
	})
	t.Run("doubles to the cap", func(t *testing.T) {
		p := &Poster{retryBase: time.Second, maxRetry: 5 * time.Second}
		want := []time.Duration{
			time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second,
		}
		for i, w := range want {
			if got := p.retryWait(i+1, 0); got != w {
				t.Errorf("attempt %d waits %s, want %s", i+1, got, w)
			}
		}
	})
}

// TestPosterRetryAfterBeatsMaxWait: the server's Retry-After is honoured even past MaxRetryWait.
func TestPosterRetryAfterBeatsMaxWait(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	opts := outageOpts()
	opts.GiveUpAfter = time.Minute
	p := c.PosterWith(turn(), nil, opts)
	p.Emit(EventTurnStart, map[string]any{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.Finish(ctx, "done", "the answer", "", "", Usage{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(times) < 2 {
		t.Fatalf("requests = %d, want the batch resent", len(times))
	}
	if gap := times[1].Sub(times[0]); gap < 1900*time.Millisecond {
		t.Errorf("resent after %v, want the server's Retry-After of 2 s, not MaxRetryWait", gap)
	}
}

// TestFinishSurvivesOutage: K3 rides out a run of transient failures too, and the answer reaches the
// platform once.
func TestFinishSurvivesOutage(t *testing.T) {
	t.Run("transient then ok", func(t *testing.T) {
		var finishes atomic.Int32
		ev := &recorder{}
		b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			if !strings.HasSuffix(r.URL.Path, "/finish") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if finishes.Add(1) <= 8 {
				if finishes.Load()%2 == 0 {
					dropConnection(w)
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			ev.add(r.URL.Path, v)
			w.WriteHeader(http.StatusNoContent)
		})
		c := newClient(t, b.server.URL, keyFile(t))
		opts := outageOpts()
		opts.GiveUpAfter = 2 * time.Second
		p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, opts)
		if err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{}); err != nil {
			t.Fatalf("Finish = %v, want nil through eight failed attempts", err)
		}
		if got := ev.k3(); len(got) != 1 {
			t.Errorf("stored finishes = %d, want 1", len(got))
		}
		if got := finishes.Load(); got <= 4 {
			t.Errorf("K3 attempts = %d, want more than the old four", got)
		}
	})

	t.Run("past the window", func(t *testing.T) {
		var finishes atomic.Int32
		b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
			if strings.HasSuffix(r.URL.Path, "/finish") {
				finishes.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		c := newClient(t, b.server.URL, keyFile(t))
		opts := outageOpts()
		opts.GiveUpAfter = 300 * time.Millisecond
		p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, opts)
		start := time.Now()
		err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{})
		if err == nil {
			t.Fatal("Finish = nil, want the last error once the window ran out")
		}
		if got := StatusOf(err); got != http.StatusServiceUnavailable {
			t.Errorf("Finish = %v, want the last 503", err)
		}
		if took := time.Since(start); took < 300*time.Millisecond {
			t.Errorf("Finish gave up after %v, want at least the 300 ms window", took)
		}
	})

	t.Run("turn_closed after a resend", func(t *testing.T) {
		var finishes atomic.Int32
		b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
			if !strings.HasSuffix(r.URL.Path, "/finish") {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			switch finishes.Add(1) {
			case 1:
				w.WriteHeader(http.StatusServiceUnavailable)
			default:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = fmt.Fprint(w, `{"code":"turn_closed","state":"done"}`)
			}
		})
		c := newClient(t, b.server.URL, keyFile(t))
		p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, outageOpts())
		if err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{}); err != nil {
			t.Errorf("Finish = %v for a 409 turn_closed after a resend, want nil", err)
		}
		if got := finishes.Load(); got != 2 {
			t.Errorf("K3 requests = %d, want 2", got)
		}
	})
}

// TestFinishRejectedAtOnce: a K3 the server refuses (a 4xx other than 404, 409 and 429) ends at once.
func TestFinishRejectedAtOnce(t *testing.T) {
	var finishes atomic.Int32
	b := newBotAPI(t, func(w http.ResponseWriter, r *http.Request, n int, body []byte) {
		if !strings.HasSuffix(r.URL.Path, "/finish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		finishes.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"code":"bad_finish"}`)
	})
	c := newClient(t, b.server.URL, keyFile(t))
	p := c.PosterWith(turn(), func(string) { t.Error("onClosed was called") }, outageOpts())
	err := p.Finish(context.Background(), "done", "the answer", "", "", Usage{})
	if got := StatusOf(err); got != http.StatusBadRequest {
		t.Fatalf("Finish = %v, want the 400 itself", err)
	}
	if got := finishes.Load(); got != 1 {
		t.Errorf("K3 requests = %d, want 1: a refused finish is not resent", got)
	}
}

// TestErrorClass: the word the poll log gives a failure.
func TestErrorClass(t *testing.T) {
	wrap := func(err error) error { return &url.Error{Op: "Get", URL: "https://tb-api.teknoir.cloud/api", Err: err} }
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"key", fmt.Errorf("read the key: %w", botapi.ErrKey), "key"},
		{"dns", wrap(&net.DNSError{Err: "no such host", Name: "tb-api.teknoir.cloud", IsNotFound: true}), "dns"},
		{"tls verify", wrap(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), "tls"},
		{"tls record", wrap(tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}), "tls"},
		{"timeout", wrap(context.DeadlineExceeded), "timeout"},
		{"refused", wrap(&net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}), "refused"},
		{"plain", wrap(errors.New("something else")), "transport"},
		{"http", &Error{Status: http.StatusServiceUnavailable}, "http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ErrorClass(tc.err); got != tc.want {
				t.Errorf("ErrorClass(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
	if got := ErrorClass(nil); got != "" {
		t.Errorf("ErrorClass(nil) = %q, want \"\"", got)
	}
}

func TestBackoff(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		got := Backoff(&Error{Status: status}, 1, rnd)
		if got != 60*time.Second {
			t.Errorf("Backoff(%d) = %v, want 60s", status, got)
		}
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i, w := range want {
		base := w * time.Second
		lo, hi := time.Duration(float64(base)*0.8), time.Duration(float64(base)*1.2)
		for k := 0; k < 20; k++ { // the jitter differs each time
			got := Backoff(fmt.Errorf("network"), i+1, rnd)
			if got < lo || got > hi {
				t.Fatalf("Backoff(attempt %d) = %v, want %v ±20%% (between %v and %v)", i+1, got, base, lo, hi)
			}
		}
	}
	// 401 and 403 are the key, everything else (including an unknown error) backs off normally.
	if got := Backoff(&Error{Status: http.StatusNotFound}, 1, rnd); got >= 60*time.Second {
		t.Errorf("Backoff(404) = %v, want the exponential path", got)
	}
}
