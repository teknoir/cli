package council

import (
	"sync"

	"github.com/teknoir/cli/pkg/council/model"
)

// caps is what one turn may spend: orchestrator model calls and counted tokens.
type caps struct {
	calls  int
	tokens int64
}

// The caps of a turn. A quick turn is one answer; a council turn convenes its members first. An auto
// turn starts quick and is upgraded when it convenes.
var (
	capsQuick   = caps{calls: 8, tokens: 150_000}
	capsCouncil = caps{calls: 30, tokens: 500_000}
	capsMember  = caps{calls: 12, tokens: 200_000}
)

// reserveTokens is the most the phase calls leave for the answer call: 20 000, or a quarter of the
// cap when that is less.
const reserveTokens = 20_000

// budget is the spending of one turn. Members run in parallel, so every method locks.
type budget struct {
	mu     sync.Mutex
	c      caps
	roster int64 // the council's own token cap (snap.Budget.MaxTokens); 0 = none
	calls  int
	spent  int64
	use    model.Usage
	onces  map[string]bool
}

// newBudget returns the budget of a turn with the caps c.
func newBudget(c caps) *budget {
	return &budget{c: c}
}

// begin asks for one orchestrator model call. A phase call (answer false) is allowed while
// calls < c.calls-1 and tokens < c.tokens minus the reserve (20 000, or a quarter of the cap when
// that is less); the answer call (answer true) while calls < c.calls (tokens are not checked for
// it). An allowed call is counted at once. When it is refused, what is "calls" or "tokens" (the
// first that refused).
func (b *budget) begin(answer bool) (ok bool, what string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if answer {
		if b.calls >= b.c.calls {
			return false, "calls"
		}
	} else {
		if b.calls >= b.c.calls-1 {
			return false, "calls"
		}
		if b.spent >= b.c.tokens-b.reserveLocked() {
			return false, "tokens"
		}
	}
	b.calls++
	return true, ""
}

// reserveLocked is what the phase calls leave for the answer call: 20 000 tokens, or a quarter of the
// cap when that is less, so a small council budget still leaves its route calls room (spec 7.4).
func (b *budget) reserveLocked() int64 {
	return min(int64(reserveTokens), b.c.tokens/4)
}

// add adds one model call's usage: counted tokens += Input + CacheWrite + Output, and the usage sum
// (all five fields) += u.
func (b *budget) add(u model.Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spendLocked(u)
}

// step adds one coder step as add does and reports whether coder work may go on: tokens below the cap
// minus the reserve after adding (20 000, or a quarter of the cap when that is less).
func (b *budget) step(u model.Usage) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spendLocked(u)
	return b.spent < b.c.tokens-b.reserveLocked()
}

// spendLocked adds one call's usage to the turn's.
func (b *budget) spendLocked(u model.Usage) {
	b.spent += u.Input + u.CacheWrite + u.Output
	b.use.Add(u)
}

// upgrade switches to c (an auto turn that convenes); spent calls and tokens stay, and the council's
// own token cap, when it has one, stays on the new caps too (spec 7.4: it lowers the turn's cap,
// never raises it).
func (b *budget) upgrade(c caps) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.c = c
	if b.roster > 0 && b.c.tokens > b.roster {
		b.c.tokens = b.roster
	}
}

// limitTokens lowers the token cap to limit, now and after every upgrade (spec 7.4: a council's own
// budget lowers the turn's cap, never raises it). limit <= 0 does nothing.
func (b *budget) limitTokens(limit int64) {
	if limit <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.roster = limit
	if b.c.tokens > limit {
		b.c.tokens = limit
	}
}

// limits is the turn's caps.
func (b *budget) limits() caps {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.c
}

// usage is the sum of every add and step.
func (b *budget) usage() model.Usage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.use
}

// tokens is the counted tokens of the turn so far.
func (b *budget) tokens() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.spent
}

// once reports true the first time it is called with what, false after: one budget event per kind
// ("calls", "tokens", "time") per turn.
func (b *budget) once(what string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.onces == nil {
		b.onces = make(map[string]bool, 3)
	}
	if b.onces[what] {
		return false
	}
	b.onces[what] = true
	return true
}
