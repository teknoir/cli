package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/teknoir/cli/pkg/api"
)

// chatMaxPrompt is the longest prompt the chat routes take.
const chatMaxPrompt = 16000

// chatMaxAttempts is how many reconnects in a row may fail before the turn is left running: its
// events are kept for the thread, so reloading it later shows the answer.
const chatMaxAttempts = 6

// The pauses of a chat turn's reconnect: 1 s, doubling to 30 s; a Retry-After the platform sends
// replaces the pause, at most 60 s. chatCancelGrace is how long the turn keeps reading after Ctrl-C
// sent the cancel, and chatWorkingAfter how long a connection that carried no event must stay open
// to count as working. They are variables so tests can shorten them.
var (
	chatReconnectDelay = time.Second
	chatReconnectMax   = 30 * time.Second
	chatRetryAfterMax  = 60 * time.Second
	chatRetryAfterUnit = time.Second
	chatCancelGrace    = 10 * time.Second
	chatWorkingAfter   = 30 * time.Second
)

// errChatInterrupted is what a turn returns when a second Ctrl-C leaves it. It is a sentinel: the
// conversation compares it and leaves too.
var errChatInterrupted = newErr(codeError, "interrupted")

// errChatCancelTimeout is what a turn returns when it did not end within chatCancelGrace of the
// first Ctrl-C. The conversation shows it and goes on: the person asked to stop the turn, not to
// leave.
var errChatCancelTimeout = newErr(codeError, "the turn did not end within %s of the cancel; it may still finish on the bot", chatCancelGrace)

// stdoutIsTerminal says whether stdout is a terminal: the Captain's answer is then printed as it
// comes. It is a variable so that tests can say yes.
var stdoutIsTerminal = func() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// chatSignals is where Ctrl-C arrives during a chat turn (SIGTERM keeps its meaning: the process
// ends). It is a variable so that a test can drive the cancel path with a channel of its own; stop
// ends the notification.
var chatSignals = func() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt)
	return ch, func() { signal.Stop(ch) }
}

type chatFlags struct {
	thread    string
	newThread bool
	follow    bool
	mode      string
	member    string
	effort    string
	json      bool
}

func newChatCmd(e *cliEnv) *cobra.Command {
	var f chatFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "chat <bot> [prompt...]",
		Short:        "Chat with a TeknoirBot's council: the Captain, Fleet, Coder and Critic",
		Long: `Chat with a TeknoirBot's council, and read its answer as it is written.

With a prompt the command runs one turn and prints the answer: stdout gets the answer (as it is
written when stdout is a terminal, at the end otherwise) and stderr the progress of the council.
Without one, in a terminal, it starts a conversation: /new starts a new thread, /cancel cancels the
thread's open turn, /quit ends it, and @fleet <text> or @coder <text> asks one member alone.

The chat's thread is printed on stderr when it is made, so it can be continued later with
--thread <id>. Ctrl-C cancels the running turn (a second one leaves at once).

With --thread <id> --follow the command does not ask anything: it follows the thread's last turn,
picking up the answer where it is when that turn still runs, and printing it when it already ended.`,
		Example: `  tnctl chat scout "what is running on my robot?"
  tnctl chat scout --thread thr_123 "and the pods?"
  tnctl chat scout --thread thr_123 --follow
  tnctl chat scout --mode quick "list the devices"
  tnctl chat threads scout`,
		Args: argsRange(1, -1, "chat needs a bot, for example: tnctl chat scout \"what is running?\""),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := chatValidate(f); err != nil {
				return err
			}
			bot := args[0]
			prompt := strings.Join(args[1:], " ")
			// --follow asks nothing, so it never reads stdin: through ssh or inside a while-read loop
			// stdin is an open pipe that would block it, or swallow the loop's own input.
			if prompt == "" && !f.follow && !stdinIsTerminal() {
				b, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				prompt = strings.TrimSpace(string(b))
			}
			if utf8.RuneCountInString(prompt) > chatMaxPrompt {
				return usageErr("the prompt is longer than %d characters", chatMaxPrompt)
			}
			if f.follow && prompt != "" {
				return usageErr("--follow follows the thread's last turn: give no prompt")
			}
			repl := prompt == "" && !f.follow
			if repl {
				if f.json {
					return usageErr("--json needs a prompt: the conversation is not available with --json")
				}
				if !stdinIsTerminal() || !stdoutIsTerminal() {
					return usageErr("chat needs a prompt: give it as arguments, or run tnctl chat <bot> in a terminal to talk")
				}
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			info, _, err := fl.ChatInfo(ctx, bot)
			if err != nil {
				return chatInfoError(err)
			}
			if !info.CanChat {
				reason := terminalText(cleanBotText(info.Reason))
				if reason == "" {
					reason = "the platform did not say why"
				}
				return newErr(codeAuth, "you cannot chat with %s: %s", bot, reason)
			}
			sigs, stopSigs := chatSignals()
			defer stopSigs()
			if f.follow {
				return chatFollow(ctx, cmd, fl, bot, f.thread, f, sigs)
			}
			if !repl {
				return chatOnce(ctx, cmd, fl, bot, prompt, f, sigs)
			}
			return chatRepl(ctx, cmd, fl, bot, info, f, sigs)
		},
	}
	c.Flags().StringVar(&f.thread, "thread", "", "continue this thread (the default is a new one)")
	c.Flags().BoolVar(&f.newThread, "new", false, "start a new thread (with --thread: a usage error)")
	c.Flags().BoolVar(&f.follow, "follow", false, "follow the thread's last turn instead of asking (needs --thread, and no prompt)")
	c.Flags().StringVar(&f.mode, "mode", "", "how the council answers: auto, quick or council")
	c.Flags().StringVar(&f.member, "member", "", "ask one member alone: fleet or coder")
	c.Flags().StringVar(&f.effort, "effort", "", "how hard the members think: low, medium or high")
	c.Flags().BoolVar(&f.json, "json", false, "print each event of the turn as one JSON line")
	c.AddCommand(newChatThreadsCmd(e))
	return c
}

func newChatThreadsCmd(e *cliEnv) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "threads <bot>",
		Short:        "List your chat threads with a TeknoirBot, newest first",
		Long: `List the chat threads you have with a TeknoirBot: id, title, how many turns it has, the state of
the last turn and when it last changed. Continue one with tnctl chat <bot> --thread <id>.`,
		Example: `  tnctl chat threads scout
  tnctl chat threads scout --json`,
		Args: argsRange(1, 1, "chat threads needs a bot, for example: tnctl chat threads scout"),
		RunE: func(cmd *cobra.Command, args []string) error {
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			threads, raw, err := fl.ChatThreads(cmd.Context(), args[0])
			if err != nil {
				return classify(err)
			}
			out := cmd.OutOrStdout()
			if asJSON {
				return writeStructured(out, "json", raw)
			}
			if len(threads) == 0 {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no chat threads with %s\n", args[0])
				return nil
			}
			printChatThreads(out, threads)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the platform's answer as JSON")
	return c
}

// chatValidate checks the flags before anything is sent.
func chatValidate(f chatFlags) error {
	switch f.mode {
	case "", "auto", "quick", "council":
	default:
		return usageErr("--mode must be auto, quick or council, not %q", f.mode)
	}
	switch f.member {
	case "", "fleet", "coder":
	default:
		return usageErr("--member must be fleet or coder, not %q", f.member)
	}
	switch f.effort {
	case "", "low", "medium", "high":
	default:
		return usageErr("--effort must be low, medium or high, not %q", f.effort)
	}
	if f.member != "" && f.mode != "" {
		return usageErr("--member and --mode cannot be used together: a member answers alone")
	}
	if f.newThread && f.thread != "" {
		return usageErr("--new and --thread cannot be used together")
	}
	if f.follow && f.thread == "" {
		return usageErr("--follow needs --thread")
	}
	return nil
}

// chatInfoError names the two answers of the chat's own route: the chat being off on the platform,
// and a bot that is not there, which classify covers.
func chatInfoError(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "council_off" {
		return newErr(codeError, "the TeknoirBot chat is turned off on this platform")
	}
	return classify(err)
}

// chatTurnError names a turn that is already running: the person has to cancel it first.
func chatTurnError(err error, bot, thread string) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == "turn_open" {
		return newErr(codeConflict, "this chat already has a turn in progress (tnctl chat %s --thread %s --follow, then Ctrl-C cancels it)", bot, escapeLine(cleanBotText(thread)))
	}
	return classify(err)
}

// chatOnce runs one turn: it makes the thread when --thread names none, asks the council and follows
// the answer to its end.
func chatOnce(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, prompt string, f chatFlags, sigs <-chan os.Signal) error {
	errw := cmd.ErrOrStderr()
	threadID := f.thread
	var after int64
	if threadID != "" {
		d, _, err := fl.ChatThread(ctx, bot, threadID)
		if err != nil {
			return classify(err)
		}
		after = d.LastSeq
	} else {
		t, _, err := fl.CreateChatThread(ctx, bot, api.NewChatThread{Title: chatTitle(prompt), Origin: "cli", Member: f.member})
		if err != nil {
			return classify(err)
		}
		threadID = t.ID
	}
	if !f.json {
		_, _ = fmt.Fprintf(errw, "thread %s\n", escapeLine(cleanBotText(threadID)))
	}
	return chatRunTurn(ctx, cmd, fl, bot, threadID, after, api.NewChatTurn{Prompt: prompt, Mode: f.mode, Member: f.member, Effort: f.effort}, f, sigs)
}

// chatRunTurn posts one turn and follows it. A turn that fails is an error with its exit code.
func chatRunTurn(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, thread string, after int64, req api.NewChatTurn, f chatFlags, sigs <-chan os.Signal) error {
	turn, _, err := fl.PostChatTurn(ctx, bot, thread, req)
	if err != nil {
		return chatTurnError(err, bot, thread)
	}
	return runChatTurn(ctx, fl, bot, thread, turn.ID, after, newChatTurnPrinter(cmd, f), sigs)
}

// newChatTurnPrinter is the printer of one turn.
func newChatTurnPrinter(cmd *cobra.Command, f chatFlags) *chatTurnPrinter {
	return &chatTurnPrinter{out: cmd.OutOrStdout(), errw: cmd.ErrOrStderr(), json: f.json, tty: !f.json && stdoutIsTerminal()}
}

// chatFollow follows the thread's last turn, asking nothing: a turn that still runs is followed from
// its first event, and one that already ended is printed from the thread's own record. With --json
// both are followed: a turn that ended replays its stored events, whose turn_end gives the exit code.
func chatFollow(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, thread string, f chatFlags, sigs <-chan os.Signal) error {
	d, _, err := fl.ChatThread(ctx, bot, thread)
	if err != nil {
		return classify(err)
	}
	last := chatLastTurn(d)
	if last == nil {
		return newErr(codeError, "thread %s has no turn yet", escapeLine(cleanBotText(thread)))
	}
	p := newChatTurnPrinter(cmd, f)
	if last.State == "queued" || last.State == "running" || f.json {
		// From 0: the turn's own events, and only those, as runChatTurn leaves other turns' out.
		return runChatTurn(ctx, fl, bot, thread, last.ID, 0, p, sigs)
	}
	if last.Final != nil {
		p.final(*last.Final)
	}
	return chatTurnState(last.State, last.Error)
}

// chatFollowOpenTurn follows the thread's last turn when it has not ended: a conversation opened on
// a thread picks up where the person left it. A thread whose last turn ended, or that has none, is
// left alone. read says the thread could be read at all: one the platform will not give has nothing
// to follow, and cannot be posted on either, so the conversation ends with that error.
func chatFollowOpenTurn(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, thread string, f chatFlags, sigs <-chan os.Signal) (read bool, err error) {
	d, _, err := fl.ChatThread(ctx, bot, thread)
	if err != nil {
		return false, classify(err)
	}
	last := chatLastTurn(d)
	if last == nil || (last.State != "queued" && last.State != "running") {
		return true, nil
	}
	return true, runChatTurn(ctx, fl, bot, thread, last.ID, 0, newChatTurnPrinter(cmd, f), sigs)
}

// chatLastTurn is the thread's newest turn: the one with the highest index, or nil for a thread
// with none.
func chatLastTurn(d *api.ChatThreadDetail) *api.ChatTurn {
	var last *api.ChatTurn
	for i := range d.Turns {
		if last == nil || d.Turns[i].Index > last.Index {
			last = &d.Turns[i]
		}
	}
	return last
}

// chatRepl talks until /quit, EOF or Ctrl-C at the prompt. Its thread is made at the first prompt.
func chatRepl(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot string, info *api.ChatInfo, f chatFlags, sigs <-chan os.Signal) error {
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	printChatInfo(out, bot, info)
	if f.thread != "" {
		_, _ = fmt.Fprintf(errw, "thread %s\n", escapeLine(cleanBotText(f.thread)))
		// A conversation opened on a thread with a turn still running follows it first.
		if read, err := chatFollowOpenTurn(ctx, cmd, fl, bot, f.thread, f, sigs); err != nil {
			if !read || errors.Is(err, errChatInterrupted) {
				// The thread cannot be read: no turn can be posted on it either (each one reads the
				// thread first), so the conversation ends with the platform's error.
				return err
			}
			_, _ = fmt.Fprintf(errw, "Error: %v\n", err)
			if exitCodeOf(err) == codeAuth {
				return err // the person may no longer chat: the next turn would fail too
			}
		}
	}

	lines := make(chan string)
	readErr := make(chan error, 1)
	done := make(chan struct{})
	defer close(done)
	go func() {
		sc := bufio.NewScanner(cmd.InOrStdin())
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-done:
				return
			}
		}
		readErr <- sc.Err()
	}()

	threadID, after := f.thread, int64(0)
	fresh := threadID == ""
	for {
		// A Ctrl-C that arrived as the last turn ended is the turn's, not the prompt's.
		drainSignals(sigs)
		_, _ = fmt.Fprint(out, "> ")
		select {
		case <-ctx.Done():
			return nil
		case <-sigs:
			// Ctrl-C at the prompt ends the conversation, as /quit does.
			_, _ = fmt.Fprintln(out)
			return nil
		case <-readErr:
			return nil // EOF
		case line := <-lines:
			line = strings.TrimSpace(line)
			switch {
			case line == "":
				continue
			case line == "/quit":
				return nil
			case line == "/new":
				// The next thread has its own events: its sequence numbers start over at 0.
				threadID, fresh, after = "", true, 0
				_, _ = fmt.Fprintln(errw, "· a new thread starts at your next message")
				continue
			case line == "/cancel":
				if err := chatCancelOpenTurn(ctx, errw, fl, bot, threadID); err != nil {
					_, _ = fmt.Fprintf(errw, "Error: %v\n", err)
				}
				continue
			case strings.HasPrefix(line, "/"):
				_, _ = fmt.Fprintln(out, "commands: /new /cancel /quit, @fleet, @coder")
				continue
			}
			member, prompt := chatMemberLine(line)
			if prompt == "" {
				continue
			}
			if threadID == "" {
				t, _, err := fl.CreateChatThread(ctx, bot, api.NewChatThread{Title: chatTitle(prompt), Origin: "cli", Member: f.member})
				if err != nil {
					_, _ = fmt.Fprintf(errw, "Error: %v\n", classify(err))
					continue
				}
				// A new thread starts before its first event: after is 0, as its ids start at 1.
				threadID, fresh, after = t.ID, true, 0
				_, _ = fmt.Fprintf(errw, "thread %s\n", escapeLine(cleanBotText(threadID)))
			}
			if !fresh {
				d, _, err := fl.ChatThread(ctx, bot, threadID)
				if err != nil {
					_, _ = fmt.Fprintf(errw, "Error: %v\n", classify(err))
					continue
				}
				after = d.LastSeq
			}
			fresh = false
			req := api.NewChatTurn{Prompt: prompt, Mode: f.mode, Member: f.member, Effort: f.effort}
			if member != "" {
				// @fleet and @coder ask one member alone: a mode is the council's, not the member's.
				req.Member, req.Mode = member, ""
			}
			if err := chatRunTurn(ctx, cmd, fl, bot, threadID, after, req, f, sigs); err != nil {
				if errors.Is(err, errChatInterrupted) {
					return err // a second Ctrl-C left the turn: it leaves the conversation too
				}
				_, _ = fmt.Fprintf(errw, "Error: %v\n", err)
				if exitCodeOf(err) == codeAuth {
					return err // the person may no longer chat: the next turn would fail too
				}
			}
		}
	}
}

// drainSignals throws away the Ctrl-Cs already queued: one that arrived as a finished turn ended is
// the turn's, and must not end the conversation at the next prompt.
func drainSignals(sigs <-chan os.Signal) {
	for {
		select {
		case <-sigs:
		default:
			return
		}
	}
}

// chatMemberLine reads "@fleet <text>" or "@coder <text>": the member asked alone, and the text.
func chatMemberLine(line string) (member, prompt string) {
	for _, m := range []string{"fleet", "coder"} {
		rest, ok := strings.CutPrefix(line, "@"+m)
		if !ok {
			continue
		}
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return m, strings.TrimSpace(rest)
		}
	}
	return "", line
}

// chatCancelOpenTurn cancels the thread's open turn, if it has one.
func chatCancelOpenTurn(ctx context.Context, errw io.Writer, fl *api.Fleet, bot, thread string) error {
	if thread == "" {
		_, _ = fmt.Fprintln(errw, "· no thread yet: nothing to cancel")
		return nil
	}
	d, _, err := fl.ChatThread(ctx, bot, thread)
	if err != nil {
		return classify(err)
	}
	for i := len(d.Turns) - 1; i >= 0; i-- {
		t := d.Turns[i]
		if t.State != "queued" && t.State != "running" {
			continue
		}
		if _, _, err := fl.CancelChatTurn(ctx, bot, thread, t.ID); err != nil {
			return classify(err)
		}
		_, _ = fmt.Fprintf(errw, "· cancelling turn %s\n", escapeLine(cleanBotText(t.ID)))
		return nil
	}
	_, _ = fmt.Fprintln(errw, "· no open turn to cancel")
	return nil
}

// printChatInfo is what the conversation says about the bot before the first prompt.
func printChatInfo(w io.Writer, bot string, info *api.ChatInfo) {
	online := "offline"
	if info.Bot.Online {
		online = "online"
	}
	access := info.Bot.Access
	if access == "" {
		access = api.BotAccessNone
	}
	council := "unknown"
	if info.Bot.Council != nil && *info.Bot.Council != "" {
		council = *info.Bot.Council
	}
	_, _ = fmt.Fprintf(w, "%s (%s, %s) — council %s\n", bot, escapeLine(cleanBotText(access)), online, escapeLine(cleanBotText(council)))
	if len(info.Members) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for _, m := range info.Members {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", terminalText(cleanBotText(m.Name)), terminalText(cleanBotText(m.Model)), terminalText(cleanBotText(m.Effort)))
		}
		_ = tw.Flush()
	}
	_, _ = fmt.Fprintf(w, "tokens today: %d of %d\n", info.Budget.TokensToday, info.Budget.TokensPerDay)
}

// printChatThreads is the threads table.
func printChatThreads(w io.Writer, threads []api.ChatThread) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tTITLE\tTURNS\tLAST\tUPDATED")
	for _, t := range threads {
		last := "-"
		if t.LastTurn != nil && t.LastTurn.State != "" {
			last = escapeLine(t.LastTurn.State)
		}
		title := terminalText(cleanBotText(t.Title))
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\n", escapeLine(t.ID), cutRunes(title, 50), t.TurnCount, last,
			time.UnixMilli(t.UpdatedAt).Local().Format("2006-01-02 15:04"))
	}
	_ = tw.Flush()
}

// chatTitle is the title of a thread: the prompt's first line, cut to 60 characters.
func chatTitle(prompt string) string {
	first, _, _ := strings.Cut(prompt, "\n")
	return cutRunes(strings.TrimSpace(first), 60)
}

// cutRunes cuts s to n characters, counting runes.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// ---------- one turn ----------

// chatItem is one read of a chat stream, tagged with the connection it came from: an event of an
// abandoned connection is dropped.
type chatItem struct {
	gen int
	ev  api.ChatEvent
	err error
}

// runChatTurn follows a turn's events to its turn_end and prints them as they come. It reconnects
// after a clean end, a read error or a silent stream, at once after stream_expired and with the
// backoff after the stream's own error; access_lost ends it. Ctrl-C arrives through sigs: the first
// one cancels the turn (and gives it chatCancelGrace to end), a second one leaves with
// errChatInterrupted.
func runChatTurn(ctx context.Context, fl *api.Fleet, bot, thread, turnID string, after int64, p *chatTurnPrinter, sigs <-chan os.Signal) error {
	// The turn's own context: every wait of the turn takes it, so a second Ctrl-C ends whichever of
	// them the turn is in — a backoff sleep, a Retry-After wait, a request still to be answered.
	turnCtx, cancelTurn := context.WithCancelCause(ctx)
	events := make(chan chatItem, 256)

	// The signal watcher belongs to this turn alone: the turn closes done before it returns, and
	// waits for the watcher, so a Ctrl-C at the prompt of the conversation never reaches it.
	done := make(chan struct{})
	var watchers sync.WaitGroup
	defer func() {
		// The turn is over: end its context too, so a watcher still sending the cancel returns.
		cancelTurn(context.Canceled)
		close(done)
		watchers.Wait()
	}()
	watchers.Add(1)
	go func() {
		defer watchers.Done()
		var grace *time.Timer
		var graceC <-chan time.Time
		defer func() {
			if grace != nil {
				grace.Stop()
			}
		}()
		cancelling := false
		for {
			select {
			case <-done:
				return
			case <-turnCtx.Done():
				return
			case <-graceC:
				// Ctrl-C asked for the cancel, and the turn did not end in time.
				cancelTurn(errChatCancelTimeout)
				return
			case <-sigs:
				if cancelling {
					cancelTurn(errChatInterrupted)
					return
				}
				cancelling = true
				p.errLine("cancelling…")
				// P7 runs beside the watcher, so a second Ctrl-C is seen while it is answered.
				watchers.Add(1)
				go func() {
					defer watchers.Done()
					if _, _, err := fl.CancelChatTurn(turnCtx, bot, thread, turnID); err != nil && turnCtx.Err() == nil {
						p.progress("cancel failed: " + escapeLine(cleanBotText(err.Error())))
					}
				}()
				grace = time.NewTimer(chatCancelGrace)
				graceC = grace.C
			}
		}
	}()

	lastSeq := after
	var cur *api.ChatStream
	var curStop chan struct{}
	var curCancel context.CancelFunc
	gen := 0
	// connStart is when the open connection was made, connWorking whether it has proved the platform
	// reachable: it carried an event of the thread, or stayed open for chatWorkingAfter.
	var connStart time.Time
	var connWorking bool
	// closeCur ends the connection: its context first (a read still waiting for data returns at
	// once), then the body.
	closeCur := func() {
		if curStop != nil {
			close(curStop)
			curStop = nil
		}
		if curCancel != nil {
			curCancel()
			curCancel = nil
		}
		if cur != nil {
			_ = cur.Close()
			cur = nil
		}
	}
	defer closeCur()
	open := func() error {
		cctx, cancel := context.WithCancel(turnCtx)
		s, err := fl.ChatEvents(cctx, bot, thread, lastSeq)
		if err != nil {
			cancel()
			return err
		}
		cur, curCancel = s, cancel
		gen++
		connStart, connWorking = time.Now(), false
		stop := make(chan struct{})
		curStop = stop
		g := gen
		go func() {
			for {
				ev, err := s.Next()
				select {
				case events <- chatItem{gen: g, ev: ev, err: err}:
				case <-stop:
					return
				case <-turnCtx.Done():
					return
				}
				if err != nil {
					if errors.Is(err, api.ErrMalformedChatEvent) {
						continue // the frame is lost, the rest of the stream is not
					}
					return
				}
			}
		}()
		return nil
	}

	delay := chatReconnectDelay
	failures := 0
	lost := func() error {
		return newErr(codeTimeout, "lost the chat stream; the turn goes on: tnctl chat %s --thread %s --follow", bot, escapeLine(cleanBotText(thread)))
	}
	countFail := func() error {
		failures++
		if failures >= chatMaxAttempts {
			return lost()
		}
		return nil
	}
	backoff := func() error {
		if err := countFail(); err != nil {
			return err
		}
		d := delay
		delay *= 2
		if delay > chatReconnectMax {
			delay = chatReconnectMax
		}
		if err := realSleep(turnCtx, d); err != nil {
			return chatTurnCtxErr(turnCtx)
		}
		return nil
	}
	waitFor := func(d time.Duration) error {
		if err := countFail(); err != nil {
			return err
		}
		if err := realSleep(turnCtx, d); err != nil {
			return chatTurnCtxErr(turnCtx)
		}
		return nil
	}
	// working marks the open connection as one that proves the platform reachable, and starts the
	// pause over: only a connection that worked makes a failure a fluke.
	working := func() {
		if connWorking {
			return
		}
		connWorking = true
		failures, delay = 0, chatReconnectDelay
	}
	// workingIfLong marks a connection that carried no event but stayed open as working too.
	workingIfLong := func() {
		if time.Since(connStart) >= chatWorkingAfter {
			working()
		}
	}

	var turnErr *api.ChatError
	var usage *api.ChatUsage
	// cutFinal is the text of a final the platform shortened for the stream; cutPending says it is
	// the last final seen, so the answer is not printed until the thread's own record is read.
	var cutFinal string
	cutPending := false
	for {
		if cur == nil {
			err := open()
			if err != nil {
				if turnCtx.Err() != nil {
					return chatTurnCtxErr(turnCtx)
				}
				var ae *api.Error
				if errors.As(err, &ae) {
					if ae.Status == http.StatusTooManyRequests && ae.RetryAfter > 0 {
						if e := waitFor(chatRetryAfterWait(ae)); e != nil {
							return e
						}
						continue
					}
					if ae.IsAuth() || ae.Status == http.StatusNotFound || ae.Status == http.StatusBadRequest {
						return classify(err)
					}
				}
				if e := backoff(); e != nil {
					return e
				}
				continue
			}
		}
		select {
		case <-turnCtx.Done():
			return chatTurnCtxErr(turnCtx)
		case it := <-events:
			if it.gen != gen {
				continue
			}
			ev := it.ev
			if it.err != nil {
				if errors.Is(it.err, api.ErrMalformedChatEvent) {
					// The frame is lost, the connection is not: move past it and read on.
					if ev.Seq > lastSeq {
						lastSeq = ev.Seq
					}
					p.skipped(ev.Seq)
					continue
				}
				closeCur()
				if turnCtx.Err() != nil {
					return chatTurnCtxErr(turnCtx)
				}
				workingIfLong()
				if connWorking {
					// The stream worked and then ended: connect again after the pause it had, and
					// count none of it.
					if err := realSleep(turnCtx, delay); err != nil {
						return chatTurnCtxErr(turnCtx)
					}
					continue
				}
				if e := backoff(); e != nil {
					return e
				}
				continue
			}
			if ev.Seq > lastSeq {
				lastSeq = ev.Seq
			}
			if ev.TurnID == "" {
				// The stream's own error: it ends the stream.
				if ev.Type != "error" {
					continue
				}
				var se struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(ev.Data, &se); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				switch se.Code {
				case "access_lost":
					msg := terminalText(cleanBotText(se.Message))
					if msg == "" {
						msg = "you may no longer chat with this bot"
					}
					return newErr(codeAuth, "%s", msg)
				case "stream_expired":
					// The stream has run its hour. Only a stream that worked connects again at
					// once: an expired one that never carried an event is a failure like any other.
					closeCur()
					workingIfLong()
					if connWorking {
						continue
					}
					if e := backoff(); e != nil {
						return e
					}
				default:
					closeCur()
					if e := backoff(); e != nil {
						return e
					}
				}
				continue
			}
			// An event of the thread: the connection works, so the next pause starts over.
			working()
			if ev.TurnID != turnID {
				continue // another turn of the thread
			}
			if p.json {
				if err := p.writeEvent(ev); err != nil {
					return err
				}
			}
			switch ev.Type {
			case "turn_end":
				var d struct {
					State string `json:"state"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				// A shortened final is never printed as the answer: with --json the events are the
				// output, and the frame says it was cut. The whole answer is printed first, so the
				// turn's summary is the last line; its read of the thread's record is also what a
				// failure the stream did not carry comes from.
				var record *api.ChatThreadDetail
				if cutPending && !p.json {
					record = chatPrintWholeFinal(ctx, fl, bot, thread, turnID, cutFinal, p)
				}
				p.summary(usage)
				return chatTurnEnd(ctx, fl, bot, thread, turnID, d.State, turnErr, record)
			case "final":
				var d struct {
					Text string `json:"text"`
					Cut  bool   `json:"cut"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				if d.Cut {
					// The platform shortened the answer for the stream; the thread's record has it
					// whole, and that is printed when the turn ends.
					cutFinal, cutPending = d.Text, true
					continue
				}
				cutFinal, cutPending = "", false
				p.final(d.Text)
			case "text_delta":
				var d struct {
					Member string `json:"member"`
					Text   string `json:"text"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				p.delta(d.Member, d.Text)
			case "usage":
				var u api.ChatUsage
				if err := json.Unmarshal(ev.Data, &u); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				usage = &u
			case "plan":
				var d struct {
					Mode  string `json:"mode"`
					Tasks []struct {
						Member string `json:"member"`
						Task   string `json:"task"`
					} `json:"tasks"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				line := d.Mode
				if len(d.Tasks) > 0 {
					var names []string
					for _, t := range d.Tasks {
						names = append(names, terminalText(cleanBotText(t.Member)))
					}
					line += ": " + strings.Join(names, ", ")
				}
				p.progress(strings.TrimRight(line, " "))
				p.resetCaptainText()
			case "member_start":
				var d struct {
					Member string `json:"member"`
					Text   string `json:"text"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				line := terminalText(cleanBotText(d.Member))
				if text := terminalText(cleanBotText(d.Text)); text != "" {
					line += ": " + cutRunes(text, 80)
				}
				p.progress(line)
			case "tool_call":
				var d struct {
					Member string `json:"member"`
					Tool   string `json:"tool"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				p.progress(terminalText(cleanBotText(d.Member)) + " → " + terminalText(cleanBotText(d.Tool)))
			case "coder_update":
				var d struct {
					Kind  string `json:"kind"`
					Title string `json:"title"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				line := "coder: " + terminalText(cleanBotText(d.Kind))
				if title := terminalText(cleanBotText(d.Title)); title != "" {
					line += " " + title
				}
				p.progress(line)
			case "member_done":
				var d struct {
					Member string `json:"member"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				p.progress(terminalText(cleanBotText(d.Member)) + " done")
				p.resetCaptainText()
			case "critique":
				var d struct {
					Issues []struct {
						Severity string `json:"severity"`
					} `json:"issues"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				blocking := 0
				for _, i := range d.Issues {
					if i.Severity == "blocking" {
						blocking++
					}
				}
				p.progress(fmt.Sprintf("critic: %d issues (%d blocking)", len(d.Issues), blocking))
				p.resetCaptainText()
			case "budget":
				var d struct {
					What  string      `json:"what"`
					Limit json.Number `json:"limit"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				p.progress(fmt.Sprintf("budget: %s limit %s", terminalText(cleanBotText(d.What)), d.Limit))
			case "error":
				var d struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				turnErr = &api.ChatError{Code: d.Code, Message: d.Message}
				line := "error"
				if d.Code != "" {
					line += ": " + d.Code
				}
				if msg := terminalText(cleanBotText(d.Message)); msg != "" {
					line += ": " + msg
				}
				p.progress(line)
			}
		}
	}
}

// chatTurnEnd is the exit of a turn: 0 for done, 1 for failed, expired and cancelled. A failure the
// stream did not carry is read from the thread's own record: the one record a shortened final already
// read, or one read here when it read none.
func chatTurnEnd(ctx context.Context, fl *api.Fleet, bot, thread, turnID, state string, ce *api.ChatError, record *api.ChatThreadDetail) error {
	if (state == "failed" || state == "expired") && ce == nil {
		d := record
		if d == nil {
			// The failure was not in a frame this run saw, and no read of the record has happened.
			d, _, _ = fl.ChatThread(context.WithoutCancel(ctx), bot, thread)
		}
		if d != nil {
			for _, t := range d.Turns {
				if t.ID == turnID {
					ce = t.Error
				}
			}
		}
	}
	return chatTurnState(state, ce)
}

// chatTurnState is the exit of a turn in state, with its failure ce.
func chatTurnState(state string, ce *api.ChatError) error {
	switch state {
	case "cancelled":
		return newErr(codeError, "the turn was cancelled")
	case "failed", "expired":
		// The code and the message are the bot's own text: neither may drive the terminal.
		code, msg := "", ""
		if ce != nil {
			code, msg = escapeLine(cleanBotText(ce.Code)), terminalText(cleanBotText(ce.Message))
		}
		switch {
		case code != "" && msg != "":
			return newErr(codeError, "the turn failed: %s: %s", code, msg)
		case msg != "":
			return newErr(codeError, "the turn failed: %s", msg)
		}
		return newErr(codeError, "the turn failed (%s)", state)
	}
	return nil
}

// chatPrintWholeFinal prints the answer of a turn whose stream carried a shortened final: the
// thread's own record has it whole. It returns that record when it could be read — the turn's end
// takes its failure from it rather than reading the thread again — and nil when it could not. When
// the record cannot be read, or holds no final for the turn, the shortened text is printed after all,
// with a line on stderr saying how to read the answer whole.
func chatPrintWholeFinal(ctx context.Context, fl *api.Fleet, bot, thread, turnID, cutText string, p *chatTurnPrinter) *api.ChatThreadDetail {
	d, _, err := fl.ChatThread(context.WithoutCancel(ctx), bot, thread)
	if err != nil {
		d = nil
	}
	if d != nil {
		for _, t := range d.Turns {
			if t.ID == turnID && t.Final != nil {
				p.final(*t.Final)
				return d
			}
		}
	}
	p.final(cutText)
	p.errLine(fmt.Sprintf("the answer was shortened by the stream; read it whole with: tnctl chat %s --thread %s --follow", bot, escapeLine(cleanBotText(thread))))
	return d
}

// chatTurnCtxErr is why the turn stopped when its context ended: a second Ctrl-C (or the cancel
// grace running out) leaves errChatInterrupted, the command's own context its error.
func chatTurnCtxErr(turnCtx context.Context) error {
	switch cause := context.Cause(turnCtx); {
	case errors.Is(cause, errChatInterrupted):
		return errChatInterrupted
	case errors.Is(cause, errChatCancelTimeout):
		return errChatCancelTimeout
	}
	return classify(turnCtx.Err())
}

// chatRetryAfterWait is how long a 429 of the stream asks the turn to wait: its Retry-After in
// chatRetryAfterUnit, at most chatRetryAfterMax. It is capped in whole units before the
// multiplication, as mqttRetryAfterWait's is.
func chatRetryAfterWait(ae *api.Error) time.Duration {
	wait := chatRetryAfterMax
	if n := time.Duration(ae.RetryAfter); n < chatRetryAfterMax/chatRetryAfterUnit {
		wait = n * chatRetryAfterUnit
	}
	return wait
}

// chatTurnPrinter writes one turn: the answer on stdout, the council's progress on stderr. Its
// mutex keeps the lines of the turn's loop and of the Ctrl-C watcher apart.
type chatTurnPrinter struct {
	mu        sync.Mutex
	out, errw io.Writer
	json      bool
	tty       bool
	// captain is the Captain's text printed since the last plan, member_done or critique event.
	captain      strings.Builder
	wroteCaptain bool
	// printedAny says whether any Captain text was printed in this turn, that text included: the
	// final then starts a paragraph of its own. lastNewline says whether that text ended its line.
	printedAny  bool
	lastNewline bool
}

// delta prints the Captain's answer as it is written, on a terminal.
func (p *chatTurnPrinter) delta(member, text string) {
	if p.json || !p.tty || member != "captain" {
		return
	}
	clean := cleanBotText(text)
	if clean == "" {
		return
	}
	p.captain.WriteString(clean)
	p.wroteCaptain = true
	p.printedAny = true
	p.lastNewline = strings.HasSuffix(clean, "\n")
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprint(p.out, clean)
}

// resetCaptainText starts the comparison for the final over.
func (p *chatTurnPrinter) resetCaptainText() {
	p.captain.Reset()
	p.wroteCaptain = false
}

// final prints the turn's answer: alone when nothing was printed yet, otherwise the final is only
// printed when the Captain's stream did not already say exactly this. After Captain text that a
// plan, a member_done or a critique reset, the final starts a new paragraph: it reads as its own
// answer and not as the end of the last sentence.
func (p *chatTurnPrinter) final(text string) {
	if p.json {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	clean := cleanBotText(text)
	switch {
	case !p.tty, !p.wroteCaptain && !p.printedAny:
		_, _ = fmt.Fprint(p.out, endWithNewline(clean))
	case p.wroteCaptain && strings.TrimSpace(p.captain.String()) == strings.TrimSpace(clean):
		_, _ = fmt.Fprint(p.out, "\n")
	default:
		sep := "\n\n"
		if p.lastNewline {
			sep = "\n"
		}
		_, _ = fmt.Fprint(p.out, sep+endWithNewline(clean))
	}
}

// progress is one line of what the council is doing, on stderr.
func (p *chatTurnPrinter) progress(line string) {
	if p.json {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.errw, "· %s\n", escapeLine(cleanBotText(line)))
}

// errLine writes one line of the turn's own text on stderr, as it is.
func (p *chatTurnPrinter) errLine(line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintln(p.errw, line)
}

// skipped says that a frame of the stream was malformed and left out, on stderr.
func (p *chatTurnPrinter) skipped(seq int64) {
	if p.json {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.errw, "· skipped a malformed event (seq %d)\n", seq)
}

// summary is what the turn cost, when the stream reported it.
func (p *chatTurnPrinter) summary(u *api.ChatUsage) {
	if p.json || u == nil {
		return
	}
	calls := fmt.Sprintf("%d calls", u.Calls)
	if u.Calls == 1 {
		calls = "1 call"
	}
	secs := strconv.FormatFloat(float64(u.MS)/1000, 'f', 1, 64)
	tokens := u.Input + u.Output + u.CacheRead + u.CacheWrite
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprintf(p.errw, "done in %ss, %s, %d tokens\n", secs, calls, tokens)
}

// writeEvent prints one event of the turn as the platform sent it, with its sequence number and type
// in front: {"seq": N, "type": "…", "turnId": "…", …}. The data is decoded and written again, so
// whatever it holds — an empty object, an odd layout, keys of its own such as seq — one line stays
// one JSON object.
func (p *chatTurnPrinter) writeEvent(ev api.ChatEvent) error {
	var data map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return fmt.Errorf("malformed chat event: %w", err)
	}
	// The frame's own number and type win over anything the data says.
	delete(data, "seq")
	delete(data, "type")
	names := make([]string, 0, len(data))
	for k := range data {
		names = append(names, k)
	}
	sort.Strings(names)
	typ, err := json.Marshal(ev.Type)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	_, _ = fmt.Fprintf(&b, `{"seq":%d,"type":%s`, ev.Seq, typ)
	for _, k := range names {
		key, err := json.Marshal(k)
		if err != nil {
			return err
		}
		var v bytes.Buffer
		if err := json.Compact(&v, data[k]); err != nil {
			// Not JSON, so an object it is not either: it cannot be written, and the event is unusable.
			return fmt.Errorf("malformed chat event: %w", err)
		}
		b.WriteByte(',')
		b.Write(key)
		b.WriteByte(':')
		b.Write(v.Bytes())
	}
	b.WriteString("}\n")
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err = io.Copy(p.out, &b)
	return err
}

// endWithNewline is s with a final newline, so a message ends its line.
func endWithNewline(s string) string {
	if s == "" {
		return "\n"
	}
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// cleanBotText drops what a TeknoirBot's text must not do to a terminal: ANSI escape sequences (CSI
// and OSC) and every control character but newline and tab.
func cleanBotText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				// CSI: parameters, then one byte of 0x40 to 0x7e.
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				if j < len(s) {
					j++
				}
				i = j
				continue
			}
			if j < len(s) && s[j] == ']' {
				// OSC: up to BEL or the string terminator ESC \.
				j++
				for j < len(s) {
					if s[j] == 0x07 {
						j++
						break
					}
					if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
						j += 2
						break
					}
					j++
				}
				i = j
				continue
			}
			i++ // a bare ESC
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}
