# `tnctl chat`

Chat with a TeknoirBot's council, or with one of its members, and read the answer as it is written.

```bash
tnctl chat <bot> [prompt...] [--thread <id>] [--new] [--roster <council>] [--thorough]
                             [--member <slug>|<scope>/<slug>] [--json]
                             [--mode auto|quick|council] [--effort low|medium|high]
tnctl chat <bot> --thread <id> --follow [--json]
tnctl chat threads <bot> [--json]
```

`--mode` and `--effort` are for advanced use: without `--mode` or `--thorough` the lead decides how the council
answers, and without `--effort` the lead, each member and the reviewer think at their own effort (the Coder, in a
thread it already worked in, at the effort its session started with).

A TeknoirBot is an AI bot with its own computer on the Teknoir platform. Its **council** answers you: a council has
a lead, its members, and a reviewer when it has one ([`council roster`](council-roster.md) has the premade councils
and the bot's own). The lead takes each council turn: it answers a simple question itself or convenes members, at
most 3 per turn. The reviewer checks the members' reports when the turn needs it, not on every turn, and a tool
result that is an error does not make it run by itself. `--thorough` (in a council chat the same as `--mode council`)
has the lead convene members, still at most 3, and the reviewer, when the council has one, check every report:
slower, and several times the tokens. The lead still answers alone when it has no member it may convene (at Off
(`none`) the Standard council has only the Coder, when code is asked for) or when its tool rounds, the budget or the
time run out first ([`council`](council.md#turn-modes)).

`--roster` picks the council: standard, ops, review or one of the bot's own. A new council chat without it gets
the bot's default council; `tnctl council roster list --bot <bot>` lists the councils, and
`tnctl council roster use <council> --bot <bot>` makes another one the default. With `--thread`, `--roster` changes
that chat's council first. `--member` asks one member alone, without a council ([`council role`](council-roles.md)).

`tnctl chat` is a person's chat with the bot's council or one of its members; a bot's own `tnctl` refuses it before
anything is sent, exit 6. Its `--help` still prints, and a missing bot, an extra argument to `chat threads`, an
unknown flag or a flag value of the wrong type is a usage error first (exit 2); other wrong flags, such as a bad
`--mode`, get the exit-6 refusal ([Bot mode](../bot-mode.md)).

## Who may chat

Who may chat with a bot is who may open its desktop:

| The bot's access | Who may chat |
| --- | --- |
| Act as me (`creator`) | Its creator. |
| Read only (`read`), Off (`none`) | Its creator, and the teamspace's admins. |

The caller also needs a device role in the teamspace (viewer, developer or admin) and permission to open a terminal
(`pods/exec`) there. Without a device role, `tnctl chat` prints the platform's reason alone, such as `You do not
have access to devices in teamspace <teamspace>. Ask a teamspace admin for read access.`, exit 6. A bot outside your
organisation units is not there for you: `No bot named <bot> in teamspace <teamspace>.`, exit 3. Anyone else is
refused before anything is asked: `you cannot chat with <bot>: <reason>`, with the platform's reason (who may open
the bot's computer), exit 6; `tnctl chat threads <bot>` prints the platform's reason alone, also exit 6. This check does not
look at whether the bot runs: a turn on a bot that is not running (suspended or starting, for example) or is being
removed is refused when it is asked, with the platform's sentence, such as `Resume the bot to chat.`, exit 8 (see
[Exit codes](#exit-codes)); in a conversation the error is printed and the prompt comes back.

A bot at Act as me (`creator`) runs the premade members and councils and, of its own members and councils and the
members the teamspace shares, only what its creator wrote: a council as last changed, and a member at the version
that runs (its latest, unless the council pins one). A chat that asks for one someone else wrote is refused with
`creator_only`, exit 1. A council chat counts its council and each member it runs, lead and reviewer included; a
member asked alone (`--member`, `@<slug>`) counts by itself, and with its council when that council pins its
version. The platform's text: `<bot> acts as its creator, and <Name> was last changed by <email>: at Act as me it
runs only members and councils its creator wrote. Clone it to this bot, or ask <creator>.`

## One turn, or a conversation

```bash
tnctl chat scout "what is running on my robot?"
tnctl chat scout --thread thr_123 "and the pods?"
tnctl chat scout --roster review --thorough "is the update safe to roll out?"
tnctl chat scout --member triage-sre "why is arm-2 offline?"
```

With a prompt, one turn runs and the command exits when the turn ends. With no prompt — and stdin and stdout
both terminals — the command starts a conversation: the bot's banner goes to stdout, then the `> ` prompt.

stdout of `tnctl chat scout`, before the prompt:

```text
scout (creator, online) — computer: tnctl/v0.3.0
council: Ops scout (ops-scout)
Captain         leads            claude-sonnet-5-5  high
Fleet           @fleet           claude-haiku-5-5   medium
Stream watcher  @stream-watcher  claude-haiku-5-5   medium
Cost watcher    @cost-watcher    claude-haiku-5-5   medium
Critic          reviews          claude-sonnet-5-5  medium
tokens today: 123456 of 5000000
```

The first line is the bot: its access (`creator`, `read` or `none`; see [Who may chat](#who-may-chat)), whether
its computer is `online` or `offline`, and the version of its own computer (`tnctl/<version>`, or `unknown` when
the platform does not say). The `council: <Name> (<name>)` line names the bot's default council: its display
name, then its name in parentheses (the name alone when the council has no display name of its own, as when the
default council is gone), and adds ` — unavailable: <reasons>` when that council cannot answer. One row per member
of that council follows: its name, its part, its model and its effort. The part is `leads` for the council's
lead, `reviews` for its reviewer, `@<slug>` for a member this conversation's `@` can ask, and `member` for any
other member, which `@` cannot ask: one the conversation's council does not have (Stream watcher and Cost watcher
with `--roster ops`), and every one in a chat with one member (a new chat with `--member`, or `--thread` on a chat
with one member). `--thread` on a council chat with `--member` keeps the `@<slug>` parts of the thread's council
(see below). A member of the conversation's council that the default council lacks has no row. An older platform
prints no `council:` line and no part column; a default council that is gone has no member rows.
The last line is the bot's budget: the tokens it spent today, of the tokens it may spend in a day.

The banner always names the bot's **default** council and its members, whatever this conversation asks: a
`--roster`, `--member` or `--thread` does not change it. What this conversation asks is one line on stderr
before the first prompt:

- `· this conversation asks council <name>`, with ` — unavailable: <reasons>` when that council cannot answer;
- `· this conversation asks <member> alone`, with `, on council <name> — unavailable: …` when `--thread` opens a
  chat that asks a council, `--member` asks one of its members, and that council cannot answer;
- `· this conversation's council is gone: pick another with --roster`, when `--thread` opens a chat whose council
  is gone (see [The thread](#the-thread)); with `--member`:
  `· this conversation's council is gone: leave out --member and pick another with --roster`.

There is no such line when the conversation asks the bot's default council, when it is opened with `--thread`
and `--roster` (the `· this chat's council is now` line names the council instead), or on an older platform.

stderr of `tnctl chat scout --roster ops`:

```text
· this conversation asks council Ops (ops)
```

stderr of `tnctl chat scout --member stream-watcher`:

```text
· this conversation asks stream-watcher alone
```

stderr of `tnctl chat scout --roster ops-scout` when scout's default is another council and ops-scout names a
member the platform reports as gone:

```text
· this conversation asks council Ops scout (ops-scout) — unavailable: old-watch: this member was archived or deleted; replace it in this council.
```

| At the `> ` prompt | Does |
| --- | --- |
| a line of text | one turn with the flags you gave: of the chat's council, or, in a chat with one member (`--member <slug>`, or `--thread` on a chat with one member), of that member alone |
| `@<slug> <text>` | one turn of that member of the chat's council alone. `--effort` applies to it (the Coder, in a thread it already worked in, keeps its session's effort); `--mode` and `--thorough` do not |
| `/new` | the next line starts a new thread |
| `/cancel` | cancels the thread's open turn, if it has one |
| `/quit` | ends the conversation (exit 0). Ctrl-C at the prompt does the same |
| any other line that starts with `/`, such as `/var/log/syslog is full` or `/new now` | the help line: the commands, and the chat's `@` members; the line is not sent |

The chat's council, whose members `@` asks, is the thread's council once the thread exists, read again before each
line. Before the thread exists, at the start and after `/new`, it is the `--roster` council, or the bot's default
council without `--roster`. With `--thread <id> --roster <council>` it is that council. In a chat with one member
there is no `@`: an `@word` line is sent as it is. In a council chat, an `@word` line whose word is not one of the
members `@` asks is sent as it is too, and `@<slug>` with no text asks nothing: stderr gets
`· type the task for @<slug>`, and the prompt comes back. `--thread` on a council chat with `--member <slug>` stays a
council chat until `/new`: a plain line asks that member alone, and `@<slug>` asks that member of the thread's
council instead; after `/new` the next thread is a chat with that member alone. On an older platform `@` asks only
`@fleet` and `@coder`. Any other line that starts with `/` is not sent: it prints the help line, which lists the
commands and the members the chat's `@` asks; in `tnctl chat scout`, which asks the bot's default council, it is
`commands: /new /cancel /quit, @fleet, @stream-watcher, @cost-watcher`.

An `@<slug>` line runs a member turn, not a council turn: the member's own progress lines on stderr and its
report as the answer, with no lead, no plan and no reviewer. stderr of `tnctl chat scout` when
`@fleet why does amr-02 restart?` is typed at the prompt:

```text
thread thr_8fa2Wn4Kt7Rb2Lm8Qp3XdA
· Fleet: why does amr-02 restart?
· Fleet → get_problems
· Fleet done
done in 6.2s, 2 calls, 1600 tokens (+9800 cached)
```

`/new` says what the next thread asks: `· a new thread starts at your next message, with council <name>` or
`, with <member> alone`; an older platform adds no suffix. stderr of `tnctl chat scout --roster ops` when
`/new` is typed:

```text
· this conversation asks council Ops (ops)
· a new thread starts at your next message, with council Ops (ops)
```

An empty line does nothing. Each line is one turn, run in the foreground: the prompt comes back when the turn
ends.

When stdin is not a terminal and there is no prompt, all of stdin is the prompt: `echo "what runs?" | tnctl
chat scout` is one turn. `--follow` reads no stdin at all: `ssh host 'tnctl chat scout --thread
thr_8fa2Pt6Jd2Nm5Kd7VnQ9Ww --follow'` does not wait for one, and inside a `while read` loop it leaves the
loop's input alone.

## The thread

Every chat is a **thread**. Its id is printed on stderr as soon as the thread is made
(`thread thr_8fa2Rb4Wn7Kt3Pb6Xc9Vdg`; not with `--json`), so a later run continues it with
`--thread thr_8fa2Rb4Wn7Kt3Pb6Xc9Vdg`; without `--thread` a new thread starts, with the first line of the prompt
(at most 60 characters) as its title and `origin: "cli"` as where it came from. `--new` with `--thread` is a
usage error: one or the other. In a conversation the thread is made at the first prompt, so a `/quit` before it
creates nothing.

`--thread <id> --roster <council>` in a conversation changes the chat's council once, before the first
prompt: `· this chat's council is now <name>` on stderr, then `· council <name> — unavailable: …` when that
council cannot answer. In a conversation the `thread <id>` line comes first, and the change is made after a
turn still open on the thread has been followed (see [Ctrl-C and reconnects](#ctrl-c-and-reconnects)); with a
prompt the change comes first, then `thread <id>`. A change the platform refuses ends the conversation.

stderr of `tnctl chat scout --thread thr_8fa2Pt6Jd2Nm5Kd7VnQ9Ww --roster ops`:

```text
thread thr_8fa2Pt6Jd2Nm5Kd7VnQ9Ww
· this chat's council is now Ops (ops)
```

A chat whose council was deleted asks the bot's default council from then on; only when the default itself is gone
is the chat's council gone. A conversation opened on such a chat with `--thread` and no `--roster` says so once,
before its first prompt; stderr of `tnctl chat scout --thread thr_8fa2Qm4Wb7Pt8Zx3Nc6HqQ`:

```text
thread thr_8fa2Qm4Wb7Pt8Zx3Nc6HqQ
· this conversation's council is gone: pick another with --roster
```

The platform refuses to delete a bot's default council (`roster_default`), so a default is gone when, for example,
a platform upgrade dropped the premade council it names ([`council roster`](council-roster.md#roster-list)).

A chat with one member takes no other member and no `--thorough`, with a prompt or in a conversation, and no
`--roster` in a conversation (with a prompt the platform refuses that change). tnctl refuses it before
anything is printed, with exit 2. stderr of `tnctl chat scout --thread thr_8fa2Jd9Nm4Xb7Rm2VnQ5Kg --thorough`:

```text
Error: thread thr_8fa2Jd9Nm4Xb7Rm2VnQ5Kg is a chat with stream-watcher alone: leave out --thorough, or start a new chat without --thread
Run the command with --help for usage and examples.
```

`--member` naming another member, and `--roster` in a conversation, print that sentence with their own flag in
place of `--thorough`.

Only in a conversation on a council chat (`--thread`), a `--member` the thread's council does not have is refused
the same way:
`thread <id> asks council <name>, which has no member <X>: leave out --member, or start a new chat without
--thread`, with a `members: …` line. With a prompt the platform refuses the turn (`unknown_member`, exit 2).
stderr, exit 2, of `tnctl chat scout --thread thr_8fa2Wb7Kt4Qm9Wc2Rq4NmA --member triage-sre`:

```text
Error: thread thr_8fa2Wb7Kt4Qm9Wc2Rq4NmA asks council Review (review), which has no member triage-sre: leave out --member, or start a new chat without --thread
members: architect, security-risk, edge-operator, ux-product, cost
Run the command with --help for usage and examples.
```

A `--roster` the bot does not have is refused with `There is no council '<council>' for <bot>.` and a
`councils: …` line, exit 2 — before the banner in a conversation.

Once the conversation has shown its prompt, two kinds of refusal end it: a new thread refused with
`unknown_roster`, `unknown_member`, `ambiguous` or `creator_only` (the same request would be refused at every
prompt), and any refusal of who you are (exit 6) while it runs a turn, makes the thread or reads it for a line.
Before the first prompt, a conversation opened with `--thread` ends on any refusal of its first read or of its
`--roster` change, and on a refusal of who you are while it follows an open turn. Any other refusal, a refused
`/cancel` included, is printed and the prompt comes back.

stdout of `tnctl chat threads scout`, which lists the bot's threads, whoever started them, the most recently changed
first and at most 100 (UPDATED is in your local time zone; this sample ran in UTC):

```text
ID                          TITLE                                     COUNCIL          TURNS  LAST       UPDATED
thr_8fa2Kd7QmVt3LnHq9Wb6Pg  why does amr-02 restart after the update  ops-scout        4      done       2026-11-15 10:42
thr_8fa2Wb7Kt4Qm9Wc2Rq4NmA  review the update plan for the fleet      review           1      failed     2026-11-15 10:00
thr_8fa2Vt3LnHq9Wb6Pt4Xc7Q  why do the amr robots lose MQTT at night  ops              2      done       2026-11-15 09:30
thr_8fa2Hq9Wb6Pt4Xc7Rm2VnQ  disk pressure on edge-2                   @triage-sre      2      done       2026-11-15 09:00
thr_8fa2Jd9Nm4Xb7Rm2VnQ5Kg  overnight MQTT gaps on the amr topics     @stream-watcher  3      cancelled  2026-11-15 08:00
thr_8fa2Pt6Jd2Nm5Kd7VnQ9Ww  what runs on this robot?                  (default)        1      done       2026-11-15 07:00
thr_8fa2Qm4Wb7Pt8Zx3Nc6HqQ  which robots missed the last update?      (default)        2      done       2026-11-14 16:10
```

The COUNCIL column shows `@<member>` for a chat with one member, the council's name for a chat that names its
council, even when that council is the default (the first row names ops-scout, scout's default), or `(default)`
for a chat that names none and follows the bot's default council, whichever that is.

## Flags

| Flag | Description |
| --- | --- |
| `--thread <id>` | Continue this thread (the default is a new one). |
| `--new` | Start a new thread (with `--thread`: a usage error). |
| `--follow` | Follow the thread's last turn instead of asking anything — its events while it runs, its answer when it already ended. It needs `--thread` and takes no prompt. |
| `--roster <council>` | The council that answers: standard, ops, review or one of the bot's own; with `--thread` it changes that chat's council first. |
| `--thorough` | Have the lead convene members, at most 3, and the reviewer, when the council has one, check every report: slower, several times the tokens (in a council chat the same as `--mode council`; a chat with one member refuses it, exit 2). The lead's own answer stands when it has no member it may convene, or its tool rounds, the budget or the time run out first. |
| `--member <slug>|<scope>/<slug>` | Ask one member alone: its slug, or `<scope>/<slug>` (scope bot, teamspace or template); with `--thread`, the slug alone. |
| `--mode auto|quick|council` | Advanced: how the council answers: `auto` (the default) lets the lead answer itself or convene members, `quick` has it answer itself, `council` is the same as `--thorough` in a council chat (a chat with one member drops it). |
| `--effort low|medium|high` | Advanced: how hard the members think: low, medium or high. It is a ceiling: the lead, a member or the reviewer whose own effort is lower keeps its own. The Coder keeps the effort its OpenCode session in the thread started with ([`council`](council.md)). |
| `--json` | Print each event of the turn as one JSON line. Not available in the conversation. With `chat threads`: print the platform's answer as JSON instead of the table. |

The command refuses these before anything is sent, as usage errors (exit 2):

- no bot argument (`chat needs a bot, for example: tnctl chat scout "what is running?"`, then a `usage:` line), or
  one that cannot be a bot's name (a bot that is not there is exit 3);
- a bad `--mode`, `--effort` or `--roster` value, or a `--member` that is neither a member's slug such as
  `triage-sre` nor `<scope>/<slug>` with scope bot, teamspace or template;
- `--member` with `--mode` or with `--roster`; `--thorough` with `--member`, or with a `--mode` other than council;
- `--new` with `--thread`; `--thread .` or `..`; `--member <scope>/<slug>` with `--thread`;
- `--follow` without `--thread`, with a prompt, or with `--roster`;
- a prompt over 16,000 characters;
- without `--follow`: `--json` without a prompt, and no prompt when stdin or stdout is not a terminal and stdin
  gives none (`chat needs a prompt: give it as arguments, or run tnctl chat <bot> in a terminal to talk`).

## Output

stdout gets the answer. On a terminal, the text the lead streams is printed as it is written, and the turn's
`final` is not printed a second time: when it says the same as the lead's text since the turn began, or since the
last `member_done`, `critique`, `plan` (a quick turn's `plan` does not count) or `tool_call` of the lead, only its
line is ended. After other streamed text, the final is printed as a paragraph of its own (a blank line first).
Before a line goes to stderr, a newline on stdout ends the line the lead's text left open, so the two streams do
not share a line on the screen. When stdout is not a terminal only the final text is printed. A member turn — an
`@<slug>` line in a conversation, `--member`, or any turn of a chat with one member — prints its `final`: the
member's report is the answer.

Without `--json`, a `final` the platform shortened for the stream (`cut: true`) is held back: at the turn's end the
command reads the thread's own record and prints the answer whole. When that read fails, or finds no final, it
prints the shortened text after all, with `the answer was shortened by the stream; read it whole with: tnctl chat
<bot> --thread <id> --follow` on stderr. With `--json` (`--follow` included) the shortened `final` event is printed
as it came, with `"cut":true`.

stderr gets a short line, starting with `·`, for each step of the council, so you can follow what it is doing; the
paragraph below the sample explains each line, the two without `·` included. One council turn, with stdout not a
terminal:

```bash
tnctl chat scout --roster ops "why does amr-02 restart?"
```

stdout:

```text
amr-02 restarted four times in the last hour. Its vision-notebook container goes over its memory limit once the DeepStream pipeline has loaded its model, and the kernel kills it (OOMKilled); the restart policy brings it back. Next step: lower the pipeline's batch size, then watch the pod's restarts for an hour.
```

stderr of `tnctl chat scout --roster ops "why does amr-02 restart?"`:

```text
thread thr_8fa2Rq4Nm7Vb3Xd6Kp9WsQ
· council Ops (ops) answers
· council: Fleet, Triage SRE
· Fleet: Find why amr-02 restarts: its problems, k3s events and the pods that failed in…
· Fleet → get_problems
· Triage SRE: Rank the likely causes of amr-02's restarts and propose the next runbook step.
· Triage SRE → inspect_logs
· Fleet done
· Triage SRE done
· Critic: 2 issues (1 blocking)
· captain → read_result
done in 41.2s, 8 calls, 22130 tokens (+96200 cached)
```

`thread <id>` is the thread the command made, or the one `--thread` names. `· council <Name> (<name>) answers` is
said when the turn names its council. The plan line says how the turn is answered: `· council: <names>` names the
members the turn convenes (`· council: Fleet, Triage SRE` here; the line before it names the council), and
`· quick`, with no names, means the lead answers alone. A member's task, cut at 80 characters and ending in `…` when
it is cut, its tool calls (`· Fleet → get_problems`) and its `done` are said by the member's name. The reviewer's
line is `· <name>: <n> issues (<m> blocking)` (`· Critic: 2 issues (1 blocking)` here), and the Coder's steps, in a
council with the Coder or when the Coder is asked alone, show as `· coder: <kind>`, then the step's title when it
has one. The lead's own tool calls show as `captain` (`· captain → read_result`), whatever the lead is called. The
last line, from the turn's `usage` event, says how long it took, how many model calls it made and how many tokens
it spent. Those are the tokens the bot's budget and its `tokens today` count: the prompt, the cache writes and the
answer. Tokens read from the prompt cache cost far less and count against neither; they follow in brackets when
there are any (`22130 tokens (+96200 cached)`). A limit the turn reaches prints `· budget: <what> limit <n>`, and
an error event `· error: <code>: <message>`.

All text from the bot — deltas, the final, titles and messages — is untrusted: ANSI escape sequences and
control characters are dropped before it is printed, except newlines and tabs, so it can never drive your
terminal.

With `--json`, stdout gets every event of the turn as one JSON line — the platform's data, with the frame's
sequence number and type in front, the other keys in alphabetical order — and stderr gets only errors and, after a
Ctrl-C, `cancelling…`:

```json
{"seq":41,"type":"text_delta","at":"2026-11-15T10:42:07.512Z","member":"captain","text":"Two pods ","turnId":"trn_8fa2Xc6Rm2VnQ5Kd9Wb4Pg"}
{"seq":42,"type":"final","at":"2026-11-15T10:42:07.640Z","text":"Two pods are running.","turnId":"trn_8fa2Xc6Rm2VnQ5Kd9Wb4Pg"}
{"seq":43,"type":"turn_end","at":"2026-11-15T10:42:07.640Z","state":"done","turnId":"trn_8fa2Xc6Rm2VnQ5Kd9Wb4Pg"}
```

Events of other turns of the thread are left out. The conversation is not available with `--json`: give a
prompt. With `--follow` it prints the followed turn's events the same way, stored ones included: a turn
that already ended replays them, and its `turn_end` gives the exit code.

## Ctrl-C and reconnects

Ctrl-C during a turn cancels it: the first one asks the platform to cancel and prints `cancelling…` on stderr,
then keeps reading until the turn ends, waiting at most 10 s for it. When the platform has not ended it by then,
the command prints `Error: the turn did not end within 10s of the cancel; it may still finish on the bot`: a
one-shot exits 1, and a conversation shows its prompt again. A second Ctrl-C leaves at once with exit 1
(`interrupted`) — also while the stream waits to reconnect, and in a conversation, where the command ends and
does not return to the prompt. In a conversation the prompt comes back after a turn that ended `cancelled` with
one Ctrl-C; Ctrl-C at the prompt itself ends the conversation with 0, as `/quit` does.

A Ctrl-C during the requests before a turn is asked — while the chat's council changes, or the thread is read
or made — asks nothing. With a prompt the command ends with exit 1 and `interrupted: nothing was asked`; the
`thread <id>` line of a thread named with `--thread`, or made before the Ctrl-C, is still printed (not with
`--json`), so `--thread` continues it, and a council change already made stays. stderr of
`tnctl chat scout --thread thr_8fa2Pt6Jd2Nm5Kd7VnQ9Ww --roster ops "why does amr-02 restart?"`
when the Ctrl-C arrives while the council changes:

```text
· this chat's council is now Ops (ops)
thread thr_8fa2Pt6Jd2Nm5Kd7VnQ9Ww
Error: interrupted: nothing was asked
```

In a conversation it prints `· interrupted: nothing was asked` and the prompt comes back. A conversation opened
with `--thread` ends instead, with exit 1 and `Error: interrupted: nothing was asked`, when the Ctrl-C comes
during its first read of the thread (it then neither follows the thread's open turn, changes its council nor
prompts) or during its `--roster` change, before the first prompt (the change stays).

A refusal of a thread's read, of the stream and of a cancel prints the platform's own sentence, with no
`(HTTP n)`, when the platform names a code tnctl knows; one that names no code, or a code tnctl does not know,
keeps the `(HTTP n)` after the platform's words. A Ctrl-C whose cancel the platform refuses prints
`· cancel failed: <the platform's words>` (nothing with `--json`) and keeps reading; a `/cancel` the platform refuses prints
`Error: <the platform's words>` and the prompt comes back.

The turn's stream is followed until its `turn_end`. When it ends early — a slow reader is cut off, the network
drops, the gateway restarts, or the platform reports `stream_expired` (the stream ran its hour) — the command
connects again from the last event it saw, at once after `stream_expired` and otherwise after 1 s, doubling to
30 s (a `429` with `Retry-After` is waited out for that long, at most 60 s). The platform answers the stream `429`
while you already have 4 chat event streams open, from any client and on any bot (the web chat and [`mcp`](mcp.md)
waits included); each such refusal counts as a failed attempt. A stream that was working — it
carried an event of the thread, or stayed open for 30 s — and then ends is connected again without counting as
a failure. After 6 failed attempts in a row it exits 4 with a hint: the turn goes on on the platform, and
`tnctl chat <bot> --thread <id> --follow` picks it up where it is. `--follow` follows the thread's last
turn and only that: a turn still `queued` or `running` is followed from its first event, and one that
already ended is printed from the thread's record (its answer, or its error with the turn's exit code).
A conversation opened on the thread (`--thread` without `--follow`) follows a turn that is still running
before it shows its prompt. If the person may no longer chat with the bot (`access_lost`), it
stops at once with exit 6 and the platform's reason. A frame whose data is not a JSON object, or whose `turnId` is
not a string, is skipped (`· skipped a malformed event (seq N)` on stderr, nothing with `--json`) and the stream is
read on. An event of the turn with a wrong type in a field tnctl reads (a number for `tool`, say) stops tnctl
following the turn, which it does not cancel: a one-shot or `--follow` exits 1 (`malformed chat event: …`), and a
conversation prints `Error: malformed chat event: …` and shows its prompt again.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok: the turn is `done` |
| 1 | error: the turn `failed` or `expired` (its code and message are printed), it was `cancelled`, or it did not end within 10 s of the cancel (a one-shot or `--follow`); the chat is turned off on this platform (`council_off`); `--follow` on a thread with no turn yet (`thread <id> has no turn yet`); a Ctrl-C before the turn was asked, or a second Ctrl-C; or the platform refused the council or member asked, at a new thread, a `--roster` change or a turn: `council_outdated`, `role_invalid`, `roster_gone`, `role_gone`, `pin_empty`, `creator_only` |
| 2 | usage: the usage errors of [Flags](#flags) (a bad flag value included), the platform's `unknown_member`, `unknown_roster` and `ambiguous` (in the platform's words, for example `There is no council 'nope' for scout.` with a `councils: …` line), and a `--thread` chat the flags do not fit (a chat with one member and another `--member` or `--thorough`, or `--roster` in a conversation; a council chat whose council does not have the `--member`), see [The thread](#the-thread) |
| 3 | not found: the bot or the thread is not there (`not_found`, or a `404` with no code tnctl knows); a bot outside your organisation units counts as not there |
| 4 | timeout: the chat stream could not be followed: 6 failed attempts in a row, the stream cap's `429` included (the turn goes on); or another request got no answer within 30 s (`the request timed out: …`) or the gateway answered it with `408` or `504` |
| 5 | rejected: the platform refused the request as invalid (`invalid`); with `--thread` and `--roster`, the chat's pinned members are not all in the new council: `this chat's pinned members are not all in council '<council>': change or clear the pin in the web chat, then run the command again`; with a prompt, `--roster` on a chat with one member: `roster: A chat with one member has no council to change.` |
| 6 | not logged in or not allowed; you may not chat with this bot (before anything is asked, `you cannot chat with <bot>: <reason>`, or without a device role the platform's reason alone; a later refusal, and that of `tnctl chat threads`, is the platform's `chat_not_allowed`, printed as its reason alone), a `--roster` change of a chat someone else started, unless you are the bot's creator (`not_author`: `Only the author of this chat or the bot's creator can change its council.`), your access ended mid-turn (`access_lost`), or `tnctl chat <bot>` and `tnctl chat threads <bot>` inside a TeknoirBot (`--help` prints, exit 0; a missing bot, an extra argument to `chat threads`, an unknown flag or a flag value of the wrong type is exit 2 first) |
| 8 | conflict: the thread already has a turn open (`turn_open`), also when `--roster` would change its council: follow it with `--follow`, then Ctrl-C cancels it; the chat's council changed meanwhile (`conflict`: `This chat's council just changed. Send again.`); the bot is suspended (`bot_suspended`: `Resume the bot to chat.`): resume the bot, then run it again; the bot is not running or is being removed (`bot_not_ready`: `The bot is starting.`, `The bot is being removed.`, or `The bot is <phase>.` with its phase in lower case for any other phase that is not running): wait, then run it again; or another `409` with no code tnctl knows |
| 9 | rate limited (`429`): the platform refuses a turn while you already have 3 turns queued or running on that bot (from any client: tnctl, an MCP client, the web chat), or while the bot already has 6 queued: `Too many open turns on this bot. Wait for one to finish.` The bot runs at most two turns at a time; a third waits queued. |

The council refusals of exit 1 (`council_outdated`, `role_invalid`, `roster_gone`, `role_gone`, `pin_empty`,
`creator_only`) can come at once, or after the turn was queued: the turn then ends `failed` and the command prints
`the turn failed: <code>: <message>` (exit 1; in a conversation the error is printed and the prompt comes back). A
turn can also end that way with `access_changed`: `The author of this chat may no longer use this bot.`
`role_invalid`'s message has one `<slug>: <reason>` for each reason the refusal names, joined with a semicolon and a
space.

A refusal whose code tnctl knows prints the platform's own sentence, with no `(HTTP n)`, whatever it refuses: the
turn, a new thread, the thread's reads and council change, the stream or a cancel. That includes `bot_not_ready` and
`bot_suspended` (exit 8) and `too_many_turns` (exit 9), so `Resume the bot to chat.` is printed as it is. Three print
tnctl's own sentence instead: `turn_open`, on a turn or a `--roster` change (`this chat already has a turn in
progress (tnctl chat <bot> --thread <id> --follow, then Ctrl-C cancels it)`); `council_off` on the chat info the
command reads first, and in `tnctl chat threads` (`the TeknoirBot chat is turned off on this platform`); and a pin
that does not fit the new council (exit 5). A refusal that names no code, or a code tnctl does not know, keeps the
`(HTTP n)` after the platform's words.

## Examples

```bash
# one question
tnctl chat scout "what is running on my robot?"

# and then the pods, in the same thread
tnctl chat scout --thread thr_123 "and the pods?"

# pick the thread's last turn up again after the stream was lost
tnctl chat scout --thread thr_123 --follow

# the Review council: the lead convenes up to 3 of its members, and the reviewer checks every report
tnctl chat scout --roster review --thorough "is the update safe to roll out?"

# one member alone, by its slug
tnctl chat scout --member triage-sre "why is arm-2 offline?"

# one member alone, with its scope
tnctl chat scout --member teamspace/triage "why is arm-3 slow?"

# the bot's chat threads, whoever started them, the most recently changed first
tnctl chat threads scout

# scripts: the events, one JSON line each
tnctl chat scout --json "list my devices" | jq -r 'select(.type=="final").text'

# a conversation
tnctl chat scout
```

## See also

- [`council role`](council-roles.md) — the members a bot's councils can use.
- [`council roster`](council-roster.md) — a bot's councils, and the default one a new chat asks.
- [`council`](council.md) — the process on the bot's computer that answers the turns, and its turn modes.
- [`mcp`](mcp.md) — the council tools a person's own agent uses on the same bots.
- [Bot mode](../bot-mode.md) — a bot does not use the person chat.
- [`inspect`](inspect.md), [`robots`](robots.md) — what the council reads about the bot's fleet.
- [`mqtt`](mqtt.md) — the devices' MQTT messages, read as a stream of server-sent events like a chat turn's.
