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

// reserveTokens is what the phase calls leave for the answer call.
const reserveTokens = 20_000

// budget is the spending of one turn. Members run in parallel, so every method locks.
type budget struct {
	mu    sync.Mutex
	c     caps
	calls int
	spent int64
	use   model.Usage
	onces map[string]bool
}

// newBudget returns the budget of a turn with the caps c.
func newBudget(c caps) *budget {
	return &budget{c: c}
}

// begin asks for one orchestrator model call. A phase call (answer false) is allowed while
// calls < c.calls-1 and tokens < c.tokens-reserveTokens; the answer call (answer true) while
// calls < c.calls (tokens are not checked for it). An allowed call is counted at once. When it is
// refused, what is "calls" or "tokens" (the first that refused).
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
		if b.spent >= b.c.tokens-reserveTokens {
			return false, "tokens"
		}
	}
	b.calls++
	return true, ""
}

// add adds one model call's usage: counted tokens += Input + CacheWrite + Output, and the usage sum
// (all five fields) += u.
func (b *budget) add(u model.Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spendLocked(u)
}

// step adds one coder step as add does and reports whether coder work may go on: tokens <
// c.tokens-reserveTokens after adding.
func (b *budget) step(u model.Usage) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.spendLocked(u)
	return b.spent < b.c.tokens-reserveTokens
}

// spendLocked adds one call's usage to the turn's.
func (b *budget) spendLocked(u model.Usage) {
	b.spent += u.Input + u.CacheWrite + u.Output
	b.use.Add(u)
}

// upgrade switches to c (an auto turn that convenes); spent calls and tokens stay.
func (b *budget) upgrade(c caps) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.c = c
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
