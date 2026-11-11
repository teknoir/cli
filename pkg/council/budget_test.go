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
