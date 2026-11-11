package coder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/council/model"
)

// happyEvents is a whole successful run: a step, a tool, a text part and a finished step.
func happyEvents() string {
	return strings.Join([]string{
		eventLine("step_start", sesA, `{"id":"prt_a","type":"step-start"}`),
		eventLine("tool_use", sesA, toolPart("shell", "completed")),
		eventLine("text", sesA, textPart("probe ok")),
		eventLine("step_finish", sesA, stepJSON(10, 3, 2, 5, 7)),
	}, "\n") + "\n"
}

// happyScript is a run that also writes a file, so Changed has something to report.
func happyScript(t *testing.T, b *TestBed) {
	t.Helper()
	script := fakeScript(b.Root, happyEvents(), 0)
	script = strings.Replace(script,
		"cat <<'TEKNOIR_EVENTS'",
		"printf 'hello\\n' > tool.txt\ncat <<'TEKNOIR_EVENTS'", 1)
	writeFake(t, b, script)
}

// writeFake installs a script as the fake OpenCode.
func writeFake(t *testing.T, b *TestBed, script string) {
	t.Helper()
	if err := os.WriteFile(b.ScriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestHappyPath(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	happyScript(t, b)

	r := New(b.Config())
	var log updateLog
	task := b.task("Fix the bug")
	task.OnUpdate = log.handler
	rep := ctxRun(r, task)

	if rep.State != StateDone || rep.Reason != "" {
		t.Fatalf("state %q reason %q error %q, want done", rep.State, rep.Reason, rep.Error)
	}
	if rep.Text != "probe ok" {
		t.Errorf("text %q, want %q", rep.Text, "probe ok")
	}
	if rep.Steps != 1 || rep.Tools != 1 {
		t.Errorf("steps %d tools %d, want 1 and 1", rep.Steps, rep.Tools)
	}
	if want := usage(1, 10, 5, 5, 7); rep.Usage != want {
		t.Errorf("usage %+v, want %+v", rep.Usage, want)
	}
	if rep.SessionID != sesA {
		t.Errorf("session %q, want %q", rep.SessionID, sesA)
	}
	if rep.Duration <= 0 {
		t.Errorf("duration %v, want > 0", rep.Duration)
	}
	if !contains(rep.Changed, "?? tool.txt") {
		t.Errorf("changed %q, want an untracked tool.txt line", rep.Changed)
	}
	// The skills link is excluded: it is not the task's work.
	for _, line := range rep.Changed {
		if strings.Contains(line, ".agents") {
			t.Errorf("changed %q lists the skills link", rep.Changed)
		}
	}

	// The command line.
	argv := b.argv()
	message := "Task from the Teknoir council (turn " + testTurn + "):\n\nFix the bug"
	want := []string{"run", "--standalone", "--format", "json", "--model", "teknoir/claude-sonnet-5-5#medium", message}
	if strings.Join(argv, "\x1f") != strings.Join(want, "\x1f") {
		t.Errorf("argv %q, want %q", argv, want)
	}

	// The work directory, and the skills link.
	if got := strings.TrimSpace(b.read("pwd")); got != b.workDir(testThread) {
		t.Errorf("working directory %q, want %q", got, b.workDir(testThread))
	}
	link := filepath.Join(b.workDir(testThread), ".agents", "skills")
	target, err := os.Readlink(link)
	if err != nil || target != b.Skills {
		t.Errorf("skills link: %q %v, want %q", target, err, b.Skills)
	}
	info, err := os.Stat(b.workDir(testThread))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("work dir mode %v, want 0700", info.Mode().Perm())
	}

	// The environment: the pass-through set plus the coder's own.
	env := b.read("env")
	for _, wantLine := range []string{
		"HOME=" + b.Home,
		"XDG_DATA_HOME=" + filepath.Join(b.Home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(b.Home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(b.Home, ".cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(b.Home, ".config"),
		"OPENCODE_CONFIG=/etc/teknoirbot/opencode.json",
		"TEKNOIRBOT_TURN=" + testTurn,
		"TEKNOIRBOT_MEMBER=coder",
	} {
		if !hasEnv(env, wantLine) {
			t.Errorf("environment lacks %q", wantLine)
		}
	}
	// And nothing else: every name is a pass-through one, one the coder sets, or one the shell
	// itself adds.
	allowed := map[string]bool{
		"PWD": true, "SHLVL": true, "_": true, "OLDPWD": true,
	}
	for _, name := range passthrough {
		allowed[name] = true
	}
	for _, name := range []string{
		"HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME",
		"OPENCODE_CONFIG", "TEKNOIRBOT_TURN", "TEKNOIRBOT_MEMBER",
	} {
		allowed[name] = true
	}
	for _, line := range strings.Split(env, "\n") {
		name, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if !allowed[name] {
			t.Errorf("environment carries %s, which is neither passed through nor set by the coder", name)
		}
	}

	// The updates, in order.
	got := log.kinds()
	wantKinds := []string{KindStep, KindTool, KindMessage}
	if strings.Join(got, ",") != strings.Join(wantKinds, ",") {
		t.Fatalf("update kinds %q, want %q", got, wantKinds)
	}
	ups := log.updates()
	if ups[1].Title != "shell completed" {
		t.Errorf("tool title %q", ups[1].Title)
	}
	if ups[2].Text != "probe ok" {
		t.Errorf("message text %q", ups[2].Text)
	}
}

func TestEnvironmentDropsOutsiders(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	cfg := b.Config()
	cfg.Environ = func() []string {
		return []string{
			"PATH=" + b.Bin + ":" + os.Getenv("PATH"),
			"ANTHROPIC_API_KEY=sk-ant-should-not-travel",
			"OPENCODE_FOO=bar",
			"HTTPS_PROXY=http://proxy:3128",
			"HOME=/root",
			"TEKNOIR_DOMAIN=teknoir.cloud",
			"TEKNOIRBOT_API=https://api.example",
		}
	}
	rep := ctxRun(New(cfg), b.task("go"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	env := b.read("env")
	for _, want := range []string{"TEKNOIR_DOMAIN=teknoir.cloud", "TEKNOIRBOT_API=https://api.example", "HOME=" + b.Home} {
		if !hasEnv(env, want) {
			t.Errorf("environment lacks %q: %s", want, env)
		}
	}
	for _, bad := range []string{"ANTHROPIC_API_KEY", "OPENCODE_FOO", "HTTPS_PROXY"} {
		if hasName(env, bad) {
			t.Errorf("environment carries %s", bad)
		}
	}
}

// TestEnvironmentPassesTheBotName: the skills read TEKNOIRBOT_NAME (the relay's own-device filter,
// teknoirbot-app-key), so the Coder's child gets it.
func TestEnvironmentPassesTheBotName(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	cfg := b.Config()
	cfg.Environ = func() []string {
		return []string{
			"PATH=" + b.Bin + ":" + os.Getenv("PATH"),
			"TEKNOIRBOT_NAME=scout",
			"TEKNOIRBOT_NAMESPACE=teknoir-demo",
		}
	}
	rep := ctxRun(New(cfg), b.task("go"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	env := b.read("env")
	for _, want := range []string{"TEKNOIRBOT_NAME=scout", "TEKNOIRBOT_NAMESPACE=teknoir-demo"} {
		if !hasEnv(env, want) {
			t.Errorf("environment lacks %q: %s", want, env)
		}
	}
}

func TestEffortMapping(t *testing.T) {
	needUnix(t)
	cases := []struct{ effort, want string }{
		{"", "#high"},
		{"high", "#high"},
		{"max", "#high"},
		{"xhigh", "#high"},
		{"bogus", "#high"},
		{"low", "#low"},
		{"medium", "#medium"},
	}
	for _, c := range cases {
		t.Run("effort="+c.effort, func(t *testing.T) {
			b := newBed(t)
			writeFake(t, b, fakeScript(b.Root, "", 0))
			task := b.task("go")
			task.Effort = c.effort
			if rep := ctxRun(New(b.Config()), task); rep.State != StateDone {
				t.Fatalf("state %q reason %q", rep.State, rep.Reason)
			}
			argv := b.argv()
			if len(argv) < 7 {
				t.Fatalf("argv %q", argv)
			}
			if want := "teknoir/claude-sonnet-5-5" + c.want; argv[5] != want {
				t.Errorf("model argument %q, want %q", argv[5], want)
			}
		})
	}
}

// freeCounter is a Statfs for a test: the values it returns in order, the last one repeated.
type freeCounter struct {
	mu     sync.Mutex
	values []uint64
	err    error
	calls  int
}

func (f *freeCounter) statfs(string) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if len(f.values) == 0 {
		return DefaultMinFreeBytes * 4, nil
	}
	if i >= len(f.values) {
		i = len(f.values) - 1
	}
	return f.values[i], nil
}

func (f *freeCounter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestDiskBeforeStart: free space under the work root below MinFreeBytes and no process starts.
func TestDiskBeforeStart(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	happyScript(t, b)
	cfg := b.Config()
	cfg.Statfs = (&freeCounter{values: []uint64{1 << 20}}).statfs

	rep := ctxRun(New(cfg), b.task("big install"))
	if rep.State != StateFailed || rep.Reason != ReasonDisk {
		t.Fatalf("state %q reason %q error %q, want disk", rep.State, rep.Reason, rep.Error)
	}
	want := "The bot's disk has less than 2 GiB free: free space under " + b.WorkRoot + " before asking for this."
	if rep.Error != want {
		t.Errorf("error %q, want %q", rep.Error, want)
	}
	if _, err := os.Stat(filepath.Join(b.Root, "argv")); err == nil {
		t.Error("a process was started")
	}
	if rep.Steps != 0 || rep.SessionID != "" {
		t.Errorf("report %+v", rep)
	}
}

// TestDiskDuringRun: free space that falls below StopFreeBytes while OpenCode runs stops its
// process group as a cancel does, and the report says the disk.
func TestDiskDuringRun(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	pidFile := filepath.Join(b.Root, "pid")
	childFile := filepath.Join(b.Root, "child.pid")
	writeFake(t, b, "#!/bin/sh\n"+
		"echo $$ > "+shellQuote(pidFile)+"\n"+
		"sleep 30 &\n"+
		"echo $! > "+shellQuote(childFile)+"\n"+
		"sleep 30\n")
	// Enough at the start, plenty for the first checks, then under the stop threshold.
	counter := &freeCounter{values: []uint64{
		DefaultMinFreeBytes * 4,
		DefaultMinFreeBytes * 4,
		DefaultMinFreeBytes * 4,
		1 << 20,
	}}
	cfg := b.Config()
	cfg.Statfs = counter.statfs
	cfg.DiskCheckEvery = 20 * time.Millisecond
	cfg.KillGrace = 300 * time.Millisecond

	start := time.Now()
	rep := ctxRun(New(cfg), b.task("fill the disk"))
	if rep.State != StateFailed || rep.Reason != ReasonDisk {
		t.Fatalf("state %q reason %q error %q, want disk", rep.State, rep.Reason, rep.Error)
	}
	if rep.Error != msgDiskStopped {
		t.Errorf("error %q, want %q", rep.Error, msgDiskStopped)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the disk stop took %v", elapsed)
	}
	pid := strings.TrimSpace(readFile(t, pidFile))
	if pid == "" {
		t.Fatal("the script did not record its pid")
	}
	if processAlive(t, pid) {
		t.Errorf("the process %s survived the disk stop", pid)
	}
	child := strings.TrimSpace(readFile(t, childFile))
	if child == "" {
		t.Fatal("the script did not record its child's pid")
	}
	if processAlive(t, child) {
		t.Errorf("the background child %s survived the disk stop", child)
	}
}

// TestStatfsErrorSkipsTheDiskChecks: a platform that cannot tell its free space never fails a
// task, and says so once.
func TestStatfsErrorSkipsTheDiskChecks(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// A run long enough for the disk checks to tick.
	writeFake(t, b, "#!/bin/sh\nsleep 0.3\n"+fakeScript(b.Root, happyEvents(), 0)[len("#!/bin/sh\n"):])
	counter := &freeCounter{err: errors.New("statfs: operation not supported")}
	var mu sync.Mutex
	var lines []string
	cfg := b.Config()
	cfg.Statfs = counter.statfs
	cfg.DiskCheckEvery = 5 * time.Millisecond
	cfg.Logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}

	rep := ctxRun(New(cfg), b.task("Fix the bug"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q, want done", rep.State, rep.Reason, rep.Error)
	}
	if counter.count() < 2 {
		t.Errorf("statfs was read %d times, want the run to check the disk too", counter.count())
	}
	mu.Lock()
	defer mu.Unlock()
	logged := 0
	for _, line := range lines {
		if strings.Contains(line, "free space under") {
			logged++
		}
	}
	if logged != 1 {
		t.Errorf("the statfs error was logged %d times, want once: %v", logged, lines)
	}
}

func TestSessions(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	writeFake(t, b, fakeScript(b.Root, eventLine("text", sesA, textPart("hi"))+"\n", 0))
	r := New(b.Config())

	if rep := ctxRun(r, b.task("one")); rep.State != StateDone || rep.SessionID != sesA {
		t.Fatalf("first run: %q %q %q", rep.State, rep.SessionID, rep.Error)
	}
	// A new session is not passed: there was none.
	if hasFlag(b.argv(), "--session") {
		t.Errorf("the first run passed --session: %q", b.argv())
	}
	path := filepath.Join(b.Home, ".local", "state", "teknoirbot", "sessions.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("sessions file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("sessions file mode %v, want 0600", info.Mode().Perm())
	}

	// The second run on the same thread continues the stored session.
	writeFake(t, b, recordTwice(b, eventLine("text", sesA, textPart("twice"))+"\n"))
	if rep := ctxRun(r, b.task("two")); rep.State != StateDone {
		t.Fatalf("second run: %q %q", rep.State, rep.Reason)
	}
	if got := splitNUL(b.read("argv2")); !hasArgv(got, "--session", sesA) {
		t.Errorf("second run argv %q does not continue %s", got, sesA)
	}

	// Another thread gets no session.
	writeFake(t, b, fakeScript(b.Root, eventLine("text", sesB, textPart("three"))+"\n", 0))
	_ = os.Remove(filepath.Join(b.Root, "argv"))
	if rep := ctxRun(r, Task{TurnID: otherTurn, ThreadID: otherThr, Prompt: "three"}); rep.State != StateDone {
		t.Fatalf("other thread: %q %q", rep.State, rep.Reason)
	}
	if hasFlag(b.argv(), "--session") {
		t.Errorf("a new thread got --session: %q", b.argv())
	}

	// A corrupt file is empty, and is rewritten with the thread's new session.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.Root, "argv")); err != nil {
		t.Fatal(err)
	}
	if rep := ctxRun(r, b.task("four")); rep.State != StateDone {
		t.Fatalf("after the corrupt file: %q %q", rep.State, rep.Reason)
	}
	argv := b.argv()
	if len(argv) == 0 {
		t.Fatal("the run after the corrupt file recorded no argv")
	}
	if hasFlag(argv, "--session") {
		t.Errorf("a corrupt sessions file passed --session: %q", argv)
	}
	if got, _ := loadSessions(b.Home).get(testThread); got != sesB {
		t.Errorf("after the corrupt file the store holds %q for the thread, want %q", got, sesB)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Errorf("sessions file after a corrupt one is not JSON: %s", data)
	}

	// An unknown version is empty too.
	if err := os.WriteFile(path, []byte(`{"version":99,"threads":{"`+testThread+`":{"session":"`+sesA+`","updated":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadSessions(b.Home).get(testThread); got != "" {
		t.Errorf("a version 99 file gave session %q", got)
	}
}

func TestSessionFromInvalidIDIgnored(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	writeFake(t, b, fakeScript(b.Root, eventLine("text", "ses_../x", textPart("hi"))+"\n", 0))
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q", rep.State, rep.Reason)
	}
	if rep.SessionID != "" {
		t.Errorf("session %q, want none", rep.SessionID)
	}
	if got, _ := loadSessions(b.Home).get(testThread); got != "" {
		t.Errorf("an invalid session was stored: %q", got)
	}
	path := filepath.Join(b.Home, ".local", "state", "teknoirbot", "sessions.json")
	data, err := os.ReadFile(path)
	if err == nil && strings.Contains(string(data), testThread) {
		t.Errorf("the sessions file has an entry for the thread: %s", data)
	}
	if hasFlag(b.argv(), "--session") {
		t.Errorf("an invalid session was passed: %q", b.argv())
	}
}

func TestSessionBound(t *testing.T) {
	home := t.TempDir()
	store := loadSessions(home)
	// Distinct update times, oldest first, so "the least recently updated" is well defined: real
	// stores can land in the same millisecond.
	for i := 0; i < MaxSessions+5; i++ {
		store.threads["thr_"+padBase64(i, 22)] = sessionEntry{Session: "ses_" + padAlnum(i, 10), Updated: int64(i)}
	}
	if err := store.save(); err != nil {
		t.Fatal(err)
	}
	again := loadSessions(home)
	if n := len(again.threads); n != MaxSessions {
		t.Fatalf("%d threads stored, want %d", n, MaxSessions)
	}
	// The five oldest went.
	for i := 0; i < 5; i++ {
		if got, _ := again.get("thr_" + padBase64(i, 22)); got != "" {
			t.Errorf("thread %d was kept (%q)", i, got)
		}
	}
	newest, _ := again.get("thr_" + padBase64(MaxSessions+4, 22))
	if want := "ses_" + padAlnum(MaxSessions+4, 10); newest != want {
		t.Errorf("the newest thread is %q, want %q", newest, want)
	}

	// Through the store, the file never holds more than the bound either.
	other := loadSessions(t.TempDir())
	for i := 0; i < MaxSessions+5; i++ {
		if err := other.store("thr_"+padBase64(i, 22), "ses_"+padAlnum(i, 10), "high"); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(other.threads); n > MaxSessions {
		t.Errorf("store() kept %d threads, want at most %d", n, MaxSessions)
	}
}

// recordTwice is a script that records its argv twice: the second copy to argv2.
func recordTwice(b *TestBed, events string) string {
	script := fakeScript(b.Root, events, 0)
	return strings.Replace(script, "printf '%s\\0' \"$@\" > ",
		"printf '%s\\0' \"$@\" > "+shellQuote(filepath.Join(b.Root, "argv2"))+"\nprintf '%s\\0' \"$@\" > ", 1)
}

func TestEffectivelyOneAtATime(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// One script for both runs, told apart by the turn in the message: a run must not replace a
	// script another run's shell is still reading.
	release := filepath.Join(b.Root, "release")
	started := filepath.Join(b.Root, "start1")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"*" + otherTurn + "*)\n" +
		"echo started > " + shellQuote(filepath.Join(b.Root, "start2")) + "\n" +
		";;\n" +
		"*)\n" +
		"echo started > " + shellQuote(started) + "\n" +
		"i=0\n" +
		"while [ ! -f " + shellQuote(release) + " ] && [ $i -lt 2000 ]; do i=$((i+1)); sleep 0.01; done\n" +
		";;\n" +
		"esac\n"
	writeFake(t, b, script)
	r := New(b.Config())
	var first, second updateLog

	firstDone := make(chan Report, 1)
	go func() {
		task := b.task("first")
		task.OnUpdate = first.handler
		firstDone <- ctxRun(r, task)
	}()
	b.awaitFile("start1")

	// The second run waits, says so once, and has not started.
	secondStarted := filepath.Join(b.Root, "start2")
	secondDone := make(chan Report, 1)
	go func() {
		task := Task{TurnID: otherTurn, ThreadID: otherThr, Prompt: "second"}
		task.OnUpdate = second.handler
		secondDone <- ctxRun(r, task)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(second.updates()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := second.kinds(); len(got) != 1 || got[0] != KindWait {
		t.Fatalf("the waiting run's updates %q, want one wait", got)
	}
	if _, err := os.Stat(secondStarted); err == nil {
		t.Fatal("the second run started before the first ended")
	}

	// Releasing the first lets the second start, and only then.
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if rep := <-firstDone; rep.State != StateDone {
		t.Fatalf("first run: %q %q", rep.State, rep.Reason)
	}
	select {
	case rep := <-secondDone:
		if rep.State != StateDone {
			t.Fatalf("second run: %q %q %q", rep.State, rep.Reason, rep.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second run did not finish")
	}
	if _, err := os.Stat(secondStarted); err != nil {
		t.Errorf("the second run never started: %v", err)
	}
}

func TestWaitCancelled(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	started := filepath.Join(b.Root, "started")
	waiterStarted := filepath.Join(b.Root, "waiter")
	// No release file: the holding run holds the lock until the test stops it. One script serves
	// both runs, so the second write does not replace a script the first run is still reading.
	writeFake(t, b, "#!/bin/sh\n"+
		"case \"$*\" in\n"+
		"*"+otherTurn+"*)\n"+
		"echo started > "+shellQuote(waiterStarted)+"\n"+
		";;\n"+
		"*)\n"+
		"echo started > "+shellQuote(started)+"\n"+
		"sleep 5\n"+
		";;\n"+
		"esac\n")
	r := New(b.Config())
	holdCtx, holdCancel := context.WithCancel(context.Background())
	defer holdCancel()
	holdDone := make(chan Report, 1)
	go func() { holdDone <- r.Run(holdCtx, b.task("hold")) }()
	b.awaitFile("started")

	ctx, cancel := context.WithCancel(context.Background())
	waiterDone := make(chan Report, 1)
	go func() {
		waiterDone <- r.Run(ctx, Task{TurnID: otherTurn, ThreadID: otherThr, Prompt: "waiter"})
	}()
	// Give the waiter time to reach the lock, then cancel it.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case rep := <-waiterDone:
		if rep.State != StateFailed || rep.Reason != ReasonCancelled {
			t.Fatalf("the cancelled wait: %q %q", rep.State, rep.Reason)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled wait did not return")
	}
	if _, err := os.Stat(waiterStarted); err == nil {
		t.Errorf("the cancelled wait started the script")
	}
	holdCancel()
	<-holdDone
}

func TestBudgetStop(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	pidFile := filepath.Join(b.Root, "grandchild.pid")
	// A background child, two finished steps, then a long sleep: the second step's false stops it.
	script := "#!/bin/sh\n" +
		"sleep 30 &\n" +
		"echo $! > " + shellQuote(pidFile) + "\n" +
		"printf '%s\\n' " + shellQuote(eventLine("step_finish", sesA, stepJSON(1, 1, 0, 0, 0))) + "\n" +
		"printf '%s\\n' " + shellQuote(eventLine("step_finish", sesA, stepJSON(1, 1, 0, 0, 0))) + "\n" +
		"sleep 30\n"
	writeFake(t, b, script)

	steps := 0
	task := b.task("budget")
	task.OnStep = func(model.Usage) bool {
		steps++
		return steps < 2
	}
	start := time.Now()
	rep := ctxRun(New(b.Config()), task)
	if rep.State != StateFailed || rep.Reason != ReasonBudget {
		t.Fatalf("state %q reason %q error %q, want budget", rep.State, rep.Reason, rep.Error)
	}
	if rep.Steps != 2 {
		t.Errorf("steps %d, want 2", rep.Steps)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the budget stop took %v", elapsed)
	}
	if pid := strings.TrimSpace(readFile(t, pidFile)); pid != "" {
		if processAlive(t, pid) {
			t.Errorf("the background child %s survived the stop", pid)
		}
	} else {
		t.Error("the script did not record its child's pid")
	}
}

func TestTimeoutKill(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	pidFile := filepath.Join(b.Root, "pid")
	childFile := filepath.Join(b.Root, "child.pid")
	// TERM is ignored; only KILL ends it. The background child records its own pid: the group kill
	// must reach it too.
	script := "#!/bin/sh\n" +
		"trap '' TERM\n" +
		"echo $$ > " + shellQuote(pidFile) + "\n" +
		"sleep 30 &\n" +
		"echo $! > " + shellQuote(childFile) + "\n" +
		"sleep 30\n"
	writeFake(t, b, script)
	cfg := b.Config()
	cfg.Timeout = 300 * time.Millisecond
	cfg.KillGrace = 300 * time.Millisecond
	start := time.Now()
	rep := ctxRun(New(cfg), b.task("timeout"))
	elapsed := time.Since(start)
	if rep.State != StateFailed || rep.Reason != ReasonTimeout {
		t.Fatalf("state %q reason %q error %q, want timeout", rep.State, rep.Reason, rep.Error)
	}
	// SIGTERM is ignored, so the whole KillGrace passes before the SIGKILL.
	if elapsed < cfg.Timeout+cfg.KillGrace {
		t.Errorf("the timeout kill took %v, less than Timeout+KillGrace (%v)", elapsed, cfg.Timeout+cfg.KillGrace)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the timeout kill took %v", elapsed)
	}
	pid := strings.TrimSpace(readFile(t, pidFile))
	if pid == "" {
		t.Fatal("the script did not record its pid")
	}
	if processAlive(t, pid) {
		t.Errorf("the process %s survived the kill", pid)
	}
	child := strings.TrimSpace(readFile(t, childFile))
	if child == "" {
		t.Fatal("the script did not record its child's pid")
	}
	if processAlive(t, child) {
		t.Errorf("the background child %s survived the kill", child)
	}
}

func TestCancel(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	started := filepath.Join(b.Root, "started")
	pidFile := filepath.Join(b.Root, "pid")
	writeFake(t, b, "#!/bin/sh\necho $$ > "+shellQuote(pidFile)+"\necho started > "+shellQuote(started)+"\nsleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan Report, 1)
	go func() { done <- New(b.Config()).Run(ctx, b.task("cancel")) }()
	b.awaitFile("started")
	cancel()
	select {
	case rep := <-done:
		if rep.State != StateFailed || rep.Reason != ReasonCancelled {
			t.Fatalf("state %q reason %q error %q, want cancelled", rep.State, rep.Reason, rep.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled run did not return")
	}
	pid := strings.TrimSpace(readFile(t, pidFile))
	if pid == "" {
		t.Fatal("the script did not record its pid")
	}
	if processAlive(t, pid) {
		t.Errorf("the process %s survived the cancel", pid)
	}
}

func TestBackgroundChildrenKilled(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	pidFile := filepath.Join(b.Root, "child.pid")
	// A task that leaves a background process behind and exits 0: the process must not survive the
	// run, or it would keep editing the work dir and pile up in the pod.
	script := "#!/bin/sh\n" +
		"sleep 60 >/dev/null 2>&1 &\n" +
		"echo $! > " + shellQuote(pidFile) + "\n" +
		"printf '%s\\n' " + shellQuote(eventLine("text", sesA, textPart("started a server"))) + "\n" +
		"exit 0\n"
	writeFake(t, b, script)
	rep := ctxRun(New(b.Config()), b.task("serve"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	pid := strings.TrimSpace(readFile(t, pidFile))
	if pid == "" {
		t.Fatal("the script did not record its child's pid")
	}
	if processAlive(t, pid) {
		t.Errorf("the background child %s outlived the run", pid)
	}
}

func TestGitConfigCannotHangTheRunner(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	happyScript(t, b)
	r := New(b.Config())
	if rep := ctxRun(r, b.task("first")); rep.State != StateDone {
		t.Fatalf("the first run: %q %q", rep.State, rep.Reason)
	}
	// The coder controls the work dir's git config: a hook that waits for a child of its own must
	// not hold the runner when Changed is built, after the run and with the lock still held.
	hook := filepath.Join(b.Root, "fsmonitor.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nsleep 15 &\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tfsmonitor = " + hook + "\n"
	if err := os.WriteFile(filepath.Join(b.workDir(testThread), ".git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rep := ctxRun(r, b.task("second"))
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v: the git config held it", elapsed)
	}
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q", rep.State, rep.Reason, rep.Error)
	}
	if !contains(rep.Changed, "?? tool.txt") {
		t.Errorf("changed %q, want the untracked tool.txt", rep.Changed)
	}
}

func TestSkillsExcluded(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	happyScript(t, b)
	r := New(b.Config())
	if rep := ctxRun(r, b.task("first")); rep.State != StateDone {
		t.Fatalf("the first run: %q %q", rep.State, rep.Reason)
	}
	path := filepath.Join(b.workDir(testThread), ".git", "info", "exclude")
	if got := countLines(readFile(t, path), excludeLine); got != 1 {
		t.Fatalf("the exclude file has %d %q lines, want 1: %q", got, excludeLine, readFile(t, path))
	}
	// A second run on the same work dir does not add it again.
	if rep := ctxRun(r, b.task("second")); rep.State != StateDone {
		t.Fatalf("the second run: %q %q", rep.State, rep.Reason)
	}
	if got := countLines(readFile(t, path), excludeLine); got != 1 {
		t.Errorf("the exclude file has %d %q lines after a second run, want 1", got, excludeLine)
	}
}

func TestWorkDirSymlinkRefused(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	// A link where the thread's work dir should be: following it would run the task in another
	// thread's directory.
	target := filepath.Join(b.Root, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, b.workDir(testThread)); err != nil {
		t.Fatal(err)
	}
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateFailed || rep.Reason != ReasonStart {
		t.Fatalf("state %q reason %q error %q, want start", rep.State, rep.Reason, rep.Error)
	}
	if !strings.Contains(rep.Error, "not a directory") {
		t.Errorf("error %q does not say why", rep.Error)
	}
	if argv := b.argv(); len(argv) != 0 {
		t.Errorf("the task started with argv %q", argv)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the link's target has %d entries, want none", len(entries))
	}
}

func TestAgentsSymlinkRefused(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	dir := b.workDir(testThread)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(b.Root, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".agents")); err != nil {
		t.Fatal(err)
	}
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q: a symlinked .agents is skipped, not failed", rep.State, rep.Reason, rep.Error)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the .agents link's target has %d entries, want none", len(entries))
	}
}

func TestStateDirSymlinkRefused(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	state := filepath.Join(b.Home, ".local", "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(b.Root, "elsewhere")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(state, "teknoirbot")); err != nil {
		t.Fatal(err)
	}
	writeFake(t, b, fakeScript(b.Root, eventLine("text", sesA, textPart("hi"))+"\n", 0))
	rep := ctxRun(New(b.Config()), b.task("go"))
	if rep.State != StateDone {
		t.Fatalf("state %q reason %q error %q: a refused save does not fail the task", rep.State, rep.Reason, rep.Error)
	}
	if _, err := os.Stat(filepath.Join(target, "sessions.json")); err == nil {
		t.Error("the state link's target got a sessions.json")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Errorf("the state link's target has %d entries (%v), want none", len(entries), err)
	}
}

// countLines is how many lines of text are want.
func countLines(text, want string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSuffix(line, "\r") == want {
			n++
		}
	}
	return n
}

func TestSkillsLink(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	r := New(b.Config())
	dir := b.workDir(testThread)
	if err := os.MkdirAll(filepath.Join(dir, ".agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".agents", "skills")

	// A symlink to somewhere else is replaced.
	elsewhere := filepath.Join(b.Root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	r.linkSkills(dir)
	if target, err := os.Readlink(link); err != nil || target != b.Skills {
		t.Errorf("the link is %q %v, want %q", target, err, b.Skills)
	}

	// A real directory there is left alone.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(link, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(link, "keep")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.linkSkills(dir)
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("the real directory was replaced: %v %v", info, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the real directory's content is gone: %v", err)
	}

	// A missing SkillsDir is not an error, and makes no link.
	cfg := b.Config()
	cfg.SkillsDir = filepath.Join(b.Root, "no-skills")
	r = New(cfg)
	other := b.workDir(otherThr)
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	r.linkSkills(other)
	if _, err := os.Lstat(filepath.Join(other, ".agents", "skills")); err == nil {
		t.Error("a missing skills dir made a link")
	}
}

// A thread's session keeps the effort it was started with: OpenCode refuses a resumed session at
// another effort.
func TestSessionKeepsItsEffort(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	writeFake(t, b, recordTwice(b, eventLine("text", sesA, textPart("one"))+"\n"))
	r := New(b.Config())

	first := b.task("one")
	first.Effort = "high"
	if rep := ctxRun(r, first); rep.State != StateDone {
		t.Fatalf("first run: %q %q", rep.State, rep.Reason)
	}
	if argv := b.argv(); len(argv) < 6 || argv[5] != "teknoir/claude-sonnet-5-5#high" {
		t.Errorf("first run argv %q", argv)
	}

	second := b.task("two")
	second.Effort = "low"
	if rep := ctxRun(r, second); rep.State != StateDone {
		t.Fatalf("second run: %q %q", rep.State, rep.Reason)
	}
	argv := b.argv()
	if len(argv) < 6 || argv[5] != "teknoir/claude-sonnet-5-5#high" {
		t.Errorf("the second run changed the effort of the session: %q", argv)
	}
	if !hasArgv(argv, "--session", sesA) {
		t.Errorf("the second run does not continue %s: %q", sesA, argv)
	}
}

// An entry without an effort was written before the effort was recorded: the task's effort wins,
// and is stored with the session.
func TestSessionWithoutEffortTakesTheTaskEffort(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	path := filepath.Join(b.Home, ".local", "state", "teknoirbot", "sessions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"threads":{"`+testThread+`":{"session":"`+sesB+`","updated":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFake(t, b, fakeScript(b.Root, eventLine("text", sesA, textPart("hi"))+"\n", 0))
	task := b.task("go")
	task.Effort = "low"
	if rep := ctxRun(New(b.Config()), task); rep.State != StateDone {
		t.Fatalf("run: %q %q", rep.State, rep.Reason)
	}
	argv := b.argv()
	if len(argv) < 6 || argv[5] != "teknoir/claude-sonnet-5-5#low" {
		t.Errorf("the entry without an effort did not take the task's: %q", argv)
	}
	if !hasArgv(argv, "--session", sesB) {
		t.Errorf("the run did not continue %s: %q", sesB, argv)
	}
	session, effort := loadSessions(b.Home).get(testThread)
	if session != sesA || effort != "low" {
		t.Errorf("the store holds (%q, %q), want (%q, %q)", session, effort, sesA, "low")
	}
}

// A new thread gets its own effort.
func TestNewThreadKeepsItsOwnEffort(t *testing.T) {
	needUnix(t)
	b := newBed(t)
	path := filepath.Join(b.Home, ".local", "state", "teknoirbot", "sessions.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"threads":{"`+testThread+`":{"session":"`+sesB+`","effort":"high","updated":1}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFake(t, b, fakeScript(b.Root, eventLine("text", sesA, textPart("hi"))+"\n", 0))
	task := Task{TurnID: otherTurn, ThreadID: otherThr, Prompt: "go", Effort: "low"}
	if rep := ctxRun(New(b.Config()), task); rep.State != StateDone {
		t.Fatalf("run: %q %q", rep.State, rep.Reason)
	}
	argv := b.argv()
	if len(argv) < 6 || argv[5] != "teknoir/claude-sonnet-5-5#low" {
		t.Errorf("a new thread did not get its own effort: %q", argv)
	}
	if hasFlag(argv, "--session") {
		t.Errorf("a new thread got --session: %q", argv)
	}
	if _, effort := loadSessions(b.Home).get(otherThr); effort != "low" {
		t.Errorf("the new thread's effort is stored as %q", effort)
	}
}
