package cmd

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// ---------- helpers ----------

// chatInfoCouncils is the chat's info on a platform that sends the bot's councils.
var chatInfoCouncils = strings.TrimSuffix(chatInfoBody, "}") +
	`,"council":{"defaultRoster":"standard","rosters":[{"name":"standard"},{"name":"ops"}]}}`

// chatThreadOnRoster is P9's answer: the thread as it is after the council change.
func chatThreadOnRoster(id, name, display string) string {
	return `{"thread":{"id":` + q(id) + `,"title":"t","origin":"cli","member":null,"author":{"ref":"r"},` +
		`"createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":null,` +
		`"roster":{"id":"ros_1","name":` + q(name) + `,"displayName":` + q(display) + `},` +
		`"membersPin":null,"memberRole":null}}`
}

// chatOldThread registers the thr_old thread with the given detail: its turn and its stream.
func chatOldThread(p *chatAPI, detail string) {
	p.on("GET", "/bots/scout/chat/threads/thr_old", 200, detail)
	p.on("POST", "/bots/scout/chat/threads/thr_old/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_old/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, quickFrames(quickFinal))
	})
}

// chatBodyMap decodes one request body: a key that must be absent is then absent from the map too.
func chatBodyMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return m
}

// chatCountMethod is how many requests of one method the fake platform saw.
func chatCountMethod(p *chatAPI, method string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, r := range p.reqs {
		if r.Method == method {
			n++
		}
	}
	return n
}

// chatReqs is a copy of the requests the fake platform saw, in order.
func chatReqs(p *chatAPI) []rec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]rec(nil), p.reqs...)
}

// chatRequestOrder is "METHOD path" of each request the fake platform saw, in order, leaving out the
// chat's info (GET /bots/scout/chat) and the event streams.
func chatRequestOrder(p *chatAPI) []string {
	var order []string
	for _, r := range chatReqs(p) {
		if r.Path == "/bots/scout/chat" || strings.HasSuffix(r.Path, "/events") {
			continue
		}
		order = append(order, r.Method+" "+r.Path)
	}
	return order
}

// chatOneShotFrames runs one turn on a new thread with these frames and returns stderr and the error.
func chatOneShotFrames(t *testing.T, frames []string) (string, error) {
	t.Helper()
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, frames)
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
	return errs, err
}

// chatMemberThreadFleet is P4's answer for thr_old, a chat with fleet alone.
const chatMemberThreadFleet = `{"thread":{"id":"thr_old","title":"t","origin":"cli","member":"fleet","author":{"ref":"r"},` +
	`"createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":null},"turns":[],"lastSeq":42}`

// chatPatchThenTurn is the order of P9, P4 and P6 when --thread and --roster ask one turn.
var chatPatchThenTurn = []string{
	"PATCH /bots/scout/chat/threads/thr_old",
	"GET /bots/scout/chat/threads/thr_old",
	"POST /bots/scout/chat/threads/thr_old/turns",
}

// chatProgressLines is the "· " lines of stderr, in order.
func chatProgressLines(errs string) []string {
	var got []string
	for _, l := range strings.Split(strings.TrimSuffix(errs, "\n"), "\n") {
		if strings.HasPrefix(l, "· ") {
			got = append(got, l+"\n")
		}
	}
	return got
}

// ---------- the flags ----------

// Every refusal of the new flags comes before any request, not even the chat's info.
func TestChatCouncilUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"an unknown member scope", []string{"chat", "scout", "--member", "robot/triage", "hi"},
			`--member is a member's slug, such as triage, or <scope>/<slug> with scope bot, teamspace or template, not "robot/triage"`},
		{"a member and a roster", []string{"chat", "scout", "--member", "fleet", "--roster", "review", "hi"},
			"--member and --roster cannot be used together: a member answers alone, without a council"},
		{"thorough with a member", []string{"chat", "scout", "--thorough", "--member", "fleet", "hi"},
			"--thorough asks the whole council: it cannot be used with --member"},
		{"thorough with another mode", []string{"chat", "scout", "--thorough", "--mode", "quick", "hi"},
			"--thorough is --mode council: it cannot be used with --mode quick"},
		{"a council name in capitals", []string{"chat", "scout", "--roster", "Review", "hi"},
			`--roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "Review"`},
		{"a council name starting with a digit", []string{"chat", "scout", "--roster", "2nd", "hi"},
			`--roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "2nd"`},
		{"a bad roster before the two flags", []string{"chat", "scout", "--member", "fleet", "--roster", "Review", "hi"},
			`--roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "Review"`},
		{"a bad roster before follow", []string{"chat", "scout", "--thread", "thr_1", "--follow", "--roster", "Review"},
			`--roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "Review"`},
		{"follow with a roster", []string{"chat", "scout", "--thread", "thr_1", "--follow", "--roster", "review"},
			"--follow asks nothing: --roster cannot be used with it"},
		{"a scope with a thread", []string{"chat", "scout", "--thread", "thr_1", "--member", "teamspace/triage", "hi"},
			"with --thread, give --member the slug alone: the scope only picks the member of a new chat"},
		{"a member before the roster", []string{"chat", "scout", "--member", "fleet", "--mode", "council", "--roster", "review", "hi"},
			"--member and --mode cannot be used together: a member answers alone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			_, _, err := runCLI(t, p.env(t), c.args...)
			if err == nil || exitCodeOf(err) != codeUsage {
				t.Fatalf("exit %d: %v", exitCodeOf(err), err)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if n := p.countAll(); n != 0 {
				t.Errorf("%d requests were sent", n)
			}
		})
	}
}

// --member's scope picks the member of a new chat only; the turn names the slug alone.
func TestChatMemberScopeSlug(t *testing.T) {
	t.Run("a teamspace member", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, quickFrames(quickFinal))
		})
		out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--member", "teamspace/triage", "hello bot")
		if err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if want := quickFinal + "\n"; out != want {
			t.Errorf("stdout %q, want %q", out, want)
		}
		created := p.seen("POST", "/bots/scout/chat/threads")
		if len(created) != 1 {
			t.Fatalf("threads created: %+v", created)
		}
		got := chatBodyMap(t, created[0].Body)
		want := map[string]any{"title": "hello bot", "origin": "cli", "member": "triage", "memberScope": "teamspace"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("thread body %v, want %v", got, want)
		}
		turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
		if len(turns) != 1 {
			t.Fatalf("turns: %+v", turns)
		}
		got = chatBodyMap(t, turns[0].Body)
		want = map[string]any{"prompt": "hello bot", "member": "triage"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("turn body %v, want %v", got, want)
		}
	})

	t.Run("a bot member without a scope", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, quickFrames(quickFinal))
		})
		if _, errs, err := runCLI(t, p.env(t), "chat", "scout", "--member", "fleet", "hello bot"); err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		created := p.seen("POST", "/bots/scout/chat/threads")
		if len(created) != 1 {
			t.Fatalf("threads created: %+v", created)
		}
		got := chatBodyMap(t, created[0].Body)
		want := map[string]any{"title": "hello bot", "origin": "cli", "member": "fleet"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("thread body %v, want %v", got, want)
		}
		turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
		if len(turns) != 1 {
			t.Fatalf("turns: %+v", turns)
		}
		got = chatBodyMap(t, turns[0].Body)
		want = map[string]any{"prompt": "hello bot", "member": "fleet"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("turn body %v, want %v", got, want)
		}
	})

	t.Run("a member of the thread", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		chatOldThread(p, chatThreadDetail("thr_old", 42))
		if _, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_old", "--member", "triage", "hello bot"); err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 0 {
			t.Errorf("%d threads were created for --thread", n)
		}
		turns := p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")
		if len(turns) != 1 {
			t.Fatalf("turns: %+v", turns)
		}
		got := chatBodyMap(t, turns[0].Body)
		want := map[string]any{"prompt": "hello bot", "member": "triage"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("turn body %v, want %v", got, want)
		}
	})

	// A chat with one member: --thread with that member's slug, or without --member, asks it.
	asks := []struct {
		name string
		args []string
		want map[string]any
	}{
		{"the chat's own member", []string{"chat", "scout", "--thread", "thr_old", "--member", "fleet", "hi"},
			map[string]any{"prompt": "hi", "member": "fleet"}},
		{"a chat with one member, no --member", []string{"chat", "scout", "--thread", "thr_old", "hi"},
			map[string]any{"prompt": "hi"}},
	}
	for _, c := range asks {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			chatOldThread(p, chatMemberThreadFleet)
			out, errs, err := runCLI(t, p.env(t), c.args...)
			if err != nil {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			if want := quickFinal + "\n"; out != want {
				t.Errorf("stdout %q, want %q", out, want)
			}
			turns := p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")
			if len(turns) != 1 {
				t.Fatalf("turns: %+v", turns)
			}
			if got := chatBodyMap(t, turns[0].Body); !reflect.DeepEqual(got, c.want) {
				t.Errorf("turn body %v, want %v", got, c.want)
			}
		})
	}

	// A chat with one member refuses, before the turn, what only a council chat can take.
	refusals := []struct {
		name   string
		detail string
		args   []string
		want   string
	}{
		{"a chat with another member alone", chatMemberThreadFleet,
			[]string{"chat", "scout", "--thread", "thr_old", "--member", "triage", "hi"},
			"thread thr_old is a chat with fleet alone: leave out --member, or start a new chat without --thread"},
		{"thorough on a chat with one member", chatMemberThreadFleet,
			[]string{"chat", "scout", "--thread", "thr_old", "--thorough", "hi"},
			"thread thr_old is a chat with fleet alone: leave out --thorough, or start a new chat without --thread"},
		{"the hub's member slug is escaped", strings.Replace(chatMemberThreadFleet, `"member":"fleet"`, `"member":"fl\neet\u001b[0m"`, 1),
			[]string{"chat", "scout", "--thread", "thr_old", "--member", "triage", "hi"},
			`thread thr_old is a chat with fl\neet alone: leave out --member, or start a new chat without --thread`},
		{"thorough: the hub's member slug is escaped", strings.Replace(chatMemberThreadFleet, `"member":"fleet"`, `"member":"fl\neet\u001b[0m"`, 1),
			[]string{"chat", "scout", "--thread", "thr_old", "--thorough", "hi"},
			`thread thr_old is a chat with fl\neet alone: leave out --thorough, or start a new chat without --thread`},
		// The hub's own test is "any member set" (member !== null): an empty member refuses too.
		{"thorough on a chat whose member is empty", strings.Replace(chatMemberThreadFleet, `"member":"fleet"`, `"member":""`, 1),
			[]string{"chat", "scout", "--thread", "thr_old", "--thorough", "hi"},
			"thread thr_old is a chat with  alone: leave out --thorough, or start a new chat without --thread"},
	}
	for _, c := range refusals {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("GET", "/bots/scout/chat/threads/thr_old", 200, c.detail)
			p.on("POST", "/bots/scout/chat/threads/thr_old/turns", 202, chatTurnBody)
			_, errs, err := runCLI(t, p.env(t), c.args...)
			if err == nil || exitCodeOf(err) != codeUsage {
				t.Fatalf("exit %d: %v", exitCodeOf(err), err)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")); n != 0 {
				t.Errorf("%d turns were posted", n)
			}
			if strings.Contains(errs, "thread thr_old") {
				t.Errorf("stderr %q: the refusal comes before the thread line", errs)
			}
		})
	}
}

// --roster goes into the new thread's body: a new chat is never patched.
func TestChatRosterNewThread(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, quickFrames(quickFinal))
	})
	_, errs, err := runCLI(t, p.env(t), "chat", "scout", "--roster", "review", "hello bot")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	created := p.seen("POST", "/bots/scout/chat/threads")
	if len(created) != 1 {
		t.Fatalf("threads created: %+v", created)
	}
	got := chatBodyMap(t, created[0].Body)
	want := map[string]any{"title": "hello bot", "origin": "cli", "roster": "review"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("thread body %v, want %v", got, want)
	}
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
	if len(turns) != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	got = chatBodyMap(t, turns[0].Body)
	want = map[string]any{"prompt": "hello bot"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("turn body %v, want %v", got, want)
	}
	if n := chatCountMethod(p, "PATCH"); n != 0 {
		t.Errorf("%d patches were sent for a new thread", n)
	}
	if strings.Contains(errs, "council is now") {
		t.Errorf("stderr %q: a new thread's council is not patched", errs)
	}
}

// --thread --roster changes the chat's council first, says so, and then asks the turn.
func TestChatThreadRosterPatches(t *testing.T) {
	t.Run("the patch, then the turn", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "review", "Review"))
		chatOldThread(p, chatThreadDetail("thr_old", 42))
		out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_old", "--roster", "review", "hello bot")
		if err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		patched := p.seen("PATCH", "/bots/scout/chat/threads/thr_old")
		if len(patched) != 1 || patched[0].Body != `{"roster":"review"}` {
			t.Fatalf("patch %+v", patched)
		}
		if order := chatRequestOrder(p); !reflect.DeepEqual(order, chatPatchThenTurn) {
			t.Errorf("requests %q, want %q", order, chatPatchThenTurn)
		}
		turns := p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")
		if len(turns) != 1 {
			t.Fatalf("turns: %+v", turns)
		}
		got := chatBodyMap(t, turns[0].Body)
		if want := map[string]any{"prompt": "hello bot"}; !reflect.DeepEqual(got, want) {
			t.Errorf("turn body %v, want %v", got, want)
		}
		line, thread := "· this chat's council is now Review (review)\n", "thread thr_old\n"
		at, th := strings.Index(errs, line), strings.Index(errs, thread)
		if at < 0 {
			t.Errorf("stderr %q lacks %q", errs, line)
		}
		if th < 0 || (at >= 0 && at > th) {
			t.Errorf("stderr %q: the council line does not come before the thread line", errs)
		}
		if want := quickFinal + "\n"; out != want {
			t.Errorf("stdout %q, want %q", out, want)
		}
	})

	t.Run("--json says nothing on stderr", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, chatThreadOnRoster("thr_old", "review", "Review"))
		chatOldThread(p, chatThreadDetail("thr_old", 42))
		_, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_old", "--roster", "review", "--json", "hello bot")
		if err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if errs != "" {
			t.Errorf("stderr %q, want nothing", errs)
		}
		// the same requests as without --json: the same order, the same PATCH body, the same turn body
		if order := chatRequestOrder(p); !reflect.DeepEqual(order, chatPatchThenTurn) {
			t.Errorf("requests %q, want %q", order, chatPatchThenTurn)
		}
		if patched := p.seen("PATCH", "/bots/scout/chat/threads/thr_old"); len(patched) != 1 || patched[0].Body != `{"roster":"review"}` {
			t.Errorf("patch %+v", patched)
		}
		if turns := p.seen("POST", "/bots/scout/chat/threads/thr_old/turns"); len(turns) != 1 {
			t.Errorf("turns: %+v", turns)
		} else if got, want := chatBodyMap(t, turns[0].Body), map[string]any{"prompt": "hello bot"}; !reflect.DeepEqual(got, want) {
			t.Errorf("turn body %v, want %v", got, want)
		}
	})

	// The line names the council the hub sent back: its display name, and its name when they differ.
	cases := []struct {
		name string
		p9   string
		want string
	}{
		{"a display name that is the name", chatThreadOnRoster("thr_old", "review", "review"),
			"· this chat's council is now review\n"},
		{"a display name with an escape", chatThreadOnRoster("thr_old", "review", "Re"+string(rune(27))+"[31mview"),
			"· this chat's council is now Review (review)\n"},
		{"a thread that names no council", `{"thread":{"id":"thr_old","title":"t","origin":"cli","member":null,` +
			`"author":{"ref":"r"},"createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":null,"roster":null,` +
			`"membersPin":null,"memberRole":null}}`,
			"· this chat's council is now review\n"},
		{"the answer's name, not the asked one", chatThreadOnRoster("thr_old", "ops", "Ops"),
			"· this chat's council is now Ops (ops)\n"},
		{"an answer without a name", chatThreadOnRoster("thr_old", "", "Review"),
			"· this chat's council is now Review (review)\n"},
		{"a name with a newline", chatThreadOnRoster("thr_old", "re\nview", ""),
			"· this chat's council is now re\\nview\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", 200, c.p9)
			chatOldThread(p, chatThreadDetail("thr_old", 42))
			_, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_old", "--roster", "review", "hello bot")
			if err != nil {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			var got []string
			for _, l := range strings.Split(strings.TrimSuffix(errs, "\n"), "\n") {
				if strings.Contains(l, "council is now") {
					got = append(got, l+"\n")
				}
			}
			if len(got) != 1 || got[0] != c.want {
				t.Errorf("council lines %q, want one %q", got, c.want)
			}
			if strings.ContainsAny(errs, "\x1b") {
				t.Errorf("stderr %q: an escape reached the terminal", errs)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")); n != 1 {
				t.Errorf("%d turns, want 1", n)
			}
		})
	}
}

// A refused patch stops the command: nothing after it is sent.
func TestChatThreadRosterPatchErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		info   string
		want   int
		wantEr string
	}{
		{
			name:   "a turn in progress",
			status: 409,
			body:   councilErrBody("turn_open", "This chat has a turn in progress. Wait for it, or cancel it.", ""),
			want:   codeConflict,
			wantEr: "this chat already has a turn in progress (tnctl chat scout --thread thr_old --follow, then Ctrl-C cancels it)",
		},
		{
			name:   "not the author",
			status: 403,
			body:   councilErrBody("not_author", "Only the author of this chat or the bot's creator can change its council.", ""),
			want:   codeAuth,
			wantEr: "Only the author of this chat or the bot's creator can change its council.",
		},
		{
			name:   "a pin that does not fit",
			status: 400,
			body: councilErrBody("invalid", "The pinned members are not all in this council: send membersPin, or null to clear the pin.",
				`"fields":[{"path":"membersPin","message":"The pinned members are not all in this council: send membersPin, or null to clear the pin."}]`),
			want:   codeRejected,
			wantEr: "this chat's pinned members are not all in council 'review': change or clear the pin in the web chat, then run the command again",
		},
		{
			name:   "a pin that does not fit, whatever the status",
			status: 422,
			body: councilErrBody("invalid", "The pinned members are not all in this council: send membersPin, or null to clear the pin.",
				`"fields":[{"path":"membersPin","message":"The pinned members are not all in this council: send membersPin, or null to clear the pin."}]`),
			want:   codeRejected,
			wantEr: "this chat's pinned members are not all in council 'review': change or clear the pin in the web chat, then run the command again",
		},
		{
			name:   "a chat with one member",
			status: 400,
			body: councilErrBody("invalid", "A chat with one member has no council to change.",
				`"fields":[{"path":"roster","message":"A chat with one member has no council to change."}]`),
			want:   codeRejected,
			wantEr: "roster: A chat with one member has no council to change.",
		},
		{
			name:   "an unknown council",
			status: 404,
			body:   councilErrBody("unknown_roster", "There is no council 'review' for scout.", ""),
			want:   codeUsage,
			wantEr: "There is no council 'review' for scout.",
		},
		{
			name:   "an unknown council, with the hub's councils",
			status: 404,
			body:   councilErrBody("unknown_roster", "There is no council 'review' for scout.", ""),
			info:   chatInfoCouncils,
			want:   codeUsage,
			wantEr: "There is no council 'review' for scout.\ncouncils: standard, ops",
		},
		{
			name:   "an unknown council, with an empty list of councils",
			status: 404,
			body:   councilErrBody("unknown_roster", "There is no council 'review' for scout.", ""),
			info:   strings.TrimSuffix(chatInfoBody, "}") + `,"council":{"defaultRoster":"standard","rosters":[]}}`,
			want:   codeUsage,
			wantEr: "There is no council 'review' for scout.",
		},
		{
			name:   "an unknown council, each council's name escaped",
			status: 404,
			body:   councilErrBody("unknown_roster", "There is no council 'review' for scout.", ""),
			info:   strings.TrimSuffix(chatInfoBody, "}") + `,"council":{"defaultRoster":"standard","rosters":[{"name":"standard"},{"name":"o\u0007ps"}]}}`,
			want:   codeUsage,
			wantEr: "There is no council 'review' for scout.\ncouncils: standard, o\\u0007ps",
		},
		{
			name:   "a turn in progress, with the hub's councils",
			status: 409,
			body:   councilErrBody("turn_open", "This chat has a turn in progress. Wait for it, or cancel it.", ""),
			info:   chatInfoCouncils,
			want:   codeConflict,
			wantEr: "this chat already has a turn in progress (tnctl chat scout --thread thr_old --follow, then Ctrl-C cancels it)",
		},
		{
			name:   "a pin field under another code keeps the hub's words",
			status: 400,
			body:   councilErrBody("bad_request", "The request is not valid.", `"fields":[{"path":"membersPin","message":"Not valid."}]`),
			want:   codeRejected,
			wantEr: "membersPin: Not valid.",
		},
		{
			name:   "a chat that is gone",
			status: 404,
			body:   councilErrBody("not_found", "This chat or turn does not exist.", ""),
			want:   codeNotFound,
			wantEr: "This chat or turn does not exist.",
		},
		{
			name:   "a chat that is gone, with the hub's councils",
			status: 404,
			body:   councilErrBody("not_found", "This chat or turn does not exist.", ""),
			info:   chatInfoCouncils,
			want:   codeNotFound,
			wantEr: "This chat or turn does not exist.",
		},
		{
			name:   "a council whose member is gone",
			status: 409,
			body:   councilErrBody("role_gone", "A member of council 'review' was removed: fix the council.", ""),
			want:   codeError,
			wantEr: "A member of council 'review' was removed: fix the council.",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			info := chatInfoBody
			if c.info != "" {
				info = c.info
			}
			p.on("GET", "/bots/scout/chat", 200, info)
			p.on("PATCH", "/bots/scout/chat/threads/thr_old", c.status, c.body)
			chatOldThread(p, chatThreadDetail("thr_old", 42))
			_, _, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_old", "--roster", "review", "hello bot")
			if err == nil || exitCodeOf(err) != c.want {
				t.Fatalf("exit %d: %v", exitCodeOf(err), err)
			}
			if err.Error() != c.wantEr {
				t.Errorf("error %q, want %q", err, c.wantEr)
			}
			if n := chatCountMethod(p, "PATCH"); n != 1 {
				t.Errorf("%d patches, want 1", n)
			}
			if n := len(p.seen("GET", "/bots/scout/chat/threads/thr_old")); n != 0 {
				t.Errorf("%d reads of the thread after the refused patch", n)
			}
			if n := len(p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")); n != 0 {
				t.Errorf("%d turns after the refused patch", n)
			}
		})
	}
}

// --thorough is mode council with thorough: only thorough makes the hub record the choice.
func TestChatThorough(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want map[string]any
	}{
		{"thorough alone", []string{"--thorough", "hello bot"},
			map[string]any{"prompt": "hello bot", "mode": "council", "thorough": true}},
		{"thorough with the mode", []string{"--thorough", "--mode", "council", "hello bot"},
			map[string]any{"prompt": "hello bot", "mode": "council", "thorough": true}},
		{"thorough with the effort", []string{"--thorough", "--effort", "high", "hello bot"},
			map[string]any{"prompt": "hello bot", "mode": "council", "effort": "high", "thorough": true}},
		{"the mode alone", []string{"--mode", "council", "hello bot"},
			map[string]any{"prompt": "hello bot", "mode": "council"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
			p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
			p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
			p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
				writeFrames(w, quickFrames(quickFinal))
			})
			if _, errs, err := runCLI(t, p.env(t), append([]string{"chat", "scout"}, c.args...)...); err != nil {
				t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
			}
			turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
			if len(turns) != 1 {
				t.Fatalf("turns: %+v", turns)
			}
			if got := chatBodyMap(t, turns[0].Body); !reflect.DeepEqual(got, c.want) {
				t.Errorf("turn body %v, want %v", got, c.want)
			}
			created := p.seen("POST", "/bots/scout/chat/threads")
			if len(created) != 1 {
				t.Fatalf("threads created: %+v", created)
			}
			if _, has := chatBodyMap(t, created[0].Body)["thorough"]; has {
				t.Errorf("thread body %s carries a thorough", created[0].Body)
			}
		})
	}
}

// The hub's own refusals come back with tnctl's exit codes and the hub's words.
func TestChatHubCouncilCodes(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		route   string // the route that refuses
		status  int
		body    string
		info    string
		want    int
		wantIn  []string
		wantEx  string
		oldTurn bool // the request is posted on thr_old
	}{
		{
			name: "a lead's slug on a new thread", args: []string{"--member", "captain", "hi"},
			route: "POST /bots/scout/chat/threads", status: 400,
			body: councilErrBody("unknown_member", "The Captain leads every turn: ask without @.", ""),
			want: codeUsage, wantEx: "The Captain leads every turn: ask without @.",
		},
		{
			name: "two members with one slug", args: []string{"--member", "triage", "hi"},
			route: "POST /bots/scout/chat/threads", status: 400,
			body: councilErrBody("ambiguous", "More than one member is called 'triage' (teamspace, bot): name the scope.", ""),
			want: codeUsage, wantEx: "More than one member is called 'triage' (teamspace, bot): name the scope.",
		},
		{
			name: "an unknown council on a new thread", args: []string{"--roster", "ops2", "hi"},
			route: "POST /bots/scout/chat/threads", status: 404,
			body: councilErrBody("unknown_roster", "There is no council 'ops2' for scout.", ""),
			want: codeUsage, wantEx: "There is no council 'ops2' for scout.",
		},
		{
			name: "an unknown council, with the hub's councils", args: []string{"--roster", "ops2", "hi"},
			route: "POST /bots/scout/chat/threads", status: 404,
			body: councilErrBody("unknown_roster", "There is no council 'ops2' for scout.", ""),
			info: chatInfoCouncils,
			want: codeUsage, wantEx: "There is no council 'ops2' for scout.\ncouncils: standard, ops",
		},
		{
			name: "a listed council deleted before the thread is made", args: []string{"--roster", "ops", "hi"},
			route: "POST /bots/scout/chat/threads", status: 404,
			body: councilErrBody("unknown_roster", "There is no council 'ops' for scout.", ""),
			info: chatInfoCouncils,
			want: codeUsage, wantEx: "There is no council 'ops' for scout.\ncouncils: standard",
		},
		{
			name: "the only listed council deleted before the thread is made", args: []string{"--roster", "ops", "hi"},
			route: "POST /bots/scout/chat/threads", status: 404,
			body: councilErrBody("unknown_roster", "There is no council 'ops' for scout.", ""),
			info: strings.TrimSuffix(chatInfoBody, "}") + `,"council":{"defaultRoster":"standard","rosters":[{"name":"ops"}]}}`,
			want: codeUsage, wantEx: "There is no council 'ops' for scout.",
		},
		{
			name: "a creator-level bot", args: []string{"--member", "coder", "hi"},
			route: "POST /bots/scout/chat/threads", status: 409,
			body: councilErrBody("creator_only", "This bot works at creator level: only its creator can start this chat.", ""),
			want: codeError, wantEx: "This bot works at creator level: only its creator can start this chat.",
		},
		{
			name: "a member outside the chat's council", args: []string{"--thread", "thr_old", "--member", "triage", "hi"},
			route: "POST /bots/scout/chat/threads/thr_old/turns", status: 400, oldTurn: true,
			body: councilErrBody("unknown_member", "'triage' is not a member of this council. Members: fleet, coder.", ""),
			want: codeUsage, wantEx: "'triage' is not a member of this council. Members: fleet, coder.",
		},
		{
			name: "a computer too old for the council", args: []string{"hi"},
			route: "POST /bots/scout/chat/threads/thr_new/turns", status: 409,
			body: councilErrBody("council_outdated", "The bot's computer is too old for this council.", ""),
			want: codeError, wantEx: "The bot's computer is too old for this council.",
		},
		{
			name: "a council only the creator may ask", args: []string{"hi"},
			route: "POST /bots/scout/chat/threads/thr_new/turns", status: 409,
			body: councilErrBody("creator_only", "Only the bot's creator can ask this council.", ""),
			want: codeError, wantEx: "Only the bot's creator can ask this council.",
		},
		{
			name: "a council that just changed", args: []string{"hi"},
			route: "POST /bots/scout/chat/threads/thr_new/turns", status: 409,
			body: councilErrBody("conflict", "This chat's council just changed. Send again.", ""),
			want: codeConflict, wantEx: "This chat's council just changed. Send again.",
		},
		{
			name: "a bot that is not ready", args: []string{"hi"},
			route: "POST /bots/scout/chat/threads/thr_new/turns", status: 409,
			body: councilErrBody("bot_not_ready", "The bot is starting.", ""),
			want: codeConflict, wantIn: []string{"The bot is starting.", "(HTTP 409)"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			info := chatInfoBody
			if c.info != "" {
				info = c.info
			}
			p.on("GET", "/bots/scout/chat", 200, info)
			p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
			if c.oldTurn {
				p.on("GET", "/bots/scout/chat/threads/thr_old", 200, chatThreadDetail("thr_old", 42))
				p.onStream("/bots/scout/chat/threads/thr_old/events", func(w http.ResponseWriter, r *http.Request, conn int) {
					t.Error("the stream was opened after a refusal")
				})
			} else {
				p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
					t.Error("the stream was opened after a refusal")
				})
			}
			p.on("POST", c.route[len("POST "):], c.status, c.body)
			_, _, err := runCLI(t, p.env(t), append([]string{"chat", "scout"}, c.args...)...)
			if err == nil || exitCodeOf(err) != c.want {
				t.Fatalf("exit %d: %v", exitCodeOf(err), err)
			}
			if c.wantEx != "" && err.Error() != c.wantEx {
				t.Errorf("error %q, want %q", err, c.wantEx)
			}
			for _, in := range c.wantIn {
				if !strings.Contains(err.Error(), in) {
					t.Errorf("error %q lacks %q", err, in)
				}
			}
			// nothing after the refusal: no turn after a refused thread, and the stream unopened
			wantTurns := 0
			if c.route != "POST /bots/scout/chat/threads" {
				wantTurns = 1
			}
			path := "/bots/scout/chat/threads/thr_new/turns"
			if c.oldTurn {
				path = "/bots/scout/chat/threads/thr_old/turns"
			}
			if n := len(p.seen("POST", path)); n != wantTurns {
				t.Errorf("%d turns to %s, want %d", n, path, wantTurns)
			}
		})
	}
}

// The progress lines name the council that answers, and the members as the bot's computer names
// them; an old computer's data keeps the slugs.
func TestChatProgressUsesNames(t *testing.T) {
	t.Run("a council turn with names", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{
				frame(1, "turn_start", `"mode":"council","roster":{"name":"review","displayName":"Review","hash":"sha256:ab"}`),
				frame(2, "plan", `"mode":"council","tasks":[{"member":"fleet","name":"Fleet","task":"look"},{"member":"triage","task":"read"}]`),
				frame(3, "member_start", `"member":"fleet","name":"Fleet","text":"checking the robot"`),
				frame(4, "tool_call", `"member":"fleet","id":"c1","tool":"robots.list","input":{}`),
				frame(5, "member_start", `"member":"triage","name":"Triage","text":"reading"`),
				frame(6, "tool_call", `"member":"triage","id":"c2","tool":"pods.list","input":{}`),
				frame(7, "member_done", `"member":"fleet"`),
				frame(8, "member_done", `"member":"triage"`),
				frame(9, "critique", `"member":"critic","name":"Critic","issues":[{"member":"fleet","severity":"blocking","claim":"x","why":"y"}]`),
				frame(10, "final", `"text":"done"`),
				frame(11, "turn_end", `"state":"done"`),
			})
		})
		out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thorough", "hello bot")
		if err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if want := "done\n"; out != want {
			t.Errorf("stdout %q, want %q", out, want)
		}
		want := []string{
			"· council Review (review) answers\n",
			"· council: Fleet, triage\n",
			"· Fleet: checking the robot\n",
			"· Fleet → robots.list\n",
			"· Triage: reading\n",
			"· Triage → pods.list\n",
			"· Fleet done\n",
			"· Triage done\n",
			"· Critic: 1 issues (1 blocking)\n",
		}
		if got := chatProgressLines(errs); !reflect.DeepEqual(got, want) {
			t.Errorf("progress\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("a council whose display name is its name", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{
				frame(1, "turn_start", `"mode":"auto","roster":{"name":"ops","displayName":"ops","hash":"h"}`),
				frame(2, "critique", `"member":"skeptic","issues":[]`),
				frame(3, "final", `"text":"done"`),
				frame(4, "turn_end", `"state":"done"`),
			})
		})
		_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
		if err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		want := []string{"· council ops answers\n", "· skeptic: 0 issues (0 blocking)\n"}
		if got := chatProgressLines(errs); !reflect.DeepEqual(got, want) {
			t.Errorf("progress\n%q\nwant\n%q", got, want)
		}
	})

	t.Run("a name with an escape", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		// The name is "Fl" + ESC + "[31m" + "eet": its escape is written as JSON writes it.
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{
				frame(1, "member_done", `"member":"fleet","name":"Fl\u001b[31meet"`),
				frame(2, "final", `"text":"done"`),
				frame(3, "turn_end", `"state":"done"`),
			})
		})
		_, errs, err := runCLI(t, p.env(t), "chat", "scout", "hello bot")
		if err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		want := []string{"· Fleet done\n"}
		if got := chatProgressLines(errs); !reflect.DeepEqual(got, want) {
			t.Errorf("progress\n%q\nwant\n%q", got, want)
		}
		if strings.ContainsAny(errs, "\x1b") {
			t.Errorf("stderr %q: an escape reached the terminal", errs)
		}
		if strings.Contains(errs, "skipped a malformed event") {
			t.Errorf("stderr %q: the frame was malformed", errs)
		}
	})
}

// --json writes the council's fields as the platform sent them.
func TestChatJSONPassesCouncilFields(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		writeFrames(w, []string{
			frame(1, "turn_start", `"mode":"council","roster":{"name":"review","displayName":"Review","hash":"sha256:ab"}`),
			frame(2, "plan", `"mode":"council","tasks":[{"member":"fleet","name":"Fleet","task":"look","why":"w"}]`),
			frame(3, "critique", `"member":"critic","name":"Critic","issues":[]`),
			frame(4, "turn_end", `"state":"done"`),
		})
	})
	out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--json", "hello bot")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if errs != "" {
		t.Errorf("stderr %q, want nothing", errs)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines %q", lines)
	}
	events := make([]map[string]any, len(lines))
	for i, l := range lines {
		if err := json.Unmarshal([]byte(l), &events[i]); err != nil {
			t.Fatalf("line %d %q: %v", i, l, err)
		}
	}
	roster, _ := events[0]["roster"].(map[string]any)
	if roster == nil || roster["displayName"] != "Review" {
		t.Errorf("turn_start line %v lacks the council's display name", events[0])
	}
	tasks, _ := events[1]["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("plan line %v", events[1])
	}
	task, _ := tasks[0].(map[string]any)
	if task == nil || task["name"] != "Fleet" || task["why"] != "w" {
		t.Errorf("the plan's task %v lacks its name and why", task)
	}
	if events[2]["member"] != "critic" || events[2]["name"] != "Critic" {
		t.Errorf("critique line %v lacks the reviewer", events[2])
	}
}

// The conversation sends the same bodies: the council, the member's scope, and thorough.
func TestChatREPLBodies(t *testing.T) {
	t.Run("thorough, then one member", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		p.on("GET", "/bots/scout/chat/threads/thr_new", 200,
			`{"thread":{"id":"thr_new","title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":0,"lastTurn":null},"turns":[],"lastSeq":2}`)
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{frame(1, "final", `"text":"ok"`), frame(2, "turn_end", `"state":"done"`)})
		})
		oldIn := cliStdin
		cliStdin = strings.NewReader("hello\n@fleet look\n/quit\n")
		t.Cleanup(func() { cliStdin = oldIn })
		if _, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thorough"); err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
		if len(turns) != 2 {
			t.Fatalf("turns: %+v", turns)
		}
		want := []map[string]any{
			{"prompt": "hello", "mode": "council", "thorough": true},
			{"prompt": "look", "member": "fleet"},
		}
		for i, w := range want {
			if got := chatBodyMap(t, turns[i].Body); !reflect.DeepEqual(got, w) {
				t.Errorf("turn %d body %v, want %v", i, got, w)
			}
		}
	})

	t.Run("a roster", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{frame(1, "final", `"text":"ok"`), frame(2, "turn_end", `"state":"done"`)})
		})
		oldIn := cliStdin
		cliStdin = strings.NewReader("hello\n/quit\n")
		t.Cleanup(func() { cliStdin = oldIn })
		if _, errs, err := runCLI(t, p.env(t), "chat", "scout", "--roster", "review"); err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		created := p.seen("POST", "/bots/scout/chat/threads")
		if len(created) != 1 {
			t.Fatalf("threads created: %+v", created)
		}
		want := map[string]any{"title": "hello", "origin": "cli", "roster": "review"}
		if got := chatBodyMap(t, created[0].Body); !reflect.DeepEqual(got, want) {
			t.Errorf("thread body %v, want %v", got, want)
		}
		if n := chatCountMethod(p, "PATCH"); n != 0 {
			t.Errorf("%d patches were sent", n)
		}
	})

	t.Run("a member with a scope", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
		p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
		p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
			writeFrames(w, []string{frame(1, "final", `"text":"ok"`), frame(2, "turn_end", `"state":"done"`)})
		})
		oldIn := cliStdin
		cliStdin = strings.NewReader("hello\n/quit\n")
		t.Cleanup(func() { cliStdin = oldIn })
		if _, errs, err := runCLI(t, p.env(t), "chat", "scout", "--member", "teamspace/triage"); err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		created := p.seen("POST", "/bots/scout/chat/threads")
		if len(created) != 1 {
			t.Fatalf("threads created: %+v", created)
		}
		want := map[string]any{"title": "hello", "origin": "cli", "member": "triage", "memberScope": "teamspace"}
		if got := chatBodyMap(t, created[0].Body); !reflect.DeepEqual(got, want) {
			t.Errorf("thread body %v, want %v", got, want)
		}
		turns := p.seen("POST", "/bots/scout/chat/threads/thr_new/turns")
		if len(turns) != 1 {
			t.Fatalf("turns: %+v", turns)
		}
		want = map[string]any{"prompt": "hello", "member": "triage"}
		if got := chatBodyMap(t, turns[0].Body); !reflect.DeepEqual(got, want) {
			t.Errorf("turn body %v, want %v", got, want)
		}
	})

	t.Run("an unknown council", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
		p.on("POST", "/bots/scout/chat/threads", 404,
			councilErrBody("unknown_roster", "There is no council 'ops2' for scout.", ""))
		oldIn := cliStdin
		cliStdin = strings.NewReader("hello\nagain\n/quit\n")
		t.Cleanup(func() { cliStdin = oldIn })
		_, errs, err := runCLI(t, p.env(t), "chat", "scout", "--roster", "ops2")
		if err == nil || exitCodeOf(err) != codeUsage {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if want := "There is no council 'ops2' for scout."; err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if strings.Contains(errs, "There is no council") {
			t.Errorf("stderr %q: the REPL printed the refusal", errs)
		}
		if strings.Contains(errs, "Error:") {
			t.Errorf("stderr %q: the REPL printed the refusal", errs)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 1 {
			t.Errorf("%d threads were created, want 1 (the refusal ends the conversation)", n)
		}
		for _, r := range chatReqs(p) {
			if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/turns") {
				t.Errorf("a turn was posted: %s", r.Path)
			}
		}
	})

	t.Run("an unknown council, with the bot's councils", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, true, true)
		p := newChatAPI(t)
		p.on("GET", "/bots/scout/chat", 200, chatInfoCouncils)
		p.on("POST", "/bots/scout/chat/threads", 404,
			councilErrBody("unknown_roster", "There is no council 'ops2' for scout.", ""))
		oldIn := cliStdin
		cliStdin = strings.NewReader("hello\nagain\n/quit\n")
		t.Cleanup(func() { cliStdin = oldIn })
		out, errs, err := runCLI(t, p.env(t), "chat", "scout", "--roster", "ops2")
		if err == nil || exitCodeOf(err) != codeUsage {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if want := "There is no council 'ops2' for scout.\ncouncils: standard, ops"; err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if out != "" {
			t.Errorf("stdout %q, want nothing (tnctl refused before the banner)", out)
		}
		if errs != "" {
			t.Errorf("stderr %q, want nothing", errs)
		}
		if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 0 {
			t.Errorf("%d threads were created, want none (tnctl refused before P3)", n)
		}
	})
}

// chat threads: the COUNCIL column says which council each chat asks.
func TestChatThreadsShowCouncil(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200, `{"items":[
{"id":"thr_1","title":"one","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"roster":{"id":"ros_1","name":"review","displayName":"Review"}},
{"id":"thr_2","title":"two","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"member":"triage-sre"},
{"id":"thr_3","title":"three","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null}]}`)
	out, _, err := runCLI(t, p.env(t), "chat", "threads", "scout")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines %q", lines)
	}
	cells := func(s string) []string { return strings.Fields(s) }
	if got, want := strings.Join(cells(lines[0]), " "), "ID TITLE COUNCIL TURNS LAST UPDATED"; got != want {
		t.Errorf("header %q, want %q", got, want)
	}
	for i, want := range []string{"review", "@triage-sre", "(default)"} {
		if got := cells(lines[i+1]); len(got) < 3 || got[2] != want {
			t.Errorf("row %d %q: the council cell is not %q", i+1, got, want)
		}
	}
}

// The help texts say what the flags do.
func TestChatHelpTexts(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	c := newChatCmd(p.env(t))
	if want := "Chat with a TeknoirBot's council, or with one of its members"; c.Short != want {
		t.Errorf("Short %q, want %q", c.Short, want)
	}
	for _, want := range []string{
		"has the lead convene the members and the reviewer check every report",
		"the council's reviewer, when it has one, checks their reports when the turn needs it",
		"@<member> <text> asks one member of the chat's council alone",
		"tnctl council role list --bot <bot> --templates lists each",
	} {
		if !strings.Contains(c.Long, want) {
			t.Errorf("Long lacks %q:\n%s", want, c.Long)
		}
	}
	if th, _, err := c.Find([]string{"threads"}); err != nil || th.Name() != "threads" {
		t.Errorf("no threads subcommand: %v", err)
	} else if !strings.Contains(th.Long, "(@<member> for a chat with one member, (default) when it follows the bot's default council)") {
		t.Errorf("threads Long lacks the COUNCIL column:\n%s", th.Long)
	}
	for _, want := range []string{"--roster review --thorough", "--member teamspace/triage"} {
		if !strings.Contains(c.Example, want) {
			t.Errorf("Example lacks %q:\n%s", want, c.Example)
		}
	}
	usages := map[string]string{
		"member":   "ask one member alone: its slug, or <scope>/<slug> (scope bot, teamspace or template)",
		"roster":   "the council that answers: standard, ops, review or one of the bot's own; with --thread it changes that chat's council first",
		"thorough": "have the lead convene the members and the reviewer check their reports: slower, several times the tokens (the same as --mode council)",
		"mode":     "advanced: how the council answers: auto, quick or council",
		"effort":   "advanced: how hard the members think: low, medium or high",
	}
	for name, want := range usages {
		f := c.Flags().Lookup(name)
		if f == nil {
			t.Errorf("the flag %s is not there", name)
			continue
		}
		if f.Usage != want {
			t.Errorf("--%s usage %q, want %q", name, f.Usage, want)
		}
	}
}

// The order of chatValidate's checks (the brief's 1 to 12). Each row is named by the check that must
// answer and the later checks that fire with it: together the rows hold every pair of checks that can
// fire at once, so moving any check past one it can fire with fails a row.
func TestChatCouncilUsageErrorOrder(t *testing.T) {
	const (
		badMode        = `--mode must be auto, quick or council, not "fast"`
		badMember      = `--member is a member's slug, such as triage, or <scope>/<slug> with scope bot, teamspace or template, not "Captain"`
		badEffort      = `--effort must be low, medium or high, not "max"`
		badRoster      = `--roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not "Review"`
		memberMode     = "--member and --mode cannot be used together: a member answers alone"
		memberRoster   = "--member and --roster cannot be used together: a member answers alone, without a council"
		thoroughMember = "--thorough asks the whole council: it cannot be used with --member"
		thoroughMode   = "--thorough is --mode council: it cannot be used with --mode quick"
		newAndThread   = "--new and --thread cannot be used together"
		followNoThread = "--follow needs --thread"
	)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"1 before 2 5 10", []string{"chat", "scout", "--mode", "fast", "--member", "Captain", "--follow"},
			badMode},
		{"1 before 3 4 5 6 7 8 9 11 12", []string{"chat", "scout", "--mode", "fast", "--member", "teamspace/triage", "--effort", "max", "--roster", "Review", "--thorough", "--new", "--thread", "thr_1", "--follow"},
			badMode},
		{"2 before 10", []string{"chat", "scout", "--member", "Captain", "--follow"},
			badMember},
		{"2 before 3 4 5 6 7 8 9 11", []string{"chat", "scout", "--mode", "quick", "--member", "Captain", "--effort", "max", "--roster", "Review", "--thorough", "--new", "--thread", "thr_1", "--follow"},
			badMember},
		{"3 before 10", []string{"chat", "scout", "--effort", "max", "--follow"},
			badEffort},
		{"3 before 4 5 6 7 8 9 11 12", []string{"chat", "scout", "--mode", "quick", "--member", "teamspace/triage", "--effort", "max", "--roster", "Review", "--thorough", "--new", "--thread", "thr_1", "--follow"},
			badEffort},
		{"4 before 10 11", []string{"chat", "scout", "--roster", "Review", "--follow"},
			badRoster},
		{"4 before 5 6 7 8 9 12", []string{"chat", "scout", "--mode", "quick", "--member", "teamspace/triage", "--roster", "Review", "--thorough", "--new", "--thread", "thr_1", "hi"},
			badRoster},
		{"5 before 10", []string{"chat", "scout", "--mode", "quick", "--member", "fleet", "--follow"},
			memberMode},
		{"5 before 6 7 8 9 11 12", []string{"chat", "scout", "--mode", "quick", "--member", "teamspace/triage", "--roster", "review", "--thorough", "--new", "--thread", "thr_1", "--follow"},
			memberMode},
		{"6 before 10 11", []string{"chat", "scout", "--member", "fleet", "--roster", "review", "--follow"},
			memberRoster},
		{"6 before 7 9 12", []string{"chat", "scout", "--member", "teamspace/triage", "--roster", "review", "--thorough", "--new", "--thread", "thr_1", "hi"},
			memberRoster},
		{"7 before 10", []string{"chat", "scout", "--member", "fleet", "--thorough", "--follow"},
			thoroughMember},
		{"7 before 9 12", []string{"chat", "scout", "--member", "teamspace/triage", "--thorough", "--new", "--thread", "thr_1", "hi"},
			thoroughMember},
		{"8 before 10", []string{"chat", "scout", "--mode", "quick", "--thorough", "--follow"},
			thoroughMode},
		{"8 before 9 11", []string{"chat", "scout", "--mode", "quick", "--roster", "review", "--thorough", "--new", "--thread", "thr_1", "--follow"},
			thoroughMode},
		{"9 before 12", []string{"chat", "scout", "--member", "teamspace/triage", "--new", "--thread", "thr_1", "hi"},
			newAndThread},
		{"9 before 11", []string{"chat", "scout", "--roster", "review", "--new", "--thread", "thr_1", "--follow"},
			newAndThread},
		{"10 before 11", []string{"chat", "scout", "--roster", "review", "--follow"},
			followNoThread},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			setTTY(t, false, false)
			p := newChatAPI(t)
			_, _, err := runCLI(t, p.env(t), c.args...)
			if err == nil || exitCodeOf(err) != codeUsage {
				t.Fatalf("exit %d: %v", exitCodeOf(err), err)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if n := p.countAll(); n != 0 {
				t.Errorf("%d requests were sent", n)
			}
		})
	}
}

// Every named event remembers its member's name for the later events of the turn, cleaned once.
func TestChatProgressRemembersNames(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	// Each name is sent only once, on the event under test: the lines after it show it was remembered.
	// The order is not a real turn's on purpose.
	errs, err := chatOneShotFrames(t, []string{
		frame(1, "plan", `"mode":"council","tasks":[{"member":"fleet","name":"Fleet\nBot","task":"look"}]`),
		frame(2, "tool_call", `"member":"fleet","id":"c1","tool":"robots.list","input":{}`),
		frame(3, "member_done", `"member":"coder","name":"Coder"`),
		frame(4, "member_start", `"member":"coder","text":"again"`),
		frame(5, "critique", `"member":"critic","name":"Critic","issues":[]`),
		frame(6, "tool_call", `"member":"critic","id":"c2","tool":"logs.read","input":{}`),
		frame(7, "final", `"text":"done"`),
		frame(8, "turn_end", `"state":"done"`),
	})
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	want := []string{
		"· council: Fleet Bot\n",
		"· Fleet Bot → robots.list\n",
		"· Coder done\n",
		"· Coder: again\n",
		"· Critic: 0 issues (0 blocking)\n",
		"· Critic → logs.read\n",
	}
	if got := chatProgressLines(errs); !reflect.DeepEqual(got, want) {
		t.Errorf("progress\n%q\nwant\n%q", got, want)
	}
}

// A turn_start whose council has no name prints no council line. A turn_start whose data is a JSON
// object of the wrong shape fails the turn; data that is not an object is skipped
// (TestChatSkipsAMalformedFrame).
func TestChatTurnStartRoster(t *testing.T) {
	t.Run("a council without a name", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		errs, err := chatOneShotFrames(t, []string{
			frame(1, "turn_start", `"mode":"council","roster":{"name":"","displayName":"Review","hash":"h"}`),
			frame(2, "final", `"text":"done"`),
			frame(3, "turn_end", `"state":"done"`),
		})
		if err != nil {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
		if got := chatProgressLines(errs); len(got) != 0 {
			t.Errorf("progress %q, want none", got)
		}
	})
	t.Run("a malformed turn_start", func(t *testing.T) {
		noBotMode(t)
		setTTY(t, false, false)
		errs, err := chatOneShotFrames(t, []string{
			frame(1, "turn_start", `"mode":"council","roster":"review"`),
			frame(2, "final", `"text":"done"`),
			frame(3, "turn_end", `"state":"done"`),
		})
		if err == nil || exitCodeOf(err) != codeError || !strings.HasPrefix(err.Error(), "malformed chat event: ") {
			t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
		}
	})
}

// The names live for one turn: in a conversation, the next turn shows the slugs again.
func TestChatREPLNamesPerTurn(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, true)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	p.on("POST", "/bots/scout/chat/threads", 201, chatThreadNew)
	p.on("POST", "/bots/scout/chat/threads/thr_new/turns", 202, chatTurnBody)
	p.on("GET", "/bots/scout/chat/threads/thr_new", 200,
		`{"thread":{"id":"thr_new","title":"t","origin":"cli","createdAt":1,"updatedAt":1,"turnCount":1,"lastTurn":null},"turns":[],"lastSeq":3}`)
	p.onStream("/bots/scout/chat/threads/thr_new/events", func(w http.ResponseWriter, r *http.Request, conn int) {
		if conn == 1 {
			writeFrames(w, []string{
				frame(1, "member_start", `"member":"fleet","name":"Fleet","text":"look"`),
				frame(2, "final", `"text":"one"`),
				frame(3, "turn_end", `"state":"done"`),
			})
			return
		}
		writeFrames(w, []string{
			frame(4, "tool_call", `"member":"fleet","id":"c1","tool":"robots.list","input":{}`),
			frame(5, "final", `"text":"two"`),
			frame(6, "turn_end", `"state":"done"`),
		})
	})
	oldIn := cliStdin
	cliStdin = strings.NewReader("hello\nagain\n/quit\n")
	t.Cleanup(func() { cliStdin = oldIn })
	_, errs, err := runCLI(t, p.env(t), "chat", "scout")
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if got, want := chatProgressLines(errs), []string{"· Fleet: look\n", "· fleet → robots.list\n"}; !reflect.DeepEqual(got, want) {
		t.Errorf("progress %q, want %q", got, want)
	}
}

// The COUNCIL cell: an empty member is no member chat, a council without a name is the default, a
// member chat shows its member even when the thread also names a council, and the hub's names are
// escaped.
func TestChatThreadsCouncilCellEdges(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat/threads", 200, `{"items":[
{"id":"thr_1","title":"one","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"member":"","roster":{"id":"ros_2","name":"ops","displayName":"Ops"}},
{"id":"thr_2","title":"two","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"member":"tri\u0007age"},
{"id":"thr_3","title":"three","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"roster":{"id":"ros_3","name":"re\u0007view","displayName":"R"}},
{"id":"thr_4","title":"four","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"member":null,"roster":{"id":"ros_4","name":"","displayName":""}},
{"id":"thr_5","title":"five","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"roster":{"id":"ros_5","name":"a\tb","displayName":""}},
{"id":"thr_6","title":"six","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"member":"x\ty"},
{"id":"thr_7","title":"seven","origin":"cli","createdAt":1,"updatedAt":1794391881385,"turnCount":1,"lastTurn":null,"member":"triage","roster":{"id":"ros_7","name":"ops","displayName":"Ops"}}]}`)
	out, _, err := runCLI(t, p.env(t), "chat", "threads", "scout")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 8 {
		t.Fatalf("lines %q", lines)
	}
	for i, want := range []string{"ops", `@tri\u0007age`, `re\u0007view`, "(default)", `a\tb`, `@x\ty`, "@triage"} {
		if got := strings.Fields(lines[i+1]); len(got) < 3 || got[2] != want {
			t.Errorf("row %d %q: the council cell is not %q", i+1, got, want)
		}
	}
}

// chatCouncilName: the display name and the name when they read differently, one of them otherwise.
func TestChatCouncilName(t *testing.T) {
	cases := []struct{ name, display, want string }{
		{"review", "Review", "Review (review)"},
		{"review", "review", "review"},
		{"review", "", "review"},
		{"", "Display", "Display"},
		{"", "", ""},
		{"a  b", "a  b", "a  b"},
	}
	for _, c := range cases {
		if got := chatCouncilName(c.name, c.display); got != c.want {
			t.Errorf("chatCouncilName(%q, %q) = %q, want %q", c.name, c.display, got, c.want)
		}
	}
}

// --thorough on a council chat continued with --thread asks the turn: only a chat with one member
// refuses it.
func TestChatThoroughOnACouncilThread(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("GET", "/bots/scout/chat", 200, chatInfoBody)
	chatOldThread(p, chatThreadDetail("thr_old", 42))
	if _, errs, err := runCLI(t, p.env(t), "chat", "scout", "--thread", "thr_old", "--thorough", "hello bot"); err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	if n := len(p.seen("POST", "/bots/scout/chat/threads")); n != 0 {
		t.Errorf("%d threads were created for --thread", n)
	}
	turns := p.seen("POST", "/bots/scout/chat/threads/thr_old/turns")
	if len(turns) != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	want := map[string]any{"prompt": "hello bot", "mode": "council", "thorough": true}
	if got := chatBodyMap(t, turns[0].Body); !reflect.DeepEqual(got, want) {
		t.Errorf("turn body %v, want %v", got, want)
	}
}

// Without a name a line shows the member's slug, cleaned; a name sent without a member is not
// remembered, so a later event without a member keeps today's text.
func TestChatProgressSlugFallback(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	errs, err := chatOneShotFrames(t, []string{
		frame(1, "tool_call", `"member":"fl\teet","id":"c1","tool":"robots.list","input":{}`),
		frame(2, "member_start", `"name":"Ghost","text":"look"`),
		frame(3, "critique", `"issues":[]`),
		frame(4, "final", `"text":"done"`),
		frame(5, "turn_end", `"state":"done"`),
	})
	if err != nil {
		t.Fatalf("exit %d: %v\n%s", exitCodeOf(err), err, errs)
	}
	want := []string{"· fl eet → robots.list\n", "· Ghost: look\n", "· critic: 0 issues (0 blocking)\n"}
	if got := chatProgressLines(errs); !reflect.DeepEqual(got, want) {
		t.Errorf("progress\n%q\nwant\n%q", got, want)
	}
}
