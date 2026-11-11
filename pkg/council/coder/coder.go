// Package coder runs the council's Coder member: OpenCode 2.2.7, headless, as a child process.
//
// One Run is one coder task. It starts OpenCode in the turn's work directory with a fixed,
// allowlisted environment, reads its JSON event stream into progress updates and a report, and
// stops it when the turn's budget is reached, the deadline passes or the turn is cancelled.
//
// Run never returns an error: every failure is a failed Report carrying a reason.
package coder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/teknoir/cli/pkg/council/model"
)

// The defaults of a zero Config field.
const (
	DefaultBinary     = "opencode"
	DefaultModel      = "teknoir/claude-sonnet-5-5"
	DefaultConfigFile = "/etc/teknoirbot/opencode.json"
	DefaultWorkRoot   = "/work"
	DefaultHome       = "/work/home"
	DefaultSkillsDir  = "/etc/teknoirbot/kit/skills"
	DefaultTimeout    = 10 * time.Minute
	DefaultKillGrace  = 10 * time.Second
	// DefaultMinFreeBytes is the least free space under the work root a task may start with, and
	// DefaultStopFreeBytes the least it may go on with: the work root is on the bot's own disk, and
	// a full disk breaks everything else the bot does.
	DefaultMinFreeBytes  = 2 << 30
	DefaultStopFreeBytes = 512 << 20
	// DefaultDiskCheckEvery is how often the free space is read while a task runs.
	DefaultDiskCheckEvery = 5 * time.Second
	// DefaultEffort is what an effort outside "low" and "medium" becomes: the coder is never given
	// more than high.
	DefaultEffort = "high"
)

// The limits of what leaves the package and what it reads.
const (
	// MaxPromptBytes is the most a task prompt may be.
	MaxPromptBytes = 32768
	// MaxUpdateText is the most text an Update carries.
	MaxUpdateText = 2 << 10
	// MaxUpdateTitle is the most a tool Update's title carries.
	MaxUpdateTitle = 200
	// MaxReportText is the most text a Report carries.
	MaxReportText = 16 << 10
	// MaxErrorBytes is the most a Report's Error carries.
	MaxErrorBytes = 2 << 10
	// MaxChangedLines and MaxChangedLine bound one line of Changed, and how many there are.
	MaxChangedLines = 200
	MaxChangedLine  = 300
	// MaxEventLine is the longest stdout line that is read; a longer one is skipped whole.
	MaxEventLine = 1 << 20
	// MaxStderrTail is the most stderr that is kept, for the Error of an exit or crash report.
	MaxStderrTail = 2 << 10
	// MaxSessions is the most threads the session file remembers.
	MaxSessions = 1000
)

// The time limits of the commands the runner runs itself.
const (
	// waitDelay is how long Wait waits for a grandchild that holds stdout after the process exited,
	// before it closes the pipes and returns.
	waitDelay = 2 * time.Second
	// gitTimeout is the wall time of one git command, and gitWaitDelay the time a grandchild
	// holding git's output is given after git exited.
	gitTimeout   = 10 * time.Second
	gitWaitDelay = time.Second
)

// excludeLine keeps the skills link out of Changed and out of a commit of everything.
const excludeLine = "/.agents/"

// The kinds of Update.
const (
	KindWait    = "wait"
	KindStep    = "step"
	KindTool    = "tool"
	KindMessage = "message"
)

// The states of a Report.
const (
	StateDone   = "done"
	StateFailed = "failed"
)

// The reasons of a failed Report.
const (
	// ReasonBadTask is a task that may not be run at all: nothing was started.
	ReasonBadTask = "bad_task"
	// ReasonCancelled is a turn whose context ended, while waiting for the runner or running.
	ReasonCancelled = "cancelled"
	// ReasonStart is OpenCode that could not be started.
	ReasonStart = "start"
	// ReasonTimeout is a task that outlived Config.Timeout.
	ReasonTimeout = "timeout"
	// ReasonBudget is a task whose token budget OnStep refused to extend.
	ReasonBudget = "budget"
	// ReasonError is OpenCode reporting an error event.
	ReasonError = "error"
	// ReasonExit is a non-zero exit without an error event.
	ReasonExit = "exit"
	// ReasonCrash is a process killed by a signal the runner did not send.
	ReasonCrash = "crash"
	// ReasonDisk is a disk with too little free space: nothing was started, or the run was
	// stopped before the disk filled.
	ReasonDisk = "disk"
)

// The Coder's disk messages.
const (
	// msgDiskBefore names the least free space, the root and what to do about it.
	msgDiskBefore = "The bot's disk has less than %s free: free space under %s before asking for this."
	// msgDiskStopped is what a run stopped by the disk reports.
	msgDiskStopped = "Stopped: the bot's disk is almost full."
)

// Config is fixed per daemon. Zero fields take the defaults.
type Config struct {
	// Binary is the OpenCode executable, looked up on PATH at Run time.
	Binary string
	// Model is the model OpenCode asks for, without the "#<effort>" suffix.
	Model string
	// ConfigFile is passed as OPENCODE_CONFIG.
	ConfigFile string
	// WorkRoot holds one work directory per thread.
	WorkRoot string
	// Home is the child's HOME and the root of its XDG directories.
	Home string
	// SkillsDir is linked into every work directory as .agents/skills.
	SkillsDir string
	// Timeout is the wall time of one OpenCode process.
	Timeout time.Duration
	// KillGrace is how long SIGTERM is given before SIGKILL.
	KillGrace time.Duration
	// Statfs reports the free bytes of the volume at path. Nil means the platform's own; a
	// platform without one reports an error, and the disk checks are skipped.
	Statfs func(path string) (free uint64, err error)
	// MinFreeBytes is the least free space under WorkRoot a task may start with: below it no
	// process starts and the report is disk.
	MinFreeBytes uint64
	// StopFreeBytes is the least free space a running task may go on with: below it the process
	// group is stopped and the report is disk.
	StopFreeBytes uint64
	// DiskCheckEvery is how often a running task's free space is read.
	DiskCheckEvery time.Duration
	// Environ is where the pass-through variables are read from; nil means os.Environ.
	Environ func() []string
	// Logf is the operator log: ids, counts, exit codes and reasons only.
	Logf func(format string, args ...any)
}

// Task is one coder task.
type Task struct {
	// TurnID is "trn_" and 22 base64url characters.
	TurnID string
	// ThreadID is "thr_" and 22 base64url characters.
	ThreadID string
	// Prompt is the coder task, 1 to MaxPromptBytes bytes of UTF-8.
	Prompt string
	// Effort is "low", "medium" or "high"; anything else means "high".
	Effort string
	// OnUpdate gets the progress updates, in order, from one goroutine. It must not block.
	OnUpdate func(Update)
	// OnStep gets the usage of each finished OpenCode step. Returning false stops OpenCode: the
	// turn's token budget is reached.
	OnStep func(step model.Usage) bool
}

// Update is one progress update of a coder task.
type Update struct {
	Kind  string `json:"kind"`            // "wait" | "step" | "tool" | "message"
	Title string `json:"title,omitempty"` // tool: "<tool> <status>"
	Text  string `json:"text,omitempty"`  // message: the text part
}

// Report is what one coder task did.
type Report struct {
	State     string        `json:"state"`            // "done" | "failed"
	Reason    string        `json:"reason,omitempty"` // "" when done
	Text      string        `json:"text"`             // the last text part of the answer
	Error     string        `json:"error,omitempty"`  // what went wrong, for a failure
	Changed   []string      `json:"changed"`          // git status --porcelain of the work dir
	SessionID string        `json:"sessionId,omitempty"`
	Steps     int           `json:"steps"`
	Tools     int           `json:"tools"`
	Usage     model.Usage   `json:"usage"`
	Duration  time.Duration `json:"durationMs"`
}

// reportJSON is Report with the duration in milliseconds, as the bot API reads it.
type reportJSON struct {
	State      string      `json:"state"`
	Reason     string      `json:"reason,omitempty"`
	Text       string      `json:"text"`
	Error      string      `json:"error,omitempty"`
	Changed    []string    `json:"changed"`
	SessionID  string      `json:"sessionId,omitempty"`
	Steps      int         `json:"steps"`
	Tools      int         `json:"tools"`
	Usage      model.Usage `json:"usage"`
	DurationMs int64       `json:"durationMs"`
}

// MarshalJSON writes the duration in milliseconds.
func (r Report) MarshalJSON() ([]byte, error) {
	changed := r.Changed
	if changed == nil {
		changed = []string{}
	}
	return json.Marshal(reportJSON{
		State:      r.State,
		Reason:     r.Reason,
		Text:       r.Text,
		Error:      r.Error,
		Changed:    changed,
		SessionID:  r.SessionID,
		Steps:      r.Steps,
		Tools:      r.Tools,
		Usage:      r.Usage,
		DurationMs: r.Duration.Milliseconds(),
	})
}

// The shapes an id must have.
var (
	turnPattern    = regexp.MustCompile(`^trn_[A-Za-z0-9_-]{22}$`)
	threadPattern  = regexp.MustCompile(`^thr_[A-Za-z0-9_-]{22}$`)
	sessionPattern = regexp.MustCompile(`^ses_[A-Za-z0-9]{1,64}$`)
)

// The variables a child inherits, and nothing else.
var passthrough = []string{
	"PATH",
	"LANG",
	"LC_ALL",
	"TZ",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"KUBERNETES_SERVICE_HOST",
	"KUBERNETES_SERVICE_PORT",
	"TNCTL_BOT_KEY_FILE",
	"TEKNOIRBOT_API",
	"TEKNOIRBOT_NAME",
	"TEKNOIRBOT_NAMESPACE",
	"TEKNOIR_DOMAIN",
	"BUILDKIT_HOST",
}

// passSet is passthrough as a set.
var passSet = func() map[string]bool {
	m := make(map[string]bool, len(passthrough))
	for _, name := range passthrough {
		m[name] = true
	}
	return m
}()

// Runner runs one OpenCode process at a time.
type Runner struct {
	cfg Config
	// sem is the lock: a buffered channel, so waiting for it can select on the turn's context.
	sem chan struct{}
	// mu guards the once-only log of a statfs that fails.
	mu            sync.Mutex
	diskErrLogged bool
}

// New returns the coder runner of a daemon. Zero Config fields take the defaults.
func New(cfg Config) *Runner {
	if cfg.Binary == "" {
		cfg.Binary = DefaultBinary
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.ConfigFile == "" {
		cfg.ConfigFile = DefaultConfigFile
	}
	if cfg.WorkRoot == "" {
		cfg.WorkRoot = DefaultWorkRoot
	}
	if cfg.Home == "" {
		cfg.Home = DefaultHome
	}
	if cfg.SkillsDir == "" {
		cfg.SkillsDir = DefaultSkillsDir
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.KillGrace <= 0 {
		cfg.KillGrace = DefaultKillGrace
	}
	if cfg.Statfs == nil {
		cfg.Statfs = defaultStatfs
	}
	if cfg.MinFreeBytes == 0 {
		cfg.MinFreeBytes = DefaultMinFreeBytes
	}
	if cfg.StopFreeBytes == 0 {
		cfg.StopFreeBytes = DefaultStopFreeBytes
	}
	if cfg.DiskCheckEvery <= 0 {
		cfg.DiskCheckEvery = DefaultDiskCheckEvery
	}
	if cfg.Environ == nil {
		cfg.Environ = os.Environ
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Runner{cfg: cfg, sem: make(chan struct{}, 1)}
}

// Run runs one coder task. It never returns an error: every failure is a failed Report.
//
// Only one OpenCode process runs per Runner: a second Run waits for the first, and stops waiting
// when its context ends.
func (r *Runner) Run(ctx context.Context, task Task) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	rep := Report{State: StateFailed, Changed: []string{}}

	if err := validateTask(task); err != nil {
		rep.Reason = ReasonBadTask
		rep.Error = err.Error()
		r.cfg.Logf("coder: turn %s thread %s: bad task: %v", idForLog(task.TurnID), idForLog(task.ThreadID), err)
		return rep
	}
	turn, thread := task.TurnID, task.ThreadID

	if !r.acquire(ctx, task) {
		rep.Reason = ReasonCancelled
		r.cfg.Logf("coder: turn %s: cancelled while waiting for the runner", turn)
		return rep
	}
	defer r.release()
	if ctx.Err() != nil {
		rep.Reason = ReasonCancelled
		r.cfg.Logf("coder: turn %s: cancelled before the start", turn)
		return rep
	}

	// The work root is on the bot's own disk: a task that would fill it must not start.
	if free, ok := r.diskFree(); ok && free < r.cfg.MinFreeBytes {
		rep.Reason = ReasonDisk
		rep.Error = fmt.Sprintf(msgDiskBefore, humanBytes(r.cfg.MinFreeBytes), r.cfg.WorkRoot)
		r.cfg.Logf("coder: turn %s: disk: %d bytes free under %s", turn, free, r.cfg.WorkRoot)
		return rep
	}

	env := r.environment(turn)
	dir, err := r.workDir(thread)
	if err != nil {
		rep.Reason = ReasonStart
		rep.Error = cutTotal(sanitize(err.Error()), MaxErrorBytes)
		r.cfg.Logf("coder: turn %s: %v", turn, err)
		return rep
	}
	r.gitInit(dir, env)
	r.linkSkills(dir)
	// Every run, not only the one that made the repository: a work dir from before this existed
	// has the link and no exclude line.
	r.ensureExcluded(dir)

	// A thread's session keeps the effort it was started with: OpenCode, resumed at another, sends
	// a mid-conversation system message the platform's model proxy does not allow.
	sessions := loadSessions(r.cfg.Home)
	session, kept := sessions.get(thread)
	effort := effortOf(task.Effort)
	if session != "" && kept != "" {
		if kept != effort {
			r.cfg.Logf("coder: turn %s thread %s: keeps effort %s of its session (asked %s)", turn, thread, kept, effort)
		}
		effort = kept
	}
	args := r.argv(task, session, effort)

	bin, err := lookPath(r.cfg.Binary, env)
	if err != nil {
		rep.Reason = ReasonStart
		rep.Error = cutTotal(sanitize("council coder: "+err.Error()), MaxErrorBytes)
		r.cfg.Logf("coder: turn %s: start: %v", turn, err)
		return rep
	}

	outR, outW := io.Pipe()
	tail := &tailBuffer{max: MaxStderrTail}

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = outW
	cmd.Stderr = tail
	// A grandchild that holds stdout past the exit must not hold Wait: after waitDelay the pipes
	// are closed and Wait returns. It is short, so a normal run is not held back either.
	cmd.WaitDelay = waitDelay
	setProcessGroup(cmd)

	started := time.Now()
	if err := cmd.Start(); err != nil {
		_ = outR.Close()
		_ = outW.Close()
		rep.Reason = ReasonStart
		rep.Error = cutTotal(sanitize("council coder: start "+r.cfg.Binary+": "+err.Error()), MaxErrorBytes)
		r.cfg.Logf("coder: turn %s: start: %v", turn, err)
		return rep
	}
	// Setpgid makes the process the head of a new group, so its id is the group's, and it stays
	// the group's id after the process is reaped.
	pgid := cmd.Process.Pid
	r.cfg.Logf("coder: turn %s thread %s: started with effort %s", turn, thread, effort)

	st := &state{}
	budget := make(chan struct{}, 1)
	readDone := make(chan struct{})
	go func() {
		defer func() { _ = outR.Close() }()
		r.consume(outR, task, st, budget)
		close(readDone)
	}()

	procDone := make(chan struct{})
	go func() {
		// The exit state is read from cmd.ProcessState; Wait's error adds nothing to it, and a
		// WaitDelay overrun is not a failure of its own.
		_ = cmd.Wait()
		close(procDone)
	}()

	// The disk is watched while the process runs: one that fills it stops like a cancel.
	diskStop := make(chan struct{})
	diskFull := make(chan struct{}, 1)
	diskDone := make(chan struct{})
	go func() {
		defer close(diskDone)
		r.watchDisk(ctx, procDone, diskStop, diskFull)
	}()
	defer func() { <-diskDone }()
	defer close(diskStop)

	reason, exited := r.watch(ctx, procDone, budget, diskFull)
	if reason != "" {
		r.stop(pgid, procDone)
		select {
		case <-procDone:
			exited = true
		default:
		}
	}
	rep.Duration = time.Since(started)
	// Every path ends here, a normal exit too: the background processes the task started (dev
	// servers, watchers) would otherwise keep the work dir changing under Changed and pile up.
	// Killing them before the reader is waited for is also what lets a grandchild that held the
	// pipes go.
	killGroup(pgid)
	// Wait has returned, so the pipes hold every byte the process wrote; closing our end is what
	// ends the reader.
	_ = outW.Close()
	<-readDone

	rep.Steps, rep.Tools, rep.Usage, rep.SessionID, rep.Text = st.report()
	rep.Text = cutText(sanitize(rep.Text), MaxReportText)

	if rep.SessionID != "" {
		// Also after a failed run: a retry continues the session instead of starting over.
		if err := sessions.store(thread, rep.SessionID, effort); err != nil {
			r.cfg.Logf("coder: turn %s: store the session: %v", turn, err)
		}
		r.cfg.Logf("coder: turn %s: session %s", turn, rep.SessionID)
	}

	switch {
	case reason != "":
		rep.Reason = reason
		if reason == ReasonDisk {
			rep.Error = msgDiskStopped
		}
	case st.failed():
		rep.Reason = ReasonError
		rep.Error = cutTotal(sanitize(st.errorText()), MaxErrorBytes)
	default:
		code, signal := exitState(cmd, exited)
		switch {
		case signal != "":
			rep.Reason = ReasonCrash
			rep.Error = failureText("opencode was killed by signal "+signal, tail.String())
		case !exited:
			// The process outlived SIGKILL and one more KillGrace: nothing is left to classify it by.
			rep.Reason = ReasonCrash
			rep.Error = failureText("opencode did not exit", tail.String())
		case code != 0:
			rep.Reason = ReasonExit
			rep.Error = failureText(fmt.Sprintf("opencode exited with code %d", code), tail.String())
		}
	}
	if rep.Reason == "" {
		rep.State = StateDone
	}
	rep.Changed = r.changed(dir, env)

	r.cfg.Logf("coder: turn %s: %s in %s, steps %d tools %d, usage %d/%d",
		turn, outcome(rep), rep.Duration.Round(time.Millisecond), rep.Steps, rep.Tools,
		rep.Usage.Input, rep.Usage.Output)
	return rep
}

// acquire takes the runner's lock, sending one wait update when another task holds it. It reports
// false when ctx ended first.
func (r *Runner) acquire(ctx context.Context, task Task) bool {
	select {
	case r.sem <- struct{}{}:
		return true
	default:
	}
	if task.OnUpdate != nil {
		task.OnUpdate(Update{Kind: KindWait})
	}
	select {
	case r.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// release gives the runner's lock back.
func (r *Runner) release() { <-r.sem }

// watch waits for the process to end and for the four things that stop it early. It returns the
// reason of an early stop, and whether the process was seen to exit.
func (r *Runner) watch(ctx context.Context, procDone <-chan struct{}, budget <-chan struct{}, disk <-chan struct{}) (string, bool) {
	timer := time.NewTimer(r.cfg.Timeout)
	defer timer.Stop()
	for {
		// An exit is always the right answer, even when a stop was due at the same moment.
		select {
		case <-procDone:
			return "", true
		default:
		}
		select {
		case <-procDone:
			return "", true
		case <-ctx.Done():
			return ReasonCancelled, false
		case <-timer.C:
			return ReasonTimeout, false
		case <-budget:
			return ReasonBudget, false
		case <-disk:
			return ReasonDisk, false
		}
	}
}

// watchDisk reads the free space under the work root while the process runs, and signals full once
// it falls below StopFreeBytes. It ends with the process, the context, or the runner's stop.
func (r *Runner) watchDisk(ctx context.Context, procDone <-chan struct{}, stop <-chan struct{}, full chan<- struct{}) {
	ticker := time.NewTicker(r.cfg.DiskCheckEvery)
	defer ticker.Stop()
	for {
		select {
		case <-procDone:
			return
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			free, ok := r.diskFree()
			if !ok || free >= r.cfg.StopFreeBytes {
				continue
			}
			select {
			case full <- struct{}{}:
			default:
			}
			return
		}
	}
}

// diskFree is the free space under the work root. A statfs that fails is logged once and the disk
// checks are skipped: a platform that cannot tell is not a full disk, and never fails a task.
func (r *Runner) diskFree() (uint64, bool) {
	free, err := r.cfg.Statfs(r.cfg.WorkRoot)
	if err != nil {
		r.mu.Lock()
		first := !r.diskErrLogged
		r.diskErrLogged = true
		r.mu.Unlock()
		if first {
			r.cfg.Logf("coder: free space under %s is unknown: %v", r.cfg.WorkRoot, err)
		}
		return 0, false
	}
	return free, true
}

// humanBytes is a count of bytes as the disk messages say it: whole GiB, then MiB, then bytes.
func humanBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%d GiB", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}

// stop ends the process: SIGTERM to its group, then SIGKILL to its group after KillGrace. It
// returns once the process is gone or the second KillGrace has passed.
func (r *Runner) stop(pgid int, done <-chan struct{}) {
	select {
	case <-done:
		return
	default:
	}
	_ = signalGroup(pgid, termSignal)
	if r.waitGrace(done) {
		return
	}
	_ = signalGroup(pgid, killSignal)
	r.waitGrace(done)
}

// waitGrace waits for done, or for KillGrace. It reports whether done arrived.
func (r *Runner) waitGrace(done <-chan struct{}) bool {
	t := time.NewTimer(r.cfg.KillGrace)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// exitState is the exit code of the process and, when a signal killed it, the signal's name.
func exitState(cmd *exec.Cmd, exited bool) (int, string) {
	if !exited || cmd.ProcessState == nil {
		return 0, ""
	}
	if name, ok := signalOf(cmd.ProcessState); ok {
		return -1, name
	}
	return cmd.ProcessState.ExitCode(), ""
}

// failureText is the Error of an exit or crash report: what happened, then the end of the stderr
// tail. When the two do not fit, the oldest stderr is what goes: the last lines say why.
func failureText(what, tail string) string {
	what = sanitize(what)
	if tail == "" {
		return cutText(what, MaxErrorBytes)
	}
	const sep = ": "
	budget := MaxErrorBytes - len(what) - len(sep)
	if budget <= 0 {
		return cutText(what, MaxErrorBytes)
	}
	return what + sep + cutTail(tail, budget)
}

// workDir makes the thread's work directory. A link planted where it should be is refused: another
// thread's work dir must not be reached through it.
func (r *Runner) workDir(thread string) (string, error) {
	dir := filepath.Join(r.cfg.WorkRoot, "chat-"+thread[len("thr_"):len("thr_")+8])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("council coder: make the work dir: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", fmt.Errorf("council coder: inspect the work dir: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("council coder: work dir is not a directory")
	}
	return dir, nil
}

// gitInit starts a repository in the work directory on first use, so Changed has something to
// report. A missing git is not an error.
func (r *Runner) gitInit(dir string, env []string) {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return
	}
	git, err := lookPath("git", env)
	if err != nil {
		return
	}
	cmd, cancel := gitCommand(git, dir, env, "init", "-q")
	defer cancel()
	if out, err := cmd.CombinedOutput(); err != nil {
		r.cfg.Logf("coder: git init in the work dir failed: %v (%d bytes of output)", err, len(out))
	}
}

// gitEnv is the environment of a git command: the child's, with git's own user and system
// configuration and its optional locks off. The coder controls .git/config and HOME, so git must
// not read what either holds.
func gitEnv(env []string) []string {
	out := make([]string, 0, len(env)+3)
	out = append(out, env...)
	return append(out,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
	)
}

// gitCommand is a git command of the work dir. A configuration key the work dir sets that makes
// git wait (core.fsmonitor, core.untrackedCache) is switched off on the command line, the command
// has a wall time, and Wait does not wait for a grandchild that holds its output.
func gitCommand(git, dir string, env []string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	full := append([]string{"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false"}, args...)
	cmd := exec.CommandContext(ctx, git, full...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env)
	cmd.WaitDelay = gitWaitDelay
	return cmd, cancel
}

// linkSkills makes the work directory's .agents/skills a symlink to the skills directory.
func (r *Runner) linkSkills(dir string) {
	src := r.cfg.SkillsDir
	info, err := os.Stat(src)
	if err != nil || !info.IsDir() {
		return
	}
	agents := filepath.Join(dir, ".agents")
	// A link where .agents should be is not followed: it would put the skills, and anything
	// written beside them, somewhere outside the work dir.
	if li, err := os.Lstat(agents); err == nil && li.Mode()&os.ModeSymlink != 0 {
		r.cfg.Logf("coder: .agents is a symlink; the skills dir is not linked")
		return
	}
	if err := os.MkdirAll(agents, 0o700); err != nil {
		r.cfg.Logf("coder: make .agents: %v", err)
		return
	}
	dst := filepath.Join(agents, "skills")
	li, err := os.Lstat(dst)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.Symlink(src, dst); err != nil {
			r.cfg.Logf("coder: link the skills dir: %v", err)
		}
	case err != nil:
		r.cfg.Logf("coder: inspect the skills link: %v", err)
	case li.Mode()&os.ModeSymlink != 0:
		if cur, err := os.Readlink(dst); err == nil && filepath.Clean(cur) == filepath.Clean(src) {
			return
		}
		if err := os.Remove(dst); err != nil {
			r.cfg.Logf("coder: replace the skills link: %v", err)
			return
		}
		if err := os.Symlink(src, dst); err != nil {
			r.cfg.Logf("coder: link the skills dir: %v", err)
		}
	default:
		// A real file or directory there is the task's own doing: leave it.
		r.cfg.Logf("coder: .agents/skills is not a symlink; left alone")
	}
}

// ensureExcluded puts the skills link in the repository's own exclude file, so Changed does not
// list it line after line and a task that commits everything does not commit a link to a path
// outside the work dir. Errors are logged and ignored: the run goes on without it.
func (r *Runner) ensureExcluded(dir string) {
	gitDir := filepath.Join(dir, ".git")
	// A .git that is a link or a file (a worktree or a submodule) is left alone: its exclude file
	// is not in this directory.
	if info, err := os.Lstat(gitDir); err != nil || !info.IsDir() {
		return
	}
	infoDir := filepath.Join(gitDir, "info")
	if info, err := os.Lstat(infoDir); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		r.cfg.Logf("coder: .git/info is not a directory; the skills link is not excluded")
		return
	}
	path := filepath.Join(infoDir, "exclude")
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSuffix(line, "\r") == excludeLine {
				return
			}
		}
	case !errors.Is(err, os.ErrNotExist):
		r.cfg.Logf("coder: read .git/info/exclude: %v", err)
		return
	}
	if err := os.Mkdir(infoDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		r.cfg.Logf("coder: make .git/info: %v", err)
		return
	}
	// The line goes on its own: an exclude file without a final newline would otherwise join it
	// to the last rule.
	line := excludeLine + "\n"
	if n := len(data); n > 0 && data[n-1] != '\n' {
		line = "\n" + line
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		r.cfg.Logf("coder: open .git/info/exclude: %v", err)
		return
	}
	if _, err := f.WriteString(line); err != nil {
		r.cfg.Logf("coder: write .git/info/exclude: %v", err)
	}
	if err := f.Close(); err != nil {
		r.cfg.Logf("coder: close .git/info/exclude: %v", err)
	}
}

// changed is git status --porcelain of the work directory, bounded.
func (r *Runner) changed(dir string, env []string) []string {
	changed := []string{}
	git, err := lookPath("git", env)
	if err != nil {
		return changed
	}
	cmd, cancel := gitCommand(git, dir, env, "status", "--porcelain")
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return changed
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		changed = append(changed, cutBytes(line, MaxChangedLine))
		if len(changed) == MaxChangedLines {
			break
		}
	}
	return changed
}

// argv is the OpenCode command line of a task, at the effort the run uses. The message never
// starts with a dash: it starts with a line naming the turn.
func (r *Runner) argv(task Task, session, effort string) []string {
	args := []string{
		"run", "--standalone", "--format", "json",
		"--model", r.cfg.Model + "#" + effort,
	}
	if session != "" {
		args = append(args, "--session", session)
	}
	return append(args, "Task from the Teknoir council (turn "+task.TurnID+"):\n\n"+task.Prompt)
}

// effortOf is the effort the model gets: never above high.
func effortOf(effort string) string {
	switch effort {
	case "low", "medium":
		return effort
	}
	return DefaultEffort
}

// environment is the child's whole environment: the pass-through variables that are set, the XDG
// directories under Home, and the coder's own, and nothing else.
func (r *Runner) environment(turn string) []string {
	env := make([]string, 0, len(passthrough)+8)
	seen := make(map[string]bool, len(passthrough))
	for _, kv := range r.cfg.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !passSet[name] || seen[name] {
			continue
		}
		seen[name] = true
		env = append(env, kv)
	}
	home := r.cfg.Home
	return append(env,
		"HOME="+home,
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"OPENCODE_CONFIG="+r.cfg.ConfigFile,
		"TEKNOIRBOT_TURN="+turn,
		"TEKNOIRBOT_MEMBER=coder",
	)
}

// validateTask refuses a task that may not be run at all.
func validateTask(task Task) error {
	switch {
	case !turnPattern.MatchString(task.TurnID):
		return errors.New("turn id is not trn_ followed by 22 base64url characters")
	case !threadPattern.MatchString(task.ThreadID):
		return errors.New("thread id is not thr_ followed by 22 base64url characters")
	case len(task.Prompt) == 0:
		return errors.New("the prompt is empty")
	case len(task.Prompt) > MaxPromptBytes:
		return fmt.Errorf("the prompt is %d bytes, not at most %d", len(task.Prompt), MaxPromptBytes)
	case !utf8.ValidString(task.Prompt):
		return errors.New("the prompt is not valid UTF-8")
	}
	return nil
}

// lookPath finds name in the PATH of env, the environment the child gets, not this process's.
func lookPath(name string, env []string) (string, error) {
	if strings.ContainsAny(name, `/\`) || runtime.GOOS == "windows" && strings.Contains(name, ":") {
		return name, nil
	}
	exts := []string{""}
	if runtime.GOOS == "windows" {
		exts = append(exts, ".exe")
	}
	for _, dir := range filepath.SplitList(envValue(env, "PATH")) {
		if dir == "" {
			dir = "."
		}
		for _, ext := range exts {
			p := filepath.Join(dir, name+ext)
			if isExecutable(p) {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("%s: %w", name, exec.ErrNotFound)
}

// envValue is a variable's value in env.
func envValue(env []string, name string) string {
	prefix := name + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return kv[len(prefix):]
		}
	}
	return ""
}

// isExecutable reports whether path is an executable file.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// idForLog is an id as it may appear in a log line: a bad id becomes "?".
func idForLog(id string) string {
	if turnPattern.MatchString(id) || threadPattern.MatchString(id) || sessionPattern.MatchString(id) {
		return id
	}
	return "?"
}

// outcome is the report's reason for a log line.
func outcome(rep Report) string {
	if rep.Reason == "" {
		return StateDone
	}
	return rep.Reason
}
