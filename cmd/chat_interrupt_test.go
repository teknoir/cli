package cmd

import (
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// chatSigs replaces the chat's Ctrl-C channel with one the test sends on.
func chatSigs(t *testing.T) chan os.Signal {
	t.Helper()
	sigs := make(chan os.Signal, 4)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })
	return sigs
}

// TestChatCtrlCBeforeTheTurn: a Ctrl-C during a request before the turn (the council's change P9, the
// thread's read P4, the new thread P3) asks nothing: no turn is posted, so none is cancelled, and the
// command ends with exit 1.
func TestChatCtrlCBeforeTheTurn(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		thread string
		setup  func(p *chatAPI, sigs chan os.Signal)
		stderr []string // lines stderr has
	}{
		{"during the council's change", []string{"--thread", "thr_old", "--roster", "review"}, "thr_old",
			func(p *chatAPI, sigs chan os.Signal) {
				chatOldThread(p, chatThreadDetail("thr_old", 42))
				p.onFunc("PATCH", "/bots/scout/chat/threads/thr_old", func(rec) (int, string) {
					sigs <- os.Interrupt // the person presses Ctrl-C while the council changes
					return 200, chatThreadOnRoster("thr_old", "review", "Review")
				})
			},
			[]string{"· this chat's council is now Review (review)\n"}},
		{"during the thread's read", []string{"--thread", "thr_old"}, "thr_old",
			func(p *chatAPI, sigs chan os.Signal) {
				chatOldThread(p, chatThreadDetail("thr_old", 42))
				p.onFunc("GET", "/bots/scout/chat/threads/thr_old", func(rec) (int, string) {
					sigs <- os.Interrupt
					return 200, chatThreadDetail("thr_old", 42)
				})
			},
			nil},
		{"during the new thread", nil, "thr_new",
			func(p *chatAPI, sigs chan os.Signal) {
				chatNewThread(p)
				p.onFunc("POST", "/bots/scout/chat/threads", func(rec) (int, string) {
					sigs <- os.Interrupt
					return 201, chatThreadNew
				})
			},
			// the thread was made: its id is said, so the person can continue it
			[]string{"thread thr_new\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			sigs := chatSigs(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("POST", "/bots/scout/chat/threads/"+c.thread+"/turns/trn_1/cancel", 202, chatTurnBody)
			c.setup(p, sigs)
			out, errs, err := runCLI(t, p.env(t), append(append([]string{"chat", "scout"}, c.args...), "hello bot")...)
			if err == nil || exitCodeOf(err) != codeError || err.Error() != "interrupted: nothing was asked" {
				t.Fatalf("exit %d: %v, want exit 1 interrupted: nothing was asked\n%s", exitCodeOf(err), err, errs)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads/"+c.thread+"/turns")); n != 0 {
				t.Errorf("%d turns posted, want none", n)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads/"+c.thread+"/turns/trn_1/cancel")); n != 0 {
				t.Errorf("%d cancels, want none", n)
			}
			if strings.Contains(errs, "cancelling") {
				t.Errorf("stderr %q: a turn was cancelled", errs)
			}
			if out != "" {
				t.Errorf("stdout %q, want nothing", out)
			}
			for _, l := range c.stderr {
				if !strings.Contains(errs, l) {
					t.Errorf("stderr %q lacks %q", errs, l)
				}
			}
		})
	}
}

// TestChatREPLCtrlCBeforeTheTurn: in the conversation, a Ctrl-C during a line's requests before its turn
// (the new thread P3, the line's read P4) asks nothing: it says so, and the conversation goes on to the next
// prompt; the next line asks its turn on the same thread.
func TestChatREPLCtrlCBeforeTheTurn(t *testing.T) {
	t.Run("during the new thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		sigs := chatSigs(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatNewThread(p)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 202, chatTurnBody)
		var once sync.Once
		p.onFunc("POST", "/bots/scout/chat/threads", func(rec) (int, string) {
			once.Do(func() { sigs <- os.Interrupt })
			return 201, chatThreadNew
		})
		out, errs, err := chatREPLRun(t, p, "hello\nsecond\n/quit\n")
		chatREPLExit(t, err, errs)
		if !strings.Contains(errs, "thread thr_new\n· interrupted: nothing was asked\n") {
			t.Errorf("stderr %q: want the thread, then the interrupted line", errs)
		}
		if strings.Contains(errs, "cancelling") || strings.Contains(errs, "Error:") {
			t.Errorf("stderr %q", errs)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
			t.Errorf("%d new threads, want 1: the second line asks on the thread the first one made", n)
		}
		want := []map[string]any{{"prompt": "second"}}
		if got := chatTurnMaps(t, p, "thr_new"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel")); n != 0 {
			t.Errorf("%d cancels, want none", n)
		}
		if n := strings.Count(out, "> "); n != 3 {
			t.Errorf("the prompt was printed %d times, want 3: %q", n, out)
		}
	})
	t.Run("during a line's read of the thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		sigs := chatSigs(t)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterStandardJSON, 42))
		p.on("POST", "/bots/scout/chat/threads/thr_old/turns/trn_1/cancel", 202, chatTurnBody)
		var mu sync.Mutex
		reads := 0
		p.onFunc("GET", "/bots/scout/chat/threads/thr_old", func(rec) (int, string) {
			mu.Lock()
			reads++
			n := reads
			mu.Unlock()
			if n == 2 { // the first line's read; the first read is the start's
				sigs <- os.Interrupt
			}
			return 200, chatDetailOn("thr_old", rosterStandardJSON, 42)
		})
		out, errs, err := chatREPLRun(t, p, "hello\nagain\n/quit\n", "--thread", "thr_old")
		chatREPLExit(t, err, errs)
		if !strings.Contains(errs, "· interrupted: nothing was asked\n") {
			t.Errorf("stderr %q lacks the interrupted line", errs)
		}
		if strings.Contains(errs, "cancelling") || strings.Contains(errs, "Error:") {
			t.Errorf("stderr %q", errs)
		}
		want := []map[string]any{{"prompt": "again"}}
		if got := chatTurnMaps(t, p, "thr_old"); !reflect.DeepEqual(got, want) {
			t.Errorf("turns %v, want %v", got, want)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_old/turns/trn_1/cancel")); n != 0 {
			t.Errorf("%d cancels, want none", n)
		}
		if n := strings.Count(out, "> "); n != 3 {
			t.Errorf("the prompt was printed %d times, want 3: %q", n, out)
		}
	})
}

// TestChatREPLCtrlCAtTheStart: a Ctrl-C during the conversation's first read of its thread (--thread) ends
// it before it follows, changes or asks anything: no PATCH with --roster, no cancel of an open turn, no
// prompt; exit 1, interrupted: nothing was asked.
func TestChatREPLCtrlCAtTheStart(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		detail string
	}{
		{"before the council's change", []string{"--thread", "thr_old", "--roster", "ops"}, chatDetailOn("thr_old", rosterStandardJSON, 42)},
		{"before following the open turn", []string{"--thread", "thr_old"},
			chatDetailOn("thr_old", rosterStandardJSON, 42, chatTurnAt("trn_9", "thr_old", 1, "running", ""))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, true, true)
			sigs := chatSigs(t)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
			chatOldThread(p, c.detail)
			chatStreamTrn9(p)
			p.on("POST", "/bots/scout/chat/threads/thr_old/turns/trn_9/cancel", 202, chatTurnBody)
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "ops", "Ops"))
			var once sync.Once
			p.onFunc("GET", "/bots/scout/chat/threads/thr_old", func(rec) (int, string) {
				once.Do(func() { sigs <- os.Interrupt }) // the person presses Ctrl-C while the conversation starts
				return 200, c.detail
			})
			out, errs, err := chatREPLRun(t, p, "/quit\n", c.args...)
			if err == nil || exitCodeOf(err) != codeError || err.Error() != "interrupted: nothing was asked" {
				t.Fatalf("exit %d: %v, want exit 1 interrupted: nothing was asked\n%s", exitCodeOf(err), err, errs)
			}
			if got, want := chatRequestOrder(p), []string{"GET /bots/scout/chat/threads/thr_old"}; !reflect.DeepEqual(got, want) {
				t.Errorf("requests %q, want %q", got, want)
			}
			if strings.Contains(out, "> ") {
				t.Errorf("stdout %q: a prompt after the Ctrl-C", out)
			}
			if strings.Contains(errs, "cancelling") || strings.Contains(errs, "Error:") {
				t.Errorf("stderr %q", errs)
			}
		})
	}
}

// TestChatREPLCtrlCDuringTheStartChange: a conversation opened with --thread --roster that gets a Ctrl-C
// during the council's change (P9) ends before it prompts, as one during the read does; the change stays.
func TestChatREPLCtrlCDuringTheStartChange(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	sigs := chatSigs(t)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
	chatOldThread(p, chatDetailOn("thr_old", rosterStandardJSON, 42))
	p.onFunc("PATCH", "/bots/scout/chat/threads/thr_old", func(rec) (int, string) {
		sigs <- os.Interrupt // the person presses Ctrl-C while the council changes
		return 200, chatThreadOnRoster("thr_old", "ops", "Ops")
	})
	out, errs, err := chatREPLRun(t, p, "hello\n/quit\n", "--thread", "thr_old", "--roster", "ops")
	if err == nil || exitCodeOf(err) != codeError || err.Error() != "interrupted: nothing was asked" {
		t.Fatalf("exit %d: %v, want exit 1 interrupted: nothing was asked\n%s", exitCodeOf(err), err, errs)
	}
	if got, want := chatRequestOrder(p), []string{"GET /bots/scout/chat/threads/thr_old", "PATCH /bots/scout/chat/threads/thr_old"}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests %q, want %q", got, want)
	}
	if !strings.Contains(errs, "· this chat's council is now Ops (ops)\n") {
		t.Errorf("stderr %q lacks the change's line", errs)
	}
	if strings.Contains(out, "> ") {
		t.Errorf("stdout %q: a prompt after the Ctrl-C", out)
	}
}
