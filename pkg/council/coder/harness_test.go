package coder

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teknoir/cli/pkg/council/model"
)

// Test ids: trn_ and thr_ and 22 base64url characters each.
const (
	testTurn   = "trn_AAAAAAAAAAAAAAAAAAAAAA"
	testThread = "thr_BBBBBBBBBBBBBBBBBBBBBB"
	otherTurn  = "trn_CCCCCCCCCCCCCCCCCCCCCC"
	otherThr   = "thr_DDDDDDDDDDDDDDDDDDDDDD"
	sesA       = "ses_e35e8ca76ffexxW1CZWtFwHAXd"
	sesB       = "ses_Zzzzzzzzzzzzzzzzzzzzzzzz"
)

// needUnix skips the tests that start processes.
func needUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the process tests need a unix shell")
	}
}

// TestBed is one test's temp dirs and fake OpenCode.
type TestBed struct {
	t          *testing.T
	Root       string
	WorkRoot   string
	Home       string
	ConfigFile string
	ConfigHome string
	Bin        string
	ScriptPath string
	// EnvironFunc replaces Environ, for a test that needs a different PATH.
	EnvironFunc func() []string
}

// newBed makes the temp dirs around a fake OpenCode that does nothing and succeeds.
func newBed(t *testing.T) *TestBed {
	t.Helper()
	root := t.TempDir()
	b := &TestBed{
		t:          t,
		Root:       root,
		WorkRoot:   filepath.Join(root, "work"),
		Home:       filepath.Join(root, "home"),
		ConfigFile: filepath.Join(root, "etc", "opencode.json"),
		ConfigHome: filepath.Join(root, "xdg"),
		Bin:        filepath.Join(root, "bin"),
	}
	for _, dir := range []string{b.WorkRoot, b.Home, filepath.Dir(b.ConfigFile), filepath.Join(b.ConfigHome, "opencode"), b.Bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// The image's config, and the empty opencode directory OpenCode needs under XDG_CONFIG_HOME.
	if err := os.WriteFile(b.ConfigFile, []byte("{\"$schema\":\"https://opencode.ai/config.json\"}\n"), 0o600); err != nil {
		t.Fatalf("write the config file: %v", err)
	}
	b.ScriptPath = filepath.Join(b.Bin, "opencode")
	b.setScript("", 0)
	return b
}

// setScript installs a fake OpenCode that records its argv, directory and environment in the
// bed's root and then prints events, exiting with exitCode.
func (b *TestBed) setScript(events string, exitCode int) {
	writeFake(b.t, b, fakeScript(b.Root, events, exitCode))
}

// Config is the runner Config of a test bed.
func (b *TestBed) Config() Config {
	return Config{
		Binary:     b.Bin + "/opencode",
		Model:      "teknoir/claude-sonnet-5-5",
		ConfigFile: b.ConfigFile,
		ConfigHome: b.ConfigHome,
		WorkRoot:   b.WorkRoot,
		Home:       b.Home,
		Timeout:    20 * time.Second,
		KillGrace:  500 * time.Millisecond,
		// The test binary's own environment is the pass-through source, so the script's
		// interpreter and git are found.
		Environ: b.Environ,
	}
}

// Environ is the pass-through source of a test: this process's environment, unless the test
// replaced it (so the script can still find git where a test keeps it).
func (b *TestBed) Environ() []string {
	if b.EnvironFunc != nil {
		return b.EnvironFunc()
	}
	return os.Environ()
}

// workDir is where the script runs.
func (b *TestBed) workDir(thread string) string {
	return filepath.Join(b.WorkRoot, "chat-"+thread[len("thr_"):len("thr_")+8])
}

// read is a file the script wrote, or "".
func (b *TestBed) read(name string) string {
	data, err := os.ReadFile(filepath.Join(b.Root, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// awaitFile waits for the script to write a file.
func (b *TestBed) awaitFile(name string) {
	b.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b.read(name) != "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.t.Fatalf("%s was not written", name)
}

// updateLog collects the updates of a task from one goroutine.
type updateLog struct {
	mu   sync.Mutex
	list []Update
}

// handler is the task's OnUpdate.
func (u *updateLog) handler(up Update) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.list = append(u.list, up)
}

// updates is what was collected.
func (u *updateLog) updates() []Update {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]Update(nil), u.list...)
}

// kinds is the sequence of update kinds.
func (u *updateLog) kinds() []string {
	out := []string{}
	for _, up := range u.updates() {
		out = append(out, up.Kind)
	}
	return out
}

// task is a valid task on the test thread.
func (b *TestBed) task(prompt string) Task {
	return Task{TurnID: testTurn, ThreadID: testThread, Prompt: prompt, Effort: "medium"}
}

// fakeScript is a shell script that writes its argv, its directory and its environment to files
// in dir, then prints the given JSON lines.
//
// The script cannot read test variables (the environment is an allowlist), so everything it does
// is baked into its text.
func fakeScript(dir, events string, exitCode int) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	// One argument per line as NUL-separated text, and the directory and environment.
	b.WriteString("printf '%s\\0' \"$@\" > " + shellQuote(filepath.Join(dir, "argv")) + "\n")
	b.WriteString("pwd > " + shellQuote(filepath.Join(dir, "pwd")) + "\n")
	b.WriteString("env > " + shellQuote(filepath.Join(dir, "env")) + "\n")
	if events != "" {
		b.WriteString("cat <<'TEKNOIR_EVENTS'\n")
		b.WriteString(events)
		if !strings.HasSuffix(events, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("TEKNOIR_EVENTS\n")
	}
	if exitCode != 0 {
		b.WriteString("exit " + strconv.Itoa(exitCode) + "\n")
	}
	return b.String()
}

// shellQuote quotes a path for the shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// argv reads the recorded arguments.
func (b *TestBed) argv() []string {
	data := b.read("argv")
	if data == "" {
		return nil
	}
	parts := strings.Split(data, "\x00")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	return parts
}

// eventLine builds one JSON event line.
func eventLine(kind, session, part string) string {
	if part == "" {
		return `{"type":"` + kind + `","timestamp":1794391881385,"sessionID":"` + session + `"}`
	}
	return `{"type":"` + kind + `","timestamp":1794391881385,"sessionID":"` + session + `","part":` + part + `}`
}

// stepJSON is a step_finish part with the given token counts.
func stepJSON(input, output, reasoning, cacheRead, cacheWrite int64) string {
	return `{"type":"step-finish","reason":"stop","cost":0,"tokens":{"input":` +
		strconv.FormatInt(input, 10) + `,"output":` + strconv.FormatInt(output, 10) +
		`,"reasoning":` + strconv.FormatInt(reasoning, 10) + `,"cache":{"read":` +
		strconv.FormatInt(cacheRead, 10) + `,"write":` + strconv.FormatInt(cacheWrite, 10) + `}}}`
}

// textPart is a text part with the given text, JSON-escaped.
func textPart(text string) string {
	return `{"id":"prt_c","type":"text","text":` + jsonString(text) + `,"time":{"start":1,"end":2}}`
}

// toolPart is a tool part.
func toolPart(tool, status string) string {
	return `{"partID":"prt_b","type":"tool","id":"toolu_1","tool":"` + tool +
		`","state":{"status":"` + status + `","input":{"command":"echo hi"},"output":"(no output)"}}`
}

// jsonString JSON-escapes s, control characters included.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x1b {
				b.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[(r>>4)&0xf])
				b.WriteByte(hex[r&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// usage is a Usage for comparisons.
func usage(calls, input, output, cacheRead, cacheWrite int64) model.Usage {
	return model.Usage{Calls: calls, Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite}
}

// ctxRun runs a task with a background context.
func ctxRun(r *Runner, task Task) Report {
	return r.Run(context.Background(), task)
}

// readFile is a file's text, or "".
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// parsePart is an event part as the stream reader makes it.
func parsePart(t *testing.T, part string) *eventPart {
	t.Helper()
	var p eventPart
	if err := json.Unmarshal([]byte(part), &p); err != nil {
		t.Fatalf("part %s: %v", part, err)
	}
	return &p
}

// newReader is a bufio.Reader over text.
func newReader(text string) *bufio.Reader {
	return bufio.NewReaderSize(strings.NewReader(text), 64<<10)
}

// hasEnv reports whether the env text has the exact line want.
func hasEnv(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// hasName reports whether the env text sets a variable at all.
func hasName(text, name string) bool {
	prefix := name + "="
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// hasArgv reports whether argv has flag followed by value.
func hasArgv(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// hasFlag reports whether argv carries flag at all.
func hasFlag(argv []string, flag string) bool {
	for _, arg := range argv {
		if arg == flag {
			return true
		}
	}
	return false
}

// splitNUL splits a recorded argument list.
func splitNUL(s string) []string {
	parts := strings.Split(s, "\x00")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	return parts
}

// padAlnum is i written as n letters and digits: what a session id may hold.
func padAlnum(i, n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for j := n - 1; j >= 0; j-- {
		b[j] = alphabet[i%len(alphabet)]
		i /= len(alphabet)
	}
	return string(b)
}

// padBase64 is i written as n base64url characters: what a turn or thread id may hold.
func padBase64(i, n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	b := make([]byte, n)
	for j := n - 1; j >= 0; j-- {
		b[j] = alphabet[i%64]
		i /= 64
	}
	return string(b)
}

// contains reports whether list has want.
func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
