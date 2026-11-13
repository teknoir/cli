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
tnctl chat scout --thread thr_8fa2 "and the pods?"
tnctl chat scout --mode quick "list my devices"
tnctl chat scout --member coder "why does the notebook fail to start?"
```

With a prompt, one turn runs and the command exits when the turn ends. With no prompt — and stdin and stdout
both terminals — the command starts a conversation:

```text
scout (creator, online) — council tnctl/v0.3.0
Captain  claude-sonnet-5-5  high
Fleet    claude-haiku-5-5   medium
Coder    claude-sonnet-5-5  high
Critic   claude-sonnet-5-5  medium
tokens today: 123456 of 5000000
> 
```

| At the `> ` prompt | Does |
| --- | --- |
| a line of text | one turn of the whole council, with the `--mode`, `--member` and `--effort` you gave the command |
| `@fleet <text>`, `@coder <text>` | one turn of that member alone (`--effort` still applies; `--mode` does not) |
| `/new` | the next line starts a new thread |
| `/cancel` | cancels the thread's open turn, if it has one |
| `/quit` | ends the conversation (exit 0). Ctrl-C at the prompt does the same |

An empty line does nothing; any other `/word` lists the commands. Each line is one turn, run in the foreground:
the prompt comes back when the turn ends.

When stdin is not a terminal and there is no prompt, all of stdin is the prompt: `echo "what runs?" | tnctl
chat scout` is one turn. `--follow` reads no stdin at all: `ssh host 'tnctl chat scout --thread thr_8fa2
--follow'` does not wait for one, and inside a `while read` loop it leaves the loop's input alone.

## The thread

Every chat is a **thread**. The command prints its id on stderr as soon as it exists (`thread thr_8fa2`), so a
later run continues it with `--thread thr_8fa2`; without `--thread` a new thread starts, with the first line of
the prompt (at most 60 characters) as its title and `origin: "cli"` as where it came from. `--new` with
`--thread` is a usage error: one or the other. In a conversation the thread is made at the first prompt, so a
`/quit` before it creates nothing.

`tnctl chat threads scout` lists your threads, newest first:

```text
ID        TITLE                                             TURNS  LAST  UPDATED
thr_8fa2  what is running on my robot?                         3  done  2026-11-11 10:32
```

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
done in 12.4s, 6 calls, 18422 tokens
```

The last line, from the turn's `usage` event, says how long it took, how many model calls it made and how many
tokens it spent.

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
then keeps reading until the turn ends, waiting at most 10 s for it. A second Ctrl-C leaves at once with exit 1
(`interrupted`) — also in a conversation (there the command ends, it does not return to the prompt) and also
while the stream is waiting to reconnect. In a conversation the prompt comes back after a turn that ended
`cancelled` with one Ctrl-C; Ctrl-C at the prompt itself ends the conversation with 0, as `/quit` does.

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
