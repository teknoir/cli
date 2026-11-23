package cmd

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestChatThreadReadHubWords: a refusal of the thread's read (P4) reads as P3, P6 and P9 do in the same
// command: the hub's words, the code's exit (12, C3a ruling 3), never "(HTTP n)" or the 403 prefix.
func TestChatThreadReadHubWords(t *testing.T) {
	notFound := councilErrBody("not_found", "This chat or turn does not exist.", "")
	cases := []struct {
		name   string
		status int
		body   string
		args   []string
		code   int
		want   string
	}{
		{"one-shot, a gone thread", 404, notFound, []string{"--thread", "thr_old", "hello"}, codeNotFound, "This chat or turn does not exist."},
		{"one-shot, not allowed", 403, councilErrBody("chat_not_allowed", "You may no longer chat with scout.", ""), []string{"--thread", "thr_old", "hello"}, codeAuth, "You may no longer chat with scout."},
		{"one-shot, council off", 404, councilErrBody("council_off", "The council is turned off on this platform.", ""), []string{"--thread", "thr_old", "hello"}, codeError, "The council is turned off on this platform."},
		{"follow, a gone thread", 404, notFound, []string{"--thread", "thr_old", "--follow"}, codeNotFound, "This chat or turn does not exist."},
		{"follow, council off", 404, councilErrBody("council_off", "The council is turned off on this platform.", ""), []string{"--thread", "thr_old", "--follow"}, codeError, "The council is turned off on this platform."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("GET", "/bots/scout/chat/threads/thr_old", c.status, c.body)
			_, _, err := runCLI(t, p.env(t), append([]string{"chat", "scout"}, c.args...)...)
			if err == nil || exitCodeOf(err) != c.code || err.Error() != c.want {
				t.Errorf("exit %d, err %v; want exit %d, %q", exitCodeOf(err), err, c.code, c.want)
			}
		})
	}
	t.Run("the conversation's start", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		p.on("GET", "/bots/scout/chat/threads/thr_old", 404, notFound)
		_, _, err := chatREPLRun(t, p, "/quit\n", "--thread", "thr_old")
		if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != "This chat or turn does not exist." {
			t.Errorf("exit %d, err %v", exitCodeOf(err), err)
		}
	})
	t.Run("a line's read", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoV2)
		chatOldThread(p, chatDetailOn("thr_old", rosterStandardJSON, 42))
		p.onSeq("GET", "/bots/scout/chat/threads/thr_old",
			chatReply{200, chatDetailOn("thr_old", rosterStandardJSON, 42)},
			chatReply{404, notFound})
		_, errs, err := chatREPLRun(t, p, "hello\n/quit\n", "--thread", "thr_old")
		chatREPLExit(t, err, errs)
		if !strings.Contains(errs, "Error: This chat or turn does not exist.\n") || strings.Contains(errs, "(HTTP") {
			t.Errorf("stderr %q", errs)
		}
	})
}

// TestChatStreamRefusalHubWords: a refusal of the event stream (P8) maps like P6's: the hub's words, the
// code's exit, and no retry.
func TestChatStreamRefusalHubWords(t *testing.T) {
	for _, c := range []struct {
		code, msg    string
		status, exit int
	}{
		{"chat_not_allowed", "You may no longer chat with scout.", 403, codeAuth},
		{"not_found", "This chat or turn does not exist.", 404, codeNotFound},
		{"council_off", "The council is turned off on this platform.", 404, codeError},
		{"bad_request", "The request is not valid.", 400, codeRejected},
	} {
		t.Run(c.code, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
			p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
			p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, councilErrBody(c.code, c.msg, ""))
			})
			_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello")
			if exitCodeOf(err) != c.exit || err == nil || err.Error() != c.msg {
				t.Errorf("exit %d, err %v; want exit %d, %q\n%s", exitCodeOf(err), err, c.exit, c.msg, errs)
			}
			if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_new/events")); n != 1 {
				t.Errorf("stream opened %d times, want 1 (a refusal is not retried)", n)
			}
		})
	}
}

const chatCancelRefused = "Only the author of this turn or the bot's creator can cancel it."

// TestChatREPLCancelRefusedShowsTheHubsWords: /cancel of another person's turn (P7 not_author) prints the
// hub's sentence alone, and the conversation goes on.
func TestChatREPLCancelRefusedShowsTheHubsWords(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.onSeq("GET", "/bots/scout/chat/threads/thr_a",
		chatReply{200, chatThreadDetail("thr_a", 4, chatTurnAt("trn_done", "thr_a", 1, "done", "the answer"))},
		chatReply{200, chatThreadDetail("thr_a", 6,
			chatTurnAt("trn_done", "thr_a", 1, "done", "the answer"),
			chatTurnAt("trn_other", "thr_a", 2, "running", ""))})
	p.on("POST", "/bots/scout/chat/threads/thr_a/turns/trn_other/cancel", 403, councilErrBody("not_author", chatCancelRefused, ""))
	p.onStream("/bots/scout/chat/threads/thr_a/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		t.Error("no turn may be followed")
	})
	_, errs, err := chatREPLRun(t, p, "/cancel\n/quit\n", "--thread", "thr_a")
	chatREPLExit(t, err, errs)
	if !strings.Contains(errs, "Error: "+chatCancelRefused+"\n") || strings.Contains(errs, "(HTTP") || strings.Contains(errs, "not allowed for this account") {
		t.Errorf("stderr %q", errs)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_a/turns/trn_other/cancel")); n != 1 {
		t.Errorf("%d cancels, want 1", n)
	}
}

// TestChatCtrlCCancelRefusedShowsTheHubsWords: a Ctrl-C whose cancel (P7) is refused says so in the hub's
// words on the progress line.
func TestChatCtrlCCancelRefusedShowsTheHubsWords(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	sigs := make(chan os.Signal, 2)
	old := chatSignals
	chatSignals = func() (<-chan os.Signal, func()) { return sigs, func() {} }
	t.Cleanup(func() { chatSignals = old })
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel", 403, councilErrBody("not_author", chatCancelRefused, ""))
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, frame(1, "text_delta", `"member":"captain","text":"start"`))
		w.(http.Flusher).Flush()
		sigs <- os.Interrupt
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && len(p.seen("POST", "/bots/scout/chat/threads/thr_new/turns/trn_1/cancel")) == 0 {
			time.Sleep(2 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, frame(2, "turn_end", `"state":"done"`))
		w.(http.Flusher).Flush()
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if !strings.Contains(errs, "· cancel failed: "+chatCancelRefused+"\n") || strings.Contains(errs, "(HTTP") {
		t.Errorf("stderr %q", errs)
	}
}

// TestChatInfoCouncilRefusals: P1 and P2 refusals read as every council route's: the hub's words, the
// code's exit; council_off keeps tnctl's sentence and a code-less 404 keeps classify's exit 3.
func TestChatInfoCouncilRefusals(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 404, councilErrBody("not_found", "There is no TeknoirBot scout.", ""))
	p.on("GET", "/bots/scout/chat/threads", 403, councilErrBody("chat_not_allowed", "You may no longer chat with scout.", ""))
	_, _, err := runCLI(t, p.env(t), "chat", "scout", "hi")
	if err == nil || exitCodeOf(err) != codeNotFound || err.Error() != "There is no TeknoirBot scout." {
		t.Errorf("chat: exit %d: %v", exitCodeOf(err), err)
	}
	_, _, err = runCLI(t, p.env(t), "chat", "threads", "scout")
	if err == nil || exitCodeOf(err) != codeAuth || err.Error() != "You may no longer chat with scout." {
		t.Errorf("chat threads: exit %d: %v", exitCodeOf(err), err)
	}
	q := newChatAPI(t)
	q.on("GET", "/bots/scout/chat", 404, `{"error":{"name":"NotFoundError","message":"TeknoirBots are not enabled."}}`)
	_, _, err = runCLI(t, q.env(t), "chat", "scout", "hi")
	if err == nil || exitCodeOf(err) != codeNotFound {
		t.Errorf("code-less: exit %d: %v", exitCodeOf(err), err)
	}
}

// TestChatRefusesDotSegmentsBeforeAnyRequest: a bot or --thread that url.PathEscape leaves as a dot
// segment is refused as a usage error before any request, as every council command refuses its --bot.
func TestChatRefusesDotSegmentsBeforeAnyRequest(t *testing.T) {
	const notThread = " is not a thread id: use an id that tnctl chat threads <bot> lists"
	const notBot = " is not a bot name: use the bot's name, such as scout"
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"chat", "scout", "--thread", "..", "--roster", "review", "hi"}, `--thread ".."` + notThread},
		{[]string{"chat", "scout", "--thread", ".", "hi"}, `--thread "."` + notThread},
		{[]string{"chat", "..", "hi"}, `".."` + notBot},
		{[]string{"chat", "threads", ".."}, `".."` + notBot},
		{[]string{"chat", "Scout", "hi"}, `"Scout"` + notBot},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			_, _, err := runCLI(t, p.env(t), c.args...)
			if err == nil || exitCodeOf(err) != codeUsage || err.Error() != c.want {
				t.Fatalf("exit %d, %v; want 2, %q", exitCodeOf(err), err, c.want)
			}
			if n := len(chatReqs(p)); n != 0 {
				t.Errorf("%d requests sent, want none", n)
			}
		})
	}
}
