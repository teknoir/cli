# `tnctl chat`

Chat with a TeknoirBot's council — the Captain, Fleet, Coder and Critic — and read the answer as it is written.

```bash
tnctl chat <bot> [prompt...] [--thread <id>] [--new] [--mode auto|quick|council]
                             [--member fleet|coder] [--effort low|medium|high] [--json]
tnctl chat <bot> --thread <id> --follow
tnctl chat threads <bot> [--json]
```

A TeknoirBot is an AI bot with its own computer on the Teknoir platform. Its **council** answers you: the
Captain runs it and writes the answer, Fleet looks at the bot's fleet and devices, the Coder reads and changes
its computer, and the Critic checks the answer before it is done. `tnctl chat` is a person's view of that
conversation; a bot's own `tnctl` refuses it (see [Bot mode](../bot-mode.md)).

## Who may chat

Who may chat with a bot is who may open its desktop:

| The bot's access | Who may chat |
| --- | --- |
| Act as me (`creator`) | Its creator. |
| Read only (`read`), Off (`none`) | Its creator, and the teamspace's admins. |

The caller also needs permission to open a terminal (`pods/exec`) in the teamspace. Anyone else gets the
platform's `403` with the reason: `you cannot chat with <bot>: <reason>`, exit 6. A bot that is suspended, or
one that has not started yet, has no council online, and the platform's reason says so.

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
| `@<slug> <text>` | one turn of that member of the chat's council alone. `--effort` applies to it; `--mode` and `--thorough` do not |
| `/new` | the next line starts a new thread |
| `/cancel` | cancels the thread's open turn, if it has one |
| `/quit` | ends the conversation (exit 0). Ctrl-C at the prompt does the same |
| any other `/word` | the help line: the commands, and the chat's `@` members |

The chat's council, whose members `@` asks, is the thread's council once the thread exists, read again before each
line. Before the thread exists, at the start and after `/new`, it is the `--roster` council, or the bot's default
council without `--roster`. With `--thread <id> --roster <council>` it is that council. In a chat with one member
there is no `@`: an `@word` line is sent as it is. `--thread` on a council chat with `--member <slug>` stays a
council chat until `/new`: a plain line asks that member alone, and `@<slug>` asks that member of the thread's
council instead; after `/new` the next thread is a chat with that member alone. On an older platform `@` asks only
`@fleet` and `@coder`. Any other `/word` prints the help line, which lists the commands and the members the chat's
`@` asks; in `tnctl chat scout`, which asks the bot's default council, it is
`commands: /new /cancel /quit, @fleet, @stream-watcher, @cost-watcher`.

An `@<slug>` line runs a member turn, not a council turn: the member's own progress lines on stderr and its
report as the answer, with no lead, no plan and no reviewer. stderr of a conversation where
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

A bot's default council is never deleted — the platform refuses that (`roster_default`) — so a default is
gone only when it names a premade council the platform no longer has, as a platform upgrade that dropped one
would leave it.

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
stderr of `tnctl chat scout --thread thr_8fa2Wb7Kt4Qm9Wc2Rq4NmA --member triage-sre`:

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

stdout of `tnctl chat threads scout`, which lists your threads, newest first (UPDATED is in your local time zone;
this sample ran in UTC):

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
| `--thread <id>` | Continue this thread (it must exist). |
| `--new` | Start a new thread. |
| `--follow` | With `--thread`: follow that thread's last turn instead of asking anything — its events while it runs, its answer when it already ended. It takes no prompt. |
| `--mode auto\|quick\|council` | How the council answers: `auto` decides, `quick` is one fast pass, `council` runs the whole council. |
| `--member fleet\|coder` | Ask one member alone. It cannot be combined with `--mode`. |
| `--effort low\|medium\|high` | How hard the members think. |
| `--json` | Print each event of the turn as one JSON line. Not available in the conversation. |

`--member` with `--mode` and a prompt over 16,000 characters are usage errors (exit 2), refused before
anything is sent, as are `--follow` without `--thread` and `--follow` with a prompt.

## Output

stdout gets the answer. On a terminal the Captain's text is printed as it is written; when the turn's `final`
arrives it is not printed again (only a newline when it is the same text), and when it is printed after text a
`plan`, `member_done` or `critique` event reset, it starts a paragraph of its own (a blank line first). When
stdout is not a terminal only the final text is printed. A member turn prints its `final`. A `final` the
platform shortened for the stream (`cut: true`) is never printed as the answer: at the turn's end the
command reads the thread's own record and prints the answer whole; if that read fails, it prints the
shortened text and a line on stderr that says how to read the answer whole.

stderr gets one short line per event of the council, prefixed with `· `, so you can follow what it is doing:

```text
· council: fleet, coder
· fleet: check the robot's pods
· fleet → robots.list
· fleet done
· critic: 2 issues (1 blocking)
· budget: tokens limit 5000000
done in 12.4s, 6 calls, 18422 tokens (+96200 cached)
```

The last line, from the turn's `usage` event, says how long it took, how many model calls it made and how many
tokens it spent. Those are the tokens the bot's budget and its `tokens today` count: the prompt, the cache writes
and the answer. Tokens read from the prompt cache cost far less and count against neither; they follow in brackets
when there are any.

All text from the bot — deltas, the final, titles and messages — is untrusted: ANSI escape sequences and
control characters are dropped before it is printed, except newlines and tabs, so it can never drive your
terminal.

With `--json`, stdout gets every event of the turn as one JSON line — the platform's data, with the frame's
sequence number and type in front, the other keys in alphabetical order — and stderr gets nothing but errors:

```json
{"seq":41,"type":"text_delta","member":"captain","text":"Two pods ","turnId":"trn_9a1"}
{"seq":42,"type":"final","text":"Two pods are running.","turnId":"trn_9a1"}
{"seq":43,"type":"turn_end","state":"done","turnId":"trn_9a1"}
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
`(HTTP n)`, when the platform names a code tnctl knows; other refusals keep the `(HTTP n)` after the platform's
words. A Ctrl-C whose cancel the platform refuses prints `· cancel failed: <the platform's words>` and
keeps reading; a `/cancel` the platform refuses prints `Error: <the platform's words>` and the prompt comes
back.

The turn's stream is followed until its `turn_end`. When it ends early — a slow reader is cut off, the network
drops, the gateway restarts, or the platform reports `stream_expired` (the stream ran its hour) — the command
connects again from the last event it saw, at once after `stream_expired` and otherwise after 1 s, doubling to
30 s (a `429` with `Retry-After` is waited out for that long, at most 60 s). A stream that was working — it
carried an event of the thread, or stayed open for 30 s — and then ends is connected again without counting as
a failure. After 6 failed attempts in a row it exits 4 with a hint: the turn goes on on the platform, and
`tnctl chat <bot> --thread <id> --follow` picks it up where it is. `--follow` follows the thread's last
turn and only that: a turn still `queued` or `running` is followed from its first event, and one that
already ended is printed from the thread's record (its answer, or its error with the turn's exit code).
A conversation opened on the thread (`--thread` without `--follow`) follows a turn that is still running
before it shows its prompt. If the person may no longer chat with the bot (`access_lost`), it
stops at once with exit 6 and the platform's reason. A frame the command cannot read is skipped
(`· skipped a malformed event (seq N)` on stderr) and the stream is read on.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok: the turn is `done` |
| 1 | error: the turn `failed` or `expired` (its code and message are printed), it was `cancelled`, the chat is turned off on this platform (`council_off`), or a second Ctrl-C left |
| 2 | usage: a bad flag value, `--member` with `--mode`, `--new` with `--thread`, `--follow` without `--thread` or with a prompt, a prompt over 16,000 characters, `--json` without a prompt |
| 3 | not found (`404`): the bot or the thread is not there |
| 4 | the chat stream could not be followed: 6 failed attempts in a row (the turn goes on) |
| 6 | not logged in or not allowed; you may not chat with this bot (`chat_not_allowed`, with the reason), or your access ended mid-turn (`access_lost`) |
| 8 | conflict (`409`): the thread already has a turn open (`turn_open`): follow it with `--follow`, then Ctrl-C cancels it; the bot is suspended (`bot_suspended`): resume the bot; the bot is starting or being removed (`bot_not_ready`): wait, then run it again |
| 9 | rate limited (`429`): too many turns or too many streams; wait the `Retry-After` |

## Examples

```bash
# one question, then continue the thread later
tnctl chat scout "what is running on my robot?"
tnctl chat scout --thread thr_8fa2 "which pod restarts?"

# pick the thread's last turn up again after the stream was lost
tnctl chat scout --thread thr_8fa2 --follow

# one member alone, with a high effort
tnctl chat scout --member coder --effort high "why does the notebook fail to start?"

# scripts: the events, one JSON line each
tnctl chat scout --json "list my devices" | jq -r 'select(.type=="final").text'

# a conversation
tnctl chat scout
```

## See also

- [Bot mode](../bot-mode.md) — a bot does not use the person chat.
- [`inspect`](inspect.md), [`robots`](robots.md) — what the council reads about the bot's fleet.
- [`mqtt`](mqtt.md) — the stream interface `chat` follows a turn with.
