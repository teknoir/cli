package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// councilSub is a command's subcommand by name, nil when it has none.
func councilSub(c *cobra.Command, name string) *cobra.Command {
	for _, s := range c.Commands() {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// setStdin makes s the stdin of this test's next command. A reader is used up once: set it again
// before each command that reads stdin.
func setStdin(t *testing.T, s string) {
	t.Helper()
	old := cliStdin
	cliStdin = strings.NewReader(s)
	t.Cleanup(func() { cliStdin = old })
}

// councilFile writes text as a council file and returns its path.
func councilFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "council.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// bodyKeys is a request body's top-level keys and raw values.
func bodyKeys(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return m
}

// bodyString is a request body's top-level key as a string, "" when the key is absent.
func bodyString(t *testing.T, body, key string) string {
	t.Helper()
	var s string
	if raw, ok := bodyKeys(t, body)[key]; ok {
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatalf("%s of %q: %v", key, body, err)
		}
	}
	return s
}

// applyBody is an apply request's body, decoded.
type applyBody struct {
	YAML    string   `json:"yaml"`
	DryRun  *bool    `json:"dryRun"`
	Confirm []string `json:"confirm"`
}

const applyText = "kind: CouncilRole\nmetadata:\n  name: triage\n---\nkind: CouncilRole\nmetadata:\n  name: watcher\n"

const applyOK = `{"results":[{"index":0,"kind":"CouncilRole","name":"triage","scope":"teamspace","action":"created","version":1},` +
	`{"index":2,"kind":"CouncilRoster","name":"ops-a","scope":"bot","bot":"scout","action":"updated","version":3}]}`

const applyTable = "DOC  KIND           NAME    SCOPE      BOT    ACTION   VERSION\n" +
	"1    CouncilRole    triage  teamspace  -      created  1\n" +
	"3    CouncilRoster  ops-a   bot        scout  updated  3\n"

var (
	applyNeedsInspect = councilErrBody("confirm_needed", "This member can read pods and logs.", `"needs":["inspect"],"documents":[1]`)
	applyNeedsBoth    = councilErrBody("confirm_needed", "This member can read pods and logs. This member may act with the bot's full access (Act as me).", `"needs":["inspect","creator"],"documents":[0,2]`)
)

// TestConfirmDocs names a file's documents as the question does: from 1, and spelled out.
func TestConfirmDocs(t *testing.T) {
	for _, c := range []struct {
		docs   []int
		prefix string
		words  string
	}{
		{nil, "", ""},
		{[]int{1}, "Document 2: ", "document 2"},
		{[]int{1, 3}, "Documents 2 and 4: ", "documents 2 and 4"},
		{[]int{0, 1, 3}, "Documents 1, 2 and 4: ", "documents 1, 2 and 4"},
	} {
		if got := confirmDocs(c.docs); got != c.prefix {
			t.Errorf("confirmDocs(%v) = %q, want %q", c.docs, got, c.prefix)
		}
		if got := confirmDocWords(c.docs); got != c.words {
			t.Errorf("confirmDocWords(%v) = %q, want %q", c.docs, got, c.words)
		}
	}
}

// TestCouncilApplyTable applies a file without a question and prints one row per document.
func TestCouncilApplyTable(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("POST", "/council/apply", 200, applyOK)
	path := councilFile(t, applyText)

	out, errs, err := councilArgs(t, p.env(t), "apply", "-f", path)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != applyTable {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, applyTable)
	}
	noTrailingSpaces(t, out)
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}
	posts := p.seen("POST", "/council/apply")
	if len(posts) != 1 {
		t.Fatalf("%d POSTs, want 1", len(posts))
	}
	keys := bodyKeys(t, posts[0].Body)
	if len(keys) != 1 || keys["yaml"] == nil {
		t.Errorf("body keys: %v", keys)
	}
	wantReqs(t, p, "POST /council/apply")
}

// TestCouncilApplyConfirmOnATTY asks on the terminal and applies once the person says yes.
func TestCouncilApplyConfirmOnATTY(t *testing.T) {
	noBotMode(t)
	path := councilFile(t, applyText)
	for _, c := range []struct {
		name  string
		stdin string
		eol   string
	}{
		{"y", "y\n", ""},
		{"Y", "Y\n", ""},
		{"paragraph", " yes \n", ""},
		{"YES", "YES\n", ""},
		{"no newline", "y", "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, true, false)
			setStdin(t, c.stdin)
			p := newChatAPI(t)
			p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsInspect}, chatReply{200, applyOK})

			out, errs, err := councilArgs(t, p.env(t), "apply", "-f", path)
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if out != applyTable {
				t.Errorf("stdout:\n%q", out)
			}
			if want := "Document 2: This member can read pods and logs. Continue? [y/N] " + c.eol; errs != want {
				t.Errorf("stderr %q, want %q", errs, want)
			}
			posts := p.seen("POST", "/council/apply")
			if len(posts) != 2 {
				t.Fatalf("%d POSTs, want 2", len(posts))
			}
			first := bodyKeys(t, posts[0].Body)
			if _, ok := first["confirm"]; ok {
				t.Errorf("the first request confirms: %v", first)
			}
			if _, ok := first["dryRun"]; ok {
				t.Errorf("the first request is a dry run: %v", first)
			}
			var second applyBody
			if err := json.Unmarshal([]byte(posts[1].Body), &second); err != nil {
				t.Fatal(err)
			}
			if strings.Join(second.Confirm, ",") != "inspect" {
				t.Errorf("the second request confirms %v, want [inspect]", second.Confirm)
			}
			if second.YAML != applyText {
				t.Errorf("the second request's yaml %q", second.YAML)
			}
			if second.DryRun != nil {
				t.Errorf("the second request is a dry run")
			}
		})
	}
}

// TestCouncilApplyConfirmDeclined writes nothing when the answer is not yes.
func TestCouncilApplyConfirmDeclined(t *testing.T) {
	noBotMode(t)
	path := councilFile(t, applyText)
	for _, answer := range []string{"n\n", "\n", "no\n", "yy\n", "ja\n"} {
		t.Run(strings.TrimSpace(answer)+"end", func(t *testing.T) {
			setTTY(t, true, false)
			setStdin(t, answer)
			p := newChatAPI(t)
			p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsInspect}, chatReply{200, applyOK})

			out, _, err := councilArgs(t, p.env(t), "apply", "-f", path)
			if err == nil || err.Error() != "not confirmed: nothing was written" {
				t.Fatalf("err %v", err)
			}
			if code := exitCodeOf(err); code != codeError {
				t.Errorf("exit %d, want %d", code, codeError)
			}
			if out != "" {
				t.Errorf("stdout %q", out)
			}
			if n := len(p.seen("POST", "/council/apply")); n != 1 {
				t.Errorf("%d POSTs, want 1", n)
			}
		})
	}
}

// TestCouncilApplyConfirmEOF: end of input is not an answer.
func TestCouncilApplyConfirmEOF(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, false)
	setStdin(t, "")
	p := newChatAPI(t)
	p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsInspect}, chatReply{200, applyOK})
	path := councilFile(t, applyText)

	_, errs, err := councilArgs(t, p.env(t), "apply", "-f", path)
	if err == nil || err.Error() != "not confirmed: nothing was written" {
		t.Fatalf("err %v", err)
	}
	if code := exitCodeOf(err); code != codeError {
		t.Errorf("exit %d, want %d", code, codeError)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 1 {
		t.Errorf("%d POSTs, want 1", n)
	}
	if want := "Document 2: This member can read pods and logs. Continue? [y/N] \n"; errs != want {
		t.Errorf("stderr %q, want %q", errs, want)
	}
}

// TestCouncilApplyWithoutATTY cannot ask: it writes nothing and says how to confirm.
func TestCouncilApplyWithoutATTY(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsBoth}, chatReply{200, applyOK})
	path := councilFile(t, applyText)

	_, errs, err := councilArgs(t, p.env(t), "apply", "-f", path)
	if err == nil {
		t.Fatal("a confirmation tnctl cannot ask for was written")
	}
	want := "Documents 1 and 3: This member can read pods and logs. This member may act with the bot's full access (Act as me). Nothing was written: pass --yes to confirm: inspect, creator"
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeUsage {
		t.Errorf("exit %d, want %d", code, codeUsage)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 1 {
		t.Errorf("%d POSTs, want 1", n)
	}
	if strings.Contains(errs, "Continue?") {
		t.Errorf("stderr asks a question: %q", errs)
	}
}

// TestCouncilApplyStdinFileNeverPrompts: -f - gives the command its input, so it cannot ask.
func TestCouncilApplyStdinFileNeverPrompts(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, false)
	setStdin(t, applyText)
	p := newChatAPI(t)
	p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsInspect}, chatReply{200, applyOK})

	_, errs, err := councilArgs(t, p.env(t), "apply", "-f", "-")
	if err == nil || exitCodeOf(err) != codeUsage {
		t.Fatalf("err %v (exit %d)", err, exitCodeOf(err))
	}
	posts := p.seen("POST", "/council/apply")
	if len(posts) != 1 {
		t.Fatalf("%d POSTs, want 1", len(posts))
	}
	if got := bodyString(t, posts[0].Body, "yaml"); got != applyText {
		t.Errorf("yaml %q, want %q", got, applyText)
	}
	if strings.Contains(errs, "Continue?") {
		t.Errorf("stderr asks a question: %q", errs)
	}
}

// TestCouncilApplyYes confirms everything up front, with one request.
func TestCouncilApplyYes(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("POST", "/council/apply", 200, applyOK)
	path := councilFile(t, applyText)

	out, errs, err := councilArgs(t, p.env(t), "apply", "-f", path, "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != applyTable {
		t.Errorf("stdout:\n%q", out)
	}
	posts := p.seen("POST", "/council/apply")
	if len(posts) != 1 {
		t.Fatalf("%d POSTs, want 1", len(posts))
	}
	var body applyBody
	if err := json.Unmarshal([]byte(posts[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.Confirm, ",") != "inspect,creator" {
		t.Errorf("confirm %v", body.Confirm)
	}

	out, errs, err = councilArgs(t, p.env(t), "apply", "-f", path, "--yes", "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != applyTable {
		t.Errorf("--dry-run stdout:\n%q", out)
	}
	if want := "· dry run: nothing was written\n"; errs != want {
		t.Errorf("--dry-run stderr %q, want %q", errs, want)
	}
	posts = p.seen("POST", "/council/apply")
	if len(posts) != 2 {
		t.Fatalf("%d POSTs, want 2", len(posts))
	}
	body = applyBody{}
	if err := json.Unmarshal([]byte(posts[1].Body), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.Confirm, ",") != "inspect,creator" {
		t.Errorf("--dry-run confirm %v", body.Confirm)
	}
	if body.DryRun == nil || !*body.DryRun {
		t.Errorf("--dry-run body has no dryRun: %s", posts[1].Body)
	}
}

// TestCouncilApplyYesAskedAgain: --yes cannot answer a second question.
func TestCouncilApplyYesAskedAgain(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, false)
	p := newChatAPI(t)
	p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsInspect}, chatReply{200, applyOK})
	path := councilFile(t, applyText)

	_, errs, err := councilArgs(t, p.env(t), "apply", "-f", path, "--yes")
	if err == nil {
		t.Fatal("the platform's second question was ignored")
	}
	want := "the platform asked to confirm (inspect) although --yes confirmed inspect, creator: nothing was written; to answer its question, run the command on a terminal without --yes, with files instead of - for stdin"
	if err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeError {
		t.Errorf("exit %d, want %d", code, codeError)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 1 {
		t.Errorf("%d POSTs, want 1", n)
	}
	if strings.Contains(errs, "Continue?") {
		t.Errorf("stderr asks a question: %q", errs)
	}
}

// TestCouncilApplyDryRunConfirmsItself previews, confirming by itself without asking.
func TestCouncilApplyDryRunConfirmsItself(t *testing.T) {
	noBotMode(t)
	for _, tty := range []bool{false, true} {
		name := "no tty"
		if tty {
			name = "tty"
		}
		t.Run(name, func(t *testing.T) {
			setTTY(t, tty, false)
			setStdin(t, "")
			p := newChatAPI(t)
			p.onSeq("POST", "/council/apply",
				chatReply{409, applyNeedsBoth}, chatReply{200, applyOK}, chatReply{200, applyOK})
			path := councilFile(t, applyText)

			out, errs, err := councilArgs(t, p.env(t), "apply", "-f", path, "--dry-run")
			if err != nil {
				t.Fatalf("%v\n%s", err, errs)
			}
			if out != applyTable {
				t.Errorf("stdout:\n%q", out)
			}
			want := "· the real apply asks to confirm: inspect, creator, for documents 1 and 3 (on a terminal it asks, unless -f - gives the file; otherwise pass --yes)\n· dry run: nothing was written\n"
			if errs != want {
				t.Errorf("stderr:\n%q\nwant:\n%q", errs, want)
			}
			posts := p.seen("POST", "/council/apply")
			if len(posts) != 2 {
				t.Fatalf("%d POSTs, want 2", len(posts))
			}
			first := applyBody{}
			if err := json.Unmarshal([]byte(posts[0].Body), &first); err != nil {
				t.Fatal(err)
			}
			if first.DryRun == nil || !*first.DryRun {
				t.Errorf("the first request is not a dry run: %s", posts[0].Body)
			}
			if len(first.Confirm) != 0 {
				t.Errorf("the first request confirms %v", first.Confirm)
			}
			second := applyBody{}
			if err := json.Unmarshal([]byte(posts[1].Body), &second); err != nil {
				t.Fatal(err)
			}
			if second.DryRun == nil || !*second.DryRun {
				t.Errorf("the second request is not a dry run: %s", posts[1].Body)
			}
			if strings.Join(second.Confirm, ",") != "inspect,creator" {
				t.Errorf("the second request confirms %v", second.Confirm)
			}
		})
	}
}

// TestCouncilApplyAsksOnce asks at most once: a second question is a conflict.
func TestCouncilApplyAsksOnce(t *testing.T) {
	noBotMode(t)
	for _, c := range []struct {
		name string
		args []string
		tty  bool
		in   string
	}{
		{"on a terminal", nil, true, "y\n"},
		{"a dry run", []string{"--dry-run"}, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, c.tty, false)
			setStdin(t, c.in)
			p := newChatAPI(t)
			p.onSeq("POST", "/council/apply",
				chatReply{409, applyNeedsInspect}, chatReply{409, applyNeedsBoth}, chatReply{200, applyOK})
			path := councilFile(t, applyText)

			_, _, err := councilArgs(t, p.env(t), append([]string{"apply", "-f", path}, c.args...)...)
			if err == nil {
				t.Fatal("the platform's second question was ignored")
			}
			want := "the platform asked again to confirm (inspect, creator): someone changed a member meanwhile; run the command again"
			if err.Error() != want {
				t.Errorf("error %q, want %q", err, want)
			}
			if code := exitCodeOf(err); code != codeConflict {
				t.Errorf("exit %d, want %d", code, codeConflict)
			}
			if n := len(p.seen("POST", "/council/apply")); n != 2 {
				t.Errorf("%d POSTs, want 2", n)
			}
		})
	}
}

// TestCouncilApplyRefusedAfterConfirm prints the platform's refusal of the second request.
func TestCouncilApplyRefusedAfterConfirm(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, false)
	setStdin(t, "y\n")
	p := newChatAPI(t)
	refused := councilErrBody("invalid", "spec.effort: effort is low, medium or high",
		`"document":1,"fields":[{"path":"spec.effort","message":"effort is low, medium or high"}]`)
	p.onSeq("POST", "/council/apply",
		chatReply{409, applyNeedsInspect}, chatReply{400, refused}, chatReply{200, applyOK})
	path := councilFile(t, applyText)

	_, _, err := councilArgs(t, p.env(t), "apply", "-f", path)
	if err == nil {
		t.Fatal("the refusal was ignored")
	}
	if want := "document 2: spec.effort: effort is low, medium or high"; err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeRejected {
		t.Errorf("exit %d, want %d", code, codeRejected)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 2 {
		t.Errorf("%d POSTs, want 2", n)
	}
}

// TestCouncilApplyErrorNamesTheDocument maps each refusal to its exit code and the hub's words.
func TestCouncilApplyErrorNamesTheDocument(t *testing.T) {
	noBotMode(t)
	for _, c := range []struct {
		name   string
		status int
		body   string
		exit   int
		want   string
	}{
		{"invalid", 400, councilErrBody("invalid", "spec.effort: effort is low, medium or high",
			`"document":2,"fields":[{"path":"spec.effort","message":"effort is low, medium or high"}]`),
			codeRejected, "document 3: spec.effort: effort is low, medium or high"},
		{"not admin", 403, councilErrBody("not_admin", "Only teamspace admins can change shared members.", `"document":0`),
			codeAuth, "document 1: Only teamspace admins can change shared members."},
		{"no bot", 404, councilErrBody("not_found", "No bot named arm in teamspace teknoir-demo.", `"document":1`),
			codeNotFound, "document 2: No bot named arm in teamspace teknoir-demo."},
		{"too large", 413, councilErrBody("too_large", "The file is larger than 64 KiB.", ""),
			codeRejected, "The file is larger than 64 KiB."},
		{"confirm without needs", 409, councilErrBody("confirm_needed", "Confirm it.", `"needs":[]`),
			codeConflict, ""},
		{"unknown member", 400, councilErrBody("unknown_member", "'ghost' is not a member this bot can use.",
			`"document":1,"fields":[{"path":"spec.members[0]","message":"'ghost' is not a member this bot can use."}]`),
			codeUsage, "document 2: spec.members[0]: 'ghost' is not a member this bot can use."},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, true, false)
			setStdin(t, "")
			p := newChatAPI(t)
			p.on("POST", "/council/apply", c.status, c.body)
			path := councilFile(t, applyText)

			stdout, errs, err := councilArgs(t, p.env(t), "apply", "-f", path)
			if err == nil {
				t.Fatal("the refusal was ignored")
			}
			if code := exitCodeOf(err); code != c.exit {
				t.Errorf("exit %d, want %d (%v)", code, c.exit, err)
			}
			if c.want != "" && err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if stdout != "" {
				t.Errorf("stdout %q", stdout)
			}
			if strings.Contains(errs, "Continue?") {
				t.Errorf("stderr asks a question: %q", errs)
			}
			if n := len(p.seen("POST", "/council/apply")); n != 1 {
				t.Errorf("%d POSTs, want 1", n)
			}
		})
	}
}

// TestCouncilApplyConfirmTextIsSafe keeps the platform's words on one line and escapes the needs.
func TestCouncilApplyConfirmTextIsSafe(t *testing.T) {
	noBotMode(t)
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{"control characters folded", councilErrBody("confirm_needed", "This member\u0007 can\tread\npods and logs.",
			`"needs":["inspect"],"documents":[0]`),
			"Document 1: This member can read pods and logs. Nothing was written: pass --yes to confirm: inspect"},
		{"no message", councilErrBody("confirm_needed", "", `"needs":["inspect"]`),
			"This change needs your confirmation (inspect). Nothing was written: pass --yes to confirm: inspect"},
		{"need escaped", councilErrBody("confirm_needed", "Confirm.", `"needs":[`+q("in\nspect")+`]`),
			`Confirm. Nothing was written: pass --yes to confirm: in\nspect`},
	} {
		t.Run(c.name, func(t *testing.T) {
			setTTY(t, false, false)
			p := newChatAPI(t)
			p.onSeq("POST", "/council/apply", chatReply{409, c.body}, chatReply{200, applyOK})
			path := councilFile(t, applyText)

			_, _, err := councilArgs(t, p.env(t), "apply", "-f", path)
			if err == nil {
				t.Fatal("the confirmation was written")
			}
			if err.Error() != c.want {
				t.Errorf("error %q, want %q", err, c.want)
			}
			if code := exitCodeOf(err); code != codeUsage {
				t.Errorf("exit %d, want %d", code, codeUsage)
			}
			if n := len(p.seen("POST", "/council/apply")); n != 1 {
				t.Errorf("%d POSTs, want 1", n)
			}
		})
	}
}

// TestCouncilApplyPassesTheFileUntouched sends the bytes as they are, from a file and from stdin.
func TestCouncilApplyPassesTheFileUntouched(t *testing.T) {
	noBotMode(t)
	texts := []string{
		"\uFEFF# council\r\n---\r\nkind: CouncilRole\t\nmetadata: {name: \"a<b>&c\"}\n\n\n---\n",
		"kind: CouncilRole",
		applyText,
	}
	for _, text := range texts {
		p := newChatAPI(t)
		p.on("POST", "/council/apply", 200, applyOK)
		path := councilFile(t, text)

		if _, errs, err := councilArgs(t, p.env(t), "apply", "-f", path); err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		posts := p.seen("POST", "/council/apply")
		if len(posts) != 1 {
			t.Fatalf("%d POSTs, want 1", len(posts))
		}
		if got := bodyString(t, posts[0].Body, "yaml"); got != text {
			t.Errorf("file yaml %q, want %q", got, text)
		}

		setTTY(t, false, false)
		setStdin(t, text)
		if _, errs, err := councilArgs(t, p.env(t), "apply", "-f", "-"); err != nil {
			t.Fatalf("-f -: %v\n%s", err, errs)
		}
		posts = p.seen("POST", "/council/apply")
		if len(posts) != 2 {
			t.Fatalf("%d POSTs, want 2", len(posts))
		}
		if got := bodyString(t, posts[1].Body, "yaml"); got != text {
			t.Errorf("stdin yaml %q, want %q", got, text)
		}
	}
}

// TestCouncilApplyRefusesNonUTF8 sends nothing that json.Marshal would rewrite.
func TestCouncilApplyRefusesNonUTF8(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	notUTF8 := "kind: CouncilRole\nspec:\n  description: F\xe4sser\n"

	path := councilFile(t, notUTF8)
	_, _, err := councilArgs(t, p.env(t), "apply", "-f", path)
	if err == nil || err.Error() != path+" is not UTF-8 text" {
		t.Errorf("file: %v", err)
	}
	if code := exitCodeOf(err); code != codeUsage {
		t.Errorf("file: exit %d, want %d", code, codeUsage)
	}

	setTTY(t, false, false)
	setStdin(t, notUTF8)
	_, _, err = councilArgs(t, p.env(t), "apply", "-f", "-")
	if err == nil || err.Error() != "<stdin> is not UTF-8 text" {
		t.Errorf("stdin: %v", err)
	}
	if code := exitCodeOf(err); code != codeUsage {
		t.Errorf("stdin: exit %d, want %d", code, codeUsage)
	}

	big := councilFile(t, strings.Repeat("\xe4", 65537))
	_, _, err = councilArgs(t, p.env(t), "apply", "-f", big)
	if err == nil || err.Error() != big+" is not UTF-8 text" {
		t.Errorf("over 64 KiB and not UTF-8: %v", err)
	}
	if code := exitCodeOf(err); code != codeUsage {
		t.Errorf("over 64 KiB and not UTF-8: exit %d, want %d (the UTF-8 check comes first)", code, codeUsage)
	}

	wantReqs(t, p)
}

// TestCouncilApplySizeLimit takes exactly 64 KiB and refuses a byte more.
func TestCouncilApplySizeLimit(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("POST", "/council/apply", 200, applyOK)
	for _, text := range []string{
		"#" + strings.Repeat("a", 65534) + "\n",
		strings.Repeat("ä", 32768),
	} {
		if len(text) != councilApplyMax {
			t.Fatalf("%d bytes", len(text))
		}
		path := councilFile(t, text)
		if _, errs, err := councilArgs(t, p.env(t), "apply", "-f", path); err != nil {
			t.Fatalf("%v\n%s", err, errs)
		}
		posts := p.seen("POST", "/council/apply")
		if got := bodyString(t, posts[len(posts)-1].Body, "yaml"); got != text {
			t.Errorf("yaml %d bytes, want %d", len(got), len(text))
		}
	}
	if n := len(p.seen("POST", "/council/apply")); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
	for _, text := range []string{
		"#" + strings.Repeat("a", 65535) + "\n",
		strings.Repeat("ä", 32768) + "a",
	} {
		if len(text) != councilApplyMax+1 {
			t.Fatalf("%d bytes", len(text))
		}
		path := councilFile(t, text)
		_, _, err := councilArgs(t, p.env(t), "apply", "-f", path)
		if err == nil || err.Error() != "The file is larger than 64 KiB." {
			t.Errorf("%d bytes: %v", len(text), err)
		}
		if code := exitCodeOf(err); code != codeRejected {
			t.Errorf("%d bytes: exit %d, want %d", len(text), code, codeRejected)
		}
	}
	if n := len(p.seen("POST", "/council/apply")); n != 2 {
		t.Errorf("%d requests, want 2 after the refusals", n)
	}
}

// TestCouncilApplyNeedsAFile sends nothing without a file it can read.
func TestCouncilApplyNeedsAFile(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	path := councilFile(t, applyText)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"apply"}, "council apply needs a file"},
		{[]string{"apply", "-f", ""}, "council apply needs a file"},
		{[]string{"apply", "x.yaml"}, "council apply takes the file with -f, for example: tnctl council apply -f x.yaml"},
		{[]string{"apply", "-f", "/nonexistent/x.yaml"}, "cannot read /nonexistent/x.yaml"},
		{[]string{"-o", "xml", "apply", "-f", path}, "unknown output format"},
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
	if n := len(p.seen("POST", "/council/apply")); n != 0 {
		t.Errorf("%d requests were sent", n)
	}
}

// TestCouncilApplyAliases: apply hangs below council and below council role.
func TestCouncilApplyAliases(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	p.on("POST", "/council/apply", 200, applyOK)
	path := councilFile(t, applyText)

	for _, args := range [][]string{
		{"apply", "-f", path},
		{"role", "apply", "-f", path},
	} {
		out, errs, err := councilArgs(t, p.env(t), args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, errs)
		}
		if out != applyTable {
			t.Errorf("%v stdout:\n%q", args, out)
		}
	}
	wantReqs(t, p, "POST /council/apply", "POST /council/apply")

	council := newCouncilCmd(&cliEnv{})
	apply := councilSub(council, "apply")
	role := councilSub(council, "role")
	if apply == nil || role == nil {
		t.Fatal("council lacks apply or role")
	}
	roleApply := councilSub(role, "apply")
	if roleApply == nil {
		t.Fatal("council role lacks apply")
	}
	if roleApply == apply {
		t.Error("council and council role share one apply command")
	}
	for _, c := range []*cobra.Command{apply, roleApply} {
		file := c.Flags().Lookup("file")
		if file == nil || file.Shorthand != "f" {
			t.Errorf("%s lacks -f/--file", c.CommandPath())
		}
		if c.Flags().Lookup("dry-run") == nil || c.Flags().Lookup("yes") == nil {
			t.Errorf("%s lacks --dry-run or --yes", c.CommandPath())
		}
	}
}

// TestCouncilApplyJSON prints the platform's answer instead of the table.
func TestCouncilApplyJSON(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.on("POST", "/council/apply", 200, applyOK)
	path := councilFile(t, applyText)
	var indented bytes.Buffer
	if err := json.Indent(&indented, []byte(applyOK), "", "  "); err != nil {
		t.Fatal(err)
	}

	out, errs, err := councilArgs(t, p.env(t), "-o", "json", "apply", "-f", path)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if want := indented.String() + "\n"; out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	if errs != "" {
		t.Errorf("stderr %q", errs)
	}

	out, errs, err = councilArgs(t, p.env(t), "-o", "yaml", "apply", "-f", path)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if !strings.Contains(out, "results:") || !strings.Contains(out, "kind: CouncilRoster") {
		t.Errorf("yaml stdout:\n%s", out)
	}

	out, errs, err = councilArgs(t, p.env(t), "-o", "json", "apply", "-f", path, "--dry-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if want := indented.String() + "\n"; out != want {
		t.Errorf("--dry-run stdout:\n%q\nwant:\n%q", out, want)
	}
	if want := "· dry run: nothing was written\n"; errs != want {
		t.Errorf("--dry-run stderr %q, want %q", errs, want)
	}
}

// failAfter yields s once, then a read error that is not EOF.
type failAfter struct {
	s    string
	done bool
}

func (r *failAfter) Read(b []byte) (int, error) {
	if r.done {
		return 0, errors.New("read failed")
	}
	r.done = true
	return copy(b, r.s), nil
}

// TestCouncilApplyConfirmReadError: a read error is not an answer, even after a "y".
func TestCouncilApplyConfirmReadError(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, false)
	old := cliStdin
	cliStdin = &failAfter{s: "y"}
	t.Cleanup(func() { cliStdin = old })
	p := newChatAPI(t)
	p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsInspect}, chatReply{200, applyOK})

	_, errs, err := councilArgs(t, p.env(t), "apply", "-f", councilFile(t, applyText))
	if err == nil || err.Error() != "not confirmed: nothing was written" {
		t.Fatalf("err %v", err)
	}
	if code := exitCodeOf(err); code != codeError {
		t.Errorf("exit %d, want %d", code, codeError)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 1 {
		t.Errorf("%d POSTs, want 1", n)
	}
	if want := "Document 2: This member can read pods and logs. Continue? [y/N] \n"; errs != want {
		t.Errorf("stderr %q, want %q", errs, want)
	}
}

// TestCouncilApplyAskedAgainWithoutNeeds names the first question's needs when the second names none.
func TestCouncilApplyAskedAgainWithoutNeeds(t *testing.T) {
	noBotMode(t)
	setTTY(t, true, false)
	setStdin(t, "y\n")
	p := newChatAPI(t)
	p.onSeq("POST", "/council/apply", chatReply{409, applyNeedsInspect},
		chatReply{409, councilErrBody("confirm_needed", "Again.", `"needs":[]`)}, chatReply{200, applyOK})

	_, _, err := councilArgs(t, p.env(t), "apply", "-f", councilFile(t, applyText))
	want := "the platform asked again to confirm (inspect): someone changed a member meanwhile; run the command again"
	if err == nil || err.Error() != want {
		t.Fatalf("err %v, want %q", err, want)
	}
	if code := exitCodeOf(err); code != codeConflict {
		t.Errorf("exit %d, want %d", code, codeConflict)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 2 {
		t.Errorf("%d POSTs, want 2", n)
	}
}

// TestCouncilApplyTableIsSafe keeps the platform's words in their cells, and says so when it applied
// no document.
func TestCouncilApplyTableIsSafe(t *testing.T) {
	noBotMode(t)
	setTTY(t, false, false)
	p := newChatAPI(t)
	p.onSeq("POST", "/council/apply",
		chatReply{200, `{"results":[{"index":0,"kind":"Council\tRole","name":"a\tb\nc","scope":"b\nt","bot":"x\ny\u001b[2J","action":"cre\tated","version":1}]}`},
		chatReply{200, `{"results":[]}`})
	path := councilFile(t, applyText)

	out, errs, err := councilArgs(t, p.env(t), "apply", "-f", path)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	want := "DOC  KIND           NAME     SCOPE  BOT            ACTION     VERSION\n" +
		`1    Council\tRole  a\tb\nc  b\nt   x\ny\u001b[2J  cre\tated  1` + "\n"
	if out != want {
		t.Errorf("stdout:\n%q\nwant:\n%q", out, want)
	}
	noTrailingSpaces(t, out)

	out, errs, err = councilArgs(t, p.env(t), "apply", "-f", path)
	if err != nil {
		t.Fatalf("%v\n%s", err, errs)
	}
	if out != "" || errs != "· the platform applied no document\n" {
		t.Errorf("no results: stdout %q, stderr %q", out, errs)
	}
	if n := len(p.seen("POST", "/council/apply")); n != 2 {
		t.Errorf("%d POSTs, want 2", n)
	}
}

// TestCouncilApplyArgEscaped shows a stray argument on one line.
func TestCouncilApplyArgEscaped(t *testing.T) {
	noBotMode(t)
	p := newChatAPI(t)
	_, _, err := councilArgs(t, p.env(t), "apply", "x\ny.yaml")
	if err == nil || !strings.Contains(err.Error(), `tnctl council apply -f x\ny.yaml`) {
		t.Fatalf("err %v", err)
	}
	if code := exitCodeOf(err); code != codeUsage {
		t.Errorf("exit %d, want %d", code, codeUsage)
	}
	wantReqs(t, p)
}

// TestCouncilApplyHelp pins where apply's help says both kinds of council file come from, on the three
// paths that share its constructor (C3a6 brief ruling 12).
func TestCouncilApplyHelp(t *testing.T) {
	noBotMode(t)
	root := newCouncilCmd(&cliEnv{})
	role, roster := councilSub(root, "role"), councilSub(root, "roster")
	if role == nil || roster == nil {
		t.Fatal("council lacks role or roster")
	}
	first := "Apply a council file: members (kind CouncilRole) and councils (kind CouncilRoster), one or more\n" +
		"documents separated by ---, as tnctl council role export <member> and tnctl council roster get\n" +
		"<name> --bot <bot> -o yaml print them. The platform checks every document and applies all of them\n" +
		"or none; tnctl sends the file as it is. A file holds at most 20 documents and 64 KiB.\n\n"
	example := "  tnctl council role export triage --bot scout > triage.yaml\n" +
		"  tnctl council apply -f triage.yaml --dry-run\n" +
		"  tnctl council apply -f triage.yaml\n" +
		"  tnctl council roster get ops-scout --bot scout -o yaml > ops-scout.yaml\n" +
		"  tnctl council apply -f ops-scout.yaml\n" +
		"  cat council.yaml | tnctl council apply -f - --yes"
	for _, c := range []*cobra.Command{councilSub(root, "apply"), councilSub(role, "apply"), councilSub(roster, "apply")} {
		if c == nil {
			t.Fatal("an apply alias is missing")
		}
		if !strings.HasPrefix(c.Long, first) {
			t.Errorf("%s Long:\n%s", c.CommandPath(), c.Long)
		}
		if c.Example != example {
			t.Errorf("%s Example:\n%s", c.CommandPath(), c.Example)
		}
	}
}
