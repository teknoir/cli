package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const draftSpec = `{"displayName":"Stream watcher","persona":null,"description":"watches MQTT streams for gaps.","instructions":"Watch.","kind":"member","model":"haiku","effort":"medium","tools":["core_read","metrics","mqtt","results"],"skills":["teknoir-mqtt-streams"],"accessCeiling":"read","budget":{"maxToolRounds":8,"maxTokens":80000},"category":"operations","templateRef":{"name":"stream-analyst","version":1}}`

// draftAnswer is the draft route's answer with these effective tools (raw JSON: null, [] or a list).
func draftAnswer(effective string) string {
	return `{"slug":"stream-watcher","spec":` + draftSpec + `,"startsFrom":{"slug":"stream-analyst","name":"Stream Analyst"},"effectiveTools":` + effective +
		`,"warnings":[{"path":"spec.effort","message":"Effort lowered to medium: the platform allows at most medium."}]}`
}

var putOK = `{"role":` + roleViewJSON("stream-watcher", "Stream watcher", "bot", "member", "ok", []string{"core_read"}, nil) + `,"action":"created"}`

const draftLines = "member:      stream-watcher\n" +
	"name:        Stream watcher\n" +
	"starts from: Stream Analyst (stream-analyst)\n" +
	"model:       haiku, effort medium\n" +
	"tools:       core_read, metrics, mqtt, results\n"

var newArgs = []string{"role", "new", "Stream watcher", "--job", "watches MQTT streams for gaps"}

// newArgsWith is newArgs with more arguments after it.
func newArgsWith(extra ...string) []string {
	return append(append([]string{}, newArgs...), extra...)
}

// draftRoute answers the draft of a member with answer.
func draftRoute(p *chatAPI, answer string) {
	p.on("POST", "/council/roles/draft", 200, answer)
}

// TestCouncilRoleNewBot drafts a bot's own member, prints the draft, then saves it.
func TestCouncilRoleNewBot(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	draftRoute(p, draftAnswer(`["mqtt_sample","metrics_query"]`))
	p.on("PUT", "/bots/scout/council/roles/stream-watcher", 200, putOK)

	out, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	wantReqs(t, p, "POST /council/roles/draft", "PUT /bots/scout/council/roles/stream-watcher")
	compactEq(t, "draft body", p.seen("POST", "/council/roles/draft")[0].Body,
		`{"name":"Stream watcher","job":"watches MQTT streams for gaps","bot":"scout","scope":"bot"}`)
	compactEq(t, "PUT body", p.seen("PUT", "/bots/scout/council/roles/stream-watcher")[0].Body,
		`{"spec":`+draftSpec+`,"expectVersion":0}`)
	want := draftLines + "effective:   mqtt_sample, metrics_query\n" + "created stream-watcher (version 1) for scout\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	wantErr := "warning: spec.effort: Effort lowered to medium: the platform allows at most medium.\n" +
		"· try it: tnctl chat scout --member stream-watcher \"…\"\n"
	if errs != wantErr {
		t.Errorf("stderr:\n%q\nwant:\n%q", errs, wantErr)
	}

	for _, c := range []struct {
		name      string
		effective string
		want      string
	}{
		{"not reported", "null", "effective:   not checked yet\n"},
		{"none", "[]", "effective:   none\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newChatAPI(t)
			draftRoute(p, draftAnswer(c.effective))
			p.on("PUT", "/bots/scout/council/roles/stream-watcher", 200, putOK)
			out, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if want := draftLines + c.want + "created stream-watcher (version 1) for scout\n"; out != want {
				t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
			}
		})
	}
}

// TestCouncilRoleNewTeamspace drafts a shared member: --how and --from name the text and the start.
func TestCouncilRoleNewTeamspace(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	draftRoute(p, draftAnswer("null"))
	p.on("PUT", "/council/roles/stream-watcher", 200, putOK)

	out, errs, err := councilArgs(t, p.env(t), newArgsWith("--teamspace", "--how", "Watch it.", "--from", "analyst")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	wantReqs(t, p, "POST /council/roles/draft", "PUT /council/roles/stream-watcher")
	compactEq(t, "draft body", p.seen("POST", "/council/roles/draft")[0].Body,
		`{"name":"Stream watcher","job":"watches MQTT streams for gaps","how":"Watch it.","from":"analyst","scope":"teamspace"}`)
	want := "effective:   not known without a bot\ncreated stream-watcher (version 1), shared in teamspace teknoir-demo\n"
	if !strings.HasSuffix(out, want) {
		t.Errorf("stdout:\n%q\nwant it to end with %q", out, want)
	}
	if wantErr := "· try it: tnctl chat <bot> --member teamspace/stream-watcher \"…\"\n"; !strings.HasSuffix(errs, wantErr) {
		t.Errorf("stderr %q, want it to end with %q", errs, wantErr)
	}
}

// TestCouncilRoleNewDryRun prints the draft and saves nothing.
func TestCouncilRoleNewDryRun(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	draftRoute(p, draftAnswer("null"))

	out, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout", "--dry-run")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	wantReqs(t, p, "POST /council/roles/draft")
	if want := draftLines + "effective:   not checked yet\n"; out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	if !strings.HasSuffix(errs, "· dry run: nothing was written\n") {
		t.Errorf("stderr %q", errs)
	}

	p = newChatAPI(t)
	draftRoute(p, draftAnswer("null"))
	out, errs, err = councilArgs(t, p.env(t), "-o", "json", "role", "new", "Stream watcher",
		"--job", "watches MQTT streams for gaps", "--bot", "scout", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(draftAnswer("null")), "", "  "); err != nil {
		t.Fatal(err)
	}
	if want := indented.String() + "\n"; out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	for _, part := range []string{"warning: spec.effort:", "· dry run: nothing was written\n"} {
		if !strings.Contains(errs, part) {
			t.Errorf("stderr %q, want %q in it", errs, part)
		}
	}
}

// TestCouncilRoleNewJSON prints the saved member instead of the draft's lines.
func TestCouncilRoleNewJSON(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	draftRoute(p, draftAnswer("null"))
	p.on("PUT", "/bots/scout/council/roles/stream-watcher", 200, putOK)

	out, errs, err := councilArgs(t, p.env(t), "-o", "json", "role", "new", "Stream watcher",
		"--job", "watches MQTT streams for gaps", "--bot", "scout")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(putOK), "", "  "); err != nil {
		t.Fatal(err)
	}
	if want := indented.String() + "\n"; out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	for _, part := range []string{"warning: spec.effort:", "· try it: tnctl chat scout --member stream-watcher"} {
		if !strings.Contains(errs, part) {
			t.Errorf("stderr %q, want %q in it", errs, part)
		}
	}
}

// TestCouncilRoleNewUsage refuses a command line it cannot send, before any request.
func TestCouncilRoleNewUsage(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	notUTF8 := councilFile(t, "F\xe4sser\n")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"role", "new", "--job", "x", "--bot", "scout"}, "council role new needs one name"},
		{[]string{"role", "new", "a", "b", "--job", "x", "--bot", "scout"}, "council role new needs one name"},
		{[]string{"role", "new", "X", "--bot", "scout"}, "council role new needs --job"},
		{[]string{"role", "new", "X", "--job", "x"}, "council role new needs --bot <bot> (a member of that bot) or --teamspace"},
		{[]string{"role", "new", "X", "--job", "x", "--teamspace=false"}, "council role new needs --bot <bot> (a member of that bot) or --teamspace"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--teamspace"}, "not both"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--how", "a", "--how-file", "f"}, "use --how or --how-file, not both"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", ""}, `"" is not a bot name`},
		{[]string{"role", "new", "X", "--job", "x", "--bot", ".."}, `".." is not a bot name`},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--how-file", ""}, "--how-file needs a file"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--how-file", "/nonexistent/h.md"}, "cannot read /nonexistent/h.md"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--how-file", notUTF8}, "is not UTF-8 text"},
		{[]string{"role", "new", "X", "--job", "F\xe4sser", "--bot", "scout"}, "--job is not UTF-8 text"},
		{[]string{"role", "new", "F\xe4", "--job", "x", "--bot", "scout"}, "the name is not UTF-8 text"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--how", "F\xe4sser"}, "--how is not UTF-8 text"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--from", "F\xe4"}, "--from is not UTF-8 text"},
		{[]string{"-o", "xml", "role", "new", "X", "--job", "x", "--bot", "scout"}, "unknown output format"},
		// Two faults: the earlier step of the order wins.
		{[]string{"-o", "xml", "role", "new", "X"}, "unknown output format"},
		{[]string{"role", "new", "X"}, "council role new needs --job"},
		{[]string{"role", "new", "X", "--job", "x", "--how", "a", "--how-file", "f"}, "use --how or --how-file, not both"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "..", "--teamspace"}, "not both"},
		{[]string{"role", "new", "F\xe4", "--job", "x", "--bot", ".."}, `".." is not a bot name`},
		{[]string{"role", "new", "F\xe4", "--job", "F\xe4sser", "--bot", "scout"}, "the name is not UTF-8 text"},
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--from", "F\xe4", "--how-file", "/nonexistent/h.md"}, "--from is not UTF-8 text"},
		// --how and --how-file are both given even when --how is empty.
		{[]string{"role", "new", "X", "--job", "x", "--bot", "scout", "--how", "", "--how-file", notUTF8}, "use --how or --how-file, not both"},
	} {
		_, _, err := councilArgs(t, p.env(t), c.args...)
		if err == nil {
			t.Fatalf("%v: no error", c.args)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("%v: exit %d, want %d", c.args, code, codeUsage)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: error %q, want %q in it", c.args, err, c.want)
		}
	}
	wantReqs(t, p)
}

// TestCouncilRoleNewEmptyJobIsSent: --job "" is given; the platform says what is missing.
func TestCouncilRoleNewEmptyJobIsSent(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("POST", "/council/roles/draft", 400, councilErrBody("invalid", "a job is required",
		`"fields":[{"path":"job","message":"a job is required"}]`))

	_, _, err := councilArgs(t, p.env(t), "role", "new", "X", "--job", "", "--bot", "scout")
	if err == nil || err.Error() != "job: a job is required" {
		t.Fatalf("err %v", err)
	}
	if code := exitCodeOf(err); code != codeRejected {
		t.Errorf("exit %d, want %d", code, codeRejected)
	}
	wantReqs(t, p, "POST /council/roles/draft")
	compactEq(t, "draft body", p.seen("POST", "/council/roles/draft")[0].Body, `{"name":"X","job":"","bot":"scout","scope":"bot"}`)
}

// TestCouncilRoleNewHowFile reads the instructions from a file and from stdin, byte for byte.
func TestCouncilRoleNewHowFile(t *testing.T) {
	noBotMode(t)
	const howText = "First line.\r\nSecond line.\n"
	for _, c := range []struct {
		name    string
		howFile string
		stdin   string
		want    string
	}{
		{"a file", councilFile(t, howText), "", howText},
		{"stdin", "-", "From stdin.\n", "From stdin.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, false, false)
			setStdin(t, c.stdin)
			p := newChatAPI(t)
			draftRoute(p, draftAnswer("null"))
			p.on("PUT", "/bots/scout/council/roles/stream-watcher", 200, putOK)

			_, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout", "--how-file", c.howFile)...)
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			posts := p.seen("POST", "/council/roles/draft")
			if len(posts) != 1 {
				t.Fatalf("%d drafts, want 1", len(posts))
			}
			if got := bodyString(t, posts[0].Body, "how"); got != c.want {
				t.Errorf("how %q, want %q", got, c.want)
			}
		})
	}
}

// TestCouncilRoleNewConfirmOnPut: the PUT is the write that may ask, as apply's is.
func TestCouncilRoleNewConfirmOnPut(t *testing.T) {
	noBotMode(t)
	const path = "/bots/scout/council/roles/stream-watcher"
	asked := councilErrBody("confirm_needed", "This member can read pods and logs.", `"needs":["inspect"]`)

	t.Run("yes on a terminal", func(t *testing.T) {
		setTTY(t, true, false)
		setStdin(t, "y\n")
		p := newChatAPI(t)
		draftRoute(p, draftAnswer("null"))
		p.onSeq("PUT", path, chatReply{409, asked}, chatReply{200, putOK})

		out, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
		if err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		puts := p.seen("PUT", path)
		if len(puts) != 2 {
			t.Fatalf("%d PUTs, want 2", len(puts))
		}
		compactEq(t, "first PUT", puts[0].Body, `{"spec":`+draftSpec+`,"expectVersion":0}`)
		compactEq(t, "second PUT", puts[1].Body, `{"spec":`+draftSpec+`,"expectVersion":0,"confirm":["inspect"]}`)
		if !strings.Contains(errs, "This member can read pods and logs. Continue? [y/N] ") {
			t.Errorf("stderr %q", errs)
		}
		if strings.Contains(errs, "Document") {
			t.Errorf("stderr names a document: %q", errs)
		}
		if !strings.HasSuffix(out, "created stream-watcher (version 1) for scout\n") {
			t.Errorf("stdout %q", out)
		}
	})

	t.Run("no terminal", func(t *testing.T) {
		setTTY(t, false, false)
		p := newChatAPI(t)
		draftRoute(p, draftAnswer("null"))
		p.onSeq("PUT", path, chatReply{409, asked}, chatReply{200, putOK})

		_, _, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
		if err == nil {
			t.Fatal("a confirmation tnctl cannot ask for was written")
		}
		want := "This member can read pods and logs. Nothing was written: pass --yes to confirm: inspect"
		if err.Error() != want {
			t.Errorf("error %q, want %q", err, want)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("exit %d, want %d", code, codeUsage)
		}
		if n := len(p.seen("PUT", path)); n != 1 {
			t.Errorf("%d PUTs, want 1", n)
		}
	})

	t.Run("how from stdin", func(t *testing.T) {
		setTTY(t, true, false)
		setStdin(t, "Watch it.\n")
		p := newChatAPI(t)
		draftRoute(p, draftAnswer("null"))
		p.onSeq("PUT", path, chatReply{409, asked}, chatReply{200, putOK})

		_, _, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout", "--how-file", "-")...)
		if err == nil || exitCodeOf(err) != codeUsage {
			t.Fatalf("err %v (exit %d)", err, exitCodeOf(err))
		}
		if n := len(p.seen("PUT", path)); n != 1 {
			t.Errorf("%d PUTs, want 1", n)
		}
		posts := p.seen("POST", "/council/roles/draft")
		if len(posts) != 1 {
			t.Fatalf("%d drafts, want 1", len(posts))
		}
		if got := bodyString(t, posts[0].Body, "how"); got != "Watch it.\n" {
			t.Errorf("how %q", got)
		}
	})

	t.Run("yes", func(t *testing.T) {
		setTTY(t, false, false)
		p := newChatAPI(t)
		draftRoute(p, draftAnswer("null"))
		p.on("PUT", path, 200, putOK)

		if _, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout", "--yes")...); err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		puts := p.seen("PUT", path)
		if len(puts) != 1 {
			t.Fatalf("%d PUTs, want 1", len(puts))
		}
		compactEq(t, "PUT", puts[0].Body, `{"spec":`+draftSpec+`,"expectVersion":0,"confirm":["inspect","creator"]}`)
	})

	t.Run("no on a terminal", func(t *testing.T) {
		setTTY(t, true, false)
		setStdin(t, "n\n")
		p := newChatAPI(t)
		draftRoute(p, draftAnswer("null"))
		p.onSeq("PUT", path, chatReply{409, asked}, chatReply{200, putOK})

		_, _, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
		if err == nil || err.Error() != "not confirmed: nothing was written" {
			t.Fatalf("err %v", err)
		}
		if code := exitCodeOf(err); code != codeError {
			t.Errorf("exit %d, want %d", code, codeError)
		}
		if n := len(p.seen("PUT", path)); n != 1 {
			t.Errorf("%d PUTs, want 1", n)
		}
	})
}

// TestCouncilRoleNewExists: someone took the slug between the draft and the PUT.
func TestCouncilRoleNewExists(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	draftRoute(p, draftAnswer("null"))
	p.onSeq("PUT", "/bots/scout/council/roles/stream-watcher",
		chatReply{409, councilErrBody("exists", "A member 'stream-watcher' exists already. Open it, or use 'stream-watcher-2'.", "")},
		chatReply{200, putOK})

	_, _, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
	if err == nil {
		t.Fatal("the taken slug was ignored")
	}
	want := "A member 'stream-watcher' exists already. Open it, or use 'stream-watcher-2'.\nrun the command again: the platform picks a free name"
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeConflict {
		t.Errorf("exit %d, want %d", code, codeConflict)
	}
	if n := len(p.seen("POST", "/council/roles/draft")); n != 1 {
		t.Errorf("%d drafts, want 1", n)
	}
	if n := len(p.seen("PUT", "/bots/scout/council/roles/stream-watcher")); n != 1 {
		t.Errorf("%d PUTs, want 1", n)
	}
}

// TestCouncilRoleNewExistsAfterConfirm: the slug is taken while the person confirms; the hint still
// says how to go on.
func TestCouncilRoleNewExistsAfterConfirm(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, false)
	setStdin(t, "y\n")
	const path = "/bots/scout/council/roles/stream-watcher"
	p := newChatAPI(t)
	draftRoute(p, draftAnswer("null"))
	p.onSeq("PUT", path,
		chatReply{409, councilErrBody("confirm_needed", "This member can read pods and logs.", `"needs":["inspect"]`)},
		chatReply{409, councilErrBody("exists", "A member 'stream-watcher' exists already. Open it, or use 'stream-watcher-2'.", "")},
		chatReply{200, putOK})

	_, _, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
	want := "A member 'stream-watcher' exists already. Open it, or use 'stream-watcher-2'.\nrun the command again: the platform picks a free name"
	if err == nil || err.Error() != want {
		t.Fatalf("err %v, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeConflict {
		t.Errorf("exit %d, want %d", code, codeConflict)
	}
	wantReqs(t, p, "POST /council/roles/draft", "PUT "+path, "PUT "+path)
}

// TestCouncilRoleNewNotAdmin keeps the hub's own words for a shared member.
func TestCouncilRoleNewNotAdmin(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	draftRoute(p, draftAnswer("null"))
	p.onSeq("PUT", "/council/roles/stream-watcher",
		chatReply{403, councilErrBody("not_admin", "Only teamspace admins can change shared members.", "")},
		chatReply{200, putOK})

	out, _, err := councilArgs(t, p.env(t), newArgsWith("--teamspace")...)
	if err == nil {
		t.Fatal("the refusal was ignored")
	}
	if want := "Only teamspace admins can change shared members."; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeAuth {
		t.Errorf("exit %d, want %d", code, codeAuth)
	}
	wantReqs(t, p, "POST /council/roles/draft", "PUT /council/roles/stream-watcher")
	if !strings.HasPrefix(out, draftLines) {
		t.Errorf("stdout %q", out)
	}
}

// TestCouncilRoleNewDraftRefused sends the draft's refusal as it is, and no PUT.
func TestCouncilRoleNewDraftRefused(t *testing.T) {
	noBotMode(t)
	for _, c := range []struct {
		name   string
		status int
		body   string
		exit   int
		want   string
	}{
		{"no job", 400, councilErrBody("invalid", "a job is required",
			`"fields":[{"path":"job","message":"a job is required"}]`), codeRejected, "job: a job is required"},
		{"bad start", 400, councilErrBody("invalid", "'coder' cannot be a start",
			`"fields":[{"path":"from","message":"'coder' cannot be a start"}]`), codeRejected, "from: 'coder' cannot be a start"},
		{"no bot", 404, councilErrBody("not_found", "No bot named scout in teamspace teknoir-demo.", ""),
			codeNotFound, "No bot named scout in teamspace teknoir-demo."},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("POST", "/council/roles/draft", c.status, c.body)

			out, _, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
			if err == nil {
				t.Fatal("the refusal was ignored")
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d (%v)", code, c.exit, err)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if out != "" {
				t.Errorf("stdout %q", out)
			}
			wantReqs(t, p, "POST /council/roles/draft")
		})
	}
}

// TestCouncilRoleNewDraftChecks refuses a draft whose slug or spec tnctl cannot send back.
func TestCouncilRoleNewDraftChecks(t *testing.T) {
	noBotMode(t)
	startsFrom := `"startsFrom":{"slug":"stream-analyst","name":"Stream Analyst"},"effectiveTools":null,"warnings":[]`
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{"slug ..", `{"slug":"..","spec":` + draftSpec + `,` + startsFrom + `}`, `the platform drafted a slug tnctl cannot use ("..")`},
		{"slug with a slash", `{"slug":"a/b","spec":` + draftSpec + `,` + startsFrom + `}`, `the platform drafted a slug tnctl cannot use ("a/b")`},
		{"spec null", `{"slug":"stream-watcher","spec":null,` + startsFrom + `}`, "the platform's draft holds no member spec: nothing was saved"},
		{"no spec", `{"slug":"stream-watcher",` + startsFrom + `}`, "the platform's draft holds no member spec: nothing was saved"},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.on("POST", "/council/roles/draft", 200, c.body)

			out, _, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
			if err == nil {
				t.Fatal("the draft was sent back")
			}
			if code := exitCodeOf(err); code != codeError {
				t.Errorf("exit %d, want %d", code, codeError)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q, want %q in it", err, c.want)
			}
			if out != "" {
				t.Errorf("stdout %q", out)
			}
			wantReqs(t, p, "POST /council/roles/draft")
		})
	}
}

// TestCouncilRoleArchive archives a bot's own member or the teamspace's shared one.
func TestCouncilRoleArchive(t *testing.T) {
	noBotMode(t)
	answer := roleAnswerJSON(roleViewJSON("triage", "Triage", "bot", "member", "ok", nil, nil))
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(answer), "", "  "); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		args   []string
		path   string
		stdout string
		stderr string
	}{
		{"a bot's own", []string{"role", "archive", "triage", "--bot", "scout"}, "/bots/scout/council/roles/triage/archive",
			"archived triage of scout\n",
			"· to bring it back: tnctl council role export bot/triage --bot scout > triage.yaml, then tnctl council apply -f triage.yaml\n"},
		{"a bot's own, by scope", []string{"role", "archive", "bot/triage", "--bot", "scout"}, "/bots/scout/council/roles/triage/archive",
			"archived triage of scout\n",
			"· to bring it back: tnctl council role export bot/triage --bot scout > triage.yaml, then tnctl council apply -f triage.yaml\n"},
		{"a shared member", []string{"role", "archive", "triage"}, "/council/roles/triage/archive",
			"archived shared member triage\n",
			"· to bring it back: tnctl council role export triage > triage.yaml, then tnctl council apply -f triage.yaml\n"},
		{"a shared member, by scope", []string{"role", "archive", "teamspace/triage"}, "/council/roles/triage/archive",
			"archived shared member triage\n",
			"· to bring it back: tnctl council role export triage > triage.yaml, then tnctl council apply -f triage.yaml\n"},
		{"json", []string{"-o", "json", "role", "archive", "triage", "--bot", "scout"}, "/bots/scout/council/roles/triage/archive",
			indented.String() + "\n",
			"· to bring it back: tnctl council role export bot/triage --bot scout > triage.yaml, then tnctl council apply -f triage.yaml\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("POST", c.path, 200, answer)

			out, errs, err := councilArgs(t, p.env(t), c.args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			wantReqs(t, p, "POST "+c.path)
			if got := p.seen("POST", c.path)[0].Body; got != "{}" {
				t.Errorf("body %q, want {}", got)
			}
			if out != c.stdout {
				t.Errorf("stdout %q, want %q", out, c.stdout)
			}
			if errs != c.stderr {
				t.Errorf("stderr %q, want %q", errs, c.stderr)
			}
		})
	}
}

// TestCouncilRoleArchiveInUse names the councils a member is in.
func TestCouncilRoleArchiveInUse(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.onSeq("POST", "/bots/scout/council/roles/triage/archive",
		chatReply{409, councilErrBody("role_in_use", "This member is in 3 councils: remove it from them first.",
			`"usedBy":[{"bot":"scout","roster":"ops-a","displayName":"Ops A"}],"hiddenCount":2`)},
		chatReply{200, roleAnswerJSON(roleViewJSON("triage", "Triage", "bot", "member", "ok", nil, nil))})

	_, _, err := councilArgs(t, p.env(t), "role", "archive", "triage", "--bot", "scout")
	if err == nil {
		t.Fatal("the member was archived")
	}
	want := "This member is in 3 councils: remove it from them first.\nused by scout (Ops A) and 2 more you cannot see"
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeRejected {
		t.Errorf("exit %d, want %d", code, codeRejected)
	}
	wantReqs(t, p, "POST /bots/scout/council/roles/triage/archive")
}

// TestCouncilRoleArchiveRefused maps each refusal, and points a bare slug's 404 at --bot.
func TestCouncilRoleArchiveRefused(t *testing.T) {
	noBotMode(t)
	const sharedGone = "This teamspace has no shared member 'gone', or it is archived already."
	const botGone = "scout has no member 'gone' of its own, or it is archived already."
	for _, c := range []struct {
		name   string
		args   []string
		path   string
		status int
		body   string
		exit   int
		want   string
	}{
		{"premade", []string{"role", "archive", "analyst"}, "/council/roles/analyst/archive", 400,
			councilErrBody("template_read_only", "Premade members and councils are read-only: give it a new name to clone it.", ""),
			codeRejected, "Premade members and councils are read-only: give it a new name to clone it."},
		{"shared 404", []string{"role", "archive", "gone"}, "/council/roles/gone/archive", 404,
			councilErrBody("not_found", sharedGone, ""), codeNotFound, sharedGone + "\na bot's own member needs --bot <bot>"},
		{"shared 404 by scope", []string{"role", "archive", "teamspace/gone"}, "/council/roles/gone/archive", 404,
			councilErrBody("not_found", sharedGone, ""), codeNotFound, sharedGone},
		{"bot 404", []string{"role", "archive", "gone", "--bot", "scout"}, "/bots/scout/council/roles/gone/archive", 404,
			councilErrBody("not_found", botGone, ""), codeNotFound,
			botGone + "\nif you meant the teamspace's shared member, leave out --bot: tnctl council role archive teamspace/gone"},
		{"bot 404 by scope", []string{"role", "archive", "bot/gone", "--bot", "scout"}, "/bots/scout/council/roles/gone/archive", 404,
			councilErrBody("not_found", botGone, ""), codeNotFound, botGone},
		{"not admin", []string{"role", "archive", "gone"}, "/council/roles/gone/archive", 403,
			councilErrBody("not_admin", "Only teamspace admins can change shared members.", ""),
			codeAuth, "Only teamspace admins can change shared members."},
		{"not the bot's editor", []string{"role", "archive", "gone", "--bot", "scout"}, "/bots/scout/council/roles/gone/archive", 403,
			councilErrBody("council_edit_not_allowed", "Only the bot's creator or a teamspace admin may change its council.", ""),
			codeAuth, "Only the bot's creator or a teamspace admin may change its council."},
	} {
		t.Run(c.name, func(t *testing.T) {
			noBotMode(t)
			p := newChatAPI(t)
			p.on("POST", c.path, c.status, c.body)

			_, _, err := councilArgs(t, p.env(t), c.args...)
			if err == nil {
				t.Fatal("the archive was accepted")
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d (%v)", code, c.exit, err)
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			wantReqs(t, p, "POST "+c.path)
		})
	}
}

// TestCouncilRoleArchiveScopeErrors refuses a scope and a --bot that contradict each other.
func TestCouncilRoleArchiveScopeErrors(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"role", "archive", "template/analyst"}, "premade members cannot be archived: they are read-only"},
		{[]string{"role", "archive", "template/analyst", "--bot", "scout"}, "premade members cannot be archived: they are read-only"},
		{[]string{"role", "archive", "bot/gone"}, "a bot's own member needs --bot <bot>"},
		{[]string{"role", "archive", "teamspace/gone", "--bot", "scout"}, "teamspace/gone is a shared member: leave out --bot to archive it"},
		{[]string{"role", "archive", ".."}, "is not a member"},
		{[]string{"role", "archive", "x"}, "is not a member"},
		{[]string{"role", "archive", "other/gone"}, "is not a member"},
		{[]string{"role", "archive", "gone", "--bot", ".."}, `".." is not a bot name`},
		{[]string{"role", "archive", "gone", "--bot", ""}, `"" is not a bot name`},
		{[]string{"role", "archive"}, "council role archive needs a member"},
		{[]string{"role", "archive", "a", "b"}, "council role archive needs a member"},
		{[]string{"-o", "xml", "role", "archive", "gone"}, "unknown output format"},
		// Two faults: the earlier step of the order wins.
		{[]string{"-o", "xml", "role", "archive", ".."}, "unknown output format"},
		{[]string{"role", "archive", "template/analyst", "--bot", ".."}, "premade members cannot be archived: they are read-only"},
		{[]string{"role", "archive", "teamspace/gone", "--bot", ".."}, "teamspace/gone is a shared member: leave out --bot to archive it"},
	} {
		_, _, err := councilArgs(t, p.env(t), c.args...)
		if err == nil {
			t.Fatalf("%v: no error", c.args)
		}
		if code := exitCodeOf(err); code != codeUsage {
			t.Errorf("%v: exit %d, want %d", c.args, code, codeUsage)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: error %q, want %q in it", c.args, err, c.want)
		}
	}
	wantReqs(t, p)
}

// TestCouncilRoleNewActionText prints what the platform says it did, on one line.
func TestCouncilRoleNewActionText(t *testing.T) {
	noBotMode(t)
	role := roleViewJSON("stream-watcher", "Stream watcher", "bot", "member", "ok", []string{"core_read"}, nil)
	for _, c := range []struct {
		action string
		want   string
	}{
		{"created\nwarning: fake", "effective:   not checked yet\ncreated warning: fake stream-watcher (version 1) for scout\n"},
		{"", "effective:   not checked yet\nsaved stream-watcher (version 1) for scout\n"},
	} {
		t.Run(c.action, func(t *testing.T) {
			setTTY(t, false, false)
			p := newChatAPI(t)
			draftRoute(p, draftAnswer("null"))
			p.on("PUT", "/bots/scout/council/roles/stream-watcher", 200, `{"role":`+role+`,"action":`+q(c.action)+`}`)

			out, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if !strings.HasSuffix(out, c.want) {
				t.Errorf("stdout %q, want it to end with %q", out, c.want)
			}
		})
	}
}

// TestCouncilRoleNewWarnings prints each warning of the draft on stderr, one line each: hubText on the
// message, escapeLine on the path, none for an empty message, and no "path: " without a path.
func TestCouncilRoleNewWarnings(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	draftRoute(p, `{"slug":"stream-watcher","spec":`+draftSpec+`,"startsFrom":{"slug":"stream-analyst","name":"Stream Analyst"},"effectiveTools":null,"warnings":[`+
		`{"path":"","message":"no\tpath\nhere"},{"path":"spec.model","message":""},{"path":"spec.a\nb","message":"lowered"}]}`)
	p.on("PUT", "/bots/scout/council/roles/stream-watcher", 200, putOK)

	_, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout")...)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "warning: no path here\nwarning: spec.a\\nb: lowered\n· try it: tnctl chat scout --member stream-watcher \"…\"\n"
	if errs != want {
		t.Errorf("stderr:\n%q\nwant:\n%q", errs, want)
	}
}

// TestCouncilRoleNewDraftLinesSafe prints the draft's words from the platform on one line each, and
// "-" for a value the draft lacks.
func TestCouncilRoleNewDraftLinesSafe(t *testing.T) {
	noBotMode(t)
	for _, c := range []struct{ name, spec, starts, want string }{
		{"empty", `{"displayName":"","tools":[]}`, `{"slug":"","name":""}`,
			"member:      stream-watcher\nname:        -\nstarts from: -\nmodel:       -, effort -\ntools:       -\neffective:   not checked yet\n"},
		{"folded", `{"displayName":"Stream\nwatcher","model":"hai\nku","effort":"med\nium","tools":["a\nb"]}`, `{"slug":"stream\nanalyst","name":"Stream\nAnalyst"}`,
			"member:      stream-watcher\nname:        Stream watcher\nstarts from: Stream Analyst (stream\\nanalyst)\nmodel:       hai ku, effort med ium\ntools:       a b\neffective:   not checked yet\n"},
		{"slug only", `{"displayName":"W"}`, `{"slug":"stream-analyst","name":""}`,
			"member:      stream-watcher\nname:        W\nstarts from: stream-analyst\nmodel:       -, effort -\ntools:       -\neffective:   not checked yet\n"},
		{"name only", `{"displayName":"W"}`, `{"slug":"","name":"Stream Analyst"}`,
			"member:      stream-watcher\nname:        W\nstarts from: Stream Analyst\nmodel:       -, effort -\ntools:       -\neffective:   not checked yet\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, false, false)
			p := newChatAPI(t)
			draftRoute(p, `{"slug":"stream-watcher","spec":`+c.spec+`,"startsFrom":`+c.starts+`,"effectiveTools":null,"warnings":[]}`)
			out, errs, err := councilArgs(t, p.env(t), newArgsWith("--bot", "scout", "--dry-run")...)
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if out != c.want {
				t.Errorf("stdout:\n%q\nwant:\n%q", out, c.want)
			}
			noTrailingSpaces(t, out)
			wantReqs(t, p, "POST /council/roles/draft")
		})
	}
}
