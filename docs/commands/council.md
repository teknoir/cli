# `tnctl council`

Run the council of a TeknoirBot: the Captain, Fleet, the Coder and the Critic. This is the process of the
bot's council image, not a person's command. It takes the chat turns the platform queues for the bot and
answers them, with the bot's own key, on the bot's own computer.

```bash
tnctl council serve [--max-turns 1|2] [--work-dir /work]
```

The command is hidden: it is not in `tnctl --help`. It runs only in bot mode; anywhere else it is a usage
error (exit 2): `tnctl council serve runs only inside a TeknoirBot`.

## What it does

The council image starts it as `tini -- tnctl council serve --work-dir /work`. From then on it:

1. **Polls the bot API for work.** It asks `GET /council/work?wait=25&free=<n>`: the platform holds the
   request up to 25 s and answers with one turn or with no work, and `free` says how many of the
   `--max-turns` slots are free. A poll with no free slot is kept open all the same, so the bot shows as
   online; the end of a turn cancels it and the free slot is offered at once. A failed poll is logged with
   the status and code it got, and waited out with a growing backoff — 60 s when the platform answers
   `council_off` (the chat is turned off) — and a platform that answers empty at once is slept off at 1 s.
2. **Runs the turn.** Which phases run depends on the turn's `kind` and `mode`; see [Turn modes](#turn-modes).
   Every model call goes through the bot API's model proxy, with the bot's key.
3. **Posts its progress and its outcome.** Events as they happen — `turn_start`, `plan`, `member_start`,
   `text_delta`, `tool_call`, `tool_result`, `coder_update`, `member_done`, `critique`, `budget`, `usage` —
   then the finish: the state, the answer and what the turn cost.

A turn ends `done` with its answer, or `failed` with a code: `bad_turn` (a kind or a member the bot does not
know), `turn_closed`, `events_lost`, `events_rejected` (the platform dropped the turn or refused its
progress), `bot_restarting`, `platform_unavailable`, `no_access` (a Fleet turn at access level `none`),
`model_error`, `model_refused` (the model declined).

## Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--max-turns` | `2` | How many turns run at once, `1` or `2`. Anything else is a usage error. |
| `--work-dir` | `/work` | The directory the Coder works in: one work directory per chat thread below it, and its home. |

## What it reads

Bot mode comes from the environment, the same variables the rest of a bot's `tnctl` uses:

| Variable | Meaning |
| --- | --- |
| `TEKNOIRBOT_API` | The bot API's base; the work queue and the model proxy are below it. https, or http on a loopback address. |
| `TNCTL_BOT_KEY_FILE` | The file holding the bot's key, read on every request. On a bot's computer `/run/teknoirbot/key`. |
| `TEKNOIRBOT_NAMESPACE` | The bot's teamspace. Required. |
| `TEKNOIR_DOMAIN` | The platform's domain. Informational. |

With either of the first two unset or empty the command is not in bot mode, and it refuses to serve; without
`TEKNOIRBOT_NAMESPACE` it is a usage error too. A base that may not carry the key ends the start with
`the bot API base must be https (or http on a loopback address)`. A request the platform refuses later does
not end the process: the poll is logged with the status and code it got, and asked again.

## The members

Only two models are used, both through the platform's proxy: `claude-sonnet-5-5` and `claude-haiku-5-5`.
Effort is `low`, `medium` or `high`; the proxy allows nothing above `high`, and neither does the council. A
turn's `effort` sets how hard the members think; when it gives none each member is at its own default.

| Member | Model | Effort | What it does |
| --- | --- | --- | --- |
| Captain | `claude-sonnet-5-5` | `high` | Plans the turn, hands the parts to the others, and writes the answer. |
| Fleet | `claude-haiku-5-5` | `medium` | Reads the teamspace with the read tools: robots, devices, configuration, problems, metrics, MQTT samples, pods and logs. |
| Coder | `claude-sonnet-5-5`, through OpenCode | `high` | Works on the bot's computer: code, builds, its k3s cluster. |
| Critic | `claude-sonnet-5-5` | `medium` | Reads the members' reports and their tool digests and lists the problems: claims without evidence, wrong numbers, risky changes, code claimed to work but not run. |

What the platform's tools give a member follows the bot's access level, and so does what it may change:

| Bot access | Fleet | Captain | Coder |
| --- | --- | --- | --- |
| `none` (Off) | `whoami` only; a Fleet turn is refused with `no_access` | `whoami` | its own computer; no platform tool |
| `read` (Read only) | every read tool but `inspect_*` | the same | its own computer, and `tnctl` as the bot at `read` |
| `creator` (Act as me) | every tool, `inspect_*` included | the same | its own computer, and `tnctl` as the bot at `creator` |

The Critic gets no tool at all. The Captain and Fleet also get `read_skill`, which reads one of the kit's
guides (`teknoir-platform`, `teknoir-robots`, `teknoir-config`, `teknoir-debug`, `teknoir-mqtt-streams`), and
the Captain gets `convene_council`: 1 to 3 tasks for `fleet` and `coder`, at most one of them a Coder task, a
task of at most 4000 characters and a reason of at most 1000. It is not offered in a quick turn, and at
access level `none` a `fleet` task is refused.

## Turn modes

A turn arrives with a `kind` — `ask` (a person asks the council, or one member) or `member` (the Captain
delegating to one member) — and, for an ask, a `mode`:

| Mode | What runs |
| --- | --- |
| `quick` | The Captain answers alone: six tool rounds at most, then one answer call with the tools off. `convene_council` is not offered. |
| `council` | The Captain always convenes. When it answers without a convene call, the daemon builds the plan itself: Fleet with the person's question, and the Coder too when the question asks for code. |
| `auto` (the default, and anything empty or unknown) | The Captain decides. The turn starts on the quick budget and is upgraded to the council budget when it convenes. |
| a member turn (`member` `fleet` or `coder`) | That member alone, with the thread's earlier question and answer pairs in its prompt for Fleet. No Captain, no Critic. |

A council turn runs the plan's tasks at once — Fleet's and the Coder's work in parallel — and then the
Critic reads their reports and tool digests (it is skipped when there are no reports). The Captain writes the
answer last, with the reports and the critique as data, in three tool rounds at most.

## Budgets and deadlines

| Turn | Model calls | Tokens |
| --- | --- | --- |
| quick | 8 | 150,000 |
| council | 30 | 500,000 |
| member | 12 | 200,000 |

Tokens are counted as input plus cache write plus output; a cache read is free.

- **The answer is held back.** A work call starts only while the turn has used fewer than its calls minus one
  and fewer than its tokens minus 20,000. The answer call needs one call left and is not stopped by tokens.
- **Fleet's own loop** gets 10 tool rounds and 120,000 tokens of its own, then it must write its report.
- **The Coder's steps** spend the same token budget. When the reserve is reached OpenCode is stopped and the
  Coder reports `budget`.
- **Four calls at once per bot**, all turns together; a call that finds no slot waits for one.
- **A limit that is hit** is posted once, as a `budget` event: `calls`, `tokens` or `time`.
- **The answer can fail and the turn still answer.** When the Captain cannot write it (no time, no budget, a
  model error, a refusal), the turn ends `done` with the members' reports, under
  `The Captain could not write the answer (<why>). The members' reports:`.

The deadlines are all relative to the turn's deadline `D`, which the platform sets, and 10 minutes from the
claim when it sets none:

| At | What ends |
| --- | --- |
| `D − 75 s` | the work phases: routing with tools, the members, the Critic |
| `D + 20 s` | the answer calls: the Captain's forced answer, and its synthesis |
| `D + 50 s` | the turn's finish is posted by |

## The Coder

The Coder member is OpenCode (2.2.7), headless, as a child process:

```text
opencode run --standalone --format json --model teknoir/claude-sonnet-5-5#<effort> [--session <ses_…>] "Task from the Teknoir council (turn <trn_…>): …"
```

| | |
| --- | --- |
| The binary | `opencode`, looked up on the PATH of the child's environment at run time. |
| Its config | `/etc/teknoirbot/opencode.json`, passed as `OPENCODE_CONFIG`. |
| The work directory | `<work-dir>/chat-<the thread id's first 8 characters>`, its own git repository (`git init` on first use). A task's changed files are that repository's `git status --porcelain`. |
| The skills | `/etc/teknoirbot/kit/skills` is linked into the work directory as `.agents/skills`, and kept out of the changed files and out of a commit of everything. |
| The home | `<work-dir>/home`, with its XDG directories below it. Only `PATH`, `LANG`, `LC_ALL`, `TZ`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, the Kubernetes service variables, the bot-mode variables and `BUILDKIT_HOST` are passed through from the bot's own environment, plus `TEKNOIRBOT_MEMBER=coder` and the turn's id. |
| The session | One OpenCode session per chat thread, remembered in `<work-dir>/home/.local/state/teknoirbot/sessions.json` (the 1000 most recent threads). A follow-up in the thread continues it. A thread keeps the effort it started with: OpenCode refuses a session resumed at another effort. |
| One at a time | The bot runs one OpenCode process at a time; a second Coder task waits for it, and its turn shows a `wait` update while it does. |

Its limits and what a task that hits one reports:

| Limit | The report's `reason` |
| --- | --- |
| 10 minutes of wall time | `timeout` |
| The turn's token budget, at the reserve | `budget` |
| The turn ended or the bot stopped: SIGTERM to the process group, SIGKILL after 10 s | `cancelled` |
| OpenCode's own error event | `error` |
| A non-zero exit | `exit` |
| A signal the council did not send | `crash` |
| A prompt over 32 KiB, a bad turn or thread id | `bad_task` (nothing is started) |

The processes a task left running are killed when it ends, so a dev server or a watcher does not outlive it.
The report carries the last text of the answer, the changed files (at most 200 lines), the session, the steps,
the tools and the usage; its text is at most 16 KiB and its error at most 2 KiB.

## Shutdown

On SIGTERM (or SIGINT or SIGHUP, which `tini` passes on) the poll ends and no turn is claimed: every turn
running is cancelled, finishes as `bot_restarting` (`The bot restarted during this turn.`) and posts that
before the 5-second stop grace ends. The process then exits 0.

## Logs

It writes to stderr only, one line an event, prefixed `council: `:

```text
council: turn=trn_… kind=ask mode=auto
council: turn=trn_… critique: 2 evidence entries dropped
council: coder: turn=trn_… thread=thr_…: started with effort high
council: turn=trn_… state=done code= calls=6 tokens=18422 ms=12345
```

A line carries ids, states, codes, counts and durations, never a prompt, an answer or a tool result: the
prompts and answers go to the bot API. A turn that fails says so in its own line with its code.

## Exit codes

| Exit | Meaning |
| --- | --- |
| 0 | ok: the bot was stopped and every turn was finished |
| 1 | error: it could not start (a bot API base that may not carry the key, an MCP server that could not be built) |
| 2 | usage: `--max-turns` is not 1 or 2, or it runs outside a TeknoirBot |

The pod restarts a process that exits.

## See also

- [`chat`](chat.md) - the same turns, as a person in a terminal.
- [`mcp`](mcp.md) - the council tools a person's own agent uses to lead a bot's members.
- [Bot mode](../bot-mode.md) - what a bot's `tnctl` may and may not run.
