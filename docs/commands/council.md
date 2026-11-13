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
   the status and code it got and the class of the failure, and waited out with a growing backoff; a
   platform that answers empty at once is slept off at 1 s. The class is one of `key` (the bot's key file
   is missing or empty, which only a person can fix, by re-creating the bot), `dns`, `tls`, `timeout`,
   `refused`, `transport` and `http`. A missing key waits 60 s, and says where the key file is. Any `404`
   waits 60 s too, not only `council_off`: a 404 is the platform having no council API there, which is the
   council being off, and it is logged at the first 404 of a run and every tenth one after that, rather
   than every 60 s.
2. **Runs the turn.** Which phases run depends on the turn's `kind` and `mode`; see [Turn modes](#turn-modes).
   Every model call goes through the bot API's model proxy, with the bot's key.
3. **Posts its progress and its outcome.** Events as they happen — `turn_start`, `plan`, `member_start`,
   `text_delta`, `tool_call`, `tool_result`, `coder_update`, `member_done`, `critique`, `budget`, `usage` —
   then the finish: the state, the answer and what the turn cost.

A batch of events, or the finish, that fails with a network error, a `429` or a `5xx` is resent with the same
batch number and the same finish body, which the platform stores once: the waits grow from 1 s to 5 s, or as
long as the server's `Retry-After` asks, but at most 30 s, and the resends continue for up to 3 minutes of
failures in a row — but no resend goes out at or after `D + 50 s`, the deadline the finish is posted by. A
platform restart, 30–90 s with no answer, does not end a running turn; the events emitted meanwhile wait in a
buffer of up to 4 MiB, and the ones past it are dropped; the work goes on. Past that window a turn that is
still working ends `events_lost`, as before. A turn that is finishing drops the batch it lost and still sends
the finish with the turn's own outcome, whose resends get a failure window of their own, still bounded by
`D + 50 s`. A `409`
(`turn_closed`, `claim_mismatch`), a `404` and any other refusal are not resent.

A turn ends `done` with its answer, or `failed` with a code: `bad_turn` (a kind or a member the bot does not
know), `turn_closed`, `events_lost`, `events_rejected` (the platform dropped the turn or refused its
progress), `bot_restarting`, `platform_unavailable`, `no_access` (a Fleet turn at access level `none`),
`model_error` (the model could not be reached), `model_refused` (the model declined),
`model_unavailable` (the platform has no model for the bot), `model_budget` (the bot used its tokens for
today), `model_busy` (too many model calls at once), `model_rejected` (the platform refused the request).

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
seven guides (`teknoir-platform`, `teknoir-robots`, `teknoir-config`, `teknoir-debug`, `teknoir-mqtt-streams`,
`teknoir-k3s-apps`, `teknoir-council`) a page at a time, and `read_result`, which pages the rest of a cut tool
result; the Captain gets `convene_council`: 1 to 3 tasks for `fleet` and `coder`, at most one of them a Coder task, a
task of at most 4000 characters and a reason of at most 1000. It is not offered in a quick turn, and at
access level `none` a `fleet` task is refused.

## Turn modes

A turn arrives with a `kind` — `ask` (a person asks the council, or one member) or `member` (a person's
turn in a conversation with one member: a thread made with `tnctl chat --member` or `council_ask`) — and,
for an ask, a `mode`:

| Mode | What runs |
| --- | --- |
| `quick` | The Captain answers alone: six tool rounds at most, then one answer call with the tools off. `convene_council` is not offered. |
| `council` | The Captain always convenes. When it answers without a convene call, the daemon builds the plan itself: Fleet with the person's question, and the Coder too when the question asks for code. |
| `auto` (the default, and anything empty or unknown) | The Captain decides. The turn starts on the quick budget and is upgraded to the council budget when it convenes. |
| a member turn (`member` `fleet` or `coder`) | That member alone, with the thread's earlier question and answer pairs in its prompt for Fleet. No Captain, no Critic. |

The Coder is convened only for what the person asked. Before the Captain has read anything, its own
`convene_council` decides; once it has read platform data — a log, a configuration, an MQTT sample, any tool
result, which devices and apps wrote; in this turn or in the thread's earlier answers — a Coder task is
accepted only when the person's own prompt asks for code or a change, and the council-mode plan the daemon
builds itself convenes it only for a prompt that asks for a change. Otherwise the convene is refused with a
tool result that says so, and the Captain answers from what it found or convenes Fleet alone. The Coder's
prompt puts the person's request first, whole when it fits; after the Captain has read platform data the prompt holds none
of the Captain's notes — the data asked, not the person — only the sentence that it reads what it needs
itself, and otherwise the notes follow the request as data.

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

## When a tool result is too long

A tool result is cut to 16 KiB before a member sees it. The MCP server the council runs in-process does
not cut in front of that — its own cap is 1 MiB, so that it only guards against a runaway answer — since a
48 KiB cut of its own would reach the council as a wrapper it then cuts again, with counts that disagree.
Neither cut is silent: the member gets a result that says what it is missing.

- **The header.** A result a member sees only part of starts with a `RESULT PARTIAL` line:
  `RESULT PARTIAL: shown 14798 of 48123 bytes, 12 of 40 items (ref r1). Do not infer missing items, counts
  or absences; read_result with ref r1 and offset 14796 gives the rest, or narrow the query.` The bytes and
  the items are counted (a cut JSON text that is not an array reads `shown S of T bytes of JSON text`,
  with no line counts, since the lines of indented JSON are its wrapper's), `of "<field>"` names the object's array that was cut, and the offset is where the
  unseen rest starts: in an indented array, right after the last element shown, before the comma that
  precedes the next one (the closing bracket the cut put back is not part of the original text). A tool
  that cut its own result (a `{"truncated": true}` wrapper, an `inspect_*` answer that says so, an MQTT
  sample) gets a first line saying so, with its own hint (a sentence ending in a period) and, when the
  tool named one, the total. An `inspect_logs` answer counts as cut upstream only when `truncated.bytes`
  is true (the log passed the platform's 1 MiB limit): `truncated.lines` alone is the last `tail` lines the
  call asked for, and gets no header. A failed call never does, whatever its text says. When the
  council cuts that result as well, the cut line follows it and the first line says whose numbers the
  wrapper's are.
- **`read_result`** reads the rest: `{"ref": "r1", "offset": 14796}` gives up to 12288 bytes of the whole
  result from that offset, after a line `PAGE: bytes 14796..27084 of 48123 (ref r1); nextOffset 27084`. A
  page joins with what was shown into the tool's own text, byte for byte. The offset must be on a rune
  boundary; an unknown reference and an offset outside the result are error results.
- **The untrusted tags.** Every MCP tool result, and every page, is inside
  `<untrusted source="tool:<name>" sha256="…">…</untrusted>`: the source, and the SHA-256 of the whole
  result, so a page is recognizable as part of the result it came from. In the result itself, a `<` that
  opens `untrusted` — in any case, and with any run of white space, C0 controls, DEL, NEL, Unicode
  separators or format characters (zero-width spaces, word joiners), `/` or `\` between the `<` and the
  name, format characters between the name's letters, or the fullwidth `＜` and small `﹤` in place of `<`,
  so that `</untrusted>`, `</ untrusted>`, `<\/untrusted>`, `<\0/untrusted>`, `<` + U+200B + `/untrusted>` and
  `</un` + U+200B + `trusted>` all count — is escaped to `&lt;` (the opener itself), and nothing else is: a device cannot close the block or open one of
  its own, and its text stays readable. The prompts tell the members that what is inside the tags comes
  from the platform and its devices. A refusal made by the council itself, and a guide from `read_skill`,
  are not wrapped.
- **The store.** A result is kept whole only when it was cut: at most 16 MiB per turn, in memory, gone
  with the turn. Past the cap the member gets no reference, and its header says `the rest is not kept`.
- **The Critic's rule.** The Critic's input counts the evidence entries that rest on a cut result
  (`truncatedCalls`, and `"truncated": true` on each), and its instructions say that a count, a "none" or
  an absence resting on one is a problem, blocking when the answer depends on it, unless the member paged
  it with `read_result`.
- **`read_skill` pages too.** A guide is read with `{"name": …, "offset": 0}`, at most 16 KiB a page, and a
  page that is not the last ends with `[more: read_skill with offset <n>]`.

## The Coder

The Coder member is OpenCode (2.2.7), headless, as a child process:

```text
opencode run --standalone --format json --model teknoir/claude-sonnet-5-5#<effort> [--session <ses_…>] "Task from the Teknoir council (turn <trn_…>): …"
```

| | |
| --- | --- |
| The binary | `opencode`, looked up on the PATH of the child's environment at run time. |
| Its config | `/etc/teknoirbot/opencode.json`, the only config it reads: passed as `OPENCODE_CONFIG`, with `OPENCODE_DISABLE_PROJECT_CONFIG=1` so no `opencode.json` in the work directory can replace it. A task that wrote one could send the bot's key to another URL. |
| The skills | The kit's, `/etc/teknoirbot/kit/skills`, read through the config's `skills.paths`; nothing is linked into the work directory. |
| Its config home | `XDG_CONFIG_HOME=/etc/teknoirbot/xdg`, read-only, with an empty `opencode` directory. The coder cannot write a global config there, and a task does not start when the image has no such directory. |
| The work directory | `<work-dir>/chat-<the thread id's first 8 characters>`, its own git repository (`git init` on first use). A task's changed files are that repository's `git status --porcelain`. |
| The home | `<work-dir>/home`, with its XDG data, state and cache directories below it. Only `PATH`, `LANG`, `LC_ALL`, `TZ`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, the Kubernetes service variables, the bot-mode variables and `BUILDKIT_HOST` are passed through from the bot's own environment, plus `TEKNOIRBOT_MEMBER=coder` and the turn's id. |
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
| No readable config file at `/etc/teknoirbot/opencode.json`, or no `opencode` directory under the config home: a wrong image | `start` (nothing is started, and the error names the path) |
| Less than 2 GiB free under the work root before it starts, or less than 512 MiB while it runs | `disk` (nothing is started; a run that hits it is stopped as a cancel stops it) |

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
