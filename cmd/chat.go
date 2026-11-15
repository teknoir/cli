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
	"slices"
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

// errChatNothingAsked is what a Ctrl-C during the requests before a turn leaves (the council's change,
// the thread's read, the new thread): no turn is posted, so none is cancelled. It is a sentinel: the
// conversation says so on one line and goes on.
var errChatNothingAsked = newErr(codeError, "interrupted: nothing was asked")

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
	thread      string
	newThread   bool
	follow      bool
	mode        string
	member      string
	effort      string
	json        bool
	roster      string // --roster: the council's name
	thorough    bool   // --thorough
	memberScope string // set by chatValidate from --member: "", bot, teamspace or template
	memberSlug  string // set by chatValidate from --member: the slug alone
}

func newChatCmd(e *cliEnv) *cobra.Command {
	var f chatFlags
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "chat <bot> [prompt...]",
		Short:        "Chat with a TeknoirBot's council, or with one of its members",
		Long: `Chat with a TeknoirBot's council, or with one of its members, and read the answer as it is written.

The council's lead takes each turn: it answers a simple question itself or convenes members, and
the council's reviewer, when it has one, checks their reports when the turn needs it. --thorough
has the lead convene the members and the reviewer check every report: slower, and several times
the tokens. --roster picks the council: standard, ops, review or one of the bot's own; a new chat
without it gets the bot's default council. tnctl council roster list --bot <bot> lists the
councils, and tnctl council roster use <council> --bot <bot> makes another one the default.
--member asks one member alone, without a council: its slug, or <scope>/<slug> when more than one
member has that slug. With --thread, --member names a member of that chat by its slug alone.
tnctl council role list --bot <bot> --templates lists each member's slug and scope.

With a prompt the command runs one turn and prints the answer: stdout gets the answer (as it is
written when stdout is a terminal, at the end otherwise) and stderr the progress of the council.
Without one, in a terminal, it starts a conversation: /new starts a new thread, /cancel cancels the
thread's open turn, /quit ends it, and @<member> <text> asks one member of the chat's council alone.

The chat's thread is printed on stderr when it is made, so it can be continued later with
--thread <id>; with --roster too, that chat's council changes first. Ctrl-C cancels the running
turn (a second one leaves at once).

With --thread <id> --follow the command does not ask anything: it follows the thread's last turn,
picking up the answer where it is when that turn still runs, and printing it when it already ended.

--mode and --effort are for advanced use: without them the lead decides how the council answers.`,
		Example: `  tnctl chat scout "what is running on my robot?"
  tnctl chat scout --thread thr_123 "and the pods?"
  tnctl chat scout --thread thr_123 --follow
  tnctl chat scout --roster review --thorough "is the update safe to roll out?"
  tnctl chat scout --member triage-sre "why is arm-2 offline?"
  tnctl chat scout --member teamspace/triage "why is arm-3 slow?"
  tnctl chat threads scout`,
		Args: argsRange(1, -1, "chat needs a bot, for example: tnctl chat scout \"what is running?\""),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := chatValidate(&f); err != nil {
				return err
			}
			bot := args[0]
			if err := councilBot(bot); err != nil {
				return err
			}
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
				return chatOnce(ctx, cmd, fl, bot, prompt, info, f, sigs)
			}
			return chatRepl(ctx, cmd, fl, bot, info, f, sigs)
		},
	}
	c.Flags().StringVar(&f.thread, "thread", "", "continue this thread (the default is a new one)")
	c.Flags().BoolVar(&f.newThread, "new", false, "start a new thread (with --thread: a usage error)")
	c.Flags().BoolVar(&f.follow, "follow", false, "follow the thread's last turn instead of asking (needs --thread, and no prompt)")
	c.Flags().StringVar(&f.mode, "mode", "", "advanced: how the council answers: auto, quick or council")
	c.Flags().StringVar(&f.member, "member", "", "ask one member alone: its slug, or <scope>/<slug> (scope bot, teamspace or template)")
	c.Flags().StringVar(&f.roster, "roster", "", "the council that answers: standard, ops, review or one of the bot's own; with --thread it changes that chat's council first")
	c.Flags().BoolVar(&f.thorough, "thorough", false, "have the lead convene the members and the reviewer check their reports: slower, several times the tokens (the same as --mode council)")
	c.Flags().StringVar(&f.effort, "effort", "", "advanced: how hard the members think: low, medium or high")
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
		Long: `List the chat threads you have with a TeknoirBot: id, title, the council that answers it
(@<member> for a chat with one member, (default) when it follows the bot's default council), how
many turns it has, the state of the last turn and when it last changed. Continue one with
tnctl chat <bot> --thread <id>.`,
		Example: `  tnctl chat threads scout
  tnctl chat threads scout --json`,
		Args: argsRange(1, 1, "chat threads needs a bot, for example: tnctl chat threads scout"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := councilBot(args[0]); err != nil {
				return err
			}
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			threads, raw, err := fl.ChatThreads(cmd.Context(), args[0])
			if err != nil {
				return chatInfoError(err)
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

// chatValidate checks the flags before anything is sent. It splits --member into its scope and its
// slug, which the thread and turn requests send.
func chatValidate(f *chatFlags) error {
	switch f.mode {
	case "", "auto", "quick", "council":
	default:
		return usageErr("--mode must be auto, quick or council, not %q", f.mode)
	}
	if f.member != "" {
		scope, slug, ok := parseMemberRef(f.member)
		if !ok {
			return usageErr("--member is a member's slug, such as triage, or <scope>/<slug> with scope bot, teamspace or template, not %q", f.member)
		}
		f.memberScope, f.memberSlug = scope, slug
	}
	switch f.effort {
	case "", "low", "medium", "high":
	default:
		return usageErr("--effort must be low, medium or high, not %q", f.effort)
	}
	if f.roster != "" && !councilSlugRE.MatchString(f.roster) {
		return usageErr("--roster is a council's name: 2 to 24 lowercase letters, digits and dashes, starting with a letter, not %q", f.roster)
	}
	if f.member != "" && f.mode != "" {
		return usageErr("--member and --mode cannot be used together: a member answers alone")
	}
	if f.member != "" && f.roster != "" {
		return usageErr("--member and --roster cannot be used together: a member answers alone, without a council")
	}
	if f.thorough && f.member != "" {
		return usageErr("--thorough asks the whole council: it cannot be used with --member")
	}
	if f.thorough && f.mode != "" && f.mode != "council" {
		return usageErr("--thorough is --mode council: it cannot be used with --mode %s", f.mode)
	}
	if f.newThread && f.thread != "" {
		return usageErr("--new and --thread cannot be used together")
	}
	if f.thread == "." || f.thread == ".." {
		return usageErr("--thread %q is not a thread id: use an id that tnctl chat threads <bot> lists", f.thread)
	}
	if f.follow && f.thread == "" {
		return usageErr("--follow needs --thread")
	}
	if f.follow && f.roster != "" {
		return usageErr("--follow asks nothing: --roster cannot be used with it")
	}
	if f.thread != "" && f.memberScope != "" {
		return usageErr("with --thread, give --member the slug alone: the scope only picks the member of a new chat")
	}
	return nil
}

// chatInfoError maps a refusal of the chat's own reads (P1 info and P2 threads): the chat being off
// on the platform gets tnctl's sentence; any other refusal goes through councilError (the hub's
// words, the code's exit), and a code-less answer such as TeknoirBots being off keeps classify.
func chatInfoError(err error) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound && ae.Code == "council_off" {
		return newErr(codeError, "the TeknoirBot chat is turned off on this platform")
	}
	return councilError(err)
}

// chatTurnError maps a refusal of a turn (P6), and of the thread PATCH (P9) once chatPatchError has
// taken the pin case: a turn already running gets tnctl's hint to follow and cancel it; any other
// refusal goes through councilError.
func chatTurnError(err error, bot, thread string) error {
	var ae *api.Error
	if errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == "turn_open" {
		return newErr(codeConflict, "this chat already has a turn in progress (tnctl chat %s --thread %s --follow, then Ctrl-C cancels it)", bot, escapeLine(cleanBotText(thread)))
	}
	return councilError(err)
}

// chatThreadRequest is the body of a new thread (P3): the member and its scope for a member chat, or
// the council by name.
func chatThreadRequest(prompt string, f chatFlags) api.NewChatThread {
	return api.NewChatThread{Title: chatTitle(prompt), Origin: "cli", Member: f.memberSlug, MemberScope: f.memberScope, Roster: f.roster}
}

// chatTurnRequest is the body of a turn (P6). --thorough is mode council with thorough: only
// thorough makes the hub record the choice.
func chatTurnRequest(prompt string, f chatFlags) api.NewChatTurn {
	req := api.NewChatTurn{Prompt: prompt, Mode: f.mode, Member: f.memberSlug, Effort: f.effort}
	if f.thorough {
		req.Mode, req.Thorough = "council", true
	}
	return req
}

// chatCouncilName is how a council is named on one line: "<display name> (<name>)"; the name alone
// when the display name is empty or reads the same; the display name alone when the name is empty.
func chatCouncilName(name, displayName string) string {
	n, d := escapeLine(name), hubText(displayName)
	if n == "" {
		return d
	}
	if d != "" && d != hubText(name) {
		return d + " (" + n + ")"
	}
	return n
}

// chatSlugList is the members `@` can ask, from a council's member list: each slug once, in order, leaving
// out entries that are not slugs (a member that was deleted shows as its raw id).
func chatSlugList(members []string) []string {
	var out []string
	for _, s := range members {
		if councilSlugRE.MatchString(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// chatOldHubSlugs are the members an older hub (no council in the chat's info) lets `@` ask.
func chatOldHubSlugs() []string { return []string{"fleet", "coder"} }

// chatSlugsForRoster is the members of the bot's council called name, from the chat's info: what `@` asks
// in a chat on that council. An older hub gives fleet and coder; a name the info does not list gives none.
func chatSlugsForRoster(info *api.ChatInfo, name string) []string {
	if info.Council == nil {
		return chatOldHubSlugs()
	}
	for _, r := range info.Council.Rosters {
		if r.Name == name {
			return chatSlugList(r.Members)
		}
	}
	return nil
}

// chatSlugsForNew is what `@` asks before the conversation's next thread exists: none for a chat with one
// member, on any hub; otherwise fleet and coder on an older hub, else the members of the council that thread
// will have (--roster, else the bot's default).
func chatSlugsForNew(info *api.ChatInfo, f chatFlags) []string {
	switch {
	case f.memberSlug != "":
		return nil
	case info.Council == nil:
		return chatOldHubSlugs()
	case f.roster != "":
		return chatSlugsForRoster(info, f.roster)
	default:
		return chatSlugList(info.Council.Roster.Members)
	}
}

// chatSlugsForThread is what `@` asks on a thread just read: none for a chat with one member, on any hub;
// otherwise fleet and coder on an older hub, else the thread's council's members (none for a council the
// hub cannot find).
func chatSlugsForThread(info *api.ChatInfo, d *api.ChatThreadDetail) []string {
	switch {
	case d.Thread.Member != nil:
		return nil
	case info.Council == nil:
		return chatOldHubSlugs()
	case d.Council == nil:
		return nil
	default:
		return chatSlugList(d.Council.Roster.Members)
	}
}

// chatHelpLine is the conversation's commands, and the members `@` asks in the current chat.
func chatHelpLine(slugs []string) string {
	var b strings.Builder
	b.WriteString("commands: /new /cancel /quit")
	for _, s := range slugs {
		b.WriteString(", @" + s)
	}
	return b.String()
}

// chatDefaultCouncil is the bot's default council named for one line, "" when the hub names none.
func chatDefaultCouncil(c *api.ChatInfoCouncil) string {
	if name := chatCouncilName(c.Roster.Name, c.Roster.DisplayName); name != "" {
		return name
	}
	return chatCouncilName(c.DefaultRoster, "")
}

// chatUnavailable is " — unavailable: <reasons>" for a council the hub says cannot answer, "" otherwise.
func chatUnavailable(status string, reasons []string) string {
	if status != "invalid" {
		return ""
	}
	if r := councilReasons(reasons); r != "" {
		return " — unavailable: " + r
	}
	return " — unavailable"
}

// chatRosterMissing refuses a --roster the bot's councils do not list, before the conversation prints or
// sends anything: the hub looks a council up in exactly the councils the chat's info lists, so the person
// need not type a prompt it would refuse. Nil on an older hub, when the info lists no council, and for a
// listed name.
func chatRosterMissing(bot string, info *api.ChatInfo, f chatFlags) error {
	if f.roster == "" || info.Council == nil || len(info.Council.Rosters) == 0 {
		return nil
	}
	names := make([]string, 0, len(info.Council.Rosters))
	for _, r := range info.Council.Rosters {
		if r.Name == f.roster {
			return nil
		}
		names = append(names, escapeLine(r.Name))
	}
	return usageErr("There is no council '%s' for %s.\ncouncils: %s", f.roster, escapeLine(bot), strings.Join(names, ", "))
}

// chatSessionLine is said on stderr before the first prompt when the conversation is not on the bot's
// default council: the council it asks (and why that council cannot answer), the one member it asks, or that
// its council is gone.
// Empty on an older hub, on the default council, and with --thread and --roster (the change's own line names
// the council). d is the thread read at the start, nil for a conversation without --thread.
func chatSessionLine(info *api.ChatInfo, f chatFlags, d *api.ChatThreadDetail) string {
	if info.Council == nil {
		return ""
	}
	if d != nil && d.Thread.Member == nil && f.roster == "" && d.Council == nil {
		// A council chat whose council the hub cannot find (deleted, or the bot's default is gone): every
		// turn is refused until the chat gets another council, one with --member included (and --roster
		// takes no --member).
		if f.memberSlug != "" {
			return "· this conversation's council is gone: leave out --member and pick another with --roster"
		}
		return "· this conversation's council is gone: pick another with --roster"
	}
	member := f.memberSlug
	if d != nil && d.Thread.Member != nil {
		member = *d.Thread.Member
	}
	if member != "" {
		line := "· this conversation asks " + escapeLine(cleanBotText(member)) + " alone"
		if d != nil && d.Thread.Member == nil && d.Council != nil && d.Council.Roster.Status == "invalid" {
			// --member on a council chat whose council cannot answer: say why, as for the council itself.
			r := d.Council.Roster
			line += ", on council " + chatCouncilName(r.Name, r.DisplayName) + chatUnavailable(r.Status, r.Reasons)
		}
		return line
	}
	var r *api.RosterSummary
	switch {
	case d == nil && f.roster != "":
		for i := range info.Council.Rosters {
			if info.Council.Rosters[i].Name == f.roster {
				r = &info.Council.Rosters[i]
			}
		}
	case d != nil && f.roster == "" && d.Council != nil:
		r = &d.Council.Roster
	}
	if r == nil || r.Name == info.Council.Roster.Name {
		return ""
	}
	return "· this conversation asks council " + chatCouncilName(r.Name, r.DisplayName) + chatUnavailable(r.Status, r.Reasons)
}

// chatNextChat says what the conversation's next thread asks, for the /new line: ", with council <name>" or
// ", with <member> alone". Empty on an older hub, or when the hub names no council.
func chatNextChat(info *api.ChatInfo, f chatFlags) string {
	switch {
	case info.Council == nil:
		return ""
	case f.memberSlug != "":
		return ", with " + escapeLine(f.memberSlug) + " alone"
	case f.roster != "":
		for _, r := range info.Council.Rosters {
			if r.Name == f.roster {
				return ", with council " + chatCouncilName(r.Name, r.DisplayName)
			}
		}
		return ", with council " + escapeLine(f.roster)
	}
	if name := chatDefaultCouncil(info.Council); name != "" {
		return ", with council " + name
	}
	return ""
}

// chatRosterHint adds the bot's councils to an unknown_roster refusal, from the chat info already
// read, so the person sees what to pick. mapped is err already mapped; any other refusal is returned
// as mapped. roster is the council the hub refused: the info, read before, may still list it (it was
// deleted meanwhile), so it is left out of the list.
func chatRosterHint(mapped, err error, info *api.ChatInfo, roster string) error {
	ce, ok := api.CouncilErrorOf(err)
	if !ok || ce.Code != "unknown_roster" || info == nil || info.Council == nil {
		return mapped
	}
	names := make([]string, 0, len(info.Council.Rosters))
	for _, r := range info.Council.Rosters {
		if r.Name != roster {
			names = append(names, escapeLine(r.Name))
		}
	}
	if len(names) == 0 {
		return mapped
	}
	return newErr(exitCodeOf(mapped), "%s\ncouncils: %s", mapped.Error(), strings.Join(names, ", "))
}

// chatPatchError maps a refusal of the thread PATCH: a pin that does not fit the new council gets
// tnctl's own words (the hub's advice names API fields a person cannot send from tnctl); the rest is
// chatTurnError's.
func chatPatchError(err error, bot, thread, roster string) error {
	if ce, ok := api.CouncilErrorOf(err); ok && ce.Code == "invalid" {
		for _, f := range ce.Fields {
			if f.Path == "membersPin" {
				return newErr(codeRejected, "this chat's pinned members are not all in council '%s': change or clear the pin in the web chat, then run the command again", roster)
			}
		}
	}
	return chatTurnError(err, bot, thread)
}

// chatPatchRoster sets the thread's council (P9) and says so on stderr. It never sends membersPin:
// pins are changed in the web chat only.
func chatPatchRoster(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, thread, roster string, info *api.ChatInfo, f chatFlags) error {
	name := roster
	t, _, err := fl.PatchChatThread(ctx, bot, thread, api.ChatThreadCouncilPatch{SetRoster: true, Roster: &name})
	if err != nil {
		return chatRosterHint(chatPatchError(err, bot, thread, roster), err, info, roster)
	}
	if f.json {
		return nil
	}
	shown := chatCouncilName(roster, "")
	if t != nil && t.Roster != nil {
		n := t.Roster.Name
		if n == "" {
			n = roster
		}
		shown = chatCouncilName(n, t.Roster.DisplayName)
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "· this chat's council is now %s\n", shown)
	return nil
}

// chatOnce runs one turn. With --thread it first sets that chat's council when --roster is given
// (P9), reads the thread (P4) and refuses, for a chat with one member, another --member or
// --thorough; without --thread it makes the thread (P3), with --roster in its body. It then asks
// (P6) and follows the answer to its end.
func chatOnce(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, prompt string, info *api.ChatInfo, f chatFlags, sigs <-chan os.Signal) error {
	errw := cmd.ErrOrStderr()
	threadID := f.thread
	var after int64
	if threadID != "" {
		if f.roster != "" {
			if err := chatPatchRoster(ctx, cmd, fl, bot, threadID, f.roster, info, f); err != nil {
				return err
			}
		}
		d, _, err := fl.ChatThread(ctx, bot, threadID)
		if err != nil {
			return councilError(err)
		}
		if f.memberSlug != "" && d.Thread.Member != nil && *d.Thread.Member != f.memberSlug {
			return usageErr("thread %s is a chat with %s alone: leave out --member, or start a new chat without --thread", escapeLine(cleanBotText(threadID)), escapeLine(cleanBotText(*d.Thread.Member)))
		}
		if f.thorough && d.Thread.Member != nil {
			// The hub would answer "thorough: Thorough asks the whole council, never one member." (exit 5).
			return usageErr("thread %s is a chat with %s alone: leave out --thorough, or start a new chat without --thread", escapeLine(cleanBotText(threadID)), escapeLine(cleanBotText(*d.Thread.Member)))
		}
		after = d.LastSeq
	} else {
		t, _, err := fl.CreateChatThread(ctx, bot, chatThreadRequest(prompt, f))
		if err != nil {
			return chatRosterHint(councilError(err), err, info, f.roster)
		}
		threadID = t.ID
	}
	if !f.json {
		_, _ = fmt.Fprintf(errw, "thread %s\n", escapeLine(cleanBotText(threadID)))
	}
	return chatRunTurn(ctx, cmd, fl, bot, threadID, after, chatTurnRequest(prompt, f), f, sigs)
}

// chatRunTurn posts one turn and follows it. A turn that fails is an error with its exit code. A
// Ctrl-C during the requests before it (P9, P4, P3) waits in sigs: it is taken here, once and without
// waiting, so the turn is not posted only for its watcher to cancel it.
func chatRunTurn(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, thread string, after int64, req api.NewChatTurn, f chatFlags, sigs <-chan os.Signal) error {
	select {
	case <-sigs:
		return errChatNothingAsked
	default:
	}
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
		return councilError(err)
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

// chatFollowOpenTurn follows the thread's last turn when it has not ended: a conversation opened on a
// thread picks up where the person left it. d is the thread as the conversation just read it. A thread
// whose last turn ended, or that has none, is left alone.
func chatFollowOpenTurn(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot, thread string, d *api.ChatThreadDetail, f chatFlags, sigs <-chan os.Signal) error {
	last := chatLastTurn(d)
	if last == nil || (last.State != "queued" && last.State != "running") {
		return nil
	}
	return runChatTurn(ctx, fl, bot, thread, last.ID, 0, newChatTurnPrinter(cmd, f), sigs)
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

// chatRepl talks until /quit, EOF or Ctrl-C at the prompt. Its thread is made at the first prompt, unless
// --thread names one: that thread is read once, checked against the flags, its open turn followed and,
// with --roster, its council changed, all before the first prompt.
func chatRepl(ctx context.Context, cmd *cobra.Command, fl *api.Fleet, bot string, info *api.ChatInfo, f chatFlags, sigs <-chan os.Signal) error {
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := chatRosterMissing(bot, info, f); err != nil {
		return err
	}
	// slugs are the members `@` asks in the current chat: the members of the thread's council as last
	// read, or of the council the next thread will have.
	var slugs []string
	if f.thread == "" {
		slugs = chatSlugsForNew(info, f)
		printChatInfo(out, bot, info, slugs)
		if line := chatSessionLine(info, f, nil); line != "" {
			_, _ = fmt.Fprintln(errw, line)
		}
	} else {
		d, _, err := fl.ChatThread(ctx, bot, f.thread)
		if err != nil {
			// The thread cannot be read: no turn can be posted on it either (each one reads the thread
			// first), so the conversation ends with the platform's error.
			return councilError(err)
		}
		if d.Thread.Member != nil {
			// A chat with one member takes no other member, no --thorough and no council: say so before
			// anything is printed (the hub would refuse the first two at every prompt, and the change).
			tid, alone := escapeLine(cleanBotText(f.thread)), escapeLine(cleanBotText(*d.Thread.Member))
			switch {
			case f.memberSlug != "" && *d.Thread.Member != f.memberSlug:
				return usageErr("thread %s is a chat with %s alone: leave out --member, or start a new chat without --thread", tid, alone)
			case f.thorough:
				return usageErr("thread %s is a chat with %s alone: leave out --thorough, or start a new chat without --thread", tid, alone)
			case f.roster != "":
				return usageErr("thread %s is a chat with %s alone: leave out --roster, or start a new chat without --thread", tid, alone)
			}
		}
		if f.memberSlug != "" && d.Thread.Member == nil && info.Council != nil && d.Council != nil {
			// A council chat asks --member alone at every prompt: one its council does not have would be
			// refused at each one. (--member and --roster never come together.)
			if members := chatSlugList(d.Council.Roster.Members); !slices.Contains(members, f.memberSlug) {
				list := "none"
				if len(members) > 0 {
					list = strings.Join(members, ", ")
				}
				return usageErr("thread %s asks council %s, which has no member %s: leave out --member, or start a new chat without --thread\nmembers: %s",
					escapeLine(cleanBotText(f.thread)), chatCouncilName(d.Council.Roster.Name, d.Council.Roster.DisplayName), escapeLine(f.memberSlug), list)
			}
		}
		// With --roster, what `@` asks once the council is changed below.
		slugs = chatSlugsForThread(info, d)
		if f.roster != "" {
			slugs = chatSlugsForRoster(info, f.roster)
		}
		printChatInfo(out, bot, info, slugs)
		_, _ = fmt.Fprintf(errw, "thread %s\n", escapeLine(cleanBotText(f.thread)))
		if line := chatSessionLine(info, f, d); line != "" {
			_, _ = fmt.Fprintln(errw, line)
		}
		// A Ctrl-C during the thread's read ends the conversation before it follows (and so cancels), changes
		// or asks anything.
		select {
		case <-sigs:
			return errChatNothingAsked
		default:
		}
		// A conversation opened on a thread with a turn still running follows it first.
		if err := chatFollowOpenTurn(ctx, cmd, fl, bot, f.thread, d, f, sigs); err != nil {
			if errors.Is(err, errChatInterrupted) || exitCodeOf(err) == codeAuth {
				// A second Ctrl-C left the turn, or the person may no longer chat (the next turn would fail
				// too): either ends the conversation, and its caller prints the error once.
				return err
			}
			_, _ = fmt.Fprintf(errw, "Error: %v\n", err)
		}
		if f.roster != "" {
			// Once, after the open turn ended (the hub refuses to change the council of a chat with a turn
			// open). A refusal ends the conversation: asking the old council instead would be wrong.
			// A Ctrl-C that arrived as the followed turn ended is the turn's, not the change's.
			drainSignals(sigs)
			if err := chatPatchRoster(ctx, cmd, fl, bot, f.thread, f.roster, info, f); err != nil {
				return err
			}
			// The change, like a new thread, accepts a council that cannot answer: say why, after the
			// change's own line.
			if info.Council != nil {
				for _, r := range info.Council.Rosters {
					if r.Name == f.roster && r.Status == "invalid" {
						_, _ = fmt.Fprintln(errw, "· council "+chatCouncilName(r.Name, r.DisplayName)+chatUnavailable(r.Status, r.Reasons))
					}
				}
			}
			// A Ctrl-C during the change ends the conversation before it prompts, as one during the read
			// does; the change stays, as in the one-shot.
			select {
			case <-sigs:
				return errChatNothingAsked
			default:
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
				threadID, after = "", 0
				slugs = chatSlugsForNew(info, f)
				_, _ = fmt.Fprintln(errw, "· a new thread starts at your next message"+chatNextChat(info, f))
				continue
			case line == "/cancel":
				if err := chatCancelOpenTurn(ctx, errw, fl, bot, threadID); err != nil {
					_, _ = fmt.Fprintf(errw, "Error: %v\n", err)
				}
				continue
			case strings.HasPrefix(line, "/"):
				_, _ = fmt.Fprintln(out, chatHelpLine(slugs))
				continue
			}
			if threadID != "" {
				// The thread's council decides what @ asks (it may have changed meanwhile): read the thread
				// before the line is matched.
				d, _, err := fl.ChatThread(ctx, bot, threadID)
				if err != nil {
					mapped := councilError(err)
					if exitCodeOf(mapped) == codeAuth {
						return mapped // the person may no longer chat: the next line would fail too
					}
					_, _ = fmt.Fprintf(errw, "Error: %v\n", mapped)
					continue
				}
				after = d.LastSeq
				slugs = chatSlugsForThread(info, d)
			}
			member, prompt := chatMemberLine(line, slugs)
			if prompt == "" {
				_, _ = fmt.Fprintf(errw, "· type the task for @%s\n", member)
				continue
			}
			if threadID == "" {
				t, _, err := fl.CreateChatThread(ctx, bot, chatThreadRequest(prompt, f))
				if err != nil {
					mapped := chatRosterHint(councilError(err), err, info, f.roster)
					if chatThreadRefusalEnds(err) || exitCodeOf(mapped) == codeAuth {
						// The same flags would be refused at every prompt, or the person may no longer chat.
						return mapped
					}
					_, _ = fmt.Fprintf(errw, "Error: %v\n", mapped)
					continue
				}
				// A new thread starts before its first event: after is 0, as its ids start at 1.
				threadID, after = t.ID, 0
				_, _ = fmt.Fprintf(errw, "thread %s\n", escapeLine(cleanBotText(threadID)))
			}
			req := chatTurnRequest(prompt, f)
			if member != "" {
				// @<slug> asks one member alone: a mode is the council's, not the member's.
				req.Member, req.Mode, req.Thorough = member, "", false
			}
			if err := chatRunTurn(ctx, cmd, fl, bot, threadID, after, req, f, sigs); err != nil {
				if errors.Is(err, errChatNothingAsked) {
					// Ctrl-C during the line's requests: nothing was posted; the next prompt follows.
					_, _ = fmt.Fprintln(errw, "· interrupted: nothing was asked")
					continue
				}
				if errors.Is(err, errChatInterrupted) || exitCodeOf(err) == codeAuth {
					// A second Ctrl-C left the turn, or the person may no longer chat (the next turn would fail
					// too): either ends the conversation, and its caller prints the error once.
					return err
				}
				_, _ = fmt.Fprintf(errw, "Error: %v\n", err)
			}
		}
	}
}

// chatThreadRefusalEnds says whether a refusal of a new thread comes from the conversation's own flags (an
// unknown council, an unknown or ambiguous member, a member only the creator may use): the same flags would
// be refused at every prompt. The hub's code decides, never the HTTP status.
func chatThreadRefusalEnds(err error) bool {
	ce, ok := api.CouncilErrorOf(err)
	if !ok {
		return false
	}
	switch ce.Code {
	case "unknown_roster", "unknown_member", "ambiguous", "creator_only":
		return true
	}
	return false
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

// chatMemberLine reads "@<slug> <text>" for a slug in slugs: the member asked alone, and the text. Any other
// line, "@<word>" for a word not in slugs included, is all prompt.
func chatMemberLine(line string, slugs []string) (member, prompt string) {
	rest, ok := strings.CutPrefix(line, "@")
	if !ok {
		return "", line
	}
	token, text := rest, ""
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		token, text = rest[:i], rest[i:]
	}
	if token == "" || !slices.Contains(slugs, token) {
		return "", line
	}
	return token, strings.TrimSpace(text)
}

// chatCancelOpenTurn cancels the thread's open turn, if it has one.
func chatCancelOpenTurn(ctx context.Context, errw io.Writer, fl *api.Fleet, bot, thread string) error {
	if thread == "" {
		_, _ = fmt.Fprintln(errw, "· no thread yet: nothing to cancel")
		return nil
	}
	d, _, err := fl.ChatThread(ctx, bot, thread)
	if err != nil {
		return councilError(err)
	}
	for i := len(d.Turns) - 1; i >= 0; i-- {
		t := d.Turns[i]
		if t.State != "queued" && t.State != "running" {
			continue
		}
		if _, _, err := fl.CancelChatTurn(ctx, bot, thread, t.ID); err != nil {
			return councilError(err)
		}
		_, _ = fmt.Fprintf(errw, "· cancelling turn %s\n", escapeLine(cleanBotText(t.ID)))
		return nil
	}
	_, _ = fmt.Fprintln(errw, "· no open turn to cancel")
	return nil
}

// printChatInfo is what the conversation says about the bot before the first prompt: the version of its
// computer, its default council (and why that council cannot answer, when it cannot), and the members of
// that council with each one's part. slugs are the members the conversation's `@` asks.
func printChatInfo(w io.Writer, bot string, info *api.ChatInfo, slugs []string) {
	online := "offline"
	if info.Bot.Online {
		online = "online"
	}
	access := info.Bot.Access
	if access == "" {
		access = api.BotAccessNone
	}
	computer := "unknown"
	if info.Bot.Council != nil && *info.Bot.Council != "" {
		computer = *info.Bot.Council
	}
	_, _ = fmt.Fprintf(w, "%s (%s, %s) — computer: %s\n", bot, escapeLine(cleanBotText(access)), online, escapeLine(cleanBotText(computer)))
	c := info.Council
	if c != nil {
		if name := chatDefaultCouncil(c); name != "" {
			_, _ = fmt.Fprintln(w, "council: "+name+chatUnavailable(c.Roster.Status, c.Roster.Reasons))
		}
	}
	if len(info.Members) > 0 {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for i, m := range info.Members {
			name, model, effort := terminalText(cleanBotText(m.Name)), terminalText(cleanBotText(m.Model)), terminalText(cleanBotText(m.Effort))
			if c == nil {
				// An older hub: no council to place the members in.
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", name, model, effort)
				continue
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, chatMemberPart(c.Roster, i, m.ID, slugs), model, effort)
		}
		_ = tw.Flush()
	}
	_, _ = fmt.Fprintf(w, "tokens today: %d of %d\n", info.Budget.TokensToday, info.Budget.TokensPerDay)
}

// chatMemberPart is a member's part in the default council, for one cell: leads, reviews, @<slug> when the
// conversation's `@` asks that member, else member. The chat's members line up one to one with the council's
// roles; where a role's slug differs (a platform that sends no roles), the council's lead and reviewer slugs
// decide.
func chatMemberPart(r api.RosterView, i int, id string, slugs []string) string {
	kind := "member"
	switch {
	case i < len(r.Roles) && r.Roles[i].Slug == id:
		kind = r.Roles[i].Kind
	case id == r.Lead:
		kind = "lead"
	case r.Reviewer != nil && id == *r.Reviewer:
		kind = "reviewer"
	}
	switch kind {
	case "lead":
		return "leads"
	case "reviewer":
		return "reviews"
	}
	if slices.Contains(slugs, id) {
		return "@" + councilCell(id)
	}
	return "member" // `@` does not ask it here: another council, or a chat with one member
}

// printChatThreads is the threads table.
func printChatThreads(w io.Writer, threads []api.ChatThread) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tTITLE\tCOUNCIL\tTURNS\tLAST\tUPDATED")
	for _, t := range threads {
		last := "-"
		if t.LastTurn != nil && t.LastTurn.State != "" {
			last = escapeLine(t.LastTurn.State)
		}
		title := terminalText(cleanBotText(t.Title))
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", escapeLine(t.ID), cutRunes(title, 50), chatThreadCouncil(t), t.TurnCount, last,
			time.UnixMilli(t.UpdatedAt).Local().Format("2006-01-02 15:04"))
	}
	_ = tw.Flush()
}

// chatThreadCouncil is a thread's council for one table cell (tab-safe): @<member> for a chat with
// one member, the council's name when the thread names one, and (default) for a thread that follows
// the bot's default council (a council's name cannot hold parentheses, so it cannot be read as one).
func chatThreadCouncil(t api.ChatThread) string {
	if t.Member != nil && *t.Member != "" {
		return "@" + councilCell(*t.Member)
	}
	if t.Roster != nil && t.Roster.Name != "" {
		return councilCell(t.Roster.Name)
	}
	return "(default)"
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
	// However the turn ends, stdout does not stay inside the Captain's text. Deferred first, so it
	// runs last: after the watcher is done.
	defer p.endLine()

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
						p.progress("cancel failed: " + escapeLine(cleanBotText(councilError(err).Error())))
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
	// names are the display names the bot's computer sent this turn, by member slug: an older
	// computer sends none, and its lines keep the slugs.
	names := map[string]string{}
	remember := func(member, name string) {
		if member == "" {
			return
		}
		if n := hubText(name); n != "" {
			names[member] = n
		}
	}
	who := func(member, name string) string {
		if n := hubText(name); n != "" {
			return n
		}
		if n := names[member]; n != "" {
			return n
		}
		return hubText(member)
	}
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
						// The stream's refusal is a council route's: the hub's code decides the exit and its
						// words are printed as P6's are (a code-less gateway answer keeps classify).
						return councilError(err)
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
			case "turn_start":
				var d struct {
					Roster *struct {
						Name        string `json:"name"`
						DisplayName string `json:"displayName"`
					} `json:"roster"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				if d.Roster != nil && d.Roster.Name != "" {
					p.progress("council " + chatCouncilName(d.Roster.Name, d.Roster.DisplayName) + " answers")
				}
			case "plan":
				var d struct {
					Mode  string `json:"mode"`
					Tasks []struct {
						Member string `json:"member"`
						Name   string `json:"name"`
						Task   string `json:"task"`
					} `json:"tasks"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				line := d.Mode
				if len(d.Tasks) > 0 {
					shown := make([]string, 0, len(d.Tasks))
					for _, t := range d.Tasks {
						remember(t.Member, t.Name)
						shown = append(shown, who(t.Member, t.Name))
					}
					line += ": " + strings.Join(shown, ", ")
				}
				p.progress(strings.TrimRight(line, " "))
				// A council's plan comes before its members work: the lead's text so far was not the
				// answer. A quick turn's plan comes after its answer (pkg/council askTurn), so the text
				// streamed so far is the answer the final repeats: keep it, or the answer prints twice.
				if d.Mode != "quick" {
					p.resetCaptainText()
				}
			case "member_start":
				var d struct {
					Member string `json:"member"`
					Name   string `json:"name"`
					Text   string `json:"text"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				remember(d.Member, d.Name)
				line := who(d.Member, d.Name)
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
				p.progress(who(d.Member, "") + " → " + terminalText(cleanBotText(d.Tool)))
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
					Name   string `json:"name"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				remember(d.Member, d.Name)
				p.progress(who(d.Member, d.Name) + " done")
				p.resetCaptainText()
			case "critique":
				var d struct {
					Member string `json:"member"`
					Name   string `json:"name"`
					Issues []struct {
						Severity string `json:"severity"`
					} `json:"issues"`
				}
				if err := json.Unmarshal(ev.Data, &d); err != nil {
					return fmt.Errorf("malformed chat event: %w", err)
				}
				remember(d.Member, d.Name)
				shown := who(d.Member, d.Name)
				if shown == "" {
					shown = "critic"
				}
				blocking := 0
				for _, i := range d.Issues {
					if i.Severity == "blocking" {
						blocking++
					}
				}
				p.progress(fmt.Sprintf("%s: %d issues (%d blocking)", shown, len(d.Issues), blocking))
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
	// Every branch ends the line.
	p.lastNewline = true
}

// endLine ends the line the Captain's text left open when the turn ends without a final that ends
// it (cancelled, failed, interrupted, or its stream lost): the error and the next prompt then start
// a line of their own.
func (p *chatTurnPrinter) endLine() {
	if p.json || !p.tty || !p.printedAny || p.lastNewline {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = fmt.Fprint(p.out, "\n")
	p.lastNewline = true
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
