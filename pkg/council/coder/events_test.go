package coder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/teknoir/cli/pkg/council/model"
)

func TestReportShapes(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// The events prove the usage and the last text; the error event then ends it.
	events := strings.Join([]string{
		eventLine("step_start", sesA, `{"id":"prt_a","type":"step-start"}`),
		eventLine("step_finish", sesA, stepJSON(10, 3, 2, 0, 0)),
		eventLine("step_finish", sesA, stepJSON(-5, 4, -1, 2, 3)),
		eventLine("text", sesA, textPart("one")),
		eventLine("text", sesA, textPart("two")),
	}, "\n") + "\n"
	writeFake(t, b, fakeScript(b.Root, events, 0))
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateDone || rep.Text != "two" {
		t.Fatalf("state %q text %q", rep.State, rep.Text)
	}
	if rep.Steps != 2 || rep.Text != "two" {
		t.Errorf("steps %d text %q", rep.Steps, rep.Text)
	}
	// Negative counts are 0; reasoning counts as output.
	// Output 3 + reasoning 2, then output 4 + reasoning 0 (the -1 is 0): 9.
	if want := usage(2, 10, 9, 2, 3); rep.Usage != want {
		t.Errorf("usage %+v, want %+v", rep.Usage, want)
	}
}

func TestErrorEvent(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	events := `{"type":"error","timestamp":1,"sessionID":"` + sesA +
		`","error":{"type":"provider.invalid-request","message":"unknown top-level field: probe","status":400}}` + "\n"
	writeFake(t, b, fakeScript(b.Root, events, 1))
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateFailed || rep.Reason != ReasonError {
		t.Fatalf("state %q reason %q", rep.State, rep.Reason)
	}
	if !strings.Contains(rep.Error, "unknown top-level field: probe") {
		t.Errorf("error %q does not carry the message", rep.Error)
	}
	if rep.SessionID != sesA {
		t.Errorf("session %q: it must be stored after a failed run", rep.SessionID)
	}
	if got := loadSessions(b.Home).get(testThread); got != sesA {
		t.Errorf("stored session %q, want %q", got, sesA)
	}
}

func TestExitWithoutErrorEvent(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// 4 KiB of stderr, in colour: only the last 2 KiB is kept, and it has no escapes.
	script := "#!/bin/sh\n" +
		"printf '\\033[31mboom\\033[0m\\n' >&2\n" +
		"i=0\n" +
		"while [ $i -lt 200 ]; do printf 'filler line %03d ......................\\n' $i >&2; i=$((i+1)); done\n" +
		"exit 3\n"
	writeFake(t, b, script)
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateFailed || rep.Reason != ReasonExit {
		t.Fatalf("state %q reason %q", rep.State, rep.Reason)
	}
	if !strings.HasPrefix(rep.Error, "opencode exited with code 3") {
		t.Errorf("error %q does not start with the exit code", rep.Error)
	}
	if len(rep.Error) > MaxErrorBytes {
		t.Errorf("error is %d bytes, want at most %d", len(rep.Error), MaxErrorBytes)
	}
	if strings.ContainsRune(rep.Error, 0x1b) {
		t.Errorf("error carries an escape")
	}
	// The last 2 KiB: the newest filler lines are in it, the first are not.
	if !strings.Contains(rep.Error, "filler line 199") {
		t.Errorf("error %q lacks the last stderr line", rep.Error)
	}
	if strings.Contains(rep.Error, "filler line 000") {
		t.Errorf("error %q kept the first stderr line", rep.Error)
	}
	if !utf8.ValidString(rep.Error) {
		t.Errorf("error is not valid UTF-8")
	}
}

func TestCrashBySignal(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	writeFake(t, b, "#!/bin/sh\necho bye >&2\nkill -9 $$\n")
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateFailed || rep.Reason != ReasonCrash {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	if !strings.Contains(rep.Error, "opencode was killed by signal") {
		t.Errorf("error %q does not name the signal", rep.Error)
	}
}

func TestMissingBinary(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	cfg := b.Config()
	cfg.Binary = filepath.Join(b.Root, "nowhere", "opencode")
	rep := ctxRun(New(cfg), b.task("go"))
	if rep.State != StateFailed || rep.Reason != ReasonStart {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	if rep.Error == "" {
		t.Error("no error text")
	}
}

func TestOutputLimits(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// A text of 100 KiB, cut on a multi-byte character: "→" is three bytes, and every boundary
	// the cutter may stop on is inside the repeated character.
	unit := "→"
	big := strings.Repeat(unit, 100<<10/len(unit))
	events := strings.Join([]string{
		eventLine("text", sesA, textPart(big)),
		// A 2 MiB line, then events that must still be read.
		`{"type":"text","timestamp":1,"junk":"` + strings.Repeat("x", 2<<20) + `"}`,
		eventLine("tool_use", sesA, toolPart("shell", "completed")),
		eventLine("step_finish", sesA, stepJSON(1, 1, 0, 0, 0)),
		"this is not JSON",
		`{"type":"something_new","timestamp":1,"part":{"text":"no"}}`,
		eventLine("text", sesA, textPart("after")),
	}, "\n") + "\n"
	writeFake(t, b, fakeScript(b.Root, events, 0))

	var log updateLog
	task := b.task("go")
	task.OnUpdate = log.handler
	rep := ctxRun(New(b.Config()), task)
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	if rep.Text != "after" {
		t.Errorf("text %q, want the event after the skipped lines", rep.Text)
	}
	if rep.Tools != 1 || rep.Steps != 1 {
		t.Errorf("tools %d steps %d, want 1 and 1", rep.Tools, rep.Steps)
	}

	// The message update of the big text is cut to 2 KiB.
	var bigUpdate string
	for _, up := range log.updates() {
		if up.Kind == KindMessage && len(up.Text) > 100 {
			bigUpdate = up.Text
		}
	}
	if bigUpdate == "" {
		t.Fatal("no message update for the big text")
	}
	if len(bigUpdate) > MaxUpdateText+len(ellipsis) {
		t.Errorf("the message update is %d bytes, want at most %d plus the ellipsis", len(bigUpdate), MaxUpdateText)
	}
	if !strings.HasSuffix(bigUpdate, ellipsis) {
		t.Errorf("the message update was not marked as cut")
	}
	if !utf8.ValidString(bigUpdate) {
		t.Errorf("the message update is not valid UTF-8")
	}
	// The script itself wrote nothing, and the skills link is excluded: nothing changed.
	if len(rep.Changed) != 0 {
		t.Errorf("changed %q, want nothing", rep.Changed)
	}
	for _, line := range rep.Changed {
		if strings.Contains(line, ".agents") {
			t.Errorf("changed %q lists the skills link", rep.Changed)
		}
	}
}

func TestReportTextLimit(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// A single text part over the report limit, of multi-byte characters.
	big := strings.Repeat("→", (20<<10)/3)
	writeFake(t, b, fakeScript(b.Root, eventLine("text", sesA, textPart(big))+"\n", 0))
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q", rep.State, rep.Reason)
	}
	if len(rep.Text) > MaxReportText+len(ellipsis) {
		t.Errorf("text is %d bytes, want at most %d plus the ellipsis", len(rep.Text), MaxReportText)
	}
	if !strings.HasSuffix(rep.Text, ellipsis) {
		t.Errorf("text was not marked as cut")
	}
	if !utf8.ValidString(rep.Text) {
		t.Errorf("text is not valid UTF-8")
	}
}

func TestRedaction(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	botKey := "tbk1.abc-def.ghi-jkl." + strings.Repeat("A", 43)
	anthropicKey := "sk-ant-api03-AbCdEfGhIjKlMn"
	text := "key " + botKey + " and " + anthropicKey + " here"
	stderr := "stderr key " + botKey + "\n"
	script := fakeScript(b.Root, eventLine("text", sesA, textPart(text))+"\n", 0)
	script = strings.Replace(script, "cat <<'TEKNOIR_EVENTS'",
		"echo "+shellQuote(stderr)+" >&2\ncat <<'TEKNOIR_EVENTS'", 1)
	writeFake(t, b, script)

	var log updateLog
	task := b.task("go")
	task.OnUpdate = log.handler
	rep := ctxRun(New(b.Config()), task)
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q", rep.State, rep.Reason)
	}
	if strings.Contains(rep.Text, "tbk1.") || strings.Contains(rep.Text, "sk-ant-") {
		t.Errorf("the report text carries a key: %q", rep.Text)
	}
	if !strings.Contains(rep.Text, "[redacted]") {
		t.Errorf("the report text has no [redacted]: %q", rep.Text)
	}
	ups := log.updates()
	if got := ups[len(ups)-1].Text; strings.Contains(got, "tbk1.") || !strings.Contains(got, "[redacted]") {
		t.Errorf("the update text %q was not redacted", got)
	}

	// The same for stderr, which only an exit report shows.
	script = "#!/bin/sh\necho " + shellQuote(stderr) + " >&2\nexit 1\n"
	writeFake(t, b, script)
	rep = ctxRun(New(b.Config()), b.task("again"))
	if rep.Reason != ReasonExit {
		t.Fatalf("reason %q", rep.Reason)
	}
	if strings.Contains(rep.Error, "tbk1.") || !strings.Contains(rep.Error, "[redacted]") {
		t.Errorf("the error %q was not redacted", rep.Error)
	}
}

func TestStderrTailKeyCut(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// A key written so that the kept 2 KiB starts 13 bytes inside it: the kept bytes are the key's
	// tail, "api03-…", which no pattern matches any more. It is at the start of the window, so the
	// oldest of it survives the cut the Error makes to fit too.
	key := "sk-ant-api03-" + strings.Repeat("AbCdEfGhIj", 6) + "AbCdEfG"
	if len(key) != 80 || len("sk-ant-api03-") != 13 {
		t.Fatalf("the key is %d bytes, want 80", len(key))
	}
	const cut = 13
	prefix := strings.Repeat("p", 100)
	const tailText = " tail"
	filler := " " + strings.Repeat("z", MaxStderrTail-len(key)+cut-1-len(tailText)) + tailText
	if written := len(prefix) + len(key) + len(filler); written-MaxStderrTail != len(prefix)+cut {
		t.Fatalf("the window starts %d bytes in, want %d", written-MaxStderrTail, len(prefix)+cut)
	}
	script := "#!/bin/sh\nprintf '%s' " + shellQuote(prefix+key+filler) + " >&2\nexit 2\n"
	writeFake(t, b, script)
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateFailed || rep.Reason != ReasonExit {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	if !strings.HasPrefix(rep.Error, "opencode exited with code 2") {
		t.Errorf("error %q does not start with the exit code", rep.Error)
	}
	// No run of 20 characters of the key may survive.
	for i := 0; i+20 <= len(key); i++ {
		if run := key[i : i+20]; strings.Contains(rep.Error, run) {
			t.Fatalf("error %q carries %q of the key", rep.Error, run)
		}
	}
	// The rest of the kept stderr is still there.
	if !strings.Contains(rep.Error, "zzz") {
		t.Errorf("error %q dropped the stderr after the key", rep.Error)
	}
}

func TestControlCharactersStripped(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	title := "shell\x1b[31m completed\x07"
	part := `{"partID":"prt_b","type":"tool","tool":` + jsonString(title) + `,"state":{"status":"done"}}`
	writeFake(t, b, fakeScript(b.Root, eventLine("tool_use", sesA, part)+"\n", 0))
	var log updateLog
	task := b.task("go")
	task.OnUpdate = log.handler
	rep := ctxRun(New(b.Config()), task)
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q", rep.State, rep.Reason)
	}
	ups := log.updates()
	if len(ups) != 1 {
		t.Fatalf("updates %q", ups)
	}
	if strings.ContainsAny(ups[0].Title, "\x1b\x07") || strings.Contains(ups[0].Title, "[31m") {
		t.Errorf("title %q keeps an escape or a control character", ups[0].Title)
	}
	// The title is "<tool> <status>", with the escape and the control character gone.
	if ups[0].Title != "shell completed done" {
		t.Errorf("title %q, want %q", ups[0].Title, "shell completed done")
	}
}

func TestBadTasks(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	ran := filepath.Join(b.Root, "ran")
	writeFake(t, b, "#!/bin/sh\necho yes > "+shellQuote(ran)+"\n")
	r := New(b.Config())
	long := strings.Repeat("x", MaxPromptBytes+1)
	bad := []struct {
		name string
		task Task
	}{
		{"bad turn", Task{TurnID: "trn_short", ThreadID: testThread, Prompt: "hi"}},
		{"empty turn", Task{ThreadID: testThread, Prompt: "hi"}},
		{"bad thread", Task{TurnID: testTurn, ThreadID: "thr_short", Prompt: "hi"}},
		{"thread is a turn", Task{TurnID: testTurn, ThreadID: testTurn, Prompt: "hi"}},
		{"empty prompt", Task{TurnID: testTurn, ThreadID: testThread}},
		{"long prompt", Task{TurnID: testTurn, ThreadID: testThread, Prompt: long}},
		{"invalid UTF-8", Task{TurnID: testTurn, ThreadID: testThread, Prompt: "hi\xff"}},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			rep := ctxRun(r, c.task)
			if rep.State != StateFailed || rep.Reason != ReasonBadTask {
				t.Fatalf("state %q reason %q", rep.State, rep.Reason)
			}
		})
	}
	if _, err := os.Stat(ran); err == nil {
		t.Error("a bad task started the script")
	}

	// The largest prompt is accepted.
	good := Task{TurnID: testTurn, ThreadID: testThread, Prompt: strings.Repeat("y", MaxPromptBytes)}
	if rep := ctxRun(r, good); rep.State != StateDone {
		t.Errorf("the largest prompt: %q %q", rep.State, rep.Reason)
	}
}

func TestGitAbsent(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// The script writes a file: Changed would list it if git were there.
	events := eventLine("text", sesA, textPart("ok"))
	script := "#!/bin/sh\nprintf 'x\\n' > tool.txt\nprintf '%s\\n' " + shellQuote(events) + "\n"
	writeFake(t, b, script)
	cfg := b.Config()
	// A PATH with the fake OpenCode and nothing else: no git, and no shell builtins' friends.
	cfg.Environ = func() []string { return []string{"PATH=" + b.Bin} }
	rep := ctxRun(New(cfg), b.task("go"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	if len(rep.Changed) != 0 {
		t.Errorf("changed %q, want empty without git", rep.Changed)
	}
}

func TestSlowGrandchild(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	pidFile := filepath.Join(b.Root, "grandchild.pid")
	// The script leaves a child that holds stdout: Wait must not hang on it, and the child must not
	// outlive the run.
	writeFake(t, b, "#!/bin/sh\nsleep 20 &\necho $! > "+shellQuote(pidFile)+"\nexit 0\n")
	cfg := b.Config()
	cfg.Timeout = 5 * time.Second
	cfg.KillGrace = 300 * time.Millisecond
	start := time.Now()
	rep := ctxRun(New(cfg), b.task("go"))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v: the grandchild held it", elapsed)
	}
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	pid := strings.TrimSpace(readFile(t, pidFile))
	if pid == "" {
		t.Fatal("the script did not record its child's pid")
	}
	if processAlive(t, pid) {
		t.Errorf("the grandchild %s outlived the run", pid)
	}
}

func TestStepUsage(t *testing.T) {
	cases := []struct {
		part string
		want model.Usage
	}{
		{`{"type":"step-finish","tokens":{"input":10,"output":3,"reasoning":2,"cache":{"read":5,"write":7}}}`, usage(1, 10, 5, 5, 7)},
		{`{"type":"step-finish","tokens":{"input":-1,"output":-2,"reasoning":-3,"cache":{"read":-4,"write":-5}}}`, usage(1, 0, 0, 0, 0)},
		{`{"type":"step-finish"}`, usage(1, 0, 0, 0, 0)},
	}
	for _, c := range cases {
		if got := stepUsage(parsePart(t, c.part)); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.part, got, c.want)
		}
	}
}

func TestSanitize(t *testing.T) {
	botKey := "tbk1.a.b." + strings.Repeat("Z", 43)
	cases := []struct{ in, want string }{
		{"plain", "plain"},
		{"a\x1b[1mb\x1b[0m", "ab"},
		{"a\x00b\x07c", "abc"},
		{"keep\nnewline\tand tab", "keep\nnewline\tand tab"},
		{"key " + botKey + " end", "key [redacted] end"},
		{"key sk-ant-abcdefghij end", "key [redacted] end"},
		// A control character inside a key must not save it: the characters go before the
		// patterns run.
		{"sk-ant-\x01api03-ABCDEFGHIJKLMNOP", "[redacted]"},
		{"tbk1.a.b.\x1b[0m" + strings.Repeat("Z", 43), "[redacted]"},
		{"a\x1b[31mtbk1.a.b." + strings.Repeat("Z", 43) + "\x1b[0mb", "a[redacted]b"},
	}
	for _, c := range cases {
		if got := sanitize(c.in); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCutText(t *testing.T) {
	if got := cutText("short", 10); got != "short" {
		t.Errorf("short text was changed: %q", got)
	}
	// A three-byte character straddling the cut is not split.
	s := strings.Repeat("→", 10)
	got := cutText(s, len("→→→→")+1)
	if !utf8.ValidString(got) {
		t.Errorf("cut text is not valid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, ellipsis) {
		t.Errorf("cut text has no ellipsis: %q", got)
	}
	if n := len(got); n > len("→→→→")+1+len(ellipsis) {
		t.Errorf("cut text is %d bytes, want at most %d", n, len("→→→→")+1+len(ellipsis))
	}
}

func TestReadLineLong(t *testing.T) {
	// A line of 3 MiB is one over-long line; the line after it is read.
	big := strings.Repeat("x", 3<<20)
	input := big + "\n" + "next\n"
	br := newReader(input)
	line, tooLong, err := readLine(br)
	if err != nil || !tooLong {
		t.Fatalf("the long line: tooLong %v err %v", tooLong, err)
	}
	if len(line) != 0 {
		t.Errorf("the long line kept %d bytes", len(line))
	}
	line, tooLong, err = readLine(br)
	if err != nil || tooLong || string(line) != "next" {
		t.Fatalf("the line after it: %q tooLong %v err %v", line, tooLong, err)
	}
}

func TestReportJSON(t *testing.T) {
	rep := Report{
		State:    StateDone,
		Text:     "ok",
		Changed:  []string{"?? a"},
		Steps:    2,
		Tools:    1,
		Usage:    usage(2, 10, 5, 0, 0),
		Duration: 1500 * time.Millisecond,
	}
	data, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if ms, ok := got["durationMs"].(float64); !ok || ms != 1500 {
		t.Errorf("durationMs %v, want 1500", got["durationMs"])
	}
	if _, ok := got["Duration"]; ok {
		t.Errorf("the report still has a Duration field: %s", data)
	}
	if _, ok := got["reason"]; ok {
		t.Errorf("a done report has a reason: %s", data)
	}
	// An empty Changed is a list, never null.
	empty := Report{State: StateDone}
	data, err = json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"changed":[]`) {
		t.Errorf("an empty changed is %s", data)
	}
}
