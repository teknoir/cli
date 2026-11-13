package council

import (
	"sync"
	"testing"

	"github.com/teknoir/cli/pkg/council/model"
)

func TestBudgetPhaseLeavesOneAnswer(t *testing.T) {
	b := newBudget(capsQuick)
	for i := 0; i < 7; i++ {
		if ok, what := b.begin(false); !ok {
			t.Fatalf("phase call %d refused with %q", i+1, what)
		}
	}
	if ok, what := b.begin(false); ok || what != "calls" {
		t.Fatalf("the 8th phase call = %v, %q, want it refused with calls", ok, what)
	}
	if ok, what := b.begin(true); !ok {
		t.Fatalf("the answer call refused with %q", what)
	}
	if ok, what := b.begin(true); ok || what != "calls" {
		t.Fatalf("the second answer call = %v, %q, want it refused with calls", ok, what)
	}
}

func TestBudgetTokenLine(t *testing.T) {
	b := newBudget(capsQuick)
	b.add(model.Usage{Calls: 1, Input: 100_000, CacheWrite: 20_000, Output: 10_001})
	if got := b.tokens(); got != 130_001 {
		t.Fatalf("tokens = %d, want 130001", got)
	}
	if ok, what := b.begin(false); ok || what != "tokens" {
		t.Fatalf("the phase call past the token line = %v, %q, want it refused with tokens", ok, what)
	}
	// The answer call does not check the tokens: the 20 000 reserve is its own.
	if ok, what := b.begin(true); !ok {
		t.Fatalf("the answer call refused with %q", what)
	}
}

func TestBudgetStep(t *testing.T) {
	b := newBudget(capsMember)
	if !b.step(model.Usage{Input: 100_000}) {
		t.Fatal("a step below the line refused")
	}
	if b.step(model.Usage{Input: 80_001}) {
		t.Fatal("a step past the line was accepted")
	}
	// 180 001 < 200 000-20 000 is false: the budget is done.
	if got := b.tokens(); got != 180_001 {
		t.Errorf("tokens = %d, want 180001", got)
	}
}

func TestBudgetUpgradeKeepsCounts(t *testing.T) {
	b := newBudget(capsQuick)
	for i := 0; i < 7; i++ {
		b.begin(false)
	}
	b.add(model.Usage{Calls: 1, Input: 1000})
	b.upgrade(capsCouncil)
	if got := b.limits(); got != capsCouncil {
		t.Errorf("limits = %+v, want capsCouncil", got)
	}
	if got := b.tokens(); got != 1000 {
		t.Errorf("tokens = %d, want the spent 1000 kept", got)
	}
	if ok, what := b.begin(false); !ok {
		t.Fatalf("a phase call after the upgrade refused with %q", what)
	}
	if got := b.usage(); got != (model.Usage{Calls: 1, Input: 1000}) {
		t.Errorf("usage = %+v", got)
	}
}

// limitTokens lowers the token cap and keeps the calls; a limit at or above the cap changes nothing,
// and neither does a limit of 0 or less.
func TestBudgetLimitTokens(t *testing.T) {
	b := newBudget(capsCouncil)
	b.limitTokens(100_000)
	if got := b.limits(); got != (caps{calls: 30, tokens: 100_000}) {
		t.Errorf("limits = %+v, want 100000 tokens and 30 calls", got)
	}
	b.add(model.Usage{Input: 80_000})
	if ok, what := b.begin(false); ok || what != "tokens" {
		t.Errorf("a phase call past the lowered cap = %v, %q, want it refused with tokens", ok, what)
	}

	for _, limit := range []int64{0, -1, 500_000, 600_000} {
		b := newBudget(capsQuick)
		b.limitTokens(limit)
		if got := b.limits(); got != capsQuick {
			t.Errorf("limitTokens(%d) changed capsQuick to %+v", limit, got)
		}
	}
}

// upgrade keeps the council's own cap: it lowers the turn's new caps when they are above it, and
// never raises a cap the council did not set.
func TestBudgetUpgradeKeepsRosterCap(t *testing.T) {
	b := newBudget(capsQuick)
	b.limitTokens(60_000)
	b.upgrade(capsCouncil)
	if got := b.limits(); got != (caps{calls: 30, tokens: 60_000}) {
		t.Errorf("limits = %+v, want 60000 tokens and 30 calls", got)
	}

	b = newBudget(capsQuick)
	b.limitTokens(300_000)
	b.upgrade(capsCouncil)
	if got := b.limits().tokens; got != 300_000 {
		t.Errorf("tokens = %d, want the council's 300000", got)
	}

	// A council cap above capsQuick's does not raise it before the upgrade.
	b = newBudget(capsQuick)
	b.limitTokens(200_000)
	if got := b.limits().tokens; got != capsQuick.tokens {
		t.Errorf("tokens before the upgrade = %d, want capsQuick's %d", got, capsQuick.tokens)
	}
	b.upgrade(capsCouncil)
	if got := b.limits().tokens; got != 200_000 {
		t.Errorf("tokens after the upgrade = %d, want the council's 200000", got)
	}
}

// The answer reserve is a quarter of the cap when that is less than 20 000.
func TestBudgetReserveIsAQuarterOfASmallCap(t *testing.T) {
	b := newBudget(caps{calls: 8, tokens: 10_000})
	if ok, what := b.begin(false); !ok {
		t.Fatalf("the first phase call refused with %q", what)
	}
	b.add(model.Usage{Input: 7_500})
	if ok, what := b.begin(false); ok || what != "tokens" {
		t.Errorf("a phase call at 7500 of 10000 = %v, %q, want it refused with tokens", ok, what)
	}

	b = newBudget(caps{calls: 8, tokens: 10_000})
	if !b.step(model.Usage{Input: 7_499}) {
		t.Error("a step below the quarter-of-the-cap reserve was refused")
	}
	if b.step(model.Usage{Input: 1}) {
		t.Error("a step past the quarter-of-the-cap reserve was accepted")
	}

	// The Standard caps keep the 20 000 reserve.
	b = newBudget(capsQuick)
	b.add(model.Usage{Input: 129_999})
	if ok, what := b.begin(false); !ok {
		t.Fatalf("a phase call at 129999 of 150000 refused with %q", what)
	}
	b = newBudget(capsQuick)
	b.add(model.Usage{Input: 130_000})
	if ok, what := b.begin(false); ok || what != "tokens" {
		t.Errorf("a phase call at 130000 of 150000 = %v, %q, want it refused with tokens", ok, what)
	}
}

func TestBudgetParallelBegin(t *testing.T) {
	b := newBudget(capsCouncil)
	const n = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, _ := b.begin(false)
			if ok {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if allowed != capsCouncil.calls-1 {
		t.Errorf("%d of %d phase calls were allowed, want %d", allowed, n, capsCouncil.calls-1)
	}
}

func TestBudgetOnce(t *testing.T) {
	b := newBudget(capsQuick)
	if !b.once("calls") {
		t.Error("the first once(calls) was false")
	}
	if b.once("calls") {
		t.Error("the second once(calls) was true")
	}
	if !b.once("tokens") {
		t.Error("the first once(tokens) was false: each kind is separate")
	}
}
